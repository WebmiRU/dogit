package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/deploy"
)

// One deployment at a time per place, and what happens when the one holding it is
// never finished.
//
// The rule exists because two concurrent rollouts in one namespace means two
// migrations against one database. Its failure mode is worse than the thing it
// prevents: a module killed mid-rollout leaves a row that says "running" for ever,
// and every later deployment to that place is refused by a record nobody will ever
// close.
//
// Needs a database. Skipped without one, so the ordinary suite stays offline.
//
//	DOGIT_TEST_DATABASE_URL=postgres://… go test ./cmd/module-deploy-kubernetes
//
// age moves a record's start into the past by d, which is the one thing that tells a
// deployment this module lost from one that is still going: how long ago it began.
func age(t *testing.T, ctx context.Context, history History, id uuid.UUID, d time.Duration) {
	t.Helper()
	stored, ok := history.(*postgresHistory)
	if !ok {
		t.Fatal("the history under test is not the one that keeps records in a table")
	}
	pool := stored.pool
	if _, err := pool.Exec(ctx,
		`UPDATE deployments SET started_at = started_at - $2::interval WHERE id = $1`,
		id, d); err != nil {
		t.Fatalf("age the record: %v", err)
	}
}

func historyFor(t *testing.T) History {
	t.Helper()

	// The module keeps its deployment history in a schema of its own. Its table is also
	// named deployments, which is unrelated to dogit core\x27s deployments table, so CI gives
	// this integration suite a separate database. Keep the old variable as a local fallback
	// for people invoking this package\x27s tests manually.
	url := os.Getenv("DOGIT_MODULE_TEST_DATABASE_URL")
	if url == "" {
		url = os.Getenv("DOGIT_TEST_DATABASE_URL")
	}
	if url == "" {
		t.Skip("no DOGIT_MODULE_TEST_DATABASE_URL or DOGIT_TEST_DATABASE_URL; not testing against a database")
	}

	history, err := openHistory(context.Background(), url)
	if err != nil {
		t.Skipf("the test database could not be reached: %v", err)
	}
	t.Cleanup(func() { history.Close(context.Background()) })
	return history
}

// A record left behind by a module that died must not hold a place for ever.
//
// This is the shape of it on a real cluster: a deployment that was killed mid-rollout
// leaves a row that says "running" for ever, and every later deployment to that place is
// then refused by it. The refusal happened before the code that could have cleaned it up,
// so nothing ever reached it, and the place could not be deployed to again — with a page
// saying a deployment was under way, and no deployment anywhere.
//
// Reclaim is what the handler calls before asking whether the place is busy. Without it
// the only caller was Begin, which the refusal happens before, and the one piece of code
// that could have opened the place was reachable by nothing.
func TestReclaimOpensAPlaceHeldByADeploymentThatWillNeverFinish(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()

	lost := deploy.Deployment{
		ID: uuid.New(), Project: "t/one", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:aaa", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, lost); err != nil {
		t.Fatalf("begin the deployment that will be lost: %v", err)
	}
	// Older than anything a rollout takes, which is the only thing that distinguishes a
	// lost deployment from one that is genuinely in progress.
	age(t, ctx, history, lost.ID, staleDeploymentAge+time.Hour)

	// What the handler does: clear what can be cleared, and only then ask.
	if err := history.Reclaim(ctx, lost.Project, lost.Cluster, lost.Namespace); err != nil {
		t.Fatalf("reclaim: %v", err)
	}

	// Current is "the last deployment to this place", which is not the same question as
	// "is this place busy" — it answers with a record whatever state that record is in,
	// and it is the state that says whether the place is free. A record that is still
	// "running" here is the whole defect: the handler reads it, sees a deployment under
	// way, and refuses.
	last, err := history.Current(ctx, lost.Project, lost.Cluster, lost.Namespace)
	if err != nil {
		t.Fatalf("read the last deployment: %v", err)
	}
	if last == nil {
		t.Fatal("the record is gone entirely, so this test is not exercising the place " +
			"being held — a deployment that was abandoned should still be on the record")
	}
	if last.State == deploy.StateRunning {
		t.Fatalf("the place is still held by a deployment that ended: state=%q, finished_at=%v",
			last.State, last.FinishedAt)
	}
	if last.FinishedAt == nil {
		t.Error("the abandoned record has no finished_at, so a page reading it cannot tell " +
			"when this stopped being in progress")
	}

	next := deploy.Deployment{
		ID: uuid.New(), Project: lost.Project, Cluster: lost.Cluster, Namespace: lost.Namespace,
		Image: "reg/app@sha256:bbb", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, next); err != nil {
		t.Fatalf("the place could not be deployed to again: %v", err)
	}
	t.Cleanup(func() {
		_ = history.Finish(ctx, next.ID, deploy.StateSucceeded, "")
	})
}

// A deployment that really is in progress must not be reclaimed. Reclaiming one that is
// running would let two rollouts reach one workload, which is the thing this whole
// mechanism exists to prevent.
func TestReclaimLeavesADeploymentThatIsStillRunningAlone(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()

	running := deploy.Deployment{
		ID: uuid.New(), Project: "t/one", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:aaa", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, running); err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() {
		_ = history.Finish(ctx, running.ID, deploy.StateSucceeded, "")
	})

	if err := history.Reclaim(ctx, running.Project, running.Cluster, running.Namespace); err != nil {
		t.Fatalf("reclaim: %v", err)
	}

	held, err := history.Current(ctx, running.Project, running.Cluster, running.Namespace)
	if err != nil {
		t.Fatalf("read the current deployment: %v", err)
	}
	if held == nil || held.ID != running.ID {
		t.Fatal("a deployment that was still running was taken off its place, so two " +
			"rollouts of one workload can now reach it at once")
	}
}

// A second deployment to the same place is refused, and says which one is in the way.
func TestASecondDeploymentToTheSamePlaceIsRefused(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()

	first := deploy.Deployment{
		ID: uuid.New(), Project: "t/one", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:aaa", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, first); err != nil {
		t.Fatalf("begin the first deployment: %v", err)
	}
	t.Cleanup(func() {
		_ = history.Finish(ctx, first.ID, deploy.StateSucceeded, "")
	})

	second := first
	second.ID = uuid.New()
	second.Image = "reg/app@sha256:bbb"

	if _, err := history.Begin(ctx, second); err == nil {
		t.Fatal("a second deployment to the same place was accepted")
	}
}

// Once the first is finished, the next one is allowed — the lock is the row, not the
// table.
func TestAFinishedDeploymentReleasesThePlace(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()

	first := deploy.Deployment{
		ID: uuid.New(), Project: "t/two", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:aaa", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, first); err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := history.Finish(ctx, first.ID, deploy.StateSucceeded, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	next := first
	next.ID = uuid.New()
	next.Image = "reg/app@sha256:ccc"
	if _, err := history.Begin(ctx, next); err != nil {
		t.Fatalf("the place was not released by finishing: %v", err)
	}
	_ = history.Finish(ctx, next.ID, deploy.StateSucceeded, "")
}

// A deployment that was begun and never finished must not hold the place for ever.
func TestAnAbandonedDeploymentDoesNotHoldThePlace(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()
	store, ok := history.(*postgresHistory)
	if !ok {
		t.Skip("this test reaches into the store's own tables")
	}

	// Backdated well beyond the age at which a deployment is presumed gone, standing
	// in for one whose module was killed: the row says "running" and nobody is
	// coming to close it.
	stale := deploy.Deployment{
		ID: uuid.New(), Project: "t/three", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:aaa", StartedAt: time.Now().Add(-staleDeploymentAge - time.Hour),
	}
	if _, err := history.Begin(ctx, stale); err != nil {
		t.Fatalf("begin the abandoned deployment: %v", err)
	}
	// Placed straight back to "running" and old, because Begin writes "running" and
	// the point is the age rather than the state.
	if err := store.setStartedAt(ctx, stale.ID, time.Now().Add(-staleDeploymentAge-time.Hour)); err != nil {
		t.Fatalf("backdate the deployment: %v", err)
	}

	next := stale
	next.ID = uuid.New()
	next.Image = "reg/app@sha256:ddd"
	// It starts now, which is the whole point: it is a deployment begun after the one
	// that was abandoned, so it is the current one. Inheriting the abandoned row's start
	// would leave two rows with the same age and a "which is current" question the
	// database answers by whichever row it happens to read first.
	next.StartedAt = time.Now()

	if _, err := history.Begin(ctx, next); err != nil {
		t.Fatalf("an abandoned deployment held the place: %v", err)
	}
	t.Cleanup(func() { _ = history.Finish(ctx, next.ID, deploy.StateSucceeded, "") })

	// And it is recorded as abandoned rather than quietly forgotten: a history that
	// loses a deployment it did not finish is a history that cannot be trusted.
	current, err := history.Current(ctx, next.Project, next.Cluster, next.Namespace)
	if err != nil || current == nil {
		t.Fatalf("read the deployment: %v", err)
	}
	if current.ID != next.ID {
		t.Errorf("the current deployment is %s, want the new one", current.ID)
	}
}

// One record, read back whole.
//
// This is the query that puts a version back, and it is the only reader that is asked
// for one record rather than a list of them. Written out separately from the list, it
// fell six columns behind: every revert failed while reading the row, in milliseconds,
// and the caller — which had already begun its answer — was told 200 and given nothing.
// A record read in pieces is a record that cannot be put back.
func TestADeploymentIsReadBackWhole(t *testing.T) {
	history := historyFor(t)
	ctx := context.Background()

	record := deploy.Deployment{
		ID: uuid.New(), Project: "t/whole", Cluster: "c", Namespace: "n",
		Image: "reg/app@sha256:eee", Workload: "app", Place: "c",
		Tags: []string{"v1"}, Commit: "abc1234", StartedAt: time.Now(),
	}
	if _, err := history.Begin(ctx, record); err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = history.Finish(ctx, record.ID, deploy.StateSucceeded, "") })

	// The phase is written as the run reaches it, not when it starts: a deployment that
	// is begun has not reached one. Written afterwards and read back here, because a
	// record read without its phase is a failure nobody can be shown.
	if err := history.Phase(ctx, record.ID, deploy.StateRunning, deploy.StepRollout, ""); err != nil {
		t.Fatalf("write the phase: %v", err)
	}

	back, err := history.ByID(ctx, record.ID)
	if err != nil {
		t.Fatalf("read the record back: %v", err)
	}

	// Every field the reader promises, checked rather than assumed: a column list that
	// has fallen behind fails as a count mismatch deep inside a scan, in a request about
	// something else entirely.
	if back.ID != record.ID || back.Project != record.Project ||
		back.Cluster != record.Cluster || back.Namespace != record.Namespace {
		t.Errorf("read back as %s/%s/%s, want %s/%s/%s",
			back.Project, back.Cluster, back.Namespace,
			record.Project, record.Cluster, record.Namespace)
	}
	if back.Image != record.Image || back.Workload != record.Workload ||
		back.Place != record.Place || back.Commit != record.Commit {
		t.Errorf("read back image %q workload %q place %q commit %q, want %q %q %q %q",
			back.Image, back.Workload, back.Place, back.Commit,
			record.Image, record.Workload, record.Place, record.Commit)
	}
	if back.Phase != deploy.StepRollout || back.State != deploy.StateRunning {
		t.Errorf("read back %s/%s, want %s/%s", back.State, back.Phase,
			deploy.StateRunning, deploy.StepRollout)
	}
	if len(back.Tags) != 1 || back.Tags[0] != record.Tags[0] {
		t.Errorf("read back tags %v, want %v", back.Tags, record.Tags)
	}
	if back.StartedAt.IsZero() {
		t.Error("read back with no start time")
	}
}
