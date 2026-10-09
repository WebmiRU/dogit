-- Merge requests, pipelines, jobs, artifacts, deployments and issues.

CREATE TABLE merge_requests (
    id              BIGSERIAL PRIMARY KEY,
    iid             INTEGER NOT NULL,
    project_id      UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    author_id       UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    source_branch   TEXT NOT NULL,
    target_branch   TEXT NOT NULL,
    title           TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    state           TEXT NOT NULL DEFAULT 'opened',
    merge_commit_sha TEXT,
    sha             TEXT NOT NULL DEFAULT '',
    squash          BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    merged_at       TIMESTAMPTZ,
    merged_by_id    UUID REFERENCES users(id) ON DELETE SET NULL,
    closed_at       TIMESTAMPTZ,
    UNIQUE (project_id, iid)
);
CREATE INDEX merge_requests_state_idx ON merge_requests (project_id, state, id DESC);

CREATE TABLE merge_request_notes (
    id               BIGSERIAL PRIMARY KEY,
    merge_request_id BIGINT NOT NULL REFERENCES merge_requests(id) ON DELETE CASCADE,
    author_id        UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    body             TEXT NOT NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX merge_request_notes_mr_idx ON merge_request_notes (merge_request_id, id);

CREATE TABLE pipelines (
    id          BIGSERIAL PRIMARY KEY,
    iid         INTEGER NOT NULL,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    ref         TEXT NOT NULL,
    sha         TEXT NOT NULL,
    source      TEXT NOT NULL DEFAULT 'push',
    status      TEXT NOT NULL DEFAULT 'pending',
    merge_request_iid INTEGER,
    variables   JSONB NOT NULL DEFAULT '{}',
    created_by_id UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    started_at  TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    UNIQUE (project_id, iid)
);
CREATE INDEX pipelines_project_status_idx ON pipelines (project_id, status, id DESC);

CREATE TABLE jobs (
    id            BIGSERIAL PRIMARY KEY,
    pipeline_id   BIGINT NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    iid           INTEGER NOT NULL,
    name          TEXT NOT NULL,
    stage         TEXT NOT NULL DEFAULT 'test',
    status        TEXT NOT NULL DEFAULT 'pending',
    runner_id     UUID,
    image         TEXT NOT NULL DEFAULT '',
    script        TEXT[] NOT NULL DEFAULT '{}',
    allow_failure BOOLEAN NOT NULL DEFAULT FALSE,
    needs         TEXT[] NOT NULL DEFAULT '{}',
    started_at    TIMESTAMPTZ,
    finished_at   TIMESTAMPTZ,
    duration_ms   BIGINT NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (pipeline_id, iid)
);
CREATE INDEX jobs_pipeline_idx ON jobs (pipeline_id, id);

CREATE TABLE job_logs (
    job_id     BIGINT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
    path       TEXT NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE artifacts (
    id         BIGSERIAL PRIMARY KEY,
    job_id     BIGINT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name       TEXT NOT NULL,
    path       TEXT NOT NULL,
    size       BIGINT NOT NULL DEFAULT 0,
    type       TEXT NOT NULL DEFAULT 'archive',
    protected  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX artifacts_job_idx ON artifacts (job_id);

CREATE TABLE runners (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    description    TEXT NOT NULL,
    runner_type    TEXT NOT NULL DEFAULT 'instance',
    project_id     UUID REFERENCES projects(id) ON DELETE CASCADE,
    token_hash     BYTEA NOT NULL,
    online         BOOLEAN NOT NULL DEFAULT FALSE,
    paused         BOOLEAN NOT NULL DEFAULT FALSE,
    last_contact_at TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX runners_token_hash_key ON runners (token_hash);

CREATE TABLE environments (
    id           BIGSERIAL PRIMARY KEY,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    description  TEXT NOT NULL DEFAULT '',
    external_url TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (project_id, name)
);

CREATE TABLE deployments (
    id           BIGSERIAL PRIMARY KEY,
    project_id   UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    environment  TEXT NOT NULL,
    job_id       BIGINT REFERENCES jobs(id) ON DELETE SET NULL,
    sha          TEXT NOT NULL DEFAULT '',
    ref          TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'running',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX deployments_project_idx ON deployments (project_id, environment, id DESC);

CREATE TABLE issues (
    id          BIGSERIAL PRIMARY KEY,
    iid         INTEGER NOT NULL,
    project_id  UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    author_id   UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    assignee_id UUID REFERENCES users(id) ON DELETE SET NULL,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'opened',
    labels      TEXT[] NOT NULL DEFAULT '{}',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    closed_at   TIMESTAMPTZ,
    UNIQUE (project_id, iid)
);
CREATE INDEX issues_project_state_idx ON issues (project_id, state, id DESC);

CREATE TABLE issue_notes (
    id         BIGSERIAL PRIMARY KEY,
    issue_id   BIGINT NOT NULL REFERENCES issues(id) ON DELETE CASCADE,
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    body       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX issue_notes_issue_idx ON issue_notes (issue_id, id);

-- Durable internal event log. The post-receive hook appends here instead of
-- calling the web process over HTTP, so pushes survive a web outage.
CREATE TABLE events (
    id         BIGSERIAL PRIMARY KEY,
    kind       TEXT NOT NULL,
    project_id UUID REFERENCES projects(id) ON DELETE CASCADE,
    actor_id   UUID REFERENCES users(id) ON DELETE SET NULL,
    payload    JSONB NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX events_created_idx ON events (id DESC);
CREATE INDEX events_kind_idx ON events (kind, id DESC);