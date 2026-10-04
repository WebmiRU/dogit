package deploy

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Fixtures shared by the cluster tests: real manifests for a real cluster.
//
// From busybox and the pause image, both of which every cluster already has, and
// neither of which does anything. What these tests are about is what dogit did to a
// manifest, not about a workload having behaviour.

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// deploymentFor is a Deployment with the placeholder where the image goes.
//
// One container, and deliberately so: the placeholder goes to every container of every
// kind, and an init container running the same image as the application inherits
// whatever that image does when given an argument. The pause image sleeps whatever it
// is told, so an init container made of it never completes and the pod is never ready —
// which is what happened the first time this fixture had two.
//
// Whether the substitution reaches init containers is tested where it belongs, in the
// package that does the substituting, against a manifest with several containers and
// no cluster involved.
func deploymentFor(namespace string, image ...string) string {
	// The image is a parameter because a test that deploys two versions needs two
	// manifests: the placeholder only gets substituted by the deployer, and a revert
	// sets the image on the cluster's own copy.
	what := "IMAGE"
	if len(image) > 0 {
		what = image[0]
	}

	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: app
  namespace: ` + namespace + `
  labels:
    app: app
spec:
  replicas: 1
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
          image: ` + what + `
`
}

// nestedContainers reads the containers out of what the cluster answered.
func nestedContainers(live *unstructured.Unstructured) ([]any, bool, error) {
	return unstructured.NestedSlice(live.Object, "spec", "template", "spec", "containers")
}

// stringField reads one field of one container, as text.
func stringField(containers []any, index int, key string) string {
	if index >= len(containers) {
		return ""
	}
	container, ok := containers[index].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := container[key].(string)
	return value
}
