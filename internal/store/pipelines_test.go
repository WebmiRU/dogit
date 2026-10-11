// The tests live in an external test package so they can use dbtest, which
// imports store itself.
package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// Two runners asking at the same moment must not get the same job.
//
// This is the one place in the system where a race would be visible to a user as
// two machines building the same thing, and it is settled by the database rather
// than by a check-then-write in Go.
func TestAJobIsClaimedByExactlyOneRunner(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	project := dbtest.NewProject(t, st, "claimdemo", nil)
	runners := []uuid.UUID{uuid.New(), uuid.New()}

	_, err := st.Pipelines().CreatePipeline(ctx, project.ID, "main", "abc123", "manual", nil, nil, store.Commit{},
		[]store.Job{{Name: "build", Stage: "build", Image: "alpine", Script: []string{"true"}}})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	type result struct {
		job   *store.Job
		owner uuid.UUID
		err   error
	}

	results := make(chan result, len(runners))
	start := make(chan struct{})

	for _, runner := range runners {
		go func(id uuid.UUID) {
			<-start
			job, err := st.Pipelines().ClaimJob(ctx, id, nil)
			results <- result{job: job, owner: id, err: err}
		}(runner)
	}

	close(start)

	var claimed []result
	for range runners {
		got := <-results
		if got.err != nil && got.err != store.ErrNotFound {
			t.Fatalf("claim: %v", got.err)
		}
		if got.job != nil {
			claimed = append(claimed, got)
		}
	}

	if len(claimed) != 1 {
		t.Fatalf("%d runners were handed the same job, want 1", len(claimed))
	}

	// And the job says who has it, so a runner cannot finish somebody else's.
	job, err := st.Pipelines().JobByID(ctx, claimed[0].job.ID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if job.RunnerID == nil || *job.RunnerID != claimed[0].owner {
		t.Errorf("the job is attributed to %v, want %v", job.RunnerID, claimed[0].owner)
	}
	if job.Status != store.JobRunning {
		t.Errorf("a claimed job is %q, want %q", job.Status, store.JobRunning)
	}
}

// A queue with nothing in it is an answer, not a failure.
func TestClaimingFromAnEmptyQueue(t *testing.T) {
	st := dbtest.Open(t)

	job, err := st.Pipelines().ClaimJob(context.Background(), uuid.New(), nil)
	if err != store.ErrNotFound {
		t.Fatalf("err = %v, want store.ErrNotFound", err)
	}
	if job != nil {
		t.Error("a job came back from an empty queue")
	}
}

// A pipeline's state is what its jobs add up to, and it is worked out on read so
// that a job's state cannot drift away from its parent's.
func TestPipelineStatusFollowsItsJobs(t *testing.T) {
	cases := []struct {
		name   string
		jobs   []store.Job
		stored string
		want   string
	}{
		{"nothing has started", []store.Job{{Status: store.JobPending}}, store.PipelinePending, store.PipelinePending},
		{"one is running", []store.Job{{Status: store.JobRunning}, {Status: store.JobPending}}, store.PipelinePending, store.PipelineRunning},
		{"all done", []store.Job{{Status: store.JobSuccess}, {Status: store.JobSuccess}}, store.PipelinePending, store.PipelineSuccess},
		{"one failed", []store.Job{{Status: store.JobSuccess}, {Status: store.JobFailed}}, store.PipelinePending, store.PipelineFailed},
		// A job that was allowed to fail does not drag the pipeline down — that is
		// the entire point of allowing it.
		{"a failure that was allowed", []store.Job{{Status: store.JobSuccess}, {Status: store.JobFailed, AllowFailure: true}},
			store.PipelinePending, store.PipelineSuccess},
		{"a skipped job is not a failure", []store.Job{{Status: store.JobSuccess}, {Status: store.JobSkipped}},
			store.PipelinePending, store.PipelineSuccess},
		{"a superseded deployment is not a passed pipeline", []store.Job{{Status: store.JobSuccess}, {Status: store.JobSuperseded}},
			store.PipelinePending, store.PipelineSuperseded},
		{"a real failure still wins over a superseded deployment", []store.Job{{Status: store.JobFailed}, {Status: store.JobSuperseded}},
			store.PipelinePending, store.PipelineFailed},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pipeline := &store.Pipeline{Status: tc.stored}
			if got := store.PipelineStatus(pipeline, tc.jobs); got != tc.want {
				t.Errorf("status = %q, want %q", got, tc.want)
			}
		})
	}

	// A pipeline that was cancelled stays cancelled whatever its jobs say.
	cancelled := &store.Pipeline{Status: store.PipelineCanceled}
	if got := store.PipelineStatus(cancelled, []store.Job{{Status: store.JobFailed}}); got != store.PipelineCanceled {
		t.Errorf("a cancelled pipeline is %q", got)
	}
}

// Finishing a job closes the pipeline when nothing of it is left waiting.
func TestFinishingTheLastJobClosesThePipeline(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	project := dbtest.NewProject(t, st, "finishdemo", nil)
	pipeline, err := st.Pipelines().CreatePipeline(ctx, project.ID, "main", "abc", "manual", nil, nil, store.Commit{},
		[]store.Job{{Name: "one"}, {Name: "two"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	first, err := st.Pipelines().ClaimJob(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := st.Pipelines().FinishJob(ctx, first.ID, store.JobSuccess, time.Second, ""); err != nil {
		t.Fatalf("finish: %v", err)
	}

	// One job is still waiting, so the pipeline is not finished.
	afterFirst, err := st.Pipelines().PipelineByIID(ctx, project.ID, pipeline.IID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if afterFirst.FinishedAt != nil {
		t.Error("the pipeline finished while a job was still pending")
	}

	second, err := st.Pipelines().ClaimJob(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatalf("claim second: %v", err)
	}
	if err := st.Pipelines().FinishJob(ctx, second.ID, store.JobFailed, 2*time.Second, "the script said no"); err != nil {
		t.Fatalf("finish second: %v", err)
	}

	afterSecond, err := st.Pipelines().PipelineByIID(ctx, project.ID, pipeline.IID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if afterSecond.Status != store.PipelineFailed {
		t.Errorf("pipeline is %q, want %q", afterSecond.Status, store.PipelineFailed)
	}
	if afterSecond.FinishedAt == nil {
		t.Error("a finished pipeline has no finish time")
	}
}

// The deploy can be overtaken while the build is still running. When that build ends,
// the stored status must retain the deploy outcome rather than replacing it with success.
func TestSupersededDeploymentStatusSurvivesBuildFinishingLater(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()
	project := dbtest.NewProject(t, st, "supersedestatus", nil)
	pipeline, err := st.Pipelines().CreatePipeline(ctx, project.ID, "v1", "abc123", "tag",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "build", Stage: "build", Script: []string{"true"}},
			{Name: "deploy", Stage: "deploy", Deploy: map[string]any{"Target": "prod"}},
		})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	jobs, err := st.Pipelines().JobsOfPipeline(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(jobs))
	}

	if err := st.Pipelines().FinishJob(ctx, jobs[1].ID, store.JobSuperseded, 0, "overtaken by a newer deployment"); err != nil {
		t.Fatalf("supersede deployment: %v", err)
	}
	// The build is still pending, so the run must not be closed yet.
	before, err := st.Pipelines().PipelineByID(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("read unfinished pipeline: %v", err)
	}
	if before.FinishedAt != nil {
		t.Fatal("pipeline finished while its build was still pending")
	}

	if err := st.Pipelines().FinishJob(ctx, jobs[0].ID, store.JobSuccess, time.Second, ""); err != nil {
		t.Fatalf("finish build: %v", err)
	}
	after, err := st.Pipelines().PipelineByID(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("read finished pipeline: %v", err)
	}
	if after.Status != store.PipelineSuperseded {
		t.Errorf("stored pipeline status = %q, want %q", after.Status, store.PipelineSuperseded)
	}
	jobs, err = st.Pipelines().JobsOfPipeline(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("read finished jobs: %v", err)
	}
	if got := store.PipelineStatus(after, jobs); got != store.PipelineSuperseded {
		t.Errorf("computed pipeline status = %q, want %q", got, store.PipelineSuperseded)
	}
}


// A hard failure stops later stages but leaves same-stage siblings runnable.
func TestFailedStageSkipsOnlyLaterStages(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()
	project := dbtest.NewProject(t, st, "stageskip", nil)
	_, err := st.Pipelines().CreatePipeline(ctx, project.ID, "main", "stage-skip", "manual",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "build-fails", Stage: "build", StageOrder: 0, Script: []string{"false"}},
			{Name: "build-sibling", Stage: "build", StageOrder: 0, Script: []string{"true"}},
			{Name: "verify", Stage: "verify", StageOrder: 1, Script: []string{"true"}},
		})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	failed, err := st.Pipelines().ClaimJob(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatalf("claim first build: %v", err)
	}
	if failed.Name != "build-fails" {
		t.Fatalf("claimed %q, want build-fails", failed.Name)
	}
	if err := st.Pipelines().FinishJob(ctx, failed.ID, store.JobFailed, time.Second, "build failed"); err != nil {
		t.Fatalf("finish failed stage job: %v", err)
	}
	jobs, err := st.Pipelines().JobsOfPipeline(ctx, failed.PipelineID)
	if err != nil {
		t.Fatalf("read jobs: %v", err)
	}
	statuses := map[string]string{}
	for _, job := range jobs {
		statuses[job.Name] = job.Status
	}
	if statuses["build-sibling"] != store.JobPending {
		t.Errorf("same-stage sibling status = %q, want pending", statuses["build-sibling"])
	}
	if statuses["verify"] != store.JobSkipped {
		t.Errorf("later-stage status = %q, want skipped", statuses["verify"])
	}
	next, err := st.Pipelines().ClaimJob(ctx, uuid.New(), nil)
	if err != nil {
		t.Fatalf("claim same-stage sibling: %v", err)
	}
	if next.Name != "build-sibling" {
		t.Fatalf("claimed %q, want build-sibling", next.Name)
	}
}

// The artifact catalog includes a pending image build before a runner claims it.
func TestListBuildArtifactsIncludesPendingBuildJobs(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()
	project := dbtest.NewProject(t, st, "artifactcatalog", nil)
	pipeline, err := st.Pipelines().CreatePipeline(ctx, project.ID, "v1", "abc123", "tag",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "image", Stage: "build", StageOrder: 0, Build: map[string]any{
				"image": "registry.example/app", "tag": "v1",
			}},
			{Name: "verify", Stage: "verify", StageOrder: 1, Script: []string{"echo verify"}},
		})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}
	got, total, err := st.Pipelines().ListBuildArtifacts(ctx, project.ID, 1, 30)
	if err != nil {
		t.Fatalf("list artifacts: %v", err)
	}
	if total != 1 || len(got) != 1 {
		t.Fatalf("got %d items (total %d), want one", len(got), total)
	}
	if got[0].PipelineIID != pipeline.IID || got[0].Status != store.JobPending {
		t.Errorf("artifact record = %+v, want pending job in pipeline %d", got[0], pipeline.IID)
	}
	if got[0].QueuePriority != store.QueuePriorityNormal {
		t.Errorf("queue priority = %d, want normal priority %d", got[0].QueuePriority, store.QueuePriorityNormal)
	}
}
// Jobs come back in the order they were declared, and a job carries its build
// definition because that is what a runner needs and only the pipeline has it.
func TestJobsCarryWhatARunnerNeeds(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	project := dbtest.NewProject(t, st, "jobdemo", nil)
	pipeline, err := st.Pipelines().CreatePipeline(ctx, project.ID, "main", "def456", "push", nil, nil, store.Commit{},
		[]store.Job{
			{Name: "lint", Image: "golang:1.25", Script: []string{"go vet ./..."}},
			{Name: "build", Stage: "build", Build: map[string]any{"tag": "dev"}},
		})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	jobs, err := st.Pipelines().JobsOfPipeline(ctx, pipeline.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("%d jobs, want 2", len(jobs))
	}
	if jobs[0].Name != "lint" || jobs[0].IID != 1 {
		t.Errorf("the first job is %s (%d), want lint (1)", jobs[0].Name, jobs[0].IID)
	}

	// The build definition survives the round trip: it is the one part of a job the
	// core stores without understanding.
	if len(jobs[1].Build) == 0 {
		t.Fatalf("the build definition was lost: %+v", jobs[1])
	}
	if jobs[1].Build["tag"] != "dev" {
		t.Errorf("the build definition came back as %+v", jobs[1].Build)
	}
	if jobs[1].ProjectID != project.ID {
		t.Errorf("the job does not know its project: %v", jobs[1].ProjectID)
	}
}

// A push starts a run by nobody's hand, and the same push twice starts it once.
//
// Both halves matter and both were wrong in production before they were written down.
// The author is nil because nobody pressed a button, and an empty identity belongs to
// no user and is refused by the database. The "once" is because the durable event log
// is read from the beginning every time the process starts, so without it every restart
// rebuilt every commit the repository had ever had pushed at it.
func TestARunWithNoAuthorIsKeptOncePerCause(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()
	project := dbtest.NewProject(t, st, "pushdemo", nil)

	const sha = "44af11bc0000000000000000000000000000dead"

	// No author, exactly as a push arrives.
	run, err := st.Pipelines().CreatePipeline(ctx, project.ID, "main", sha, "push", nil,
		nil, store.Commit{},
		[]store.Job{{Name: "build", Stage: "build", Image: "alpine", Script: []string{"true"}}})
	if err != nil {
		t.Fatalf("a run nobody started should still be filed: %v", err)
	}
	if run.CreatedBy != nil {
		t.Fatalf("the run claims an author: %v", *run.CreatedBy)
	}

	seen, err := st.Pipelines().AutomaticRunExists(ctx, project.ID, sha, "push")
	if err != nil {
		t.Fatalf("ask whether it ran: %v", err)
	}
	if !seen {
		t.Fatal("the run that just happened was not seen")
	}

	// The same commit for a different reason is a different thing: a person pressing
	// the button again means it, and the answer must not swallow that.
	seen, err = st.Pipelines().AutomaticRunExists(ctx, project.ID, sha, "web")
	if err != nil {
		t.Fatalf("ask about another cause: %v", err)
	}
	if seen {
		t.Fatal("a run started by hand was refused because a push had already run it")
	}
}
