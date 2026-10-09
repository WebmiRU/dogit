-- What a job deploys, when a job deploys instead of running a script.
--
-- This is the same bargain as `build`, deliberately: the definition is the module's
-- own, the core stores it and hands it back, and does not decide what a cluster is.
-- A job that has this set is not work for a runner — no runner may claim it, because
-- there is nothing on a machine to run — and the core carries it out itself by
-- handing the task to the module that was named.
--
-- Storing it on the job rather than in a table of its own means a deploy is part of
-- the run that caused it: it gets the same log, the same retry, the same place on
-- the pipeline page, and it is finished by the same call that finishes a test job.
ALTER TABLE jobs
    ADD COLUMN deploy JSONB;