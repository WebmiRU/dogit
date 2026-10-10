package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/deploy"
	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// A pre-step written as a command is a Job, and not a request for nothing.
//
// The form is written down in the pipeline configuration, the core reads it and names it, and
// it arrived here with an empty kind and an empty body — so the cluster was asked for
// `no matches for kind "" in version ""`, which names neither this module nor the file that
// asked. Three deployments on a stand failed that way before anybody read the error as what it
// was.
// recordingHistory remembers whether anything asked it to free a place, and what it
// still holds once they have.
type recordingHistory struct {
	History
	reclaimed int
	held      *deploy.Deployment
}

func (r *recordingHistory) Reclaim(_ context.Context, _, _, _ string) error {
	r.reclaimed++
	return nil
}

func (r *recordingHistory) Current(_ context.Context, _, _, _ string) (*deploy.Deployment, error) {
	return r.held, nil
}

// The place is asked about only after something has been allowed to clear it.
//
// This is the defect in one assertion. A record left by a module that died holds its
// place until something frees it, and the code that knows how is reached only once a
// deployment has been accepted — so the refusal happened first and the freeing never ran.
// A place whose deployment ended hours ago could not be deployed to again, with nothing
// to show for it: a page saying a deployment was under way, and no deployment anywhere.
func TestABusyPlaceIsClearedBeforeItIsAskedAbout(t *testing.T) {
	held := &deploy.Deployment{
		ID: uuid.New(), Project: "test/versions", Cluster: "deploy2", Namespace: "dogit-test",
		Image: "registry.test/versions@sha256:abc", State: deploy.StateRunning,
	}
	history := &recordingHistory{held: held}
	core := &coreClient{history: history}

	if err := core.placeIsFree(context.Background(), held.Project, held.Cluster, held.Namespace); err == nil {
		t.Fatal("a place with a deployment under way was reported free")
	}

	if history.reclaimed == 0 {
		t.Fatal("the place was asked about without anything being allowed to clear it: " +
			"the record that refuses this is the same kind of record the reclaiming removes, " +
			"so a module that died leaves a place locked for ever")
	}
}

// A deployment that really is under way is still refused. Freeing first must not turn
// into freeing everything: two rollouts reaching one workload is the thing this whole
// check exists to prevent.
func TestAPlaceHeldByALiveDeploymentIsStillRefused(t *testing.T) {
	history := &recordingHistory{held: &deploy.Deployment{
		State: deploy.StateRunning, Image: "registry.test/versions@sha256:abc",
	}}
	core := &coreClient{history: history}

	err := core.placeIsFree(context.Background(), "test/versions", "deploy2", "dogit-test")

	var busy deploy.ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("a place with a deployment under way was not refused as busy: %v", err)
	}
}

// A place nothing holds is free, and saying so is not an error.
func TestAFreePlaceIsFree(t *testing.T) {
	core := &coreClient{history: &recordingHistory{}}

	if err := core.placeIsFree(context.Background(), "test/versions", "deploy2", "dogit-test"); err != nil {
		t.Fatalf("an empty place was refused: %v", err)
	}
}

func TestACommandStepBecomesAJob(t *testing.T) {
	got := jobs(k8s.Substitution{}, []jobRequest{{
		Name:    "migrate",
		Image:   "alpine:3.21",
		Command: []string{"sh", "-c", "echo hi"},
	}}, "versions-dev")

	if len(got) != 1 {
		t.Fatalf("got %d jobs, want 1", len(got))
	}
	obj := got[0].Object
	if obj.Kind != "Job" || obj.APIVersion != "batch/v1" {
		t.Errorf("the step is a %s/%s, want batch/v1 Job", obj.APIVersion, obj.Kind)
	}
	if obj.Namespace != "versions-dev" {
		t.Errorf("the job is in %q, want the namespace it was asked for", obj.Namespace)
	}

	body := string(obj.Body)
	for _, want := range []string{
		"name: migrate", "image: alpine:3.21",
		`command: ["sh","-c","echo hi"]`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the job does not say %q:\\n%s", want, body)
		}
	}
}

// A command is data, and a repository is allowed to put anything in a file.
//
// The manifest is assembled by hand, so the one thing that has to be right here is escaping: a
// command with a quote in it must not be able to close the JSON it is written into and add a key
// of its own to the Job.
func TestACommandIsEscapedRatherThanConcatenated(t *testing.T) {
	got := jobs(k8s.Substitution{}, []jobRequest{{
		Name:    "sneaky",
		Image:   "alpine:3.21",
		Command: []string{`sh`, "-c", `echo "} && restartPolicy: Always #`},
	}}, "versions-dev")

	body := string(got[0].Object.Body)
	// The command survives as one JSON string, and the key it tried to break out of is still a
	// key in the argument rather than in the manifest.
	if strings.Count(body, "restartPolicy: Never") != 1 {
		t.Errorf("the command changed the manifest:\\n%s", body)
	}
	if !strings.Contains(body, `\"`) {
		t.Errorf("the quote was not escaped, so the argument ends where it should not:\\n%s", body)
	}
}

// A manifest step is untouched by any of this.
func TestAManifestStepIsStillAManifest(t *testing.T) {
	got := jobs(k8s.Substitution{}, []jobRequest{{
		APIVersion: "batch/v1", Kind: "Job", Name: "from-file",
		Body: "apiVersion: batch/v1\nkind: Job\n",
	}}, "versions-dev")

	if got[0].Object.Kind != "Job" {
		t.Errorf("the step is a %s, want the kind it arrived with", got[0].Object.Kind)
	}
	if strings.Contains(string(got[0].Object.Body), "restartPolicy") {
		t.Errorf("a manifest step was rewritten: %s", got[0].Object.Body)
	}
}
