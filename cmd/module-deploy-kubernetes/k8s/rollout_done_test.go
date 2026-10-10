package k8s

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The middle of a rolling update, which is where a deployment goes wrong when it asks the
// wrong question.
//
// A Deployment that has been given new pods is, for a while, in a state where every number
// a person would look at says three of three are ready — because the three pods being
// replaced are still there and still serving. Only `updatedReplicas` says otherwise, because
// it counts the pods of the revision being rolled *to*. A rollout judged on readiness alone
// is therefore finished the instant the controller picks the spec up — roughly a second into
// a rollout that takes forty — and the deployment reports success over a namespace the pods
// being replaced are still answering for.
//
// This is not the corner of a rollout; it is most of it.
func TestARolloutWithPodsStillOnTheOldRevisionIsNotDone(t *testing.T) {
	for _, one := range []struct {
		name      string
		replicas  int32
		updated   int32
		ready     int32
		available int32
		done      bool
	}{
		{
			// The controller has seen the spec, one new pod exists out of three, and the
			// three old ones are still ready and still counted as ready.
			name: "one of three updated", replicas: 3, updated: 1, ready: 3, available: 3,
		},
		{
			// Nothing has been created yet — the state right after apply.
			name: "nothing updated", replicas: 3, updated: 0, ready: 3, available: 3,
		},
		{
			// All three created, none of them serving yet. Available is still three,
			// because the old ones are, so a rollout judged on availability alone calls
			// this finished as surely as one judged on readiness.
			name: "created but not yet serving", replicas: 3, updated: 3, ready: 0, available: 3,
		},
		{
			// And the shape that must still be accepted: everything asked for is updated
			// and everything asked for is ready. Available is above the ask because a
			// surge leaves the old pods up while the new ones come in, and a rollout that
			// waited for that to fall back to three would never finish.
			name: "everything updated and ready", replicas: 3, updated: 3, ready: 3, available: 4,
			done: true,
		},
		{
			name: "scaled to nothing", replicas: 0, updated: 0, ready: 0, available: 0,
			done: true,
		},
	} {
		t.Run(one.name, func(t *testing.T) {
			got := rolloutOf(&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{Generation: 2},
				Spec:       appsv1.DeploymentSpec{Replicas: replicasPtr(one.replicas)},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					Replicas:           one.replicas,
					UpdatedReplicas:    one.updated,
					ReadyReplicas:      one.ready,
					AvailableReplicas:  one.available,
				},
			})
			if got.Done != one.done {
				t.Errorf("Done is %v, want %v — desired %d, updated %d, ready %d, available %d",
					got.Done, one.done, got.Desired, got.Updated, got.Ready, one.available)
			}
		})
	}
}

// A Deployment the cluster has not seen is not a finished rollout, even when the numbers
// add up to nothing.
//
// It has no observed generation at all, and a Deployment created a moment ago may have no
// replica count either — so "zero of zero are ready" is true and means nothing. Reading it
// as finished is how every deployment comes to report itself successful the instant it
// starts, which is the one answer nobody can undo later.
func TestARolloutTheClusterHasNotSeenIsNotDoneEvenAtZero(t *testing.T) {
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Generation: 1},
		Spec:       appsv1.DeploymentSpec{Replicas: replicasPtr(0)},
		Status:     appsv1.DeploymentStatus{ObservedGeneration: 0},
	}
	if rolloutOf(d).Done {
		t.Error("a Deployment the cluster has not seen was reported as a finished rollout")
	}

	// And once it has been seen, zero of zero is the answer.
	d.Status.ObservedGeneration = 1
	if !rolloutOf(d).Done {
		t.Error("a workload scaled to nothing was reported as unfinished, so every " +
			"deployment to one of them times out over a number that was never wrong")
	}
}

func replicasPtr(v int32) *int32 { return &v }
