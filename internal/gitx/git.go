// Package gitx wraps the git binary. Every repository operation goes through
// git's plumbing commands against bare repositories: no working tree is ever
// checked out and no process state is left behind, which makes all operations
// safe to run concurrently.
//
// Arguments are always passed as a slice to exec.Command, and user-supplied
// paths are separated with "--", so a repository path or ref can never be
// interpreted as a shell fragment or an option.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// EmptyTree is the well-known hash of git's empty tree object. Diffing against
// it shows every file of a root commit as added.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Git is a handle to the git executable.
type Git struct {
	binary  string
	timeout time.Duration
	env     []string

	// version caches "git version ..." output for capability checks.
	once    sync.Once
	version string
}

type Options struct {
	Binary  string
	Timeout time.Duration
}

func New(opts Options) *Git {
	if opts.Binary == "" {
		opts.Binary = "git"
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}
	return &Git{binary: opts.Binary, timeout: opts.Timeout}
}

func (g *Git) Binary() string { return g.binary }

// Version returns the git version string, e.g. "2.55.0".
func (g *Git) Version(ctx context.Context) string {
	g.once.Do(func() {
		out, err := exec.CommandContext(ctx, g.binary, "--version").Output()
		if err != nil {
			g.version = ""
			return
		}
		fields := strings.Fields(string(out))
		if len(fields) >= 3 {
			g.version = fields[2]
		}
	})
	return g.version
}

// SupportsMergeTree reports whether `git merge-tree --write-tree` is available,
// which requires git 2.38 or newer.
func (g *Git) SupportsMergeTree(ctx context.Context) bool {
	v := g.Version(ctx)
	if v == "" {
		return false
	}
	var major, minor int
	if _, err := fmt.Sscanf(v, "%d.%d", &major, &minor); err != nil {
		return false
	}
	return major > 2 || (major == 2 && minor >= 38)
}

// CommandError carries the exit status and stderr of a failed git invocation,
// so callers can surface git's own diagnostics to the user.
type CommandError struct {
	Args     []string
	ExitCode int
	Stderr   string
}

func (e *CommandError) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

// IsConflict reports whether git failed because of merge conflicts.
func IsConflict(err error) bool {
	var ce *CommandError
	if errors.As(err, &ce) {
		return ce.ExitCode == 1
	}
	return false
}

// run executes git in dir with the given arguments and returns stdout.
func (g *Git) run(ctx context.Context, dir string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, g.binary, args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	// A deterministic environment keeps commit hashes reproducible and avoids
	// inheriting an interactive git config from the operator's shell.
	cmd.Env = append(g.baseEnv(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return nil, &CommandError{Args: args, ExitCode: exitCode, Stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

// runEnv is run with extra environment entries, used for commit metadata
// (GIT_AUTHOR_*, GIT_COMMITTER_*).
func (g *Git) runEnv(ctx context.Context, dir string, extraEnv []string, stdin []byte, args ...string) ([]byte, error) {
	if len(extraEnv) == 0 {
		return g.run(ctx, dir, stdin, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, g.binary, args...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Env = append(g.baseEnv(),
		"GIT_TERMINAL_PROMPT=0", "LC_ALL=C")
	cmd.Env = append(cmd.Env, extraEnv...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		exitCode := -1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return nil, &CommandError{Args: args, ExitCode: exitCode, Stderr: stderr.String()}
	}
	return stdout.Bytes(), nil
}

func (g *Git) baseEnv() []string {
	if g.env != nil {
		return g.env
	}
	env := append(os.Environ(),
		"HOME=", // ignore the operator's ~/.gitconfig
		"XDG_CONFIG_HOME=",
		"GIT_CONFIG_NOSYSTEM=1",
	)
	g.env = env
	return env
}

// InitBare creates an empty bare repository at path.
func (g *Git) InitBare(ctx context.Context, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create repository directory: %w", err)
	}
	_, err := g.run(ctx, filepath.Dir(path), nil,
		"init", "--bare", "--initial-branch=main", path)
	if err != nil {
		return fmt.Errorf("init bare repository: %w", err)
	}
	// rerere lets git remember every conflict resolution a user makes and
	// replay it automatically on later merges of the same conflict.
	for _, cfg := range [][2]string{
		{"rerere.enabled", "true"},
		{"receive.denyNonFastForwards", "false"},
		{"gc.auto", "0"},
		{"uploadpack.allowFilter", "true"},
		{"receive.advertisePushOptions", "true"},
	} {
		if _, err := g.run(ctx, path, nil, "config", cfg[0], cfg[1]); err != nil {
			return fmt.Errorf("configure %s: %w", cfg[0], err)
		}
	}
	// Dumb-HTTP read access for browsers and plain curl.
	if _, err := g.run(ctx, path, nil, "update-server-info"); err != nil {
		return fmt.Errorf("update-server-info: %w", err)
	}
	return nil
}

// UpdateServerInfo refreshes the files that dumb HTTP clients read.
func (g *Git) UpdateServerInfo(ctx context.Context, repoPath string) error {
	_, err := g.run(ctx, repoPath, nil, "update-server-info")
	return err
}

// errorsAs is a thin wrapper so callers in this package can use errors.As
// without importing it everywhere.
func errorsAs(err error, target any) bool { return errors.As(err, target) }

// Run executes a git command in repoDir and returns its stdout. Use it for
// occasional plumbing calls that do not warrant a dedicated helper.
func (g *Git) Run(ctx context.Context, repoDir string, stdin []byte, args ...string) ([]byte, error) {
	return g.run(ctx, repoDir, stdin, args...)
}

// SetConfig sets a single git configuration value in the repository.
func (g *Git) SetConfig(ctx context.Context, repoPath, key, value string) error {
	_, err := g.run(ctx, repoPath, nil, "config", key, value)
	if err != nil {
		return fmt.Errorf("set config %s: %w", key, err)
	}
	return nil
}
