DROP INDEX IF EXISTS merge_requests_reviewer_idx;
DROP INDEX IF EXISTS merge_requests_assignee_idx;

ALTER TABLE merge_requests
    DROP COLUMN IF EXISTS pipeline_required,
    DROP COLUMN IF EXISTS remove_source_branch,
    DROP COLUMN IF EXISTS labels,
    DROP COLUMN IF EXISTS milestone,
    DROP COLUMN IF EXISTS reviewer_id,
    DROP COLUMN IF EXISTS assignee_id,
    DROP COLUMN IF EXISTS is_draft;
