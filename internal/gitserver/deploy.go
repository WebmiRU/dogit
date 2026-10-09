package gitserver

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// A deploy key is a machine's credential, not a person's.
//
// It is carried in the forced command rather than in the login name because every
// client logs in as the same shared "git" account: the identity travels in the
// command line, and this is where that line is read back.

type contextKey int

const deployKeyContextKey contextKey = 1

// WithDeployKey marks a connection as having arrived with a deploy key's
// fingerprint rather than a user's name.
func WithDeployKey(ctx context.Context, fingerprint string) context.Context {
	return context.WithValue(ctx, deployKeyContextKey, fingerprint)
}

// DeployIdentityFrom reports which deploy key connected, if any.
func DeployIdentityFrom(ctx context.Context) (string, bool) {
	fingerprint, ok := ctx.Value(deployKeyContextKey).(string)
	return fingerprint, ok && fingerprint != ""
}

// handleDeploy authorises a connection that presented a deploy key.
//
// The key names its own project, so there is nothing to be asked about which
// repository was wanted: a key issued for one project cannot be pointed at another,
// which is the whole point of issuing it rather than borrowing a person's account.
//
// And a deploy key may only read. Writing is a different permission and a separate
// decision, and a key that cannot push cannot be blamed for a push.
func (s *Server) handleDeploy(ctx context.Context, verb, requested string,
	fingerprint string, stdin io.Reader, stdout, stderr io.Writer) int {

	if verb != "git-upload-pack" {
		// Said in the words a git client prints, so the user sees why rather than a
		// permission error they have no way to act on.
		fmt.Fprintf(stderr, "ERROR: this key may only read %s. Pushing needs an account on the instance.\n", requested)
		return 1
	}

	key, err := s.store.DeployKeys().ByFingerprint(ctx, fingerprint)
	if errors.Is(err, store.ErrNotFound) {
		// Gone or expired. The message says the key is not valid rather than that
		// something is missing, because an expired key is exactly what it is.
		fmt.Fprintf(stderr, "ERROR: this key is not valid. Keys given to a build are short-lived.\n")
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "ERROR: %v\n", err)
		return 1
	}

	project, repoDir, err := s.AuthorizeDeploy(ctx, key.ProjectID, verb, requested)
	if err != nil {
		var pe *ProtocolError
		if errors.As(err, &pe) {
			fmt.Fprintf(stderr, "ERROR: %s\n", pe.Message)
		} else {
			fmt.Fprintf(stderr, "ERROR: %v\n", err)
		}
		return 1
	}

	return s.Exec(ctx, verb, nil, project, repoDir, stdin, stdout, stderr)
}

// AuthorizeDeploy checks that a key's project is the repository being asked for.
//
// The check is not a formality: without it a key issued for one project would read
// every project on the instance, because the fingerprint resolves to a project while
// the request names another.
func (s *Server) AuthorizeDeploy(ctx context.Context, projectID uuid.UUID, verb, requestedPath string) (*models.Project, string, error) {
	project, err := s.store.Projects().ByID(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", &ProtocolError{Message: "Repository not found"}
	}
	if err != nil {
		return nil, "", err
	}

	// The request must name the very repository the key was issued for. Compared as
	// normalised paths, because a client may ask for it with or without ".git".
	if NormalizePath(requestedPath) != project.Path {
		return nil, "", &ProtocolError{
			Message: fmt.Sprintf("This key is for %s, not for %s",
				project.Path, NormalizePath(requestedPath)),
		}
	}

	repoDir, err := ResolveRepo(s.repoRoot, project.Path)
	if err != nil {
		return nil, "", err
	}
	return project, repoDir, nil
}

// DeployKeyLine renders one deploy key as an authorized_keys entry.
//
// The forced command carries the fingerprint and a marker saying this identity is a
// key rather than a person, so the server never has to guess which kind of identity
// connected — and a key issued to a runner can never be mistaken for an account.
func DeployKeyLine(key store.DeployKey, hookBinary string) string {
	return fmt.Sprintf("restrict,command=%q %s",
		hookBinary+" deploy "+key.Fingerprint, key.PublicKey)
}
