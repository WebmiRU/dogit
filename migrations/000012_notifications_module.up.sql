-- Which module a notification is for.
--
-- The core decides which notification module is in use for a project — somebody
-- has to, and it is the core that knows what is installed — and says so here, so
-- that a module reads only what was meant for it. Without this every installed
-- module would deliver every message, and installing a second channel would
-- double the messages rather than divide them.
--
-- Empty for a record nobody has claimed, which is what a test message is: it is
-- not addressed to anybody and is read only by whoever asked for it.
ALTER TABLE notifications
    ADD COLUMN module_kind TEXT NOT NULL DEFAULT '';

CREATE INDEX notifications_module_idx ON notifications (module_kind, id);

COMMENT ON COLUMN notifications.module_kind IS
    'The kind of the notification module this record is for; empty when it is addressed to nobody.';