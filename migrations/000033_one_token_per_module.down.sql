-- Two tokens per module again: one to register with, one to work with.
--
-- Puts the secret back on the module's row and removes what 33 added, so that going down and
-- coming up again works. That is the whole requirement, and it is worth being blunt about why the
-- first version of this file was wrong: it kept expires_at and integration_id on the way down, on
-- the grounds that a token with an end date is worth having whether or not a module is behind it.
--
-- That reasoning is what made the migration unusable. A rollback that leaves the migration's own
-- additions behind cannot be followed by re-applying it, so an operator who rolled back to fix
-- something found a database that could not be brought forward again without hand-editing it —
-- at the exact moment they were already dealing with a problem. Whether a column is worth keeping
-- is a question for a later migration, which can add it with data to preserve. It is not a
-- question this file gets to answer by making itself irreversible.
ALTER TABLE integrations ADD COLUMN IF NOT EXISTS token_hash BYTEA;

-- One row per module carries the credential, so the copy-back is one-to-one.
UPDATE integrations i
   SET token_hash = m.token_hash
  FROM module_tokens m
 WHERE m.integration_id = i.id
   AND i.token_hash IS NULL;

-- What 33 could not be restored: the "one credential per module" rule, and the index the lookup
-- behind it needs.
DROP INDEX IF EXISTS module_tokens_one_per_module;
DROP INDEX IF EXISTS module_tokens_by_module_idx;

-- Dropped rather than left: see above. The dates on the rows go with them, which is what rolling
-- back means.
ALTER TABLE module_tokens
    DROP COLUMN IF EXISTS integration_id,
    DROP COLUMN IF EXISTS expires_at;
