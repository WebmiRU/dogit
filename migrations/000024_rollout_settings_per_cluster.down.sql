-- Puts the rollout timeout and "keep finished jobs" back as settings of the whole
-- module.
--
-- Only where every cluster agrees. These were one value for all of them before, so
-- putting a value back that no single cluster said would be inventing one; an
-- installation whose clusters disagree keeps them disagreeing, because there is no one
-- number that was ever theirs.
--
-- Written so that running it twice says the same thing twice rather than failing: a
-- migration that can be re-run is one that can be tested.

WITH every_cluster AS (
	SELECT setting.integration_id, setting.scope_type, setting.scope_id,
		jsonb_agg(cluster.value ORDER BY cluster.ordinality) AS clusters,
		count(*) AS n,
		count(*) FILTER (WHERE cluster.value ? 'rollout_timeout') AS said,
		count(DISTINCT cluster.value -> 'rollout_timeout') AS one_value
	FROM integration_settings setting
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS cluster(value, ordinality)
	WHERE setting.key = 'clusters'
	GROUP BY setting.integration_id, setting.scope_type, setting.scope_id
)
INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
SELECT integration_id, scope_type, scope_id, 'default_rollout_timeout',
	(clusters -> 0) -> 'rollout_timeout'
FROM every_cluster
WHERE n > 0 AND said = n AND one_value = 1
ON CONFLICT (integration_id, scope_type, scope_id, key)
DO UPDATE SET value = EXCLUDED.value, updated_at = now();

WITH every_cluster AS (
	SELECT setting.integration_id, setting.scope_type, setting.scope_id,
		jsonb_agg(cluster.value ORDER BY cluster.ordinality) AS clusters,
		count(*) AS n,
		count(*) FILTER (WHERE cluster.value ? 'keep_jobs') AS said,
		count(DISTINCT cluster.value -> 'keep_jobs') AS one_value
	FROM integration_settings setting
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS cluster(value, ordinality)
	WHERE setting.key = 'clusters'
	GROUP BY setting.integration_id, setting.scope_type, setting.scope_id
)
INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
SELECT integration_id, scope_type, scope_id, 'keep_jobs', (clusters -> 0) -> 'keep_jobs'
FROM every_cluster
WHERE n > 0 AND said = n AND one_value = 1
ON CONFLICT (integration_id, scope_type, scope_id, key)
DO UPDATE SET value = EXCLUDED.value, updated_at = now();

-- And the fields leave the clusters, which is the other half of what one setting for all
-- of them meant.
WITH narrowed AS (
	SELECT setting.integration_id, setting.scope_type, setting.scope_id,
		jsonb_agg(cluster.value - 'rollout_timeout' - 'keep_jobs' ORDER BY cluster.ordinality) AS clusters
	FROM integration_settings setting
	CROSS JOIN LATERAL jsonb_array_elements(
		CASE WHEN jsonb_typeof(setting.value) = 'array' THEN setting.value ELSE '[]'::jsonb END
	) WITH ORDINALITY AS cluster(value, ordinality)
	WHERE setting.key = 'clusters'
	GROUP BY setting.integration_id, setting.scope_type, setting.scope_id
)
UPDATE integration_settings setting
SET value = narrowed.clusters, updated_at = now()
FROM narrowed
WHERE setting.integration_id = narrowed.integration_id
	AND setting.scope_type = narrowed.scope_type
	AND setting.scope_id IS NOT DISTINCT FROM narrowed.scope_id
	AND setting.key = 'clusters';