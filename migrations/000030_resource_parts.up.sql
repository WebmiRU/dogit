-- A resource is written down as the parts it is made of, rather than as one address.
--
-- The address was a string an administrator had to assemble by hand: a database is a host, a
-- port, a database, a user and a password, and "postgres://deploy:hunter2@db.example.com:5432/deploy"
-- asks one person to get five things right and to encode all of them in one field where a typo
-- is not caught until a module tries to connect and says something about a socket.
--
-- The parts are columns, not one JSON blob, and not for tidiness. There are a handful of them,
-- they are known, and each is queried: the page lists where each resource lives, and the
-- duplicate check asks whether this place, this user and this database are already written down.
-- A blob would make all of that a parse inside application code, in the one place where a
-- mistake is hardest to see, and would put the shape of a resource in a string nobody can
-- constrain.
--
-- The unused ones are empty rather than absent, for the same reason the table has defaults: a
-- row for an object store has no `database_name`, and saying so with NULL would only mean that
-- every read has to distinguish NULL from '' and one of the two is always wrong.
--
-- `address` becomes `secret`: what was in it was the password, and only the password. It is
-- sealed, it is now named for what it is, and it holds whichever secrets this kind has — a
-- database's password, an object store's secret key. A module that wants both gets both; one
-- that wants neither is asked for neither.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS host TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS port INTEGER;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS database_name TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS username TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS endpoint TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS region TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS bucket TEXT NOT NULL DEFAULT '';
ALTER TABLE resources ADD COLUMN IF NOT EXISTS access_key TEXT NOT NULL DEFAULT '';

ALTER TABLE resources ADD COLUMN IF NOT EXISTS secret BYTEA NOT NULL DEFAULT ''::BYTEA;

-- The old address column carried the sealed password together with the facts, and the facts
-- are columns now. Nothing reads it and nothing writes it; it is dropped rather than left, so
-- that there is exactly one place a password is kept and no column that looks like a second.
ALTER TABLE resources DROP COLUMN IF EXISTS address;

-- Two records naming the same place, user and database are the same resource written down
-- twice. Previously the check could not be an index at all — every address is sealed with a
-- fresh nonce, so no two of them are byte-identical and the comparison had to open every row on
-- the instance to compare them in the clear. That was correct and it scaled with the size of
-- the instance rather than the size of the table.
--
-- Partial on the kind, so that a database and an object store that happen to share a host do
-- not collide. The key is the identity and not the password: a record whose password has gone
-- stale is still a record of the same database, and refusing it would leave somebody unable to
-- describe a database they have rather than holding one they do not.
--
-- Not unique across free resources alone. A held resource and a described one naming the same
-- place are exactly the situation this table exists for: the second is waiting for the module
-- that the first was given to.
CREATE INDEX IF NOT EXISTS resources_identity_idx
    ON resources (kind, host, port, database_name, username, endpoint, region, bucket, access_key);