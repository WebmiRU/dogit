package api

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/objects"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// What the queue decides is which of several deployments into one place reaches it, and
// the pieces around that decision are where it goes wrong quietly. These are the two: the
// key it decides on, and what it says about the ones that did not go.

func deployQueueServer(t *testing.T, st *store.Store, dir string) *Server {
	t.Helper()
	kept, err := objects.NewLocal(dir, "")
	if err != nil {
		t.Fatalf("make somewhere to keep job logs: %v", err)
	}
	return &Server{
		store: st,
		cfg:   &config.Config{},
		log:   logger.Discard(),
		git:   gitx.New(gitx.Options{}),
		// The repositories service is needed because finishing a deployment announces it,
		// and announcing one reads the project's path out of where its repository lives.
		// A server without it panics the first time a deployment ends, which is a
		// startling way to learn that.
		repos:   repos.New(st, gitx.New(gitx.Options{}), t.TempDir()),
		objects: kept,
	}
}

// jobLogText is everything written to a job's log, which is where a reader is told what
// happened to a deployment that did not.
func jobLogText(t *testing.T, dir string, logKey string) string {
	t.Helper()
	kept, err := objects.NewLocal(dir, "")
	if err != nil {
		t.Fatalf("open the job logs: %v", err)
	}
	raw, _, err := kept.Get(context.Background(), logKey)
	if err != nil {
		return ""
	}
	defer raw.Close()
	body, err := io.ReadAll(raw)
	if err != nil {
		return ""
	}
	return string(body)
}

// supersededFixture is a project, a run, and a deployment waiting its turn.
func supersededFixture(t *testing.T, s *Server) (*store.Job, queuedDeploy) {
	t.Helper()
	ctx := context.Background()
	st := s.store

	project := dbtest.NewProject(t, st, "queue", nil)
	run, err := st.Pipelines().CreatePipeline(ctx, project.ID, "v1", "abc123", "tag",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "deploy", Deploy: map[string]any{"Target": "prod", "Module": "kubernetes"}},
		})
	if err != nil {
		t.Fatalf("create the run: %v", err)
	}
	jobs, err := st.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("the run has %d jobs, want 1: %v", len(jobs), err)
	}
	return &jobs[0], queuedDeploy{jobID: jobs[0].ID, pipelineID: run.ID}
}

// A deployment that was overtaken is recorded, not forgotten.
//
// A deployment that did not happen is a fact somebody will want to read a week later —
// which commit went out, which one did not, and why — and a job left sitting in pending is
// none of those things. It says "still going", for ever, about work that was never going
// to be done.
func TestADeploymentOvertakenIsRecordedRatherThanLeftWaiting(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	job, waiting := supersededFixture(t, s)
	ctx := context.Background()

	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}
	s.supersedeDeploy(ctx, waiting, place)

	got, err := st.Pipelines().JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("read the job back: %v", err)
	}
	if got.Status != store.JobSuperseded {
		t.Errorf("status: got %q, want %q. A job left pending says it is still going, "+
			"about work that was never going to be done.", got.Status, store.JobSuperseded)
	}
	if got.Error == "" {
		t.Error("nothing says why the deployment was not made, so the record answers only " +
			"that it was not")
	}
}

// Superseded is grey, not red. A failure is a notification to somebody whose afternoon has
// just been interrupted, and nothing here broke: the cluster is carrying the newest
// deployment, which is what everybody was waiting for.
func TestBeingSupersededDoesNotFailTheRun(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	job, waiting := supersededFixture(t, s)
	ctx := context.Background()

	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}
	s.supersedeDeploy(ctx, waiting, place)

	got, err := st.Pipelines().JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("read the job back: %v", err)
	}
	if got.Status == store.JobFailed {
		t.Fatal("being overtaken is being failed, which is a different thing entirely")
	}

	run, err := st.Pipelines().PipelineByID(ctx, waiting.pipelineID)
	if err != nil {
		t.Fatalf("read the run back: %v", err)
	}
	jobs, err := st.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil {
		t.Fatalf("read the jobs back: %v", err)
	}
	if status := store.PipelineStatus(run, jobs); status == store.PipelineFailed {
		t.Errorf("the run is failed over a deployment that was never started, while the " +
			"newest one — the one everybody was waiting for — reached the cluster")
	}
}

// The log is the only place the reader is told what happened, so a superseded deployment
// that says nothing is a page with a grey card and no sentence behind it.
func TestADeploymentOvertakenSaysSoInItsLog(t *testing.T) {
	st := dbtest.Open(t)
	dir := t.TempDir()
	s := deployQueueServer(t, st, dir)
	job, waiting := supersededFixture(t, s)
	ctx := context.Background()

	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}
	s.supersedeDeploy(ctx, waiting, place)

	body := jobLogText(t, dir, s.jobLogKey(job))
	if !contains(body, "overtaken") && !contains(body, "newer deployment") {
		t.Errorf("the log does not say what overtook it: %q", body)
	}
	if !contains(body, "Nothing was deployed") {
		t.Errorf("the log does not say that nothing was deployed, which is the fact a "+
			"reader of a grey card most needs: %q", body)
	}
}

// A run that was cancelled while waiting has left the queue, and reaching it here means it
// did not. Marking it superseded would say it lost a race it was never in.
func TestARunCancelledWhileWaitingIsNotRecordedAsOvertaken(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	job, waiting := supersededFixture(t, s)
	ctx := context.Background()

	if err := st.Pipelines().FinishJob(ctx, job.ID, store.JobCanceled, 0, "cancelled by hand"); err != nil {
		t.Fatalf("cancel the job: %v", err)
	}

	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}
	s.supersedeDeploy(ctx, waiting, place)

	got, err := st.Pipelines().JobByID(ctx, job.ID)
	if err != nil {
		t.Fatalf("read the job back: %v", err)
	}
	if got.Status != store.JobCanceled {
		t.Errorf("status: got %q, want it left as cancelled. A run somebody stopped by hand "+
			"was not overtaken by anything.", got.Status)
	}
}

// The key is what makes two deployments "the same place", so a key that conflated the
// project, the place and the namespace would serialise work that never conflicts — or,
// worse, let two rollouts into one namespace overlap.
func TestThePlaceAJobIsGoingToIsItsProjectItsPlaceAndItsNamespace(t *testing.T) {
	st := dbtest.Open(t)
	kind := dbtest.Unique("deploy")
	module, err := st.Integrations().Register(context.Background(), "deploy:kubernetes", kind,
		"http://module-deploy:8090",
		models.Manifest{Settings: []models.SettingSpec{{
			Key: "clusters", Type: "list",
			Items: &models.SettingItems{
				Identify: []string{"name"},
				Fields: []models.SettingSpec{
					{Key: "name", Type: "string"},
					{Key: "default_namespace", Type: "string"},
				},
			},
		}}})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	places, err := json.Marshal([]map[string]any{
		{"name": "prod", "default_namespace": "versions"},
	})
	if err != nil {
		t.Fatalf("describe the places: %v", err)
	}
	if err := st.Integrations().SetSetting(context.Background(), module.ID,
		store.ScopeInstance, nil, "clusters", places); err != nil {
		t.Fatalf("write the places down: %v", err)
	}

	s := deployQueueServer(t, st, t.TempDir())
	project := dbtest.NewProject(t, st, "queuekey", nil)
	// The module is named by its target — "kubernetes" — and not by its kind: the core
	// builds "deploy:" + this, and a name already carrying the prefix comes back as
	// "deploy:deploy:kubernetes", which is not installed anywhere.
	job := &store.Job{Deploy: map[string]any{
		"Target": "prod", "Module": "kubernetes",
	}}

	place, ok := s.deployPlaceOf(context.Background(), project, job)
	if !ok {
		t.Fatal("no place was found for a job that names one, so deployments would not queue")
	}
	if place.project != project.Path {
		t.Errorf("project: got %q, want %q", place.project, project.Path)
	}
	if place.cluster != "prod" {
		t.Errorf("cluster: got %q, want %q", place.cluster, "prod")
	}
	if place.namespace != "versions" {
		t.Errorf("namespace: got %q, want %q — the namespace is written on the module's row "+
			"of places, not on the job, and a queue keyed on anything else is keyed on nothing",
			place.namespace, "versions")
	}

	// And the two jobs that are not deployments at all get no place, rather than an empty
	// one that every such job would share.
	if _, ok := s.deployPlaceOf(context.Background(), project, &store.Job{}); ok {
		t.Error("a job that is not a deployment was given a place to queue behind")
	}
	if _, ok := s.deployPlaceOf(context.Background(), project,
		&store.Job{Deploy: map[string]any{}}); ok {
		t.Error("a deployment naming no place was given one")
	}
}

// Two jobs of two different runs going to one place are one queue, and the second waits —
// which is the difference from the refusal this replaces.
func TestTheSecondDeploymentToAPlaceWaitsAndTheFirstIsNotDisturbed(t *testing.T) {
	q := newDeployQueue()
	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}

	mayStart, holder := q.take(place, 1, 100)
	if !mayStart || !holder {
		t.Fatal("the first deployment to a free place did not get it")
	}

	mayStart, holder = q.take(place, 2, 200)
	if mayStart {
		t.Error("a second deployment into one place was allowed to start, which is what the " +
			"module used to refuse and what this is meant to stop")
	}
	if holder {
		t.Error("a deployment that is waiting was told it holds the place, so nothing would " +
			"ever release it")
	}
}

var _ = uuid.Nil

// A deployment that stopped waiting while the place was held must not keep it.
//
// The place is handed to the promoted job rather than asked for again, so a job that never
// starts and never finishes would hold that place for the life of the process — and
// everything pushed after it would sit in a queue that is never released. It was handed
// over by a deployment that has ended, so it is handed on to whoever is next, and this job
// is skipped without a word on its record: it was never in the race.
func TestADeploymentThatStoppedWaitingHandsThePlaceOn(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	ctx := context.Background()
	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}

	first, _ := supersededFixture(t, s)

	// A second deployment, of another run, waiting behind the same place.
	second := dbtest.NewProject(t, st, "queue2", nil)
	otherRun, err := st.Pipelines().CreatePipeline(ctx, second.ID, "v1", "def456", "tag",
		nil, nil, store.Commit{}, []store.Job{
			{Name: "deploy", Deploy: map[string]any{"Target": "prod", "Module": "kubernetes"}},
		})
	if err != nil {
		t.Fatalf("create the second run: %v", err)
	}
	otherJobs, err := st.Pipelines().JobsOfPipeline(ctx, otherRun.ID)
	if err != nil || len(otherJobs) != 1 {
		t.Fatalf("the second run has %d jobs, want 1: %v", len(otherJobs), err)
	}
	later := queuedDeploy{jobID: otherJobs[0].ID, pipelineID: otherRun.ID}

	s.deploys.take(place, first.ID, first.PipelineID)
	s.deploys.take(place, later.jobID, later.pipelineID)

	// The one that would be promoted stops waiting — a run cancelled by hand.
	if err := st.Pipelines().FinishJob(ctx, later.jobID, store.JobCanceled, 0, "cancelled"); err != nil {
		t.Fatalf("cancel the waiting run: %v", err)
	}

	promoted, superseded := s.deploys.free(place, first.ID)
	if promoted == nil || promoted.jobID != later.jobID {
		t.Fatalf("promoted %v, want the newest of those waiting", promoted)
	}
	if len(superseded) != 0 {
		t.Errorf("superseded %v, want nothing", superseded)
	}

	// And the place goes on to whoever is next rather than being kept.
	_, _ = s.deploys.free(place, promoted.jobID)
	if mayStart, _ := s.deploys.take(place, 999, 999); !mayStart {
		t.Error("the place is still held by a deployment that never started and so will " +
			"never release it")
	}
}


// A concurrent caller can win the durable claim after the first caller took the place.
// The loser must not free the slot underneath the deployment that is now running.
func TestLosingAConcurrentClaimDoesNotReleaseTheRunningDeployPlace(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	ctx := context.Background()
	job, waiting := supersededFixture(t, s)
	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}

	mayStart, holder := s.deploys.take(place, job.ID, waiting.pipelineID)
	if !mayStart || !holder {
		t.Fatal("the first caller did not take the free place")
	}

	// Another caller won ClaimDeployJob for the same row. The first caller now sees a
	// failed claim, but the job is running and must keep the place until it finishes.
	claimed, err := st.Pipelines().ClaimDeployJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("claim the deployment from the concurrent caller: %v", err)
	}
	if !claimed {
		t.Fatal("the competing caller did not claim the pending deployment")
	}

	s.releasePlaceAfterLostClaim(ctx, place, job.ID)

	mayStart, _ = s.deploys.take(place, 999, 999)
	if mayStart {
		t.Error("a second deployment entered the place after the original holder lost its claim, "+
			"even though the winning caller had already marked the deployment running")
	}
}

// A deployment that has not reached the queue is not waiting its turn.
//
// A deploy job sits pending from the moment it is created until it claims a place, and for
// most of that time it is building its image and pushing it to the registry. From the row it
// looks exactly like one standing in a queue, because both have not started, and the page
// said the second about the first: the header read "a deployment is waiting its turn" while
// the only deployment on the place was busy pushing an image, on every deploy, for as long as
// the build took.
//
// Only the queue knows which of the two this is, so the answer comes from there.
func TestADeploymentBeingBuiltIsNotSaidToBeWaitingItsTurn(t *testing.T) {
	st := dbtest.Open(t)
	s := deployQueueServer(t, st, t.TempDir())
	job, waiting := supersededFixture(t, s)
	place := deployPlace{project: "test/versions", cluster: "prod", namespace: "versions"}

	// The row says pending, which is all a built-but-not-started deployment has to say.
	operation := store.DeployOperation{JobID: job.ID, Status: store.JobPending}
	if s.waitingItsTurn(operation) {
		t.Fatal("a deployment that has not reached the queue was said to be waiting its turn")
	}

	if mayStart, holder, _ := s.deploys.takeWithStatus(place, 999, 999); !mayStart || !holder {
		t.Fatalf("the place was not taken: mayStart=%v holder=%v", mayStart, holder)
	}
	if mayStart, _, enqueued := s.deploys.takeWithStatus(place, waiting.jobID, waiting.pipelineID); mayStart || !enqueued {
		t.Fatalf("the second deployment did not join the queue: mayStart=%v enqueued=%v", mayStart, enqueued)
	}

	if !s.waitingItsTurn(operation) {
		t.Error("a deployment standing in the queue was not said to be waiting its turn")
	}
}
