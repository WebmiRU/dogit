-- Puts the table back to what it was called before it was generalised.
--
-- In reverse order, because the index names are part of what has to be undone.
ALTER INDEX module_targets_overrides_idx RENAME TO notification_targets_overrides_idx;
ALTER INDEX module_targets_scope_idx RENAME TO notification_targets_scope_idx;

-- The comments go with the names: nothing else in the schema refers to them, and
-- leaving them would describe a table that no longer exists.
COMMENT ON TABLE module_targets IS NULL;
COMMENT ON COLUMN module_targets.enabled IS NULL;
COMMENT ON COLUMN module_targets.position IS NULL;
COMMENT ON COLUMN module_targets.values IS NULL;

ALTER TABLE module_targets RENAME TO notification_targets;