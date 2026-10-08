-- Resources go away with the table. Nothing else refers to it and nothing in a drop of this
-- table can be undone: a resource's credentials are sealed, so they cannot be read back out
-- to be written down anywhere, and the databases themselves were created by the instance and
-- are not touched here either way — they are left for an administrator, which is what the
-- "a removal does not destroy a resource" rule says.
DROP TABLE IF EXISTS resources;