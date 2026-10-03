package api

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// A notification has to say what happened in a way every channel can use.
//
// The core writing the sentence is what made one Telegram-shaped message the
// property of everybody's notifications. These check that the facts are there and
// complete, because a module that has to guess at a key is a module that will
// show somebody a blank field.

// notifyFixture is a project, a run and a job on that run.
type notifyFixture struct {
	server   *Server
	store    *store.Store
	project  *models.Project
	pipeline *store.Pipeline
	job      *store.Job
}

func setupNotify(t *testing.T) *notifyFixture {
	t.Helper()

	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "notifydemo", nil)

	pipeline, err := f.store.Pipelines().CreatePipeline(t.Context(), project.ID, "main",
		"0123456789abcdef0123456789abcdef01234567", "manual", nil, nil,
		store.Commit{Title: "A change worth telling about", AuthorName: "Evgeniy"},
		[]store.Job{{
			Name: "build", Stage: "build", Image: "alpine",
			Script: []string{"true"},
			Build:  map[string]any{"tag": "dev", "image": "registry.example/home-store/www"},
		}},
	)
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	jobs, err := f.store.Pipelines().JobsOfPipeline(t.Context(), pipeline.ID)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("read jobs: %v", err)
	}

	return &notifyFixture{server: f.server, store: f.store, project: project,
		pipeline: pipeline, job: &jobs[0]}
}

// contextFor is the notification context for a finished job, as the core builds it.
func (f *notifyFixture) contextFor(t *testing.T, status string) notifyContext {
	t.Helper()

	f.job.Status = status
	f.job.DurationMS = 1234

	return notifyContext{
		Event:    "job.finished",
		Project:  projectContext(f.project),
		Pipeline: pipelineContext(f.pipeline, f.project, store.PipelineRunning),
		Job:      jobContext(f.job),
		Images:   imageContext(f.job),
	}
}

// The link in a notification has to be the run's number, not its row id.
func TestNotificationLinksToTheRunPeopleCanSee(t *testing.T) {
	f := setupNotify(t)

	want := "/p/" + f.project.Path + "/-/pipelines/" + strconv.Itoa(f.pipeline.IID)
	got, _ := pipelineContext(f.pipeline, f.project, store.PipelineSuccess)["url"].(string)

	if got != want {
		t.Fatalf("link is %q, want %q", got, want)
	}
}

// Everything a module would put in a message has to be present, or the module
// shows a blank where a commit's title should be.
func TestNotificationCarriesTheFactsAModuleNeeds(t *testing.T) {
	f := setupNotify(t)
	context := f.contextFor(t, store.JobSuccess)

	for _, key := range []string{"path", "name"} {
		if _, ok := context.Project[key]; !ok {
			t.Errorf("the project context has no %q", key)
		}
	}
	for _, key := range []string{"iid", "ref", "sha", "sha_short", "url",
		"commit_title", "commit_author", "status"} {
		if _, ok := context.Pipeline[key]; !ok {
			t.Errorf("the pipeline context has no %q", key)
		}
	}
	for _, key := range []string{"iid", "name", "stage", "status", "duration_ms"} {
		if _, ok := context.Job[key]; !ok {
			t.Errorf("the job context has no %q", key)
		}
	}
	if len(context.Images) != 1 {
		t.Fatalf("the job produced %d images, want 1", len(context.Images))
	}
	if context.Images[0]["name"] != "registry.example/home-store/www" {
		t.Errorf("image name is %v", context.Images[0]["name"])
	}
	if context.Images[0]["tag"] != "dev" {
		t.Errorf("image tag is %v", context.Images[0]["tag"])
	}
}

// The level is stored rather than left for a module to read out of the text.
func TestNotificationLevelSaysHowSeriousItIs(t *testing.T) {
	f := setupNotify(t)

	for status, want := range map[string]string{
		store.JobSuccess:  "success",
		store.JobFailed:   "failure",
		store.JobCanceled: "canceled",
	} {
		if got := notificationLevel(f.contextFor(t, status)); got != want {
			t.Errorf("a job that %s has level %q, want %q", status, got, want)
		}
	}
}

// A run is finished when nothing of it is left waiting, and not one job earlier.
func TestARunIsFinishedOnlyWhenNothingIsLeftWaiting(t *testing.T) {
	f := setupNotify(t)

	remaining, err := f.store.Pipelines().UnfinishedJobs(t.Context(), f.pipeline.ID)
	if err != nil {
		t.Fatalf("count unfinished: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("a fresh run has %d unfinished jobs, want 1", remaining)
	}

	if err := f.store.Pipelines().FinishJob(t.Context(), f.job.ID, store.JobSuccess, 0); err != nil {
		t.Fatalf("finish the job: %v", err)
	}

	remaining, err = f.store.Pipelines().UnfinishedJobs(t.Context(), f.pipeline.ID)
	if err != nil {
		t.Fatalf("count unfinished: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("a finished job's run still has %d jobs waiting", remaining)
	}
}

// The queue keeps the facts as well as the sentence.
func TestNotificationKeepsWhatItKnows(t *testing.T) {
	f := setupNotify(t)

	context := f.contextFor(t, store.JobSuccess)
	id, err := f.store.Notifications().Record(t.Context(), "job.finished",
		"notify:telegram", "something happened", notificationLevel(context), context.asMap())
	if err != nil {
		t.Fatalf("record: %v", err)
	}

	notes, err := f.store.Notifications().Since(t.Context(), "notify:telegram", id-1, 10)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(notes) != 1 {
		t.Fatalf("read back %d notifications, want 1", len(notes))
	}

	if len(notes[0].Levels) != 1 || notes[0].Levels[0] != "success" {
		t.Errorf("levels are %v, want [success]", notes[0].Levels)
	}

	var data map[string]any
	if err := json.Unmarshal(encoded(t, notes[0].Data), &data); err != nil {
		t.Fatalf("decode the facts: %v", err)
	}
	if data["event"] != "job.finished" {
		t.Errorf("event is %v", data["event"])
	}
	if _, ok := data["pipeline"]; !ok {
		t.Error("the queue lost the pipeline")
	}
}

// A module must be able to read what it is told without a translation table of its
// own, so the facts are keyed the way the pipeline page names them.
func TestNotificationFactsAreNamedLikeThePageNamesThem(t *testing.T) {
	f := setupNotify(t)

	context := f.contextFor(t, store.JobSuccess)
	raw := encoded(t, context.asMap())

	for _, key := range []string{"\"iid\"", "\"ref\"", "\"sha_short\"", "\"url\""} {
		if !strings.Contains(string(raw), key) {
			t.Errorf("the facts do not carry %s", key)
		}
	}
}

// The text is a fallback, so it names what happened without dressing it up.
func TestTheFallbackSaysWhatHappened(t *testing.T) {
	f := setupNotify(t)

	text := notificationSummary(f.contextFor(t, store.JobFailed), f.project)
	if !strings.Contains(text, "build") || !strings.Contains(text, "failed") {
		t.Errorf("the fallback says %q, which does not say what happened", text)
	}
	if strings.Contains(text, f.project.Path) == false {
		t.Errorf("the fallback %q does not say where", text)
	}
}

// encoded is a value as the wire carries it, so the test reads what a module reads.
func encoded(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return raw
}
