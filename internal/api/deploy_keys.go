package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/ssh"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/store"
)

// deployKeyLifetime is how long a key handed to a runner may be used.
//
// Sixty seconds, and the number is smaller than it looks because the key is only
// needed for one thing: the SSH handshake that starts a clone. Once that
// connection is open it runs for as long as the fetch takes, so a big repository
// is not affected by this at all — the clock governs whether a key may be
// presented, not how long a session it opened may last.
//
// It starts when the runner asks for the key, which is the moment it is about to
// clone, rather than when it claimed the job: a runner may hold a job for minutes
// before it begins, and a key on a clock from the moment of claiming would be dead
// before the machine was ready.
const deployKeyLifetime = 60 * time.Second

// jobKeyName is what a key handed to a job is called.
//
// The job's id is in the name, which is what makes revocation exact: it removes the
// keys of one job and nothing else, and doing it twice removes nothing.
func jobKeyName(jobID int64) string {
	return fmt.Sprintf("ci-job-%d", jobID)
}

// issueJobKey mints a key a runner can clone with, and returns it once.
//
// The private half is never stored. The core writes down what may be used and hands
// the rest to whoever asked, exactly once, because a stored private key is a
// credential that exists in two places and is expected to be used from both.
func (s *Server) issueJobKey(r *http.Request, projectID uuid.UUID, jobID int64) (map[string]any, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate a key: %w", err)
	}

	sshPublic, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil, fmt.Errorf("prepare a key: %w", err)
	}

	block, err := ssh.MarshalPrivateKey(private, "dogit-ci")
	if err != nil {
		return nil, fmt.Errorf("prepare the private half: %w", err)
	}

	// A key the runner already holds is reused rather than replaced. The private
	// half is not stored, so the one the runner has is the only one that will ever
	// work with this fingerprint — which is why the row is left alone and the runner
	// is told it already has one.
	if existing, err := s.store.DeployKeys().ActiveByName(r.Context(), projectID, jobKeyName(jobID)); err == nil {
		return map[string]any{
			"fingerprint": existing.Fingerprint,
			"expires_at":  existing.ExpiresAt.Format(time.RFC3339),
			"unchanged":   true,
		}, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	expires := time.Now().Add(deployKeyLifetime)
	key := &store.DeployKey{
		ID:          uuid.New(),
		ProjectID:   projectID,
		Name:        jobKeyName(jobID),
		Fingerprint: auth.FingerprintBytes(sshPublic.Marshal()),
		PublicKey:   string(ssh.MarshalAuthorizedKey(sshPublic)),
		// A job reads code in order to build it. Writing is a different permission
		// and a separate decision, and a key that can only read cannot be blamed for
		// a push it could not have made.
		CanPush:   false,
		ExpiresAt: &expires,
	}

	if err := s.store.DeployKeys().Create(r.Context(), key); err != nil {
		return nil, err
	}

	s.log.Info("issued a key for a job", "job_id", jobID, "project_id", projectID,
		"expires_at", expires.Format(time.RFC3339))

	return map[string]any{
		"private_key": string(pem.EncodeToMemory(block)),
		"fingerprint": key.Fingerprint,
		"expires_at":  expires.Format(time.RFC3339),
	}, nil
}

// revokeJobKey removes the keys one job was given.
//
// Called when the job reports it is over. A job that never finishes leaves its key
// behind, which is what the expiry is for — a key nobody remembered cannot be
// trusted, so it stops working whether or not anybody noticed it.
func (s *Server) revokeJobKey(r *http.Request, projectID uuid.UUID, jobID int64) error {
	if projectID == uuid.Nil {
		return nil
	}

	removed, err := s.store.DeployKeys().RevokeForJob(r.Context(), projectID, jobKeyName(jobID))
	if err != nil {
		return err
	}
	if removed > 0 {
		s.log.Info("a job's key was removed", "job_id", jobID, "project_id", projectID)
	}
	return nil
}
