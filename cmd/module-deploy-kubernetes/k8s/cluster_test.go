package k8s

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Against a real cluster, because this is the one part of dogit that cannot be right
// on paper.
//
// Everything above this package is tested with a fake, which is the right way round:
// a fake is what makes those tests run at all. But a fake cannot tell you that the
// patch type is wrong, that the label a ReplicaSet is found by is not the label the
// cluster sets, or that a rollout is never "done" because of a field that does not
// exist. Those are found here, once, against a cluster somebody can look at.
//
//	KCUBECONFIG=~/.kube/config go test ./cmd/module-deploy-kubernetes/k8s -run Cluster
//
// Skipped without a kubeconfig, so the ordinary suite stays runnable offline. The
// cluster used is whatever KUBECONFIG points at; the namespace is one this run
// created and removes afterwards, and nothing outside it is touched.
func clusterClientFor(t *testing.T) Client {
	t.Helper()

	kubeconfig := os.Getenv("KCUBECONFIG")
	if kubeconfig == "" {
		if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".kube", "config")); err != nil {
			t.Skip("no kubeconfig; not testing against a cluster")
		}
		kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
	}

	client, err := Connect(context.Background(), Access{Kubeconfig: kubeconfig})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return client
}

// namespace creates one for this run and removes it afterwards.
//
// A namespace per run rather than a fixed one, so two runs cannot tread on each other
// and so nothing that happened here is still there tomorrow.
func namespace(t *testing.T, client Client) string {
	t.Helper()

	// Nanoseconds, because two tests a second apart are otherwise quite likely to
	// be given the same name -- and a namespace that already exists but is being
	// deleted makes the deployment vanish underneath the next test, which looks
	// exactly like a bug in the cluster and is not one.
	name := fmt.Sprintf("dogit-test-%d", time.Now().UnixNano())

	manifest := `apiVersion: v1
kind: Namespace
metadata:
  name: ` + name + `
`
	if _, err := client.Apply(context.Background(), Object{
		APIVersion: "v1", Kind: "Namespace", Name: name, Body: []byte(manifest),
	}); err != nil {
		t.Fatalf("create the namespace: %v", err)
	}

	t.Cleanup(func() {
		// Best effort: a test that fails must not leave a namespace behind, and a
		// test that cannot remove one must not fail because of it.
		_ = exec.Command("kubectl", "delete", "namespace", name,
			"--ignore-not-found", "--wait=false").Run()
	})
	return name
}

// deploymentManifest is a real Deployment with the placeholder where the image goes.
//
// From `registry.k8s.io/pause`, which every cluster already has and which is
// harmless: this test is about what dogit did to a manifest, not about a workload
// doing anything.
func deploymentManifest(replicas int, image string) string {
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  labels:
    app: app
spec:
  replicas: ` + itoa(replicas) + `
  selector:
    matchLabels:
      app: app
  template:
    metadata:
      labels:
        app: app
    spec:
      containers:
        - name: app
          image: IMAGE
`
}

func itoa(n int) string {
	return string(rune('0' + n))
}

// Applying a manifest puts the image in and the cluster takes it.
func TestClusterAppliesWhatWasSubstituted(t *testing.T) {
	client := clusterClientFor(t)
	space := namespace(t, client)

	body := Substitution{
		Placeholder: "IMAGE",
		Image:       "registry.k8s.io/pause:3.9",
	}.Apply([]byte(deploymentManifest(1, "")))

	state, err := client.Apply(context.Background(), Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
		Body: body,
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !state.Applied {
		t.Error("the cluster says nothing was applied")
	}

	live, err := client.Get(context.Background(), Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
	})
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	containers, found, err := unstructured.NestedSlice(live.Object,
		"spec", "template", "spec", "containers")
	if err != nil || !found {
		t.Fatalf("read the containers: found=%v err=%v", found, err)
	}
	if len(containers) != 1 {
		t.Fatalf("the cluster has %d containers", len(containers))
	}
	first, ok := containers[0].(map[string]any)
	if !ok {
		t.Fatalf("the cluster's container is %T, not an object", containers[0])
	}
	if image, _ := first["image"].(string); image != "registry.k8s.io/pause:3.9" {
		t.Errorf("the cluster is running %q, so the placeholder was not what got applied", image)
	}
}

// A rollout is finished when what was asked for is created and serving — and the
// reason it is not is in the conditions, which is the difference between a red row and
// a red row somebody can act on.
func TestClusterReportsTheRolloutAndWhyItIsNotDone(t *testing.T) {
	client := clusterClientFor(t)
	space := namespace(t, client)

	// An image that does not exist: the rollout will stall, and the cluster will say
	// so in a condition rather than leaving us to guess from a number.
	body := Substitution{
		Placeholder: "IMAGE",
		Image:       "127.0.0.1:5000/nothing-here:does-not-exist",
	}.Apply([]byte(deploymentManifest(2, "")))

	if _, err := client.Apply(context.Background(), Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
		Body: body,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	rollout, err := client.Rollout(context.Background(), space, "app")
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	if rollout.Desired != 2 {
		t.Errorf("the deployment asked for %d replicas, want 2", rollout.Desired)
	}
	if rollout.Done {
		t.Error("a rollout with an image that cannot be pulled reports itself finished")
	}
	if rollout.Reason == "" {
		t.Error("a rollout that is not done says nothing about why")
	}
	t.Logf("the cluster's reason: %s", rollout.Reason)
}

// A good rollout reports itself finished, or the whole module is a red dot forever.
func TestClusterReportsAFinishedRolloutAsFinished(t *testing.T) {
	client := clusterClientFor(t)
	space := namespace(t, client)

	body := Substitution{
		Placeholder: "IMAGE",
		Image:       "registry.k8s.io/pause:3.9",
	}.Apply([]byte(deploymentManifest(1, "")))

	if _, err := client.Apply(context.Background(), Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
		Body: body,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	deadline := time.Now().Add(90 * time.Second)
	var (
		rollout Rollout
		err     error
	)
	for time.Now().Before(deadline) {
		rollout, err = client.Rollout(context.Background(), space, "app")
		if err != nil {
			t.Fatalf("rollout: %v", err)
		}
		if rollout.Done {
			break
		}
		time.Sleep(time.Second)
	}

	if !rollout.Done {
		t.Errorf("a running workload never reported itself finished: %+v", rollout)
	}
}

// applyTwice puts two different images through, which is what a revision history is.
//
// It waits for each rollout to finish in between, and that wait is not politeness: a
// Deployment's revisions are ReplicaSets, and the ReplicaSet for the second image is
// created by the cluster's controller a moment after the Deployment changes. Two
// applies back to back therefore produce one revision, and a module that never waits
// would find no history to roll back to.
func applyTwice(client Client, space string) error {
	for _, image := range []string{"registry.k8s.io/pause:3.9", "registry.k8s.io/pause:3.10"} {
		body := Substitution{Placeholder: "IMAGE", Image: image}.Apply([]byte(deploymentManifest(1, "")))
		if _, err := client.Apply(context.Background(), Object{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: space, Name: "app",
			Body: body,
		}); err != nil {
			return fmt.Errorf("apply %s: %w", image, err)
		}
		if err := waitForRollout(client, space, 90*time.Second); err != nil {
			return fmt.Errorf("after applying %s: %w", image, err)
		}
	}
	return nil
}

// waitForRollout waits for a workload to become ready, and says why it did not.
func waitForRollout(client Client, space string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		rollout, err := client.Rollout(context.Background(), space, "app")
		if err != nil {
			return err
		}
		if rollout.Done {
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("the rollout did not finish")
}

// The history is what a rollback is made of, so it has to say which image each
// revision ran and not only which number.
func TestClusterListsRevisionsWithTheirImages(t *testing.T) {
	client := clusterClientFor(t)
	space := namespace(t, client)

	if err := applyTwice(client, space); err != nil {
		t.Fatal(err)
	}

	revisions, err := client.Revisions(context.Background(), space, "app")
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}
	if len(revisions) < 2 {
		t.Fatalf("the cluster kept %d revisions after two deployments", len(revisions))
	}

	images := map[string]bool{}
	current := 0
	for _, one := range revisions {
		images[one.Image] = true
		if one.Current {
			current++
		}
	}
	if !images["registry.k8s.io/pause:3.9"] || !images["registry.k8s.io/pause:3.10"] {
		t.Errorf("the history does not name both images: %v", images)
	}
	if current != 1 {
		t.Errorf("%d revisions claim to be current, want 1", current)
	}
}

// Rolling back returns the previous template — and nothing else. This test exists
// because the honest answer to "what does rollback undo" has to be checked somewhere.
func TestClusterRollbackReturnsThePreviousImage(t *testing.T) {
	client := clusterClientFor(t)
	space := namespace(t, client)

	if err := applyTwice(client, space); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Rollback(context.Background(), space, "app"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	revisions, err := client.Revisions(context.Background(), space, "app")
	if err != nil {
		t.Fatalf("revisions: %v", err)
	}

	current := ""
	for _, one := range revisions {
		if one.Current {
			current = one.Image
		}
	}
	if current != "registry.k8s.io/pause:3.9" {
		t.Errorf("after a rollback the cluster runs %q, want the image before the last deploy", current)
	}
}

// A namespace that is not there is somebody else's decision, not ours to make.
func TestAMissingNamespaceIsRefusedRatherThanCreated(t *testing.T) {
	client := clusterClientFor(t)

	body := Substitution{Placeholder: "IMAGE", Image: "registry.k8s.io/pause:3.9"}.
		Apply([]byte(deploymentManifest(1, "")))

	_, err := client.Apply(context.Background(), Object{
		APIVersion: "apps/v1", Kind: "Deployment",
		Namespace: "dogit-no-such-namespace", Name: "app", Body: body,
	})
	if err == nil {
		t.Fatal("a deploy into a namespace that does not exist was accepted")
	}
	t.Logf("the refusal: %v", err)
}
