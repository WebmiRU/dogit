-- What a module reports about itself alongside each heartbeat.

-- A module lives on its own node and is the only thing that knows how much space
-- it uses and how loaded it is, so the numbers have to come from there. This is
-- a time series rather than a column on the module: an administrator wants to see
-- that a disk is filling up, and a single current value cannot show that.

CREATE TABLE module_stats (
    integration_id     UUID NOT NULL REFERENCES integrations(id) ON DELETE CASCADE,
    at                 TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- The module's own storage: the filesystem it writes to, or what it knows of
    -- its bucket. Both are absent when the module cannot see them.
    storage_total_bytes BIGINT,
    storage_used_bytes  BIGINT,

    -- The module process itself, as opposed to the machine under it.
    process_cpu_percent     REAL,
    process_memory_bytes    BIGINT,

    -- The node, read from /proc by the module. A container may not be allowed to
    -- see all of it, and a missing value is left missing rather than zeroed.
    host_cpu_percent        REAL,
    host_memory_total_bytes BIGINT,
    host_memory_used_bytes  BIGINT,
    host_load1              REAL,

    uptime_seconds BIGINT,

    -- Whatever else the module finds worth saying: how many images it holds, how
    -- many repositories, how long its last request took.
    extra JSONB NOT NULL DEFAULT '{}'::jsonb,

    PRIMARY KEY (integration_id, at)
);

CREATE INDEX module_stats_recent_idx ON module_stats (integration_id, at DESC);
