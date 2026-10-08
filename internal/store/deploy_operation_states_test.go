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
	// Waiting is a status as well as a missing start time: a step that was declined before it
	// began is in exactly the same position and is never going to run.
	none := store.DeployOperation{JobID: 1, Status: store.JobPending}
	running := store.DeployOperation{JobID: 2, Status: store.JobRunning, StartedAt: ptr(time.Now())}
	finished := store.DeployOperation{JobID: 3, Status: store.JobSuccess,
		StartedAt: ptr(time.Now()), FinishedAt: ptr(time.Now())}
	skipped := store.DeployOperation{JobID: 4, Status: store.JobSkipped}
	refused := store.DeployOperation{JobID: 5, Status: store.JobRefused}

	cases := map[string]struct {
		op                                    store.DeployOperation
		wantRunning, wantQueued, wantFinished bool
	}{
		"waiting":          {none, false, true, false},
		"under way":        {running, true, false, false},
		"over":             {finished, false, false, true},
		"started and over": {finished, false, false, true},
		"skipped":          {skipped, false, false, true},
		"refused":          {refused, false, false, true},
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

// A card somebody is watching does not vanish, and does not jump to the bottom of the page.
//
// Somebody watching saw a deployment go to "Waiting" and then disappear, and read it as the page
// forgetting it. The page had never had it: the query that chooses which finished operations to
// keep asked for a start time and an end time, and a step declined before it began has neither.
// It was excluded from the choice, so it never reached the outer query either — and being
// excluded there it was not in the answer on any page size, which no sort can put right.
//
// So the cap is exercised here rather than assumed. A cap of ten over five operations never
// chooses anything, and the query that chooses is a different query from the one that lists.
func TestAStepThatNeverBeganIsOnTheListAndKeepsItsPlace(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-order", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i, name := range []string{"older", "old", "newest"} {
		at := began.Add(time.Duration(i) * time.Minute)
		anOperationAt(t, st, project.ID, name, &at, ptr(at.Add(time.Second)), "success")
	}
	// Two that never began, in runs created after all three. One skipped, one refused: the
	// same shape and the same argument, and a status the page had never heard of.
	anOperationAt(t, st, project.ID, "skipped-one", nil, nil, "skipped")
	anOperationAt(t, st, project.ID, "refused-one", nil, nil, "refused")

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 3)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 3 {
		t.Fatalf("read %d operations, want the 3 most recent: %v", len(found), names(found))
	}

	byName := map[string]store.DeployOperation{}
	for _, one := range found {
		byName[one.Name] = one
	}
	for _, want := range []string{"deploy:skipped-one", "deploy:refused-one"} {
		one, there := byName[want]
		if !there {
			t.Fatalf("%s is not among the three most recent, so a card the reader was watching was never drawn at all: %v",
				want, names(found))
		}
		// Waiting is a status and not a missing start time. A step that was declined before it
		// began is in the same position and is never going to run, and drawn as waiting it is
		// a promise the page cannot keep.
		if one.Queued() {
			t.Errorf("%s is drawn as waiting its turn, and it is never going to run", want)
		}
		if !one.Finished() {
			t.Errorf("%s is on neither list, so its card is not drawn", want)
		}
	}

	// And they are at the top, because their runs were created last: they are the cards the
	// reader was watching a moment before.
	if found[0].Name == "deploy:older" || found[1].Name == "deploy:older" {
		t.Errorf("a step that never began sorted to the bottom: %v", names(found))
	}
}

// names is the list in the order it came back, for a failure message.
func names(found []store.DeployOperation) []string {
	out := make([]string, 0, len(found))
	for _, one := range found {
		out = append(out, one.Name)
	}
	return out
}
