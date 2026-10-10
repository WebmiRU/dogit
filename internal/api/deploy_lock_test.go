package api

import (
	"context"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/dbtest"
)

// Session advisory locks are the protection that crosses process boundaries. Separate Server
// values sharing only PostgreSQL must not be able to hold the same target concurrently.
func TestDeployLockIsSharedAcrossServerInstances(t *testing.T) {
	st := dbtest.Open(t)
	first := deployQueueServer(t, st, t.TempDir())
	second := deployQueueServer(t, st, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const identity = "k8s:test-cluster:web"
	one, acquired, err := first.tryDeployLock(ctx, identity)
	if err != nil || !acquired {
		t.Fatalf("first server could not take a free lock: acquired=%v error=%v", acquired, err)
	}
	defer first.releaseDeployLock(context.Background(), one)

	if two, acquired, err := second.tryDeployLock(ctx, identity); err != nil {
		t.Fatalf("second server failed to ask for the shared lock: %v", err)
	} else if acquired {
		second.releaseDeployLock(context.Background(), two)
		t.Fatal("two server instances held the same physical namespace lock")
	}

	first.releaseDeployLock(context.Background(), one)
	two, acquired, err := second.tryDeployLock(ctx, identity)
	if err != nil || !acquired {
		t.Fatalf("second server could not take the lock after release: acquired=%v error=%v", acquired, err)
	}
	second.releaseDeployLock(context.Background(), two)
}

// If taking a multi-target lock reaches a busy second target, every earlier lock must be
// released before the connection is returned. Otherwise unrelated targets can silently stay
// blocked behind a partial acquisition.
func TestDeployLockReleasesPartialAcquisition(t *testing.T) {
	st := dbtest.Open(t)
	first := deployQueueServer(t, st, t.TempDir())
	second := deployQueueServer(t, st, t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	blocker, acquired, err := first.tryDeployLock(ctx, "target-b")
	if err != nil || !acquired {
		t.Fatalf("take blocker: acquired=%v error=%v", acquired, err)
	}
	defer first.releaseDeployLock(context.Background(), blocker)

	if partial, acquired, err := second.tryDeployLocks(ctx, []string{"target-a", "target-b"}); err != nil {
		t.Fatalf("ask for partially overlapping locks: %v", err)
	} else if acquired {
		second.releaseDeployLock(context.Background(), partial)
		t.Fatal("a multi-target operation acquired a target held by another deployment")
	}

	third := deployQueueServer(t, st, t.TempDir())
	released, acquired, err := third.tryDeployLock(ctx, "target-a")
	if err != nil || !acquired {
		t.Fatalf("partial acquisition left target-a locked: acquired=%v error=%v", acquired, err)
	}
	third.releaseDeployLock(context.Background(), released)
}
