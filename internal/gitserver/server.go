package gitserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// Server authorises and runs git service commands for a repository.
type Server struct {
	store    *store.Store
	repoRoot string
	gitBin   string
	hookBin  string
}

func New(st *store.Store, repoRoot, gitBin, hookBin string) *Server {
	if gitBin == "" {
		gitBin = "git"
	}
	if hookBin == "" {
		hookBin = "dogit"
	}
	return &Server{store: st, repoRoot: repoRoot, gitBin: gitBin, hookBin: hookBin}
}

// Authorize verifies that user may perform verb against the named repository.
// It returns the project and the absolute repository directory.
//
// Pushes (git-receive-pack) need ActionPush, clones and fetches need
// ActionReadProject. Projects that do not exist produce the same message git
// users expect, not a stack trace.
func (s *Server) Authorize(ctx context.Context, user *models.User, verb, requestedPath string) (*models.Project, string, error) {
	if user == nil {
		return nil, "", errors.New("not authenticated")
	}

	action := store.ActionReadProject
	if verb == VerbReceivePack {
		action = store.ActionPush
	}

	project, err := s.store.Projects().ByPath(ctx, NormalizePath(requestedPath))
	if errors.Is(err, store.ErrNotFound) {
		if action == store.ActionPush {
			return nil, "", &ProtocolError{Message: fmt.Sprintf("Project does not exist: %s", NormalizePath(requestedPath))}
		}
		return nil, "", &ProtocolError{Message: fmt.Sprintf("Repository not found: %s", NormalizePath(requestedPath))}
	}
	if err != nil {
		return nil, "", err
	}

	allowed, err := s.store.Permissions().Can(ctx, user, project, action)
	if err != nil {
		return nil, "", err
	}
	if !allowed {
		return nil, "", &ProtocolError{
			Message: fmt.Sprintf("You do not have permission to %s %s", verb, project.Path),
		}
	}

	repoDir, err := ResolveRepo(s.repoRoot, requestedPath)
	if err != nil {
		return nil, "", err
	}
	return project, repoDir, nil
}

// ProtocolError is a user-facing error: git prints it verbatim on stderr.
type ProtocolError struct{ Message string }

func (e *ProtocolError) Error() string { return e.Message }

// Exec runs a git service command with the caller's streams attached. stdin and
// stdout must be the raw SSH channel: that pairing is the git wire protocol,
// which is why no part of it needs reimplementing.
//
// The returned int is the process exit code reported to the client.
func (s *Server) Exec(
	ctx context.Context,
	verb string,
	user *models.User,
	project *models.Project,
	repoDir string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	// A large first push is slow but legitimate; a stuck client is not.
	runCtx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	// git's plumbing entry points are subcommands of git itself: the service
	// names git-upload-pack / git-receive-pack map to `git upload-pack` and
	// `git receive-pack`.
	subcommand := strings.TrimPrefix(verb, "git-")
	cmd := exec.CommandContext(runCtx, s.gitBin, subcommand, repoDir)
	cmd.Dir = repoDir
	cmd.Env = append(os.Environ(),
		// Protocol v2 gives better ref advertisement; git falls back itself.
		"GIT_PROTOCOL=version=2",
		"DOGIT_PROJECT_ID="+project.ID.String(),
		"DOGIT_PROJECT_PATH="+project.Path,
		"DOGIT_HOOK_BINARY="+s.hookBin,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
	if user != nil {
		cmd.Env = append(cmd.Env,
			"DOGIT_USER_ID="+user.ID.String(),
			"DOGIT_USERNAME="+user.Username,
		)
	}

	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// git already reported the reason on stderr.
			return ee.ExitCode()
		}
		fmt.Fprintf(stderr, "fatal: %s could not be started: %v\n", verb, err)
		return 1
	}
	return 0
}

// Handle parses a raw command line, authorises it and executes git. It is the
// single entry point used by both transports.
func (s *Server) Handle(ctx context.Context, rawCommand, username string, stdin io.Reader, stdout, stderr io.Writer) int {
	verb, requested, err := ParseGitCommand(rawCommand)
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	user, err := s.store.Users().ByUsername(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "ERROR: unknown user %q\n", username)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	project, repoDir, err := s.Authorize(ctx, user, verb, requested)
	if err != nil {
		var pe *ProtocolError
		if errors.As(err, &pe) {
			fmt.Fprintf(stderr, "ERROR: %s\n", pe.Message)
		} else {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
		return 1
	}

	return s.Exec(ctx, verb, user, project, repoDir, stdin, stdout, stderr)
}

// AuthorizedKeyLine renders one authorized_keys entry: the key with the
// "restrict" option (no pty, no forwarding, no agent, no X11) and a forced
// command that routes the connection back into dogit, naming the account the key
// belongs to and the key itself.
//
//	restrict,command="/usr/local/bin/dogit-hook git alice SHA256:abc" ssh-ed25519 AAAA...
//
// Every client logs in as the shared "git" account, so the real identity travels
// in the forced command rather than in the login name. The fingerprint rides
// along for the same reason: it is what lets the server record which key was used,
// which sshd does not expose to the command it runs.
func AuthorizedKeyLine(username, fingerprint, publicKey, hookBinary string) string {
	return fmt.Sprintf("restrict,command=%q %s", hookBinary+" git "+username+" "+fingerprint, publicKey)
}
