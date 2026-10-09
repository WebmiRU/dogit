package k8s

import (
	"strings"
	"testing"

	sigsyaml "sigs.k8s.io/yaml"
)

// Putting a pull secret on a pod template.
//
// Worth testing in its own right, because every way this can be wrong looks like
// something else entirely: a manifest that keeps no pull secret is indistinguishable
// from a registry that will not serve the image, and the person reading it goes and
// looks at the registry.

func podTemplate(t *testing.T, body []byte) map[string]any {
	t.Helper()

	var object map[string]any
	if err := sigsyaml.Unmarshal(body, &object); err != nil {
		t.Fatalf("the manifest became unreadable: %v", err)
	}
	return object
}

func pullSecretNames(t *testing.T, spec map[string]any) []string {
	t.Helper()

	raw, _ := spec["imagePullSecrets"].([]any)
	names := make([]string, 0, len(raw))
	for _, one := range raw {
		if entry, ok := one.(map[string]any); ok {
			names = append(names, entry["name"].(string))
		}
	}
	return names
}

func TestAPullSecretReachesThePodTemplate(t *testing.T) {
	manifest := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: www
          image: registry/app@sha256:abc
`)

	changed := WithPullSecret(manifest, "Deployment", "dogit-registry")
	spec := podTemplate(t, changed)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

	names := pullSecretNames(t, spec)
	if len(names) != 1 || names[0] != "dogit-registry" {
		t.Errorf("the pod asks for %v, want dogit-registry", names)
	}
}

// A CronJob keeps its pod three levels down, and a manifest that stops one level early
// gets no pull secret on the jobs it creates.
func TestAPullSecretReachesACronJobsJobs(t *testing.T) {
	manifest := []byte(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: nightly
spec:
  schedule: "0 3 * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
            - name: nightly
              image: registry/app@sha256:abc
`)

	changed := WithPullSecret(manifest, "CronJob", "dogit-registry")
	job := podTemplate(t, changed)["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

	if names := pullSecretNames(t, job); len(names) != 1 || names[0] != "dogit-registry" {
		t.Errorf("the job's pod asks for %v, want dogit-registry", names)
	}
}

// Somebody else's secret in the repository is left alone: removing it would break a
// deployment dogit had no part in.
func TestASecretAlreadyInTheManifestIsKept(t *testing.T) {
	manifest := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
spec:
  template:
    spec:
      imagePullSecrets:
        - name: their-own
      containers:
        - name: www
          image: registry/app@sha256:abc
`)

	changed := WithPullSecret(manifest, "Deployment", "dogit-registry")
	spec := podTemplate(t, changed)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

	names := pullSecretNames(t, spec)
	if len(names) != 2 {
		t.Fatalf("the pod asks for %v, want both secrets", names)
	}
	if names[0] != "their-own" {
		t.Errorf("the secret from the repository went missing: %v", names)
	}
}

// One of ours is replaced rather than added twice: a namespace asking for the same
// secret twice is a manifest nobody meant to write.
func TestOurOwnSecretIsNotListedTwice(t *testing.T) {
	manifest := []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
spec:
  template:
    spec:
      imagePullSecrets:
        - name: dogit-registry
      containers:
        - name: www
          image: registry/app@sha256:abc
`)

	changed := WithPullSecret(manifest, "Deployment", "dogit-registry")
	spec := podTemplate(t, changed)["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)

	if names := pullSecretNames(t, spec); len(names) != 1 {
		t.Errorf("the pod asks for %v, want dogit-registry once", names)
	}
}

// A Service has no pod spec, and writing a field that does not belong on a kind is
// how an apply fails with a message about a field nobody mentioned.
func TestSomethingWithNoPodTemplateIsLeftAlone(t *testing.T) {
	manifest := []byte(`apiVersion: v1
kind: Service
metadata:
  name: www
spec:
  selector:
    app: www
`)

	if changed := WithPullSecret(manifest, "Service", "dogit-registry"); string(changed) != string(manifest) {
		t.Errorf("a Service was rewritten:\n%s", changed)
	}
}

// A manifest that does not change is returned byte for byte.
//
// Not a tidiness point: rewriting a document that needed nothing means losing its
// comments and its quoting for no reason, and the repository is the thing that owns
// that text.
func TestAManifestThatNeedsNoChangeIsUntouched(t *testing.T) {
	manifest := []byte(`# the front end
apiVersion: apps/v1
kind: Deployment
metadata:
  name: www          # named for the project
spec:
  template:
    spec:
      imagePullSecrets:
        - name: dogit-registry
      containers:
        - name: www
          image: "registry/app@sha256:abc"
`)

	changed := WithPullSecret(manifest, "Deployment", "dogit-registry")
	if string(changed) != string(manifest) {
		t.Errorf("a manifest that already had the secret was rewritten:\n%s", changed)
	}
	if !strings.Contains(string(changed), "# the front end") {
		t.Error("the comment was lost")
	}
}

// What is left after a rewrite is still the same objects, which is what matters: a
// manifest that gains a secret is re-serialised, and comments are lost in that.
func TestARewrittenManifestIsStillTheSameObjects(t *testing.T) {
	manifest := []byte(`# the front end
apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
spec:
  replicas: 2
  template:
    spec:
      containers:
        - name: www
          image: "registry/app@sha256:abc"
`)

	changed := string(WithPullSecret(manifest, "Deployment", "dogit-registry"))

	var object map[string]any
	if err := sigsyaml.Unmarshal([]byte(changed), &object); err != nil {
		t.Fatalf("the rewritten manifest is not valid YAML: %v", err)
	}
	spec := object["spec"].(map[string]any)
	if spec["replicas"] != float64(2) {
		t.Errorf("the replica count changed to %#v", spec["replicas"])
	}
	if object["metadata"].(map[string]any)["name"] != "www" {
		t.Error("the name changed")
	}
	if !strings.Contains(changed, "dogit-registry") {
		t.Errorf("the pull secret was not written:\n%s", changed)
	}
}
