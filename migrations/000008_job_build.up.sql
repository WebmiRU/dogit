-- What a job produces, when it produces an image.
--
-- The definition is the registry module's shape rather than the core's: which
-- Dockerfile, which context, which tag. The core stores it, hands it to a runner,
-- and does not decide what any of those mean — the same arrangement as settings,
-- which are declared by the module that will read them.

ALTER TABLE jobs
    ADD COLUMN build JSONB;

COMMENT ON COLUMN jobs.build IS
    'Optional description of an image this job produces. Stored as given and passed on unchanged; the core does not interpret it.';
