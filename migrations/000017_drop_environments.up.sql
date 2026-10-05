-- Dropped: nothing ever read or wrote it.
--
-- It was going to be the list of environments a project deploys to. What a place is
-- now settled in the deploy module's own settings, because that is what carries a
-- cluster, a namespace and a credential, and a second table holding the same names in a
-- different shape is a second answer to the same question. They would have drifted, and
-- the drift would have shown up as a deployment going to a cluster nobody wrote down.
DROP TABLE IF EXISTS environments;
