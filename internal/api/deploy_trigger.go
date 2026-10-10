package api

import (
	"context"
	"fmt"
	"strings"
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

	// The place first, and before the claim: a deployment that has to wait must not have
	// been claimed to wait, because a claimed job is one that has been started, and the
	// run would sit on a job that says it is running and is not.
	//
	// Nothing has happened yet at this point, so saying so is free and reversible. What
	// replaces this is the module refusing a second rollout into one namespace — which
	// loses a deployment somebody pushed for, over a conflict that resolved itself in a
	// minute, and which nobody re-tried because the push was already done.
	place, placed := s.deployPlaceOf(ctx, project, job)
	mayStart, holder, enqueued := true, false, false
	if placed {
		mayStart, holder, enqueued = s.deploys.takeWithStatus(place, job.ID, run.ID)
	}
	if !mayStart {
		// Readiness can be checked more than once for the same pending job. Only the call
		// that actually added it to the queue writes the waiting line and announces the
		// change to the page; repeats are not another event or another log entry.
		if enqueued {
			s.writeDeployInfo(ctx, job, fmt.Sprintf(
				"Another deployment is already under way in %s, so this one is waiting its turn "+
					"rather than being refused. It will be deployed when that one is finished, "+
					"unless a newer deployment of this project arrives first — in which case this "+
					"one will be superseded and will not be deployed at all.\n", place.cluster))
			s.publishPipeline(ctx, project.ID, nil, models.EventDeployQueued, map[string]any{
				"job_id": job.ID,
				"place":  place.cluster,
			})
			s.log.Info("the deployment is waiting for its place",
				"job_id", job.ID, "project", project.Path,
				"place", place.cluster, "namespace", place.namespace)
		}
		return
	}

	// Claimed before any work is done, so two calls that arrive together cannot both
	// start one. The store does the claiming, because that is where the state lives:
	// reading the row and then writing it would be a race with anything else asking
	// at the same moment.
	claimed, err := s.store.Pipelines().ClaimDeployJob(ctx, job.ID)
	if !claimed {
		if err != nil {
			s.log.Error("claim the deployment", "job_id", job.ID, "error", err)
		}
		if holder {
			s.releasePlaceAfterLostClaim(context.WithoutCancel(ctx), place, job.ID)
		}
		return
	}
	// ClaimDeployJob can return an error after it has already marked the job running
	// (for example, if updating the pipeline's summary timestamp then fails). The claim
	// is durable at that point, so this caller must still start the worker rather than
	// leave a running row and a slot that nobody will release.

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

// releasePlaceAfterLostClaim checks the durable job state before giving a place back after
// a claim did not succeed.
//
// Several completion callbacks can reach the same pending deploy together. The first call
// takes the in-memory place, but another call for that same job is also allowed to try the
// durable claim. If that second call wins, the first call's claim returns false. Freeing the
// place just because this call was the original holder would let a different deployment
// start while the winning caller is already deploying.
//
// A running row means the successful claimant owns this place until carryOutDeploy releases
// it. A terminal row means there is no deployment left to hold it. If the row cannot be
// read, leave the place held: allowing overlapping rollouts is worse than requiring recovery
// from a place that could not safely be released.
func (s *Server) releasePlaceAfterLostClaim(ctx context.Context, place deployPlace, jobID int64) {
	job, err := s.store.Pipelines().JobByID(ctx, jobID)
	if err != nil {
		s.log.Error("check the deployment after an unsuccessful claim",
			"job_id", jobID, "error", err)
		return
	}
	if job.Status == store.JobRunning {
		return
	}
	s.releaseDeployPlace(ctx, place, jobID)
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

	// The place is released on every path out of this function, including the ones that
	// ended without touching a cluster — including a deployment that could not even be
	// described. A place released only on success is a place a single failure closes for
	// as long as this process lives, and the next push to it is refused for a reason
	// nobody can see. Deferred rather than put in `finish`, because `finish` is not
	// reached by every way out of here.
	if place, placed := s.deployPlaceOf(ctx, project, job); placed {
		defer s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
	}

	finish := func(status string, reason string) {
		elapsed := time.Since(started)
		if err := s.store.Pipelines().FinishJob(ctx, job.ID, status, elapsed, reason); err != nil {
			s.log.Error("finish the deployment", "job_id", job.ID, "error", err)
			return
		}
		s.reportDeployFinished(ctx, job, run, project, status, reason)

		// Then ask whether this run has another deployment waiting.
		//
		// A deployment does not end through the path a runner's job ends through — the
		// core carries it out itself — so nothing was looking for the next one, and a
		// configuration with two places in it deployed the first and left the second
		// pending for ever: a run that says it is still going somewhere it has already
		// been.
		s.startDeployIfReady(context.WithoutCancel(ctx), run.ID)
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

	// A place this project has switched off is not a failure. It is a place this project
	// may not deploy to, and the job is recorded as skipped so the run carries on.
	//
	// The module refuses such a deployment, correctly, and in words worth reading. Letting
	// that refusal stand would fail the run over a switch somebody flipped on purpose: a
	// project with two places and one of them off would deploy to neither, because the
	// first failure skips the rest of the run. The switch is the core's own knowledge — it
	// is this project's row of the places list — so the core reads it here rather than
	// recognising a refusal after the fact.
	if spec := deploySpecForJob(job, config); spec.Present &&
		!s.placeInUse(ctx, project, spec.Module, spec.Target) {
		reason := fmt.Sprintf("%s is switched off for this project", spec.Target)
		s.writeDeployInfo(ctx, job, reason+", so nothing was deployed here.\n")
		finish(store.JobSkipped, reason)
		return
	}

	repoDir := s.repos.PathFor(project)
	err = s.runDeployJob(ctx, job, run, project, repoDir, config)
	// The module declined. Recorded as a refusal and not as a failure, for the same reason the
	// switched-off place above is recorded as skipped: the run goes on to the next place, and
	// nothing is red in the cluster. A run that failed here would take every place after it
	// down with it, over a place that was merely busy.
	if reason, wasRefused := refusedBy(err); wasRefused {
		finish(store.JobRefused, reason)
		return
	}
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

// deployPlaceOf is where this deployment is going, as the thing the queue holds a place
// for.
//
// The same three the module holds: the project, the place, and the namespace inside it.
// Read here rather than carried in the job because the namespace is not written on the job
// — it lives in the module's list of places and can change between one run and the next —
// and a queue keyed on yesterday's namespace would be keyed on nothing.
//
// False means there is no place to speak of: a job that names none, a project whose
// module is gone, a place whose row cannot be read. Those deploy as they always did,
// unqueued, because refusing to deploy anything the core cannot place would be a worse
// failure than two deployments at once — and the module's own check still stands behind it.
func (s *Server) deployPlaceOf(ctx context.Context, project *models.Project,
	job *store.Job) (deployPlace, bool) {

	if project == nil || job == nil || job.Deploy == nil {
		return deployPlace{}, false
	}
	place := strings.TrimSpace(asString(job.Deploy["Target"]))
	if place == "" {
		return deployPlace{}, false
	}

	module, err := s.deployModuleForPlace(ctx, project, place,
		strings.TrimSpace(asString(job.Deploy["Module"])))
	if err != nil || module == nil {
		return deployPlace{}, false
	}

	return deployPlace{
		project:   project.Path,
		cluster:   place,
		namespace: placeNamespaceIn(s.placeRows(ctx, project, module), place),
	}, true
}

// releaseDeployPlace gives a place back, and deals with whoever was behind it.
//
// Called once, at the end of the deployment, on every path — including the ones that ended
// without touching a cluster. A place has to be released whether the deployment worked or
// not, or a single failure would close the place for as long as the process lives, which
// is the whole failure this replaces.
//
// The place is given back before anything is started from it. The promoted deployment does
// not ask again — it is already holding the place — so the gap between giving it back and
// it actually starting has to stay closed, and starting it from here rather than from the
// next caller is what closes it.
//
// A superseded deployment is not a failure and does not fail its run: nothing broke,
// nothing was attempted, and the code that reaches the cluster is the newest one rather
// than this one. Its own status because everything that reads a job's outcome means
// something different for it, and it is still recorded — a deployment that did not happen
// is a fact somebody will want to read later.
func (s *Server) releaseDeployPlace(ctx context.Context, place deployPlace, jobID int64) {
	promoted, superseded := s.deploys.free(place, jobID)
	for _, one := range superseded {
		s.supersedeDeploy(ctx, one, place)
	}
	if promoted != nil {
		s.startQueuedDeploy(ctx, promoted, place)
	}
}

// supersedeDeploy records a deployment that was waiting and will not run.
func (s *Server) supersedeDeploy(ctx context.Context, one queuedDeploy, place deployPlace) {
	job, err := s.store.Pipelines().JobByID(ctx, one.jobID)
	if err != nil {
		s.log.Error("read the superseded deployment", "job_id", one.jobID, "error", err)
		return
	}
	// One that has already stopped waiting — cancelled, say — has nothing to supersede.
	// It was never in the race, and marking a cancelled run as superseded would say it
	// lost one. Its place in the queue is gone either way: this is the only list of
	// waiters, and it is being walked.
	if job.Status != store.JobPending {
		return
	}

	reason := fmt.Sprintf("a newer deployment of this project was waiting for %s, so this one was not deployed",
		place.cluster)
	s.writeDeployInfo(ctx, job, "Waiting for "+place.cluster+" was overtaken: "+reason+
		". Nothing was deployed and nothing broke — the newest deployment of this project is the one "+
		"that reached the cluster.\n")

	if err := s.store.Pipelines().FinishJob(ctx, job.ID, store.JobSuperseded, 0, reason); err != nil {
		s.log.Error("finish the superseded deployment", "job_id", job.ID, "error", err)
		return
	}

	run, err := s.store.Pipelines().PipelineByID(ctx, one.pipelineID)
	if err != nil {
		s.log.Error("read the run of the superseded deployment", "job_id", job.ID, "error", err)
		return
	}
	project, err := s.store.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		s.log.Error("read the project of the superseded deployment", "job_id", job.ID, "error", err)
		return
	}
	s.reportDeployFinished(ctx, job, run, project, store.JobSuperseded, reason)

	// The run may have another place after this one, and a run that stops here is a run
	// that says it went somewhere it did not go.
	s.startDeployIfReady(context.WithoutCancel(ctx), run.ID)
}

// startQueuedDeploy begins the deployment that was promoted, which already holds its place.
func (s *Server) startQueuedDeploy(ctx context.Context, one *queuedDeploy, place deployPlace) {
	placeCtx := context.WithoutCancel(ctx)
	retryPending := true
	defer func() {
		if !retryPending {
			return
		}
		// The promoted job owns the slot already. If loading its description fails,
		// return the slot and let the pending job take another turn rather than blocking
		// every deployment behind a transient read error.
		s.releaseDeployPlace(placeCtx, place, one.jobID)
		s.startDeployIfReady(placeCtx, one.pipelineID)
	}()

	run, err := s.store.Pipelines().PipelineByID(ctx, one.pipelineID)
	if err != nil {
		s.log.Error("read the run to deploy", "job_id", one.jobID, "error", err)
		return
	}
	job, err := s.store.Pipelines().JobByID(ctx, one.jobID)
	if err != nil {
		s.log.Error("read the deployment waiting to run", "job_id", one.jobID, "error", err)
		return
	}
	if job.Status != store.JobPending {
		// It stopped waiting while the place was held — cancelled, most likely.
		// It never started, so hand the slot on without retrying this terminal job.
		retryPending = false
		s.log.Info("a deployment stopped waiting before its turn came",
			"job_id", one.jobID, "status", job.Status)
		s.releaseDeployPlace(placeCtx, place, one.jobID)
		return
	}
	project, err := s.store.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		s.log.Error("read the project to deploy", "project_id", run.ProjectID, "error", err)
		return
	}

	claimed, err := s.store.Pipelines().ClaimDeployJob(ctx, job.ID)
	if !claimed {
		if err != nil {
			s.log.Error("claim the queued deployment", "job_id", job.ID, "error", err)
		}
		retryPending = false
		s.releasePlaceAfterLostClaim(placeCtx, place, job.ID)
		if current, readErr := s.store.Pipelines().JobByID(placeCtx, job.ID); readErr == nil &&
			current.Status == store.JobPending {
			s.startDeployIfReady(placeCtx, one.pipelineID)
		}
		return
	}
	// As with the direct path, a claim can succeed even if its follow-up pipeline
	// timestamp update failed. The job row already says running; it still needs a worker.
	if err != nil {
		s.log.Error("claimed the queued deployment but could not update its pipeline",
			"job_id", job.ID, "error", err)
	}
	retryPending = false

	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployTimeoutLimit)
	go func() {
		defer cancel()
		s.carryOutDeploy(detached, job, run, project)
	}()
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
			// The first deployment of this run that has not been started yet.
			//
			// It used to be the first deployment of the run, full stop, which meant a
			// configuration with two places in it deployed the first one and left the
			// second pending for ever: the check found the one that had already run, saw
			// that it was not waiting for anything, and stopped there. They are taken in
			// the order the configuration lists them and one at a time — the module
			// refuses two rollouts into one namespace at the same moment on purpose, and
			// there is no reason to arrange that deliberately across two.
			if deploy == nil && job.Status == store.JobPending {
				deploy = &jobs[index]
			}
			continue
		}

		switch job.Status {
		case store.JobPending, store.JobRunning:
			unfinished++
		case store.JobRefused, store.JobSkipped, store.JobSuperseded:
			// Neither blocks anything. A place that was busy, or switched off, is a
			// deployment that did not happen, and the places after it in the same run are
			// not waiting on it. Counting it as a blocker would fail the run over a
			// cluster that is fine.
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

// placeRecordOf is which place a job is about, as an event about that place has to name
// itself — the place's name and the namespace it deploys into, and nothing else.
//
// Nil when the job names no place, which is every job that is not a deployment. That is
// not a shrug: an event with no place in it belongs to the project, and the pages that
// draw per place decide for themselves what to do with one.
func (s *Server) placeRecordOf(ctx context.Context, project *models.Project,
	job *store.Job) map[string]any {

	if job == nil || job.Deploy == nil {
		return nil
	}
	place := strings.TrimSpace(asString(job.Deploy["Target"]))
	if place == "" {
		return nil
	}
	record := map[string]any{"cluster": place}

	module, err := s.deployModuleForPlace(ctx, project, place,
		strings.TrimSpace(asString(job.Deploy["Module"])))
	if err != nil || module == nil {
		return record
	}
	if namespace := placeNamespaceIn(s.placeRows(ctx, project, module), place); namespace != "" {
		record["namespace"] = namespace
	}
	return record
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
		// A deployment is a job like any other, so this line is the same one the build
		// sends and says no more about the run — and a run with two places in it has one
		// deployment still to come. Said apart, so a page watching the second place is
		// not told by the first place's ending that nothing is left.
		"run_finished": !s.runStillHasWork(ctx, run.ID),
		"job_id":       job.ID,
	})

	// The history changed — once, now, and not again until the next deployment. Its own
	// event so that a page watching the list of what has been deployed does not also
	// redraw it for every pod that comes up.
	history := map[string]any{
		"job_id":  job.ID,
		"status":  status,
		"project": project.Path,
	}
	// Which place, because "a deployment ended" is not one event but one per place, and
	// a project has as many of them as it has places.
	//
	// Said apart because a place that is switched off ends in a millisecond: a run with
	// two places was over for the one nobody may deploy to before the other had read a
	// single manifest, and a page that took every ending as its own closed the card of
	// the place still rolling out and took its log with it.
	if record := s.placeRecordOf(ctx, project, job); record != nil {
		history["deployment"] = record
	}
	s.publishPipeline(ctx, project.ID, nil, models.EventDeployHistory, history)

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
	s.writeDeployOutput(ctx, job, "err", line)
}

// writeDeployInfo records a non-failure fact, such as waiting or being skipped. It shares the
// job's log with errors but keeps the ordinary output colour: nothing broke in those cases.
func (s *Server) writeDeployInfo(ctx context.Context, job *store.Job, line string) {
	s.writeDeployOutput(ctx, job, "out", line)
}

func (s *Server) writeDeployOutput(ctx context.Context, job *store.Job, stream, line string) {
	if _, err := s.appendJobOutput(ctx, job, stream, line); err != nil {
		s.log.Error("write to the deployment log", "job_id", job.ID, "error", err)
	}
}
