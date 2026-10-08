package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
	"github.com/ewolf/dogit/internal/pipeline"
	"github.com/ewolf/dogit/internal/store"
)

// The core's half of a deployment.
//
// What the core does is: notice that a run has something to deploy, read the
// repository's manifests at the commit being deployed, work out which image that
// run produced, and hand all of it to the module that was named. What it does not
// do is apply anything, decide that a rollout succeeded, or know what a namespace
// is — all of that is the module's, because the day somebody deploys to Nomad this
// file changes in one place: the target's kind.
//
// The core's job here is to be a good narrator. Everything a person would want to
// know while a deployment is happening goes into the job's log as it happens,
// because that log is already on the pipeline page and the alternative is a module
// with a page of its own that nobody looks at.

// deployTargetKind is what a `target:` names in a module kind.
//
// Written in the file rather than assumed, because "kubernetes" here means a module
// of kind "deploy:kubernetes" and the mapping is not something this program may
// guess at: a target whose module is not installed has to be an error that says so,
// not a silently missing deployment.
const deployTargetKind = "deploy:%s"

// deployRequest is what the core sends a deploy module.
//
// It is the repository's words, not the core's: paths as written, the image as
// built. The module substitutes and applies, and answers with what happened.
type deployRequest struct {
	Project   string `json:"project"`
	Target    string `json:"target"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`

	// Image is what to substitute, and what the rollback would return to. Empty means
	// the manifests are applied exactly as written, which is only right for a
	// deployment that changes nothing about what runs.
	Image       string `json:"image"`
	Placeholder string `json:"placeholder"`

	Manifests []deployManifest `json:"manifests"`
	Pre       []deployStep     `json:"pre"`
	Post      []deployStep     `json:"post"`

	Expect struct {
		Secrets    []string `json:"secrets"`
		ConfigMaps []string `json:"config_maps"`
	} `json:"expect"`

	// Registry is the credential the cluster pulls with, when it will not serve the
	// image on its own. Omitted rather than sent empty: a module that is told there is
	// no credential writes nothing, and a module told to write an empty one writes a
	// Secret that fails in a more confusing way.
	Registry *registryCredential `json:"registry,omitempty"`

	WaitForRollout bool `json:"wait_for_rollout"`
	TimeoutSeconds int  `json:"timeout_seconds"`

	// Ref and Sha travel with the task so the module can log what it deployed and a
	// rollback can say what it returned to, without asking the core again.
	Ref string `json:"ref"`
	Sha string `json:"sha"`

	// Tags are the names the image was published under, as they were when this
	// deployment happened.
	//
	// Written down rather than asked for later, on purpose. Asking the registry which
	// tags point at a digest means listing every tag and reading every manifest, and
	// the answer changes: a tag may be moved afterwards, so a lookup a year later
	// describes now, not what was deployed. This is what was true then, recorded when
	// it was true, which is the only version of the question that has an answer.
	Tags []string `json:"tags,omitempty"`

	// Commit is the short hash the image was built from, kept apart from the tags so
	// that a page can show a column of names and a column of commits rather than one
	// list that is neither.
	Commit string `json:"commit,omitempty"`

	// Preface is what this run did to the image before the deployment began, under the
	// phases the plan already promises: the build and the push, which happened in another
	// job and finished before this one started.
	//
	// For the record rather than for the watchers. The build said what it was doing
	// while it did it, in its own words, to whoever was watching then — and a page that
	// was not open at the time reads this deployment's log next week, where a log
	// starting at the manifests says the image arrived from nowhere. Two lines, said
	// once, in the past tense, about work that is already done.
	Preface []prefaceLine `json:"preface,omitempty"`

	// Place is the name this repository gave the destination, when it named it.
	//
	// Carried because the cluster and namespace do not say which of three places this
	// is: a file deploying web, worker and site writes three records that differ only by
	// where they went, and a list of those tells a reader nothing about which one failed.
	Place string `json:"place,omitempty"`
}

// registryCredential is what a cluster needs to pull this project's images.
type registryCredential struct {
	// Address is the registry host:port, which has to match the image name exactly.
	Address string `json:"address"`
	Token   string `json:"token"`
	// Username pairs with the token. Registries that issue a token rather than a password
	// want the token in the password field and anything here, which is the Docker
	// convention every client follows; a registry that issues neither is reached with its
	// own login, which is what an address from the list of registries brings.
	Username string `json:"username,omitempty"`
	// SecretName is what the module should call the Secret it writes.
	SecretName string `json:"secret_name"`
	// InsecureTLS is what is known about this registry's certificate, for a module that
	// has to reach the same registry over the same connection to ask whether an image is
	// still there. It is not written into the cluster: a kubelet is told to accept a bad
	// certificate by a flag, not by a secret, and a secret is not that flag.
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

type deployManifest struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Body       string `json:"body"`
}

type deployStep struct {
	APIVersion string   `json:"api_version"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"`
	Image      string   `json:"image"`
	Command    []string `json:"command"`
	Body       string   `json:"body"`
}

// imagePlaceholder is what the manifests write where the image goes.
//
// Fixed rather than configurable, because a second spelling is a second thing to
// get wrong and nothing about it is a per-project decision: every manifest uses it
// or the substitution does not happen, and a silent no-op there is a deployment of
// whatever was in the repository.
const imagePlaceholder = "IMAGE"

// runDeployJob carries out one deployment.
//
// It is called when a run has finished the work that produced an image, and it does
// the narrating as it goes: the log is written line by line, so somebody watching the
// pipeline sees the phases as they pass rather than a job that goes quiet.
func (s *Server) runDeployJob(ctx context.Context, job *store.Job, pipelineRun *store.Pipeline,
	project *models.Project, repoDir string, config *pipeline.Config) error {

	// This job's own place, not whichever one the file happened to list first.
	//
	// Read from the job because that is where it was decided: the rules were applied
	// when the run was created, and a file that has been edited since describes a
	// different set of places than the one this run was asked to do.
	spec := deploySpecForJob(job, config)
	if !spec.Present {
		return fmt.Errorf("this job carries no deployment to carry out")
	}

	// The newline is in the format string, not added here: appendJobOutput marks each
	// line it is given, and a line that has already ended gets an empty one after it,
	// which reads as though the deployment paused between every step.
	log := func(format string, args ...any) {
		_, _ = s.appendJobOutput(ctx, job, "out", fmt.Sprintf(format, args...))
	}
	if spec.Name != "" {
		log("Deploying to %s\n", spec.Name)
	}

	started := time.Now()

	// The core's own steps, on the same channel the module narrates on.
	//
	log("Deploying to %s\n", spec.Target)
	log("  module:    %s\n", spec.Module)

	// The image this run built. A deployment of something else is a different thing
	// and is refused here rather than sent on to be applied as though it were what
	// the run produced: the whole point of rolling out by digest is that the thing
	image, err := s.imageForDeploy(ctx, job, pipelineRun, log)
	if err != nil {
		return err
	}
	log("  image:     %s\n", image)

	// Build and push are not announced from here any more.
	//
	// They used to be, because they were over before the deploy started and this was
	// the only place that knew they had happened. The runner now says so itself, while
	// it is doing it, in its own words — and a step re-announced here afterwards with
	// worse ones ("the image was built by \"image\"") overwrote what the reader had
	// already watched happen. What is still recorded is the deploy job's own log: what
	// is being deployed, and which digest, which is what decides what gets rolled out.

	manifests, err := s.readDeployManifests(ctx, repoDir, pipelineRun.SHA, spec.Manifests, log)
	if err != nil {
		return err
	}

	pre, err := s.readDeploySteps(ctx, repoDir, pipelineRun.SHA, spec.Pre, log)
	if err != nil {
		return err
	}
	post, err := s.readDeploySteps(ctx, repoDir, pipelineRun.SHA, spec.Post, log)
	if err != nil {
		return err
	}

	// The names the image went out under, asked for once: the deployment is about this
	// image under these names, and its record says what became of them.
	tags := s.deployedImageTags(ctx, repoDir, pipelineRun)

	request := deployRequest{
		Project: project.Path,
		// Where in the cluster, and which cluster, is the place's own business: it is
		// written down on the row that carries the kubeconfig, and a configuration that
		// repeated it here would be a second place to keep in step with the first.
		Place:          spec.Target,
		Image:          image,
		Placeholder:    imagePlaceholder,
		Manifests:      manifests,
		Pre:            pre,
		Post:           post,
		WaitForRollout: spec.Rollout,
		TimeoutSeconds: deployTimeoutSeconds(spec.Timeout),
		Ref:            pipelineRun.Ref,
		Sha:            pipelineRun.SHA,
		Tags:           tags,
		Commit:         shortRunSHA(pipelineRun),
		Preface:        prefaceFor(ctx, s, pipelineRun, job.ID, image, tags),
	}

	request.Expect.Secrets = spec.Expect.Secrets
	request.Expect.ConfigMaps = spec.Expect.ConfigMaps

	target, err := s.deployModuleForPlace(ctx, project, spec.Target, spec.Module)
	if err != nil {
		return err
	}

	// Which registry this place pulls from, and what it pulls with.
	//
	// The place's own row may name one, and when it does that is where the image is
	// fetched from — the same path and the same digest at another address, so the thing
	// rolled out is the thing that was built. With nothing named, or with the registry this
	// instance runs named, this is the path every deployment took before any of this
	// existed: the address the image carries, and a credential minted per project for a
	// pull and nothing else.
	//
	// The registry is private by default, so a cluster given nothing to pull with will sit
	// at ImagePullBackOff and the deployment will fail on a rollout timeout with no cause
	// in it. That credential goes to the module and nowhere else.
	pull, err := s.placePullFor(ctx, project, target, spec.Target, image, log)
	switch {
	case err != nil:
		// Two kinds of "no", and only one of them stops the job. A refusal is the core
		// saying the deployment cannot be carried out from what this instance knows —
		// the place has named no registry, or named one that is not on the list — and it
		// says so here rather than letting the module be sent to a cluster that would
		// spend a rollout timeout failing to pull. Anything else is written into the log
		// and the deployment goes on: an unanswerable registry lookup is a problem with
		// this installation, not with the place, and refusing every deployment over it
		// would turn one broken thing into no deployments at all.
		if sentence, refused := registryRefusal(err); refused {
			s.writeDeployLog(ctx, job, "This deployment cannot be carried out: "+sentence+"\n")
			return &placeRegistryRefusal{sentence: sentence}
		}
		log("  registry:   %v\n", err)
	case pull != nil:
		log("  registry:   %s%s\n", pull.Address, pullSaid(pull))
		request.Image = pull.Image
		request.Registry = pull.credential()
	}

	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("could not describe the deployment: %w", err)
	}

	call, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(target.Endpoint, "/")+"/deploy", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not address the %s module: %w", spec.Module, err)
	}
	call.Header.Set("Content-Type", "application/json")
	call.Header.Set("Accept", "application/x-ndjson")

	// A deployment is not something to time out on the way out: the module decides how
	// long a rollout takes, and cutting it off here would leave a cluster doing
	// something the core has stopped watching, which is the state nobody can answer a
	// question about.
	client := &http.Client{Timeout: 0}
	response, err := client.Do(call)
	if err != nil {
		log("The %s module did not answer: %v\n", spec.Module, err)
		return fmt.Errorf("the %s module did not answer: %w", spec.Module, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		message := refusalOf(raw, response.Status)
		log("The %s module refused: %s\n", spec.Module, message)
		// Typed, because a refusal is not a failure and the code that records the outcome has
		// to be able to tell them apart. As a plain error the only thing downstream could do
		// was print it, and printing it as a failure says the module broke something when it
		// declined to touch anything.
		return &refused{module: spec.Module, reason: message}
	}

	// The module narrates as it goes, and everything it says goes into the job's log
	// as it arrives rather than at the end. This log is what the pipeline page shows,
	// and a deployment that reports itself only once it has finished is a spinner with
	// words in it.
	//
	// It is published as an event too, so a page that is watching shows the pods coming
	// up instead of waiting to be told that the whole thing is over.
	var (
		failed  string
		arrived int
		// The record the last line named, kept so that a line which names none is
		// still attributable.
		//
		// The module writes the record onto every line it means a page to attribute.
		// The ones that only close a phase are the exception it is easy to forget, and
		// the cost of forgetting is not a missing record but a page that cannot tell
		// which place a line belongs to — which on a project with two places means
		// both cards draw both rollouts. A line of one deployment in the middle of
		// another is about the same record, so the core says which.
		lastRecord map[string]any
	)
	reader := bufio.NewReader(response.Body)
	// Пауза между строками от модуля, для разработки и демонстраций.
	//
	// Настоящий деплой на настоящем кластере занимает столько, сколько занимает, и
	// подгонять его нечем: прогресс, который виден за две секунды, виден плохо, и
	// страница не успевает показать ничего, кроме «всё кончилось». Здесь она
	// включается переменной окружения и по умолчанию равна нулю, то есть в обычной
	// работе её нет вовсе.
	stepDelay := 0 * time.Millisecond
	if raw := os.Getenv("DOGIT_DEPLOY_STEP_DELAY_MS"); raw != "" {
		if ms, err := strconv.Atoi(raw); err == nil && ms > 0 {
			stepDelay = time.Duration(ms) * time.Millisecond
		}
	}

	for {
		line, readErr := reader.ReadBytes('\n')

		if trimmed := strings.TrimSpace(string(line)); trimmed != "" {
			var progress deployProgress
			if json.Unmarshal([]byte(trimmed), &progress) == nil && progress.Message != "" {
				arrived++
				stream := "out"
				if progress.Failed {
					stream = "err"
					failed = progress.Message
				}
				_, _ = s.appendJobOutput(ctx, job, stream, progressLine(progress))

				if progress.Deployment != nil {
					lastRecord = progress.Deployment
				} else if lastRecord != nil {
					progress.Deployment = lastRecord
				}

				// The operation's own event. Separate from the history below because
				// this arrives many times a minute and that arrives once.
				//
				// The module's line, whole. Not the fields this build happens to know
				// about: a module says what is happening — which pods are on which
				// image, how many of each, what it is about to do next — and a core
				// that picks the fields out of it is a core that has to be taught a new
				// field every time a module learns to say something. What belongs here
				// is routing, not editing: who is allowed to hear it, and where it goes.
				s.publishPipeline(ctx, project.ID, nil, models.EventDeployOperation,
					relayOf([]byte(trimmed), job.ID, progress.Deployment, "deploy"))

				if stepDelay > 0 {
					time.Sleep(stepDelay)
				}
			}
		}

		if readErr != nil {
			break
		}
	}

	if failed != "" {
		return fmt.Errorf("%s", failed)
	}
	if arrived == 0 {
		// Silence after a deployment is not a result. It means the stream ended before
		// anything was said, and the cluster may or may not have been touched at all.
		return fmt.Errorf("the %s module said nothing about what it did", spec.Module)
	}

	log("Deployed in %s.\n", time.Since(started).Round(time.Second))
	return nil
}

// shortDigest is an image reference cut down to something a log line can carry.
// refused is a module declining a deployment, with its reason.
//
// A type rather than a sentence, and the reason it has to be one: the message a refusal produces
// is a perfectly good sentence and a terrible discriminator. A caller that has to recognise it
// by reading it is reading a string that was written for a person, and the first person to
// reword it breaks the recognition.
type refused struct {
	module string
	reason string
}

func (r *refused) Error() string {
	return fmt.Sprintf("the %s module refused: %s", r.module, r.reason)
}

// refusedBy reports whether an error is a module's refusal, and why.
func refusedBy(err error) (string, bool) {
	var r *refused
	if errors.As(err, &r) {
		return r.reason, true
	}
	return "", false
}

func shortDigest(image string) string {
	at := strings.Index(image, "@")
	if at < 0 {
		return image
	}
	digest := strings.TrimPrefix(image[at+1:], "sha256:")
	if len(digest) > 12 {
		digest = digest[:12]
	}
	return "sha256:" + digest
}

// deployProgress is one thing the module is doing, as it tells it.
type deployProgress struct {
	Phase   string `json:"phase"`
	Step    int    `json:"step"`
	Of      int    `json:"of"`
	Message string `json:"message"`
	Ready   int    `json:"ready"`
	Desired int    `json:"desired"`
	Failed  bool   `json:"failed"`
	// Finished closes one phase of the deployment. Carried through because a page
	// showing two phases at once has to know which of them is done: without it the
	// arrow stays on a step whose work finished, and nobody can say when it did.
	Finished bool `json:"finished"`
	Done     bool `json:"done"`
	// Previous is what the pods being taken off are running, while a rollout has some to
	// take off. Read here only because it is read by nothing — the line below is relayed
	// whole — and the field is named so that a reader of this struct finds it.
	Previous string `json:"previous,omitempty"`
	// Deployment is the record the module is writing. Carried on every line, not only
	// on the last one: a page that gets it once at the end has to spend the whole
	// rollout with an empty row, which is exactly when somebody is reading it.
	Deployment map[string]any `json:"deployment,omitempty"`
}

// relayOf is the module's own line, as an event, with the two things the core knows and
// the module cannot: which job this is about, and which record it is about — the latter
// carried on every line so that a page listening from the first of them has the image
// and the workload from the first, rather than a race with its own first paint.
func relayOf(line []byte, jobID int64, record map[string]any, kind string) map[string]any {
	payload := map[string]any{}
	if len(line) > 0 {
		// A line that is not an object is the module's own to explain; it is relayed as
		// a message so that a page is not left waiting for a field that never arrives.
		_ = json.Unmarshal(line, &payload)
	}
	if len(payload) == 0 {
		payload["message"] = strings.TrimSpace(string(line))
	}
	payload["job_id"] = jobID
	// When this line happened, in this core's milliseconds.
	//
	// Not for ordering — the stream already arrives in order, and a client that reorders by this
	// would be sorting by when the core read the line rather than by when the work was done. It is
	// there so that a hole is visible: a page that fetched its list and then subscribed can see
	// that the first line it received is older than the moment it subscribed, and knows it missed
	// something instead of drawing a log that starts in the middle and looks whole.
	//
	// Only ever added when the module did not say one, so a module that stamps its own line is not
	// overridden — and a line carrying two clocks is worse than a line carrying one.
	if _, stamped := payload["at"]; !stamped {
		payload["at"] = time.Now().UnixMilli()
	}
	// The module's own record wins. What the core adds is a place to hang it on, for the
	// operations where the module has no record to send — a rollback's own lines carry
	// theirs — and overwriting a record that says which image is going back, with one that
	// says only which place it is going back to, is how a page watched a rollback for a
	// minute with no image to mark.
	if record != nil {
		if _, said := payload["deployment"]; !said {
			payload["deployment"] = record
		}
	}
	// Which operation this is, stamped on whichever record is going out.
	//
	// After the choice above and not before it, because the module's record is the one that
	// travels in a rollback — stamping only the core's own would have named the operation on
	// a map that is then thrown away, and the field would arrive on deployments and never on
	// rollbacks, which is the half that needed it. It is passed in by both callers rather
	// than read off the record for the same reason: the record the core builds for a
	// rollback is also the one that loses.
	if carried, ok := payload["deployment"].(map[string]any); ok && kind != "" {
		if _, said := carried["kind"]; !said {
			carried["kind"] = kind
		}
	}
	return payload
}

// progressLine is one step as it goes into the job's log.
//
// The phase and the counts come first, because a log read afterwards is scanned for
// "did it work", and "rollout: 2 of 3 ready — 1 of 1 updated" answers that where a
// bare sentence does not.
func progressLine(progress deployProgress) string {
	var line strings.Builder

	switch progress.Phase {
	case "prepare", "pre", "pull", "apply", "rollout", "post":
		line.WriteString(progress.Phase + ": ")
	}

	switch {
	case progress.Desired > 0:
		line.WriteString(fmt.Sprintf("%d of %d ready", progress.Ready, progress.Desired))
		if progress.Message != "" {
			line.WriteString(" — ")
		}
	case progress.Of > 0:
		line.WriteString(fmt.Sprintf("%d of %d — ", progress.Step, progress.Of))
	}

	line.WriteString(progress.Message)
	return line.String() + "\n"
}

// prefaceLine is one line of a deployment's record that a job other than this one said.
type prefaceLine struct {
	Phase   string `json:"phase"`
	Message string `json:"message"`
}

// prefaceFor is what this run did to the image before the deployment, as lines under the
// phases the plan gives them.
//
// Read out of the run rather than remembered from watching it: the build is over by the
// time a deployment starts, and the record is written for somebody who was not here. The
// job that built the image and how long it took are both in the run, and the names the
// image was pushed under are what this deployment is about to use.
//
// Nothing when the run built nothing — a deployment of an image from somewhere else has
// no build to account for, and a step with a line that invents one is worse than a step
// the plan left out.
func prefaceFor(ctx context.Context, s *Server, run *store.Pipeline, jobID int64,
	image string, tags []string) []prefaceLine {

	built, from := builtImage(ctx, s, run, jobID)
	if strings.TrimSpace(built) == "" {
		return nil
	}

	builtLine := fmt.Sprintf("built by the %q job", from)
	if jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, run.ID); err == nil {
		for _, job := range jobs {
			if job.Name != from || job.DurationMS <= 0 {
				continue
			}
			builtLine += fmt.Sprintf(" in %s",
				(time.Duration(job.DurationMS) * time.Millisecond).Round(time.Second))
			break
		}
	}
	builtLine += "."

	// The name without its digest: a tag is a name and a digest is not one, and
	// "image@sha256:…:v88.53" is a sentence nobody can read.
	name, _, _ := strings.Cut(image, "@")
	pushed := "pushed"
	switch {
	case name != "" && len(tags) > 0:
		pushed = fmt.Sprintf("pushed as %s:%s", name, strings.Join(tags, ", :"))
	case name != "":
		pushed = fmt.Sprintf("pushed as %s", name)
	}

	return []prefaceLine{
		{Phase: "build", Message: builtLine},
		{Phase: "push", Message: pushed},
	}
}

// imageForDeploy is the image this run produced, resolved to a digest.
//
// A digest rather than a tag, and that is the whole argument for it: a tag can move
// between the build and the rollout, so a rollback that went back to a tag could
// return to a different image than the one that was rolled out from. Asking the
// registry what the tag currently points at pins it to what was actually tested.
// The image is the one the run built, which is not this job's own build: a deploy job
// has no build of its own, so asking it here would find nothing and apply the
// manifests with the placeholder still in them. It comes from whichever job in this
// run produced an image.
func (s *Server) imageForDeploy(ctx context.Context, job *store.Job, run *store.Pipeline,
	log func(string, ...any)) (string, error) {

	image, from := builtImage(ctx, s, run, job.ID)
	if strings.TrimSpace(image) == "" {
		// Said rather than refused, because a repository can deploy something it did
		// not build, and the manifests then apply as they are written. A rollout of
		// whatever the manifest names is a real deployment; silently substituting
		// nothing is not, so the log says which of the two happened.
		if from == "" {
			log("  image:     none — no job in this run built one, so the manifests are " +
				"applied as they are written\n")
			return "", nil
		}
		log("  image:     none — job %q ran but produced no image\n", from)
		return "", nil
	}

	log("  image:     %s (built by %q)\n", image, from)

	// Under the commit's own name when the repository named no image, because the
	// runner pushes under that name always: a short commit is the one tag it can never
	// be without. Resolving by anything else — a tag left pointing somewhere, or the
	// name on its own — answers what the registry holds *now*, which by then may be
	// an image from a later run, and the deployment applies that while reporting it
	// as this one's.
	tag := s.buildTag(ctx, run)
	if tag == "" {
		tag = shortRunSHA(run)
	}

	digest, err := s.resolveImageDigest(ctx, job, image, tag)
	if err != nil {
		// A tag that cannot be resolved is reported and the tag is used, because the
		// alternative is refusing to deploy something that is perfectly deployable.
		// What was deployed is said plainly, so nobody believes it was pinned.
		log("  image:     %s (could not be pinned to a digest: %v)\n", image, err)
		return image, nil
	}
	return digest, nil
}

// deployedImageTags are the names this image was published under.
//
// Asked of the repository at the commit being deployed, because that is where the names
// live. A run started from a tag knows only one of them — the tag it was started by —
// while a commit can carry a dozen, and a row in the operations list showing no tag
// beside a release somebody tagged is a row that cannot be matched to that release.
//
// Read from git, and at the commit rather than at the branch, so these are the names
// the image was actually pushed under: a tag pointing anywhere else describes a
// different commit and has no business on this row.
//
// The commit is not among them. It is kept in its own column because a list mixing
// "v1.01" and "0681599" reads as two releases when it is one release and where it came
// from.
func (s *Server) deployedImageTags(ctx context.Context, repoDir string, run *store.Pipeline) []string {
	names := []string{}
	seen := map[string]bool{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}

	// The tag this run was triggered by, when it is one of this commit's own tags,
	// because that is the name whoever started this run had in mind.
	add(strings.TrimSpace(run.Variables["CI_COMMIT_TAG"]))

	if repoDir != "" && run.SHA != "" {
		if out, err := s.git.Run(ctx, repoDir, nil, "tag", "--points-at", run.SHA); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				add(line)
			}
		}
		// A repository that cannot be asked is a repository with no tags to report, and
		// that is not worth refusing a deployment over.
	}
	return names
}

// shortRunSHA is the commit, abbreviated the way a person writes it.
func shortRunSHA(run *store.Pipeline) string {
	if short := strings.TrimSpace(run.Variables["CI_COMMIT_SHORT_SHA"]); short != "" {
		return short
	}
	if len(run.SHA) > 7 {
		return run.SHA[:7]
	}
	return run.SHA
}

// deploySpecForJob is the place this job was created to deploy to.
//
// The job carries its own copy of the place, written when the rules were applied. The
// configuration is only a fallback for a run filed before places were named separately,
// and never a second answer to a question the job has already answered.
func deploySpecForJob(job *store.Job, config *pipeline.Config) pipeline.DeploySpec {
	if len(job.Deploy) > 0 {
		var spec pipeline.DeploySpec
		encoded, err := json.Marshal(job.Deploy)
		if err == nil && json.Unmarshal(encoded, &spec) == nil && spec.Present {
			return spec
		}
	}
	for _, spec := range config.Deploys {
		if spec.Name != "" && job.Name == "deploy:"+spec.Name {
			return spec
		}
	}
	return config.Deploy
}

// deployModuleForPlace is the module that does this kind of deployment to this place.
//
// The place is what tells two modules of one kind apart, and it is already in everything that
// asks: a job carries both a target kind and the cluster it is going to, and a cluster is
// configured on exactly one module. So the question was never really "which module of kind
// deploy:kubernetes" — it was "which module has jabjab-deploy2", and the kind was only ever a
// filter on the answer.
//
// Where there is no place to go by, the only module of that kind is the answer, because there is
// nothing to choose between. Where there is more than one and no place, this refuses rather than
// takes the oldest: the choice decides which cluster a deployment lands on, and the core does not
// know enough to make it. The refusal names the modules, so the fix is a word rather than a
// reading of somebody else's configuration.
func (s *Server) deployModuleForPlace(ctx context.Context, project *models.Project,
	place, target string) (*models.Integration, error) {

	kind := fmt.Sprintf(deployTargetKind, strings.TrimSpace(target))
	place = strings.TrimSpace(place)

	installed, err := s.store.Integrations().ByKindAll(ctx, kind)
	if err != nil {
		return nil, err
	}
	if len(installed) == 0 {
		// A 404 and not a plain error, because it is one.
		//
		// Left as it was, this came back as "an unexpected error occurred" with a 500,
		// which says the server broke. Nothing broke: the caller named something this
		// instance has no deploy module for. It is also the most likely mistake here,
		// because a person reads "target" as the cluster — the name they can see in the
		// places list — while this asks for the module's kind, which is a different word
		// entirely and never appears in the interface. So the answer says what it wanted
		// and what is actually installed, instead of leaving that to the server's log.
		return nil, errNotFoundf(
			"no deploy module of kind %q is installed; this instance has %s",
			kind, s.installedDeployKinds(ctx))
	}

	usable := make([]*models.Integration, 0, len(installed))
	for _, candidate := range installed {
		if candidate.Enabled {
			usable = append(usable, candidate)
		}
	}
	if len(usable) == 0 {
		return nil, errForbiddenf("the %q module has been forbidden on this instance", kind)
	}

	if place != "" {
		for _, candidate := range usable {
			if s.hasPlace(ctx, project, candidate, place) {
				return candidate, nil
			}
		}
		// Said plainly rather than falling through to "no module": the module is there,
		// this cluster is not on it, and those are different mistakes with different fixes.
		return nil, errNotFoundf(
			"cluster %q is not configured on any installed %q module (they are %s)",
			place, kind, s.moduleNames(usable))
	}

	if len(usable) == 1 {
		return usable[0], nil
	}
	return nil, errBadRequestf(
		"this instance has %d %q modules (%s) and nothing says which one to use, because no "+
			"cluster was named. Name the cluster, or take the module out of the instance.",
		len(usable), kind, s.moduleNames(usable))
}

// moduleNames is how a list of modules is shown to somebody who has to pick one.
func (s *Server) moduleNames(modules []*models.Integration) string {
	names := make([]string, 0, len(modules))
	for _, module := range modules {
		names = append(names, module.Name)
	}
	return strings.Join(names, ", ")
}

// hasPlace says whether a cluster of that name is configured on a module.
//
// Read at the project's own level, because that is where somebody would have written it: a
// cluster inherited from the group is not the same cluster as one written here, and treating them
// as the same would send a deployment somewhere the project never configured.
func (s *Server) hasPlace(ctx context.Context, project *models.Project,
	module *models.Integration, place string) bool {

	if project == nil {
		return false
	}
	id := project.ID
	settings, err := s.store.Integrations().SettingsFor(ctx, module.ID,
		project.GroupID, &id, module.Capabilities.Settings)
	if err != nil {
		s.log.Warn("could not read the places of a module",
			"module", module.Name, "place", place, "error", err)
		return false
	}
	// placeField rather than a loop of our own: it already knows how a place is named in a
	// settings row, and a second way of reading that is a second thing to get wrong when the
	// format moves.
	_, said := placeField(placesOf(settings), place, "name")
	return said
}

// installedDeployKinds are the deploy modules this instance actually has, said the way a
// caller can act on: as the kinds to pass, not as a count.
//
// A count would be enough to tell that the answer is wrong and useless for working out
// what to pass instead, and somebody who mistook a cluster for a kind has exactly that
// question.
func (s *Server) installedDeployKinds(ctx context.Context) string {
	installed, err := s.store.Integrations().List(ctx)
	if err != nil {
		return "none that could be read"
	}

	kinds := []string{}
	for _, one := range installed {
		if strings.HasPrefix(one.Kind, "deploy:") && one.Enabled {
			kinds = append(kinds, one.Kind)
		}
	}
	if len(kinds) == 0 {
		return "no deploy modules at all"
	}
	return strings.Join(kinds, " and ")
}

// readDeployManifests reads the repository's manifests at the commit.
//
// Read at the commit rather than from a branch, for the same reason the pipeline's
// configuration is: a deployment is a claim about code, and applying today's
// manifests to yesterday's image is not the deployment anybody described.
// deployStepNames is a step of a deployment, as the page is told to draw it.
//
// The label is the core's because the core is the one assembling the list, and a
// client that invented its own labels would show words the module never said.
type deployStepName struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// deployStepsFor is the plan for one particular deployment.
//
// Only what this deployment will actually do. A deployment with no pre-step jobs has
// no pre-step row, and one that deploys an image somebody else built has no build or
// push row — a list drawn from a fixed set of phases is a list of things that might
// happen, and a page full of blue dots that never turn is a page nobody can read.
func deployStepsFor(built string, manifests, pre, post int) []deployStepName {
	steps := make([]deployStepName, 0, 9)

	if built != "" {
		steps = append(steps,
			deployStepName{"build", "Build the image"},
			deployStepName{"push", "Push the image"})
	}

	steps = append(steps, deployStepName{"prepare", "Read the manifests"})
	if pre > 0 {
		steps = append(steps, deployStepName{"pre", "Run the pre-step jobs"})
	}
	steps = append(steps,
		deployStepName{"pull", "Give the cluster a way to pull"},
		deployStepName{"apply", "Apply to the cluster"},
		deployStepName{"rollout", "Bring the new pods up"},
		// Only a deployment that rolls something can have something to retire. A step
		// for retiring old pods on a workload there are none of would be a row that
		// never changes; if it turns out there was nothing to retire, the page draws
		// it as skipped, which is what happened.
		deployStepName{"retire", "Retire the old pods"})
	if post > 0 {
		steps = append(steps, deployStepName{"post", "Run the post-step jobs"})
	}

	return steps
}

// refusalOf is what a module said in refusing, in words.
//
// A module's refusal is an object with the reason inside it, the same shape its
// successes arrive in; printed as it comes, a refusal read "refused: {\"error\":{\"message\":\"another
// deployment is under way\"}}", which is the one line of a run somebody will read
// twice. Taken out of the envelope here, once, for every caller.
func refusalOf(raw []byte, status string) string {
	var refused struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &refused) == nil && refused.Error.Message != "" {
		return refused.Error.Message
	}
	if text := strings.TrimSpace(string(raw)); text != "" {
		return text
	}
	return status
}

// deployStepsForRevert is what putting a version back goes through.
//
// Its own list, and not the deployment's with some rows left out by hand, because a
// rollback is not a deployment with fewer steps: nothing is built, nothing is pushed,
// there are no manifests to read and no credential to write, and there is no image
// anywhere in it that is new. Its whole story is one change to a workload and the pods
// that change brings up.
//
// Drawing the deployment's list beside a rollback's log is a page that claims a build
// happened while somebody is watching the pods of an image that was built days ago, and
// the rows that never turn are read as steps that failed.
func deployStepsForRevert() []deployStepName {
	return []deployStepName{
		{"apply", "Put the image back on the workload"},
		{"rollout", "Bring the pods up on it"},
		{"retire", "Retire the old pods"},
	}
}

// buildJob names the job that built the image this run deploys, or nothing.
func buildJob(ctx context.Context, s *Server, run *store.Pipeline, skipJobID int64) string {
	_, built := builtImage(ctx, s, run, skipJobID)
	return built
}

// publishDeployPlan tells the instance what this run's deployment will involve.
//
// Sent with the run rather than with the deployment, because the build and push steps
// happen in a job that finishes before the deployment starts: a plan sent from there
// arrives after the first two steps have already been reported, and the page draws
// them from the events' own sentences until it does.
func (s *Server) publishDeployPlan(ctx context.Context, project *models.Project,
	config *pipeline.Config, run *store.Pipeline, jobs []store.Job) {

	if config == nil || !config.Deploy.Present {
		return
	}

	// The deployment's own job, so that a client can tell one run's plan from
	// another's. A run with a build in it is a run that will have an image.
	deployJobID := int64(0)
	builds := false
	for _, job := range jobs {
		if job.Deploy != nil {
			deployJobID = job.ID
		}
		if len(job.Build) > 0 {
			builds = true
		}
	}

	built := ""
	if builds {
		built = "the build job"
	}

	s.publishPipeline(ctx, project.ID, nil, models.EventDeployPlan, map[string]any{
		"job_id":       deployJobID,
		"pipeline_iid": run.IID,
		"steps": deployStepsFor(built, len(config.Deploy.Manifests),
			len(config.Deploy.Pre), len(config.Deploy.Post)),
	})
}

func (s *Server) readDeployManifests(ctx context.Context, repoDir, sha string,
	paths []string, log func(string, ...any)) ([]deployManifest, error) {

	manifests := make([]deployManifest, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		body, apiVersion, kind, name, err := s.readManifest(ctx, repoDir, sha, path)
		if err != nil {
			return nil, err
		}
		log("  manifest:  %s (%s/%s)\n", path, kind, name)
		manifests = append(manifests, deployManifest{
			APIVersion: apiVersion, Kind: kind, Name: name, Path: path, Body: body,
		})
	}
	return manifests, nil
}

// readDeploySteps reads the pre- and post-jobs, given either as a manifest path or
// as a command in an image.
func (s *Server) readDeploySteps(ctx context.Context, repoDir, sha string,
	steps []pipeline.DeployStep, log func(string, ...any)) ([]deployStep, error) {

	out := make([]deployStep, 0, len(steps))
	for _, step := range steps {
		if path := strings.TrimSpace(step.Manifest); path != "" {
			body, apiVersion, kind, name, err := s.readManifest(ctx, repoDir, sha, path)
			if err != nil {
				return nil, err
			}
			log("  %s: %s (%s/%s)\n", phaseWord(len(out)), path, kind, name)
			out = append(out, deployStep{APIVersion: apiVersion, Kind: kind, Name: name, Body: body})
			continue
		}

		image := strings.TrimSpace(step.Image)
		if image == "" {
			return nil, fmt.Errorf(
				"a deploy step runs a command but names no image to run it from")
		}
		log("  %s: %s %s\n", phaseWord(len(out)), image, strings.Join(step.Command, " "))
		out = append(out, deployStep{Image: image, Command: step.Command, Name: stepImageName(image)})
	}
	return out, nil
}

// phaseWord is how a step is announced before the module has said which phase it is
// in. "step" rather than "pre" or "post" on purpose: the core sends both lists and
// the module decides the order, so claiming a phase here would be a guess.
func phaseWord(int) string { return "step" }

// stepImageName is the Job name for a command step.
//
// Derived from the image so the same command twice in one deployment does not collide
// in the cluster: two Jobs with one name means the second silently does nothing.
func stepImageName(image string) string {
	name := image
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	if index := strings.IndexAny(name, ":@"); index > 0 {
		name = name[:index]
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '-'
		}
	}, name)
	name = strings.Trim(name, "-")
	if name == "" {
		return "deploy-step"
	}
	if len(name) > 40 {
		name = name[:40]
	}
	return "dogit-" + strings.Trim(name, "-")
}

// deployTimeoutSeconds reads the repository's timeout, in seconds.
//
// Empty means the target's own default, which is why zero is not sent: the module
// has a default for exactly this case, and sending zero would be an instruction to
// give up immediately.
func deployTimeoutSeconds(Timeout string) int {
	timeout := strings.TrimSpace(Timeout)
	if timeout == "" {
		return 0
	}
	parsed, err := time.ParseDuration(timeout)
	if err != nil || parsed <= 0 {
		return 0
	}
	return int(parsed.Seconds())
}

// readManifest reads one manifest from the repository and says what it is.
//
// The kind and name are read out of the file rather than configured, so the log line
// describes what is actually going to be applied. A log that said "applying
// k8s/deployment.yaml" and the cluster disagreed would leave somebody reading the
// wrong file.
func (s *Server) readManifest(ctx context.Context, repoDir, sha, path string) (
	body string, apiVersion, kind, name string, err error) {

	contents, _, _, err := s.git.CatFile(ctx, repoDir, sha, path)
	if err != nil {
		return "", "", "", "", fmt.Errorf("%s is not in this repository at %s", path, shortSHA(sha))
	}

	// Only enough of a YAML document to answer "what is this". A manifest can use
	// anchors and multi-document streams, and pulling in a full YAML parser to read
	// three header fields would be a dependency the core does not otherwise have.
	apiVersion, kind, name = manifestIdentity(string(contents))
	if kind == "" {
		return "", "", "", "", fmt.Errorf(
			"%s does not say what it is: a manifest has to begin with apiVersion and kind", path)
	}
	return string(contents), apiVersion, kind, name, nil
}

// manifestIdentity is the apiVersion, kind, namespace and name at the top of a
// manifest.
//
// Handled rather than parsed with a YAML library, because the only three facts needed
// are the kind and the name — for the log line and to know what to wait for — and a
// real parser for that would be a dependency the core otherwise does not have. It does
// read the name from inside the metadata block, which is where it always is: reading
// only the unindented lines would find apiVersion and kind and no name at all, and a
// deployment applied to a nameless object is not a deployment.
func manifestIdentity(body string) (apiVersion, kind, name string) {
	inMetadata := false

	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(trimmed) == "" || strings.HasPrefix(strings.TrimSpace(trimmed), "#") {
			continue
		}
		if trimmed == "---" {
			// The document has ended; anything after this is somebody else's.
			if name != "" {
				break
			}
			inMetadata = false
			continue
		}

		indented := strings.TrimSpace(trimmed) != trimmed
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)

		if !indented {
			inMetadata = key == "metadata"
			switch key {
			case "apiVersion":
				if apiVersion == "" {
					apiVersion = value
				}
			case "kind":
				if kind == "" {
					kind = value
				}
			}
			continue
		}

		if inMetadata && key == "name" && value != "" && name == "" {
			name = value
			if apiVersion != "" && kind != "" {
				return apiVersion, kind, name
			}
		}
	}
	return apiVersion, kind, name
}

// resolveImageDigest asks the registry what a tag currently points at.
//
// The registry module is the one that knows: the core has no idea a registry exists,
// and the answer is only correct because somebody who does know went and asked the
// storage API. A failure is returned rather than guessed at — an unpinned image is
// visible on the log line that follows, which is enough for a person to act on.
func (s *Server) resolveImageDigest(ctx context.Context, job *store.Job, image, tag string) (string, error) {
	// The repository is what the build recorded and the tag is what it was pushed as,
	// kept apart on purpose. Handed over as one string, the registry can only see
	// everything after the last slash, which for "registry:5000/home-store/www" is
	// "www" — not a repository, and not this project's either.
	repository, fromName := splitImage(image)
	if tag == "" {
		tag = fromName
	}

	// The image names the registry it lives at, and that is what picks the module: the credential
	// has to be one the registry holding the image will accept, and on an instance with several
	// of them that is not whichever is oldest.
	registry, _, err := s.registryServing(ctx, imageHost(image))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", fmt.Errorf(
				"no registry on this instance publishes %s, so an image cannot be pinned", imageHost(image))
		}
		return "", err
	}

	// The credential is the project's, minted for a pull and nothing else, and it
	// belongs to the builder account rather than to whoever pressed the button — the
	// same reasoning as the push credential, for the same reason: this happens at
	// three in the morning and acts for the project, not for a session.
	token, err := s.resolveToken(ctx, registry, job.ProjectPath)
	if err != nil {
		return "", err
	}

	body, err := json.Marshal(map[string]string{
		"project": job.ProjectPath,
		"image":   repository,
		"tag":     tag,
		"token":   token,
	})
	if err != nil {
		return "", err
	}

	// Over the channel, not over HTTP: the module opens the connection, so a registry the
	// core cannot dial — behind NAT, on a host it has no route to — can still be asked. The
	// question is the same one the HTTP endpoint answers, and both go through the same code
	// in the module, so the two cannot answer differently.
	answer, err := s.moduleChannel().Call(ctx, registry, Decision{
		Kind:    modulechan.ResolveImage,
		Payload: body,
	}, 30*time.Second)
	if refused := moduleRefusalOf(registry, "what that tag points at", answer, err); refused != nil {
		return "", refused
	}

	var found struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(answer, &found); err != nil {
		return "", fmt.Errorf("could not read what the %s module said: %w", registry.Kind, err)
	}
	if !strings.HasPrefix(found.Digest, "sha256:") {
		return "", fmt.Errorf("the registry did not answer with a digest")
	}

	return repository + "@" + found.Digest, nil
}

// resolveToken is a short-lived pull credential for this project's images.
func (s *Server) resolveToken(ctx context.Context, registry *models.Integration,
	projectPath string) (string, error) {

	builder, err := s.serviceUser(ctx)
	if err != nil {
		return "", err
	}

	project, err := s.store.Projects().ByPath(ctx, projectPath)
	if err != nil {
		return "", err
	}

	token, _, err := s.mintModuleToken(ctx, builder, registry, &project.ID,
		[]string{models.ScopeRegistryPull}, 10*time.Minute)
	if err != nil {
		return "", err
	}
	return token, nil
}

// builtImage is the image this run produced, and the job that produced it.
//
// The run's jobs rather than the deploy's own, and the job name with it: "the image
// came from nowhere" and "the image came from the build job" are the same blank on a
// log line and completely different things when a rollout misbehaves.
func builtImage(ctx context.Context, s *Server, run *store.Pipeline, skipJobID int64) (string, string) {
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil {
		return "", ""
	}

	fallback := ""
	fallbackFrom := ""
	for _, job := range jobs {
		if job.ID == skipJobID || job.Build == nil {
			continue
		}
		image, _ := job.Build["image"].(string)
		image = strings.TrimSpace(image)
		if image == "" {
			continue
		}

		// A job that runs a build is the better answer than one that merely mentions
		// an image, so a second candidate is kept rather than taking the first.
		if strings.TrimSpace(asString(job.Build["dockerfile"])) != "" ||
			strings.TrimSpace(asString(job.Build["context"])) != "" {
			return image, job.Name
		}
		if fallback == "" {
			fallback, fallbackFrom = image, job.Name
		}
	}
	return fallback, fallbackFrom
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

// splitImage is the repository and the tag of an image name.
//
// The host is kept in the repository: it is what a cluster pulls from, and it is part
// of the name rather than something to strip off. A colon after the last slash is a
// tag, and a colon before one is a port — which is the whole difference between a
// registry on port 5000 and an image called "www:dev".
func splitImage(image string) (repository, tag string) {
	repository = strings.TrimSpace(image)
	if index := strings.LastIndex(repository, "@"); index > 0 {
		repository = repository[:index]
	}

	colon := strings.LastIndex(repository, ":")
	if colon > strings.LastIndex(repository, "/") {
		tag = repository[colon+1:]
		repository = repository[:colon]
	}
	return repository, tag
}

// imageHost is the registry an image name points at, or "" when the name carries none.
//
// Docker's own rule: the first path segment is a host only if it looks like one, so
// "mygroup/myproject" is a project on the default registry and "registry.example.com/myproject"
// is a project on a named one. Reading it any other way turns every unqualified name into a host,
// and then every unqualified image asks this instance for a registry that does not exist.
//
// The tag and the digest are stripped first, since "registry:5000/a/b:tag" ends in neither the
// repository nor the host.
func imageHost(image string) string {
	repository, _ := splitImage(image)
	first, _, _ := strings.Cut(repository, "/")
	if !strings.ContainsAny(first, ".:") {
		return ""
	}
	return first
}

// buildTag is the tag the run pushed, which is not in the image name.
//
// The core records the repository and the tag separately — the tag is what a pipeline
// asked for and the repository is what the registry's naming rule produced — so the
// tag has to come from where it was recorded rather than from being parsed back out
// of a name that does not carry it.
func (s *Server) buildTag(ctx context.Context, run *store.Pipeline) string {
	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, run.ID)
	if err != nil {
		return ""
	}
	for _, one := range jobs {
		if one.Build == nil {
			continue
		}
		if tag, _ := one.Build["tag"].(string); strings.TrimSpace(tag) != "" {
			return strings.TrimSpace(tag)
		}
	}
	return ""
}

// registryHostFrom is the host and port out of an image name.
//
// Only that, because it is what an auth entry is keyed by: a credential filed under
// "registry:5000/group/project" is a credential no client will ever look up, and the
// failure is a pod that cannot pull with nothing in the log to say why.
func registryHostFrom(image string) string {
	host := image
	if index := strings.Index(host, "/"); index >= 0 {
		host = host[:index]
	}
	return registryHost(host)
}
