package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheConfigurationIsCalledWhatItIs(t *testing.T) {
	if ConfigFileName != ".dogit-ci.yml" {
		t.Errorf("configuration file = %q, want .dogit-ci.yml", ConfigFileName)
	}
}

// A file that is not ours is not a pipeline, whatever it is called.
//
// In particular a configuration from another forge is an ordinary file in the
// repository: it is shown in the tree like anything else, and it does nothing.
// Reading it would mean implementing a different format and calling it support for
// this one, and every surprise that follows from that belongs to somebody who was
// told it worked.
func TestAConfigurationFromAnotherForgeIsJustAFile(t *testing.T) {
	dir := t.TempDir()

	for _, name := range []string{ConfigFileName, ".gitlab-ci.yml", "somefile-ci.yml", "ci.yml"} {
		body := "job:\n  script:\n    - true\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	config, err := Load(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, present := config.Jobs["job"]; !present {
		t.Fatalf("the project's own configuration was not read: %+v", config.Jobs)
	}

	// And with only the foreign file there, there is no configuration at all —
	// said plainly rather than half-read.
	only := t.TempDir()
	if err := os.WriteFile(filepath.Join(only, ".gitlab-ci.yml"),
		[]byte("job:\n  script:\n    - true\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(only); err == nil {
		t.Error("a file from another forge was read as a pipeline configuration")
	}
	if Exists(only) {
		t.Error("a project with only a foreign file reports having a configuration")
	}
}

// A configuration is somebody's instructions to a machine, so what is not
// understood has to be visible rather than quietly dropped.
func TestParsingAConfiguration(t *testing.T) {
	config, err := Parse([]byte(`
stages:
  - build
  - test

default:
  image: alpine:3.21

variables:
  GOFLAGS: "-mod=readonly"

lint:
  stage: test
  script:
    - echo linting
    - echo done

build:
  stage: build
  image: golang:1.25
  script:
    - go build ./...
  build:
    tag: test
  artifacts:
    when: always
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(config.Stages) != 2 || config.Stages[0] != "build" || config.Stages[1] != "test" {
		t.Errorf("stages = %v, want the declared order [build test]", config.Stages)
	}
	if len(config.Jobs) != 2 {
		t.Fatalf("%d jobs, want 2: %v", len(config.Jobs), config.Jobs)
	}

	lint := config.Jobs["lint"]
	if lint.Image != "alpine:3.21" {
		t.Errorf("lint runs in %q, want the default image", lint.Image)
	}
	if len(lint.Script) != 2 {
		t.Errorf("lint has %d script lines, want 2", len(lint.Script))
	}

	build := config.Jobs["build"]
	if build.Image != "golang:1.25" {
		t.Errorf("build runs in %q", build.Image)
	}

	// The order jobs were written in is the order a person wrote them, and the
	// order the page shows them in.
	if config.Order[0] != "lint" || config.Order[1] != "build" {
		t.Errorf("job order = %v, want the order they were written in", config.Order)
	}
}


func TestCustomStagesMaySurroundBuildAndDeploy(t *testing.T) {
	config, err := Parse([]byte(`
stages: [prepare, build, verify, deploy, cleanup]
compile:
  stage: build
  script: [true]
  build:
    tag: demo
before:
  stage: prepare
  script: [echo before]
scan:
  stage: verify
  script: [echo scan]
after:
  stage: cleanup
  script: [echo after]
deploy:
  target: test
  module: deploy:kubernetes
  manifests: [k8s/deployment.yaml]
`))
	if err != nil {
		t.Fatalf("parse stages around build/deploy: %v", err)
	}
	want := []string{"prepare", "build", "verify", "deploy", "cleanup"}
	if fmt.Sprint(config.Stages) != fmt.Sprint(want) {
		t.Errorf("stages = %v, want %v", config.Stages, want)
	}
}

func TestDeployCannotPrecedeBuildStage(t *testing.T) {
	_, err := Parse([]byte(`
stages: [prepare, deploy, verify, build, cleanup]
compile:
  stage: build
  script: [true]
  build:
    tag: demo
deploy:
  target: test
  module: deploy:kubernetes
  manifests: [k8s/deployment.yaml]
`))
	if err == nil || !strings.Contains(err.Error(), "deploy cannot appear before stage build") {
		t.Fatalf("error = %v, want build/deploy ordering error", err)
	}
}

func TestDeployCanExistWithoutBuildStage(t *testing.T) {
	config, err := Parse([]byte(`
stages: [prepare, deploy, cleanup]
before:
  stage: prepare
  script: [echo before]
after:
  stage: cleanup
  script: [echo after]
deploy:
  target: test
  module: deploy:kubernetes
  manifests: [k8s/deployment.yaml]
`))
	if err != nil {
		t.Fatalf("parse deploy-only pipeline: %v", err)
	}
	want := []string{"prepare", "deploy", "cleanup"}
	if fmt.Sprint(config.Stages) != fmt.Sprint(want) {
		t.Errorf("stages = %v, want %v", config.Stages, want)
	}
}

func TestStageFirstConfigDefaultsFailureFlagsToTrue(t *testing.T) {
	config, err := Parse([]byte(`
variables:
  GREETING: hello
stages:
  - name: prepare
    commands:
      - shell:
          command: echo "$GREETING"
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
	if err != nil { t.Fatalf("parse stage-first config: %v", err) }
	if len(config.StageSpecs) != 3 || config.StageSpecs[0].Name != "prepare" { t.Fatalf("stages = %+v", config.StageSpecs) }
	if !config.StageSpecs[0].BreaksOnFail() || !config.StageSpecs[0].Commands[0].Shell.ChecksExitStatus() { t.Fatal("failure flags must default to true") }
	if len(config.Deploys) != 1 || config.Deploys[0].Target != "production" { t.Fatalf("deployment config = %+v", config.Deploys) }
}

func TestStageFirstConfigAllowsFailurePoliciesPerStageAndCommand(t *testing.T) {
	config, err := Parse([]byte(`
stages:
  - name: build
    break_on_fail: false
    build: {image: registry.example/app}
  - name: custom
    break_on_fail: false
    commands:
      - shell:
          command: echo first
          check_exit_status: false
      - shell:
          command: echo second
          check_exit_status: true
`))
	if err != nil { t.Fatalf("parse stage-first config: %v", err) }
	if config.StageSpecs[0].BreaksOnFail() { t.Fatal("explicit break_on_fail=false was not preserved") }
	commands := config.StageSpecs[1].Commands
	if commands[0].Shell.ChecksExitStatus() { t.Fatal("explicit check_exit_status=false was not preserved") }
	if !commands[1].Shell.ChecksExitStatus() { t.Fatal("check_exit_status=true was not preserved") }
}

func TestStageFirstConfigRejectsDeployBeforeBuild(t *testing.T) {
	_, err := Parse([]byte(`
stages:
  - name: deploy
    deploy:
      module: deploy:kubernetes
      target: production
      manifests: [k8s/deployment.yaml]
  - name: build
    build: {image: registry.example/app}
`))
	if err == nil || !strings.Contains(err.Error(), "deploy cannot appear before stage build") { t.Fatalf("error = %v", err) }
}

func TestStageFirstConfigRejectsUnknownCommandFields(t *testing.T) {
	_, err := Parse([]byte(`
stages:
  - name: inspect
    commands:
      - shell:
          command: echo ok
          chek_exit_status: false
`))
	if err == nil || !strings.Contains(err.Error(), "chek_exit_status") { t.Fatalf("error = %v", err) }
}

// A job without a stage or an image still has to run somewhere.
func TestAJobWithNothingSaidAboutItStillRuns(t *testing.T) {
	config, err := Parse([]byte("noop:\n  script:\n    - true\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	job := config.Jobs["noop"]
	if job.Stage != "test" {
		t.Errorf("stage = %q, want test", job.Stage)
	}
	if job.Image == "" {
		t.Error("a job with no image would have nothing to run in")
	}
}

// A file that is not a configuration must say so rather than produce an empty
// pipeline that silently does nothing.
func TestAMalformedConfigurationIsReported(t *testing.T) {
	if _, err := Parse([]byte("this is: [not: valid")); err == nil {
		t.Error("a malformed configuration parsed successfully")
	}
}

// The rules are where a pipeline either works or quietly does the wrong thing, so
// each of these is checked against the branch it names.
func TestWhenAJobRuns(t *testing.T) {
	cases := []struct {
		name   string
		job    string
		branch string
		want   bool
	}{
		{"a job with no conditions runs everywhere", "b:\n  script: [true]\n", "main", true},
		{"only on one branch", "b:\n  only:\n    refs: [main]\n", "main", true},
		{"and not on another", "b:\n  only:\n    refs: [main]\n", "feature/x", false},
		{"only on a prefix", "b:\n  only:\n    refs: ['release/*']\n", "release/1.2", true},
		{"and not outside it", "b:\n  only:\n    refs: ['release/*']\n", "main", false},
		{"except removes a branch", "b:\n  except:\n    refs: [main]\n", "main", false},
		{"and leaves the rest", "b:\n  except:\n    refs: [main]\n", "feature/x", true},
		{"a rule on the default branch", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n", "main", true},
		{"and not anywhere else", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n", "dev", false},
		{"a negated rule", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH != \"main\"\n", "dev", true},
		{"which excludes the default", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH != \"main\"\n", "main", false},
		{"an explicit never", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n      when: never\n", "main", false},

		// This is the one that matters. A condition nobody reads must not be treated
		// as satisfied: a deploy job that runs on every push is the failure a
		// pipeline must never have.
		{"a condition this build cannot read does not run", "b:\n  rules:\n    - if: $CI_COMMIT_TAG\n", "main", false},
		{"nor does an unknown expression", "b:\n  rules:\n    - if: $CI_PIPELINE_SOURCE == \"schedule\"\n", "main", false},
		{"and a change rule is not guessed at either", "b:\n  rules:\n    - changes: [src/**]\n", "main", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := Parse([]byte(tc.job))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			job := config.Jobs["b"]
			if got := RunsOn(job, Ref{Name: tc.branch}, false); got != tc.want {
				t.Errorf("runs on %q = %v, want %v", tc.branch, got, tc.want)
			}
		})
	}
}

// A project without a configuration is not broken: most projects have nothing to
// build, and the page says so rather than showing an error.
func TestAMissingConfigurationIsNotAnError(t *testing.T) {
	if Exists(t.TempDir()) {
		t.Error("an empty directory reports a configuration")
	}
}

// A job that produces an image carries that in its configuration, and the core
// stores it without understanding it.
func TestABuildDefinitionSurvivesParsing(t *testing.T) {
	config, err := Parse([]byte(`
image:
  stage: build
  script:
    - make build
  build:
    dockerfile: Dockerfile
    context: .
    tag: ${CI_COMMIT_SHORT_SHA}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	build := config.Jobs["image"].Build
	if build == nil {
		t.Fatal("the build definition was lost")
	}
	if build["dockerfile"] != "Dockerfile" {
		t.Errorf("dockerfile = %v", build["dockerfile"])
	}
	if build["tag"] != "${CI_COMMIT_SHORT_SHA}" {
		t.Errorf("tag = %v", build["tag"])
	}
}

// A release is a tag, and a tag is not a branch.
//
// Each case is a rule and a run, and what is being checked is that a rule written about
// one is never answered by the other. The dangerous direction is a branch rule matching a
// tag, because that is a deployment nobody asked for.
func TestRulesTellTagsFromBranches(t *testing.T) {
	cases := []struct {
		name string
		job  string
		ref  Ref
		want bool
	}{
		{"a tag rule matches its tag", "b:\n  rules:\n    - if: $CI_COMMIT_TAG == \"v*\"\n",
			Ref{Name: "v1.01", IsTag: true}, true},
		{"a tag rule does not match another tag", "b:\n  rules:\n    - if: $CI_COMMIT_TAG == \"v*\"\n",
			Ref{Name: "nightly", IsTag: true}, false},
		{"a tag rule does not match a branch named like a tag", "b:\n  rules:\n    - if: $CI_COMMIT_TAG == \"v*\"\n",
			Ref{Name: "v1.01"}, false},
		{"a branch rule does not match a tag", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n",
			Ref{Name: "v1.01", IsTag: true}, false},
		{"a branch rule matches its branch", "b:\n  rules:\n    - if: $CI_COMMIT_BRANCH == \"main\"\n",
			Ref{Name: "main"}, true},
		{"refs/tags says tags only", "b:\n  rules:\n    - if: $CI_COMMIT_REF_NAME == \"refs/tags/v*\"\n",
			Ref{Name: "v1.01", IsTag: true}, true},
		{"a plain name is not a tag pattern", "b:\n  rules:\n    - if: $CI_COMMIT_REF_NAME == \"v*\"\n",
			Ref{Name: "v1.01", IsTag: true}, false},
		{"an unknown variable matches nothing", "b:\n  rules:\n    - if: $CI_NOW == \"whatever\"\n",
			Ref{Name: "main"}, false},
		{"the short sha is what a label says", "b:\n  rules:\n    - if: $CI_COMMIT_SHORT_SHA == \"abc1234\"\n",
			Ref{Name: "main", SHA: "abc1234def"}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := Parse([]byte(tc.job))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got := RunsOn(config.Jobs["b"], tc.ref, false); got != tc.want {
				t.Errorf("runs on %+v = %v, want %v", tc.ref, got, tc.want)
			}
		})
	}
}
