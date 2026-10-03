package pipeline

import (
	"strings"
	"testing"
)

// What a pipeline says about deploying.
//
// The dangerous failure in this block is silence: a misspelt field that is dropped, a
// manifest path that reaches outside the repository, an empty step that looks like it
// did something. Each of those deploys something, or nothing, and both are worse than
// a refusal at the moment the file is read.

const deployable = "image:\n  stage: build\n  script: [true]\n"

// A file that says nothing about deploying is a file that does not deploy.
func TestAFileThatDoesNotMentionDeployingDoesNotDeploy(t *testing.T) {
	config, err := Parse([]byte(deployable))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if config.Deploy.Present {
		t.Error("a file that never mentioned deploying claims to")
	}
	if !config.Deploy.Empty() {
		t.Error("Empty() does not agree with Present")
	}
}

// The whole block, read as written.
func TestTheBlockIsRead(t *testing.T) {
	config, err := Parse([]byte(`
deploy:
  target: kubernetes
  cluster: production-eu
  namespace: web
  manifests:
    - k8s/deployment.yaml
    - k8s/service.yaml
  expect:
    secrets: [db-credentials, tls-cert]
    config_maps: [app-config]
  pre:
    - k8s/migrate-job.yaml
  post:
    - image: IMAGE
      command: ["/app", "smoke"]
  rollout: true
  timeout: 10m
` + deployable))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	d := config.Deploy
	if d.Target != "kubernetes" || d.Cluster != "production-eu" || d.Namespace != "web" {
		t.Errorf("the destination was read as %+v", d)
	}
	if len(d.Manifests) != 2 || d.Manifests[0] != "k8s/deployment.yaml" {
		t.Errorf("the manifests are %v", d.Manifests)
	}
	if len(d.Expect.Secrets) != 2 || len(d.Expect.ConfigMaps) != 1 {
		t.Errorf("what it expects to exist is %+v", d.Expect)
	}
	if len(d.Pre) != 1 || d.Pre[0].Manifest != "k8s/migrate-job.yaml" {
		t.Errorf("the step before the rollout is %+v", d.Pre)
	}
	if len(d.Post) != 1 || len(d.Post[0].Command) != 2 {
		t.Errorf("the step after the rollout is %+v", d.Post)
	}
	if !d.Rollout || d.Timeout != "10m" {
		t.Errorf("rollout/timeout were read as %v/%q", d.Rollout, d.Timeout)
	}
}

// `deploy` is not a job, however much it looks like one.
func TestDeployIsNotAJob(t *testing.T) {
	config, err := Parse([]byte("deploy:\n  target: kubernetes\n  manifests: [a.yaml]\n" + deployable))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, isJob := config.Jobs["deploy"]; isJob {
		t.Error("the deploy block was parsed as a job to run")
	}
	if len(config.Order) != 1 || config.Order[0] != "image" {
		t.Errorf("the jobs are %v, want only the one that was written", config.Order)
	}
}

// A misspelt field is refused. Silently dropping `namespace` deploys somewhere else,
// and somewhere else is not something anybody can undo by looking at the result.
func TestAMisspeltFieldIsRefused(t *testing.T) {
	_, err := Parse([]byte("deploy:\n  target: kubernetes\n  namespacee: web\n  manifests: [a.yaml]\n" + deployable))
	if err == nil {
		t.Fatal("a misspelt field was accepted")
	}
	if !strings.Contains(err.Error(), "namespacee") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// A file cannot choose the image. It deploys what the run built, and a file that
// believes otherwise would deploy the wrong thing on a pipeline that builds nothing.
func TestAFileCannotChooseTheImage(t *testing.T) {
	_, err := Parse([]byte(
		"deploy:\n  target: kubernetes\n  manifests: [a.yaml]\n  image: something-else\n" + deployable))
	if err == nil {
		t.Fatal("a file was allowed to name the image to deploy")
	}
	if !strings.Contains(err.Error(), "deploy.image") {
		t.Errorf("the refusal does not say which field: %v", err)
	}
}

// A deploy with nowhere to go, or nothing to apply, is refused rather than recorded as
// a deploy that will do nothing at some later point.
func TestADeployWithNothingToDoIsRefused(t *testing.T) {
	for name, file := range map[string]string{
		"no target":     "deploy:\n  manifests: [a.yaml]\n",
		"no manifests":  "deploy:\n  target: kubernetes\n",
		"a bare true":   "deploy: true\n",
		"an empty step": "deploy:\n  target: kubernetes\n  manifests: [a.yaml]\n  pre:\n    - name: migrate\n",
	} {
		if _, err := Parse([]byte(file + deployable)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A manifest path reaches out of the repository: it is read from the commit, and the
// commit is not the place to read anything else from.
func TestAManifestMayOnlyLiveInsideTheRepository(t *testing.T) {
	for _, path := range []string{"/etc/passwd", "../../../etc/shadow", "k8s/../../secrets"} {
		file := "deploy:\n  target: kubernetes\n  manifests: [" + path + "]\n" + deployable
		_, err := Parse([]byte(file))
		if err == nil {
			t.Errorf("the path %q was accepted", path)
		}
	}
}

// An unknown key is refused here, where it is not for a job's.
//
// A job's unknown keys are kept and shown, because a job that nobody reads a key in
// still runs and shows that it ran. A deploy that nobody reads a key in either applies
// something, or applies nothing, and both are found out in production — which is why
// this block says no to keys it does not know instead of quietly skipping them.
func TestAnUnknownKeyIsRefused(t *testing.T) {
	_, err := Parse([]byte(
		"deploy:\n  target: kubernetes\n  manifests: [a.yaml]\n  our_own_note: migrate before Friday\n" + deployable))
	if err == nil {
		t.Fatal("an unknown key was accepted")
	}
	if !strings.Contains(err.Error(), "our_own_note") {
		t.Errorf("the refusal does not name the key: %v", err)
	}
}

// A step written as a bare path is the same as one written out, because both are
// natural to write.
func TestABareStepIsAPathToAManifest(t *testing.T) {
	config, err := Parse([]byte(
		"deploy:\n  target: kubernetes\n  manifests: [a.yaml]\n  pre:\n    - k8s/migrate-job.yaml\n" + deployable))
	if err != nil {
		t.Fatalf("a bare path was refused: %v", err)
	}
	if len(config.Deploy.Pre) != 1 || config.Deploy.Pre[0].Manifest != "k8s/migrate-job.yaml" {
		t.Errorf("the step was read as %+v", config.Deploy.Pre)
	}
}
