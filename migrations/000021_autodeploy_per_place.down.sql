-- Puts the project's brake back.
--
-- It comes back switched off, because a column that said "every automatic run for this
-- project" means nothing now that the switches are per place and this is the only way
-- to have it. Anyone relying on it wants the per-place switches set instead, which this
-- cannot reconstruct: the flags are not derivable from a single yes or no.
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS auto_deploy_paused BOOLEAN NOT NULL DEFAULT false;

COMMENT ON COLUMN projects.auto_deploy_paused IS
  'Kept only for installations upgrading from before the switches were per place. It '
  'stops every automatic run for the project and is no longer written to; set the '
  'Autodeploy switch on each place instead.';