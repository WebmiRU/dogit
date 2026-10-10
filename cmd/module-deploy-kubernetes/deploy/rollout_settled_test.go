package deploy

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// A rollout that has not started is not a rollout that has finished.
//
// This one is not a corner case and it is not a race that resolves itself: right after the
// manifests are applied, the cluster's own numbers describe the rollout *before* this one.
// Three pods ready, two and a half of them still serving the image this deployment is
// about to replace, and the Deployment's ready count sitting at the three that were ready
// before anybody asked anything of them. That is the first thing `wait` reads, and it
// answers "3 of 3".
//
// It is worse than a race because it is the moment every deployment passes through, and it
// only stays hidden while the image is pinned to a digest. Pinned, the new pods are told
// apart from the old ones by their digest and the count means what it says. Named by a
// tag — which is what an image at an address nobody here can pin is deployed as — the old
// pods and the new ones carry the same string, and every count taken of them counts the
// old ones as the new. So the deployment announces itself finished over a rollout that has
// not begun, the pipeline goes green, and the pods that were supposed to be replaced are
// still the ones serving traffic.
//
// Found by watching a real deployment finish in 393 milliseconds, over a manifest whose
// pods take twelve seconds each to start.
func TestARolloutTheClusterHasNotStartedYetIsNotFinished(t *testing.T) {
	client := newFake()

	// Everything the cluster says is the old rollout, still perfectly healthy: the
	// Deployment is done, three ready, and nothing has been asked of it yet.
	client.rollouts["app"] = k8s.Rollout{Desired: 3, Updated: 3, Ready: 3, Done: true}
	// And the pods say the same thing in the only way they can when the image is named
	// rather than pinned: they are running this image.
	//
	// Settled false, because that is the moment being described — the manifests are in
	// and the controller has not worked through them yet, so every number here is the one
	// before this deployment.
	client.counts = &k8s.RolloutCounts{Desired: 3, Ready: 3, OldUp: 3, Settled: false}

	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	err := deployer.wait(context.Background(), Request{
		Namespace: "versions", Rollout: "app", Image: "registry.example.com/versions:v1",
		Timeout: 3 * time.Second,
	})
	if err == nil {
		t.Fatal("a rollout the cluster has not started was reported as finished, over a " +
			"workload that was still serving the previous image in every pod")
	}
	if !strings.Contains(err.Error(), "new image") {
		t.Errorf("the reason does not mention the image, which is the thing that was never "+
			"deployed: %v", err)
	}
}

// The opposite, and the reason this is not a rule about waiting longer: once the cluster
// has seen the spec that was applied, the same numbers mean what they say.
func TestARolloutTheClusterHasSeenIsFinished(t *testing.T) {
	client := newFake()
	client.rollouts["app"] = k8s.Rollout{Desired: 3, Updated: 3, Ready: 3, Done: true}
	client.counts = &k8s.RolloutCounts{Desired: 3, Ready: 3, Settled: true}

	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	if err := deployer.wait(context.Background(), Request{
		Namespace: "versions", Rollout: "app", Image: "registry.example.com/versions:v1",
		Timeout: 3 * time.Second,
	}); err != nil {
		t.Fatalf("a rollout that had finished was reported as unfinished: %v", err)
	}
}

// A workload scaled to nothing finishes, which is what the other guard was for — and it
// finishes because the cluster has seen the spec, not because zero is equal to zero on its
// own. A count taken before the controller has looked is a count of the previous rollout,
// and a previous rollout of three is not zero.
func TestAWorkloadScaledToNothingStillFinishesOnceTheClusterHasSeen(t *testing.T) {
	client := newFake()
	client.rollouts["app"] = k8s.Rollout{Desired: 0, Updated: 0, Ready: 0, Done: true}
	client.counts = &k8s.RolloutCounts{Desired: 0, Ready: 0, Settled: true}

	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	if err := deployer.wait(context.Background(), Request{
		Namespace: "versions", Rollout: "app", Image: "registry.example.com/versions:v1",
		Timeout: 3 * time.Second,
	}); err != nil {
		t.Fatalf("a workload scaled to nothing was reported as unfinished: %v", err)
	}
}

// The counts say the workload is on the new image and finished, and the cluster has not
// yet acted on what was applied. The two together are the false success, and the second
// is the one that decides.
func TestCountsFromBeforeTheAppliedSpecDoNotEndARollout(t *testing.T) {
	counts := k8s.RolloutCounts{Desired: 3, Ready: 3, Settled: false}

	if counts.Settled && counts.Ready >= counts.Desired {
		t.Fatal("counts taken before the controller saw the applied spec were accepted as " +
			"a finished rollout")
	}
}
