-- Forge modules: separate services that register with the core and work with its
-- users and permissions.
--
-- The core owns identity and authorisation. A module never stores its own copy
-- of users; it asks the core who a caller is and what they may do, or presents a
-- token the core minted.

CREATE TABLE module_tokens (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    -- Only the hash is stored: a leaked database row must not yield a token.
    token_hash  BYTEA NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at  TIMESTAMPTZ
);
CREATE UNIQUE INDEX module_tokens_hash_key ON module_tokens (token_hash);

CREATE TABLE integrations (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- What the module does: "registry:docker", "cache:npm", "builder:docker".
    kind          TEXT NOT NULL,
    name          TEXT NOT NULL,
    -- Where the core reaches the module. In Kubernetes this is a service DNS
    -- name, so it changes on every restart and cannot live in configuration.
    endpoint      TEXT NOT NULL,
    -- Bearer token the module presents on every call.
    token_hash    BYTEA NOT NULL,
    module_version TEXT NOT NULL DEFAULT '',
    -- What the module claims it can do. The admin UI renders only these, so an
    -- older module never shows settings it does not implement.
    capabilities  JSONB NOT NULL DEFAULT '{}',
    settings_schema JSONB NOT NULL DEFAULT '{}',
    status        TEXT NOT NULL DEFAULT 'pending',
    enabled       BOOLEAN NOT NULL DEFAULT TRUE,
    last_seen_at  TIMESTAMPTZ,
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One module of a kind per name; re-registering replaces the endpoint.
    UNIQUE (kind, name)
);
CREATE INDEX integrations_status_idx ON integrations (status, kind);

-- Per-scope configuration: instance-wide, per group, per project. Settings
-- cascade, so a project only stores what it overrides.
CREATE TABLE integration_settings (
    id             BIGSERIAL PRIMARY KEY,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    scope_type     TEXT NOT NULL,  -- instance | group | project
    scope_id       UUID,           -- NULL for the instance scope
    key            TEXT NOT NULL,
    value          JSONB NOT NULL DEFAULT 'null',
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (integration_id, scope_type, scope_id, key),
    CONSTRAINT integration_settings_scope CHECK (
        (scope_type = 'instance' AND scope_id IS NULL)
        OR (scope_type IN ('group', 'project') AND scope_id IS NOT NULL)
    )
);
CREATE INDEX integration_settings_scope_idx ON integration_settings (scope_type, scope_id);

-- Short-lived tokens the core mints for a user to act on a module, such as the
-- tokens a docker client presents to the registry.
CREATE TABLE integration_tokens (
    id             BIGSERIAL PRIMARY KEY,
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    user_id        UUID REFERENCES users(id) ON DELETE CASCADE,
    project_id     UUID REFERENCES projects(id) ON DELETE CASCADE,
    -- What the token allows: ["registry:pull", "registry:push"].
    scopes         TEXT[] NOT NULL DEFAULT '{}',
    token_hash     BYTEA NOT NULL,
    expires_at     TIMESTAMPTZ NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at   TIMESTAMPTZ
);
CREATE INDEX integration_tokens_hash_idx ON integration_tokens (token_hash);
CREATE INDEX integration_tokens_integration_idx ON integration_tokens (integration_id, expires_at);
