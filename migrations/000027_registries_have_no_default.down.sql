-- Puts the flag back, for going back rather than for having it.
--
-- Whether it should ever be marked as the default again is a question this instance has
-- already answered once by ignoring it; restoring the column is not an answer, it is
-- only the possibility of one.
ALTER TABLE docker_registries ADD COLUMN IF NOT EXISTS is_default BOOLEAN NOT NULL DEFAULT FALSE;

CREATE UNIQUE INDEX IF NOT EXISTS docker_registries_default_key
    ON docker_registries (is_default) WHERE is_default;
