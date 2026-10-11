package api

import (
	"testing"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/pipeline"
)


// A run started by a push has nobody behind it, and saying so must not read a field out
// of nothing.
//
// This took the process down on the first real push: the author was correctly made nil
// because no person started it, and a line further down still read a name off it.
func TestStageFirstJobsCarryOrderAndErrorPolicies(t *testing.T) {
	config, err := pipeline.Parse([]byte(`
stages:
  - name: prepare
    break_on_error: false
    commands:
      - shell:
          command: echo prepare
          check_exit_status: false
  - name: build
    build:
      image: registry.example/app
      tag: "$CI_COMMIT_SHORT_SHA"
  - name: deploy
    deploy:
      module: deploy:kubernetes
      target: production
      manifests: [k8s/deployment.yaml]
`))
	if err != nil {
		t.Fatalf("parse stage-first config: %v", err)
	}
	jobs := jobsFrom(config, pipeline.Ref{Name: "main", SHA: "0123456789abcdef"}, "")
	if len(jobs) != 3 {
		t.Fatalf("jobs = %+v, want prepare, build and deploy", jobs)
	}
	if jobs[0].Name != "prepare" || jobs[0].StageOrder != 0 || jobs[0].BreakOnError || !jobs[0].BreakOnErrorSet {
		t.Fatalf("prepare job did not preserve its error policy: %+v", jobs[0])
	}
	if len(jobs[0].CheckExitStatus) != 1 || jobs[0].CheckExitStatus[0] {
		t.Fatalf("prepare command policy = %v, want [false]", jobs[0].CheckExitStatus)
	}
	if jobs[1].Name != "build" || jobs[1].StageOrder != 1 || len(jobs[1].Build) == 0 {
		t.Fatalf("build job = %+v", jobs[1])
	}
	if jobs[2].Stage != "deploy" || jobs[2].StageOrder != 2 || jobs[2].Deploy == nil {
		t.Fatalf("deploy job = %+v", jobs[2])
	}
}
func TestARunNobodyStartedStillHasSomebodyToBlame(t *testing.T) {
	if got := startedBy(nil); got == "" {
		t.Fatal("a run with no author says nothing about who started it")
	}

	user := &models.User{Username: "alice"}
	if got := startedBy(user); got != "alice" {
		t.Fatalf("startedBy = %q, want alice", got)
	}
}
