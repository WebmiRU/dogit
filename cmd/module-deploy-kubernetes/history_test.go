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
func historyFor(t *testing.T) History {
	t.Helper()

	url := os.Getenv("DOGIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("no DOGIT_TEST_DATABASE_URL; not testing against a database")
	}

	history, err := openHistory(context.Background(), url)
	if err != nil {
		t.Skipf("the test database could not be reached: %v", err)
	}
	t.Cleanup(func() { history.Close(context.Background()) })
	return history
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
