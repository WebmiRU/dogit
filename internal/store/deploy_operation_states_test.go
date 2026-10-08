package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// The same question asked of the whole store rather than of one page's worth: which operations
// there are, and which of them are still under way.
//
// The case this file exists for is the one a page draws a card for: a deploy step nobody has
// claimed has neither started nor finished, and it must not end up in either list. It is the
// shape a pipeline's own skipped step takes, so it is not a rare state — it is what the list
// contains most of the time on a busy project.

// anOperationAt is one deploy job in the state given, returning its id.
func anOperationAt(t *testing.T, st *store.Store, projectID uuid.UUID, name string,
	started, finished *time.Time, status string) int64 {
	t.Helper()

	pipeline, err := st.Pipelines().CreatePipeline(context.Background(), projectID, "main",
		"abc123", "manual", nil, nil, store.Commit{}, []store.Job{{Name: "deploy:" + name}})
	if err != nil {
		t.Fatalf("create a pipeline: %v", err)
	}

	var id int64
	if err := st.Pool().QueryRow(context.Background(), `
		UPDATE jobs SET status = $2, started_at = $3, finished_at = $4, deploy = $5::jsonb
		WHERE pipeline_id = $1 RETURNING id`,
		pipeline.ID, status, started, finished,
		`{"Cluster":"local-k3s","Namespace":"dogit-dev","Target":"kubernetes"}`,
	).Scan(&id); err != nil {
		t.Fatalf("put the deploy job into the state %q: %v", status, err)
	}
	return id
}

// A deploy step nobody has claimed is on neither list.
//
// It has no beginning to sort by and no end to show, so a card drawn for it would be a step list
// with nothing in it — and calling it finished puts a card on the page for a deployment that the
// pipeline decided not to do.
func TestADeployStepNobodyHasClaimedIsOnNeitherList(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-neither", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	ended := began.Add(time.Minute)
	anOperationAt(t, st, project.ID, "waiting", nil, nil, "pending")
	anOperationAt(t, st, project.ID, "running", &began, nil, "running")
	anOperationAt(t, st, project.ID, "done", &began, &ended, "success")

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("read %d operations, want the 2 that began: %+v", len(found), found)
	}

	var running, finished int
	for _, one := range found {
		switch {
		case one.Running():
			running++
		case one.Finished():
			finished++
		default:
			t.Errorf("operation %d is on the list but is neither running nor finished", one.JobID)
		}
	}
	if running != 1 || finished != 1 {
		t.Errorf("read %d running and %d finished, want 1 and 1", running, finished)
	}
}

// The three states are asked of the row and never inferred from the absence of the other two, so
// that a fourth state appearing later does not quietly become "finished".
func TestTheThreeStatesAreEachDecidedOnTheirOwn(t *testing.T) {
	none := store.DeployOperation{JobID: 1}
	running := store.DeployOperation{JobID: 2, StartedAt: ptr(time.Now())}
	finished := store.DeployOperation{JobID: 3, StartedAt: ptr(time.Now()), FinishedAt: ptr(time.Now())}

	cases := map[string]struct {
		op                        store.DeployOperation
		wantRunning, wantFinished bool
	}{
		"never started":    {none, false, false},
		"under way":        {running, true, false},
		"over":             {finished, false, true},
		"started and over": {finished, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.op.Running(); got != c.wantRunning {
				t.Errorf("Running() = %v, want %v", got, c.wantRunning)
			}
			if got := c.op.Finished(); got != c.wantFinished {
				t.Errorf("Finished() = %v, want %v", got, c.wantFinished)
			}
		})
	}
}

func ptr(at time.Time) *time.Time { return &at }
