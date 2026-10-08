package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/secrets"
)

// RowIDField is the core's own field in a list whose entries have names: a row's
// identity, so that renaming a row overrides one field of it rather than making a
// second row of the same thing.
const RowIDField = "dogit_row_id"

// RowIDFor is the identity a row that has none is given.
//
// Derived from what names the row rather than from its position, so the same row is the
// same row on every read and in every place, including rows written before identities
// existed. A row that already carries one keeps it: the point of an identity is to
// survive the name changing, and recomputing it from the name would undo that.
func RowIDFor(integrationID uuid.UUID, name string) string {
	sum := sha256.Sum256([]byte("dogit-row:" + integrationID.String() + ":" + name))
	return hex.EncodeToString(sum[:8])
}

// stampRowIDs gives every row of a list whose entries have names an identity, if it does
// not have one already.
func stampRowIDs(specs []models.SettingSpec, integrationID uuid.UUID, key string, value json.RawMessage) json.RawMessage {
	var spec *models.SettingSpec
	for i := range specs {
		if specs[i].Key == key {
			spec = &specs[i]
			break
		}
	}
	if spec == nil || spec.Type != "list" || spec.Items == nil || len(spec.Items.Identify) == 0 {
		return value
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(value, &rows); err != nil {
		return value
	}
	changed := false
	for _, row := range rows {
		if _, has := row[RowIDField]; has {
			continue
		}
		name := ""
		for _, field := range spec.Items.Identify {
			raw, ok := row[field]
			if !ok {
				name = ""
				break
			}
			text := strings.Trim(string(raw), `"`)
			if name != "" {
				name += " "
			}
			name += text
		}
		row[RowIDField] = json.RawMessage(strconv.Quote(RowIDFor(integrationID, name)))
		changed = true
	}
	if !changed {
		return value
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return value
	}
	return encoded
}

// heartbeatWindow is how long a module may stay silent before it is considered
// offline. It is generous because a module restart in Kubernetes is not instant.
const heartbeatWindow = 90 * time.Second

type IntegrationRepo struct{ s *Store }

func (s *Store) Integrations() *IntegrationRepo { return &IntegrationRepo{s: s} }

const integrationColumns = `id, kind, name, endpoint, module_version, capabilities,
	settings_schema, status, enabled, last_seen_at, registered_at, created_at, updated_at`

func scanIntegration(row interface{ Scan(...any) error }) (*models.Integration, error) {
	var (
		m       models.Integration
		capsRaw []byte
		schema  []byte
	)
	err := row.Scan(&m.ID, &m.Kind, &m.Name, &m.Endpoint, &m.ModuleVersion,
		&capsRaw, &schema, &m.Status, &m.Enabled, &m.LastSeenAt, &m.RegisteredAt,
		&m.CreatedAt, &m.UpdatedAt)
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
		&integration.CreatedAt, &integration.UpdatedAt)
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
// open a stored value that is sealed, and hand back one that is not exactly as it is.
//
// Called on every value this store hands out rather than only on the ones somebody
// remembers to ask about. That is what makes it safe: a reader cannot get it wrong by
// forgetting which values are credentials, because it does not have to know.
func (r *IntegrationRepo) open(value json.RawMessage) (json.RawMessage, error) {
	if r.s.sealer == nil {
		if !sealedValue(value) {
			return value, nil
		}
		return nil, fmt.Errorf("%s: this value is sealed and this instance has no key to open it with",
			secrets.ErrNoKey)
	}
	plain, err := r.s.sealer.Open(value)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// Whether a stored value is a sealed one, without needing the key.
//
// The same test the sealer uses, so that "there is a sealed value here" can be answered on
// an instance that cannot open it — which is the whole of what that instance can say.
func sealedValue(value json.RawMessage) bool {
	var wrapper struct {
		Sealed string `json:"__sealed"`
		Box    string `json:"box"`
	}
	if err := json.Unmarshal(value, &wrapper); err != nil {
		return false
	}
	return wrapper.Sealed != "" && wrapper.Box != ""
}

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
//
// The specs are the module's own. They are needed because one kind of setting does not
// take overriding as replacement: a list whose entries have names inherits entry by
// entry, the way a module's rows do, and which lists those are is the module's business
// and not the core's to guess.
func (r *IntegrationRepo) SettingsFor(ctx context.Context, integrationID uuid.UUID, groupID, projectID *uuid.UUID, specs []models.SettingSpec) (map[string]json.RawMessage, error) {
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
			opened, err := r.open(value)
			if err != nil {
				rows.Close()
				return nil, fmt.Errorf("read module setting %s: %w", key, err)
			}
			value = stampRowIDs(specs, integrationID, key, opened)
			if inherited, ok := out[key]; ok {
				if merged, ok := mergeEntries(specs, key, inherited, value); ok {
					out[key] = merged
					continue
				}
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

// mergeEntries merges a lower scope's list over an inherited one, entry by entry, and
// says whether it did.
//
// It applies only where the module named what identifies an entry — a list of clusters
// is a list of places, each with a name, and a project that changes one cluster's
// namespace has not stopped using the other two. Any other list is replaced whole,
// because there is no telling which of its entries the smaller list was talking about,
// and a half-merged answer there would be a guess.
//
// The smaller scope names only the fields it means to change; the rest are inherited as
// they were. An entry whose name nobody gave is kept as its own rather than merged into
// anything, because an empty name is not a reference to a row that exists.
func mergeEntries(specs []models.SettingSpec, key string, inherited, override json.RawMessage) (json.RawMessage, bool) {
	var spec *models.SettingSpec
	for i := range specs {
		if specs[i].Key == key {
			spec = &specs[i]
			break
		}
	}
	if spec == nil || spec.Type != "list" || spec.Items == nil || len(spec.Items.Identify) == 0 {
		return nil, false
	}

	var base, over []map[string]any
	if err := json.Unmarshal(inherited, &base); err != nil {
		return nil, false
	}
	if err := json.Unmarshal(override, &over); err != nil {
		return nil, false
	}

	// A row is matched by the identity the core gave it, and by name only when it has
	// none: a name is a field a lower level may change, and a row that could only be
	// recognised by the name it used to have could never be renamed without becoming a
	// second row.
	idOf := func(entry map[string]any) string {
		text, _ := entry[RowIDField].(string)
		return text
	}
	nameOf := func(entry map[string]any) string {
		parts := make([]string, 0, len(spec.Items.Identify))
		for _, field := range spec.Items.Identify {
			text, _ := entry[field].(string)
			if text == "" {
				// Half a name is not a name. Merging on it would join two entries that
				// only appear to be the same one.
				return ""
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, " ")
	}

	merged := make([]map[string]any, len(base))
	for i, entry := range base {
		copied := make(map[string]any, len(entry))
		for field, value := range entry {
			copied[field] = value
		}
		merged[i] = copied
	}
	for _, entry := range over {
		name := nameOf(entry)
		id := idOf(entry)
		replaced := false
		if name != "" || id != "" {
			for _, existing := range merged {
				if (id != "" && idOf(existing) == id) || (name != "" && nameOf(existing) == name) {
					for field, value := range entry {
						// The identity is the row's, not this scope's edit of it. Two
						// scopes holding one row are one row: a row that overrode a
						// switch at project level took the place above's identity with
						// it, and from then on nothing could tell the two rows apart —
						// the name is not written down at that level (it is inherited),
						// so the next read had two rows of one place, one of them with
						// no name and therefore invisible on the page.
						if field == RowIDField {
							continue
						}
						existing[field] = value
					}
					replaced = true
					break
				}
			}
		}
		if !replaced {
			merged = append(merged, entry)
		}
	}

	out, err := json.Marshal(merged)
	if err != nil {
		return nil, false
	}
	return out, true
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
		opened, err := r.open(s.Value)
		if err != nil {
			return nil, fmt.Errorf("read module setting %s: %w", s.Key, err)
		}
		s.Value = opened
		out = append(out, s)
	}
	return out, rows.Err()
}

// SettingAt is one setting as it is stored at one scope.
//
// It returns nothing rather than an error when the setting was never set here,
// because "not set at this scope" is the normal answer for a value that is inherited
// — and the caller in this case is deciding whether to keep a masked value, which has
// nothing stored to keep in the common case.
func (r *IntegrationRepo) SettingAt(ctx context.Context, integrationID uuid.UUID,
	scopeType string, scopeID *uuid.UUID, key string) (json.RawMessage, error) {

	var value json.RawMessage
	err := r.s.pool.QueryRow(ctx, `
		SELECT value FROM integration_settings
		WHERE integration_id = $1 AND scope_type = $2
		  AND scope_id IS NOT DISTINCT FROM $3 AND key = $4`,
		integrationID, scopeType, scopeID, key).Scan(&value)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read setting %s: %w", key, err)
	}
	return r.open(value)
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
