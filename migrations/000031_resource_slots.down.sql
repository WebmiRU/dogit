-- The slot is dropped and the index that forbade a module several resources comes back.
--
-- Restoring the index is a choice worth naming: it makes the database refuse a state the
-- product now allows, so anything written while this migration was applied down — a module
-- with two resources — cannot be inserted again. It is here because a down migration that
-- silently permits what the up migration forbade is worse than one that refuses, and refusing
-- is visible.
-- Statements of their own rather than ALTER TABLE ... DROP INDEX, which is a shape Postgres
-- refuses: IF EXISTS is not offered there. The up migration says more about why.
DROP INDEX IF EXISTS resources_by_module_idx;
DROP INDEX IF EXISTS resources_awaiting_idx;

CREATE UNIQUE INDEX IF NOT EXISTS resources_one_per_module
    ON resources (integration_id)
    WHERE integration_id IS NOT NULL;

-- The slot names are cleared before the column goes, because a module whose resource said
-- which of its needs it answered cannot have that answer kept in a shape that no longer
-- exists: `need_key` is what says a module holds several resources at all.
UPDATE resources SET need_key = '';
ALTER TABLE resources DROP COLUMN IF EXISTS need_key;