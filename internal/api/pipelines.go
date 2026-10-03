package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/gitx"
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

	// Who wrote the change and what it says. Read once here so the run remembers it:
	// later the branch will have moved, and the list has to keep describing the
	// commit it was actually made against.
	commit := store.Commit{}
	if head := s.commitInfo(r.Context(), rc.RepoDir, sha); head != nil {
		commit = store.Commit{
			Title:       head.Subject,
			AuthorName:  head.AuthorName,
			AuthorEmail: head.AuthorEmail,
		}
	}

	created, err := s.store.Pipelines().CreatePipeline(r.Context(), project.ID, ref, sha,
		"manual", config.VariablesAsStrings(), &user.ID, commit, jobs)
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
	s.notify(r, project.Path, "pipeline.started", fmt.Sprintf(
		"Pipeline #%d started on %s in %s", pipeline.IID, ref, project.Path),
		fmt.Sprintf("/p/%s/-/pipelines/%d", project.Path, pipeline.IID), nil)

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"pipeline": s.pipelineView(r, pipeline, nil)})
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
		views = append(views, s.pipelineView(r, pipeline, jobs))
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
		"pipeline": s.pipelineView(r, pipeline, jobs),
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

	// How to get the code. The key itself is not issued here: a runner may sit on a
	// job for as long as it likes before it starts, and a key whose clock ran from
	// the moment of claiming would be dead before the machine was ready. The runner
	// asks for one when it is actually about to clone, which is the moment the key
	// is needed and the moment its life should start.
	answer["clone_url"] = s.cloneURL()

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

	// The key goes with the job. A credential that outlived the build it was made
	// for would be a credential nobody is watching.
	if err := s.revokeJobKey(r, job.ProjectID, jobID); err != nil {
		s.log.Warn("remove a job's key", "job_id", jobID, "error", err)
	}

	s.publishPipeline(r, job.ProjectID, nil, models.EventJobUpdated, map[string]any{
		"job_id": jobID,
		"status": status,
	})

	if job.ProjectPath != "" {
		emoji := "✅"
		switch status {
		case store.JobFailed:
			emoji = "❌"
		case store.JobCanceled:
			emoji = "🚫"
		}
		s.notify(r, job.ProjectPath, "pipeline."+status, fmt.Sprintf(
			"%s Job %q %s in %s", emoji, job.Name, status, job.ProjectPath),
			fmt.Sprintf("/p/%s/-/pipelines/%d", job.ProjectPath, job.PipelineID), nil)
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

	key := s.jobLogKey(job)
	// Appending means reading what is there: object storage has no append, and a
	// runner streams in pieces rather than holding a whole build's output in memory.
	// Each line carries which stream it came from, so the log can be coloured
	// later without the storage format becoming something only this program reads.
	chunk := []byte(markStream(req.Stream, req.Text))
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

// markStream prefixes every line with its stream.
//
// The file stays plain text and still reads sensibly in a terminal — "out" and
// "err" in front of each line — rather than becoming a format that only this
// program can interpret.
func markStream(stream, text string) string {
	if text == "" {
		return ""
	}
	tag := "out| "
	if stream == "err" {
		tag = "err| "
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
