package k8s

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

const (
	oldImage = "registry.f220.ru/test/versions@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	newImage = "registry.f220.ru/test/versions@sha256:2222222222222222222222222222222222222222222222222222222222222222"

	// What a cluster hands back, which is not what was asked for: a pod reports the address it
	// pulled and some runtimes put a scheme in front of it.
	pulledImage = "docker-pullable://registry.f220.ru/test/versions@sha256:2222222222222222222222222222222222222222222222222222222222222222"
	taggedImage = "registry.k8s.io/pause:3.9"
)

// A deployment is not finished because the old pods are still serving.
//
// This is the shape of what a reader saw: a card that said everything was fine while the new
// pods were still Pending. `status.readyReplicas` answers three of three the moment the old
// pods are up, because they are ready and they belong to the Deployment — and the Deployment
// counts every pod it owns, from every revision. So the count was satisfied before the thing
// that was deployed had started, and a deployment reported itself done on a cluster where it
// had not happened.
func TestOldPodsSatisfyNothing(t *testing.T) {
	ready := int32(3)
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 3, ready),
		podOf("versions-old-1", oldImage, corev1.PodRunning, true),
		podOf("versions-old-2", oldImage, corev1.PodRunning, true),
		podOf("versions-old-3", oldImage, corev1.PodRunning, true),
		// The new pod is up in the API and has not left Pending.
		podOf("versions-new-1", newImage, corev1.PodPending, false),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}

	if counts.Ready != 0 {
		t.Errorf("counted %d pods ready on the new image, want 0: the only new pod is Pending", counts.Ready)
	}
	if counts.OldUp != 3 {
		t.Errorf("counted %d old pods, want 3", counts.OldUp)
	}
	if counts.Desired != 3 {
		t.Errorf("want 3 pods, got %d", counts.Desired)
	}
}

// And it is finished once the new pods are up, serving, and the old ones are gone.
func TestNewPodsOnTheImageCountWhenTheyServe(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 3, 3),
		podOf("versions-new-1", newImage, corev1.PodRunning, true),
		podOf("versions-new-2", newImage, corev1.PodRunning, true),
		podOf("versions-new-3", newImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 3 {
		t.Errorf("counted %d ready, want 3", counts.Ready)
	}
	if counts.OldUp != 0 {
		t.Errorf("counted %d old pods still up, want 0", counts.OldUp)
	}
}

// A pod that exists but would not take traffic is not one of the pods that did.
//
// A new pod between starting and passing its readiness probe is the ordinary state of a rollout
// that is working, and counting it is how a deployment says it is done while the cluster is
// still pulling images.
func TestAPodThatIsNotServingIsNotReady(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 2, 2),
		podOf("versions-new-1", newImage, corev1.PodRunning, true),
		podOf("versions-new-2", newImage, corev1.PodRunning, false),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 1 {
		t.Errorf("counted %d ready, want 1: a pod not passing its probe serves nothing", counts.Ready)
	}
}

// A pod on its way out is not on the new image any more.
//
// The cluster takes a terminating pod out of the workload's available replicas the moment it
// starts deleting it. Counting one here let a place that had finished rolling out go on
// announcing that the image it had just replaced was still there, for as long as the pod took
// to go — which is a minute of a badge with nothing left to say.
func TestAPodBeingDeletedIsNotCounted(t *testing.T) {
	dying := podOf("versions-old-1", oldImage, corev1.PodRunning, true)
	now := metav1.Now()
	dying.DeletionTimestamp = &now

	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 1, 1),
		dying,
		podOf("versions-new-1", newImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.OldUp != 0 {
		t.Errorf("counted %d pods still up, want 0: a pod being deleted is already gone from the count", counts.OldUp)
	}
}

// Eleven of ten is a number nobody can act on.
func TestNeverMoreReadyThanWereAskedFor(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 1, 1),
		podOf("versions-new-1", newImage, corev1.PodRunning, true),
		podOf("versions-new-2", newImage, corev1.PodRunning, true),
		podOf("versions-new-3", newImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 1 {
		t.Errorf("counted %d ready for a workload of 1, want 1", counts.Ready)
	}
}

// A sidecar that has not come up means the pod serves nothing.
//
// "Any container ready" was the rule in the tally a watcher is drawn from; here it is "every
// container ready". A pod with one container cannot tell them apart, which is why the two
// counts drifted apart unnoticed for as long as they did — nothing on a three-pod Deployment
// ever disagreed. A pod with two is where it shows.
func TestASidecarNotUpMeansThePodDoesNotServe(t *testing.T) {
	pod := podOf("versions-new-1", newImage, corev1.PodRunning, true)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "sidecar", Image: oldImage})
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{
		Name: "sidecar", ImageID: oldImage, Ready: false,
	})

	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 1, 1),
		pod,
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 0 {
		t.Errorf("counted %d ready, want 0: a pod waiting on its sidecar serves nothing", counts.Ready)
	}
}

// A pod whose image it has not reported is not on the old image either.
//
// Saying nothing is not the same as saying it is on the old one, and the two were once counted
// the same way, which put pods that were still pulling into the number of pods being retired.
func TestAPodThatHasNotNamedItsImageIsNotAnOldPod(t *testing.T) {
	silent := podOf("versions-new-1", "", corev1.PodPending, false)
	silent.Status.ContainerStatuses[0].ImageID = ""

	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 1, 1),
		silent,
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.OldUp != 0 {
		t.Errorf("counted %d old pods, want 0: a pod that has not said which image it is on has not said it is on the old one", counts.OldUp)
	}
}

// The two counts that a deployment is judged by and a card is drawn from ask about the same
// thing, from the same image string, in the same way.
//
// They did not once. One counted ready pods of any revision, the other ready pods of the new
// image, and a deployment finished while the numbers beside it said otherwise. A test that
// compares the two directly is what stops them being allowed to drift apart a second time.
func TestTheTwoCountsCannotDisagree(t *testing.T) {
	for _, newPods := range []int{0, 1, 2, 3} {
		objects := []runtime.Object{deploymentOf("versions", 3, 3)}
		for index := range 3 {
			objects = append(objects, podOf(
				"versions-old-"+string(rune('1'+index)), oldImage, corev1.PodRunning, true))
		}
		for index := range newPods {
			objects = append(objects, podOf(
				fmt.Sprintf("versions-new-%d", index+1), newImage, corev1.PodRunning, true))
		}
		client := &clusterClient{typed: fake.NewSimpleClientset(objects...)}

		counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
		if err != nil {
			t.Fatalf("count with %d new pods: %v", newPods, err)
		}

		// What the watcher a card is drawn from would say, from the same pods, by the same
		// rules — arrived at here through the other implementation on purpose.
		cache := map[string]podSeen{}
		list, _ := client.typed.CoreV1().Pods("versions-dev").List(context.Background(), metav1.ListOptions{})
		for index := range list.Items {
			pod := list.Items[index]
			remember(cache, &pod, "")
		}
		watched := tally(cache, counts.Desired, newImage, "")

		if counts.Ready != watched.Ready || counts.OldUp != watched.OldUp {
			t.Errorf("with %d new pods: the deployment is judged on %d ready/%d old, the card is drawn from %d ready/%d old",
				newPods, counts.Ready, counts.OldUp, watched.Ready, watched.OldUp)
		}
	}
}

// A rollback can make an older ReplicaSet the current revision again. The Deployment's
// revision annotation, not creation time or a mutable image tag, says which ReplicaSet is
// being rolled out to. Both the live counter and the watcher must reach the same answer.
func TestCountsAndWatcherAgreeForTaggedRollbackToAnOlderReplicaSet(t *testing.T) {
	const (
		targetSet    = "versions-target"
		currentSet   = "versions-current"
		targetImage  = "registry.f220.ru/test/versions:v-old"
		currentImage = "registry.f220.ru/test/versions:v-new"
		targetDigest = "docker-pullable://registry.f220.ru/test/versions@sha256:2222"
		oldDigest    = "docker-pullable://registry.f220.ru/test/versions@sha256:1111"
	)

	deployment := deploymentOf("versions", 3, 3)
	deployment.UID = types.UID("deployment-uid")
	deployment.Generation = 2
	deployment.Annotations = map[string]string{"deployment.kubernetes.io/revision": "8"}
	deployment.Status.ObservedGeneration = 2
	deployment.Status.UpdatedReplicas = 0
	deployment.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: targetImage}}

	owner := []metav1.OwnerReference{{
		Kind: "Deployment", Name: deployment.Name, UID: deployment.UID,
	}}
	// This ReplicaSet was created earlier, but has been promoted to revision 8 by the
	// rollback. The newer-created set is revision 7 and still has the ready old pods.
	target := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: targetSet, Namespace: "versions-dev", UID: types.UID(targetSet),
			CreationTimestamp: metav1.NewTime(time.Unix(100, 0)),
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "8"},
			OwnerReferences: owner,
		},
	}
	current := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name: currentSet, Namespace: "versions-dev", UID: types.UID(currentSet),
			CreationTimestamp: metav1.NewTime(time.Unix(200, 0)),
			Annotations: map[string]string{"deployment.kubernetes.io/revision": "7"},
			OwnerReferences: owner,
		},
	}

	objects := []runtime.Object{deployment, target, current}
	addPod := func(name, setName string, setUID types.UID, image, specImage string, ready bool) {
		pod := podOf(name, image, corev1.PodRunning, ready)
		pod.Spec.Containers[0].Image = specImage
		pod.OwnerReferences = []metav1.OwnerReference{{
			Kind: "ReplicaSet", Name: setName, UID: setUID,
		}}
		objects = append(objects, pod)
	}
	for i := 0; i < 3; i++ {
		addPod(fmt.Sprintf("target-%d", i), targetSet, target.UID, targetDigest, targetImage, false)
		addPod(fmt.Sprintf("current-%d", i), currentSet, current.UID, oldDigest, currentImage, true)
	}

	client := &clusterClient{typed: fake.NewSimpleClientset(objects...)}
	counts, err := client.Counts(context.Background(), "versions-dev", "versions", targetImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 0 {
		t.Errorf("ready: got %d, want 0; the target revision's pods have not passed readiness", counts.Ready)
	}
	if counts.OldUp != 3 {
		t.Errorf("retiring: got %d, want 3; the old ready pods must be counted once per pod", counts.OldUp)
	}

	revision, err := currentRevision(context.Background(), client.typed, "versions-dev", "versions")
	if err != nil {
		t.Fatalf("read current revision: %v", err)
	}
	if revision != targetSet {
		t.Errorf("current revision: got %q, want %q; the rollback target has the Deployment's revision even though it was created earlier", revision, targetSet)
	}

	list, err := client.typed.CoreV1().Pods("versions-dev").List(context.Background(),
		metav1.ListOptions{LabelSelector: labelSelectorOf(deployment)})
	if err != nil {
		t.Fatalf("list pods for the watcher: %v", err)
	}
	seen := make(map[string]podSeen, len(list.Items))
	for index := range list.Items {
		remember(seen, &list.Items[index], revision)
	}
	watched := tally(seen, counts.Desired, targetImage, revision)
	if counts.Ready != watched.Ready || counts.OldUp != watched.OldUp {
		t.Errorf("counts differ: Counts says %d ready/%d retiring, watcher says %d ready/%d retiring",
			counts.Ready, counts.OldUp, watched.Ready, watched.OldUp)
	}
}

func deploymentOf(name string, replicas, ready int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "versions-dev"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
		},
		Status: appsv1.DeploymentStatus{Replicas: replicas, ReadyReplicas: ready},
	}
}

func podOf(name, image string, phase corev1.PodPhase, ready bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "versions-dev", Labels: map[string]string{"app": "versions"},
			// Real pods have one and the counts are keyed by it: without it a cache of pods
			// collapses to a single entry, which is a thing about the test rather than about
			// the cluster and hides everything the counting is being checked for.
			UID: types.UID(name),
		},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: image}}},
		Status: corev1.PodStatus{
			Phase:             phase,
			ContainerStatuses: []corev1.ContainerStatus{{Name: "app", ImageID: image, Ready: ready}},
		},
	}
}

// The Deployment's own ready count must not be the answer, and here it cannot be even by
// accident: nothing in this cluster has the new image on it at all.
func TestTheReadyCountOnTheDeploymentIsNotTheAnswer(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		// The Deployment claims three of three and there is no new pod at all.
		deploymentOf("versions", 3, 3),
		podOf("versions-old-1", oldImage, corev1.PodRunning, true),
		podOf("versions-old-2", oldImage, corev1.PodRunning, true),
		podOf("versions-old-3", oldImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 0 {
		t.Errorf("the Deployment's own ready count was used: got %d, want 0", counts.Ready)
	}
}

// Same image, two spellings: a pod reports the address it pulled and some runtimes put a scheme
// in front of it, and asking whether it is the new image cannot turn on whether somebody wrote
// `docker-pullable://` in front.
//
// This is not a corner: it is the difference between a rollout counting its own pods and counting
// none of them, which looks exactly like a rollout that has not started. It did.
func TestTheSameImageInTwoSpellings(t *testing.T) {
	if !SameImage(newImage, pulledImage) {
		t.Error("a pod reporting the same digest with a scheme in front of it is not being counted")
	}
	if !SameImage(pulledImage, newImage) {
		t.Error("the comparison is not symmetric, so one side of it is not asking anything")
	}
	if SameImage(newImage, oldImage) {
		t.Error("two different digests are being called the same image")
	}
}

// A tag is a promise and two runs of it can be different images, so two references with no digest
// are the same image only when they are written the same.
func TestTwoOfTheSameTagAreNotOneImage(t *testing.T) {
	if !SameImage(taggedImage, taggedImage) {
		t.Error("the same tag twice is the same image")
	}
	if SameImage(taggedImage, "registry.k8s.io/pause:3.8") {
		t.Error("two different tags are being called the same image")
	}
}

// And a promise cannot be told from an address by comparing them.
func TestATagIsNotAnAddress(t *testing.T) {
	if SameImage(taggedImage, newImage) {
		t.Error("a tag was matched to a digest, which is the one thing a tag cannot be compared to")
	}
	if SameImage(newImage, taggedImage) {
		t.Error("a digest was matched to a tag")
	}
	if DigestOf(taggedImage) != "" {
		t.Errorf("a tag was read as a digest: %q", DigestOf(taggedImage))
	}
	if DigestOf(newImage) == "" {
		t.Error("a pinned reference was read as having no digest")
	}
}

// Asked of a tag, the count cannot tell the new pods from the old ones — the pods being replaced
// are already running the image this deployment names — and the Deployment's own ready count is
// the only number there is.
//
// Which is what it used unconditionally, and that was the bug: it counts ready pods of the
// revision being replaced too, so on a pinned image it is satisfied before the new pods have
// started. The fallback is a fallback, and only for the case where there is nothing better.
func TestATagFallsBackToWhatCanBeKnown(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 3, 3),
		podOf("versions-old-1", taggedImage, corev1.PodRunning, true),
		podOf("versions-old-2", taggedImage, corev1.PodRunning, true),
		podOf("versions-old-3", taggedImage, corev1.PodRunning, true),
		podOf("versions-new-1", taggedImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", taggedImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 3 {
		t.Errorf("counted %d ready for a tag, want the Deployment's own 3", counts.Ready)
	}
	// Every pod is on the name this deployment gives, so none of them is distinguished as old —
	// which is the truth, and why the sentence a reader gets does not claim a rollout happened.
	if counts.OldUp != 0 {
		t.Errorf("counted %d old pods, want 0: nothing can be called old when the image is a name", counts.OldUp)
	}
}

// And a pinned image still refuses the Deployment's number.
func TestAPinnedImageNeverFallsBack(t *testing.T) {
	client := &clusterClient{typed: fake.NewSimpleClientset(
		deploymentOf("versions", 3, 3),
		podOf("versions-old-1", oldImage, corev1.PodRunning, true),
		podOf("versions-old-2", oldImage, corev1.PodRunning, true),
		podOf("versions-old-3", oldImage, corev1.PodRunning, true),
		podOf("versions-new-1", pulledImage, corev1.PodRunning, true),
	)}

	counts, err := client.Counts(context.Background(), "versions-dev", "versions", newImage)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if counts.Ready != 1 {
		t.Errorf("counted %d ready, want 1: the Deployment's own 3 was used", counts.Ready)
	}
	if counts.OldUp != 3 {
		t.Errorf("counted %d old pods, want 3", counts.OldUp)
	}
}
