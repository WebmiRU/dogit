package api

import "testing"

// What the core says about a manifest, and what the module logs about applying it.
//
// Both are read by hand rather than parsed, so the thing worth testing is the hand:
// a name that comes back empty is applied to a nameless object, and a rollout that
// waits for "" finishes immediately and reports success over a deployment that never
// happened.

// A deployment is the shape almost every manifest has: apiVersion and kind at the top,
// and the name one level down inside metadata.
func TestAManifestIsNamedFromItsMetadata(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
  labels:
    app: www
spec:
  replicas: 1
  template:
    spec:
      containers:
        - name: www
          image: IMAGE
`

	apiVersion, kind, name := manifestIdentity(manifest)

	if apiVersion != "apps/v1" {
		t.Errorf("apiVersion was read as %q", apiVersion)
	}
	if kind != "Deployment" {
		t.Errorf("kind was read as %q", kind)
	}
	if name != "www" {
		t.Errorf("name was read as %q, want www — a deployment applied to a nameless object is not a deployment", name)
	}
}

// Several documents in one file, which is what a manifest with a Service next to a
// Deployment looks like. The first is the one that matters: it is the one the rollout
// waits for.
func TestTheFirstDocumentIsTheOneThatIsNamed(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: www
---
apiVersion: v1
kind: Service
metadata:
  name: www
`

	_, kind, name := manifestIdentity(manifest)
	if kind != "Deployment" || name != "www" {
		t.Errorf("read %s/%s, want Deployment/www from the first document", kind, name)
	}
}

// A file that is not a manifest at all. Refused by the caller rather than deployed as
// something with no kind.
func TestSomethingWithNoKindIsNotAManifest(t *testing.T) {
	if _, kind, _ := manifestIdentity("just a note\nand another line\n"); kind != "" {
		t.Errorf("a note was read as kind %q", kind)
	}
}

// A resource that genuinely has no name: a Namespace does not, and a cluster is
// perfectly happy to be told so. Nothing is invented for it.
func TestAResourceWithoutANameHasNoName(t *testing.T) {
	manifest := `apiVersion: v1
kind: Namespace
`

	_, kind, name := manifestIdentity(manifest)
	if kind != "Namespace" {
		t.Errorf("kind was read as %q", kind)
	}
	if name != "" {
		t.Errorf("a namespace was given the name %q; it has none", name)
	}
}
