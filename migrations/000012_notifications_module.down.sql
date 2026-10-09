DROP INDEX IF EXISTS notifications_module_idx;

ALTER TABLE notifications DROP COLUMN IF EXISTS module_kind;
