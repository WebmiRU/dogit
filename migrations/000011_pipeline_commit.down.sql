ALTER TABLE pipelines
    DROP COLUMN IF EXISTS commit_title,
    DROP COLUMN IF EXISTS commit_author_name,
    DROP COLUMN IF EXISTS commit_author_email;