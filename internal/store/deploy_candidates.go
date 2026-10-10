package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// DeployCandidateChange says which older pipeline stopped being the latest candidate
// when a new pipeline was recorded for the same physical place.
type DeployCandidateChange struct {
	TargetKey         string
	PreviousPipelineID int64
}

// DeployCandidateStatus is the shared, durable state of a target's newest candidate.
type DeployCandidateStatus struct {
	PipelineID  int64
	JobID       int64
	WaitingJobID int64
}

// uniqueDeployCandidateKeys is also the lock order. Every transaction that touches more
// than one target takes its rows in the same order, so deployments to overlapping sets of
// targets cannot deadlock each other merely by arriving in opposite orders.
func uniqueDeployCandidateKeys(keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, found := seen[key]; found {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

// registerDeployCandidateTx records a pipeline as the latest candidate for one target.
// Pipeline ids are allocated by PostgreSQL's sequence, so a delayed registration from an
// older pipeline can never replace a newer candidate that has already been recorded.
func registerDeployCandidateTx(ctx context.Context, tx pgx.Tx, key string,
	jobID, pipelineID int64) (*DeployCandidateChange, error) {
	_, err := tx.Exec(ctx, `
		INSERT INTO deploy_candidates (target_key, latest_pipeline_id, latest_job_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (target_key) DO NOTHING`, key, pipelineID, jobID)
	if err != nil {
		return nil, fmt.Errorf("create deployment candidate: %w", err)
	}

	var previousPipelineID int64
	if err := tx.QueryRow(ctx, `
		SELECT latest_pipeline_id
		FROM deploy_candidates
		WHERE target_key = $1
		FOR UPDATE`, key).Scan(&previousPipelineID); err != nil {
		return nil, fmt.Errorf("read current deployment candidate: %w", err)
	}

	if previousPipelineID >= pipelineID {
		// The same pipeline may contain ordered deployment steps to the same place.
		// They are one candidate, not newer commits competing with one another.
		return nil, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE deploy_candidates
		SET latest_pipeline_id = $2, latest_job_id = $3,
		    waiting_job_id = NULL, updated_at = now()
		WHERE target_key = $1`, key, pipelineID, jobID); err != nil {
		return nil, fmt.Errorf("replace deployment candidate: %w", err)
	}

	if previousPipelineID == pipelineID {
		return nil, nil
	}
	return &DeployCandidateChange{TargetKey: key, PreviousPipelineID: previousPipelineID}, nil
}

// IsLatestDeployCandidate says whether this pipeline is still the newest candidate for every
// target it may change. A missing row is not permission: the caller must refuse to deploy
// when coordination has not been recorded.
func (r *PipelineRepo) IsLatestDeployCandidate(ctx context.Context, keys []string,
	pipelineID int64) (bool, error) {
	keys = uniqueDeployCandidateKeys(keys)
	if len(keys) == 0 {
		return false, nil
	}
	for _, key := range keys {
		var latest int64
		err := r.s.pool.QueryRow(ctx, `
			SELECT latest_pipeline_id
			FROM deploy_candidates
			WHERE target_key = $1`, key).Scan(&latest)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, fmt.Errorf("deployment candidate %q is not registered: %w", key, ErrNotFound)
		}
		if err != nil {
			return false, fmt.Errorf("read latest deployment candidate: %w", err)
		}
		if latest != pipelineID {
			return false, nil
		}
	}
	return true, nil
}

// ClaimDeployCandidate atomically verifies candidate freshness and claims the deploy job.
// Candidate registration and claiming lock the same target rows, so a new pipeline cannot
// slip between the freshness check and the durable transition to Running.
func (r *PipelineRepo) ClaimDeployCandidate(ctx context.Context, keys []string,
	jobID, pipelineID int64) (claimed, latest bool, err error) {
	keys = uniqueDeployCandidateKeys(keys)
	if len(keys) == 0 {
		return false, false, nil
	}

	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return false, false, fmt.Errorf("begin deployment claim: %w", err)
	}
	defer tx.Rollback(ctx)

	for _, key := range keys {
		var current int64
		err := tx.QueryRow(ctx, `
			SELECT latest_pipeline_id
			FROM deploy_candidates
			WHERE target_key = $1
			FOR UPDATE`, key).Scan(&current)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, false, nil
		}
		if err != nil {
			return false, false, fmt.Errorf("lock deployment candidate: %w", err)
		}
		if current != pipelineID {
			return false, false, nil
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE jobs
		SET status = $2, started_at = now()
		WHERE id = $1 AND pipeline_id = $3 AND status = $4 AND deploy IS NOT NULL`,
		jobID, JobRunning, pipelineID, JobPending)
	if err != nil {
		return false, true, fmt.Errorf("claim deployment job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, true, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE pipelines SET started_at = COALESCE(started_at, now())
		WHERE id = $1 AND status = $2`, pipelineID, PipelinePending); err != nil {
		return false, true, fmt.Errorf("mark the pipeline running: %w", err)
	}
	for _, key := range keys {
		if _, err := tx.Exec(ctx, `
			UPDATE deploy_candidates
			SET waiting_job_id = NULL, updated_at = now()
			WHERE target_key = $1 AND waiting_job_id = $2`, key, jobID); err != nil {
			return false, true, fmt.Errorf("clear deployment waiting state: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, true, fmt.Errorf("commit deployment claim: %w", err)
	}
	return true, true, nil
}

// SetDeployCandidateWaiting exposes the cross-process wait state to the operations page.
// It only writes for the current pipeline: an older goroutine waking after it was overtaken
// must not repaint the newer candidate as waiting.
func (r *PipelineRepo) SetDeployCandidateWaiting(ctx context.Context, keys []string,
	jobID, pipelineID int64, waiting bool) error {
	keys = uniqueDeployCandidateKeys(keys)
	for _, key := range keys {
		if waiting {
			_, err := r.s.pool.Exec(ctx, `
				UPDATE deploy_candidates
				SET waiting_job_id = $3, updated_at = now()
				WHERE target_key = $1 AND latest_pipeline_id = $2`,
				key, pipelineID, jobID)
			if err != nil {
				return fmt.Errorf("record deployment waiting state: %w", err)
			}
		} else {
			_, err := r.s.pool.Exec(ctx, `
				UPDATE deploy_candidates
				SET waiting_job_id = NULL, updated_at = now()
				WHERE target_key = $1 AND waiting_job_id = $2`, key, jobID)
			if err != nil {
				return fmt.Errorf("clear deployment waiting state: %w", err)
			}
		}
	}
	return nil
}

// DeployCandidateWaiting reports waiting from durable state, not from a queue owned by one
// application process.
func (r *PipelineRepo) DeployCandidateWaiting(ctx context.Context, jobID int64) (bool, error) {
	var waiting bool
	err := r.s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM deploy_candidates WHERE waiting_job_id = $1
		)`, jobID).Scan(&waiting)
	if err != nil {
		return false, fmt.Errorf("read deployment waiting state: %w", err)
	}
	return waiting, nil
}
