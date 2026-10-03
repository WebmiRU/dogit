-- What happened, in words, for the modules that were asked to be told.
--
-- This is a queue rather than a broadcast. A notification module that was down for
-- an hour has to be able to deliver that hour when it comes back, and a signal
-- that fires once and is forgotten cannot do that: a notification system that
-- loses what happened while it was broken is worse than none, because it is
-- trusted.

CREATE TABLE notifications (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL,
    text       TEXT NOT NULL,
    url        TEXT NOT NULL DEFAULT '',
    -- Which levels this is worth sending at, so a module's own filter does not have
    -- to guess from the text.
    levels     TEXT[] NOT NULL DEFAULT '{}',
    data       JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX notifications_kind_id_idx ON notifications (kind, id);

-- How far each kind of module has got. Nothing is deleted when it is acknowledged:
-- a module that is reinstalled and asks what it missed gets an answer, and that is
-- a question somebody always asks afterwards.
CREATE TABLE notification_cursors (
    kind_prefix TEXT PRIMARY KEY,
    cursor      BIGINT NOT NULL DEFAULT 0,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
