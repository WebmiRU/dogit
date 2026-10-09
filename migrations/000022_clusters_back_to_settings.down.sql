-- Puts the clusters back into rows, one per place, at the level that wrote each of them.
--
-- And the project's brake away again: with a switch on every place there is no need for
-- one that stops everything, and stopping everything is a coarser thing to have to
-- explain.
--
-- Only the lists are moved back. A project's override of an inherited cluster is not
-- restored, because there was no way to write one in the first place — see the migration
-- that went the other way.

WITH rows AS (
	SELECT
		gen_random_uuid() AS id,
		setting.integration_id,
		setting.scope_type,
		setting.scope_id,
		entry.ordinality AS position,
		entry.value AS value
	FROM integration_settings setting
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS entry(value, ordinality)
	WHERE setting.key = 'clusters'
)
INSERT INTO module_targets
	(id, integration_id, scope_type, scope_id, label, enabled, position, overrides, values)
SELECT
	id, integration_id, scope_type, scope_id,
	trim(both ' ' from value ->> 'name'), NULL, position, NULL, value
FROM rows
WHERE NULLIF(trim(both ' ' from value ->> 'name'), '') IS NOT NULL;

DELETE FROM integration_settings WHERE key = 'clusters';

ALTER TABLE projects DROP COLUMN IF EXISTS auto_deploy_paused;