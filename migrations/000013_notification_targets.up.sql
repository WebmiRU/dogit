-- Where notifications are sent: one row per recipient.
--
-- A row here is a named set of settings belonging to a module — for Telegram a
-- chat id, a bot token and a topic; for email an SMTP host, a login and a
-- password. The core does not know what any of those are. What it does know is
-- that one event may need to be delivered more than once: to two chats, to a chat
-- and a mailbox, or twice to the same chat because two rows were configured that
-- way deliberately. So the queue gets one record per switched-on row, each
-- carrying its own address, rather than one record that every module has to
-- re-interpret.
--
-- Rows are defined at a level and inherited downwards: instance, then group, then
-- project. A row at a lower level either *overrides* a row it inherits — it names
-- it in `overrides` and only the values it sets differ — or adds a recipient of its
-- own. Which of the two happened is decided by the id, never by the label: a
-- rename at the instance level must not turn every project's override into a new
-- row.
CREATE TABLE notification_targets (
    id             UUID PRIMARY KEY,
    integration_id UUID NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    scope_type     TEXT NOT NULL CHECK (scope_type IN ('instance', 'group', 'project')),
    -- Null for the instance level, which is the module's own set of recipients.
    scope_id       UUID,
    -- A name for the human. Nothing depends on it.
    label          TEXT NOT NULL DEFAULT '',
    -- Whether this row is switched on *at the level that defines it*, and NULL
    -- when that level did not say. The distinction is the whole point of the
    -- hierarchy: a group that only sets a chat id has not switched anything off,
    -- and a column that could not tell those apart would turn every partial
    -- override into a mute. The most specific level that did say decides.
    enabled        BOOLEAN,
    -- Kept so a shared channel does not have its deployments overtaken by whatever
    -- experiment happened to be built first.
    position       INT NOT NULL DEFAULT 0,
    -- The inherited row this one overrides, if it overrides rather than adds.
    overrides      UUID REFERENCES notification_targets (id) ON DELETE SET NULL,
    -- Whatever the module declared it needs. Secrets live here like any other
    -- value; the module marks them secret in its manifest and the core never
    -- returns them.
    values         JSONB NOT NULL DEFAULT '{}',

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT notification_targets_scope CHECK (
        (scope_type = 'instance' AND scope_id IS NULL) OR
        (scope_type <> 'instance' AND scope_id IS NOT NULL)
    )
);

CREATE INDEX notification_targets_scope_idx
    ON notification_targets (integration_id, scope_type, scope_id, position);

-- One row may override only a row that exists, and the lookup above is what finds
-- inherited rows by it, so it is worth having an index of its own.
CREATE INDEX notification_targets_overrides_idx
    ON notification_targets (overrides)
    WHERE overrides IS NOT NULL;

-- What a notification is for: the row it was addressed to, and that row's values
-- as they were when it was queued.
--
-- The snapshot is deliberate. A module delivering a queue that was written an hour
-- ago must use the address the message was meant for, not whatever the chat id
-- says now — and a row deleted in the meantime must not turn the message into one
-- with nowhere to go.
ALTER TABLE notifications
    ADD COLUMN target_id UUID REFERENCES notification_targets (id) ON DELETE SET NULL;

ALTER TABLE notifications
    ADD COLUMN target_values JSONB NOT NULL DEFAULT '{}';

COMMENT ON COLUMN notifications.target_values IS
    'The recipient row values as they were when this was queued, so delivery does not depend on what the settings say now.';

-- One row per notification still; a fan-out writes the same event several times,
-- each addressed to its own row, which is what keeps a failure to deliver to one
-- channel from holding up another.
CREATE INDEX notifications_target_idx ON notifications (target_id, id);