-- The parts become one sealed address again.
--
-- Cannot be undone for the data: a host, a database and a user that were columns are folded
-- back into a string inside the sealed envelope, which requires the core to assemble a
-- connection string — the thing this migration was written to stop doing. The assembly below
-- covers PostgreSQL and MySQL and nothing else, and an object store is left with an envelope
-- holding its secret key and nothing to go with it.
--
-- Kept because a down migration that cannot be run is worse than one that can be run badly: it
-- says plainly that going back here loses something, and what.
UPDATE resources SET secret = CASE
    WHEN kind = 'db' THEN convert_to(
        'postgres://' || username || ':' || COALESCE(convert_from(secret, 'UTF8'), '') ||
        '@' || host || ':' || COALESCE(port, 5432)::text || '/' || database_name,
        'UTF8')
    ELSE secret
END
WHERE kind = 'db';

ALTER TABLE resources ADD COLUMN IF NOT EXISTS address BYTEA NOT NULL DEFAULT ''::BYTEA;
UPDATE resources SET address = secret;

ALTER TABLE resources DROP COLUMN IF EXISTS secret;
ALTER TABLE resources DROP COLUMN IF EXISTS access_key;
ALTER TABLE resources DROP COLUMN IF EXISTS bucket;
ALTER TABLE resources DROP COLUMN IF EXISTS region;
ALTER TABLE resources DROP COLUMN IF EXISTS endpoint;
ALTER TABLE resources DROP COLUMN IF EXISTS username;
ALTER TABLE resources DROP COLUMN IF EXISTS database_name;
ALTER TABLE resources DROP COLUMN IF EXISTS port;
ALTER TABLE resources DROP COLUMN IF EXISTS host;

DROP INDEX IF EXISTS resources_identity_idx;