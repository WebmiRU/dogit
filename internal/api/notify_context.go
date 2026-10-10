package api

import (
	"context"
	"fmt"
	"strings"

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

// notifyEvent queues a notification with its facts — if the pipeline said anything.
//
// It is the one way notifications are written, so that every module gets the same
// shape whatever happened and whoever caused it. The text is a fallback for a
// module with nothing of its own to say: a module is not obliged to use it.
//
// Whether there is a notification at all is the pipeline's own decision, read from its
// configuration in the commit that is running. A run with no `notify` block is silent
// however many channels are switched on: a project has a hundred pipelines and the
// person who wants to hear about the deployment does not want to hear about every
// experiment that was run on a branch nobody reads.
func (s *Server) notifyEvent(ctx context.Context, kind string, context notifyContext, text string) {
	entry, announces := s.pipelineAnnounces(ctx, context, context.Event, notificationLevel(context))
	if !announces {
		return
	}

	s.announceTo(ctx, entry, context, text)
}

// announceTo writes what a pipeline said, once per recipient, each in that recipient's
// own words.
//
// Split out from the decision so that the writing can be reached without a
// configuration on disk: whether a run speaks is a question about its file, and what
// it says is a question about these words, and neither should only be testable
// together.
func (s *Server) announceTo(ctx context.Context, entry pipelineNotifyEntry,
	context notifyContext, fallback string) {

	project, _ := context.Project["path"].(string)
	targets := s.notificationTargets(ctx, project)

	// Filled in per recipient, because what a recipient says when the pipeline said
	// nothing is a property of the recipient. Two rows can describe the same build in
	// two different ways, and a message that arrived in the wrong words is a message
	// somebody has to read twice.
	for _, target := range targets {
		s.deliver(ctx, target, context, target.withDefaults(entry, fallback))
	}
}

// withDefaults is what this recipient says when the pipeline said nothing itself.
//
// A default fills in and never speaks first: a run whose configuration says nothing is
// still silent, and these two settings change what a message looks like rather than
// whether there is one. They are the module's own — its wording, in its formatting —
// which is why they live on the recipient rather than in the core.
func (t notificationTarget) withDefaults(entry pipelineNotifyEntry, fallback string) pipelineNotifyEntry {
	out := entry

	if out.Title == "" {
		out.Title = t.value("default_title")
	}
	if out.Text == "" {
		if own := t.value("default_text"); own != "" {
			out.Text = own
		} else {
			out.Text = fallback
		}
	}
	return out
}

// deliver writes one notification to one recipient.
func (s *Server) deliver(ctx context.Context, target notificationTarget,
	context notifyContext, entry pipelineNotifyEntry) {

	project, _ := context.Project["path"].(string)
	level := notificationLevel(context)

	title, text, missing := renderNotification(entry, context, "")
	if len(missing) > 0 {
		// Not sent, and said out loud. A message with a hole in it reads as delivered
		// while telling nobody anything, and the reason belongs where the author of the
		// template will look.
		s.log.Error("a notification was not sent because its wording asks for something that does not exist",
			"project", project, "event", context.Event, "recipient", target.Label,
			"unknown", strings.Join(missing, ", "))
		return
	}

	facts := context.asMap()
	if title != "" {
		facts["title"] = title
	}

	address := target.Address()
	if _, err := s.store.Notifications().Record(ctx, context.Event, target.ModuleKind,
		text, level, facts, &address); err != nil {
		s.log.Warn("could not queue a notification",
			"event", context.Event, "recipient", target.Label, "error", err)
	}
}

// pipelineAnnounces reads what a pipeline said it would announce.
//
// The configuration is read at the commit that is running, not from the branch: a
// pipeline is a claim about code, and asking the working tree what it thinks now would
// have yesterday's run speaking with today's words.
func (s *Server) pipelineAnnounces(ctx context.Context, context notifyContext,
	event, level string) (pipelineNotifyEntry, bool) {

	project, _ := context.Project["path"].(string)
	sha, _ := context.Pipeline["sha"].(string)

	entry := pipelineNotifyEntry{}
	if project == "" || sha == "" {
		// Nothing to read the configuration from. Silence, and a line in the log: a
		// message about something the core cannot describe is worse than none.
		return entry, false
	}

	record, err := s.store.Projects().ByPath(ctx, project)
	if err != nil {
		s.log.Warn("could not find the project to read its notifications from",
			"project", project, "error", err)
		return entry, false
	}

	config, err := s.pipelineConfig(ctx, s.repos.PathFor(record), sha)
	if err != nil {
		// No configuration, or one that could not be read. A broken file is refused
		// when the pipeline is created, so this is mostly a project with nothing to say.
		s.log.Debug("no notification decision from the configuration", "project", project, "error", err)
		return entry, false
	}

	found, ok := config.Notify.Announces(event, level)
	return pipelineNotifyEntry{Title: found.Title, Text: found.Text}, ok
}

// pipelineNotifyEntry is what a pipeline said, with its ${…} not yet filled in.
type pipelineNotifyEntry = struct {
	Title string
	Text  string
}

// notificationLevel is how serious a thing is, in the few words everybody already
// uses for it.
func notificationLevel(context notifyContext) string {
	// The words are the ones everybody already uses for a build. The pipeline and
	// the job spell their states the same way, so one switch covers both — and the
	// state words are shared constants rather than two vocabularies.
	_, _, level := statusMark(contextLevelStatus(context))
	return level
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

// statusMark is the one sign for one state, everywhere.
//
// The interface draws a colour and a notification carries a character, and the two
// were chosen separately for years and disagreed: a message that said "success" in
// one place and showed a green tick in another meant somebody had to learn which
// page they were on. A state has one mark; how it is drawn is the reader's
// business.
//
// Colour is decided where something is rendered, not here: a message cannot be
// green. What belongs to the state is the character and the severity.
func statusMark(status string) (mark, colour string, level string) {
	switch status {
	case store.PipelineSuccess:
		return "✅", "green", "success"
	case store.PipelineFailed:
		return "❌", "red", "failure"
	case store.PipelineRunning:
		return "🟡", "yellow", "running"
	case store.PipelinePending:
		return "🔵", "blue", "running"
	case store.PipelineSuperseded:
		return "⚪", "grey", "superseded"
	case store.PipelineCanceled, store.PipelineInterrupted:
		return "⚪", "grey", "canceled"
	default:
		return "•", "grey", "running"
	}
}

// notificationSummary is the sentence a module falls back to.
//
// Deliberately plain: what happened and where to look, nothing else. Anything
// prettier is the module's business, and a prettier sentence here would be a
// sentence every channel is stuck with.
// notificationSummary is the fallback text: what happened, and where.
//
// Facts rather than a sentence, and no mark and no status word. The headline and the
// green tick belong to the channel — a Telegram message opens with a bold line, an
// email subject does not, and a webhook has neither — so what is left here is the one
// line every channel needs and none of them would write differently: what ran, in
// which run, in which project.
//
// A module with words of its own ignores this entirely, which is the point.
func notificationSummary(context notifyContext, project *models.Project) string {
	where := ""
	if project != nil {
		where = project.Path
	}

	run := ""
	if iid, ok := context.Pipeline["iid"]; ok {
		run = fmt.Sprintf("#%v", iid)
		if ref, ok := context.Pipeline["ref"].(string); ok && ref != "" {
			run += " " + ref
		}
	}

	switch {
	case context.Job != nil:
		name, _ := context.Job["name"].(string)
		return joinFacts(fmt.Sprintf("job %q", name), run, where)
	case context.Pipeline != nil:
		return joinFacts(run, where)
	default:
		return joinFacts(context.Event, where)
	}
}

// joinFacts puts the parts together without leaving holes where one is missing.
func joinFacts(parts ...string) string {
	kept := []string{}
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " · ")
}
