package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// One operation, as the Now page needs it.
//
// Not a Job because a Job is about a job, and this is about a deploy that a job performed: the
// identity here is the job's, because the job's id is what travels on every line the module says
// while it works, but what a reader wants to know is when this deployment began, what became of
// it, and which place it was for.
type DeployOperation struct {
	// JobID is the operation's identity, and the only thing that tells two deployments
	// apart. Every event about a deploy carries it.
	JobID int64

	// Status is the job's own status, not a separate answer: a deployment is finished when
	// its job is, and a second status would be a second thing to keep in step.
	Status string

	// Name is the deploy step's name, as the pipeline configuration named it.
	Name string

	// StartedAt is when it began and FinishedAt when it ended, both nil for one that has not
	// got that far. Sorted on the first with milliseconds kept, because two operations in the
	// same second are otherwise in an order nobody can reproduce.
	StartedAt  *time.Time
	FinishedAt *time.Time

	// Cluster and Namespace are which place it was for, or empty when the page was not told
	// about a place and so claims nothing about one.
	Cluster   string
	Namespace string

	// Error is the reason a failed operation gives, in the words of whoever wrote it.
	Error string
}

// Running reports whether this operation is still under way.
//
// A job that has started and not finished is running, whatever its status says. The status is
// there to be drawn on the card; this is the question of whether the card belongs in the
// "happening now" half of the page or in the list of what happened.
func (o DeployOperation) Running() bool {
	return o.StartedAt != nil && o.FinishedAt == nil
}

// Finished reports whether this operation is over.
//
// Its own question rather than "not Running()", because a deploy step nobody has claimed has
// neither started nor finished, and a card drawn for it would carry a step list with nothing in it
// and an end time that never came. Treating that as finished is how a list ends up with cards for
// steps the pipeline decided to skip.
func (o DeployOperation) Finished() bool {
	return o.FinishedAt != nil
}

// DeployOperations lists a project's deployments, running ones first.
//
// Two lists in one answer because the page draws two: everything still under way, without a limit,
// because a limit there would hide a deployment somebody is waiting for, and then the most recent
// finished ones. `finished` says how many of those to keep — ten on the page, and a page that
// shows a hundred finished deployments is a page nobody reads the running ones off.
//
// Ordered by when each began, newest first, with milliseconds kept. Two deployments in the same
// second are a normal thing to happen and a coin toss otherwise, and a card that moves about
// between two reloads is a page that cannot be looked at.
func (r *PipelineRepo) DeployOperations(ctx context.Context, projectID uuid.UUID,
	cluster, namespace string, finished int) ([]DeployOperation, error) {

	if finished < 0 {
		finished = 0
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT j.id, j.status, j.name, j.started_at, j.finished_at, j.deploy, j.error
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE p.project_id = $1 AND j.deploy IS NOT NULL
		  AND j.started_at IS NOT NULL
		  AND ($2 = '' OR j.deploy->>'Cluster' = $2)
		  AND ($3 = '' OR j.deploy->>'Namespace' = $3)
		  AND (j.finished_at IS NULL
		       OR j.id IN (
		         SELECT j2.id FROM jobs j2
		         JOIN pipelines p2 ON p2.id = j2.pipeline_id
		         WHERE p2.project_id = $1 AND j2.deploy IS NOT NULL
		           AND j2.started_at IS NOT NULL AND j2.finished_at IS NOT NULL
		           AND ($2 = '' OR j2.deploy->>'Cluster' = $2)
		           AND ($3 = '' OR j2.deploy->>'Namespace' = $3)
		         ORDER BY j2.started_at DESC, j2.id DESC LIMIT $4
		       ))
		ORDER BY j.started_at DESC NULLS LAST, j.id DESC`,
		projectID, cluster, namespace, finished)
	if err != nil {
		return nil, fmt.Errorf("list deploy operations: %w", err)
	}
	defer rows.Close()

	operations := []DeployOperation{}
	for rows.Next() {
		var (
			op      DeployOperation
			started *time.Time
			ended   *time.Time
			deploy  []byte
		)
		if err := rows.Scan(&op.JobID, &op.Status, &op.Name, &started, &ended, &deploy, &op.Error); err != nil {
			return nil, fmt.Errorf("scan a deploy operation: %w", err)
		}
		op.StartedAt, op.FinishedAt = started, ended

		var described map[string]any
		decodeJSONB(deploy, &described)
		op.Cluster, _ = described["Cluster"].(string)
		op.Namespace, _ = described["Namespace"].(string)
		operations = append(operations, op)
	}
	return operations, rows.Err()
}
