-- A second switch per row, for the modules that need one.
--
-- `enabled` asks whether this place may be acted on at all: a project may have a
-- cluster and not be allowed to deploy there, which is what it is for. That is a
-- property of the place and the same question for every kind of module.
--
-- What this adds is a question that is not the same one and only some kinds of module
-- ask: whether a push may come here by itself. A deployment module asks it per place,
-- because "this project builds and deploys nothing unless somebody presses the button"
-- is not a fact about the project — it is a fact about each destination, and a project
-- with two of them may well want one automatic and the other by hand. A notification
-- module does not ask it: there is nothing to start.
--
-- So the module says which switches its rows have, in its manifest, and the core stores
-- whatever it says without knowing what any of them mean. That is the same bargain the
-- values are under, and the reason a second switch is not a column called
-- `auto_deploy_paused` that only a deployment could read.
--
-- A key absent from a level means that level said nothing about it, exactly as a
-- nullable enabled means. Absent from every level means the default, which is on:
-- nothing is stopped unless somebody stops it.
ALTER TABLE module_targets
    ADD COLUMN flags JSONB NOT NULL DEFAULT '{}';

COMMENT ON COLUMN module_targets.flags IS
  'The switches this level has decided about the row, by the name the module gave them '
  'in its manifest. A key that is absent here was not decided at this level and is '
  'inherited; a key present with true or false is this level''s decision and the most '
  'specific level that has one decides. Absent from every level means the default, '
  'which is that nothing is switched off unless somebody switches it off.';