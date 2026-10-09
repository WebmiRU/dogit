-- What the pipeline is about.
--
-- The commit a run was made against has a message and an author, and both are
-- facts about that run rather than about the repository as it is now: the branch
-- will have moved on by the time anybody reads the list. Copied here when the
-- pipeline is created, so a list of old runs still says what each one was for.
ALTER TABLE pipelines
    ADD COLUMN commit_title TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_author_name TEXT NOT NULL DEFAULT '',
    ADD COLUMN commit_author_email TEXT NOT NULL DEFAULT '';