// Command dogit-hook is the forced command that OpenSSH executes for every git
// connection. Keeping it a separate tiny binary means the per-connection cost
// is one exec and one database lookup.
//
// Usage: dogit-hook git <username>
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
	// sshd closes stdin when the client disconnects; exit on that rather than
	// blocking forever.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := cli.HookEntry(ctx, os.Args[1:]); err != nil {
		var code cli.ExitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintf(os.Stderr, "dogit-hook: %v\n", err)
		os.Exit(1)
	}
}
