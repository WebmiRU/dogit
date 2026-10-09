package k8s

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// A write built on a version the cluster has moved past is refused, and the refusal is not a
// failure.
//
// Kubernetes stamps every object with a version and will not let a write built on a stale one
// through — which is exactly what it is for. The deployment controller is writing to this same
// object the whole time, updating status and conditions, so this happened about one run in four
// on a stand, always right after a rollout, which is when the controller is busiest. The answer
// was never to give up but to read again and try once more.
func TestAConflictIsTriedAgainRatherThanReported(t *testing.T) {
	updates := 0
	client := &clusterClient{typed: conflictTwiceThenAccept(&updates)}

	// The wait afterwards is not what is under test, and a cluster with no pods and no controller
	// never satisfies it — so the answer being checked is whether the *write* got through. What
	// comes back says which of the two it was: a refusal names the object it could not write, and
	// a rollout that never arrived names the workload and how little of it is up.
	_, err := client.SetImage(context.Background(), "versions-dev", "versions",
		"registry.k8s.io/pause:3.10", rolloutWait)
	assertWrittenNotRefused(t, err, "after two conflicts")
	if updates < 3 {
		t.Errorf("made %d update attempts after two conflicts, want at least 3", updates)
	}
}

// assertWrittenNotRefused says the failure is about the rollout rather than about the write.
func assertWrittenNotRefused(t *testing.T, err error, when string) {
	t.Helper()
	if err == nil {
		return
	}
	if apierrors.IsConflict(errors.Unwrap(err)) {
		t.Fatalf("%s the write was still refused: %v", when, err)
	}
	if !strings.Contains(err.Error(), "never finished rolling it out") {
		t.Fatalf("%s the write did not happen and the reason is not the rollout: %v", when, err)
	}
}

// And the thing that is eventually written is the image asked for, on the object as it is now.
//
// A retry that re-sent what it had would conflict again, so passing is not evidence that the
// right thing was written — and a workload left on the old image while the operation reports
// success is the failure that matters.
func TestWhatIsWrittenAfterAConflictIsTheImageAskedFor(t *testing.T) {
	updates := 0
	client := &clusterClient{typed: conflictTwiceThenAccept(&updates)}

	_, err := client.SetImage(context.Background(), "versions-dev", "versions",
		"registry.k8s.io/pause:3.10", rolloutWait)
	assertWrittenNotRefused(t, err, "after two conflicts")

	written, err := client.typed.AppsV1().Deployments("versions-dev").Get(context.Background(),
		"versions", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("read the workload back: %v", err)
	}
	got := written.Spec.Template.Spec.Containers[0].Image
	if got != "registry.k8s.io/pause:3.10" {
		t.Errorf("the workload is on %q, want the image that was asked for", got)
	}
}

// A conflict that never stops is reported rather than retried for ever.
//
// Retrying is for a writer that has stopped by now. An object being rewritten back to back is
// something else, and it is something the caller needs to hear about.
func TestAConflictThatNeverEndsIsReported(t *testing.T) {
	updates := 0
	client := &clusterClient{typed: conflictAlways(&updates)}

	_, err := client.SetImage(context.Background(), "versions-dev", "versions",
		"registry.k8s.io/pause:3.10", rolloutWait)
	if err == nil {
		t.Fatal("an object that cannot be written to reported success")
	}
	if !strings.Contains(err.Error(), "versions") {
		t.Errorf("the failure does not say which workload: %v", err)
	}
	if updates < 2 {
		t.Errorf("gave up after %d attempt, want more than one", updates)
	}
}

// Anything that is not a conflict comes back at once.
//
// Missing, forbidden and refused are answers. Asking again changes nothing, and a rollback that
// cannot see a workload should say so at once rather than appearing to try.
func TestAnAnswerThatIsNotAConflictIsNotRetried(t *testing.T) {
	updates := 0
	client := &clusterClient{typed: refusingUpdates(&updates)}

	_, err := client.SetImage(context.Background(), "versions-dev", "versions",
		"registry.k8s.io/pause:3.10", rolloutWait)
	if err == nil {
		t.Fatal("a refused write reported success")
	}
	if updates != 1 {
		t.Errorf("tried %d times to write to a workload that refused, want 1", updates)
	}
}

// Long enough for the retry to finish and for a rollout of one pod to be seen. Not the module's
// own budget: this is about whether a write lands, and a test that waits ten minutes to say so
// is a test nobody runs.
const rolloutWait = time.Second

// conflictTwiceThenAccept refuses the first writes and then behaves, counting every attempt.
func conflictTwiceThenAccept(attempts *int) *fake.Clientset {
	client := fake.NewSimpleClientset(workloadNamed("versions", "registry.k8s.io/pause:3.9"))
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		*attempts++
		if *attempts <= 2 {
			return true, nil, apierrors.NewConflict(
				schema.GroupResource{Group: "apps", Resource: "deployments"}, "versions",
				errors.New("the object was changed underneath this write"))
		}
		return false, nil, nil
	})
	return client
}

// conflictAlways never lets a write through.
func conflictAlways(attempts *int) *fake.Clientset {
	client := fake.NewSimpleClientset(workloadNamed("versions", "registry.k8s.io/pause:3.9"))
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		*attempts++
		return true, nil, apierrors.NewConflict(
			schema.GroupResource{Group: "apps", Resource: "deployments"}, "versions",
			errors.New("the object is being rewritten continuously"))
	})
	return client
}

// refusingUpdates refuses for a reason that is not a conflict, and must not be retried.
func refusingUpdates(attempts *int) *fake.Clientset {
	client := fake.NewSimpleClientset(workloadNamed("versions", "registry.k8s.io/pause:3.9"))
	client.PrependReactor("update", "deployments", func(ktesting.Action) (bool, runtime.Object, error) {
		*attempts++
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: "apps", Resource: "deployments"}, "versions",
			errors.New("this account may not change that workload"))
	})
	return client
}

func workloadNamed(name, image string) *appsv1.Deployment {
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "versions-dev"},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "app", Image: image}},
				},
			},
		},
	}
}
