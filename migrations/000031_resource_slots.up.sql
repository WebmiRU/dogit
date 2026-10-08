-- A resource belongs to one module and to one slot of it.
--
-- The index this drops enforced the other half of that sentence, and the other half is not a
-- rule: it said a module may hold at most one resource. So a registry that needs a database for
-- its tags and an object store for its blobs could not ask for both, and the only way to give a
-- module a second resource was to take the first away — which is a worse answer than doing
-- nothing, because taking away a database a module needs leaves a module that runs and quietly
-- forgets everything it deployed.
--
-- Nothing replaces it, and that is deliberate rather than a gap. A resource row has one
-- integration_id, so a row cannot belong to two modules no matter what the database allows,
-- and sharing was never possible without this index. It only looked like it was guarded.
--
-- What is added instead is the slot. A module may hold several resources, so which of its
-- needs a given resource answers has to be written down rather than inferred from there being
-- at most one of them.
-- A statement of its own rather than `ALTER TABLE resources DROP INDEX`, which is a shape
-- Postgres does not accept: IF EXISTS belongs to DROP INDEX and to DROP COLUMN, and an ALTER
-- TABLE taking a DROP INDEX wants the bare name — so the safe form is refused as a syntax
-- error and the index stays, which is the worst outcome for a migration whose whole job is to
-- remove it.
DROP INDEX IF EXISTS resources_one_per_module;

-- Which need of the holding module this resource answers, by the key the module named it.
--
-- Null for a resource nobody holds, and for one held by a module that asked only the old
-- `database: true` and therefore has no slot names to record. Not unique: two needs of one
-- module may name the same kind and be told apart only by their keys, and two needs may
-- legitimately be answered by the same resource when a module declares the same thing twice.
ALTER TABLE resources ADD COLUMN IF NOT EXISTS need_key TEXT NOT NULL DEFAULT '';

-- A module holding several resources, and the free ones it could take. Both are ordered by
-- creation, which is the order they were written in and therefore the order somebody reading
-- a list of them expects.
CREATE INDEX IF NOT EXISTS resources_by_module_idx
    ON resources (integration_id, need_key)
    WHERE integration_id IS NOT NULL;

-- The resources nobody holds that were described for a kind of module. The page of a module's
-- own resources reads this to offer something to attach, so it is an index rather than a scan
-- over every resource that happens to be free.
CREATE INDEX IF NOT EXISTS resources_awaiting_idx
    ON resources (last_integration_kind, created_at DESC)
    WHERE integration_id IS NULL AND last_integration_kind <> '';