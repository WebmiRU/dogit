package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
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
}

// registryCredential is what a cluster needs to pull this project's images.
type registryCredential struct {
	// Address is the registry host:port, which has to match the image name exactly.
	Address string `json:"address"`
	Token   string `json:"token"`
	// SecretName is what the module should call the Secret it writes.
	SecretName string `json:"secret_name"`
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

	spec := config.Deploy
	// The newline is in the format string, not added here: appendJobOutput marks each
	// line it is given, and a line that has already ended gets an empty one after it,
	// which reads as though the deployment paused between every step.
	log := func(format string, args ...any) {
		_, _ = s.appendJobOutput(ctx, job, "out", fmt.Sprintf(format, args...))
	}

	started := time.Now()

	log("Deploying to %s\n", spec.Target)
	log("  target:    %s\n", spec.Target)
	log("  cluster:   %s\n", spec.Cluster)
	if spec.Namespace != "" {
		log("  namespace: %s\n", spec.Namespace)
	}

	// The image this run built. A deployment of something else is a different thing
	// and is refused here rather than sent on to be applied as though it were what
	// the run produced: the whole point of rolling out by digest is that the thing
	// being deployed is the thing that was tested.
	image, err := s.imageForDeploy(ctx, job, pipelineRun, log)
	if err != nil {
		return err
	}
	log("  image:     %s\n", image)

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

	request := deployRequest{
		Project:        project.Path,
		Target:         spec.Target,
		Cluster:        spec.Cluster,
		Namespace:      spec.Namespace,
		Image:          image,
		Placeholder:    imagePlaceholder,
		Manifests:      manifests,
		Pre:            pre,
		Post:           post,
		WaitForRollout: spec.Rollout,
		TimeoutSeconds: deployTimeoutSeconds(spec.Timeout),
		Ref:            pipelineRun.Ref,
		Sha:            pipelineRun.SHA,
	}
	// The credential the cluster pulls with.
	//
	// The registry is private by default, so a cluster given nothing to pull with will
	// sit at ImagePullBackOff and the deployment will fail on a rollout timeout with no
	// cause in it. Minted per project, for a pull and nothing else, and it goes to the
	// module and nowhere else.
	credential, err := s.registryCredential(ctx, job, image)
	if err != nil {
		log("  registry:   %v\n", err)
	} else if credential != nil {
		log("  registry:   %s (the cluster will pull with a credential of ours)\n", credential.Address)
	}
	request.Registry = credential

	request.Expect.Secrets = spec.Expect.Secrets
	request.Expect.ConfigMaps = spec.Expect.ConfigMaps

	target, err := s.deployModule(ctx, spec.Target)
	if err != nil {
		return err
	}

	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("could not describe the deployment: %w", err)
	}

	call, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(target.Endpoint, "/")+"/deploy", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("could not address the %s module: %w", spec.Target, err)
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
		log("The %s module did not answer: %v\n", spec.Target, err)
		return fmt.Errorf("the %s module did not answer: %w", spec.Target, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
		message := strings.TrimSpace(string(raw))
		log("The %s module refused: %s\n", spec.Target, message)
		return fmt.Errorf("the %s module refused: %s", spec.Target, message)
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
	)
	reader := bufio.NewReader(response.Body)
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

				// The operation's own event. Separate from the history below because
				// this arrives many times a minute and that arrives once.
				s.publishPipeline(ctx, project.ID, nil, models.EventDeployOperation, map[string]any{
					"job_id":  job.ID,
					"phase":   progress.Phase,
					"message": progress.Message,
					"ready":   progress.Ready,
					"desired": progress.Desired,
					"step":    progress.Step,
					"of":      progress.Of,
				})
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
		return fmt.Errorf("the %s module said nothing about what it did", spec.Target)
	}

	log("Deployed in %s.\n", time.Since(started).Round(time.Second))
	return nil
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
	Done    bool   `json:"done"`
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

	digest, err := s.resolveImageDigest(ctx, job, image, s.buildTag(ctx, run))
	if err != nil {
		// A tag that cannot be resolved is reported and the tag is used, because the
		// alternative is refusing to deploy something that is perfectly deployable.
		// What was deployed is said plainly, so nobody believes it was pinned.
		log("  image:     %s (could not be pinned to a digest: %v)\n", image, err)
		return image, nil
	}
	return digest, nil
}

// deployModule is the module that does this kind of deployment.
func (s *Server) deployModule(ctx context.Context, target string) (*models.Integration, error) {
	kind := fmt.Sprintf(deployTargetKind, target)
	module, err := s.store.Integrations().ByKind(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf(
			"no module of kind %q is installed, so %q cannot be deployed to; "+
				"this instance has nothing that deploys to it", kind, target)
	}
	if !module.Enabled {
		return nil, fmt.Errorf("the %q module has been forbidden on this instance", kind)
	}
	return module, nil
}

// readDeployManifests reads the repository's manifests at the commit.
//
// Read at the commit rather than from a branch, for the same reason the pipeline's
// configuration is: a deployment is a claim about code, and applying today's
// manifests to yesterday's image is not the deployment anybody described.
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

	registry, err := s.store.Integrations().ByKind(ctx, registryKind)
	if err != nil {
		return "", fmt.Errorf("no registry is installed, so an image cannot be pinned")
	}

	// The credential is the project's, minted for a pull and nothing else, and it
	// belongs to the builder account rather than to whoever pressed the button — the
	// same reasoning as the push credential, for the same reason: this happens at
	// three in the morning and acts for the project, not for a session.
	token, err := s.resolveToken(ctx, registry, job)
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

	call, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(registry.Endpoint, "/")+"/resolve", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	call.Header.Set("Content-Type", "application/json")

	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(call)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return "", fmt.Errorf("the registry said %s: %s",
			response.Status, strings.TrimSpace(string(raw)))
	}

	var answer struct {
		Digest string `json:"digest"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", err
	}
	if !strings.HasPrefix(answer.Digest, "sha256:") {
		return "", fmt.Errorf("the registry did not answer with a digest")
	}

	return repository + "@" + answer.Digest, nil
}

// resolveToken is a short-lived pull credential for this project's images.
func (s *Server) resolveToken(ctx context.Context, registry *models.Integration,
	job *store.Job) (string, error) {

	builder, err := s.serviceUser(ctx)
	if err != nil {
		return "", err
	}

	project, err := s.store.Projects().ByPath(ctx, job.ProjectPath)
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

// registryCredential is what this project's cluster pulls its images with, or nil
// when there is nothing to say.
//
// Nil rather than an error for a missing registry: a deployment of an image from
// somewhere else is a real thing somebody does, and refusing it would be dogit
// deciding that images come from dogit.
// The image is passed in rather than read off the job, because a deploy job builds
// nothing: its own build is empty, and a credential asked for from it is a credential
// for nothing at all — which is how a private registry ends up being pulled from with
// no way in and a rollout that times out with no cause.
func (s *Server) registryCredential(ctx context.Context, job *store.Job, image string) (*registryCredential, error) {
	image = strings.TrimSpace(image)
	if image == "" {
		return nil, nil
	}

	registry, err := s.store.Integrations().ByKind(ctx, registryKind)
	if err != nil {
		return nil, fmt.Errorf("no registry is installed on this instance")
	}

	repository, _ := splitImage(image)
	if repository == "" {
		return nil, nil
	}

	token, err := s.resolveToken(ctx, registry, job)
	if err != nil {
		return nil, err
	}

	return &registryCredential{
		Address:    registryHostFrom(repository),
		Token:      token,
		SecretName: "dogit-registry",
	}, nil
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
