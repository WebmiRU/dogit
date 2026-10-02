package cli

import (
	"context"
	"fmt"

	"github.com/ewolf/dogit/internal/api"
	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/web"
)

// Serve runs the web and API tier.
func Serve(ctx context.Context, args []string) error {
	fs := newFlagSet("serve")
	if err := parse(fs, args); err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	web.SetVersion(Version)

	repoSvc := repos.New(a.Store, a.Git, a.Cfg.RepoDir)
	apiSrv := api.New(a.Cfg, a.Log, a.Store, a.Git, repoSvc)

	srv := web.NewServer(a.Cfg, a.Log, a.Store, apiSrv)
	if err := srv.Listen(); err != nil {
		return err
	}

	// Consume the durable event log so subscribers see hook-published events, and
	// run the periodic housekeeping (expired tokens, stale modules, sessions).
	go a.Events.RunTail(ctx, 0)
	startJanitor(ctx, a)

	a.Log.Info("dogit starting",
		"version", Version,
		"env", a.Cfg.Environment,
		"git", a.Cfg.GitBinary,
		"repo_root", a.Cfg.RepoDir)

	if err := srv.Serve(ctx); err != nil {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// Migrate applies pending database migrations and exits. The web, ssh and
// runner processes apply migrations on start-up as well, so this command exists
// mainly for deployments that want to gate start-up on the schema.
func Migrate(ctx context.Context, args []string) error {
	fs := newFlagSet("migrate")
	if err := parse(fs, args); err != nil {
		return err
	}

	a, err := app.New(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	a.Log.Info("migrations applied", "version", Version)
	return nil
}
