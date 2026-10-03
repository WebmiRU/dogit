package pipeline

import "testing"

// The file name is the one everybody already knows. Inventing a new one would be a
// novelty nobody asked for.
func TestTheConfigurationIsCalledWhatPeopleExpect(t *testing.T) {
	if ConfigFileName != ".gitlab-ci.yml" {
		t.Errorf("configuration file = %q, want .gitlab-ci.yml", ConfigFileName)
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
  artifacts:
    when: always
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if len(config.Stages) != 2 || config.Stages[0] != "build" {
		t.Errorf("stages = %v", config.Stages)
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
			if got := RunsOn(job, tc.branch, false); got != tc.want {
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
