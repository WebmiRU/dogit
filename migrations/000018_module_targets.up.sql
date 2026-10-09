-- notification_targets was never about notifications.
--
-- It was one row of a module's settings, defined at a level and inherited downwards,
-- switchable at each of them — and the deploy module wanting the same three things
-- meant writing the resolution a second time, in a second table, free to disagree
-- with the first. So it is renamed: a recipient is now one kind of row in here, and a
-- place a project may deploy to is another.
--
-- What the rows mean is still the module's business, not the core's. The core knows
-- that a row belongs to a module, that it was written at some level, that a lower
-- level may change the values it inherits and may switch it off, and nothing at all
-- about what any of the values are. It does not know what a chat id is and it does
-- not know what a kubeconfig is, which is what lets the day come that somebody writes
-- a module for either of them and nothing here has to change.
ALTER TABLE notification_targets RENAME TO module_targets;

ALTER INDEX notification_targets_scope_idx RENAME TO module_targets_scope_idx;
ALTER INDEX notification_targets_overrides_idx RENAME TO module_targets_overrides_idx;

-- Renamed above, so these read as claims about a table that no longer exists.
COMMENT ON TABLE module_targets IS
  'One row per place a module may act: a recipient for a notification module, a cluster '
  'for a deployment module. Defined at a level (instance, group, project) and inherited '
  'downwards. A row at a lower level either overrides the row it inherits, naming it in '
  '"overrides" and setting only what differs, or adds a row of its own. Which of the two '
  'happened is decided by the id, never by the label: a rename at the instance level '
  'must not turn every project''s override into a row of its own.';

COMMENT ON COLUMN module_targets.enabled IS
  'Whether this row is switched on *at the level that defines it*, and NULL when that '
  'level did not say. The distinction is the whole point of the hierarchy: a group that '
  'only sets a chat id has not switched anything off, and a column that could not tell '
  'those apart would turn every partial override into a mute. The most specific level '
  'that did say decides.';

COMMENT ON COLUMN module_targets.position IS
  'Kept so a shared row is not overtaken by whatever was created first. A channel shared '
  'by twenty projects and a cluster every project deploys to are both a case of the '
  'order being somebody''s decision rather than the clock''s.';

COMMENT ON COLUMN module_targets.values IS
  'Whatever the module declared it needs, in this row. Secrets live here like any other '
  'value; the module marks them secret in its manifest and the core never returns them.';