-- Removing a module is a task, not a call.
--
-- Deleting half a terabyte takes longer than an HTTP request may last, and the
-- administrator who asked for it will close the tab long before that. So the work
-- happens in the background and what it has to say is kept in the core's own
-- database: an administrator who comes back an hour later finds the log still
-- there. The browser is a window onto it, not the place it lives.

CREATE TABLE module_uninstall_jobs (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    integration_id UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,

    -- The keys of the options the administrator chose, as the module declared
    -- them. Only keys, no values: an option is a switch, and a switch's meaning
    -- belongs to whoever declared it.
    options JSONB NOT NULL DEFAULT '[]'::jsonb,

    -- queued | running | done | failed | stalled | interrupted
    status TEXT NOT NULL DEFAULT 'queued',

    -- The module's own progress, when it reports any.
    progress_done  BIGINT,
    progress_total BIGINT,

    -- What the module said it had done, in its own words.
    summary JSONB,

    error      TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The last moment the module said anything. Silence past the deadline is what
    -- makes a job stalled, and is recorded rather than guessed at.
    last_line_at TIMESTAMPTZ
);

-- One job per module at a time: a second deletion of the same module would be
-- deleting what the first one is still deleting.
CREATE UNIQUE INDEX module_uninstall_jobs_one_per_module_idx
    ON module_uninstall_jobs (integration_id)
    WHERE finished_at IS NULL;

CREATE TABLE module_uninstall_log (
    id         BIGSERIAL PRIMARY KEY,
    job_id     UUID NOT NULL REFERENCES module_uninstall_jobs(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    level      TEXT NOT NULL DEFAULT 'info',
    message    TEXT NOT NULL,
    progress   JSONB
);

-- The log is read forwards and, on reconnect, forwards from a remembered point.
CREATE INDEX module_uninstall_log_job_idx ON module_uninstall_log (job_id, id);
