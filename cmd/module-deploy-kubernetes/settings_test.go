package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Reading the rows the core resolved, and what it says when they do not say enough.
//
// A cluster is a row rather than an element of a list, because it is written at a
// level, inherited from the levels above, and switched on or off at whichever of them
// says so. So what these check is that a row arrives as a row, that half-filled rows
// are ignored rather than half-used, and — the part that was not true before this
// became rows — that a row somebody switched off is not a place this module will
// deploy to, whatever the repository asks for.

// rowsOf turns the same JSON a list setting used to arrive in into rows, so a test
// reads the way the thing is written rather than the way it used to be stored.
func rowsOf(t *testing.T, encoded string) []moduleTarget {
	t.Helper()
	var values []map[string]any
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		t.Fatalf("read the rows: %v", err)
	}
	rows := make([]moduleTarget, 0, len(values))
	for _, one := range values {
		rows = append(rows, moduleTarget{Enabled: true, Values: one})
	}
	return rows
}

// clusterList is the shape a row list arrives in from the core.
const clusterList = `[
  {"name": "production-eu", "in_cluster": false, "kubeconfig": "YXBpVmVyc2lvbjogdjEK",
   "context": "prod", "default_namespace": "web"},
  {"name": "staging", "default_namespace": "staging"}
]`

func TestClustersArriveAsAList(t *testing.T) {
	clusters, err := clustersOf(rowsOf(t, clusterList))
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
	clusters, err := clustersOf(rowsOf(t,
		`[{"name": "", "kubeconfig": "eA=="}, {"name": "real", "kubeconfig": "eQ=="}]`))
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
	clusters, err := clustersOf(nil)
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

// A row somebody switched off is not a place to deploy to.
//
// This is the whole point of a row having a switch: a cluster the instance shares with
// a hundred projects must be usable by all of them and by none of them, and the way to
// say "not this one" has to survive a repository asking for it by name. A row that
// stayed available because the file asked for it would make the switch a suggestion.
func TestASwitchedOffRowIsNotAPlaceToDeployTo(t *testing.T) {
	rows := []moduleTarget{
		{Label: "production-eu", Enabled: true, Values: map[string]any{"name": "production-eu"}},
		{Label: "staging", Enabled: false, Values: map[string]any{"name": "staging"}},
	}

	clusters, err := clustersOf(rows)
	if err != nil {
		t.Fatalf("read the clusters: %v", err)
	}
	if len(clusters) != 1 || clusters[0].Name != "production-eu" {
		t.Fatalf("read %+v, want only the row that is switched on", clusters)
	}

	if _, found := find(clusters, "staging"); found {
		t.Error("a switched-off row was found as a cluster")
	}
}

// And the refusal has to say which of the two happened. "There is no such cluster"
// sends somebody to add one that is already there; "it is switched off" sends them to
// the switch, which is the thing that actually needs pressing.
func TestASwitchedOffRowIsDistinguishedFromAMissingOne(t *testing.T) {
	rows := []moduleTarget{
		{Label: "staging", Enabled: false, Values: map[string]any{"name": "staging"}},
	}

	label, isOff := findSwitchedOff(rows, "staging")
	if !isOff {
		t.Fatal("a row that is switched off was not recognised as one")
	}
	if !strings.Contains(errClusterOff(label, "staging").Error(), "switched off") {
		t.Errorf("the refusal does not say what is wrong: %v", errClusterOff(label, "staging"))
	}

	if _, isOff := findSwitchedOff(rows, "nowhere"); isOff {
		t.Error("a cluster nobody has heard of was reported as switched off")
	}
}
