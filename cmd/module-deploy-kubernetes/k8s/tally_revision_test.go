package k8s

import "testing"

// A tag cannot say which pod is which.
//
// A pod reports the digest it is actually running; the name it is deployed under is a tag
// for anything this instance cannot pin — an image at an address nobody here stores. The
// two can never be equal, so asking "is this pod on the new image" by name answers no for
// every pod, forever, and the answer does not depend on the cluster at all.
//
// Which is why the page said "0 of 3 running the new image" over a rollout that brought
// three pods up, and then ended the card in Success. The record said 3 of 3. Nothing was
// broken; the number was answering a question it was never able to answer.
//
// The cluster can be asked instead, and does know: a pod belongs to a ReplicaSet, and the
// newest ReplicaSet of a Deployment is the revision being rolled to. That answer holds
// whatever the image is called.
func TestPodsAreCountedByRevisionWhenTheImageIsNamedRatherThanPinned(t *testing.T) {
	const (
		oldSet = "versions-6dd78546bc"
		newSet = "versions-95fb5f4b8"
		// A tag, which is what an unpinnable image is deployed under.
		tagged = "registry.f220.ru/test/versions:v-again.3"
		// What a pod actually reports: the digest it is running.
		digestOld = "docker-pullable://registry.f220.ru/test/versions@sha256:1111"
		digestNew = "docker-pullable://registry.f220.ru/test/versions@sha256:2222"
	)

	// Three pods on the new revision, serving, and three still on the old one. Both sets
	// name the same image, because the deployment is named by a tag.
	pods := map[string]podSeen{}
	for i := 0; i < 3; i++ {
		pods[string(rune('a'+i))] = podSeen{revision: newSet, new: true, ready: true, image: digestNew}
		pods[string(rune('A'+i))] = podSeen{revision: oldSet, new: false, ready: true, image: digestOld}
	}

	got := tally(pods, 3, tagged, newSet)

	if got.Ready != 3 {
		t.Errorf("ready: got %d, want 3. Asked by image name the answer is 0 for every pod, "+
			"because a tag is never equal to the digest a pod reports — so the count does not "+
			"move off zero on a rollout that is working.", got.Ready)
	}
	if got.OldUp != 3 {
		t.Errorf("retiring: got %d, want 3 — the pods being replaced are the ones on the "+
			"previous revision, and none of them is one of the new ones", got.OldUp)
	}
	if got.Desired != 3 {
		t.Errorf("desired: got %d, want 3", got.Desired)
	}
}

// The other half: a deployment that has only ever had one revision has nothing to compare
// against, and there the image is still the answer. Dropping to it is what keeps a pod in
// its first instants — before the cluster has given it an owner — from being counted among
// the pods being replaced, which would make the drain go up and down by one every rollout.
func TestAPodWithNoRevisionYetIsStillCountedByItsImage(t *testing.T) {
	const digest = "docker-pullable://registry.f220.ru/test/versions@sha256:2222"

	pods := map[string]podSeen{
		// Arrived moments ago: no owner reference yet.
		"a": {revision: "", new: false, ready: true, image: digest},
	}
	got := tally(pods, 1, digest, "versions-95fb5f4b8")

	if got.Ready != 1 {
		t.Errorf("ready: got %d, want 1. A pod the cluster has not yet attributed to a "+
			"revision is counted among the ones being replaced, and the drain goes up and "+
			"down by one for every rollout.", got.Ready)
	}
}

// And where there is no revision to compare against at all, the image is the whole answer.
func TestWithoutARevisionTheImageIsStillTheQuestion(t *testing.T) {
	const digest = "docker-pullable://registry.f220.ru/test/versions@sha256:2222"

	pods := map[string]podSeen{"a": {ready: true, image: digest}}
	if got := tally(pods, 1, digest, ""); got.Ready != 1 {
		t.Errorf("ready: got %d, want 1", got.Ready)
	}
}
