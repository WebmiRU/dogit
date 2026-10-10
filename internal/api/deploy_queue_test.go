package api

import "testing"

// The queue is the part that has to be right without anybody watching: it decides which of
// three deployments into one namespace actually reaches it, and nothing else in the system
// gets a second look at that answer.

func queuePlace() deployPlace {
	return deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}
}

func TestTheFirstDeploymentToAPlaceTakesIt(t *testing.T) {
	q := newDeployQueue()
	mayStart, holder := q.take(queuePlace(), 1, 10)
	if !mayStart {
		t.Fatal("nothing else holds this place, so the first deployment was made to wait")
	}
	if !holder {
		t.Error("the deployment that took a free place was not told it has to give it back, " +
			"so nothing would ever release it")
	}
}

// The refusal that used to be here lost a deployment somebody had pushed for.
func TestASecondDeploymentWaitsRatherThanBeingRefused(t *testing.T) {
	q := newDeployQueue()
	q.take(queuePlace(), 1, 10)

	if mayStart, _ := q.take(queuePlace(), 2, 20); mayStart {
		t.Fatal("two deployments hold one place, which is the thing this exists to prevent")
	}
}

// Only the newest runs. Deploying the oldest of three would roll the namespace back to
// code that had already been replaced, and the newer ones would follow it there.
func TestOnlyTheNewestOfThoseWaitingGoes(t *testing.T) {
	q := newDeployQueue()
	q.take(queuePlace(), 1, 10)
	q.take(queuePlace(), 2, 20)
	q.take(queuePlace(), 3, 30)

	promoted, superseded := q.free(queuePlace(), 1)
	if promoted == nil || promoted.jobID != 3 {
		t.Fatalf("promoted %v, want the newest of those waiting", promoted)
	}
	if len(superseded) != 1 || superseded[0].jobID != 2 {
		t.Errorf("superseded %v, want only the middle one", superseded)
	}
}

// The place must still be held after a promotion, because the promoted deployment does not
// ask for it again. If the slot were dropped here, two deployments arriving in the gap
// would both be told they may start.
func TestThePlaceStaysHeldForWhoeverWasPromoted(t *testing.T) {
	q := newDeployQueue()
	q.take(queuePlace(), 1, 10)
	q.take(queuePlace(), 2, 20)

	promoted, _ := q.free(queuePlace(), 1)
	if promoted == nil || promoted.jobID != 2 {
		t.Fatalf("promoted %v, want the deployment that was waiting", promoted)
	}

	// A third arriving in the gap must wait: the place is already spoken for.
	if mayStart, _ := q.take(queuePlace(), 3, 30); mayStart {
		t.Error("the place was free between one deployment ending and the promoted one " +
			"beginning, so two could have started in that gap")
	}

	// And the deployment that ended cannot give the place back a second time, however
	// much it would like to: it no longer holds it, and the promoted one does.
	_, _ = q.free(queuePlace(), 1)
	if mayStart, _ := q.take(queuePlace(), 4, 40); mayStart {
		t.Error("a deployment that had already released the place was able to release it again")
	}

	// The promoted one ends, and then the newest of those waiting goes.
	next, older := q.free(queuePlace(), 2)
	if next == nil || next.jobID != 4 {
		t.Fatalf("promoted %v, want the newest of those waiting", next)
	}
	if len(older) != 1 || older[0].jobID != 3 {
		t.Errorf("superseded %v, want only the one that had been waiting longest", older)
	}
}

// A release from a job that does not hold the place must change nothing.
//
// Failing closed here is deliberate: if a stray release freed the place, two deployments
// would be told they may start, and that is the one outcome nothing here can undo. A
// release that should have happened and did not leaves a place closed until the process
// restarts, which is visible and recoverable; two rollouts into one namespace is neither.
func TestAPlaceIsNotFreedBySomethingThatDoesNotHoldIt(t *testing.T) {
	q := newDeployQueue()
	q.take(queuePlace(), 1, 10)

	_, _ = q.free(queuePlace(), 999)
	if mayStart, _ := q.take(queuePlace(), 2, 20); mayStart {
		t.Error("a release from a job that does not hold the place let another deployment in")
	}
}

// Two places are two places. Serialising deployments that never conflict would be the
// queue doing its job in the wrong direction.
func TestTwoPlacesDoNotWaitForEachOther(t *testing.T) {
	q := newDeployQueue()
	staging := deployPlace{project: "test/versions", cluster: "staging", namespace: "versions"}

	if mayStart, _ := q.take(queuePlace(), 1, 10); !mayStart {
		t.Fatal("nothing holds the first place")
	}
	if mayStart, _ := q.take(staging, 2, 20); !mayStart {
		t.Error("a deployment to another place waited for one that was not in its way")
	}
}

// The same place in two projects is two places, and the same project on two clusters is
// two places. A key that conflated any of them would serialise work that never conflicts —
// or worse, would not.
func TestTheKeyIsAllThreeOfItsParts(t *testing.T) {
	q := newDeployQueue()
	other := deployPlace{project: "test/other", cluster: "prod", namespace: "versions"}
	otherNamespace := deployPlace{project: "test/versions", cluster: "prod", namespace: "other"}
	otherCluster := deployPlace{project: "test/versions", cluster: "staging", namespace: "versions"}

	q.take(queuePlace(), 1, 10)
	for _, place := range []deployPlace{other, otherNamespace, otherCluster} {
		if mayStart, _ := q.take(place, 2, 20); !mayStart {
			t.Errorf("%v waited for a deployment into %v, which is not its place", place, queuePlace())
		}
	}
	if mayStart, _ := q.take(queuePlace(), 3, 30); mayStart {
		t.Error("two deployments hold one place")
	}
}

// A queue this core never filled is not a queue this core is managing, and pretending
// otherwise would be a map that grows for ever.
func TestAFreedPlaceIsForgotten(t *testing.T) {
	q := newDeployQueue()
	q.take(queuePlace(), 1, 10)
	q.free(queuePlace(), 1)

	q.mu.Lock()
	left := len(q.slots)
	q.mu.Unlock()
	if left != 0 {
		t.Errorf("%d places are still held after everything finished", left)
	}
}


// A repeated readiness check must not put the same pending job in the queue twice.
// Otherwise one copy can be promoted while the other copy supersedes that same job.
func TestADeploymentJoinsAPlaceQueueOnlyOnce(t *testing.T) {
	q := newDeployQueue()
	place := queuePlace()
	q.take(place, 1, 10)

	mayStart, holder, enqueued := q.takeWithStatus(place, 2, 20)
	if mayStart || holder || !enqueued {
		t.Fatalf("first queue entry: got mayStart=%v holder=%v enqueued=%v, want false/false/true",
			mayStart, holder, enqueued)
	}

	mayStart, holder, enqueued = q.takeWithStatus(place, 2, 20)
	if mayStart || holder || enqueued {
		t.Fatalf("repeated queue entry: got mayStart=%v holder=%v enqueued=%v, want false/false/false",
			mayStart, holder, enqueued)
	}

	q.take(place, 3, 30)
	promoted, superseded := q.free(place, 1)
	if promoted == nil || promoted.jobID != 3 {
		t.Fatalf("promoted %v, want the newest distinct deployment (job 3)", promoted)
	}
	if len(superseded) != 1 || superseded[0].jobID != 2 {
		t.Errorf("superseded %v, want only the older distinct job 2", superseded)
	}
}
