-- The progress of a deployment is not part of its job and never was.
--
-- It is a note about a moment: what the module had said the last time it spoke. The deployments
-- this column ever held are all over now, and the column goes with them. The log that carries
-- the account of what happened is untouched, and a page opened halfway through a deployment will
-- be back to showing the plan and no marks on it — which is what it did before, and which this
-- migration is undoing, not a state to be preserved.
ALTER TABLE jobs DROP COLUMN IF EXISTS deploy_progress;
