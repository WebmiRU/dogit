-- The project's brake comes back.
--
-- Only the column. What it stopped — a push starting anything at all — is no longer
-- what the code does, and restoring the column does not restore that: the brake now
-- lives on each cluster row as "auto_deploy", and this only puts back the storage a
-- project-level switch used to have. Turning it on here would say nothing and change
-- nothing.

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS auto_deploy_paused BOOLEAN NOT NULL DEFAULT false;