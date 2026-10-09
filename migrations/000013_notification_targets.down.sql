DROP INDEX IF EXISTS notifications_target_idx;

ALTER TABLE notifications DROP COLUMN IF EXISTS target_values;
ALTER TABLE notifications DROP COLUMN IF EXISTS target_id;

DROP INDEX IF EXISTS notification_targets_overrides_idx;
DROP INDEX IF EXISTS notification_targets_scope_idx;

DROP TABLE IF EXISTS notification_targets;