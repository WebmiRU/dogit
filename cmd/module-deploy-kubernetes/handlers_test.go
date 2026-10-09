package main

import (
	"strings"
	"testing"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// A pre-step written as a command is a Job, and not a request for nothing.
//
// The form is written down in the pipeline configuration, the core reads it and names it, and
// it arrived here with an empty kind and an empty body — so the cluster was asked for
// `no matches for kind "" in version ""`, which names neither this module nor the file that
// asked. Three deployments on a stand failed that way before anybody read the error as what it
// was.
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
