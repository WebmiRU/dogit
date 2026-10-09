-- Core identity, membership and repository metadata.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    username      TEXT NOT NULL,
    email         TEXT NOT NULL,
    name          TEXT NOT NULL DEFAULT '',
    password_hash BYTEA NOT NULL,
    is_admin      BOOLEAN NOT NULL DEFAULT FALSE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_sign_in  TIMESTAMPTZ
);
CREATE UNIQUE INDEX users_username_key ON users (lower(username));
CREATE UNIQUE INDEX users_email_key ON users (lower(email));

CREATE TABLE ssh_keys (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title        TEXT NOT NULL,
    fingerprint  TEXT NOT NULL,
    public_key   TEXT NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_used_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX ssh_keys_fingerprint_key ON ssh_keys (fingerprint);
CREATE INDEX ssh_keys_user_id_idx ON ssh_keys (user_id);

CREATE TABLE sessions (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_seen_at TIMESTAMPTZ,
    user_agent  TEXT NOT NULL DEFAULT '',
    ip          TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id_idx ON sessions (user_id);

CREATE TABLE personal_access_tokens (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    token_hash   BYTEA NOT NULL,
    scopes       TEXT[] NOT NULL DEFAULT '{}',
    expires_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ,
    revoked_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX personal_access_tokens_hash_key ON personal_access_tokens (token_hash);
CREATE INDEX personal_access_tokens_user_id_idx ON personal_access_tokens (user_id);

CREATE TABLE groups (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug       TEXT NOT NULL,
    name       TEXT NOT NULL,
    full_path  TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX groups_full_path_key ON groups (full_path);

CREATE TABLE group_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id   UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (group_id, user_id)
);

CREATE TABLE group_roles (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id         UUID NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    min_access_level  SMALLINT NOT NULL,
    max_access_level  SMALLINT NOT NULL,
    source_user_id   UUID REFERENCES users(id) ON DELETE CASCADE,
    source_group_id  UUID REFERENCES groups(id) ON DELETE CASCADE,
    CONSTRAINT group_roles_source CHECK (source_user_id IS NOT NULL OR source_group_id IS NOT NULL)
);
CREATE INDEX group_roles_group_id_idx ON group_roles (group_id);

CREATE TABLE projects (
    id                    UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    group_id              UUID REFERENCES groups(id) ON DELETE SET NULL,
    path                  TEXT NOT NULL,
    name                  TEXT NOT NULL,
    description           TEXT NOT NULL DEFAULT '',
    visibility            TEXT NOT NULL DEFAULT 'private',
    default_branch        TEXT NOT NULL DEFAULT 'main',
    allow_pipeline_trigger BOOLEAN NOT NULL DEFAULT TRUE,
    allow_merge            BOOLEAN NOT NULL DEFAULT TRUE,
    merge_method           TEXT NOT NULL DEFAULT 'merge',
    remove_source_branch   BOOLEAN NOT NULL DEFAULT TRUE,
    public_emails          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at           TIMESTAMPTZ
);
CREATE UNIQUE INDEX projects_path_key ON projects (path);
CREATE INDEX projects_group_id_idx ON projects (group_id);

CREATE TABLE project_members (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, user_id)
);

CREATE TABLE project_roles (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id       UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name             TEXT NOT NULL,
    min_access_level SMALLINT NOT NULL,
    max_access_level SMALLINT NOT NULL,
    source_user_id   UUID REFERENCES users(id) ON DELETE CASCADE,
    source_group_id  UUID REFERENCES groups(id) ON DELETE CASCADE,
    CONSTRAINT project_roles_source CHECK (source_user_id IS NOT NULL OR source_group_id IS NOT NULL)
);
CREATE INDEX project_roles_project_id_idx ON project_roles (project_id);

-- Commits created inside a project, produced by the post-receive hook.
CREATE TABLE commits (
    sha             TEXT NOT NULL,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    ref             TEXT NOT NULL,
    branch          TEXT NOT NULL DEFAULT '',
    author_name     TEXT NOT NULL DEFAULT '',
    author_email    TEXT NOT NULL DEFAULT '',
    committer_name  TEXT NOT NULL DEFAULT '',
    committer_email TEXT NOT NULL DEFAULT '',
    message         TEXT NOT NULL DEFAULT '',
    timestamp       TIMESTAMPTZ NOT NULL DEFAULT now(),
    added           INTEGER NOT NULL DEFAULT 0,
    removed         INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (project_id, sha)
);
CREATE INDEX commits_project_branch_idx ON commits (project_id, branch, timestamp DESC);