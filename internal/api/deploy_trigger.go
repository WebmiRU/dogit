package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
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

	place, placed, placeErr := s.deployPlaceOf(ctx, project, job)
	if placeErr != nil {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot safely resolve the deployment destination: "+placeErr.Error())
		return
	}
	if !placed || place.identity == "" || place.namespace == "" {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot safely identify the physical cluster and namespace; refusing to deploy without an exclusive lock")
		return
	}

	latest, err := s.store.Pipelines().IsLatestDeployCandidate(ctx, place.candidateKeys(), run.ID)
	if err != nil {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot verify this deployment is still the latest candidate: "+err.Error())
		return
	}
	if !latest {
		s.supersedeDeploy(ctx, queuedDeploy{jobID: job.ID, pipelineID: run.ID}, place)
		return
	}

	mayStart, holder, enqueued := s.deploys.takeWithStatus(place, job.ID, run.ID)
	if !mayStart {
		if enqueued {
			s.announceDeployWaiting(ctx, job, project, run, place)
		}
		return
	}
	// Repeated callbacks can see the same job holding the local slot. Only the call
	// that took it is allowed to start a worker.
	if !holder {
		return
	}
	s.launchDeployWorker(ctx, job, run, project, place)
}

// announceDeployWaiting records a queue state once, with both durable UI state and an event.
func (s *Server) announceDeployWaiting(ctx context.Context, job *store.Job,
	project *models.Project, run *store.Pipeline, place deployPlace) {
	if err := s.store.Pipelines().SetDeployCandidateWaiting(ctx, place.candidateKeys(),
		job.ID, run.ID, true); err != nil {
		s.log.Error("record deployment waiting state", "job_id", job.ID, "error", err)
	}
	s.writeDeployInfo(ctx, job, fmt.Sprintf(
		"Another deployment is already under way in %s, so this one is waiting its turn. "+
			"It will start only if it is still the latest candidate when the place becomes available.\n",
		place.cluster))
	s.publishPipeline(ctx, project.ID, nil, models.EventDeployQueued, map[string]any{
		"job_id": job.ID,
		"place":  place.cluster,
	})
	s.log.Info("the deployment is waiting for its place",
		"job_id", job.ID, "pipeline_id", run.ID, "project", project.Path,
		"place", place.cluster, "namespace", place.namespace, "target_key", place.identity)
}

// failUnstartableDeploy finishes a pending deployment without asking a module or touching
// a cluster. A failure to establish the lock identity is never permission to deploy unlocked.
func (s *Server) failUnstartableDeploy(ctx context.Context, job *store.Job,
	run *store.Pipeline, project *models.Project, place deployPlace, reason string) {
	keys := place.candidateKeys()
	if len(keys) > 0 {
		latest, err := s.store.Pipelines().IsLatestDeployCandidate(ctx, keys, run.ID)
		switch {
		case err == nil && !latest:
			s.supersedeDeploy(ctx, queuedDeploy{jobID: job.ID, pipelineID: run.ID}, place)
			return
		case err != nil:
			reason += "; candidate freshness could not be verified: " + err.Error()
		}
	}

	current, err := s.store.Pipelines().JobByID(ctx, job.ID)
	if err != nil {
		s.log.Error("read the deployment that could not be started", "job_id", job.ID, "error", err)
		return
	}
	if current.Status != store.JobPending {
		return
	}
	claimed, claimErr := s.store.Pipelines().ClaimDeployJob(ctx, job.ID)
	if !claimed {
		if claimErr != nil {
			s.log.Error("claim the unstartable deployment", "job_id", job.ID, "error", claimErr)
		}
		return
	}
	if claimErr != nil {
		s.log.Error("claimed the unstartable deployment but its pipeline timestamp failed",
			"job_id", job.ID, "error", claimErr)
	}
	s.writeDeployLog(ctx, job, reason+"\n")
	if err := s.store.Pipelines().FinishJob(ctx, job.ID, store.JobFailed, 0, reason); err != nil {
		s.log.Error("finish the unstartable deployment", "job_id", job.ID, "error", err)
		return
	}
	job.Status = store.JobFailed
	s.reportDeployFinished(ctx, job, run, project, store.JobFailed, reason)
	s.startDeployIfReady(context.WithoutCancel(ctx), run.ID)
}

// launchDeployWorker keeps the exclusive local place while it waits for the shared database lock
// and until the rollout finishes. Build, tests and image push remain parallel.
func (s *Server) launchDeployWorker(ctx context.Context, job *store.Job,
	run *store.Pipeline, project *models.Project, place deployPlace) {
	detached, cancel := context.WithTimeout(context.WithoutCancel(ctx), deployTimeoutLimit)
	go func() {
		defer cancel()
		s.waitForDeployPlace(detached, job, run, project, place)
	}()
}

// waitForDeployPlace waits for the PostgreSQL advisory lock, checking the candidate before and
// atomically with the durable claim. This prevents an older build or a second dogit replica from
// changing the same physical namespace, even if it was not in this process's local queue.
func (s *Server) waitForDeployPlace(ctx context.Context, job *store.Job,
	run *store.Pipeline, project *models.Project, place deployPlace) {
	keys := place.candidateKeys()
	waitingAnnounced := false
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		current, err := s.store.Pipelines().JobByID(ctx, job.ID)
		if err != nil {
			s.log.Error("read deployment while waiting for its lock", "job_id", job.ID, "error", err)
		} else if current.Status != store.JobPending {
			_ = s.store.Pipelines().SetDeployCandidateWaiting(context.WithoutCancel(ctx), keys,
				job.ID, run.ID, false)
			s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
			return
		}

		latest, latestErr := s.store.Pipelines().IsLatestDeployCandidate(ctx, keys, run.ID)
		if latestErr == nil && !latest {
			s.supersedeDeploy(context.WithoutCancel(ctx),
				queuedDeploy{jobID: job.ID, pipelineID: run.ID}, place)
			_ = s.store.Pipelines().SetDeployCandidateWaiting(context.WithoutCancel(ctx), keys,
				job.ID, run.ID, false)
			s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
			return
		}
		if latestErr != nil && errors.Is(latestErr, store.ErrNotFound) {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			s.failUnstartableDeploy(cleanup, job, run, project, place,
				"The shared deployment candidate record is missing; refusing to deploy without coordination")
			cancel()
			s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
			return
		}
		if latestErr != nil {
			s.log.Error("verify deployment candidate while waiting for the shared lock",
				"job_id", job.ID, "error", latestErr)
		} else if latest {
			conn, acquired, lockErr := s.tryDeployLock(ctx, place.identity)
			if lockErr != nil {
				s.log.Error("acquire the shared deployment lock", "job_id", job.ID,
					"target", place.identity, "error", lockErr)
			} else if acquired {
				claimed, stillLatest, claimErr := s.store.Pipelines().ClaimDeployCandidate(
					ctx, keys, job.ID, run.ID)
				if claimErr != nil {
					s.releaseDeployLock(context.WithoutCancel(ctx), conn, place.identity)
					s.log.Error("claim the latest deployment candidate", "job_id", job.ID, "error", claimErr)
				} else if !stillLatest {
					s.releaseDeployLock(context.WithoutCancel(ctx), conn, place.identity)
					s.supersedeDeploy(context.WithoutCancel(ctx),
						queuedDeploy{jobID: job.ID, pipelineID: run.ID}, place)
					s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
					return
				} else if !claimed {
					s.releaseDeployLock(context.WithoutCancel(ctx), conn, place.identity)
					now, readErr := s.store.Pipelines().JobByID(context.WithoutCancel(ctx), job.ID)
					if readErr == nil && now.Status != store.JobPending {
						_ = s.store.Pipelines().SetDeployCandidateWaiting(
							context.WithoutCancel(ctx), keys, job.ID, run.ID, false)
						s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
						return
					}
				} else {
					_ = s.store.Pipelines().SetDeployCandidateWaiting(
						context.WithoutCancel(ctx), keys, job.ID, run.ID, false)
					s.carryOutDeploy(ctx, job, run, project, place, conn)
					return
				}
			}

			if !waitingAnnounced {
				s.announceDeployWaiting(ctx, job, project, run, place)
				waitingAnnounced = true
			}
		}

		select {
		case <-ctx.Done():
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			s.failUnstartableDeploy(cleanup, job, run, project, place,
				"Timed out waiting to verify or acquire the exclusive deployment lock; nothing was deployed")
			_ = s.store.Pipelines().SetDeployCandidateWaiting(cleanup, keys, job.ID, run.ID, false)
			cancel()
			s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
			return
		case <-ticker.C:
		}
	}
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
	project *models.Project, place deployPlace, lockConn *pgxpool.Conn) {

	started := time.Now()

	// Unlock the shared PostgreSQL session before passing the in-process slot to the next
	// waiter. Both are released on every exit path, including a failed module call.
	defer s.releaseDeployPlace(context.WithoutCancel(ctx), place, job.ID)
	defer s.releaseDeployLock(context.WithoutCancel(ctx), lockConn, place.identity)

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

// deployPlaceOf describes a target and refuses to pretend it is safe when its identity cannot
// be resolved. A deploy job with a broken or missing target still returns a logical candidate
// key, so it can invalidate an older build for that target; the caller must handle the error
// and must never send the job to a module without a physical lock identity.
func (s *Server) deployPlaceOf(ctx context.Context, project *models.Project,
	job *store.Job) (deployPlace, bool, error) {

	if job == nil || job.Deploy == nil {
		return deployPlace{}, false, nil
	}
	target := strings.TrimSpace(asString(job.Deploy["Target"]))
	moduleName := strings.TrimSpace(asString(job.Deploy["Module"]))
	place := deployPlace{cluster: target}
	if project != nil {
		place.project = project.Path
		place.logicalKey = logicalDeployCandidateKey(project, moduleName, target)
	}
	if target == "" {
		return place, true, fmt.Errorf("the deploy job has no target")
	}
	if moduleName == "" {
		return place, true, fmt.Errorf("the deploy job has no module")
	}
	if project == nil {
		return place, true, fmt.Errorf("the project for the deploy job could not be resolved")
	}

	module, err := s.deployModuleForPlace(ctx, project, target, moduleName)
	if err != nil {
		return place, true, fmt.Errorf("resolve module %q for target %q: %w", moduleName, target, err)
	}
	if module == nil {
		return place, true, fmt.Errorf("module %q for target %q is unavailable", moduleName, target)
	}

	rows := s.placeRows(ctx, project, module)
	namespace := placeNamespaceIn(rows, target)
	if namespace == "" {
		return place, true, fmt.Errorf("target %q has no explicit default_namespace; refusing to guess the namespace for its lock", target)
	}
	identity, err := physicalDeployIdentity(rows, target, namespace)
	if err != nil {
		return place, true, err
	}
	place.namespace = namespace
	place.identity = identity
	return place, true, nil
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
func (s *Server) startQueuedDeploy(ctx context.Context, one *queuedDeploy, heldPlace deployPlace) {
	run, err := s.store.Pipelines().PipelineByID(ctx, one.pipelineID)
	if err != nil {
		s.log.Error("read the run to deploy", "job_id", one.jobID, "error", err)
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	job, err := s.store.Pipelines().JobByID(ctx, one.jobID)
	if err != nil {
		s.log.Error("read the deployment waiting to run", "job_id", one.jobID, "error", err)
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	if job.Status != store.JobPending {
		s.log.Info("a deployment stopped waiting before its turn came",
			"job_id", one.jobID, "status", job.Status)
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	project, err := s.store.Projects().ByID(ctx, run.ProjectID)
	if err != nil {
		s.log.Error("read the project to deploy", "project_id", run.ProjectID, "error", err)
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}

	place, placed, placeErr := s.deployPlaceOf(ctx, project, job)
	if placeErr != nil {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot safely resolve the deployment destination: "+placeErr.Error())
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	if !placed || place.identity == "" || place.namespace == "" {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot safely identify the physical cluster and namespace; refusing to deploy without an exclusive lock")
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	if place.queueKey() != heldPlace.queueKey() {
		// Target settings changed while the deployment waited. Do not use a lock for the
		// old destination to deploy into the new one; release the old slot and resolve again.
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		s.startDeployIfReady(context.WithoutCancel(ctx), one.pipelineID)
		return
	}

	latest, err := s.store.Pipelines().IsLatestDeployCandidate(ctx, place.candidateKeys(), run.ID)
	if err != nil {
		s.failUnstartableDeploy(ctx, job, run, project, place,
			"Cannot verify this deployment is still the latest candidate: "+err.Error())
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}
	if !latest {
		s.supersedeDeploy(context.WithoutCancel(ctx),
			queuedDeploy{jobID: job.ID, pipelineID: run.ID}, place)
		s.releaseDeployPlace(context.WithoutCancel(ctx), heldPlace, one.jobID)
		return
	}

	s.launchDeployWorker(ctx, job, run, project, place)
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
