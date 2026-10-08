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

	// Place is which place it was for, by the name the deploy step gave — a row of the deploy
	// module's own settings. Empty when the page was not told about a place and so claims
	// nothing about one.
	Place string

	// Cluster and Namespace are the place as the older generation of the configuration wrote
	// it down: a cluster and a namespace rather than a name. Kept because those rows are still
	// in databases and still drawn, and a card that could be read only one way would go blank on
	// exactly the deployments it is easiest to want to look at.
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

// Queued reports whether this operation is waiting its turn.
//
// Its own question, and not the absence of an answer to Running. A deploy step that has not
// begun has no start time, so "is it running" says no, and a page that drew that as finished
// would show a deployment that is going to happen as one that is over — on a run where an
// earlier place is still rolling out and this one is plainly next.
//
// Asked of the status as well as the times, and that is the whole difficulty. A deploy step that
// was skipped also has no start time, and it is not waiting for anything: it was declined before
// it began and never will run. Called by the absence of a start time, it would be drawn "Waiting"
// for ever — a card promising a deployment that is never coming.
func (o DeployOperation) Queued() bool {
	return o.StartedAt == nil && o.FinishedAt == nil && o.Status == JobPending
}

// Finished reports whether this operation is over.
//
// Its own question rather than "not Running()", because a deploy step nobody has claimed has
// neither started nor finished, and a card drawn for it would carry a step list with nothing in it
// and an end time that never came. Treating that as finished is how a list ends up with cards for
// steps the pipeline decided to skip.
func (o DeployOperation) Finished() bool {
	if o.FinishedAt != nil {
		return true
	}
	// A step that was declined before it began has no end time to be found by — nothing
	// ran, so there was no last moment of running — and it is over all the same. Leaving it
	// off both lists is how a card somebody was watching vanished: the job was skipped, the
	// page had never heard of skipped, and there was nothing left to draw.
	switch o.Status {
	case JobSuccess, JobFailed, JobRefused, JobSkipped, JobCanceled, JobAbandoned, JobInterrupted:
		return true
	}
	return false
}

// DeployOperations lists a project's deployments, running ones first.
//
// Two lists in one answer because the page draws two: everything still under way, without a limit,
// because a limit there would hide a deployment somebody is waiting for, and then the most recent
// finished ones. `finished` says how many of those to keep — ten on the page, and a page that
// shows a hundred finished deployments is a page nobody reads the running ones off.
//
// Ordered by when each became visible to a reader, newest first, with milliseconds kept. Two
// deployments in the same second are a normal thing to happen and a coin toss otherwise, and a
// card that moves about between two reloads is a page that cannot be looked at.
//
// A deployment that has not started has no start time, and that is not a reason to put it last.
// It was asked for at a known moment — the run that wants it was created then — and that is the
// moment a reader watching the page first saw a card for it. Ordering on the start time alone
// sent it to the bottom of the page: a card somebody had been watching for a minute jumped below
// every deployment that finished before it was even queued, which reads as the page forgetting
// it rather than as the page sorting it.
//
// `place` is a place's name and nothing else — not a cluster, not a namespace. One name is one
// place: a deploy step says `target: jabjab.ru`, and what that row of the module's settings holds
// inside it is the module's business, not the pipeline's and not this list's. Filtering on a
// cluster instead would be filtering on something the step never said, and a project deploying
// into two namespaces of one cluster would answer with a page for each namespace rather than one
// for the place.
func (r *PipelineRepo) DeployOperations(ctx context.Context, projectID uuid.UUID,
	place string, finished int) ([]DeployOperation, error) {

	if finished < 0 {
		finished = 0
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT j.id, j.status, j.name, j.started_at, j.finished_at, j.deploy, j.error
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		WHERE p.project_id = $1 AND j.deploy IS NOT NULL
		  AND ($2 = '' OR COALESCE(NULLIF(j.deploy->>'Cluster', ''), j.deploy->>'Target') = $2)
		  AND (j.finished_at IS NULL
		       OR j.id IN (
		         SELECT j2.id FROM jobs j2
		         JOIN pipelines p2 ON p2.id = j2.pipeline_id
		         WHERE p2.project_id = $1 AND j2.deploy IS NOT NULL
		           AND j2.started_at IS NOT NULL AND j2.finished_at IS NOT NULL
		           AND ($2 = '' OR COALESCE(NULLIF(j2.deploy->>'Cluster', ''), j2.deploy->>'Target') = $2)
		         ORDER BY j2.started_at DESC, j2.id DESC LIMIT $3
		       ))
		ORDER BY COALESCE(j.started_at, p.started_at, p.created_at) DESC, j.id DESC`,
		projectID, place, finished)
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
		op.Place = placeOf(described, op.Cluster)
		operations = append(operations, op)
	}
	return operations, rows.Err()
}

// placeOf is the place a deploy record names, whichever way that record was written down.
//
// The same rule as the `COALESCE(NULLIF(Cluster,”), Target)` in the query above, written twice
// because a filter has to run in the database and a card has to be labelled in Go. They are kept
// in step by a test that asks for a module name and gets nothing: the two rules drifting apart
// would not break any single case, it would break exactly the records that carry both fields.
//
// Databases hold two generations of the deploy step, and they do not agree about which field
// carries the place. The current one names a place in `Target` — a row of the module's own
// settings. The older one wrote a `Cluster` and a `Namespace` and used `Target` for the *module*,
// which is why a record of that vintage has `Target: kubernetes` beside `Cluster: local-k3s`.
//
// The cluster is preferred where both are present, because in that generation `Target` names a
// module and preferring it would file every deployment of that repository under the module's
// name — a page that answers a question about places with a list of module names. The other
// order gets this right for the newer records and silently empties the tab for every deployment
// made before the configuration changed, which is the worse of the two failures: it is invisible,
// and it is invisible exactly where somebody goes looking for the deployment that broke.
func placeOf(record map[string]any, cluster string) string {
	if cluster != "" {
		return cluster
	}
	target, _ := record["Target"].(string)
	return target
}
