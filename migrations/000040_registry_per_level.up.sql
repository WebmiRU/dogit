-- Each level's registry moves from "one per place" to "one per level".
--
-- The value is not lost, it is lifted: a project's registry used to be written inside every
-- place it deployed to, and it is now written once for the level. A deployment from March is
-- still looked up against what that project deployed, because the address did not change —
-- only where it is written down.
--
-- A level whose places disagree is NOT migrated, and that is the point of writing it this
-- way. Picking one of three addresses is a decision about where a cluster pulls from, and
-- the only reader of it is a cluster: an address chosen here and wrong is a namespace
-- waiting at ImagePullBackOff, and nobody would be looking at this migration when that
-- happened. A level left alone keeps deploying as it did until somebody says which is
-- right, and the refusal to choose on its own is visible rather than silent.
--
-- 'registry_source' is written alongside: the lifted value is a decision of this level, so
-- the level must be saying "mine" for the address to be read. Without it the new address
-- would sit unread while the level still claims to take the group's — a setting that looks
-- filled in and governs nothing, which is the same failure as the flag this replaces.
INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value, updated_at)
SELECT s.integration_id, s.scope_type, s.scope_id, 'image_registry',
       to_jsonb(agreed.address), now()
FROM integration_settings s
CROSS JOIN LATERAL (
    -- The one address every place in this level named, and nothing at all when they do not
    -- agree or when none of them named one.
    SELECT min(c ->> 'registry') AS address
    FROM jsonb_array_elements(COALESCE(s.value, '[]'::jsonb)) AS c
    WHERE NULLIF(btrim(c ->> 'registry'), '') IS NOT NULL
    HAVING count(DISTINCT btrim(c ->> 'registry')) = 1
) AS agreed
WHERE s.key = 'clusters' AND agreed.address IS NOT NULL
ON CONFLICT (integration_id, scope_type, scope_id, key) DO NOTHING;

INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value, updated_at)
SELECT s.integration_id, s.scope_type, s.scope_id, 'registry_source', '"own"'::jsonb, now()
FROM integration_settings s
WHERE s.key = 'image_registry'
ON CONFLICT (integration_id, scope_type, scope_id, key) DO NOTHING;
