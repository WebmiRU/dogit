package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
)

// heartbeatWindow is how long a module may stay silent before it is considered
// offline. It is generous because a module restart in Kubernetes is not instant.
const heartbeatWindow = 90 * time.Second

type IntegrationRepo struct{ s *Store }

func (s *Store) Integrations() *IntegrationRepo { return &IntegrationRepo{s: s} }

const integrationColumns = `id, kind, name, endpoint, module_version, capabilities,
	settings_schema, status, enabled, last_seen_at, registered_at, created_at, updated_at,
	database_name, database_role`

func scanIntegration(row interface{ Scan(...any) error }) (*models.Integration, error) {
	var (
		m       models.Integration
		capsRaw []byte
		schema  []byte
	)
	err := row.Scan(&m.ID, &m.Kind, &m.Name, &m.Endpoint, &m.ModuleVersion,
		&capsRaw, &schema, &m.Status, &m.Enabled, &m.LastSeenAt, &m.RegisteredAt,
		&m.CreatedAt, &m.UpdatedAt, &m.DatabaseName, &m.DatabaseRole)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}

	if len(capsRaw) > 0 {
		_ = json.Unmarshal(capsRaw, &m.Capabilities)
	}
	// The settings schema is stored alongside the manifest but kept separate in
	// the row so that a module which predates it still registers.
	if len(schema) > 0 {
		var settings []models.SettingSpec
		if err := json.Unmarshal(schema, &settings); err == nil {
			m.Capabilities.Settings = settings
		}
	}
	return &m, nil
}

// Register adds a module or updates the one already registered under the same
// kind and name.
//
// Modules restart with a new address, and in Kubernetes that address changes
// every time, so registration is idempotent on (kind, name) rather than
// appending a new row each time.
func (r *IntegrationRepo) Register(ctx context.Context, kind, name, endpoint string, tokenHash []byte, manifest models.Manifest) (*models.Integration, error) {
	caps := manifest
	caps.Version = manifest.Version

	settings, err := json.Marshal(manifest.Settings)
	if err != nil {
		return nil, fmt.Errorf("encode module settings schema: %w", err)
	}
	capsRaw, err := json.Marshal(caps)
	if err != nil {
		return nil, fmt.Errorf("encode module manifest: %w", err)
	}

	integration := &models.Integration{}
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO integrations (kind, name, endpoint, token_hash, module_version,
			capabilities, settings_schema, status, enabled, last_seen_at, registered_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'online', TRUE, now(), now())
		ON CONFLICT (kind, name) DO UPDATE SET
			endpoint       = EXCLUDED.endpoint,
			token_hash     = EXCLUDED.token_hash,
			module_version = EXCLUDED.module_version,
			capabilities   = EXCLUDED.capabilities,
			settings_schema = EXCLUDED.settings_schema,
			status         = 'online',
			enabled        = TRUE,
			last_seen_at   = now(),
			registered_at  = now(),
			updated_at     = now()
		RETURNING `+integrationColumns,
		kind, name, endpoint, tokenHash, manifest.Version, capsRaw, settings,
	).Scan(&integration.ID, &integration.Kind, &integration.Name, &integration.Endpoint,
		&integration.ModuleVersion, &capsRaw, &settings, &integration.Status,
		&integration.Enabled, &integration.LastSeenAt, &integration.RegisteredAt,
		&integration.CreatedAt, &integration.UpdatedAt,
		&integration.DatabaseName, &integration.DatabaseRole)
	if err != nil {
		return nil, fmt.Errorf("register module: %w", err)
	}

	if err := json.Unmarshal(capsRaw, &integration.Capabilities); err == nil {
		var specs []models.SettingSpec
		if json.Unmarshal(settings, &specs) == nil {
			integration.Capabilities.Settings = specs
		}
	}
	return integration, nil
}

func (r *IntegrationRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Integration, error) {
	return scanIntegration(r.s.pool.QueryRow(ctx,
		`SELECT `+integrationColumns+` FROM integrations WHERE id = $1`, id))
}

// ByKind returns the enabled module of a kind, if there is one.
func (r *IntegrationRepo) ByKind(ctx context.Context, kind string) (*models.Integration, error) {
	return scanIntegration(r.s.pool.QueryRow(ctx,
		`SELECT `+integrationColumns+` FROM integrations WHERE kind = $1 AND enabled ORDER BY created_at LIMIT 1`,
		kind))
}

func (r *IntegrationRepo) List(ctx context.Context) ([]*models.Integration, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+integrationColumns+` FROM integrations ORDER BY kind, name`)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	defer rows.Close()

	out := []*models.Integration{}
	for rows.Next() {
		integration, err := scanIntegration(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, integration)
	}
	return out, rows.Err()
}

// Heartbeat records that a module is alive.
func (r *IntegrationRepo) Heartbeat(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE integrations SET last_seen_at = now(), updated_at = now(),
		        status = CASE WHEN enabled THEN 'online' ELSE 'disabled' END
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("module heartbeat: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *IntegrationRepo) SetEnabled(ctx context.Context, id uuid.UUID, enabled bool) error {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE integrations SET enabled = $2, updated_at = now(),
		       status = CASE WHEN $2 THEN 'online' ELSE 'disabled' END
		WHERE id = $1`, id, enabled)
	if err != nil {
		return fmt.Errorf("enable module: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *IntegrationRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM integrations WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete module: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkStaleOffline flips modules that stopped sending heartbeats.
//
// It runs on a timer in the web tier: the UI should not have to compute liveness,
// and Kubernetes readiness should reflect the same state.
func (r *IntegrationRepo) MarkStaleOffline(ctx context.Context, window time.Duration) ([]StaleModule, error) {
	if window <= 0 {
		window = heartbeatWindow
	}
	// Which modules went quiet is returned rather than just how many: the caller has
	// to say so, and a count would leave an administrator looking at a page that
	// changed without being told why.
	rows, err := r.s.pool.Query(ctx, `
		UPDATE integrations SET status = 'offline', updated_at = now()
		WHERE enabled
		  AND status = 'online'
		  AND (last_seen_at IS NULL OR last_seen_at < now() - $1::interval)
		RETURNING id, kind, name`, window)
	if err != nil {
		return nil, fmt.Errorf("mark stale modules: %w", err)
	}
	defer rows.Close()

	var stale []StaleModule
	for rows.Next() {
		var one StaleModule
		if err := rows.Scan(&one.ID, &one.Kind, &one.Name); err != nil {
			return nil, fmt.Errorf("scan a stale module: %w", err)
		}
		stale = append(stale, one)
	}
	return stale, rows.Err()
}

// StaleModule is a module that stopped sending heartbeats.
type StaleModule struct {
	ID   uuid.UUID
	Kind string
	Name string
}

// --- module tokens ------------------------------------------------------

type ModuleTokenRepo struct{ s *Store }

func (s *Store) ModuleTokens() *ModuleTokenRepo { return &ModuleTokenRepo{s: s} }

func (r *ModuleTokenRepo) Create(ctx context.Context, name, description string, hash []byte) (*models.ModuleToken, error) {
	token := &models.ModuleToken{Name: name, Description: description}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO module_tokens (name, description, token_hash)
		VALUES ($1, $2, $3) RETURNING id, created_at`,
		name, description, hash).Scan(&token.ID, &token.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create module token: %w", err)
	}
	return token, nil
}

// ByHash resolves a registration token.
func (r *ModuleTokenRepo) ByHash(ctx context.Context, hash []byte) (*models.ModuleToken, error) {
	var token models.ModuleToken
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, name, description, created_at, revoked_at
		FROM module_tokens WHERE token_hash = $1 AND revoked_at IS NULL`, hash,
	).Scan(&token.ID, &token.Name, &token.Description, &token.CreatedAt, &token.RevokedAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get module token: %w", err)
	}
	return &token, nil
}

func (r *ModuleTokenRepo) List(ctx context.Context) ([]*models.ModuleToken, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, name, description, created_at, revoked_at FROM module_tokens ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.ModuleToken{}
	for rows.Next() {
		var t models.ModuleToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.CreatedAt, &t.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, &t)
	}
	return out, rows.Err()
}

func (r *ModuleTokenRepo) Revoke(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE module_tokens SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke module token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- per-scope settings -------------------------------------------------

// SetSetting writes one setting at one scope. Re-registering a module never
// touches settings: they are configuration, not module state.
func (r *IntegrationRepo) SetSetting(ctx context.Context, integrationID uuid.UUID, scopeType string, scopeID *uuid.UUID, key string, value json.RawMessage) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (integration_id, scope_type, scope_id, key)
		DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
		integrationID, scopeType, scopeID, key, value)
	if err != nil {
		return fmt.Errorf("set module setting: %w", err)
	}
	return nil
}

// SettingsFor resolves the effective settings for a scope: instance defaults,
// overridden by the group, overridden by the project.
func (r *IntegrationRepo) SettingsFor(ctx context.Context, integrationID uuid.UUID, groupID, projectID *uuid.UUID) (map[string]json.RawMessage, error) {
	scopes := []struct {
		scopeType string
		scopeID   *uuid.UUID
	}{
		{ScopeInstance, nil},
		{"group", groupID},
		{"project", projectID},
	}

	out := map[string]json.RawMessage{}
	for _, scope := range scopes {
		if scope.scopeType != ScopeInstance && scope.scopeID == nil {
			continue
		}
		rows, err := r.s.pool.Query(ctx, `
			SELECT key, value FROM integration_settings
			WHERE integration_id = $1 AND scope_type = $2
			  AND scope_id IS NOT DISTINCT FROM $3`,
			integrationID, scope.scopeType, scope.scopeID)
		if err != nil {
			return nil, fmt.Errorf("read module settings: %w", err)
		}

		for rows.Next() {
			var (
				key   string
				value json.RawMessage
			)
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return nil, err
			}
			out[key] = value
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// SettingsAt returns the raw rows at one scope, for the settings UI.
func (r *IntegrationRepo) SettingsAt(ctx context.Context, integrationID uuid.UUID, scopeType string, scopeID *uuid.UUID) ([]models.IntegrationSetting, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, integration_id, scope_type, scope_id, key, value, updated_at
		FROM integration_settings
		WHERE integration_id = $1 AND scope_type = $2 AND scope_id IS NOT DISTINCT FROM $3
		ORDER BY key`, integrationID, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.IntegrationSetting{}
	for rows.Next() {
		var s models.IntegrationSetting
		if err := rows.Scan(&s.ID, &s.IntegrationID, &s.ScopeType, &s.ScopeID,
			&s.Key, &s.Value, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSettingAt removes one override, restoring the inherited value.
func (r *IntegrationRepo) DeleteSettingAt(ctx context.Context, integrationID uuid.UUID, scopeType string, scopeID *uuid.UUID, key string) error {
	tag, err := r.s.pool.Exec(ctx, `
		DELETE FROM integration_settings
		WHERE integration_id = $1 AND scope_type = $2 AND scope_id IS NOT DISTINCT FROM $3 AND key = $4`,
		integrationID, scopeType, scopeID, key)
	if err != nil {
		return fmt.Errorf("delete module setting: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Scope names re-exported from the model package so SQL helpers and handlers use
// the same constants.
const (
	ScopeInstance = models.ScopeInstance
	ScopeGroup    = models.ScopeGroup
	ScopeProject  = models.ScopeProject
)

// SetModuleDatabase records which database and role belong to a module.
//
// Only the names are stored. The password is returned once at provisioning and
// kept by the module in its own secret, so a leaked database dump cannot hand a
// module access to anything.
func (r *IntegrationRepo) SetModuleDatabase(ctx context.Context, id uuid.UUID, database, role string) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE integrations SET database_name = $2, database_role = $3, updated_at = now()
		 WHERE id = $1`, id, database, role)
	if err != nil {
		return fmt.Errorf("record the module database: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearModuleDatabase forgets the module's database, used after it is dropped.
func (r *IntegrationRepo) ClearModuleDatabase(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE integrations SET database_name = '', database_role = '', updated_at = now()
		 WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("clear the module database: %w", err)
	}
	return nil
}

// --- user tokens for modules --------------------------------------------

type IntegrationTokenRepo struct{ s *Store }

func (s *Store) IntegrationTokens() *IntegrationTokenRepo { return &IntegrationTokenRepo{s: s} }

func (r *IntegrationTokenRepo) Create(ctx context.Context, token *models.IntegrationToken, hash []byte) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO integration_tokens (integration_id, user_id, project_id, scopes, token_hash, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at`,
		token.IntegrationID, token.UserID, token.ProjectID, token.Scopes, hash, token.ExpiresAt,
	).Scan(&token.ID, &token.CreatedAt)
	if err != nil {
		return fmt.Errorf("create module token: %w", err)
	}
	return nil
}

// Introspect resolves a user token presented to a module.
func (r *IntegrationTokenRepo) Introspect(ctx context.Context, hash []byte) (*models.IntegrationToken, error) {
	var token models.IntegrationToken
	err := r.s.pool.QueryRow(ctx, `
		SELECT t.id, t.integration_id, t.user_id, t.project_id, t.scopes,
		       t.expires_at, t.created_at, t.last_used_at, i.enabled
		FROM integration_tokens t
		JOIN integrations i ON i.id = t.integration_id
		WHERE t.token_hash = $1`, hash,
	).Scan(&token.ID, &token.IntegrationID, &token.UserID, &token.ProjectID,
		&token.Scopes, &token.ExpiresAt, &token.CreatedAt, &token.LastUsedAt,
		&token.ModuleEnabled)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("introspect token: %w", err)
	}
	return &token, nil
}

func (r *IntegrationTokenRepo) TouchUsed(ctx context.Context, id int64) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE integration_tokens SET last_used_at = now() WHERE id = $1`, id)
	return err
}

// DeleteExpired clears tokens that can no longer authorise anything.
func (r *IntegrationTokenRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM integration_tokens WHERE expires_at <= now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// IntegrationByTokenHash resolves the module a request came from.
func (r *IntegrationRepo) ByTokenHash(ctx context.Context, hash []byte) (*models.Integration, error) {
	return scanIntegration(r.s.pool.QueryRow(ctx,
		`SELECT `+integrationColumns+` FROM integrations WHERE token_hash = $1`, hash))
}
