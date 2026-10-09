package api

import (
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
)

// Which places a run under way has not got to yet.
//
// A page asked about one place can say what that place last did, and that is an older
// commit's work. Beside a run that is plainly still going through its other places, a
// green answer there reads as "finished" — so the run has to say which of its places it
// is still going to reach, and the page has to be able to ask it.

// One run with two deployments in it, both of them waiting, and one build in front.
//
// Built by the same function that turns a repository's file into jobs, rather than
// written out here: a fixture with the specification spelled differently from the one
// that is stored tests a shape nothing else has, and passes while the code under it
// reads a field that was never written.
func dueRunJobs() []store.Job {
	return []store.Job{
		{Name: "image", Stage: "build"},
		deployJob(pipeline.DeploySpec{Name: "dev", Module: "kubernetes", Target: "dev"}),
		deployJob(pipeline.DeploySpec{Name: "stage", Module: "kubernetes", Target: "stage"}),
	}
}

func setupDue(t *testing.T) (*moduleFixture, *models.Project, []store.Job) {
	t.Helper()

	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "dueplaces", nil)

	run, err := f.store.Pipelines().CreatePipeline(t.Context(), project.ID, "v1",
		"0123456789abcdef0123456789abcdef01234567", "push", nil, nil, store.Commit{},
		dueRunJobs())
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}

	jobs, err := f.store.Pipelines().JobsOfPipeline(t.Context(), run.ID)
	if err != nil || len(jobs) != len(dueRunJobs()) {
		t.Fatalf("read the jobs: %v", err)
	}
	return f, project, jobs
}

// A deployment that has not started is a place the run has not reached yet.
func TestAPlaceWaitingItsTurnIsNamed(t *testing.T) {
	f, project, _ := setupDue(t)

	due := f.server.placesDueNow(t.Context(), project)
	if !due["dev"] || !due["stage"] {
		t.Errorf("a run with both deployments waiting said %v, want both of them", due)
	}
}

// One that has started is not waiting any more, whatever the others are doing.
func TestAPlaceThatHasStartedIsNotWaiting(t *testing.T) {
	f, project, jobs := setupDue(t)

	if _, err := f.store.Pipelines().ClaimDeployJob(t.Context(), jobs[1].ID); err != nil {
		t.Fatalf("claim the first deployment: %v", err)
	}

	due := f.server.placesDueNow(t.Context(), project)
	if due["dev"] {
		t.Error("the place being deployed to was said to be waiting its turn")
	}
	if !due["stage"] {
		t.Error("the place after it was not said to be waiting")
	}
}

// A finished run says nothing about anybody's turn: a place it never reached was not
// waiting for anything, and a row that can never stop saying "queued" is a row that has
// stopped saying anything.
func TestAFinishedRunQueuesNobody(t *testing.T) {
	f, project, jobs := setupDue(t)

	for _, job := range jobs {
		if job.Stage != "deploy" {
			continue
		}
		if err := f.store.Pipelines().FinishJob(t.Context(), job.ID, store.JobSuccess,
			time.Second, ""); err != nil {
			t.Fatalf("finish %s: %v", job.Name, err)
		}
	}

	due := f.server.placesDueNow(t.Context(), project)
	if len(due) != 0 {
		t.Errorf("a finished run said %v, want nobody waiting", due)
	}
}
