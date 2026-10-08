package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// The list of deployments a page draws one card each for.
//
// Two things here are the whole reason this exists rather than being built out of the live event
// feed: a page must know what is under way *before* anything arrives on the socket, and a
// deployment must be identifiable so that a card can be about one deployment and not about
// "whatever is happening".

// anOperation is one deploy job, in the state given.
//
// Written as a direct update rather than through whatever creates a job, because that path takes
// its times from the clock — and a list of finished operations ordered by start time cannot be built
// out of a clock nobody controls. What is under test is the list, not how a job comes to exist.
func anOperation(t *testing.T, st *store.Store, projectID uuid.UUID, name string,
	cluster, namespace string, started, finished *time.Time, status string) int64 {
	t.Helper()

	pipeline, err := st.Pipelines().CreatePipeline(context.Background(), projectID, "main",
		"abc123", "manual", nil, nil, store.Commit{}, []store.Job{{Name: "deploy:" + name}})
	if err != nil {
		t.Fatalf("create a pipeline: %v", err)
	}

	var id int64
	err = st.Pool().QueryRow(context.Background(), `
		UPDATE jobs SET status = $2, started_at = $3, finished_at = $4, deploy = $5::jsonb
		WHERE pipeline_id = $1 RETURNING id`,
		pipeline.ID, status, started, finished,
		`{"Cluster":"`+cluster+`","Namespace":"`+namespace+`","Target":"kubernetes"}`,
	).Scan(&id)
	if err != nil {
		t.Fatalf("put the deploy job into the state %q: %v", status, err)
	}
	return id
}

// namedOperation is one deploy job written the current way: the step names a place, and the
// module says which module it is.
func namedOperation(t *testing.T, st *store.Store, projectID uuid.UUID, name, place string,
	started, finished *time.Time, status string) int64 {
	t.Helper()

	pipeline, err := st.Pipelines().CreatePipeline(context.Background(), projectID, "main",
		"abc123", "manual", nil, nil, store.Commit{}, []store.Job{{Name: "deploy:" + name}})
	if err != nil {
		t.Fatalf("create a pipeline: %v", err)
	}

	var id int64
	err = st.Pool().QueryRow(context.Background(), `
		UPDATE jobs SET status = $2, started_at = $3, finished_at = $4, deploy = $5::jsonb
		WHERE pipeline_id = $1 RETURNING id`,
		pipeline.ID, status, started, finished,
		`{"Name":"`+name+`","Module":"kubernetes","Target":"`+place+`","Rollout":true}`,
	).Scan(&id)
	if err != nil {
		t.Fatalf("put the deploy job into the state %q: %v", status, err)
	}
	return id
}

// A deployment under way is in the running list and not the finished one, whatever its status says.
// A job that has started and not finished is running; the status is there to be drawn on the card.
func TestAnUnfinishedDeploymentIsRunning(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-running", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	anOperation(t, st, project.ID, "in flight", "local-k3s", "dogit-dev", &began, nil, "running")

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("read %d operations, want 1: %+v", len(found), found)
	}
	if !found[0].Running() {
		t.Error("a deployment with no end is not reported as running")
	}
	if found[0].StartedAt == nil {
		t.Error("a running deployment has no start time, so nothing could be sorted by it")
	}
}

// The finished ones are capped, and the cap keeps the *recent* ones. Paging a running deployment
// out behind a hundred older rows is a deployment somebody is waiting for disappearing.
func TestTheFinishedOnesAreCappedAndKeepTheRecent(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-cap", nil)

	// Truncated to the microsecond, which is what a timestamp column keeps. Left at nanoseconds
	// the value written and the value read back differ by a few hundred, and every comparison
	// against it fails while the ordering still looks right — the most confusing possible
	// failure, and one that says nothing about the thing under test.
	base := time.Now().UTC().Truncate(time.Microsecond).Add(-10 * time.Hour)
	for i := range 25 {
		began := base.Add(time.Duration(i) * time.Minute)
		ended := began.Add(30 * time.Second)
		anOperation(t, st, project.ID, "old-"+time.Duration(i).String(), "local-k3s", "dogit-dev",
			&began, &ended, "success")
	}

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 10 {
		t.Fatalf("read %d operations, want the 10 most recent: %+v", len(found), found)
	}

	// Newest first, and the newest of them is the last one made.
	for i := 1; i < len(found); i++ {
		if found[i-1].StartedAt.Before(*found[i].StartedAt) {
			t.Errorf("operation %d is older than the one after it: %s before %s",
				i, found[i-1].StartedAt, found[i].StartedAt)
		}
	}
	if !found[0].StartedAt.Equal(base.Add(24 * time.Minute)) {
		t.Errorf("the newest listed operation began at %s, want the one just made", found[0].StartedAt)
	}
}

// A running deployment is never paged out, however much has been finished since it started. That is
// the difference between a limit on the finished list and a limit on the list.
func TestARunningDeploymentIsNeverPagedOut(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-never", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-9 * time.Hour)
	anOperation(t, st, project.ID, "still going", "local-k3s", "dogit-dev", &began, nil, "running")

	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i := range 20 {
		ended := base.Add(time.Duration(i) * time.Minute)
		started := ended.Add(-30 * time.Second)
		anOperation(t, st, project.ID, "later-"+time.Duration(i).String(), "local-k3s", "dogit-dev",
			&started, &ended, "success")
	}

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 3)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}

	var running int
	for _, one := range found {
		if one.Running() {
			running++
		}
	}
	if running != 1 {
		t.Errorf("read %d running operations out of a list capped at 3 finished, want the 1 that "+
			"is still going: %+v", running, found)
	}
}

// Zero finished is a way of saying none of the old ones, not the same as leaving the number out
// and getting ten of them.
func TestZeroFinishedAsksForNoHistory(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-zero", nil)

	base := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Hour)
	for i := range 5 {
		began := base.Add(time.Duration(i) * time.Minute)
		ended := began.Add(time.Second)
		anOperation(t, st, project.ID, "done-"+time.Duration(i).String(), "local-k3s", "dogit-dev",
			&began, &ended, "success")
	}

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 0)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("asking for no history returned %d: %+v", len(found), found)
	}
}

// A card is about one place. A project with two places has deployments in both, and a page that
// showed a place's rollout on the other place's page would put one project's work under another's
// name.
//
// Asked by the place's name, which is what a deploy step gives: `target: jabjab.ru`. Two places in
// two namespaces of one cluster are two names, and each answers for itself — filtering by cluster
// instead would put both of them on one page and neither of them where it belongs.
func TestOperationsAreFilteredByPlaceName(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-place", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	namedOperation(t, st, project.ID, "dev", "jabjab.ru", &began, nil, "running")
	namedOperation(t, st, project.ID, "second", "jabjab-deploy2", &began, nil, "running")
	namedOperation(t, st, project.ID, "elsewhere", "other.host", &began, nil, "running")

	for _, c := range []struct {
		place string
		want  int
	}{
		{"", 3},
		{"jabjab.ru", 1},
		{"jabjab-deploy2", 1},
		{"other.host", 1},
		{"no-such-place", 0},
	} {
		found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, c.place, 10)
		if err != nil {
			t.Fatalf("list operations for %q: %v", c.place, err)
		}
		if len(found) != c.want {
			t.Errorf("for %q read %d operations, want %d: %+v", c.place, len(found), c.want, found)
		}
	}
}

// A record written the older way still belongs to its place, and still says which one.
//
// This is the whole reason the filter looks in two fields. Deployments made before the
// configuration changed are in every database there is, they name a `Cluster` and a `Namespace`,
// and their `Target` names the module. A filter that read only the current field would answer
// correctly about everything deployed since the change and nothing at all about everything
// deployed before it — and the deployments somebody opens this page for are overwhelmingly the
// ones that already went wrong.
func TestAnOlderRecordIsStillClaimedByItsPlace(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-older", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	anOperation(t, st, project.ID, "dev", "local-k3s", "dogit-dev", &began, nil, "running")

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "local-k3s", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("read %d operations, want 1: %+v", len(found), found)
	}
	if found[0].Place != "local-k3s" {
		t.Errorf("the operation is filed under %q, want local-k3s", found[0].Place)
	}

	// And not under the module, which is what its Target says.
	byModule, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "kubernetes", 10)
	if err != nil {
		t.Fatalf("list operations by module name: %v", err)
	}
	if len(byModule) != 0 {
		t.Errorf("a module name was taken for a place, and matched %d operations", len(byModule))
	}
}

// Two places in two namespaces of one cluster, asked for by name, each answering for itself.
//
// The case that a cluster-and-namespace filter got wrong in the other direction: it split one
// logical place into two pages, so a deployment of the second namespace never appeared on the
// page of the first, and neither page could be said to be about the cluster.
func TestTwoNamespacesOfOneClusterAreTwoPlaces(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-two-namespaces", nil)

	began := time.Now().UTC().Truncate(time.Microsecond).Add(-time.Minute)
	anOperation(t, st, project.ID, "first", "shared-cluster", "one", &began, nil, "running")
	anOperation(t, st, project.ID, "second", "shared-cluster", "two", &began, nil, "running")

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "shared-cluster", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("one cluster holding two namespaces answered with %d operations, want 2: %+v",
			len(found), found)
	}
	// Both claim the same place name, because as far as a place is concerned they are the
	// same place, and the namespaces are a detail of what is inside it.
	for _, one := range found {
		if one.Place != "shared-cluster" {
			t.Errorf("an operation is filed under %q, want shared-cluster", one.Place)
		}
	}
}

// A build job is not a deployment and must never appear as one: the card would show a step list
// with nothing in it and an end time belonging to something else entirely.
func TestABuildIsNotADeployment(t *testing.T) {
	st := dbtest.Open(t)
	project := dbtest.NewProject(t, st, "ops-build", nil)

	if _, err := st.Pipelines().CreatePipeline(context.Background(), project.ID, "main",
		"abc123", "manual", nil, nil, store.Commit{},
		[]store.Job{{Name: "build", Stage: "build", Status: "success"}}); err != nil {
		t.Fatalf("create a build job: %v", err)
	}

	found, err := st.Pipelines().DeployOperations(context.Background(), project.ID, "", 10)
	if err != nil {
		t.Fatalf("list operations: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("a build job was listed as a deployment: %+v", found)
	}
}
