// Package cli implements the dogit subcommands.
package cli

import (
	"flag"
	"fmt"
	"io"
	"runtime"
)

// Version is overridden at build time with -ldflags "-X ...cli.Version=...".
var Version = "dev"

const usage = `dogit - a self-hosted Git forge

Usage:
  dogit serve             run the web and API server
  dogit runner            run the CI job runner
  dogit hook <name>       git hook entry point (post-receive, post-update)
  dogit authorized-keys   print the OpenSSH authorized_keys line for a user
  dogit user create       create an account
  dogit user list         list accounts
  dogit project create    create a project and its bare repository
  dogit migrate           apply database migrations and exit
  dogit version           print version information

Git traffic is served by the system OpenSSH server: sshd authenticates keys via
"dogit authorized-keys" and then force-executes dogit-hook.

All settings come from environment variables prefixed with DOGIT_.
Run a subcommand with -h to see its flags.
`

func PrintUsage(w io.Writer) { fmt.Fprint(w, usage) }

func PrintVersion(w io.Writer) {
	fmt.Fprintf(w, "dogit %s (%s %s/%s)\n", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

// newFlagSet returns a flag set that reports errors instead of exiting, so that
// main can control the failure path.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parse(fs *flag.FlagSet, args []string) error {
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("%s: %w", fs.Name(), err)
	}
	return nil
}
