-- The clusters become rows.
--
-- They were one settings key holding a list, which cannot express the three things
-- that actually matter about them: they are written at a level, they are inherited
-- from the levels above, and a project may be allowed one and not another. A project
-- that wanted to stop deploying somewhere had no way to say so — switching off
-- autodeploy turned off every place at once, and deleting the block lost the
-- configuration and the row with it.
--
-- So each cluster becomes a row at the level that wrote it, with the same values, and
-- the setting goes away. Nothing is invented: the rows are the list, cut into pieces,
-- which is what a list of clusters always was.

-- One row per cluster, at the level whose list it was in.
--
-- The source is the settings rows rather than anything on the integration, because
-- settings live per scope and a project may have had a list of its own: read from the
-- integration, a project's clusters would have been taken from the instance's and
-- written at the instance level, which is a different place than where they were said.
CREATE TEMPORARY TABLE cluster_rows AS
SELECT
	gen_random_uuid() AS id,
	setting.integration_id,
	setting.scope_type,
	setting.scope_id,
	entry.ordinality AS position,
	entry.value AS value
FROM integration_settings setting
CROSS JOIN LATERAL jsonb_array_elements(
	CASE
		WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value
		ELSE '[]'::jsonb
	END
) WITH ORDINALITY AS entry(value, ordinality)
WHERE setting.key = 'clusters';

-- A cluster with no name is a row somebody started filling in, and a row a project may
-- be switched off is not the same thing as a row that was never a place. The module
-- already skipped these; nothing that existed is lost by skipping them here too.
DELETE FROM cluster_rows WHERE NULLIF(trim(both ' ' from value ->> 'name'), '') IS NULL;

-- Values as the row holds them: the whole cluster, so the module reads the name out of
-- it like any other value and nothing about the shape of a row is special-cased here.
INSERT INTO module_targets
	(id, integration_id, scope_type, scope_id, label, enabled, position, overrides, values)
SELECT
	id,
	integration_id,
	scope_type,
	scope_id,
	-- A name a person can read in a list of rows, and one nobody resolves through: a
	-- repository names a cluster in `cluster:`, not in a title somebody may change.
	trim(both ' ' from value ->> 'name'),
	-- NULL, not true: this level never said anything about whether the row is on, and
	-- a column that cannot tell "left alone" from "switched on" turns every inherited
	-- row into a decision nobody made.
	NULL,
	position,
	NULL,
	value
FROM cluster_rows;

-- The same cluster written at two levels is one row and a change to it, not two rows
-- with the same name, decided by the name because the name is what a repository writes
-- in `cluster:` and so what makes two mentions the same mention. The least specific row
-- is the root and each row below it overrides that root, which is the shape the core
-- already resolves: the row keeps its place in the list it came from, and the level
-- that wrote it keeps the claim on it.
WITH depth AS (
	SELECT
		id,
		integration_id,
		trim(both ' ' from value ->> 'name') AS name,
		CASE scope_type WHEN 'instance' THEN 0 WHEN 'group' THEN 1 ELSE 2 END AS level
	FROM cluster_rows
),
root AS (
	SELECT DISTINCT ON (integration_id, name) integration_id, name, id
	FROM depth
	ORDER BY integration_id, name, level
)
UPDATE module_targets row
SET overrides = root.id
FROM depth, root
WHERE row.id = depth.id
	AND root.integration_id = depth.integration_id
	AND root.name = depth.name
	AND root.id <> depth.id;

-- And the list itself goes, so there is one place a cluster is written and not two
-- that can disagree. The rows above are a copy of it; leaving the key would mean the
-- next edit of the setting lands where nobody looks.
DELETE FROM integration_settings WHERE key = 'clusters';