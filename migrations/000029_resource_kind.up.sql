-- Which kind of module a released resource belonged to.
--
-- `last_integration_id` is not enough, and the failure is quiet: a resource left behind names
-- the module that had it, and removing that module deletes the row the name pointed at. After
-- that there is nothing to compare against, and the one case this column exists for — a module
-- of the same kind coming back and being given the database it had, with its data in it —
-- becomes the one case that never fires.
--
-- The kind rather than the id, then, because the kind outlives the instance: a module is
-- removed and reinstalled, and it is `deploy:kubernetes` that comes back, not a uuid.
--
-- Left empty for a resource that was never given to anybody. That is not the same as a
-- resource whose owner is forgotten, and the difference matters: an unowned resource is not
-- handed to whatever module arrives next.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS last_integration_kind TEXT NOT NULL DEFAULT '';

-- The reinstall path reads by this: free resources of one kind, newest first.
CREATE INDEX IF NOT EXISTS resources_by_kind_idx
    ON resources (last_integration_kind, created_at DESC)
    WHERE integration_id IS NULL;