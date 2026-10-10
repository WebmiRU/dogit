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

-- Every deploy job in the latest pipeline that can change a target. Keeping all job ids
-- matters for pipelines with multiple sequential deploy steps into the same namespace:
-- when a newer candidate appears, every still-pending step from the previous pipeline
-- must be superseded immediately, not left to become eligible later.
CREATE TABLE deploy_candidate_members (
    target_key TEXT NOT NULL,
    pipeline_id BIGINT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    job_id BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    PRIMARY KEY (target_key, job_id)
);

CREATE INDEX deploy_candidate_members_pipeline
    ON deploy_candidate_members (target_key, pipeline_id);
