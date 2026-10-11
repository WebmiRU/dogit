-- The registry goes back to being written per place.
--
-- Nothing else is undone. The level-wide keys stay, and the level's own choice stays, because
-- both are facts about the level that were true when this ran and are still true: a project
-- that had one registry for every place it deployed to has one registry for the level, and
-- writing that back into each place says the same thing three times and makes the next
-- change to one of them a fact about only one of them.
DELETE FROM integration_settings WHERE key IN ('image_registry', 'registry_source');
