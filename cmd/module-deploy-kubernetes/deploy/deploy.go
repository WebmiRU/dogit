package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
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

	// StateReverted is an image that was put back on purpose: not a deployment that
	// went out, and not one that failed. It gets its own name because in a history of
	// a dozen entries "succeeded" next to "succeeded" says nothing about which of them
	// somebody deliberately went back to.
	StateReverted State = "reverted"
)

// Request is a deployment somebody asked for.
type Request struct {
	// Project is whose deployment this is, and it is what the history is filed under.
	Project string
	// Cluster is the row of settings it goes to, by name.
	Cluster string
	// Namespace is where in the cluster.
	Namespace string
	// PullSecret is the credential the cluster needs to pull Image, when the registry
	// will not serve it anonymously. Empty means the registry is public to the cluster
	// and there is nothing to arrange.
	PullSecret *k8s.PullSecret

	// Image is what to substitute for the placeholder, digest and all. Empty means the
	// repository's manifests are applied as they are, which is only right for a deploy
	// that changes nothing about what runs — so it has to be asked for deliberately.
	Image string
	// Place is the name the repository gave this destination.
	Place string
	// Commit is the short hash the image was built from, kept beside the tags rather
	// than among them.
	Commit string
	// Tags are the names that image was published under. Kept with the deployment
	// rather than looked up later, because a tag can be moved: what a lookup says now
	// is not what was deployed then.
	Tags []string
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

	// Progress says what is happening, as it happens.
	//
	// Called throughout and never waited on: this is what somebody is watching while
	// a rollout runs, and the alternative — a page that says "deploying" until it is
	// either done or wrong — cannot tell them which of a hundred pods is unhappy.
	// A nil Progress is fine and means nothing is watching.
	Progress func(Progress)
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
	ID        uuid.UUID `json:"id"`
	Project   string    `json:"project"`
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	// Image is what was deployed, digest included. It is the answer to "what is in
	// production", and the reason rollback can be exact.
	Image string `json:"image"`
	// FromOurRegistry says whether that image came from dogit's own registry. False
	// means it came from somewhere else and a re-deploy depends on somebody else
	// still having it, which the page says rather than hides.
	FromOurRegistry bool `json:"from_our_registry,omitempty"`
	// Workload is the Deployment a rollback would act on, empty when there is none.
	Workload   string     `json:"workload,omitempty"`
	State      State      `json:"state,omitempty"`
	Phase      Phase      `json:"phase,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	// Place is the name the repository gave this destination. Carried because the
	// cluster and namespace do not say which of three places a record is.
	Place string `json:"place,omitempty"`

	// Tags are the names the image was published under when this deployment happened.
	// Written down rather than asked of the registry afterwards, because a tag can be
	// moved and a lookup later answers a different question than the one being asked.
	Tags   []string `json:"tags,omitempty"`
	Commit string   `json:"commit,omitempty"`
	// Log is what this deployment said, in order. Kept with it because the question the
	// log answers — where did it break — is asked long after the page that watched it
	// was closed.
	Log []LogLine `json:"log,omitempty"`
	// Pods are the counts the rollout passed through: how many were wanted, how
	// many were running the new image, and how many were still on the old one when
	// it was over.
	//
	// Kept because a deployment is remembered by what it did and this is most of
	// it. Without them the history says an operation succeeded, which is the one
	// fact about it that cannot be read off anything.
	PodsWanted  int
	PodsReady   int
	PodsRetired int
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
	// Counts records what the rollout actually did: how many pods were wanted, how
	// many reached the new image, and how many of the old ones went away. Written
	// while it happens rather than read back afterwards, because the old pods are
	// gone by then and their number is not.
	Counts(ctx context.Context, id uuid.UUID, wanted, ready, retired int) error
	// Current is the last deployment that reached a decision, which is what a rollback
	// returns to.
	Current(ctx context.Context, project, cluster, namespace string) (*Deployment, error)
	ByID(ctx context.Context, id uuid.UUID) (Deployment, error)
	// Images is the catalogue of what this project has put on a place, which is not the
	// same question as what it did most recently.
	Images(ctx context.Context, project, cluster, namespace string, limit, offset int) ([]KnownImage, int, error)
	// TagsOf is every name an image has been published under in one place.
	//
	// Asked of the whole history rather than read off the newest record of it, because a
	// rollback's record names the image it put back and not what that image was called:
	// it was called that when it was deployed, days ago, by a run that is not the newest
	// thing in the table. So the page answers "what is running here" with a bare digest
	// exactly when the answer is a version somebody recognises.
	TagsOf(ctx context.Context, project, cluster, namespace, image string) ([]string, error)
	// List is one page of a project's history, newest first, and how many there are.
	//
	// A page, and a total, because a project that has been deployed to for a year has
	// more rows than anybody will read and more than anybody should be made to wait for.
	List(ctx context.Context, project, cluster, namespace string, limit, offset int) ([]Deployment, int, error)
	// LogOf reads back what a deployment said, for a page that arrives afterwards.
	LogOf(ctx context.Context, id uuid.UUID) ([]LogLine, error)
	// Log writes down what a deployment said, in the order it said it.
	//
	// Written when it ends rather than as each line arrives. A rollout says a few dozen
	// things over minutes, so there is nothing to gain from a write per line, and a
	// deployment that died halfway still leaves the lines that explain why.
	Log(ctx context.Context, id uuid.UUID, lines []LogLine) error
}

// KnownImage is one image in the catalogue.
//
// The last deployment that put it somewhere is carried with it, because putting an
// image back is not a request about an image: it is a request about a place and a
// workload, and those are recorded by the deployment rather than by the image.
type KnownImage struct {
	Image     string    `json:"image"`
	FirstSeen time.Time `json:"first_seen"`
	Times     int       `json:"times"`
	Succeeded int       `json:"succeeded"`
	// Tags are the names this image was published under, gathered from every
	// deployment of it.
	//
	// From the deployments rather than from the registry, and for the same reason the
	// operations list reads them from there: a tag can be moved, so asking a registry
	// now answers about whatever is there now, and a catalogue row that disagrees with
	// the operations row above it is worse than a row with no tags at all.
	Tags     []string         `json:"tags,omitempty"`
	Deployed *ImageDeployment `json:"deployed,omitempty"`
}

// ClosingPhases wraps a progress reporter so that a phase is closed when the next one
// begins.
//
// Every phase but the last needs saying goodbye to, and the module knows when that is
// without having to remember it at every step: the moment anything is reported under a
// new phase, the one before it is over. Without this a page has no way to tell a phase
// that is finished from one that is merely quiet — the difference between an arrow on
// a step whose work is done and an arrow on a step that is still going.
//
// The closing line repeats the phase's own name rather than inventing a sentence for
// it, so the page can say "this is finished" in its own words and this does not have to
// know any.
func ClosingPhases(report func(Progress)) func(Progress) {
	last := ""
	return func(progress Progress) {
		// A rolling update brings the new pods up and sends the old ones away at the
		// same time, so retiring does not follow rolling — it overlaps it. Closing one
		// because the other has begun is what left the page sitting on a single arrow
		// through a whole rollout, with the phase everybody came to watch already
		// declared finished and its numbers no longer on screen.
		if progress.Phase == StepRetire {
			report(progress)
			return
		}

		if progress.Phase != "" && last != "" && progress.Phase != last && last != StepRetire {
			report(Progress{Phase: last, Message: "finished", Finished: true})
		}
		if progress.Phase != "" {
			last = progress.Phase
		}
		report(progress)
	}
}

// ImageDeployment is the last deployment of an image, and the place it went to.
type ImageDeployment struct {
	// Image is which image this deployment put on the workload.
	//
	// Named here rather than assumed by whoever is looking: the summary hangs off a
	// catalogue row that already knows the digest, so it looks like a field nobody
	// needs — until somebody asks what "put this back" means and the row says a place
	// and a workload but not what would be going back there.
	Image     string    `json:"image"`
	ID        string    `json:"id"`
	Cluster   string    `json:"cluster"`
	Namespace string    `json:"namespace"`
	Workload  string    `json:"workload"`
	State     State     `json:"state"`
	StartedAt time.Time `json:"started_at"`
}

// LogLine is one thing a deployment said, kept as it was said.
type LogLine struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
	Step    int    `json:"step"`
	Of      int    `json:"of"`
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
	// Log says what is happening as it happens, in the module's own words.
	Log func(string, ...any)
	// Now is the clock, so a test can say what time it is.
	Now func() time.Time
}

// New builds a deployer over a cluster and a history.
func New(client k8s.Client, history History, log func(string, ...any)) *Deployer {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Deployer{client: client, history: history, Log: log, Now: time.Now}
}

func (d *Deployer) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
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
		Tags:      request.Tags,
		Commit:    request.Commit,
		Place:     request.Place,
		State:     StateRunning,
		StartedAt: now(),
	})
	if err != nil {
		return record, err
	}
	d.logf("deployment started: %s to %s/%s", record.ID, request.Cluster, request.Namespace)

	// Every line this deployment says is also written down, here as it happens and to
	// the database when it ends. A watcher sees a deployment once; somebody reading
	// about it next week needs it to still be there.
	var said []LogLine
	watching := request.Progress
	request.Progress = func(progress Progress) {
		// Every line carries the record, including the ones that only close a phase.
		//
		// A line that says which deployment it belongs to is one a page can put in the
		// right place's card. A line without one belongs to none of them, and a project
		// with two places in it then draws one deployment's rollout on both cards — the
		// same steps twice on each, in an order that belongs to neither.
		if progress.Deployment == nil {
			progress.Deployment = &record
		}
		if progress.Message != "" {
			said = append(said, LogLine{
				Phase: progress.Phase, Message: progress.Message,
				Step: progress.Step, Of: progress.Of,
			})
		}
		if watching != nil {
			watching(progress)
		}
	}
	defer func() {
		if err := d.history.Log(ctx, record.ID, said); err != nil {
			d.logf("could not write down the deployment log: %s", err)
		}
	}()

	// Every phase says what it is about to do, so a watcher is never left with a
	// deployment that has started and nothing further to show for it.
	request.report(Progress{Phase: StepPrepare, Deployment: &record, Message: fmt.Sprintf(
		"preparing %d manifest(s) for %s/%s", len(request.Manifests), request.Cluster, request.Namespace)})

	// Pre: jobs that must pass before anything moves.
	for index, job := range request.Pre {
		request.report(Progress{Phase: StepPre, Step: index + 1, Of: len(request.Pre),
			Message: fmt.Sprintf("running the pre-step job %s", job.Name)})
		job.Object.Body = k8s.WithPullSecret(job.Object.Body, job.Object.Kind, pullSecretFor(request))
		if err := d.runJob(ctx, substitution, request, job); err != nil {
			request.report(Progress{Phase: StepPre, Step: index + 1, Of: len(request.Pre),
				Message: err.Error(), Failed: true})
			return d.fail(ctx, request, record, PhasePre, err)
		}
	}

	// The credential the cluster pulls with, before anything is applied: a namespace
	// whose pods cannot pull is a namespace whose pods never start, and finding that
	// out from a rollout timeout is a long way round.
	secretName := ""
	if request.PullSecret != nil {
		if err := d.client.EnsurePullSecret(ctx, request.Namespace, *request.PullSecret); err != nil {
			return d.fail(ctx, request, record, PhaseApply, err)
		}
		secretName = request.PullSecret.Name
		d.logf("wrote the pull secret %s", secretName)
		request.report(Progress{Phase: StepPull, Message: fmt.Sprintf(
			"the cluster can pull from %s", secretName)})
	} else {
		request.report(Progress{Phase: StepPull, Message: "the image needs no credential"})
	}

	// Apply: what the repository wrote, with one thing changed.
	for index, object := range request.Manifests {
		request.report(Progress{Phase: StepApply, Step: index + 1, Of: len(request.Manifests),
			Message: fmt.Sprintf("applying %s %s", object.Kind, object.Name)})
		body := k8s.WithPullSecret(substitution.Apply(object.Body), object.Kind, secretName)
		written := object
		written.Body = body

		if _, err := d.client.Apply(ctx, written); err != nil {
			return d.fail(ctx, request, record, PhaseApply, err)
		}
		d.logf("applied %s", written.Ref())
		request.report(Progress{Phase: StepApply, Step: index + 1, Of: len(request.Manifests),
			Message: fmt.Sprintf("applied %s %s", written.Kind, written.Name)})
	}

	// The rollout itself, when somebody asked to wait for it.
	if request.WaitForRollout && request.Rollout != "" {
		// Watching: the count of pods is the thing somebody watching actually wants,
		// and it is only knowable by asking the cluster as it goes.
		stop, counted := d.watchRollout(ctx, request)
		err := d.wait(ctx, request)

		// The old pods are still on their way out when the new ones are all up: a
		// rolling update finishes as soon as the replacement is serving, and the
		// deployment it replaces is terminated afterwards. Stopping here would cut the
		// watch off one message early, and the line saying the drain is done — the one
		// that says this phase finished rather than what its last number happened to be
		// — would never be said at all.
		//
		// Bounded, because the pods are somebody else's to remove: a finalizer left on a
		// pod must not turn a finished deployment into a deployment that hangs.
		drainDeadline := time.Now().Add(drainGrace)
		for {
			_, _, retired := counted()
			if retired == 0 || time.Now().After(drainDeadline) {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(500 * time.Millisecond):
			}
		}
		stop()

		// Asked once more after the watching has stopped, because watching is by
		// definition a sample: a rollout that finishes between two ticks is a rollout
		// that was never seen at all, and a history that says "0 of 0" about three
		// pods that came up a moment ago is worse than one that says nothing.
		//
		// The state at the end is also the only one worth recording. The numbers during
		// a rollout are for the person watching; the numbers afterwards are the record.
		wanted, ready, retired := counted()
		if final, cerr := d.client.Counts(ctx, request.Namespace, request.Rollout,
			request.Image); cerr == nil {
			wanted, ready = final.Desired, final.Ready
		}
		if err := d.history.Counts(ctx, record.ID, wanted, ready, retired); err != nil {
			d.logf("record what the rollout did: %v", err)
		}

		if err != nil {
			request.report(Progress{Phase: StepRollout, Message: err.Error(), Failed: true})
			return d.fail(ctx, request, record, PhaseApply, err)
		}
		request.report(Progress{Phase: StepRollout, Ready: ready, Desired: wanted,
			Message: fmt.Sprintf("every pod is running the new image (%d of %d)", ready, wanted)})
	}

	// Post: judged separately, and never mistaken for the deployment having worked.
	for index, job := range request.Post {
		request.report(Progress{Phase: StepPost, Step: index + 1, Of: len(request.Post),
			Message: fmt.Sprintf("running the post-step job %s", job.Name)})
		job.Object.Body = k8s.WithPullSecret(job.Object.Body, job.Object.Kind, pullSecretFor(request))
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
// Revert puts back one particular image.
//
// This is not an undo, and the difference is the whole point of the operation. An undo
// steps back one revision of whatever the cluster remembers: its history is bounded,
// prunable, and lost entirely if the Deployment is recreated — and it reports success
// while leaving the same image in place when there is nothing to step back to, which
// is a green tick over nothing.
//
// Here the caller names the deployment they want back, this module looks up the digest
// that deployment ran, and that digest is written onto the workload. Where it goes is
// decided by what this module recorded, not by what the cluster still has a copy of.
//
// What it does not do is undo a migration, a ConfigMap or anything else that was
// applied alongside: only the image goes back.
// Revertable says what is wrong with going back to this record, or nothing.
//
// Asked before the response has begun rather than after, because a revert is narrated:
// the first line written commits the answer to being a success with a story in it, and
// a refusal that arrives afterwards has nowhere to be said. It then goes as a plain
// error with a status of its own, and the caller sees it. Refused inside the stream it
// was a status of 200 and silence — a page that watched a workload change for two
// minutes and was told, at the end, nothing at all.
func Revertable(target Deployment, workload string) error {
	if target.State != StateSucceeded && target.State != StateRolledBack && target.State != StateReverted {
		return fmt.Errorf(
			"deployment %s did not finish, so there is nothing in it to go back to: it %s",
			target.ID, target.State)
	}
	if strings.TrimSpace(target.Image) == "" {
		return fmt.Errorf(
			"deployment %s deployed no image, so there is nothing to go back to", target.ID)
	}
	if strings.TrimSpace(workload) == "" && strings.TrimSpace(target.Workload) == "" {
		return fmt.Errorf(
			"deployment %s says what namespace it went to but not which workload, so there is nothing to change",
			target.ID)
	}
	return nil
}

func (d *Deployer) Revert(ctx context.Context, request RevertRequest) (Deployment, error) {
	target, err := d.history.ByID(ctx, request.ID)
	if err != nil {
		return Deployment{}, err
	}
	if err := Revertable(target, request.Workload); err != nil {
		return Deployment{}, err
	}

	workload := request.Workload
	if workload == "" {
		workload = target.Workload
	}

	// The namespace is the target's, not the target deployment's: somebody may have
	// deployed the same project to two places, and reverting a row has to act on the
	// place being looked at.
	namespace := request.Namespace
	if namespace == "" {
		namespace = target.Namespace
	}

	timeout := request.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	// The place is taken before anything is changed, not after.
	//
	// Written the other way round for a while: roll the pods, then write down what was
	// rolled. Then a revert that found the place already busy had already changed the
	// image before it discovered it could not record the change — the pods on one image,
	// the history saying another, and the refusal arriving after the answer had begun,
	// where it had nowhere to be read. One deployment per place is a rule about the
	// cluster, so it is claimed like one: first, or not at all.
	//
	// A new record rather than a change to the old one: the old one is history, it is
	// the record that this image ran, and rewriting it to say it did not would leave a
	// history with no memory of a version having been live at all.
	reverted := Deployment{
		ID:        uuid.New(),
		Project:   target.Project,
		Cluster:   target.Cluster,
		Namespace: namespace,
		Image:     target.Image,
		// The names this image was published under when it was deployed here, so a page
		// watching the rollback say which version is going back rather than a digest it
		// has to look up. Taken from the deployment being reverted, which is where they
		// were written down.
		Tags:      target.Tags,
		Commit:    target.Commit,
		Workload:  workload,
		State:     StateRunning,
		Phase:     PhaseApply,
		StartedAt: time.Now(),
	}
	reverted, err = d.history.Begin(ctx, reverted)
	if err != nil {
		return reverted, err
	}

	// Watching, as with a deploy: the pods are the progress — and every line of it names
	// the record it is about, so that a page can mark the image being put back from the
	// first line rather than waiting for the end of the rollout to be told what it was
	// watching. Sent without the record it has nothing to mark: the digits arrive with
	// no image beside them, and a table of images does not move for the whole rollback.
	// What it said, written down when it is over, as a deployment's is.
	//
	// A rollback that kept nothing left a page opened a minute later with no steps and
	// no figures at all — the live lines exist only for somebody watching, and a record
	// that does not carry them is a record that cannot answer "what did it do" to the
	// person who comes to find out.
	said := []LogLine{}
	say := func(progress Progress) {
		if progress.Message != "" {
			said = append(said, LogLine{
				Phase: progress.Phase, Message: progress.Message,
				Step: progress.Step, Of: progress.Of,
			})
		}
		if progress.Deployment == nil {
			progress.Deployment = &reverted
		}
		// Through the helper, which tolerates a caller that asked for no narration at
		// all — a rollback run from a test or a script still records what it did, and
		// there being nobody watching is not a reason to stop.
		report(request.Progress, progress)
	}

	stop, counted := d.watchRollout(ctx, Request{Progress: say, Rollout: workload,
		Namespace: namespace, Image: target.Image})
	rollout, err := d.client.SetImage(ctx, namespace, workload, target.Image, timeout)

	// And then the old pods, which a rolling update sends away after the new ones are
	// serving — the same wait a deployment makes, and for the same reason: stopping when
	// the last new pod is ready cuts the watch off before the drain is reported, and the
	// record of a rollback then ends with "one pod still running the previous image" and
	// nothing that says it finished. Bounded, because removing a pod is the cluster's
	// business and a finalizer left on one must not hang a rollback.
	drainDeadline := time.Now().Add(drainGrace)
	for {
		_, _, retired := counted()
		if err != nil || retired == 0 || time.Now().After(drainDeadline) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(500 * time.Millisecond):
		}
	}
	stop()
	wanted, ready, retired := counted()
	if final, cerr := d.client.Counts(ctx, namespace, workload, target.Image); cerr == nil {
		wanted, ready = final.Desired, final.Ready
	}
	if err != nil {
		failed, finishErr := d.finish(ctx, reverted, PhaseApply, StateFailed, err.Error())
		if finishErr != nil {
			return failed, finishErr
		}
		return failed, err
	}
	d.logf("put %s back on %s: %s", target.Image, workload, rollout.Reason)
	if err := d.history.Counts(ctx, reverted.ID, wanted, ready, retired); err != nil {
		d.logf("record what the rollback rolled out: %v", err)
	}

	report(say, Progress{Phase: StepApply, Message: fmt.Sprintf(
		"%s now runs %s", workload, target.Image)})

	// And that the drain is over, in the words the deploy path uses: a record whose last
	// line is a count that is still going down has never said it finished.
	report(say, Progress{Phase: StepRollout, Ready: wanted, Desired: ready,
		Message: fmt.Sprintf("every pod is running the image again (%d of %d)", ready, wanted)})

	reverted, err = d.finish(ctx, reverted, PhaseApply, StateReverted,
		fmt.Sprintf("the image of deployment %s was put back on %s; a migration, a ConfigMap and "+
			"anything else applied alongside were left as they are", request.ID, workload))
	if err != nil {
		return reverted, err
	}
	if err := d.history.Log(ctx, reverted.ID, said); err != nil {
		d.logf("could not write down the rollback log: %v", err)
	}
	return reverted, nil
}

// Progress is one thing that is happening, or has happened, in a deployment.
//
// A flat shape on purpose: this crosses a process boundary as JSON and is rendered by
// something that is not this module, and every level of nesting is a place the two can
// disagree about what a field means.
type Progress struct {
	// Phase is which part of the deployment this is: prepare, pull, apply, rollout.
	Phase string `json:"phase"`
	// Step and of say where in that phase, so a rollout of four manifests reads "3 of 4"
	// rather than three lines in a row with nothing to count.
	Step int `json:"step,omitempty"`
	Of   int `json:"of,omitempty"`
	// Previous is what the pods being taken off are running, by digest, while a rollout
	// has some to take off.
	//
	// Carried on the line rather than looked up afterwards because the line is what a
	// page is watching: it arrives when the drain changes, it arrives from the module
	// that watched the pods, and it names the image the step is about — so the same
	// figure that says "one pod still on it" says which one. A page that has to ask the
	// module what is there is a page that is a request behind the thing it is drawing.
	Previous string `json:"previous,omitempty"`
	// Retiring is how many pods are still on `Previous`, as a number rather than only as
	// words. Sent so that a page can count the drain forwards — "retire 1 of 3" is a
	// figure, and a figure is better off read from a field than guessed out of a
	// sentence that is written for a person.
	Retiring int `json:"retiring,omitempty"`
	// Message is the sentence to show, in this module's words.
	Message string `json:"message"`
	// Ready and Desired are the pods, when this is about pods.
	Ready   int `json:"ready,omitempty"`
	Desired int `json:"desired,omitempty"`
	// Failed is set once something has gone wrong, and the message says what.
	Failed bool `json:"failed,omitempty"`
	// Finished closes a phase, and only that phase.
	//
	// A phase otherwise never says it is over: it simply stops speaking, and a client
	// cannot tell "nothing more is coming" from "something is coming in a moment". That
	// is why an arrow on the page used to sit on a step long after the work behind it
	// had finished, and why the line saying "0 pods left" was missing — the honest
	// closing line was the one the count made impossible to send.
	Finished bool `json:"finished,omitempty"`
	// Done ends the stream.
	Done bool `json:"done,omitempty"`
	// Deployment is the record, sent once at the end.
	Deployment *Deployment `json:"deployment,omitempty"`
}

// The phases a deployment goes through, in the order they happen.
const (
	StepPrepare = "prepare"
	StepPre     = "pre"
	StepPull    = "pull"
	StepApply   = "apply"
	StepRollout = "rollout"
	StepRetire  = "retire"
	StepPost    = "post"
)

// report is the progress callback, or nothing at all.
func (r Request) report(progress Progress) {
	report(r.Progress, progress)
}

// report says something happened, to whoever is watching. A nil callback means
// nothing is, which is a normal way to run this.
func report(to func(Progress), progress Progress) {
	if to == nil {
		return
	}
	to(progress)
}

// RevertRequest is which deployment to go back to, and where.
type RevertRequest struct {
	// ID is the deployment whose image goes back. Not "the previous one": the caller
	// picked a row on a page and this is that row.
	ID uuid.UUID
	// Workload and Namespace say where to put it, and default to what that deployment
	// recorded when they are not given.
	Workload  string
	Namespace string
	Timeout   time.Duration
	// Progress says what is happening, as it happens.
	Progress func(Progress)
}

// Finish closes a deployment from outside Run, which is what a rollback does.
func (d *Deployer) Finish(ctx context.Context, id uuid.UUID, state State, reason string) error {
	return d.history.Finish(ctx, id, state, reason)
}

func (d *Deployer) finish(ctx context.Context, record Deployment, phase Phase, state State,
	reason string) (Deployment, error) {

	// The outcome is written with a context that does not care whether the caller is
	// still there.
	//
	// This matters more than it looks. A deployment that is cut short — the core going
	// away, a browser closing, a proxy giving up — is exactly a deployment that must
	// still be recorded as finished, because the record that says "running" is what
	// holds the place, and a place held by a record nobody will ever close cannot be
	// deployed to again. Writing the outcome on the caller's context loses it in
	// precisely the case it exists for.
	finishing, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	ctx = finishing

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
func (d *Deployer) fail(ctx context.Context, request Request, record Deployment,
	phase Phase, cause error) (Deployment, error) {

	d.logf("deployment %s failed in %s: %v", record.ID, phase, cause)

	// Said the way every other step is said, and for the reason every other step is said:
	// the log of a deployment is the list of things that happened to it, and a list that
	// ends at the last thing that worked is missing the one that matters.
	//
	// Written through the request rather than straight to the store, because that is
	// what puts it in both places at once: on the card while it happens, and in the log
	// when it is read afterwards. A line written only to the store is overwritten by the
	// run's own closing write, so the failure has to go in with the rest.
	request.report(Progress{Phase: string(phase), Failed: true,
		Message: fmt.Sprintf("failed in %s: %v", phase, cause)})

	failed, writeErr := d.finish(ctx, record, phase, StateFailed, cause.Error())
	if writeErr != nil {
		// Both: the deployment did fail, and the reason it failed could not be written
		// down. A caller that sees only the write failure would go looking for the
		// wrong thing.
		return failed, errors.Join(cause, writeErr)
	}
	return failed, cause
}

// pullSecretFor is the name of the pull secret a request carries, or empty.
func pullSecretFor(request Request) string {
	if request.PullSecret == nil {
		return ""
	}
	return request.PullSecret.Name
}

// drainGrace is how long a finished deployment waits for the pods it replaced to go.
//
// The pods are not the module's to remove — the cluster terminates them, and something
// of theirs may hold one for a while — so this is a courtesy rather than a requirement:
// it lets the drain finish and be reported, and then the deployment is finished whatever
// is left.
const drainGrace = 30 * time.Second

// watchRollout follows a rollout while it happens, and returns two things: how to stop
// following it, and what the last counts it saw were.
//
// The counting is the cluster's own event stream rather than a question asked every
// couple of seconds. That is the whole of the difference between a rollout being
// watched and a rollout being guessed at: a timer can only ever see the states that
// happened to coincide with it, so a rollout that went from one ready pod to ten
// inside one interval was two numbers and nothing at all between them — and the page
// showed exactly that jump, which looked like a broken counter and was in fact an
// honest report of what it had been able to see.
//
// Whether the stream is still a complete answer is the client's business, not this
// one's: it re-reads the truth every few seconds so that a severed watch costs a
// moment of history rather than a rollout nobody can account for.
func (d *Deployer) watchRollout(ctx context.Context, request Request) (func(), func() (int, int, int)) {
	if request.Progress == nil || request.Rollout == "" {
		return func() {}, func() (int, int, int) { return 0, 0, 0 }
	}

	// The counts are read by the stop-and-report path while the watch is still writing
	// them, so they sit behind a lock rather than being shared as plain variables: a
	// race here would show one deployment's final numbers under another's name.
	var (
		mutex   sync.Mutex
		wanted  int
		ready   int
		retired int
	)
	remember := func(counts k8s.RolloutCounts) {
		mutex.Lock()
		wanted, ready, retired = counts.Desired, counts.Ready, counts.OldUp
		mutex.Unlock()
	}
	last := func() (int, int, int) {
		mutex.Lock()
		defer mutex.Unlock()
		return wanted, ready, retired
	}

	// The context is what ends this, not a channel of its own: the watcher is started
	// by a deploy that has a deadline, and a second way to stop it would be a second
	// thing to forget to use.
	child, cancel := context.WithCancel(ctx)
	finished := make(chan struct{})

	go func() {
		defer close(finished)

		// Each phase is spoken for by its own number, and says so only when that
		// number moves. Publishing both on every change was how a log came to read
		// "1 of 10", "1 of 10", "2 of 10", "2 of 10": the pods retiring changed a
		// number the rollout line was not reporting, and the rollout line repeated
		// itself to match.
		lastReady, lastOld, lastReadyDesired := -1, -1, -1
		retireClosed := false

		_ = d.client.WatchCounts(child, request.Namespace, request.Rollout, request.Image,
			func(counts k8s.RolloutCounts) {
				remember(counts)

				// Said at zero too, which is the beginning and not the absence of
				// anything. Held back, a rollout's first seconds were a step on the
				// page with nothing written on it, which reads as a step that has not
				// begun rather than one that has.
				if counts.Ready != lastReady || counts.Desired != lastReadyDesired {
					lastReady, lastReadyDesired = counts.Ready, counts.Desired
					request.Progress(Progress{
						Phase:    StepRollout,
						Message:  fmt.Sprintf("%d of %d running the new image", counts.Ready, counts.Desired),
						Ready:    counts.Ready,
						Desired:  counts.Desired,
						Previous: counts.OldImage,
					})
				}

				if counts.OldUp == lastOld {
					return
				}
				lastOld = counts.OldUp

				// The closing line, sent once, saying the drain is done. Without it the
				// last thing anybody reads about this phase is a count that is still
				// going down, which never becomes the sentence that says it finished.
				if counts.OldUp > 0 {
					// How many there are altogether, not only how many are left.
					//
					// Without it a page watching this drain has nothing to count
					// against: it takes the first number it hears as the whole, so a
					// rollout that began with two pods still up is "0 of 2" for ever,
					// with a bar that fills to the end while a third pod is still
					// serving. The number of pods is the one this step is about.
					request.Progress(Progress{
						Phase:    StepRetire,
						Message:  fmt.Sprintf("%d pod(s) still running the previous image", counts.OldUp),
						Ready:    counts.Ready,
						Desired:  counts.Desired,
						Previous: counts.OldImage,
						Retiring: counts.OldUp,
					})
					return
				}
				if retireClosed {
					return
				}
				retireClosed = true
				request.Progress(Progress{
					Phase:    StepRetire,
					Message:  "the old pods are gone",
					Finished: true,
				})
			})
	}()

	return func() {
		cancel()
		<-finished
	}, last
}
