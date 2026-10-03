package api

import (
	"context"
	"fmt"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// What a notification module is told about something that happened.
//
// The point of this is that the core should not write anybody's messages. A
// sentence composed here is one every channel receives, which is why a sentence
// written in the core reads right for Telegram and wrong for Slack, wrong for a
// webhook and hopeless for email. So the core sends the facts and each module
// writes the words.
//
// The facts are a fixed set rather than whatever happened to be in hand: a module
// written against this can count on the fields being there. The alternative — an
// open map — would leave every module checking for keys the core happens to know
// about today.

// notifyContext is everything known about one thing that happened.
type notifyContext struct {
	Event    string           `json:"event"`
	Project  map[string]any   `json:"project"`
	Pipeline map[string]any   `json:"pipeline,omitempty"`
	Job      map[string]any   `json:"job,omitempty"`
	Images   []map[string]any `json:"images,omitempty"`
}

// asMap is the context as it goes into the queue.
func (c notifyContext) asMap() map[string]any {
	out := map[string]any{"event": c.Event, "project": c.Project}
	if c.Pipeline != nil {
		out["pipeline"] = c.Pipeline
	}
	if c.Job != nil {
		out["job"] = c.Job
	}
	if len(c.Images) > 0 {
		out["images"] = c.Images
	}
	return out
}

// pipelineContext describes one run.
//
// The link is the run's own number, not the number in the database: everything
// shown to a person uses the first, and a link built from the second leads
// somewhere else — to a different run, or to none.
func pipelineContext(pipeline *store.Pipeline, project *models.Project, status string) map[string]any {
	context := map[string]any{
		"iid":    pipeline.IID,
		"ref":    pipeline.Ref,
		"sha":    pipeline.SHA,
		"status": status,
		"url":    fmt.Sprintf("/p/%s/-/pipelines/%d", project.Path, pipeline.IID),
	}
	if len(pipeline.SHA) > 8 {
		context["sha_short"] = pipeline.SHA[:8]
	}
	if pipeline.CommitTitle != "" {
		context["commit_title"] = pipeline.CommitTitle
	}
	if pipeline.CommitAuthorName != "" {
		context["commit_author"] = pipeline.CommitAuthorName
	}
	if pipeline.StartedAt != nil {
		context["started_at"] = pipeline.StartedAt
	}
	if pipeline.FinishedAt != nil {
		context["finished_at"] = pipeline.FinishedAt
	}
	if ms := pipeline.DurationMs(); ms != nil {
		context["duration_ms"] = *ms
	}
	return context
}

func projectContext(project *models.Project) map[string]any {
	return map[string]any{
		"id":   project.ID,
		"path": project.Path,
		"name": project.Name,
	}
}

// jobContext describes one job within a run.
func jobContext(job *store.Job) map[string]any {
	context := map[string]any{
		"iid":    job.IID,
		"name":   job.Name,
		"stage":  job.Stage,
		"status": job.Status,
	}
	if job.DurationMS > 0 {
		context["duration_ms"] = job.DurationMS
	}
	return context
}

// imageContext describes what a run produced.
//
// Only what the core knows. The name and tag come from the job's own build
// definition; the digest, the size and the moment the image was built are the
// registry module's facts, and are not invented here — a module that needs them
// asks the module that keeps them, the same way it asks about images in the first
// place.
func imageContext(job *store.Job) []map[string]any {
	if len(job.Build) == 0 {
		return nil
	}
	image := map[string]any{}
	if name, ok := job.Build["image"].(string); ok && name != "" {
		image["name"] = name
	}
	if tag, ok := job.Build["tag"].(string); ok && tag != "" {
		image["tag"] = tag
	}
	if len(image) == 0 {
		return nil
	}
	return []map[string]any{image}
}

// notifyEvent queues a notification with its facts.
//
// It is the one way notifications are written, so that every module gets the same
// shape whatever happened and whoever caused it. The text is a fallback for a
// module with nothing of its own to say: a module is not obliged to use it.
func (s *Server) notifyEvent(ctx context.Context, kind string, context notifyContext, text string) {
	s.notify(ctx, kind, text, notificationLevel(context), context.asMap())
}

// notificationLevel is how serious a thing is, in the few words everybody already
// uses for it.
func notificationLevel(context notifyContext) string {
	// The words are the ones everybody already uses for a build. The pipeline and
	// the job spell their states the same way, so one switch covers both — and the
	// state words are shared constants rather than two vocabularies.
	switch contextLevelStatus(context) {
	case store.PipelineSuccess:
		return "success"
	case store.PipelineCanceled, store.PipelineInterrupted:
		return "canceled"
	case store.PipelineFailed:
		return "failure"
	default:
		return "running"
	}
}

func contextLevelStatus(context notifyContext) string {
	if context.Job != nil {
		if status, ok := context.Job["status"].(string); ok {
			return status
		}
	}
	if context.Pipeline != nil {
		if status, ok := context.Pipeline["status"].(string); ok {
			return status
		}
	}
	return ""
}

// pipelineFinishedStatus is how a run ended, judged by the jobs it is made of.
//
// The same rule the page uses, for the same reason: a message and a screen that
// disagree about whether a build passed is worse than either being absent.
func pipelineFinishedStatus(ctx context.Context, s *Server, pipelineID int64) string {
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, pipelineID)
	if err != nil {
		return store.PipelinePending
	}
	return store.PipelineStatus(&store.Pipeline{ID: pipelineID}, jobs)
}

// notificationSummary is the sentence a module falls back to.
//
// Deliberately plain: what happened and where to look, nothing else. Anything
// prettier is the module's business, and a prettier sentence here would be a
// sentence every channel is stuck with.
func notificationSummary(context notifyContext, project *models.Project) string {
	switch {
	case context.Job != nil:
		return fmt.Sprintf("Job %q %s in %s",
			context.Job["name"], context.Job["status"], project.Path)
	case context.Pipeline != nil:
		return fmt.Sprintf("Pipeline #%v %s in %s",
			context.Pipeline["iid"], context.Pipeline["status"], project.Path)
	default:
		return fmt.Sprintf("%s in %s", context.Event, project.Path)
	}
}
