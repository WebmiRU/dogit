ALTER TABLE merge_requests
    ADD COLUMN is_draft BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN reviewer_id UUID REFERENCES users(id) ON DELETE SET NULL,
    ADD COLUMN milestone TEXT NOT NULL DEFAULT '',
    ADD COLUMN labels TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN remove_source_branch BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN pipeline_required BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX merge_requests_assignee_idx ON merge_requests (assignee_id) WHERE assignee_id IS NOT NULL;
CREATE INDEX merge_requests_reviewer_idx ON merge_requests (reviewer_id) WHERE reviewer_id IS NOT NULL;
