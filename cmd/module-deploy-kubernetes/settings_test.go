package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Reading this module's own settings, and what it says when they do not say enough.
//
// The clusters are a list because a cluster is a set of values that belong together,
// and one reached through the pod's own service account cannot be two values of one
// key with one reached through a kubeconfig somebody copied. What the tests check is
// that a list arrives as a list, that half-filled rows are ignored rather than
// half-used, and that the module refuses where guessing would be worse.

// clusterList is the shape a list setting arrives in from the core.
const clusterList = `[
  {"name": "production-eu", "in_cluster": false, "kubeconfig": "YXBpVmVyc2lvbjogdjEK",
   "context": "prod", "default_namespace": "web"},
  {"name": "staging", "default_namespace": "staging"}
]`

func TestClustersArriveAsAList(t *testing.T) {
	clusters, err := clustersOf(map[string]any{"clusters": anyValue(clusterList)})
	if err != nil {
		t.Fatalf("read the clusters: %v", err)
	}
	if len(clusters) != 2 {
		t.Fatalf("read %d clusters, want 2: %+v", len(clusters), clusters)
	}

	first := clusters[0]
	if first.Name != "production-eu" || len(first.Kubeconfig) == 0 || first.Context != "prod" {
		t.Errorf("the first cluster was read as %+v", first)
	}
	if first.DefaultNamespace != "web" {
		t.Errorf("the default namespace was read as %q", first.DefaultNamespace)
	}
	if clusters[1].DefaultNamespace != "staging" {
		t.Errorf("the second cluster was read as %+v", clusters[1])
	}
}

// A row somebody started filling in is not a cluster. Using it would mean deploying to
// a place with no address, and a list that has one is a list somebody is halfway
// through writing.
func TestARowWithoutANameIsNotACluster(t *testing.T) {
	clusters, err := clustersOf(map[string]any{"clusters": anyValue(
		`[{"name": "", "kubeconfig": "eA=="}, {"name": "real", "kubeconfig": "eQ=="}]`)})
	if err != nil {
		t.Fatalf("read the clusters: %v", err)
	}
	if len(clusters) != 1 || clusters[0].Name != "real" {
		t.Errorf("read %+v, want only the row with a name", clusters)
	}
}

// Nothing configured is nothing, not an error: an installation with no clusters yet is
// a normal state, and the page says so.
func TestNoClustersIsNotAFailure(t *testing.T) {
	clusters, err := clustersOf(map[string]any{})
	if err != nil || len(clusters) != 0 {
		t.Errorf("read %+v (%v) from settings that have no clusters", clusters, err)
	}
}

// A cluster with no credential is a problem the operator has to fix, and the message
// has to say which cluster is unusable.
func TestAClusterWithNoWayInSaysWhatIsMissing(t *testing.T) {
	_, err := Cluster{Name: "staging"}.Connect(t.Context())
	if err == nil {
		t.Fatal("a cluster with neither a kubeconfig nor an in-cluster setting was accepted")
	}
	if !strings.Contains(err.Error(), "staging") {
		t.Errorf("the refusal does not name the cluster: %v", err)
	}
}

// A timeout that is not a number falls back rather than refusing: the module said what
// it does when nobody says otherwise, and a typo in a settings page should not stop
// deployments from being configured.
func TestAnUnreadableTimeoutFallsBack(t *testing.T) {
	fallback := 10 * time.Minute

	if got := timeoutFrom(map[string]any{}, "default_rollout_timeout", fallback); got != fallback {
		t.Errorf("an absent timeout became %s", got)
	}
	if got := timeoutFrom(map[string]any{"default_rollout_timeout": "soon"}, "default_rollout_timeout", fallback); got != fallback {
		t.Errorf("a timeout that is not a number became %s", got)
	}
	if got := timeoutFrom(map[string]any{"default_rollout_timeout": float64(60)}, "default_rollout_timeout", fallback); got != time.Minute {
		t.Errorf("a timeout of sixty seconds became %s", got)
	}
	if got := timeoutFrom(map[string]any{"default_rollout_timeout": float64(-5)}, "default_rollout_timeout", fallback); got != fallback {
		t.Errorf("a negative timeout became %s", got)
	}
}

// A namespace is somebody's decision. The module uses the cluster's default, or the
// one the deployment named, and says so rather than picking one.
func TestAMissingNamespaceIsSaidRatherThanGuessed(t *testing.T) {
	err := errNoNamespace("production-eu")

	for _, want := range []string{"production-eu", "does not create"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}
}

// anyValue is a decoded JSON value, which is how a list setting arrives.
func anyValue(encoded string) any {
	var value any
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		panic("a fixture that is not valid JSON: " + encoded)
	}
	return value
}
