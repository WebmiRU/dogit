-- Where a build pushes to an address an administrator wrote down, and what it
-- authenticates with.
--
-- A registry module mints its own tokens and names its own images. An address written
-- down here is neither: it authenticates with an account this instance holds, and names
-- an image by host and project path. Both are a registry, and until this table the core
-- could only ever see the first kind.
--
-- Three scopes, in the order the module settings already use: the instance says what is
-- true everywhere, a group says what is true inside it, a project says what is true for
-- it alone. A row carries only what it changes rather than a whole credential, so a group
-- can name a different account without restating the address, and a project can change
-- the password of an account it inherited and leave the login alone.
--
-- An empty column is not a value: it means "whatever the scope above me said", which is
-- what lets a row override one field and not three. A column set to the empty string at
-- the instance level is the absence of a decision, not a decision to have no login.
CREATE TABLE docker_registry_credentials (
    registry_id       UUID NOT NULL REFERENCES docker_registries(id) ON DELETE CASCADE,
    scope_type        TEXT NOT NULL,  -- instance | group | project
    scope_id          UUID,           -- NULL for the instance scope
    credential_source TEXT NOT NULL DEFAULT '',  -- '' inherits; static | user
    login             TEXT NOT NULL DEFAULT '',
    password          TEXT NOT NULL DEFAULT '',
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT docker_registry_credentials_unique UNIQUE (registry_id, scope_type, scope_id),
    CONSTRAINT docker_registry_credentials_scope CHECK (
        (scope_type = 'instance' AND scope_id IS NULL)
        OR (scope_type IN ('group', 'project') AND scope_id IS NOT NULL)
    ),
    CONSTRAINT docker_registry_credentials_source CHECK (
        credential_source IN ('', 'static', 'user')
    )
);

CREATE INDEX docker_registry_credentials_scope_idx
    ON docker_registry_credentials (registry_id, scope_type, scope_id);

-- The source beside the account it is about, at the instance level, so a registry that
-- has never been overridden still says how it is used.
ALTER TABLE docker_registries
    ADD COLUMN credential_source TEXT NOT NULL DEFAULT 'static';

ALTER TABLE docker_registries
    ADD CONSTRAINT docker_registries_credential_source CHECK (credential_source IN ('static', 'user'));

-- A registry already written down pushes with the account on its own row, which is what
-- a static credential is and what it did before this column said so. The default is
-- therefore 'static' and not '' : an address nobody has thought about is not an address
-- that inherits its meaning from somewhere that does not exist.
