package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// One deployment, from the moment it is asked for to the moment it is decided.
//
// The phases are in the order they happen and they are the whole of what a deployment
// is: jobs that must pass first, the objects themselves, and jobs that only make sense
// afterwards. The reason for the order is a database: a migration that runs during a
// rolling update leaves some pods on a new schema and some on an old one, and whether
// that is survivable is a decision about the application, not about the cluster.

// Phase is one step of a deployment.
type Phase string

const (
	// PhasePre runs before anything is applied. A migration belongs here: it either
	// succeeds and the rollout starts, or it fails and nothing has moved.
	PhasePre Phase = "pre"
	// PhaseApply writes the manifests and, when asked, waits for the rollout.
	PhaseApply Phase = "apply"
	// PhasePost runs after. Judged separately, because a failed smoke test after a
	// successful rollout is information about the new version, not a failed deployment
	// — and treating the two alike is how people learn to ignore one of them.
	PhasePost Phase = "post"
)

// State is how far a deployment got.
type State string

const (
	StateRunning   State = "running"
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	// StateRolledBack means somebody undid it afterwards. It is not a failure: the
	// deployment did what it was asked to, and a person decided it was wrong.
	StateRolledBack State = "rolled_back"
)

// Request is a deployment somebody asked for.
type Request struct {
	// Project is whose deployment this is, and it is what the history is filed under.
	Project string
	// Cluster is the row of settings it goes to, by name.
	Cluster string
	// Namespace is where in the cluster.
	Namespace string
	// Image is what to substitute for the placeholder, digest and all. Empty means the
	// repository's manifests are applied as they are, which is only right for a deploy
	// that changes nothing about what runs — so it has to be asked for deliberately.
	Image string
	// Placeholder is the token the manifests leave where the image goes.
	Placeholder string

	// Manifests are the objects, as they came out of the repository.
	Manifests []k8s.Object
	// Pre and Post are the one-shot jobs around the rollout.
	Pre  []Job
	Post []Job

	// WaitForRollout waits for the new pods to become ready and reports how it went.
	WaitForRollout bool
	// Rollout names the workload whose progress is watched.
	Rollout string
	// Workload is the Deployment a rollback would act on. Recorded on the deployment
	// so that a rollback has something to act on without asking the cluster what it
	// thinks was deployed.
	Workload string
	// Timeout is how long to wait. Zero means the target's own.
	Timeout time.Duration
	// KeepJobs leaves the phase Jobs in place after they finish. Off by default: a Job
	// that has run and succeeded is history, and a namespace that accumulates them is a
	// namespace nobody can read.
	KeepJobs bool
}

// Job is one one-shot job around the rollout.
type Job struct {
	Name   string
	Object k8s.Object
}

// Deployment is a deployment as it is being carried out, and as it is remembered.
type Deployment struct {
	ID        uuid.UUID
	Project   string
	Cluster   string
	Namespace string
	// Image is what was deployed, digest included. It is the answer to "what is in
	// production", and the reason rollback can be exact.
	Image string
	// FromOurRegistry says whether that image came from dogit's own registry. False
	// means it came from somewhere else and a re-deploy depends on somebody else
	// still having it, which the page says rather than hides.
	FromOurRegistry bool
	// Workload is the Deployment a rollback would act on, empty when there is none.
	Workload   string
	State      State
	Phase      Phase
	Reason     string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// History is what a module remembers, so that a rollback has something to go back to.
//
// An interface rather than a database, because the part that has to be right — which
// revision is current, what an undo means, that two rollouts cannot race — is not a
// property of Postgres, and testing it against a real one every time would be slower
// and would test the database rather than the rule.
type History interface {
	// Begin files a deployment and refuses if one is already under way for this
	// cluster and namespace. The refusal is the point: two rollouts at once is two
	// migrations against one database.
	Begin(ctx context.Context, d Deployment) (Deployment, error)
	// Phase records how far it got, which is also what the lock is released against.
	Phase(ctx context.Context, id uuid.UUID, state State, phase Phase, reason string) error
	// Finish closes a deployment, leaving the lock free.
	Finish(ctx context.Context, id uuid.UUID, state State, reason string) error
	// Current is the last deployment that reached a decision, which is what a rollback
	// returns to.
	Current(ctx context.Context, project, cluster, namespace string) (*Deployment, error)
	// List is a project's history, newest first.
	List(ctx context.Context, project, cluster, namespace string) ([]Deployment, error)
}

// ErrBusy is returned when a deployment for this cluster and namespace is already under
// way.
type ErrBusy struct {
	Namespace string
	Running   string
}

func (e ErrBusy) Error() string {
	return fmt.Sprintf("a deployment to %s is already running (%s); two at once would run two migrations against one database",
		e.Namespace, e.Running)
}

// Deployer carries one deployment out.
type Deployer struct {
	client  k8s.Client
	history History
	log     func(string, ...any)
	// Now is the clock, so a test can say what time it is.
	Now func() time.Time
}

func (d *Deployer) logf(format string, args ...any) {
	if d.log != nil {
		d.log(format, args...)
	}
}

// Run carries out a deployment and returns what happened.
//
// Every step is attempted in order and a failure stops it there: a failed migration
// must not be followed by a rollout, and a failed rollout must not be followed by a
// smoke test that would report the new version as working.
func (d *Deployer) Run(ctx context.Context, request Request) (Deployment, error) {
	now := d.Now
	if now == nil {
		now = time.Now
	}

	image := request.Image
	substitution := k8s.Substitution{Placeholder: request.Placeholder, Image: image}

	record, err := d.history.Begin(ctx, Deployment{
		ID:        uuid.New(),
		Project:   request.Project,
		Cluster:   request.Cluster,
		Namespace: request.Namespace,
		Image:     image,
		Workload:  request.Workload,
		State:     StateRunning,
		StartedAt: now(),
	})
	if err != nil {
		return record, err
	}
	d.logf("deployment started: %s to %s/%s", record.ID, request.Cluster, request.Namespace)

	// Pre: jobs that must pass before anything moves.
	for _, job := range request.Pre {
		if err := d.runJob(ctx, substitution, request, job); err != nil {
			return d.fail(ctx, record, PhasePre, err)
		}
	}

	// Apply: what the repository wrote, with one thing changed.
	for _, object := range request.Manifests {
		body := substitution.Apply(object.Body)
		written := object
		written.Body = body

		if _, err := d.client.Apply(ctx, written); err != nil {
			return d.fail(ctx, record, PhaseApply, err)
		}
		d.logf("applied %s", written.Ref())
	}

	// The rollout itself, when somebody asked to wait for it.
	if request.WaitForRollout && request.Rollout != "" {
		if err := d.wait(ctx, request); err != nil {
			return d.fail(ctx, record, PhaseApply, err)
		}
	}

	// Post: judged separately, and never mistaken for the deployment having worked.
	for _, job := range request.Post {
		if err := d.runJob(ctx, substitution, request, job); err != nil {
			failed, finishErr := d.finish(ctx, record, PhasePost, StateFailed, err.Error())
			if finishErr != nil {
				return failed, finishErr
			}
			d.logf("the deployment applied but its post step failed: %v", err)
			return failed, fmt.Errorf(
				"the rollout finished and then the post step failed: %w", err)
		}
	}

	return d.finish(ctx, record, "", StateSucceeded, "")
}

// runJob runs one one-shot job and waits for it, then removes it.
func (d *Deployer) runJob(ctx context.Context, substitution k8s.Substitution,
	request Request, job Job) error {

	written := job.Object
	written.Body = substitution.Apply(job.Object.Body)

	if _, err := d.client.Apply(ctx, written); err != nil {
		return fmt.Errorf("start %s: %w", written.Ref(), err)
	}
	d.logf("started %s", written.Ref())

	if !request.KeepJobs {
		// Removed when it is done, not before: a Job that failed is the evidence of
		// why, and deleting it on the way out is deleting the evidence.
		defer func() {
			if err := d.client.Delete(ctx, written); err != nil {
				d.logf("could not remove the job %s: %v", written.Ref(), err)
			}
		}()
	}

	done, err := d.waitForJob(ctx, written)
	if err != nil {
		return err
	}
	if !done {
		return fmt.Errorf("%s did not finish", written.Ref())
	}
	return nil
}

// wait waits for a workload to become ready.
func (d *Deployer) wait(ctx context.Context, request Request) error {
	within := request.Timeout
	if within <= 0 {
		within = 10 * time.Minute
	}

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		rollout, err := d.client.Rollout(ctx, request.Namespace, request.Rollout)
		if err != nil {
			return err
		}
		if rollout.Done {
			d.logf("rollout finished: %d/%d ready", rollout.Ready, rollout.Desired)
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}

	// What it said last, which is what a person needs rather than "it timed out".
	last, err := d.client.Rollout(ctx, request.Namespace, request.Rollout)
	if err == nil {
		return fmt.Errorf("the rollout did not finish in %s: %s", within, last.Reason)
	}
	return fmt.Errorf("the rollout did not finish in %s", within)
}

// waitForJob waits for a Job to complete, and says why when it failed.
func (d *Deployer) waitForJob(ctx context.Context, object k8s.Object) (bool, error) {
	within := 30 * time.Minute
	deadline := time.Now().Add(within)

	for time.Now().Before(deadline) {
		live, err := d.client.Get(ctx, object)
		if err != nil {
			// A Job the cluster has not admitted yet is a Job that has not failed.
			if strings.Contains(err.Error(), "not found") {
				select {
				case <-ctx.Done():
					return false, ctx.Err()
				case <-time.After(2 * time.Second):
					continue
				}
			}
			return false, err
		}

		if failed, reason := jobFailed(live); failed {
			return false, fmt.Errorf("%s failed: %s", object.Ref(), reason)
		}
		if succeeded, _ := jobSucceeded(live); succeeded {
			return true, nil
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return false, nil
}

// Rollback returns a project's last deployment to the revision before it.
//
// It does one thing and refuses to pretend otherwise: the image goes back, and
// anything else that was applied alongside stays where it is. A database does not
// roll back, and a page that said "rolled back" without saying that would be the worst
// kind of wrong.
func (d *Deployer) Rollback(ctx context.Context, project, cluster, namespace string) (Deployment, error) {
	current, err := d.history.Current(ctx, project, cluster, namespace)
	if err != nil {
		return Deployment{}, err
	}
	if current == nil {
		return Deployment{}, fmt.Errorf("there is nothing to roll back: %s has not been deployed", namespace)
	}

	workload := workloadOf(current)
	if workload == "" {
		return Deployment{}, fmt.Errorf("the last deployment to %s is not a workload, so there is no revision to go back to", namespace)
	}

	rollout, err := d.client.Rollback(ctx, namespace, workload)
	if err != nil {
		return *current, err
	}

	d.logf("rolled %s back: %s", workload, rollout.Reason)

	// Said plainly, because "rolled back" without it is the answer that makes people
	// believe a database went backwards too.
	reason := fmt.Sprintf(
		"the image came back to the previous revision of %s; anything applied alongside was left alone, "+
			"and a database is not undone by anything", workload)

	if err := d.Finish(ctx, current.ID, StateRolledBack, reason); err != nil {
		return *current, err
	}

	// The copy was read before the history was told, so the answer is assembled here
	// rather than handed back stale — a caller that reports "succeeded" right after a
	// rollback has undone the one message that mattered.
	current.State = StateRolledBack
	current.Reason = reason
	return *current, nil
}

// Finish closes a deployment from outside Run, which is what a rollback does.
func (d *Deployer) Finish(ctx context.Context, id uuid.UUID, state State, reason string) error {
	return d.history.Finish(ctx, id, state, reason)
}

func (d *Deployer) finish(ctx context.Context, record Deployment, phase Phase, state State,
	reason string) (Deployment, error) {

	if phase != "" {
		if err := d.history.Phase(ctx, record.ID, state, phase, reason); err != nil {
			return record, err
		}
	}
	if err := d.history.Finish(ctx, record.ID, state, reason); err != nil {
		return record, err
	}

	record.State = state
	record.Phase = phase
	record.Reason = reason
	return record, nil
}

// fail records where a deployment stopped and hands the cause back.
//
// The cause is returned as well as recorded, because the two answer different
// questions: the history answers "what happened to this deployment" for somebody
// reading the page later, and the returned error answers "why did this call fail" for
// the task that made the call. Losing the second while keeping the first is how a
// failed deployment is reported as a successful one with a note in a table.
func (d *Deployer) fail(ctx context.Context, record Deployment, phase Phase, cause error) (Deployment, error) {
	d.logf("deployment %s failed in %s: %v", record.ID, phase, cause)

	failed, writeErr := d.finish(ctx, record, phase, StateFailed, cause.Error())
	if writeErr != nil {
		// Both: the deployment did fail, and the reason it failed could not be written
		// down. A caller that sees only the write failure would go looking for the
		// wrong thing.
		return failed, errors.Join(cause, writeErr)
	}
	return failed, cause
}
