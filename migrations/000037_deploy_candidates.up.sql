CREATE TABLE deploy_candidates (
    target_key TEXT PRIMARY KEY,
    latest_pipeline_id BIGINT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    latest_job_id BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    waiting_job_id BIGINT REFERENCES jobs(id) ON DELETE SET NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX deploy_candidates_waiting_job
    ON deploy_candidates (waiting_job_id)
    WHERE waiting_job_id IS NOT NULL;
