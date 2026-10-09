package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/hooks"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulehost"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
)

// The kind of module that runs jobs.
//
// It is a module because it is: it registers, reports what it is doing, is given
// its settings from here, and can be forbidden with the same button as anything
// else. What it is not is something the core talks to directly — a runner on a
// machine the core has never heard of still does the work.
const runnerKind = "runner:docker"

// handleCreatePipeline runs a pipeline configuration against a ref.
func (s *Server) handleCreatePipeline(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	project, _, err := s.projectWithAccess(r, store.ActionTriggerCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	if !project.AllowPipelineTrigger {
		s.writeError(w, r, errForbidden("pipelines are turned off for this project"))
		return
	}

	var req struct {
		Ref string `json:"ref"`
		// Job runs one named job on its own. Without it the whole configuration runs,
		// which is what a push does and what most people mean by "run the pipeline".
		Job string `json:"job"`
		// Source says why this run is happening. Set by the core when a tag triggers
		// it; a client asking for a different reason is not believed.
		Source string `json:"-"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = project.DefaultBranch
	}
	// A run started by a tag says so, so that a page and a notification can tell a
	// release from a build somebody kicked off by hand.
	source := models.PipelineSourceWeb
	if req.Source != "" {
		source = models.PipelineSource(req.Source)
	}
	// A pipeline runs against a commit, so a ref that does not resolve is refused
	// here rather than by a runner three seconds later: "no commit on that branch"
	// is worth saying before anybody waits for a build that cannot start.
	rc, err := s.repoWithAccess(r, store.ActionTriggerCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	sha, err := s.git.RevParse(r.Context(), rc.RepoDir, ref)
	if err != nil {
		s.writeError(w, r, errBadRequestf("no commit on %q to run against", ref))
		return
	}

	// The configuration is read from the commit being run, not from the working
	// state of anybody's machine: a pipeline that ran yesterday's file is not the
	// pipeline this commit describes.
	config, err := s.pipelineConfig(r.Context(), rc.RepoDir, sha)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// A run triggered by a tag is on a tag, and the rules have to be able to tell.
	isTag := s.git.IsTag(r.Context(), rc.RepoDir, ref)
	pipelineRef := pipeline.RefFor(ref, sha, isTag)

	jobs := jobsFrom(config, pipelineRef, req.Job)
	if len(jobs) == 0 {
		s.writeError(w, r, errBadRequestf(
			"nothing to run for %q in %s", ref, pipeline.ConfigFileName))
		return
	}

	created, err := s.startRun(r.Context(), project, config, pipelineRef, source, jobs, user)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	run := created

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"pipeline": s.pipelineView(r, run, nil)})
}

// startedBy is what a log line says about who began a run.
//
// A nil user is not a user with an empty name: it is the ordinary case for a run started
// by a push or a tag, and reading a field out of it took the process down.
func startedBy(user *models.User) string {
	if user == nil {
		return "the push"
	}
	return user.Username
}

// startRun files a pipeline and says so, for every reason a pipeline is started.
//
// One place, because the three reasons — a person, a tag, a push — must not drift apart
// in what they announce. A run that files silently leaves a page showing nothing and a
// notification that never arrives, and the two are worst noticed exactly when somebody
// is waiting.
func (s *Server) startRun(ctx context.Context, project *models.Project, config *pipeline.Config,
	ref pipeline.Ref, source models.PipelineSource, jobs []store.Job, user *models.User,
) (*store.Pipeline, error) {

	actor := startedBy(user)

	commit := store.Commit{}
	if head := s.commitInfo(ctx, s.repos.PathFor(project), ref.SHA); head != nil {
		commit = store.Commit{
			Title:       head.Subject,
			AuthorName:  head.AuthorName,
			AuthorEmail: head.AuthorEmail,
		}
	}

	// Nobody started this run by hand, and saying so is what a nil author means. An
	// empty identity would be one that belongs to no user, and the database refuses it
	// because it should.
	var author *uuid.UUID
	if user != nil {
		id := user.ID
		author = &id
	}

	created, err := s.store.Pipelines().CreatePipeline(ctx, project.ID, ref.Name, ref.SHA,
		string(source), variablesFor(config, ref), author, commit, jobs)
	if err != nil {
		return nil, err
	}
	run := created

	s.log.Info("pipeline created", "project", project.Path, "pipeline", run.IID,
		"ref", ref.Name, "source", string(source), "by", actor)

	// Told after the commit, never inside it. A runner that heard this before the rows were
	// there would call claim and be told the queue was empty, and go back to sleep — which is
	// exactly the state this is meant to end.
	s.announceWork(ctx)

	s.publishPipeline(ctx, project.ID, nil, models.EventPipelineCreated, map[string]any{
		"pipeline_iid": run.IID,
		"ref":          ref.Name,
		"sha":          ref.SHA,
		"source":       string(source),
		// The tag, when there is one. Carried so that a page or a notification can
		// say "v1.01" rather than "a run", which is the whole reason a release is a
		// separate kind of event.
		"tag": ref.Tag(),
	})

	// The deployment's plan, sent with the run rather than when the deployment starts.
	//
	// Published here because this is the last moment everything it depends on is known
	// without having done any of the work: whether the run builds an image, and
	// whether it has pre- and post-step jobs. Sent from the deployment job instead —
	// where the same facts are known a moment later — the build and push steps arrive
	// first and reach the page before anything says what they are called.
	//
	// The step list is the core's to give: the module that carries the deployment out
	// is not asked to describe it in advance, and a client that kept its own copy of
	// the phases would be drawing a list of what might happen rather than what will.
	s.publishDeployPlan(ctx, project, config, run, jobs)
	// Queued with the facts, not with a sentence: what a channel says about this is
	// that channel's business. The text here is only for a module with nothing of
	// its own to say.
	started := notifyContext{
		Event:    "pipeline.started",
		Project:  projectContext(project),
		Pipeline: pipelineContext(run, project, store.PipelineRunning),
	}
	s.notifyEvent(ctx, "pipeline.started", started, notificationSummary(started, project))

	return run, nil
}

// WatchPushes starts the runs a push asks for.
//
// The same run either way, on purpose. A tag pushed from somebody's laptop and a tag
// typed into this interface are the same decision about the same commit, and the one
// that silently did nothing would be discovered in production rather than before it —
// which is the worst way to find out that a feature you were told works does not.
//
// A branch does not start a run unconditionally either: it starts one when the file at
// that commit says something runs for it. The rules in the repository are the only thing
// consulted, so a project that wants nothing automatic gets nothing automatic without
// anybody having to configure that anywhere.
//
// Runs in the background, off the durable log rather than off the hook: the hook is a
// short-lived process for somebody's push and must not be held up by a build queue.
func (s *Server) WatchPushes(ctx context.Context) {
	updates, unsubscribe := s.events.Subscribe(64)
	defer unsubscribe()

	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-updates:
			if !open {
				return
			}
			if event.Kind != models.EventPush {
				continue
			}
			s.startRunsForPush(ctx, event)
		}
	}
}

// startRunsForPush is one push, read for the refs in it that start something.
func (s *Server) startRunsForPush(ctx context.Context, event models.Event) {
	var raw hooks.Payload
	if err := json.Unmarshal(event.Payload, &raw); err != nil {
		s.log.Debug("push event carried something unreadable", "error", err)
		return
	}

	for _, update := range raw.RefUpdates {
		if isZeroSHA(update.NewSHA) {
			// Deleting a ref starts nothing. A deleted tag deploys nothing: whatever it
			// released stays released, because taking something out of production
			// because a name was removed from a repository is not what removing a tag
			// means.
			continue
		}

		switch {
		case strings.HasPrefix(update.Ref, "refs/tags/"):
			s.startRunForPushedRef(ctx, raw.ProjectPath,
				strings.TrimPrefix(update.Ref, "refs/tags/"), update.NewSHA, true)
		case strings.HasPrefix(update.Ref, "refs/heads/"):
			s.startRunForPushedRef(ctx, raw.ProjectPath,
				strings.TrimPrefix(update.Ref, "refs/heads/"), update.NewSHA, false)
		}
	}
}

func (s *Server) startRunForPushedRef(ctx context.Context, projectPath, name, sha string, isTag bool) {
	project, err := s.store.Projects().ByPath(ctx, projectPath)
	if err != nil {
		s.log.Warn("a ref arrived for a project that is gone", "project", projectPath,
			"ref", name, "error", err)
		return
	}
	// Whoever pushed is not carried over: this runs with no request behind it, and
	// inventing an author for somebody else's push would put their name on a run they
	// did not start. An unattributed release is better than a wrong one.
	_ = s.startRunForRef(ctx, project, name, sha, isTag)
}

// isZeroSHA is git's way of saying a ref is gone.
func isZeroSHA(sha string) bool {
	return sha == "" || strings.Trim(sha, "0") == ""
}

// startRunForRef starts a run for a ref that something else decided on — a tag arriving,
// or a push — and says why it did not when it did not.
//
// Returns nothing rather than an error in the ordinary cases, because a tag is created
// whether or not it starts anything: refusing to tag a commit because no build was
// configured for tags would make the tag itself the thing that cannot be done.
func (s *Server) startRunForRef(ctx context.Context, project *models.Project,
	name, sha string, isTag bool) *store.Pipeline {

	if !project.AllowPipelineTrigger {
		return nil
	}

	config, err := s.pipelineConfig(ctx, s.repos.PathFor(project), sha)
	if err != nil {
		s.log.Info("no pipeline configuration for this ref", "project", project.Path,
			"ref", name, "error", err)
		return nil
	}

	ref := pipeline.RefFor(name, sha, isTag)
	jobs := jobsFrom(config, ref, "")

	// The places this project does not deploy to by itself, dropped from a run that
	// started by itself. The run still happens and the image is still built and pushed:
	// the switch is about where the result may be put, and stopping the build would be
	// stopping something else in order to say something about it.
	jobs = s.withoutAutodeployJobs(ctx, project, jobs)

	if len(jobs) == 0 {
		s.log.Info("nothing runs for this ref", "project", project.Path, "ref", name)
		return nil
	}

	source := models.PipelineSourceTag
	if !isTag {
		source = models.PipelineSourcePush
	}

	// One run per commit per cause.
	//
	// Not only about repeated pushes: the durable event log is read from the beginning
	// each time the process starts, so without this every restart builds every commit
	// the repository has ever had pushed at it.
	exists, err := s.store.Pipelines().AutomaticRunExists(ctx, project.ID, sha, string(source))
	if err != nil {
		s.log.Warn("could not tell whether this commit was already run", "project", project.Path,
			"ref", name, "error", err)
	} else if exists {
		s.log.Debug("this commit was already run", "project", project.Path, "ref", name)
		return nil
	}

	// No author rather than an empty one. A run started by a push has nobody behind it
	// in this process, and an empty user is not nobody: it is a user with an identity
	// that belongs to nobody, which the database rightly refuses.
	run, err := s.startRun(ctx, project, config, ref, source, jobs, userFrom(ctx))
	if err != nil {
		s.log.Warn("a run could not be started for this ref", "project", project.Path,
			"ref", name, "error", err)
		return nil
	}
	return run
}

// pipelineConfig reads a project's configuration from a commit.
//
// The file is read at the commit rather than from the working tree, because a
// pipeline is a claim about code: running yesterday's configuration against today's
// code produces a build that never existed.
func (s *Server) pipelineConfig(ctx context.Context, repoDir, sha string) (*pipeline.Config, error) {
	contents, _, _, err := s.git.CatFile(ctx, repoDir, sha, pipeline.ConfigFileName)
	if err != nil {
		return nil, errNotFoundf("this project has no %s at %s", pipeline.ConfigFileName, shortSHA(sha))
	}

	config, err := pipeline.Parse(contents)
	if err != nil {
		// A broken configuration is reported with the file and the commit, because
		// "cannot run" without those is not something anybody can act on.
		return nil, errBadRequestf("%s at %s could not be read: %v",
			pipeline.ConfigFileName, shortSHA(sha), err)
	}
	return config, nil
}

// jobsFrom turns a configuration into the jobs a run will actually do.
//
// A job whose rules exclude this branch is left out entirely rather than created
// and skipped: a pipeline page listing a deploy job that is not going to deploy is
// noise, and one listing it as skipped invites somebody to read the reason.
// expandBuild is the run's variables written into the build's own fields.
//
// Only the fields that name things, because only those are ever written as variables
// and rewriting a dockerfile path or a build argument on every run would be a surprise
// with no upside.
func expandBuild(build map[string]any, variables map[string]string) map[string]any {
	if build == nil {
		return nil
	}
	expanded := make(map[string]any, len(build))
	for key, value := range build {
		if text, ok := value.(string); ok {
			expanded[key] = os.Expand(text, func(name string) string {
				return variables[name]
			})
			continue
		}
		expanded[key] = value
	}
	return expanded
}

// variablesFor is what a run's scripts are told about the run itself.
//
// Written here rather than in the runner because the runner is told the answer, not
// asked for it: a build script that had to guess whether it is on a tag would be one
// git call away from building the wrong thing.
func variablesFor(config *pipeline.Config, ref pipeline.Ref) map[string]string {
	variables := config.VariablesAsStrings()
	variables["CI_COMMIT_REF_NAME"] = ref.Name
	variables["CI_COMMIT_SHA"] = ref.SHA
	variables["CI_COMMIT_SHORT_SHA"] = ref.ShortSHA()
	variables["CI_COMMIT_BRANCH"] = ref.Branch()
	variables["CI_COMMIT_TAG"] = ref.Tag()
	return variables
}

func jobsFrom(config *pipeline.Config, ref pipeline.Ref, only string) []store.Job {
	jobs := []store.Job{}
	for _, name := range config.Order {
		spec, ok := config.Jobs[name]
		if !ok {
			continue
		}
		if only != "" && name != only {
			continue
		}
		if !pipeline.RunsOn(spec, ref, false) {
			continue
		}

		script := append([]string{}, spec.BeforeScript...)
		script = append(script, spec.Script...)

		jobs = append(jobs, store.Job{
			Name:         name,
			Stage:        spec.Stage,
			Image:        spec.Image,
			Script:       script,
			AllowFailure: spec.AllowFailure,
			Needs:        spec.Needs,
			// The build's own fields, with the run's answers in them. A tag written
			// in the file as $CI_COMMIT_TAG is how a repository says "name the image
			// after whatever this run is" without knowing what that will be.
			Build: expandBuild(spec.Build, variablesFor(config, ref)),
		})
	}

	// The deployment is a job of this run, not a separate thing that happens later.
	//
	// Adding it here rather than creating it when the build finishes means the
	// pipeline page lists the deploy from the start, so a person watching a run knows
	// a deployment is coming — which is the difference between waiting and wondering.
	// It also means it is retried and logged like anything else, because there is
	// nothing special about it once it exists.
	//
	// No rules of its own: a deploy block has none to have, and inventing a way to
	// exclude one by branch would be a feature nobody asked for.
	//
	// Skipped when one job was named on the command line, because that is somebody
	// asking to run one thing; adding a deployment they did not ask for would be the
	// pipeline surprising them in the most expensive way available.
	//
	// A place now carries rules, and so does the mark that says a place is only reached
	// by a tag. Both are read here rather than at deploy time, so that a run says what
	// it is going to do before it starts doing any of it — a page listing three places
	// shows one line, not three that appear and disappear.
	if only == "" {
		for _, spec := range pipeline.DeploysFor(config.Deploys, ref) {
			jobs = append(jobs, deployJob(spec))
		}
	}

	return jobs
}

// deployJob is the job that carries out a deployment.
//
// It has no script, which is exactly why no runner may claim it: there is nothing on
// a machine to run. The core carries it out itself, once the jobs it needs have
// passed, by handing the task to the module the file named.
func deployJob(spec pipeline.DeploySpec) store.Job {
	deploy := map[string]any{}
	encoded, err := json.Marshal(spec)
	if err == nil {
		_ = json.Unmarshal(encoded, &deploy)
	}

	// One job per place, each named for the place. The name is what a failed run says
	// it was deploying to, and "deploy" said only that something was deployed.
	name := "deploy"
	if strings.TrimSpace(spec.Name) != "" {
		name = "deploy:" + strings.TrimSpace(spec.Name)
	}

	return store.Job{
		// "deploy" is a reserved job name in the file's own terms: somebody who
		// writes a job called deploy gets this one instead of theirs, which is why it
		// is checked for while the configuration is read.
		Name:   name,
		Stage:  "deploy",
		Deploy: deploy,
	}
}

// registryHost is the address a docker client is given as the registry host:
// no scheme, no trailing slash.
func registryHost(address string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
	return strings.TrimSuffix(trimmed, "/")
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// handleListPipelines returns a project's pipelines.
func (s *Server) handleListPipelines(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	query := store.PipelineQuery{
		Search:  r.URL.Query().Get("search"),
		Ref:     r.URL.Query().Get("ref"),
		Status:  r.URL.Query().Get("status"),
		Source:  r.URL.Query().Get("source"),
		Page:    atoiOr(r.URL.Query().Get("page"), 1),
		PerPage: atoiOr(r.URL.Query().Get("per_page"), store.PipelinePageSizeDefault),
	}

	pipelines, total, err := s.store.Pipelines().ListPipelinesPage(r.Context(), project.ID, query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]map[string]any, 0, len(pipelines))
	for index := range pipelines {
		pipeline := &pipelines[index]
		jobs, err := s.store.Pipelines().JobsOfPipeline(r.Context(), pipeline.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		views = append(views, s.pipelineView(r, pipeline, jobs))
	}

	// How many pages there are, said rather than left to be worked out: a control that
	// stops early looks like a list that ends.
	pages := (total + query.PerPage - 1) / query.PerPage
	if pages < 1 {
		pages = 1
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"pipelines": views,
		"total":     total,
		"page":      query.Page,
		"pages":     pages,
		"per_page":  query.PerPage,
	})
}

// handleGetPipeline returns one pipeline with its jobs.
func (s *Server) handleGetPipeline(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	iid, err := strconv.Atoi(pathParam(r, "pipelineIID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a pipeline number is required"))
		return
	}

	pipeline, err := s.store.Pipelines().PipelineByIID(r.Context(), project.ID, iid)
	if err != nil {
		s.writeError(w, r, errNotFoundf("pipeline %d does not exist", iid))
		return
	}

	jobs, err := s.store.Pipelines().JobsOfPipeline(r.Context(), pipeline.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]map[string]any, 0, len(jobs))
	for index := range jobs {
		views = append(views, jobView(r, &jobs[index]))
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"pipeline": s.pipelineView(r, pipeline, jobs),
		"jobs":     views,
	})
}

// handleRetryJob puts one job of a finished run back in the queue.
//
// Retrying is a separate call rather than a new pipeline because the point is to
// run the same thing again: a different pipeline would be a different build of a
// different commit's configuration, and would answer a question nobody asked.
func (s *Server) handleRetryJob(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionTriggerCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	pipelineIID, err := strconv.Atoi(pathParam(r, "pipelineIID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a pipeline number is required"))
		return
	}
	jobIID, err := strconv.Atoi(pathParam(r, "jobIID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a job number is required"))
		return
	}

	pipeline, err := s.store.Pipelines().PipelineByIID(r.Context(), project.ID, pipelineIID)
	if err != nil {
		s.writeError(w, r, errNotFoundf("pipeline %d does not exist", pipelineIID))
		return
	}

	job, err := s.store.Pipelines().JobByIID(r.Context(), pipeline.ID, jobIID)
	if err != nil {
		s.writeError(w, r, errNotFoundf("job %d does not exist", jobIID))
		return
	}

	if job.Status == store.JobRunning || job.Status == store.JobPending {
		s.writeError(w, r, errBadRequestf("%s is already %s", job.Name, job.Status))
		return
	}

	if err := s.store.Pipelines().RetryJob(r.Context(), job.ID); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("job retried", "project", project.Path, "pipeline", pipeline.IID, "job", job.Name)
	s.publishPipeline(r.Context(), project.ID, userFrom(r.Context()), models.EventPipelineUpdated, map[string]any{
		"pipeline_id": pipeline.IID,
		"job_name":    job.Name,
		"status":      store.JobPending,
	})
	retried := notifyContext{
		Event:    "job.retried",
		Project:  projectContext(project),
		Pipeline: pipelineContext(pipeline, project, store.PipelineRunning),
		Job:      jobContext(job),
	}
	s.notifyEvent(r.Context(), "job.retried", retried, notificationSummary(retried, project))

	s.writeJSON(w, r, http.StatusOK, map[string]any{"job": jobView(r, job)})
}

// pendingJobCount is how deep the queue is, or nil when that cannot be read.
//
// `any` rather than a plain count, because the one answer worse than not telling a runner how
// deep the queue is, is telling it the queue is empty. A runner that believes it is idle on
// an empty queue is behaving correctly, and the panel would be the only thing saying
// otherwise. Nil means "not known", which a runner can pass on and a person can act on.
// pendingJobCount is how deep the queue is, for whoever wants to know.
//
// A context rather than the request, because the queue is also asked about from the channel and
// from anywhere else that is not a request at all — and a function that takes a request can only
// be called from one.
func (s *Server) pendingJobCount(ctx context.Context) any {
	count, err := s.store.Pipelines().CountPendingJobs(ctx)
	if err != nil {
		s.log.Warn("count the queue", "error", err)
		return nil
	}
	return count
}

// handleClaimJob hands the next pending job to a runner module.
//
// The endpoint is module-authenticated, so a runner needs no user token and no
// session: it is a machine on a network, talking to the core about work.
func (s *Server) handleClaimJob(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	if !strings.HasPrefix(integration.Kind, "runner:") {
		// Not an error, but not a claim either: the answer is "nothing to do" rather
		// than a refusal, because a runner polling should not have to distinguish
		// the two to behave correctly.
		s.writeJSON(w, r, http.StatusOK, map[string]any{"job": nil})
		return
	}

	var req struct {
		Tags []string `json:"tags"`
	}
	if err := decodeOptionalJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	job, err := s.store.Pipelines().ClaimJob(r.Context(), integration.ID, req.Tags)
	if err != nil {
		if err == store.ErrNotFound {
			// Nothing to do is an answer, not a failure: a runner polls this every few
			// seconds and an empty queue is the normal case.
			//
			// The depth goes back either way, and that is the point of it. A runner
			// that is polling an empty queue is the one case where the answer is not
			// obvious from the runner's side — it cannot tell "nobody has pushed
			// anything" from "the jobs are there and nobody is coming for them", and
			// those two want opposite responses from whoever is watching.
			s.writeJSON(w, r, http.StatusOK, map[string]any{
				"job":     nil,
				"waiting": s.pendingJobCount(r.Context()),
			})
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.log.Info("job claimed", "job", job.ID, "job_name", job.Name,
		"project", job.ProjectPath, "runner", integration.Kind)

	answer := map[string]any{
		"job":     s.runnerJobView(r, job),
		"waiting": s.pendingJobCount(r.Context()),
	}

	// A job that builds an image needs a credential for the registry, and the
	// registry's naming rule is the registry's own. The core asks the registry
	// module what the name is and mints a token for exactly that project, so the
	// runner never holds a general-purpose account.
	if len(job.Build) > 0 {
		credentials, err := s.registryCredentialsFor(r, job)
		if err != nil {
			// The job is already marked as this runner's, and the runner never heard
			// about it: nothing will ever report on it. It goes back into the queue
			// rather than staying "running" on a machine that is not running it,
			// which is a pipeline that hangs for ever over a problem another attempt
			// may not even have.
			if releaseErr := s.store.Pipelines().ReleaseJob(r.Context(), job.ID); releaseErr != nil {
				s.log.Error("could not put a claimed job back", "job", job.ID, "error", releaseErr)
			}
			s.writeError(w, r, err)
			return
		}
		answer["registry"] = credentials

		// The name the image was given is written back onto the job.
		//
		// It is worked out here rather than kept only in this answer because the job
		// is the thing that produced the image: everything said about it afterwards
		// — a notification, a page, an audit — has to be able to name it, and a name
		// that only exists in a reply the runner will never see is no name at all.
		if image, ok := credentials["image"].(string); ok && image != "" {
			job.Build["image"] = image
			if err := s.store.Pipelines().SetJobBuild(r.Context(), job.ID, job.Build); err != nil {
				s.log.Warn("record what a job produces", "job", job.ID, "error", err)
			}
		}
	}

	// Where the logs go: object storage, not a file on the machine that ran the
	// job. A runner that dies must not take the log with it.
	answer["log_key"] = s.jobLogKey(job)

	// How to get the code. The key itself is not issued here: a runner may sit on a
	// job for as long as it likes before it starts, and a key whose clock ran from
	// the moment of claiming would be dead before the machine was ready. The runner
	// asks for one when it is actually about to clone, which is the moment the key
	// is needed and the moment its life should start.
	answer["clone_url"] = s.cloneURL()

	s.writeJSON(w, r, http.StatusOK, answer)
}

// pushRegistryFor is the registry module a job pushes to.
//
// The cluster decides, and this is how: a place names the registry it pulls from, and a build
// pushes where the cluster will pull from, or the deployment fails on the cluster's side with a
// pull error that says nothing about the push. The same field already chooses what a deployment
// pulls with, so push and pull read one thing and cannot disagree.
//
// A place that names no registry falls back to "the one registry on this instance", which is the
// whole of what the old code ever did — it took whichever module was oldest. With several and
// nowhere to choose from, this refuses instead: the choice decides where an image is written, and
// a build that pushes to the wrong registry is a build whose output nobody asked for.
//
// A job with no deploy target at all — a library, a test run, anything that builds and never
// deploys — is in the same position, and gets the same answer.
func (s *Server) pushRegistryFor(ctx context.Context, project *models.Project,
	job *store.Job) (*models.Integration, error) {

	place := strings.TrimSpace(asString(job.Deploy["Target"]))
	if place != "" {
		if module, err := s.deployModuleForPlace(ctx, project, place,
			strings.TrimSpace(asString(job.Deploy["Module"]))); err == nil && module != nil {

			// A place with no registry of its own pulls from the instance's own registry, which
			// is the address the image carries rather than a configured one — so there is
			// nothing to match on and the fallback below is the right answer.
			if address := s.placesRegistry(ctx, project, module, place, nil); address != "" {
				if registry, _, err := s.registryServing(ctx, address); err == nil {
					return registry, nil
				} else if !errors.Is(err, store.ErrNotFound) {
					return nil, err
				}
				return nil, errBadRequestf(
					"cluster %q names the registry %s, and no registry module on this instance "+
						"publishes that address, so there is nowhere to push", place, address)
			}
		}
	}

	registries, err := s.store.Integrations().ByKindAll(ctx, registryKind)
	if err != nil {
		return nil, err
	}
	usable := make([]*models.Integration, 0, len(registries))
	for _, candidate := range registries {
		if candidate.Enabled {
			usable = append(usable, candidate)
		}
	}
	switch len(usable) {
	case 0:
		if len(registries) > 0 {
			return nil, errBadRequest("the registry module has been forbidden")
		}
		return nil, errBadRequest("no registry is installed on this instance, so nothing can be pushed")
	case 1:
		return usable[0], nil
	default:
		return nil, errBadRequestf(
			"this instance has %d registry modules (%s) and this build does not say which cluster "+
				"it is for, so there is no way to tell where its image should go",
			len(usable), s.moduleNames(usable))
	}
}

// registryCredentialsFor works out what a job needs to push an image.
func (s *Server) registryCredentialsFor(r *http.Request, job *store.Job) (map[string]any, error) {
	project, err := s.store.Projects().ByPath(r.Context(), job.ProjectPath)
	if err != nil {
		return nil, errBadRequestf("project %q no longer exists", job.ProjectPath)
	}

	registry, err := s.pushRegistryFor(r.Context(), project, job)
	if err != nil {
		return nil, err
	}

	// The credential belongs to the project, not to the person who pressed the
	// button: a job runs at three in the morning, and it runs the project's work,
	// not somebody's session.
	builder, err := s.serviceUser(r.Context())
	if err != nil {
		return nil, err
	}

	token, _, err := s.mintModuleToken(r.Context(), builder, registry, &project.ID,
		[]string{models.ScopeRegistryPush, models.ScopeRegistryPull}, 2*time.Hour)
	if err != nil {
		return nil, err
	}

	settings, err := s.store.Integrations().SettingsFor(r.Context(), registry.ID, nil, &project.ID, registry.Capabilities.Settings)
	if err != nil {
		return nil, err
	}

	template := ""
	if raw, ok := settings["image_name_template"]; ok {
		_ = json.Unmarshal(raw, &template)
	}
	if template == "" {
		if spec, found := settingSpecOf(registry, "image_name_template"); found {
			template, _ = spec.Default.(string)
		}
	}
	if template == "" {
		return nil, errBadRequest("the registry module has no image name template")
	}

	address := ""
	if raw, ok := settings["public_address"]; ok {
		_ = json.Unmarshal(raw, &address)
	}
	if strings.TrimSpace(address) == "" {
		if spec, found := settingSpecOf(registry, "public_address"); found {
			address, _ = spec.Default.(string)
		}
	}
	if strings.TrimSpace(address) == "" {
		address, _ = modulehost.BaseURL(s.cfg.PublicHost, registry.Capabilities.Routing)
	}

	return map[string]any{
		"url":          strings.TrimRight(address, "/"),
		"internal_url": strings.TrimRight(registry.Endpoint, "/"),
		// With the address in front, because that is what the client is told to
		// push to. A bare name is Docker Hub's: docker reads everything before the
		// first slash as a registry, and an unqualified name means the public one,
		// whatever the client happens to be logged in to.
		"image":      registryHost(address) + "/" + imageNameFor(template, project.Path, jobRef(job)),
		"token":      token,
		"expires_in": int((2 * time.Hour).Seconds()),
	}, nil
}

// builderName is the account machines act as.
const builderName = "builder"

// serviceUser is the account a machine acts as.
//
// A job has no session and no person behind it, so the credential it pushes with
// belongs to a system account rather than to whoever pressed the button. Naming
// it keeps the audit trail honest: an image pushed at three in the morning is
// attributed to the builder, not to the person who started the pipeline.
//
// The account is created the first time a build asks for one. It is a real row in
// the users table rather than a name in a log line, because a credential has to
// point at something: the token table's foreign key would otherwise refuse every
// build on this instance, and the reason would be an integrity error instead of a
// missing account.
func (s *Server) serviceUser(ctx context.Context) (*models.User, error) {
	user, err := s.store.Users().ByUsername(ctx, builderName)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}

	user = &models.User{
		Username: builderName,
		Email:    builderName + "@" + serviceEmailDomain,
		Name:     "Build service",
		// Nothing can sign in as it: the hash is of nothing anybody has, and the
		// account exists to be named in an audit trail rather than to be used.
		PasswordHash: []byte("!"),
	}
	if err := s.store.Users().Create(ctx, user); err != nil {
		// Somebody else creating it at the same moment is success, not failure.
		if errors.Is(err, store.ErrConflict) {
			return s.store.Users().ByUsername(ctx, builderName)
		}
		return nil, err
	}
	return user, nil
}

// serviceEmailDomain is what a service account's address looks like. Reserved and
// unroutable, so a message to it goes nowhere rather than to somebody.
const serviceEmailDomain = "users.noreply.dogit.invalid"

// jobRef is the branch a job's image is named after, when the build says so.
func jobRef(job *store.Job) string {
	if ref, ok := job.Build["ref"].(string); ok {
		return ref
	}
	return ""
}

// handleJobKey issues the key a runner needs to clone, at the moment it starts.
//
// The clock starts here rather than when the job was claimed. A runner with one
// machine and several projects may hold a job for minutes before it begins, and a
// key that had to survive that wait would be a long-lived credential for a machine
// that did not need it yet.
//
// Asking twice within one job returns the same key: a runner whose first attempt
// failed halfway needs to be able to try again without being refused for using up
// its allowance.
func (s *Server) handleJobKey(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	jobID, err := strconv.ParseInt(pathParam(r, "jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a job id is required"))
		return
	}

	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such job"))
		return
	}

	// Only the runner that was given the job may ask for its key. Without this,
	// any runner on the network could mint a credential for a job it does not hold.
	if job.RunnerID == nil || *job.RunnerID != integration.ID {
		s.writeError(w, r, errForbidden("this job belongs to another runner"))
		return
	}
	if job.Status != store.JobRunning {
		s.writeError(w, r, errBadRequestf("this job is %q, not running", job.Status))
		return
	}

	key, err := s.issueJobKey(r, job.ProjectID, job.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"key": key, "clone_url": s.cloneURL()})
}

// handleFinishJob records what a runner did with a job.
// handleJobProgress takes a runner's word for which part of a job it is on, and
// announces it.
//
// A job's log says what was printed; this says what it meant. A build streams
// hundreds of lines of layers and pushes, and a page watching it live cannot tell
// from those which part of the work is being done — so the machine doing the work
// names the phase, and the same feed the deployment already uses carries it.
//
// Refused for a job another runner owns, on the same grounds as finishing one: this
// is a claim about somebody else's work.
func (s *Server) handleJobProgress(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	jobID, err := strconv.ParseInt(pathParam(r, "jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a job id is required"))
		return
	}

	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such job"))
		return
	}
	if job.RunnerID == nil || *job.RunnerID != integration.ID {
		s.writeError(w, r, errForbidden("this job belongs to another runner"))
		return
	}

	var req struct {
		Phase   string `json:"phase"`
		Message string `json:"message"`
		Ready   int    `json:"ready"`
		Desired int    `json:"desired"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	phase := strings.TrimSpace(req.Phase)
	message := strings.TrimSpace(req.Message)
	if phase == "" || message == "" {
		s.writeError(w, r, errBadRequest("a phase and a message are both required"))
		return
	}

	project, err := s.store.Projects().ByID(r.Context(), job.ProjectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Said once per place this run is on its way to, because a card is watching one place
	// and drops an event that does not name it.
	//
	// A runner says what it is doing and the core is what knows where the run is going, so
	// a build's progress that names no place reaches a page that is not watching a build:
	// the two steps a deployment's plan promises sit there with nothing under them while
	// twenty seconds of build go by, and the plan looks like two steps nobody performs.
	// Which is what it was — not the work, which the runner does and says, but the place
	// the work is for, which only the core knows.
	//
	// Once for the project when the run names no place at all: a build that is not on its
	// way anywhere is still worth a line, and a page that cannot see it says so by saying
	// nothing, which is not the same as having nothing to say.
	//
	// Which operation this is about, and which is not always the job that is speaking: the image
	// is built by a job of its own, and its progress is part of the deployment it was built for.
	// Published under the reporting job's own id it is a second operation as far as a page is
	// concerned — a card of its own, beside the deployment's, for one push — and the page's rule
	// is one card per operation.
	publish := func(record map[string]any, operation int64) {
		payload := map[string]any{
			"job_id":  operation,
			"phase":   phase,
			"message": message,
			"ready":   req.Ready,
			"desired": req.Desired,
		}
		if record != nil {
			payload["deployment"] = record
		}

		// Written down as well as published, and for a different reader: the event reaches
		// pages that are open, and the note reaches the ones that arrive later.
		s.rememberDeployProgress(r.Context(), operation, payload)

		// The phase this one ends, said before the one that ends it.
		//
		// Published and not written down: it is a fact about the moment the phase changed, and
		// a note read an hour later must say what the job was doing at the end rather than
		// which stage it was leaving behind. Kept before the new line so a page cannot have
		// heard of the new phase and not the closing of the one it replaced.
		if closed := s.phaseBefore(job.ID, phase); closed != "" && record != nil {
			closing := map[string]any{
				"job_id":     operation,
				"phase":      closed,
				"message":    "finished",
				"finished":   true,
				"deployment": record,
			}
			s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployOperation, closing)
		}

		s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployOperation, payload)
	}

	places := s.placesOfRun(r.Context(), project, job.PipelineID)
	if len(places) == 0 {
		// A run that is not on its way anywhere has no operation to file this under, and the
		// job that is doing the work is the operation: a build nobody asked to deploy is a thing
		// of its own, and there is nothing else it could be filed under.
		publish(nil, job.ID)
	}
	for _, place := range places {
		record := map[string]any{"cluster": place.place}
		if place.namespace != "" {
			record["namespace"] = place.namespace
		}
		publish(record, whichOperation(job.ID, job.Deploy != nil, place))
	}

	s.writeJSON(w, r, http.StatusNoContent, nil)
}

// whichOperation says which deployment a line from this job belongs to.
//
// A run has more jobs than it has operations: the image is built by a job of its own and put
// somewhere by another, and a page watching a place is watching deployments. Filed under the
// reporting job's own id, a build is a second operation as far as that page is concerned — a
// card of its own beside the deployment's, for one push.
//
// The job that is the deployment is always its own operation, whatever else the run holds: the
// module speaks for it and nobody else can.
func whichOperation(reporting int64, isDeployment bool, place aPlaceNamed) int64 {
	if isDeployment || place.job == 0 {
		return reporting
	}
	return place.job
}

// runStillHasWork is whether a run has a job left to do, in the sense that decides whether
// a line about a finished job is also a line about a finished run.
//
// Out of the run's own jobs rather than out of the run's status, because a run's status is
// only written once, at the end: read there, it says "running" for the whole run and cannot
// answer this at all. And it is asked of the run rather than of the job because the whole
// of the difference between the two is what else the run has in it.
func (s *Server) runStillHasWork(ctx context.Context, runID int64) bool {
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, runID)
	if err != nil {
		// Cannot tell, and the answer that keeps a page honest is the careful one: a card
		// left open over nothing ends on the next thing said, while a card closed over a
		// run that is still going cannot be reopened by the run itself.
		return true
	}
	for _, job := range jobs {
		if job.Status == store.JobPending || job.Status == store.JobRunning {
			return true
		}
	}
	return false
}

func (s *Server) handleFinishJob(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	jobID, err := strconv.ParseInt(pathParam(r, "jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a job id is required"))
		return
	}

	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such job"))
		return
	}
	// A runner may only finish a job it was given. Without this, any runner on the
	// network could mark somebody else's job as passed.
	if job.RunnerID == nil || *job.RunnerID != integration.ID {
		s.writeError(w, r, errForbidden("this job belongs to another runner"))
		return
	}

	var req struct {
		Status     string `json:"status"`
		DurationMS int64  `json:"duration_ms"`
		Error      string `json:"error"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	status := req.Status
	switch status {
	case store.JobSuccess, store.JobFailed, store.JobCanceled:
	case "":
		status = store.JobSuccess
	default:
		s.writeError(w, r, errBadRequestf("unknown job status %q", status))
		return
	}

	duration := time.Duration(req.DurationMS) * time.Millisecond
	// The reason is kept as well as the verdict: a job that died before printing
	// anything has a log with nothing in it, and a page that can say only "failed"
	// sends the reader somewhere else to find out why.
	reason := strings.TrimSpace(req.Error)
	if err := s.store.Pipelines().FinishJob(r.Context(), jobID, status, duration, reason); err != nil {
		s.writeError(w, r, err)
		return
	}

	// The job as it now is, not as it was when it was claimed.
	//
	// The copy in hand was read before the finish was recorded, so it still says
	// "running": a notification about a job that has just passed, saying it is
	// running, is worse than no notification at all.
	job.Status = status
	job.DurationMS = duration.Milliseconds()

	// The phase the job was in when it stopped, said out loud before it is forgotten.
	//
	// A phase is normally closed by the next one beginning, which leaves the last phase of a job
	// with nothing to close it: the build says "build", then "push", then the job is over. Without
	// this, "push" stayed open until the page was closed — an arrow on a step of an image that
	// had been pushed and was sitting on the cluster minutes earlier.
	//
	// Only for phases this endpoint heard about. A deployment's phases come from its module,
	// which closes its own, and a job that never said a phase has nothing to close.
	if closed := s.closePhases(jobID); closed != "" {
		if project, perr := s.store.Projects().ByID(r.Context(), job.ProjectID); perr == nil {
			for _, place := range s.placesOfRun(r.Context(), project, job.PipelineID) {
				record := map[string]any{"cluster": place.place}
				if place.namespace != "" {
					record["namespace"] = place.namespace
				}
				s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployOperation,
					map[string]any{
						"job_id":     whichOperation(jobID, job.Deploy != nil, place),
						"phase":      closed,
						"message":    "finished",
						"finished":   true,
						"deployment": record,
					})
			}
		}
	}

	// The key goes with the job. A credential that outlived the build it was made
	// for would be a credential nobody is watching.
	if err := s.revokeJobKey(r, job.ProjectID, jobID); err != nil {
		s.log.Warn("remove a job's key", "job_id", jobID, "error", err)
	}

	s.publishPipeline(r.Context(), job.ProjectID, nil, models.EventJobUpdated, map[string]any{
		"job_id": jobID,
		"status": status,
	})

	// Something changed about a pipeline this project can see, so a page watching
	// one re-reads it. The event is the signal only: what the pipeline now looks
	// like is fetched again through the endpoint that already knows how.
	// Two facts, and they are not the same one. A job finishing says something about
	// that job; the run is finished only when nothing of it is left waiting, which
	// on a pipeline of three jobs is a different moment. Confusing the two is how a
	// "build passed" message goes out while two jobs are still queued.
	if job.ProjectPath != "" {
		project, err := s.store.Projects().ByPath(r.Context(), job.ProjectPath)
		if err == nil {
			pipeline, perr := s.store.Pipelines().PipelineByID(r.Context(), job.PipelineID)
			if perr != nil {
				// Without the run there is nothing to describe, and a notification
				// about a job with no pipeline is a link to nowhere.
				pipeline = nil
			}
			if pipeline != nil {
				s.publishPipeline(r.Context(), project.ID, nil, models.EventPipelineUpdated, map[string]any{
					"pipeline_id": pipeline.ID,
					"job_name":    job.Name,
					"status":      status,
					// Said separately because it is a different fact, and a page that
					// cannot tell them apart treats a build ending as a run ending — which
					// closes a card watching the rollout that follows the build, a second
					// before that rollout says anything.
					"run_finished": !s.runStillHasWork(r.Context(), pipeline.ID),
					// The job it is about, so that a page watching one run can tell this
					// line from the same line about another. Two runs are under way at
					// once more often than not — a push builds while a tag is being rolled
					// out — and a page that took every end as its own closed over a rollout
					// it was watching because a build somewhere else had finished.
					"job_id": job.ID,
				})

				jobEvent := notifyContext{
					Event:    "job.finished",
					Project:  projectContext(project),
					Pipeline: pipelineContext(pipeline, project, store.PipelineRunning),
					Job:      jobContext(job),
					Images:   imageContext(job),
				}
				s.notifyEvent(r.Context(), "job.finished", jobEvent,
					notificationSummary(jobEvent, project))

				// The deployment is waited for rather than started here. It is a job
				// of this run and it is still pending, so the check below finds it and
				// the run is not over yet — which is right: a pipeline whose deploy has
				// not happened has not finished.
				s.startDeployIfReady(r.Context(), pipeline.ID)

				if remaining, err := s.store.Pipelines().UnfinishedJobs(r.Context(), pipeline.ID); err == nil && remaining == 0 {
					final := notifyContext{
						Event:    "pipeline.finished",
						Project:  projectContext(project),
						Pipeline: pipelineContext(pipeline, project, pipelineFinishedStatus(r.Context(), s, pipeline.ID)),
					}
					s.notifyEvent(r.Context(), "pipeline.finished", final,
						notificationSummary(final, project))
				}
			}
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"job_id": jobID, "status": status})
}

// handleAppendJobLog stores a piece of a job's output.
//
// Logs go to object storage rather than to the database: they are written once,
// read occasionally, and can be the largest thing a pipeline produces. A runner
// appends; it never rewrites, so what arrived is what happened.
func (s *Server) handleAppendJobLog(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	jobID, err := strconv.ParseInt(pathParam(r, "jobID"), 10, 64)
	if err != nil {
		s.writeError(w, r, errBadRequest("a job id is required"))
		return
	}

	job, err := s.store.Pipelines().JobByID(r.Context(), jobID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such job"))
		return
	}
	if job.RunnerID == nil || *job.RunnerID != integration.ID {
		s.writeError(w, r, errForbidden("this job belongs to another runner"))
		return
	}

	var req struct {
		Text string `json:"text"`
		// Stream is "out" or "err". Kept because a build's stderr is the line the
		// reader is looking for, and a merged log makes it look like progress.
		Stream string `json:"stream"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Text == "" {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"appended": 0})
		return
	}
	if req.Stream != "err" {
		req.Stream = "out"
	}

	appended, err := s.appendJobOutput(r.Context(), job, req.Stream, req.Text)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"appended": appended})
}

// handleJobLog returns a job's output.
func (s *Server) handleJobLog(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	pipelineIID, err := strconv.Atoi(pathParam(r, "pipelineIID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a pipeline number is required"))
		return
	}
	jobIID, err := strconv.Atoi(pathParam(r, "jobIID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a job number is required"))
		return
	}

	pipeline, err := s.store.Pipelines().PipelineByIID(r.Context(), project.ID, pipelineIID)
	if err != nil {
		s.writeError(w, r, errNotFoundf("pipeline %d does not exist", pipelineIID))
		return
	}

	jobs, err := s.store.Pipelines().JobsOfPipeline(r.Context(), pipeline.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	for index := range jobs {
		if jobs[index].IID != jobIID {
			continue
		}
		job := &jobs[index]

		body, _, err := s.objects.Get(r.Context(), s.jobLogKey(job))
		if err != nil {
			// No log yet is an ordinary state: the job may have produced none, or may
			// not have started.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(""))
			return
		}
		defer body.Close()

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, body)
		return
	}

	s.writeError(w, r, errNotFoundf("job %d does not exist", jobIID))
}

// publishPipeline announces something a pipeline did.
//
// Events are for things other parts of the system need to hear about, and they
// are deliberately small: what happened, and enough to look the thing up. Anything
// more and every subscriber would have to keep up with the shape of everybody
// else's work.
// It takes a context rather than a request for the same reason minting a token does:
// a deployment publishes its result long after the request that started it is gone.
func (s *Server) publishPipeline(ctx context.Context, projectID uuid.UUID, actor *models.User,
	kind models.EventKind, payload map[string]any) {

	if s.events == nil {
		return
	}
	if actor == nil {
		actor = &models.User{Username: builderName}
	}
	var actorID *uuid.UUID
	if actor.ID != uuid.Nil {
		id := actor.ID
		actorID = &id
	}
	if err := s.events.Publish(ctx, kind, &projectID, actorID, payload); err != nil {
		s.log.Debug("publish pipeline event", "kind", kind, "error", err)
	}
}

// markStream prefixes every line with its stream.
//
// The file stays plain text and still reads sensibly in a terminal — "out" and
// "err" in front of each line — rather than becoming a format that only this
// program can interpret.
// markStream prefixes every line with which stream it came from and when it was written.
//
// The time is the core's, taken here rather than asked of the module. That is not a detail: the
// operation's start and end on the same card come from this clock, so a line stamped by another
// process's clock would sort against them across two timepieces and produce an order that is
// wrong in a way nothing can detect.
//
// And it is written into the stored line, not only onto the live event. A card read back after the
// fact and a card watched while it happened have to say the same thing about the same line, or the
// first is a log of something else.
//
// Milliseconds, for the same reason the operation list uses them: two lines in the same second are
// ordinary, and a rollout that reports three times a second is unreadable at one-second resolution.
func markStream(stream, text string) string {
	if text == "" {
		return ""
	}
	at := time.Now().UnixMilli()
	tag := fmt.Sprintf("out| %d| ", at)
	if stream == "err" {
		tag = fmt.Sprintf("err| %d| ", at)
	}

	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for index, line := range lines {
		lines[index] = tag + line
	}
	return strings.Join(lines, "\n") + "\n"
}

// cloneURL is the address a runner clones from.
//
// The runner is on the network the core is on — usually the same deployment — so
// this is the internal address, and there is nothing about it a client needs to see.
func (s *Server) cloneURL() string {
	if strings.TrimSpace(s.cfg.SSHHost) != "" && s.cfg.SSHPort != 22 {
		return fmt.Sprintf("ssh://git@%s:%d", s.cfg.SSHHost, s.cfg.SSHPort)
	}
	return fmt.Sprintf("ssh://git@%s", s.cfg.SSHHost)
}

// jobLogKey is where a job's output lives.
// appendJobOutput adds a line to a job's log and says how much was written.
//
// A runner appends through here, and so does a deployment the core carried out
// itself: a deploy is part of the run that caused it, so it lands in the same log
// the reader is already watching rather than somewhere they have to find.
//
// Appending means reading what is there: object storage has no append, and a
// runner streams in pieces rather than holding a whole build's output in memory.
// Each line carries which stream it came from, so the log can be coloured later
// without the storage format becoming something only this program reads.
func (s *Server) appendJobOutput(ctx context.Context, job *store.Job, stream, text string) (int, error) {
	if stream != "err" {
		stream = "out"
	}
	if text == "" {
		return 0, nil
	}

	key := s.jobLogKey(job)
	chunk := []byte(markStream(stream, text))
	if previous, _, err := s.objects.Get(ctx, key); err == nil {
		if existing, err := io.ReadAll(previous); err == nil {
			chunk = append(existing, chunk...)
		}
		previous.Close()
	}

	if _, err := s.objects.Put(ctx, key, strings.NewReader(string(chunk)),
		int64(len(chunk)), "text/plain"); err != nil {
		return 0, err
	}
	return len(text), nil
}

func (s *Server) jobLogKey(job *store.Job) string {
	return fmt.Sprintf("ci/%s/%d/job-%d.log", job.ProjectPath, job.PipelineID, job.IID)
}

// pipelineView is one pipeline as the interface sees it, with the state its jobs
// actually add up to.
func (s *Server) pipelineView(r *http.Request, pipeline *store.Pipeline, jobs []store.Job) map[string]any {
	view := map[string]any{
		"id":         pipeline.ID,
		"iid":        pipeline.IID,
		"project_id": pipeline.ProjectID,
		"ref":        pipeline.Ref,
		"sha":        pipeline.SHA,
		"source":     pipeline.Source,
		"status":     store.PipelineStatus(pipeline, jobs),
		"created_at": pipeline.CreatedAt,
	}
	if pipeline.StartedAt != nil {
		view["started_at"] = pipeline.StartedAt
	}
	if pipeline.FinishedAt != nil {
		view["finished_at"] = pipeline.FinishedAt
	}
	if len(jobs) > 0 {
		view["jobs"] = len(jobs)
	}

	// The commit's message and author, as they were when this run was made. A list
	// of pipelines is read precisely to answer "what was this for and who sent it",
	// and neither question is about the repository now.
	if pipeline.CommitTitle != "" {
		view["title"] = pipeline.CommitTitle
	}
	if pipeline.CommitAuthorName != "" {
		view["author_name"] = pipeline.CommitAuthorName
		view["avatar_url"] = avatarURL(pipeline.CommitAuthorEmail, pipeline.CommitAuthorName)
	}
	if ms := pipeline.DurationMs(); ms != nil {
		view["duration_ms"] = *ms
	}

	if pipeline.CreatedBy != nil {
		if who, err := s.store.Users().ByID(r.Context(), *pipeline.CreatedBy); err == nil {
			name := who.Name
			if name == "" {
				name = who.Username
			}
			view["triggered_by"] = map[string]any{
				"username":   who.Username,
				"name":       name,
				"avatar_url": avatarURL(who.Email, name),
			}
		}
	}

	if len(jobs) > 0 {
		view["stages"] = stageViews(jobs)
	}
	return view
}

// stageViews groups jobs by stage, keeping the order the stages first appear in.
//
// The order is the configuration's, not the alphabetical one: stages are a
// sequence people read as a sequence, and sorting them by name would put "test"
// before "build" often enough to be actively misleading.
func stageViews(jobs []store.Job) []map[string]any {
	var order []string
	grouped := map[string][]store.Job{}

	for _, job := range jobs {
		stage := job.Stage
		if stage == "" {
			stage = "test"
		}
		if _, seen := grouped[stage]; !seen {
			order = append(order, stage)
		}
		grouped[stage] = append(grouped[stage], job)
	}

	stages := make([]map[string]any, 0, len(order))
	for _, name := range order {
		inside := grouped[name]
		stageJobs := make([]map[string]any, 0, len(inside))
		for _, job := range inside {
			stageJobs = append(stageJobs, map[string]any{
				"iid":         job.IID,
				"name":        job.Name,
				"status":      job.Status,
				"duration_ms": job.DurationMS,
			})
		}
		stages = append(stages, map[string]any{
			"name":      name,
			"status":    store.StageStatus(inside),
			"jobs":      stageJobs,
			"job_count": len(inside),
		})
	}
	return stages
}

// commitInfo reads one commit's subject and author, or nil when it cannot be
// read. A pipeline whose message is unknown is still a pipeline, so this never
// refuses anything: it only fills in what it can.
func (s *Server) commitInfo(ctx context.Context, repoDir, sha string) *gitx.CommitInfo {
	commits, err := s.git.Log(ctx, repoDir, sha, 1, 0)
	if err != nil || len(commits) == 0 {
		return nil
	}
	return &commits[0]
}

// avatarURL is where a person's picture is looked up.
//
// Gravatar hashes the address rather than taking it, so the address itself never
// leaves the installation and there is no question of what was sent anywhere.
func avatarURL(email, name string) string {
	address := strings.ToLower(strings.TrimSpace(email))
	if address == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(address))
	return "https://secure.gravatar.com/avatar/" + hex.EncodeToString(sum[:])
}

// runnerJobView is one job as a runner sees it, which is not quite the same thing.
//
// The difference is the commit. A runner clones a repository, and a clone without a
// ref lands on whatever the repository's default branch happens to be pointing at —
// which, on a project whose releases come from a tag and whose work happens on a
// branch, is a different piece of code from the one the run was created for. The build
// then succeeds, produces an image, and is an image of the wrong commit: a run on a
// branch that built the default branch, reported as a pass.
//
// So the commit the run is about is carried on the job itself, and the runner checks
// that commit out rather than asking where the repository points.
func (s *Server) runnerJobView(r *http.Request, job *store.Job) map[string]any {
	view := jobView(r, job)

	run, err := s.store.Pipelines().PipelineByID(r.Context(), job.PipelineID)
	if err == nil {
		view["sha"] = run.SHA
		view["ref"] = run.Ref
	}
	return view
}

// jobView is one job as the interface sees it.
func jobView(r *http.Request, job *store.Job) map[string]any {
	view := map[string]any{
		"id":            job.ID,
		"pipeline_id":   job.PipelineID,
		"iid":           job.IID,
		"name":          job.Name,
		"stage":         job.Stage,
		"status":        job.Status,
		"image":         job.Image,
		"script":        job.Script,
		"allow_failure": job.AllowFailure,
		"created_at":    job.CreatedAt,
	}
	if job.ProjectPath != "" {
		view["project_path"] = job.ProjectPath
	}
	if job.ProjectID != uuid.Nil {
		view["project_id"] = job.ProjectID
	}
	// What the run is about, so a runner can name an image after it without asking.
	// Absent when the run had no variables of its own, which keeps an ordinary build
	// from carrying an empty object around.
	if len(job.Variables) > 0 {
		view["variables"] = job.Variables
	}
	if len(job.Build) > 0 {
		view["build"] = job.Build
	}
	if job.StartedAt != nil {
		view["started_at"] = job.StartedAt
		view["duration_ms"] = job.DurationMS
	}
	if job.FinishedAt != nil {
		view["finished_at"] = job.FinishedAt
	}
	// Why it failed, when it did. Sent only then: an empty reason on a job that passed
	// is a field nobody reads and everybody has to look at.
	if job.Error != "" {
		view["error"] = job.Error
	}
	return view
}
