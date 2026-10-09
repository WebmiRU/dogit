package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ewolf/dogit/internal/app"
	"github.com/ewolf/dogit/internal/gitserver"
	"github.com/ewolf/dogit/internal/hooks"
	"github.com/ewolf/dogit/internal/hooks/postreceive"
)

// ExitCode lets a subcommand end the process with a specific status, which git
// clients interpret as the result of upload-pack or receive-pack.
type ExitCode int

func (e ExitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func exitCode(code int) error {
	if code == 0 {
		return nil
	}
	return ExitCode(code)
}

// HookEntry is the forced command that OpenSSH executes for every git
// connection. It ships as a separate small binary (dogit-hook) so the
// authorisation round trip stays cheap.
//
// Usage: dogit-hook git <username>
//
// The raw client command arrives on stdin, exactly as git sends it.
func HookEntry(ctx context.Context, args []string) error {
	fs := newFlagSet("hook-entry")
	if err := parse(fs, args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "dogit-hook: no arguments")
		return exitCode(1)
	}

	// dogit-hook doubles as the entry point for the hooks installed into each
	// repository, so the same binary handles both roles.
	if rest[0] == "hook" {
		return Hook(ctx, rest[1:])
	}

	// A deploy key is a machine's credential, not a person's. It arrives with its
	// fingerprint where a username would be, and the server resolves it there rather
	// than as a user: a key issued to a runner must never be able to act as somebody.
	if rest[0] == "deploy" {
		if len(rest) < 2 || rest[1] == "" {
			fmt.Fprintln(os.Stderr, "dogit-hook: no deploy key fingerprint")
			return exitCode(1)
		}

		a, err := app.NewWithLogLevel(ctx, hookLogLevel())
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
			return exitCode(1)
		}
		defer a.Close()

		srv := gitserver.New(a.Store, a.Cfg.RepoDir, a.Cfg.GitBinary, a.Cfg.HookBinary)
		return exitCode(srv.Handle(
			gitserver.WithDeployKey(ctx, rest[1]),
			originalCommand(), "", os.Stdin, os.Stdout, os.Stderr))
	}

	if rest[0] != "git" {
		fmt.Fprintf(os.Stderr, "dogit-hook: refusing %q; only git sessions are allowed\n", rest[0])
		return exitCode(1)
	}

	username := ""
	if len(rest) > 1 {
		username = rest[1]
	}
	// Optional third argument: the fingerprint of the key that authenticated,
	// appended by AuthorizedKeysCommand so that key usage can be recorded.
	fingerprint := ""
	if len(rest) > 2 {
		fingerprint = rest[2]
	}
	if username == "" {
		username = os.Getenv("USER")
	}
	if username == "" {
		fmt.Fprintln(os.Stderr, "dogit-hook: cannot determine the user")
		return exitCode(1)
	}

	a, err := app.NewWithLogLevel(ctx, hookLogLevel())
	if err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %v\n", err)
		return exitCode(1)
	}
	defer a.Close()

	if fingerprint != "" {
		// Best effort: a failed bookkeeping write must never block a push.
		if err := a.Store.SSHKeys().TouchUsed(ctx, fingerprint); err != nil {
			a.Log.Debug("record key usage", "fingerprint", fingerprint, "error", err)
		}
	}

	srv := gitserver.New(a.Store, a.Cfg.RepoDir, a.Cfg.GitBinary, a.Cfg.HookBinary)
	return exitCode(srv.Handle(ctx, originalCommand(), username, os.Stdin, os.Stdout, os.Stderr))
}

// AuthorizedKeys implements the OpenSSH AuthorizedKeysCommand hook.
//
// sshd passes the login name, which is always the shared "git" account, so every
// registered key is offered. Each entry carries a forced command naming the
// account that owns the key: that is where the real identity travels.
func AuthorizedKeys(ctx context.Context, args []string) error {
	fs := newFlagSet("authorized-keys")
	if err := parse(fs, args); err != nil {
		return err
	}

	// sshd reads our stdout verbatim, so diagnostics go to stderr only; sshd
	// copies them into its own log and never shows them to the client.
	a, err := app.New(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorized-keys: %v\n", err)
		return exitCode(1)
	}
	defer a.Close()

	keys, err := a.Store.SSHKeys().AllWithUser(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorized-keys: %v\n", err)
		return exitCode(1)
	}

	for _, k := range keys {
		fmt.Fprintln(os.Stdout,
			gitserver.AuthorizedKeyLine(k.Username, k.Key.Fingerprint, k.Key.PublicKey, a.Cfg.HookBinary))
	}

	// Deploy keys are offered here too, because sshd asks this command for every
	// connection and passes the login name — which is always the shared "git"
	// account, so it says nothing about which credential is being presented. The
	// forced command carries the fingerprint, and the server resolves that.
	deployKeys, err := a.Store.DeployKeys().AllValid(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "authorized-keys: %v\n", err)
		return nil
	}
	for _, key := range deployKeys {
		fmt.Fprintln(os.Stdout, gitserver.DeployKeyLine(key, a.Cfg.HookBinary))
	}

	return nil
}

// hookLogLevel reads the level the git entry points log at.
//
// It is read from the environment directly because the configuration has not been
// loaded yet at the point where the level has to be applied.
func hookLogLevel() string {
	if level := os.Getenv("DOGIT_HOOK_LOG_LEVEL"); level != "" {
		return level
	}
	return "warn"
}

// Hook dispatches the git hook entry points.
func Hook(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: dogit hook <post-receive>")
	}
	switch args[0] {
	case hooks.HookPostReceive:
		return runPostReceive(ctx, args[1:])
	default:
		return fmt.Errorf("unsupported hook %q", args[0])
	}
}

func runPostReceive(ctx context.Context, args []string) error {
	fs := newFlagSet("hook post-receive")
	projectPath := fs.String("project-path", "", "repository path as known to dogit")
	gitDir := fs.String("git-dir", "", "path to the bare repository")
	projectID := fs.String("project-id", "", "project UUID, taken from the environment if absent")

	if err := parse(fs, args); err != nil {
		return err
	}

	if *gitDir == "" {
		return fmt.Errorf("--git-dir is required")
	}
	if *projectPath == "" && *projectID == "" {
		return fmt.Errorf("--project-path or --project-id is required")
	}

	a, err := app.NewWithLogLevel(ctx, hookLogLevel())
	if err != nil {
		// A failing post-receive hook cannot undo the push: report and let the
		// commit stand. The next ref update catches up.
		fmt.Fprintf(os.Stderr, "post-receive: %v\n", err)
		return nil
	}
	defer a.Close()

	// The installed hook script passes the path; the SSH server passes the id.
	if *projectID == "" {
		*projectID = os.Getenv("DOGIT_PROJECT_ID")
	}

	return postreceive.Run(ctx, a.Store, a.Git, a.Events, os.Stdin, postreceive.Options{
		RepoPath:    *gitDir,
		ProjectPath: *projectPath,
		ProjectID:   *projectID,
		ActorID:     os.Getenv("DOGIT_USER_ID"),
		Logger:      a.Log,
	})
}

// originalCommand returns the git command the client asked sshd to run.
//
// sshd puts it in SSH_ORIGINAL_COMMAND, not on stdin: stdin carries the git
// protocol stream, so reading a command from there would corrupt it. The stdin
// fallback exists for the in-process fallback server, which has no sshd.
func originalCommand() string {
	if cmd, ok := os.LookupEnv("SSH_ORIGINAL_COMMAND"); ok {
		return strings.TrimSpace(cmd)
	}
	return readCommandLine(os.Stdin)
}

// readCommandLine is the stdin fallback described above. The read is bounded
// because the input comes from an untrusted client.
func readCommandLine(r io.Reader) string {
	const max = 4096
	buf := make([]byte, max)
	n, err := io.ReadFull(r, buf)
	if n == 0 && err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return ""
	}
	line := buf[:n]
	if i := strings.IndexAny(string(line), "\x00\r\n"); i >= 0 {
		line = line[:i]
	}
	return strings.TrimSpace(string(line))
}
