package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

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
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	ref := strings.TrimSpace(req.Ref)
	if ref == "" {
		ref = project.DefaultBranch
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

	jobs := jobsFrom(config, ref, req.Job)
	if len(jobs) == 0 {
		s.writeError(w, r, errBadRequestf(
			"nothing to run for %q in %s", ref, pipeline.ConfigFileName))
		return
	}

	created, err := s.store.Pipelines().CreatePipeline(r.Context(), project.ID, ref, sha,
		"manual", config.VariablesAsStrings(), &user.ID, jobs)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	pipeline := created

	s.log.Info("pipeline created", "project", project.Path, "pipeline", pipeline.IID,
		"ref", ref, "by", user.Username)

	s.publishPipeline(r, project.ID, nil, models.EventPipelineCreated, map[string]any{
		"pipeline_iid": pipeline.IID,
		"ref":          ref,
		"sha":          sha,
	})

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"pipeline": pipelineView(r, pipeline, nil)})
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
func jobsFrom(config *pipeline.Config, ref, only string) []store.Job {
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
			Build:        spec.Build,
		})
	}
	return jobs
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

	pipelines, err := s.store.Pipelines().ListPipelines(r.Context(), project.ID, 20)
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
		views = append(views, pipelineView(r, pipeline, jobs))
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"pipelines": views})
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
		"pipeline": pipelineView(r, pipeline, jobs),
		"jobs":     views,
	})
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
			s.writeJSON(w, r, http.StatusOK, map[string]any{"job": nil})
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.log.Info("job claimed", "job", job.ID, "job_name", job.Name,
		"project", job.ProjectPath, "runner", integration.Kind)

	answer := map[string]any{"job": jobView(r, job)}

	// A job that builds an image needs a credential for the registry, and the
	// registry's naming rule is the registry's own. The core asks the registry
	// module what the name is and mints a token for exactly that project, so the
	// runner never holds a general-purpose account.
	if len(job.Build) > 0 {
		credentials, err := s.registryCredentialsFor(r, job)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		answer["registry"] = credentials
	}

	// Where the logs go: object storage, not a file on the machine that ran the
	// job. A runner that dies must not take the log with it.
	answer["log_key"] = s.jobLogKey(job)

	// How to get the code.
	//
	// A runner is a machine the deployment knows nothing about, so it cannot already
	// have a key on the project. The credential is minted for this one job and is
	// the only reason the runner can read anything at all.
	answer["clone_url"] = s.cfg.HTTPAddr
	token, _, err := s.mintModuleToken(r, serviceUser(), integration, &job.ProjectID,
		[]string{models.ScopeRegistryPull}, 2*time.Hour)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	answer["clone_token"] = token

	s.writeJSON(w, r, http.StatusOK, answer)
}

// registryCredentialsFor works out what a job needs to push an image.
func (s *Server) registryCredentialsFor(r *http.Request, job *store.Job) (map[string]any, error) {
	registry, err := s.store.Integrations().ByKind(r.Context(), registryKind)
	if err != nil {
		return nil, errBadRequest("no registry is installed on this instance, so nothing can be pushed")
	}
	if !registry.Enabled {
		return nil, errBadRequest("the registry module has been forbidden")
	}

	project, err := s.store.Projects().ByPath(r.Context(), job.ProjectPath)
	if err != nil {
		return nil, errBadRequestf("project %q no longer exists", job.ProjectPath)
	}

	// The credential belongs to the project, not to the person who pressed the
	// button: a job runs at three in the morning, and it runs the project's work,
	// not somebody's session.
	token, _, err := s.mintModuleToken(r, serviceUser(), registry, &project.ID,
		[]string{models.ScopeRegistryPush, models.ScopeRegistryPull}, 2*time.Hour)
	if err != nil {
		return nil, err
	}

	settings, err := s.store.Integrations().SettingsFor(r.Context(), registry.ID, nil, &project.ID)
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
		"image":        imageNameFor(template, project.Path, jobRef(job)),
		"token":        token,
		"expires_in":   int((2 * time.Hour).Seconds()),
	}, nil
}

// serviceUser is the account a machine acts as.
//
// A job has no session and no person behind it, so the credential it pushes with
// belongs to a system account rather than to whoever pressed the button. Naming
// it keeps the audit trail honest: an image pushed at three in the morning is
// attributed to the builder, not to the person who started the pipeline.
func serviceUser() *models.User {
	return &models.User{Username: "builder"}
}

// jobRef is the branch a job's image is named after, when the build says so.
func jobRef(job *store.Job) string {
	if ref, ok := job.Build["ref"].(string); ok {
		return ref
	}
	return ""
}

// handleFinishJob records what a runner did with a job.
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
	if err := s.store.Pipelines().FinishJob(r.Context(), jobID, status, duration); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.publishPipeline(r, job.ProjectID, nil, models.EventJobUpdated, map[string]any{
		"job_id": jobID,
		"status": status,
	})

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
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Text == "" {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"appended": 0})
		return
	}

	key := s.jobLogKey(job)
	// Appending means reading what is there: object storage has no append, and a
	// runner streams in pieces rather than holding a whole build's output in memory.
	chunk := []byte(req.Text)
	if previous, _, err := s.objects.Get(r.Context(), key); err == nil {
		if existing, err := io.ReadAll(previous); err == nil {
			chunk = append(existing, chunk...)
		}
		previous.Close()
	}

	if _, err := s.objects.Put(r.Context(), key, strings.NewReader(string(chunk)),
		int64(len(chunk)), "text/plain"); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"appended": len(req.Text)})
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
func (s *Server) publishPipeline(r *http.Request, projectID uuid.UUID, actor *models.User,
	kind models.EventKind, payload map[string]any) {

	if s.events == nil {
		return
	}
	if actor == nil {
		actor = serviceUser()
	}
	var actorID *uuid.UUID
	if actor.ID != uuid.Nil {
		id := actor.ID
		actorID = &id
	}
	if err := s.events.Publish(r.Context(), kind, &projectID, actorID, payload); err != nil {
		s.log.Debug("publish pipeline event", "kind", kind, "error", err)
	}
}

// jobLogKey is where a job's output lives.
func (s *Server) jobLogKey(job *store.Job) string {
	return fmt.Sprintf("ci/%s/%d/job-%d.log", job.ProjectPath, job.PipelineID, job.IID)
}

// pipelineView is one pipeline as the interface sees it, with the state its jobs
// actually add up to.
func pipelineView(r *http.Request, pipeline *store.Pipeline, jobs []store.Job) map[string]any {
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
	return view
}
