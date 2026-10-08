-- The column goes, and the index with it.
--
-- Nothing else reads it: `last_integration_id` still records which module held a resource, and
-- a downgrade finds a table with one more column than it expects rather than one that is
-- missing something it asked for.
DROP INDEX IF EXISTS resources_by_kind_idx;

ALTER TABLE resources DROP COLUMN IF EXISTS last_integration_kind;