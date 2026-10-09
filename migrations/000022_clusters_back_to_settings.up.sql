-- The clusters are settings again.
--
-- They were moved into rows so that each could be switched off on its own, and the
-- module was changed to read them from there. That turned out to be the wrong trade:
-- the module's own page stopped being where a cluster is configured, and an operator
-- who had always entered a kubeconfig under the module's Settings found no such box
-- anywhere. The rows go back to being one settings key holding a list, and the module
-- reads it as it always did.
--
-- The rows are not thrown away by hand. Every row that was a root — a cluster written
-- at some level of its own — becomes one element of that level's list, in the order it
-- had, and a row that only overrode another is dropped along with its change: the old
-- form could not say "this cluster but with a different namespace", so there is nothing
-- here that was lost by losing it.

-- Whatever is in the settings already wins: it is the form the module reads, and this
-- migration is putting the rows back rather than arguing with anybody. Deleted first
-- because one level may only have one of these, so an insert that met an existing row
-- would fail rather than replace it.
DELETE FROM integration_settings WHERE key = 'clusters';

INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
SELECT
	integration_id,
	scope_type,
	scope_id,
	'clusters',
	COALESCE(jsonb_agg(values ORDER BY position, created_at), '[]'::jsonb)
FROM module_targets
WHERE overrides IS NULL
	AND integration_id IN (SELECT id FROM integrations WHERE kind LIKE 'deploy:%')
GROUP BY integration_id, scope_type, scope_id
HAVING count(*) > 0;

DELETE FROM module_targets WHERE integration_id IN (SELECT id FROM integrations WHERE kind LIKE 'deploy:%');

-- The project's brake comes back with it: a cluster is one settings key again, so there
-- is no row of its own to carry a switch, and the one thing that can be said from a
-- project is whether a push starts anything here at all.
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS auto_deploy_paused BOOLEAN NOT NULL DEFAULT false;