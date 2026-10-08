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
// Three states and each decided on its own: under way, waiting its turn, and over. Waiting was
// left out of this list at first, on the grounds that a step nobody has claimed has no beginning
// to sort by — which is true and is not the question. The question is what a reader is looking
// at, and on a run with two places where the first is still rolling out, a list that shows one
// deployment and no sign of the one behind it answers "one thing is happening" where the truth is
// "one thing is happening and another is next".

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

// All three are on the list, each saying which of the three it is.
//
// The waiting one is the case this test was written against, in the other direction. A step
// nobody has claimed has no beginning to sort by, and it used to be left off both lists for
// exactly that reason — which left a page showing one deployment while a second sat behind it
// waiting, and no sign of the second anywhere. It is a known state and not an absence, so it is
// on the list and says so; calling it finished would be worse, because that draws a card for a
// deployment that is over when it has not begun.
func TestWaitingRunningAndOverAreEachOnTheList(t *testing.T) {
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
	if len(found) != 3 {
		t.Fatalf("read %d operations, want all 3: %+v", len(found), found)
	}

	var running, finished, queued int
	for _, one := range found {
		switch {
		case one.Running():
			running++
		case one.Queued():
			queued++
		case one.Finished():
			finished++
		default:
			t.Errorf("operation %d is on the list but is none of the three states", one.JobID)
		}
	}
	if running != 1 || finished != 1 || queued != 1 {
		t.Errorf("read %d running, %d queued and %d finished, want 1 of each", running, queued, finished)
	}

	// Two of them at once, which is the whole reason the list exists: a rollout under way and
	// another waiting behind it, on the same project, at the same moment.
	if running != 1 || queued != 1 {
		t.Errorf("a run with one deployment going and one waiting read as %d and %d", running, queued)
	}
}

// The three states are asked of the row and never inferred from the absence of the other two, so
// that a fourth state appearing later does not quietly become "finished".
func TestTheThreeStatesAreEachDecidedOnTheirOwn(t *testing.T) {
	none := store.DeployOperation{JobID: 1}
	running := store.DeployOperation{JobID: 2, StartedAt: ptr(time.Now())}
	finished := store.DeployOperation{JobID: 3, StartedAt: ptr(time.Now()), FinishedAt: ptr(time.Now())}

	cases := map[string]struct {
		op                                    store.DeployOperation
		wantRunning, wantQueued, wantFinished bool
	}{
		"never started":    {none, false, true, false},
		"under way":        {running, true, false, false},
		"over":             {finished, false, false, true},
		"started and over": {finished, false, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.op.Running(); got != c.wantRunning {
				t.Errorf("Running() = %v, want %v", got, c.wantRunning)
			}
			if got := c.op.Queued(); got != c.wantQueued {
				t.Errorf("Queued() = %v, want %v", got, c.wantQueued)
			}
			if got := c.op.Finished(); got != c.wantFinished {
				t.Errorf("Finished() = %v, want %v", got, c.wantFinished)
			}
		})
	}
}

func ptr(at time.Time) *time.Time { return &at }
