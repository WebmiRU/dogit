package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulehost"
)

// routesRefresh is how often --watch re-reads the module list.
//
// A proxy reloading its configuration is disruptive enough that doing it on every
// heartbeat would be absurd; a module being installed or removed is an
// administrator's deliberate act, and a few seconds of delay is invisible.
const routesRefresh = 5 * time.Second

// moduleRoutes prints, writes or follows where modules are published.
//
// This is the one place the answer about addresses is produced outside a request,
// and it is deliberately the same function the API uses: an operator reading the
// output and a browser showing it cannot tell which came first.
//
// In Docker it runs as a sidecar writing into the web service's configuration
// volume; in Kubernetes it fills a ConfigMap. Neither the command nor the text it
// writes knows which of those it is in.
func moduleRoutes(ctx context.Context, args []string) error {
	fs := newFlagSet("module routes")
	nginx := fs.Bool("nginx", false, "write proxy configuration instead of a list")
	output := fs.String("output", "", "write to this file instead of standard output")
	watch := fs.Bool("watch", false, "keep the output up to date")
	if err := parse(fs, args); err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	if *output != "" && *watch {
		return watchRoutes(ctx, a, *output, *nginx)
	}

	rendered, err := renderRoutes(ctx, a, *nginx)
	if err != nil {
		return err
	}

	if *output == "" {
		fmt.Fprint(os.Stdout, rendered)
		return nil
	}
	return writeFileAtomically(*output, rendered)
}

func renderRoutes(ctx context.Context, a *app.App, nginx bool) (string, error) {
	modules, err := a.Store.Integrations().List(ctx)
	if err != nil {
		return "", err
	}

	routes := modulehost.Describe(a.Cfg.PublicHost, modules)
	if !nginx {
		return describeRoutes(a, routes), nil
	}
	return modulehost.NginxServerBlocks(routes, a.Cfg.TLSServerName), nil
}

// describeRoutes is the human-readable form: which names point here, and which
// of them an operator still has to create.
//
// An address in a configuration file is not a name in DNS. Saying which of the
// two an administrator is looking at is the difference between a five-minute job
// and an afternoon of wondering why nothing answers.
func describeRoutes(a *app.App, routes []modulehost.Route) string {
	out := fmt.Sprintf("instance: %s\n", a.Cfg.PublicHost)

	if len(routes) == 0 {
		return out + "\nno module asked to be reachable from outside\n"
	}

	out += "\n"
	for _, route := range routes {
		address := route.URL()
		out += fmt.Sprintf("%-22s %s\n", route.Kind, address)
		out += fmt.Sprintf("  %-20s upstream %s\n", "serves", route.Upstream)

		routing := models.RoutingSpec{
			Domains: route.Domains, Path: route.Path, Websocket: route.Websocket,
		}
		if modulehost.IsDedicatedHost(a.Cfg.PublicHost, routing) {
			out += "  point DNS and the certificate at this name\n"
		} else {
			out += "  no DNS record or certificate is needed: this is the instance's own name\n"
		}
	}
	return out
}

// watchRoutes keeps a configuration file in step with the modules.
//
// The write is atomic, because a proxy reading a half-written file will refuse
// to start, and the whole point is for a reload never to be the reason something
// is unreachable.
func watchRoutes(ctx context.Context, a *app.App, output string, nginx bool) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(routesRefresh)
	defer ticker.Stop()

	var previous string
	for {
		rendered, err := renderRoutes(ctx, a, nginx)
		if err != nil {
			a.Log.Warn("render module routes", "error", err)
		} else if rendered != previous {
			if err := writeFileAtomically(output, rendered); err != nil {
				a.Log.Error("write module routes", "path", output, "error", err)
			} else {
				previous = rendered
				a.Log.Info("module routes written", "path", output)
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// writeFileAtomically replaces a file in one step.
//
// A proxy reading a partially written configuration will either fail to start or,
// worse, come up without half its modules. Writing beside it and renaming means
// a reader sees the old file or the new one, never a third state.
func writeFileAtomically(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

var _ = flag.ContinueOnError
