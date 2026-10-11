DROP INDEX IF EXISTS jobs_claim_queue_idx;

ALTER TABLE jobs
    DROP COLUMN IF EXISTS queue_priority_since,
    DROP COLUMN IF EXISTS queue_priority,
    DROP COLUMN IF EXISTS stage_order;
