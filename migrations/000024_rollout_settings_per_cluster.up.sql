-- The rollout timeout and "keep finished jobs" move into the cluster.
--
-- Both were one setting for the whole module, which is the wrong size for either. Two
-- clusters of one installation are two machines, and one of them rolling out in twenty
-- seconds while the other takes four minutes is normal; a single number is either an
-- endless wait on the fast one or a rollout declared stuck on the slow one. Keeping the
-- finished Jobs is the same size of mistake: leaving yesterday's migrations in staging to
-- read them and leaving them in production are two different wants.
--
-- The values already written down are not thrown away: each cluster that does not have
-- one of these of its own is given what the module said for all of them, so nothing
-- changes for an installation that had only one opinion.

-- The timeout.
WITH module_settings AS (
	SELECT integration_id, scope_type, scope_id, value
	FROM integration_settings
	WHERE key = 'default_rollout_timeout'
), widened AS (
	SELECT
		setting.integration_id,
		setting.scope_type,
		setting.scope_id,
		jsonb_agg(
			CASE WHEN cluster.value ? 'rollout_timeout' THEN cluster.value
				ELSE cluster.value || jsonb_build_object('rollout_timeout', module_settings.value)
			END
			ORDER BY cluster.ordinality
		) AS clusters
	FROM integration_settings setting
	JOIN module_settings
		ON module_settings.integration_id = setting.integration_id
		AND module_settings.scope_type = setting.scope_type
		AND module_settings.scope_id IS NOT DISTINCT FROM setting.scope_id
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS cluster(value, ordinality)
	WHERE setting.key = 'clusters'
	GROUP BY setting.integration_id, setting.scope_type, setting.scope_id
)
UPDATE integration_settings setting
SET value = widened.clusters, updated_at = now()
FROM widened
WHERE setting.integration_id = widened.integration_id
	AND setting.scope_type = widened.scope_type
	AND setting.scope_id IS NOT DISTINCT FROM widened.scope_id
	AND setting.key = 'clusters';

-- The finished Jobs, the same way.
WITH module_settings AS (
	SELECT integration_id, scope_type, scope_id, value
	FROM integration_settings
	WHERE key = 'keep_jobs'
), widened AS (
	SELECT
		setting.integration_id,
		setting.scope_type,
		setting.scope_id,
		jsonb_agg(
			CASE WHEN cluster.value ? 'keep_jobs' THEN cluster.value
				ELSE cluster.value || jsonb_build_object('keep_jobs', module_settings.value)
			END
			ORDER BY cluster.ordinality
		) AS clusters
	FROM integration_settings setting
	JOIN module_settings
		ON module_settings.integration_id = setting.integration_id
		AND module_settings.scope_type = setting.scope_type
		AND module_settings.scope_id IS NOT DISTINCT FROM setting.scope_id
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS cluster(value, ordinality)
	WHERE setting.key = 'clusters'
	GROUP BY setting.integration_id, setting.scope_type, setting.scope_id
)
UPDATE integration_settings setting
SET value = widened.clusters, updated_at = now()
FROM widened
WHERE setting.integration_id = widened.integration_id
	AND setting.scope_type = widened.scope_type
	AND setting.scope_id IS NOT DISTINCT FROM widened.scope_id
	AND setting.key = 'clusters';

-- They are no longer settings at all, so leaving them behind would be a second answer to
-- a question the module has stopped asking.
DELETE FROM integration_settings
WHERE key IN ('default_rollout_timeout', 'keep_jobs')
	AND integration_id IN (SELECT id FROM integrations WHERE kind LIKE 'deploy:%');