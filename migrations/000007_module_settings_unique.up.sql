-- One row per setting, including at the instance scope.
--
-- The old constraint listed scope_id, which is NULL for the instance scope, and
-- in PostgreSQL NULLs are distinct in a unique index: every write of an
-- instance-wide setting therefore inserted a new row instead of replacing the old
-- one. Reading took the newest, so the effect was invisible — until something
-- counted the rows.

-- Keep the newest of any duplicates that accumulated, then say plainly what is
-- being collapsed: these are settings, and the last write is the one that counts.
DELETE FROM integration_settings older
USING integration_settings newer
WHERE older.integration_id = newer.integration_id
  AND older.scope_type = newer.scope_type
  AND older.scope_id IS NOT DISTINCT FROM newer.scope_id
  AND older.key = newer.key
  AND older.id < newer.id;

ALTER TABLE integration_settings
    DROP CONSTRAINT IF EXISTS integration_settings_integration_id_scope_type_scope_id_key_key;

ALTER TABLE integration_settings
    ADD CONSTRAINT integration_settings_one_per_scope
    UNIQUE NULLS NOT DISTINCT (integration_id, scope_type, scope_id, key);
