package k8s

import (
	"strings"
	"testing"
)

// What dogit changes in what a repository wrote.
//
// One thing: the image placeholder, everywhere it appears. Everything else in a
// manifest is the repository's own, and a module that rearranged it would be a module
// applying something nobody wrote. These are the tests on that one rule — including
// the case that motivated it, where substituting only the first container leaves a
// migration running on a tag that has moved since.

func TestThePlaceholderGoesWhereverItAppears(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: migrate
          image: IMAGE
          command: ["/app", "migrate"]
      containers:
        - name: app
          image: IMAGE
        - name: sidecar
          image: IMAGE
`

	got := string(Substitution{Placeholder: "IMAGE", Image: "reg/app@sha256:bbb"}.Apply([]byte(manifest)))

	if strings.Contains(got, "image: IMAGE") {
		t.Errorf("a container was left on the placeholder:\n%s", got)
	}
	if strings.Count(got, "reg/app@sha256:bbb") != 3 {
		t.Errorf("expected three substituted containers, got:\n%s", got)
	}
	// Everything else has to survive byte for byte: this is a substitution, not a
	// reformatting.
	if !strings.Contains(got, `command: ["/app", "migrate"]`) {
		t.Errorf("the substitution changed something else:\n%s", got)
	}
}

// An image with no digest in it is what makes a rollback land on the wrong build, so
// the substitution does not care what it is handed — and this is where the module
// refuses to apply a floating tag at all.
func TestASubstitutionWithNothingToSubstituteIsANoOp(t *testing.T) {
	body := []byte("image: IMAGE\n")

	noPlaceholder := Substitution{Placeholder: "", Image: "x"}
	if got := noPlaceholder.Apply(body); string(got) != string(body) {
		t.Error("substituting with no placeholder changed the manifest")
	}

	noImage := Substitution{Placeholder: "IMAGE", Image: ""}
	if got := noImage.Apply(body); string(got) != string(body) {
		t.Error("substituting with no image changed the manifest")
	}
}

// Every image a manifest names, listed once each, in the order they appear.
//
// This is what gets resolved to digests before anything is applied, so it has to see
// init containers and sidecars too — and it has to see them only once, or a manifest
// with three containers on the same image would look like three things to check.
func TestImagesAreListedOnceEach(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
spec:
  template:
    spec:
      initContainers:
        - name: migrate
          image: reg/app:dev
      containers:
        - name: app
          image: reg/app:dev
        - name: sidecar
          image: "docker.io/library/redis:7"
          env:
            - name: IMAGE_OF_SOMETHING
              value: not-an-image
`

	got := ImagesIn([]byte(manifest))

	want := []string{"reg/app:dev", "docker.io/library/redis:7"}
	if len(got) != len(want) {
		t.Fatalf("images found = %v, want %v", got, want)
	}
	for i, one := range want {
		if got[i] != one {
			t.Errorf("image %d is %q, want %q", i, got[i], one)
		}
	}
}
