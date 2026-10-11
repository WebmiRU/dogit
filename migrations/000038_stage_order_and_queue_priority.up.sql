ALTER TABLE jobs
    ADD COLUMN stage_order INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN queue_priority INTEGER NOT NULL DEFAULT 100,
    ADD COLUMN queue_priority_since TIMESTAMPTZ NOT NULL DEFAULT now();

-- Reconstruct a safe conventional order for existing rows: build first, user stages in
-- their previous job order, deploy last. New pipelines persist the exact YAML order.
WITH stage_positions AS (
    SELECT pipeline_id, stage,
           row_number() OVER (
               PARTITION BY pipeline_id
               ORDER BY CASE stage WHEN 'build' THEN 0 WHEN 'deploy' THEN 2 ELSE 1 END,
                        min(iid)
           ) - 1 AS stage_order
    FROM jobs
    GROUP BY pipeline_id, stage
)
UPDATE jobs j
SET stage_order = p.stage_order
FROM stage_positions p
WHERE p.pipeline_id = j.pipeline_id AND p.stage = j.stage;

CREATE INDEX jobs_claim_queue_idx
    ON jobs (status, queue_priority DESC, created_at, id)
    WHERE deploy IS NULL;
