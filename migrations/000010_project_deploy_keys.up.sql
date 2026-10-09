-- Keys that may reach a project without being one of its members.
--
-- A runner is a machine. Giving it somebody's account would be both wrong and
-- useless — the account belongs to a human whose rights change for reasons that
-- have nothing to do with a build — so a build gets a key of its own that says
-- what it may do and nothing else.

CREATE TABLE project_deploy_keys (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    public_key  TEXT NOT NULL,
    -- A key that may only read cannot be blamed for a push it could not make, and a
    -- runner that only builds and pulls has no business being able to write.
    can_push    BOOLEAN NOT NULL DEFAULT FALSE,
    -- A key handed to a job is good for that job. Expiry is checked on use as well
    -- as by cleanup: a key past its moment is refused even while the row remains.
    expires_at  TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One key by fingerprint across the instance: a connection that named one is
-- resolved before anybody says which project it wanted.
CREATE UNIQUE INDEX project_deploy_keys_fingerprint_idx ON project_deploy_keys (fingerprint);
CREATE INDEX project_deploy_keys_project_idx ON project_deploy_keys (project_id);
