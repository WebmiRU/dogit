package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// BuildArtifact is the durable record of an artifact-producing job and its current state.
// The build definition begins as configuration and also holds the resolved registry reference
// and digest once the runner confirms a successful push.
type BuildArtifact struct {
	JobID         int64          `json:"job_id"`
	PipelineID    int64          `json:"pipeline_id"`
	PipelineIID   int            `json:"pipeline_iid"`
	JobIID        int            `json:"job_iid"`
	JobName       string         `json:"job_name"`
	Stage         string         `json:"stage"`
	Status        string         `json:"status"`
	QueuePriority int            `json:"queue_priority"`
	ProjectID     uuid.UUID      `json:"project_id"`
	ProjectPath   string         `json:"project_path"`
	Ref           string         `json:"ref"`
	SHA           string         `json:"sha"`
	Build         map[string]any `json:"build"`
	CreatedAt     time.Time      `json:"created_at"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	DurationMS    int64          `json:"duration_ms"`
	Error         string         `json:"error,omitempty"`
}

// ListBuildArtifacts returns every image/artifact producer for a project, newest first,
// together with pagination metadata. Pending jobs are included intentionally: an artifact
// exists as a tracked outcome from the moment its producer is created, not only after push.
func (r *PipelineRepo) ListBuildArtifacts(ctx context.Context, projectID uuid.UUID, page, perPage int) ([]BuildArtifact, int, error) {
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = PipelinePageSizeDefault
	}
	if perPage > PipelinePageSizeMax {
		perPage = PipelinePageSizeMax
	}

	var total int
	if err := r.s.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE p.project_id = $1 AND j.build IS NOT NULL`, projectID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count build artifacts: %w", err)
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT j.id, j.pipeline_id, p.iid, j.iid, j.name, j.stage, j.status,
		       j.queue_priority, p.project_id, pr.path, p.ref, p.sha, j.build,
		       j.created_at, j.started_at, j.finished_at, j.duration_ms, j.error
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		JOIN projects pr ON pr.id = p.project_id
		WHERE p.project_id = $1 AND j.build IS NOT NULL
		ORDER BY p.id DESC, j.iid ASC
		LIMIT $2 OFFSET $3`, projectID, perPage, (page-1)*perPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list build artifacts: %w", err)
	}
	defer rows.Close()

	artifacts := make([]BuildArtifact, 0)
	for rows.Next() {
		var one BuildArtifact
		var buildJSON []byte
		if err := rows.Scan(
			&one.JobID, &one.PipelineID, &one.PipelineIID, &one.JobIID, &one.JobName,
			&one.Stage, &one.Status, &one.QueuePriority, &one.ProjectID, &one.ProjectPath,
			&one.Ref, &one.SHA, &buildJSON, &one.CreatedAt, &one.StartedAt, &one.FinishedAt,
			&one.DurationMS, &one.Error,
		); err != nil {
			return nil, 0, fmt.Errorf("scan build artifact: %w", err)
		}
		one.Build = map[string]any{}
		if len(buildJSON) > 0 {
			if err := json.Unmarshal(buildJSON, &one.Build); err != nil {
				return nil, 0, fmt.Errorf("decode build artifact %d: %w", one.JobID, err)
			}
		}
		artifacts = append(artifacts, one)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("read build artifacts: %w", err)
	}
	return artifacts, total, nil
}
