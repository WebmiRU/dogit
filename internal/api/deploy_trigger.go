package api

import (
	"context"
	"fmt"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
)

// When a deployment runs.
//
// The rule is short: a deployment happens when everything else in the run has passed,
// and never otherwise. It waits for the build rather than racing it — deploying an
// image whose tests are still running is the failure everybody discovers later, on a
// cluster, rather than in a pipeline — and it does not happen at all when something
// failed, because a deploy after a red build is a way of shipping a broken commit
// that looks like a deliberate act.
//
// It runs in the background. The runner's request is not waiting on a cluster, and
// holding a connection open for a rollout would time out long before the interesting
// part happened.

// startDeployIfReady runs a pipeline's deployment when the run has earned one.
//
// Called after every job finishes. It does nothing at all unless the run has a
// deployment waiting and nothing else is unfinished, so the cost of being called from
// the hot path of every job is one indexed read.
func (s *Server) startDeployIfReady(ctx context.Context, pipelineID int64) {
	job, ready, err := s.pendingDeploy(ctx, pipelineID)
	if err != nil {
		s.log.Error("read the deployment waiting to run", "pipeline_id", pipelineID, "error", err)
		return
	}
	if !ready {
		return
	}

	run, err := s.store.Pipelines().PipelineByID(ctx, pipelineID)
	if err != nil {
		s.log.Error("read the pipeline to deploy", "pipeline_id", pipelineID, "error", err)
		return
	}
	project, err := s.store.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		s.log.Error("read the project to deploy", "project_id", run.ProjectID, "error", err)
		return
	}

	// Claimed before any work is done, so two calls that arrive together cannot both
	// start one. The store does the claiming, because that is where the state lives:
	// reading the row and then writing it would be a race with anything else asking
	// at the same moment.
	claimed, err := s.store.Pipelines().ClaimDeployJob(ctx, job.ID)
	if err != nil {
		s.log.Error("claim the deployment", "job_id", job.ID, "error", err)
		return
	}
	if !claimed {
		return
	}

	// Not derived from the caller's request: that request is about a runner reporting
	// a test result, and it is answered already. A deploy outlives it.
	//
	// The deadline belongs to the goroutine rather than to this function, and that is
	// not a detail. A `defer cancel()` here would fire the moment this function
	// returned — which is immediately, since the goroutine has only just started — and
	// cancel a deployment that had not yet read a single manifest. It fails in the
	// least legible way available: the job is left "running" with an error about a
	// cancelled context and nothing in a cluster.
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployTimeoutLimit)

	go func() {
		defer cancel()
		s.carryOutDeploy(detached, job, run, project)
	}()
}

// deployTimeoutLimit is how long a deployment may take before it is abandoned.
//
// An hour, which is longer than any rollout and longer than any migration that would
// be run as part of one. It exists so a deployment that hangs — a cluster that accepts
// a connection and then never answers — ends as a failed job rather than as a job
// that stays "running" for ever and blocks the next one to the same place.
const deployTimeoutLimit = time.Hour

// carryOutDeploy is the deployment itself, off the request that led to it.
func (s *Server) carryOutDeploy(ctx context.Context, job *store.Job, run *store.Pipeline,
	project *models.Project) {

	started := time.Now()

	finish := func(status string, reason string) {
		elapsed := time.Since(started)
		if err := s.store.Pipelines().FinishJob(ctx, job.ID, status, elapsed, reason); err != nil {
			s.log.Error("finish the deployment", "job_id", job.ID, "error", err)
			return
		}
		s.reportDeployFinished(ctx, job, run, project, status, reason)
	}

	// The configuration is read from the commit being run, once more, rather than
	// kept from when the pipeline was created. The job already carries everything the
	// module needs, and re-reading would be a second answer to "what was this
	// deployment" that could disagree with the first.
	config, err := s.deploySpecFor(ctx, project, run)
	if err != nil {
		s.writeDeployLog(ctx, job, "This deployment cannot be carried out: "+err.Error()+"\n")
		finish(store.JobFailed, err.Error())
		return
	}

	repoDir := s.repos.PathFor(project)
	err = s.runDeployJob(ctx, job, run, project, repoDir, config)
	switch {
	case err == nil:
		finish(store.JobSuccess, "")
	case job.AllowFailure:
		// Allowed to fail means allowed to fail: the run continues and the pipeline
		// page says this job did not pass, which is the whole of what
		// allow_failure means anywhere else in this file.
		s.log.Warn("the deployment failed and was allowed to", "job_id", job.ID, "error", err)
		finish(store.JobFailed, err.Error())
	default:
		s.log.Error("the deployment failed", "job_id", job.ID, "error", err)
		finish(store.JobFailed, err.Error())
	}
}

// pendingDeploy is the deployment waiting to run, and whether the run has earned it.
func (s *Server) pendingDeploy(ctx context.Context, pipelineID int64) (*store.Job, bool, error) {
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, pipelineID)
	if err != nil {
		return nil, false, err
	}

	var deploy *store.Job
	unfinished, failed := 0, false
	for index := range jobs {
		job := jobs[index]
		if job.Deploy != nil {
			if deploy == nil {
				deploy = &jobs[index]
			}
			continue
		}

		switch job.Status {
		case store.JobPending, store.JobRunning:
			unfinished++
		case store.JobFailed:
			// A failed job that was allowed to fail is a job that went on. Counting
			// it as a blocker would make allow_failure mean nothing at all, and it
			// would mean the same word has two meanings on one page.
			if !job.AllowFailure {
				failed = true
			}
		}
	}

	if deploy == nil || deploy.Status != store.JobPending {
		return nil, false, nil
	}
	if unfinished > 0 || failed {
		return nil, false, nil
	}

	return deploy, true, nil
}

// deploySpecFor is the deployment this run described, as a configuration again.
//
// A job created hours ago has to be able to say what it was asked to do without
// anybody having kept that in memory, so the repository is read once more at the
// commit. It is the same commit the run is on, so it cannot have changed underneath.
func (s *Server) deploySpecFor(ctx context.Context, project *models.Project,
	run *store.Pipeline) (*pipeline.Config, error) {

	config, err := s.pipelineConfig(ctx, s.repos.PathFor(project), run.SHA)
	if err != nil {
		return nil, err
	}
	if len(config.Deploys) == 0 {
		return nil, fmt.Errorf(
			"this commit has no deploy block, so there is nothing to deploy")
	}
	return config, nil
}

// reportDeployFinished tells the rest of the system a deployment is done.
//
// Same events as any other job, because a deployment is any other job: the pipeline
// page updates, and a project configured to announce finished pipelines says so.
func (s *Server) reportDeployFinished(ctx context.Context, job *store.Job, run *store.Pipeline,
	project *models.Project, status, reason string) {

	job.Status = status

	s.publishPipeline(ctx, project.ID, nil, models.EventJobUpdated, map[string]any{
		"job_id": job.ID,
		"status": status,
	})
	s.publishPipeline(ctx, project.ID, nil, models.EventPipelineUpdated, map[string]any{
		"pipeline_id": run.ID,
		"job_name":    job.Name,
		"status":      status,
	})

	// The history changed — once, now, and not again until the next deployment. Its own
	// event so that a page watching the list of what has been deployed does not also
	// redraw it for every pod that comes up.
	s.publishPipeline(ctx, project.ID, nil, models.EventDeployHistory, map[string]any{
		"job_id":  job.ID,
		"status":  status,
		"project": project.Path,
	})

	remaining, err := s.store.Pipelines().UnfinishedJobs(ctx, run.ID)
	if err != nil || remaining != 0 {
		return
	}

	final := notifyContext{
		Event:    "pipeline.finished",
		Project:  projectContext(project),
		Pipeline: pipelineContext(run, project, pipelineFinishedStatus(ctx, s, run.ID)),
	}
	s.notifyEvent(ctx, "pipeline.finished", final, notificationSummary(final, project))
}

func (s *Server) writeDeployLog(ctx context.Context, job *store.Job, line string) {
	if _, err := s.appendJobOutput(ctx, job, "err", line); err != nil {
		s.log.Error("write to the deployment log", "job_id", job.ID, "error", err)
	}
}
