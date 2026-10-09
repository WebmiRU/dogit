-- Resources come back.
--
-- Not as what they were. The table that comes back has the column list of migration 28 — the
-- address sealed together with the password, one row per resource, one module holding at most
-- one — because that is the shape this instance was on when it went forward, and a down
-- migration that reconstructs a different table is not a way back.
--
-- The parts added in 30 and the slot in 31 are not reconstructed: they are in the file below
-- as a comment rather than as columns, because a resource written down between 32 and this
-- undo carries its parts, and a schema that accepts the old shape and silently drops what it
-- did not know about is the worst of both. Restore from a dump taken before 32 instead.
CREATE TABLE IF NOT EXISTS resources (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    kind TEXT NOT NULL DEFAULT '',
    software TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    origin TEXT NOT NULL DEFAULT 'managed',
    address BYTEA NOT NULL DEFAULT ''::BYTEA,
    integration_id UUID REFERENCES integrations(id) ON DELETE SET NULL,
    released_at TIMESTAMPTZ,
    last_integration_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS resources_one_per_module
    ON resources (integration_id)
    WHERE integration_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS resources_free_idx
    ON resources (created_at DESC)
    WHERE integration_id IS NULL;

-- Migration 29 added last_integration_kind, which the table above leaves out. It is a plain
-- column with a default, so nothing depends on it and restoring it costs nothing.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS last_integration_kind TEXT NOT NULL DEFAULT '';

ALTER TABLE integrations ADD COLUMN IF NOT EXISTS database_name TEXT NOT NULL DEFAULT '';
ALTER TABLE integrations ADD COLUMN IF NOT EXISTS database_role TEXT NOT NULL DEFAULT '';