package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ewolf/dogit/internal/models"
)

type UserRepo struct{ s *Store }

func (s *Store) Users() *UserRepo { return &UserRepo{s: s} }

func (r *UserRepo) Create(ctx context.Context, u *models.User) error {
	const q = `
		INSERT INTO users (username, email, name, password_hash, is_admin)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at, updated_at`
	err := r.s.pool.QueryRow(ctx, q,
		u.Username, strings.ToLower(u.Email), u.Name, u.PasswordHash, u.IsAdmin,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return fmt.Errorf("%w: username or email already taken", ErrConflict)
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

const userColumns = `id, username, email, name, password_hash, is_admin, created_at, updated_at, last_sign_in`

func scanUser(row interface{ Scan(...any) error }) (*models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.Name, &u.PasswordHash,
		&u.IsAdmin, &u.CreatedAt, &u.UpdatedAt, &u.LastSignIn)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (r *UserRepo) ByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	return scanUser(r.s.pool.QueryRow(ctx, `SELECT `+userColumns+` FROM users WHERE id = $1`, id))
}

func (r *UserRepo) ByUsername(ctx context.Context, username string) (*models.User, error) {
	return scanUser(r.s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(username) = lower($1)`, username))
}

func (r *UserRepo) ByEmail(ctx context.Context, email string) (*models.User, error) {
	return scanUser(r.s.pool.QueryRow(ctx,
		`SELECT `+userColumns+` FROM users WHERE lower(email) = lower($1)`, email))
}

func (r *UserRepo) TouchLastSignIn(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx, `UPDATE users SET last_sign_in = now() WHERE id = $1`, id)
	return err
}

func (r *UserRepo) UpdatePassword(ctx context.Context, id uuid.UUID, hash []byte) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1`, id, hash)
	return err
}

func (r *UserRepo) UpdateProfile(ctx context.Context, id uuid.UUID, name, email string) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE users SET name = $2, email = lower($3), updated_at = now() WHERE id = $1`,
		id, name, strings.ToLower(email))
	return err
}

type ListUsersFilter struct {
	Query  string
	Limit  int
	Offset int
}

func (r *UserRepo) List(ctx context.Context, f ListUsersFilter) ([]*models.User, int, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 20
	}
	if f.Query == "" {
		var total int
		if err := r.s.pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count users: %w", err)
		}
		rows, err := r.s.pool.Query(ctx,
			`SELECT `+userColumns+` FROM users ORDER BY username LIMIT $1 OFFSET $2`,
			f.Limit, f.Offset)
		if err != nil {
			return nil, 0, fmt.Errorf("list users: %w", err)
		}
		return collectUsers(rows, total, err)
	}

	pattern := "%" + f.Query + "%"
	var total int
	if err := r.s.pool.QueryRow(ctx,
		`SELECT count(*) FROM users WHERE username ILIKE $1 OR email ILIKE $1`, pattern,
	).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+userColumns+` FROM users
		 WHERE username ILIKE $1 OR email ILIKE $1
		 ORDER BY username LIMIT $2 OFFSET $3`, pattern, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("search users: %w", err)
	}
	return collectUsers(rows, total, err)
}

func collectUsers(rows pgx.Rows, total int, err error) ([]*models.User, int, error) {
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*models.User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, u)
	}
	return out, total, rows.Err()
}

type SSHKeyRepo struct{ s *Store }

func (s *Store) SSHKeys() *SSHKeyRepo { return &SSHKeyRepo{s: s} }

const sshKeyColumns = `id, user_id, title, fingerprint, public_key, created_at, last_used_at`

func (r *SSHKeyRepo) Create(ctx context.Context, k *models.SSHKey) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO ssh_keys (user_id, title, fingerprint, public_key)
		VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		k.UserID, k.Title, k.Fingerprint, k.PublicKey,
	).Scan(&k.ID, &k.CreatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return fmt.Errorf("%w: key already registered", ErrConflict)
		}
		return fmt.Errorf("create ssh key: %w", err)
	}
	return nil
}

func (r *SSHKeyRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*models.SSHKey, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+sshKeyColumns+` FROM ssh_keys WHERE user_id = $1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list ssh keys: %w", err)
	}
	defer rows.Close()

	out := []*models.SSHKey{}
	for rows.Next() {
		var k models.SSHKey
		if err := rows.Scan(&k.ID, &k.UserID, &k.Title, &k.Fingerprint, &k.PublicKey,
			&k.CreatedAt, &k.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &k)
	}
	return out, rows.Err()
}

func (r *SSHKeyRepo) Delete(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM ssh_keys WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete ssh key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// KeyWithUser joins a registered key with the account that owns it.
type KeyWithUser struct {
	Key      models.SSHKey
	UserID   uuid.UUID
	Username string
}

// AllWithUser returns every registered key together with its owner.
//
// This is what the OpenSSH AuthorizedKeysCommand serves: all clients log in as
// the shared "git" account, so every key must be offered and each one carries a
// forced command naming the account it actually belongs to.
func (r *SSHKeyRepo) AllWithUser(ctx context.Context) ([]KeyWithUser, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT k.id, k.user_id, k.title, k.fingerprint, k.public_key, k.created_at,
		       k.last_used_at, u.username
		FROM ssh_keys k JOIN users u ON u.id = k.user_id
		ORDER BY u.username, k.created_at`)
	if err != nil {
		return nil, fmt.Errorf("list all ssh keys: %w", err)
	}
	defer rows.Close()

	out := []KeyWithUser{}
	for rows.Next() {
		var e KeyWithUser
		if err := rows.Scan(&e.Key.ID, &e.Key.UserID, &e.Key.Title, &e.Key.Fingerprint,
			&e.Key.PublicKey, &e.Key.CreatedAt, &e.Key.LastUsedAt, &e.Username); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *SSHKeyRepo) TouchUsed(ctx context.Context, fingerprint string) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE ssh_keys SET last_used_at = now() WHERE fingerprint = $1`, fingerprint)
	return err
}

type SessionRepo struct{ s *Store }

func (s *Store) Sessions() *SessionRepo { return &SessionRepo{s: s} }

func (r *SessionRepo) Create(ctx context.Context, sess *models.Session) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO sessions (user_id, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		sess.UserID, sess.ExpiresAt, sess.UserAgent, sess.IP,
	).Scan(&sess.ID, &sess.CreatedAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

func (r *SessionRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Session, error) {
	var sess models.Session
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, user_id, expires_at, created_at, last_seen_at, user_agent, ip
		FROM sessions WHERE id = $1 AND expires_at > now()`, id,
	).Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt,
		&sess.LastSeenAt, &sess.UserAgent, &sess.IP)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	return &sess, nil
}

func (r *SessionRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, id)
	return err
}

func (r *SessionRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func (r *SessionRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*models.Session, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, user_id, expires_at, created_at, last_seen_at, user_agent, ip
		FROM sessions WHERE user_id = $1 ORDER BY created_at DESC LIMIT 50`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.Session{}
	for rows.Next() {
		var sess models.Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.ExpiresAt, &sess.CreatedAt,
			&sess.LastSeenAt, &sess.UserAgent, &sess.IP); err != nil {
			return nil, err
		}
		out = append(out, &sess)
	}
	return out, rows.Err()
}

type TokenRepo struct{ s *Store }

func (s *Store) Tokens() *TokenRepo { return &TokenRepo{s: s} }

func (r *TokenRepo) Create(ctx context.Context, t *models.PersonalAccessToken) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO personal_access_tokens (user_id, name, token_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at`,
		t.UserID, t.Name, t.TokenHash, t.Scopes, t.ExpiresAt,
	).Scan(&t.ID, &t.CreatedAt)
	if err != nil {
		return fmt.Errorf("create token: %w", err)
	}
	return nil
}

func (r *TokenRepo) ByHash(ctx context.Context, hash []byte) (*models.PersonalAccessToken, error) {
	var t models.PersonalAccessToken
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, user_id, name, scopes, expires_at, last_used_at, created_at
		FROM personal_access_tokens
		WHERE token_hash = $1 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > now())`, hash,
	).Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &t.ExpiresAt, &t.LastUsedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get token: %w", err)
	}
	return &t, nil
}

// TouchUsed records that a token was just used, which the UI surfaces as
// "last used".
func (r *TokenRepo) TouchUsed(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE personal_access_tokens SET last_used_at = now() WHERE id = $1`, id)
	return err
}

func (r *TokenRepo) Revoke(ctx context.Context, id, userID uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE personal_access_tokens SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		id, userID)
	if err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *TokenRepo) ListByUser(ctx context.Context, userID uuid.UUID) ([]*models.PersonalAccessToken, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, user_id, name, scopes, expires_at, last_used_at, created_at, revoked_at
		FROM personal_access_tokens WHERE user_id = $1 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.PersonalAccessToken{}
	for rows.Next() {
		var t models.PersonalAccessToken
		if err := rows.Scan(&t.ID, &t.UserID, &t.Name, &t.Scopes, &t.ExpiresAt,
			&t.LastUsedAt, &t.CreatedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}
