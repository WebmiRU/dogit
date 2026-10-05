-- Whether this project's deployments wait to be started by a push or a tag.
--
-- Not a policy: a brake. The rules about which branch reaches which place live in the
-- repository, because they are a claim about code and are reviewed with it. This is the
-- other thing entirely — the thing somebody needs at eleven at night when the thing
-- that is deploying must stop now, and a commit is not something anybody can safely
-- push while it is happening.
--
-- Manual runs are unaffected. Pausing automatic deployments is about stopping a
-- pipeline from starting itself, not about being unable to deploy.
ALTER TABLE projects
	ADD COLUMN IF NOT EXISTS auto_deploy_paused BOOLEAN NOT NULL DEFAULT FALSE;
