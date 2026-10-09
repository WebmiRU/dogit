-- Puts the clusters back the way they were, as one settings key per scope.
--
-- The rows go first and the setting is rebuilt from the rows that were not overrides,
-- since a row that overrode another was that cluster changed rather than a second
-- cluster. A project that had its own list gets its own list again; a project that
-- had none gets nothing, which is where it started.
--
-- What this cannot bring back is a level that had *changed* an inherited cluster
-- rather than added one. The old form could not express that — a project's list was a
-- whole list, and the only way to change one cluster was to write all of them out
-- again — so there is nothing here that was lost by dropping it, and a downgrade is
-- not the place to start keeping such a record.
--
-- In reverse: the setting is deleted before the rows go, so the two never both exist
-- and a failure in between leaves the rows alone rather than a list nobody reads.

CREATE TEMPORARY TABLE plain_rows AS
SELECT
	integration_id,
	scope_type,
	scope_id,
	jsonb_agg(values ORDER BY position, created_at) AS clusters
FROM module_targets
WHERE overrides IS NULL
	AND integration_id IN (SELECT id FROM integrations WHERE kind LIKE 'deploy:%')
GROUP BY integration_id, scope_type, scope_id;

INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
SELECT
	integration_id,
	scope_type,
	scope_id,
	'clusters',
	clusters
FROM plain_rows
WHERE jsonb_array_length(clusters) > 0;

DELETE FROM module_targets WHERE integration_id IN (SELECT id FROM integrations WHERE kind LIKE 'deploy:%');