// Command dogit is the single binary behind every process role: the web and API
// tier, the CI runner, the git hook entry points, and the OpenSSH
// AuthorizedKeysCommand helper.
//
// Git traffic itself is served by the system OpenSSH server, which
// authenticates keys through `dogit authorized-keys` and then force-executes
// dogit-hook. That keeps the audited SSH implementation out of this codebase.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/ewolf/dogit/internal/cli"
)

func main() {
	if err := run(); err != nil {
		var code cli.ExitCode
		switch {
		case errors.Is(err, context.Canceled):
			// Graceful shutdown on SIGINT/SIGTERM.
		case errors.As(err, &code):
			os.Exit(int(code))
		default:
			fmt.Fprintf(os.Stderr, "dogit: %v\n", err)
			os.Exit(1)
		}
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	argv := os.Args[1:]
	if len(argv) == 0 {
		cli.PrintUsage(os.Stdout)
		return nil
	}

	command, args := argv[0], argv[1:]

	switch command {
	case "serve":
		return cli.Serve(ctx, args)
	case "runner":
		return cli.Runner(ctx, args)
	case "hook":
		return cli.Hook(ctx, args)
	case "user":
		return cli.User(ctx, args)
	case "module":
		return cli.Module(ctx, args)
	case "key":
		return cli.Key(ctx, args)
	case "project":
		return cli.Project(ctx, args)
	case "authorized-keys":
		return cli.AuthorizedKeys(ctx, args)
	case "migrate":
		return cli.Migrate(ctx, args)
	case "version":
		cli.PrintVersion(os.Stdout)
		return nil
	case "help", "-h", "--help":
		cli.PrintUsage(os.Stdout)
		return nil
	default:
		cli.PrintUsage(os.Stderr)
		return fmt.Errorf("unknown command %q", command)
	}
}
