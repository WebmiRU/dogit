-- The project's brake is gone.
--
-- It stopped a push or a tag from starting a run at all, which is not the question
-- anybody was asking: the build is the repository's own and has nothing to do with where
-- anything is put, and one switch could not say anything about one place rather than
-- another.
--
-- The switch is now a field on a cluster row, inherited like any other and decided by
-- whichever level says something about it. A push still builds and pushes; it simply
-- does not carry a deployment to a place that says it does not deploy by itself, and a
-- manual run deploys there anyway.
--
-- The column was restored by the migration that put the clusters back into the module's
-- settings, so it is dropped again here.

ALTER TABLE projects DROP COLUMN IF EXISTS auto_deploy_paused;