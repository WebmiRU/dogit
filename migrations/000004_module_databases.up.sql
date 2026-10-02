-- Per-module databases inside the shared PostgreSQL cluster.
--
-- Modules share one cluster but never one database: each gets its own database
-- and its own role, so a module cannot reach the application's tables. The
-- password is never stored here — it is returned once at provisioning and lives
-- in the module's own secret.

ALTER TABLE integrations
    ADD COLUMN database_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN database_role TEXT NOT NULL DEFAULT '';

COMMENT ON COLUMN integrations.database_name IS
    'Name of the module database inside the shared cluster; empty when none was provisioned.';
COMMENT ON COLUMN integrations.database_role IS
    'Database role that owns the module database; empty when none was provisioned.';
