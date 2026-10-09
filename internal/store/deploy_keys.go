package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// DeployKeyRepo holds the keys that may read or write a project without being a
// member of it.
//
// A runner is a machine, not a person. Giving it somebody's account would be both
// wrong and useless — the account belongs to a human whose rights change for
// reasons that have nothing to do with a build — so the runner gets a key of its
// own that says what it may do and nothing else.
type DeployKeyRepo struct{ s *Store }

func (s *Store) DeployKeys() *DeployKeyRepo { return &DeployKeyRepo{s: s} }

// DeployKey is one key on one project.
type DeployKey struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	Name        string
	Fingerprint string
	PublicKey   string
	// CanPush is false for a key that may only read. A runner that only builds and
	// pulls has no business being able to write, and a key that can do less cannot
	// be blamed for something it could not do.
	CanPush   bool
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// Create records a key.
func (r *DeployKeyRepo) Create(ctx context.Context, key *DeployKey) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO project_deploy_keys (id, project_id, name, fingerprint, public_key,
		                                 can_push, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		key.ID, key.ProjectID, key.Name, key.Fingerprint, key.PublicKey,
		key.CanPush, key.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create deploy key: %w", err)
	}
	return nil
}

// ForProject returns a project's keys that are still valid.
//
// Expiry is checked here rather than by a cleanup job: a key whose moment has
// passed is not usable, and a key that is merely not yet cleaned up is still
// refused. The row is kept so that a log can say which key was used.
func (r *DeployKeyRepo) ForProject(ctx context.Context, projectID uuid.UUID) ([]DeployKey, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, project_id, name, fingerprint, public_key, can_push, expires_at, created_at
		FROM project_deploy_keys
		WHERE project_id = $1 AND (expires_at IS NULL OR expires_at > now())
		ORDER BY created_at`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list deploy keys: %w", err)
	}
	defer rows.Close()

	keys := []DeployKey{}
	for rows.Next() {
		var key DeployKey
		if err := rows.Scan(&key.ID, &key.ProjectID, &key.Name, &key.Fingerprint,
			&key.PublicKey, &key.CanPush, &key.ExpiresAt, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan deploy key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// AllValid returns every key that may still be used.
//
// Every connection is offered every key, because the OpenSSH AuthorizedKeysCommand
// hook is asked once per login and is told only the login name — which is the same
// shared account for everybody. So keys are filtered after the connection, where
// the fingerprint is finally known.
func (r *DeployKeyRepo) AllValid(ctx context.Context) ([]DeployKey, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, project_id, name, fingerprint, public_key, can_push, expires_at, created_at
		FROM project_deploy_keys
		WHERE expires_at IS NULL OR expires_at > now()
		ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list deploy keys: %w", err)
	}
	defer rows.Close()

	keys := []DeployKey{}
	for rows.Next() {
		var key DeployKey
		if err := rows.Scan(&key.ID, &key.ProjectID, &key.Name, &key.Fingerprint,
			&key.PublicKey, &key.CanPush, &key.ExpiresAt, &key.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan deploy key: %w", err)
		}
		keys = append(keys, key)
	}
	return keys, rows.Err()
}

// ByFingerprint finds a key anywhere, which is how a connection that named one is
// resolved before anybody says which project it wanted.
func (r *DeployKeyRepo) ByFingerprint(ctx context.Context, fingerprint string) (*DeployKey, error) {
	var key DeployKey
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, project_id, name, fingerprint, public_key, can_push, expires_at, created_at
		FROM project_deploy_keys
		WHERE fingerprint = $1 AND (expires_at IS NULL OR expires_at > now())`,
		fingerprint).Scan(&key.ID, &key.ProjectID, &key.Name, &key.Fingerprint,
		&key.PublicKey, &key.CanPush, &key.ExpiresAt, &key.CreatedAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read deploy key: %w", err)
	}
	return &key, nil
}

// ActiveByName returns a job's key while it is still within its life.
func (r *DeployKeyRepo) ActiveByName(ctx context.Context, projectID uuid.UUID, name string) (*DeployKey, error) {
	var key DeployKey
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, project_id, name, fingerprint, public_key, can_push, expires_at, created_at
		FROM project_deploy_keys
		WHERE project_id = $1 AND name = $2 AND (expires_at IS NULL OR expires_at > now())`,
		projectID, name).Scan(&key.ID, &key.ProjectID, &key.Name, &key.Fingerprint,
		&key.PublicKey, &key.CanPush, &key.ExpiresAt, &key.CreatedAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read a job's key: %w", err)
	}
	return &key, nil
}

// Revoke removes one key.
//
// Used when a job is over: a key that outlived its job would be a key nobody is
// watching, which is the state nobody should have to think about.
func (r *DeployKeyRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM project_deploy_keys WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("revoke deploy key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeForJob removes every key a job was given, by their name.
//
// The name carries the job's id, which is what makes this safe to call more than
// once: a second call removes nothing and says so.
func (r *DeployKeyRepo) RevokeForJob(ctx context.Context, projectID uuid.UUID, name string) (int, error) {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM project_deploy_keys WHERE project_id = $1 AND name = $2`, projectID, name)
	if err != nil {
		return 0, fmt.Errorf("revoke a job's keys: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// PurgeExpired removes keys whose moment has passed.
func (r *DeployKeyRepo) PurgeExpired(ctx context.Context) (int64, error) {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM project_deploy_keys WHERE expires_at IS NOT NULL AND expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("purge expired keys: %w", err)
	}
	return tag.RowsAffected(), nil
}
