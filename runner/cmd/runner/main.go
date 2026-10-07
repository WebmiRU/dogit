// The runner: registers with a dogit instance, reports what it can see, and — once it can
// actually carry work out — asks for jobs and runs them.
//
// What it does *not* do is ask for jobs before it can run them. A claim takes a job out of
// the queue and marks it as this machine's, and a runner that then has no way to run it
// leaves a job at "running" for ever, on a machine that is not running it. That is the worst
// state a queue can be in: the page says a build is in progress and the build has not started.
// So the claim loop is here and switched off, and it gets switched on in the same commit
// that can finish a job.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ewolf/runner/internal/builder"
	"github.com/ewolf/runner/internal/core"
	"github.com/ewolf/runner/internal/jobs"
	"github.com/ewolf/runner/internal/measure"
	"github.com/ewolf/runner/internal/settings"
)

// The kind this runner registers under. A different word from `runner:docker` on purpose:
// two runners that answer to the same kind are one runner as far as the core is concerned,
// and this one cannot execute a job yet, so a shared kind would have it stealing work from a
// runner that can.
const runnerKind = "runner:buildkit"

// The account machines act as. The core names its registry credential to this, so that a push
// at three in the morning is attributed to a machine rather than to whoever pressed the button.
const builderName = "builder"

// The job field this runner understands. Set from -claim.
var mayClaim atomic.Bool

// The core's own answer to the last claim, kept because a runner holding a queue is a claim
// about somebody else's queue and the two must not be confused.
var (
	queueDepth atomic.Int64
	queueKnown atomic.Bool
	lastWorkAt atomic.Int64
)

var startedAt = time.Now()

// runnerSlots is this runner's capacity, read by the heartbeat to say how busy it is. A plain
// variable because it is set once, before any goroutine exists that could read it, and
// because len() of a channel is the answer already — counting the same slots separately would
// be a second answer that can disagree with the first.
var runnerSlots chan struct{}

type config struct {
	coreURL      string
	instanceFile string
	endpoint     string
	name         string
	buildkit     string
	buildkitHost string
	buildkitCA   string
	buildkitCert string
	buildkitKey  string
	serverName   string
	concurrency  int
	jobLimit     time.Duration
	poll         time.Duration
	workspace    string
	claim        bool
}

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", env("DOGIT_CORE_URL", "http://app:8080"),
		"the dogit core to talk to")
	flag.StringVar(&cfg.instanceFile, "registration-token-file", env("DOGIT_REGISTRATION_TOKEN_FILE", ""),
		"a file holding the instance's registration token, which is how a module introduces "+
			"itself. A file and not the token itself, because an environment variable cannot "+
			"be taken back once set: it stays in /proc/1/environ for the life of the process, "+
			"and a job script running in this container can read that")
	flag.StringVar(&cfg.endpoint, "endpoint", env("DOGIT_ENDPOINT", "http://runner:8092"),
		"the address the core and an operator will reach this runner at")
	flag.StringVar(&cfg.name, "name", env("DOGIT_RUNNER_NAME", "buildkit"),
		"what this runner calls itself when it asks for work")
	flag.StringVar(&cfg.buildkit, "buildkit", env("DOGIT_BUILDKIT_ADDR", "tcp://127.0.0.1:1234"),
		"the builder. Loopback, because it is in this pod: the runner and the daemon share a network namespace, and so does every RUN step the daemon executes — which is why the listener is still asked for a client certificate.")
	flag.StringVar(&cfg.buildkitHost, "buildkit-host", env("DOGIT_BUILDKIT_HOST", "127.0.0.1:1234"),
		"the builder's host:port, for the reachability check")
	flag.StringVar(&cfg.buildkitCA, "buildkit-ca", env("DOGIT_BUILDKIT_CA", "/certs/ca.pem"),
		"the authority that vouches for the builder")
	flag.StringVar(&cfg.buildkitCert, "buildkit-cert", env("DOGIT_BUILDKIT_CERT", "/certs/client.crt"),
		"this runner's certificate, presented to the builder")
	flag.StringVar(&cfg.buildkitKey, "buildkit-key", env("DOGIT_BUILDKIT_KEY", "/certs/client.key"),
		"the key for that certificate")
	flag.StringVar(&cfg.serverName, "buildkit-server-name", env("DOGIT_BUILDKIT_SERVER_NAME", ""),
		"the name in the builder's certificate, when it is not simply the address it is reached at")
	flag.StringVar(&cfg.workspace, "workspace", env("DOGIT_RUNNER_WORKSPACE", "/data/work"),
		"where checkouts live")
	flag.IntVar(&cfg.concurrency, "concurrency", envInt("DOGIT_RUNNER_CONCURRENCY", 1),
		"how many jobs at once")
	flag.DurationVar(&cfg.jobLimit, "job-timeout", envDuration("DOGIT_RUNNER_JOB_TIMEOUT", time.Hour),
		"how long one job may run before it is stopped")
	flag.DurationVar(&cfg.poll, "poll", envDuration("DOGIT_RUNNER_POLL", 3*time.Second),
		"how often to ask for work")
	flag.BoolVar(&cfg.claim, "claim", envBool("DOGIT_RUNNER_CLAIM", false),
		"ask for work. Off by default: a claim that cannot be finished strands the job.")

	// A one-shot build, for proving the builder path without the job loop.
	//
	// Here because the loop cannot be used for that: claiming is off until there is an
	// executor, and turning it on to test a build would take a job out of the queue that
	// nothing here can finish. This is how the builder was tested before the runner existed
	// and it is how it will be tested after the job loop grows a shape — a mode that reaches
	// the builder without reaching into the queue.
	buildOnce := flag.Bool("build-once", false,
		"build one image from -build-context and print the digest, then exit")
	buildContext := flag.String("build-context", env("DOGIT_BUILD_CONTEXT", ""),
		"the checkout to build from, for -build-once")
	buildDockerfile := flag.String("build-dockerfile", env("DOGIT_BUILD_DOCKERFILE", "Dockerfile"),
		"the Dockerfile inside that checkout, for -build-once")
	buildImage := flag.String("build-image", env("DOGIT_BUILD_IMAGE", ""),
		"where to put the result, for -build-once")
	buildTarget := flag.String("build-target", env("DOGIT_BUILD_TARGET", ""),
		"a stage to stop at, for -build-once")
	buildPush := flag.Bool("build-push", envBool("DOGIT_BUILD_PUSH", false),
		"push the result, for -build-once")
	buildRegistry := flag.String("build-registry", env("DOGIT_BUILD_REGISTRY", ""),
		"registry host the credential below is for, for -build-once")
	buildUser := flag.String("build-user", env("DOGIT_BUILD_USER", ""),
		"user to push as, for -build-once")
	buildPassword := flag.String("build-password", os.Getenv("DOGIT_BUILD_PASSWORD"),
		"token to push with, for -build-once")
	flag.Parse()

	if *buildOnce {
		buildAndExit(cfg, *buildContext, *buildDockerfile, *buildImage, *buildTarget,
			*buildPush, *buildRegistry, *buildUser, *buildPassword)
		return
	}

	var token string
	if cfg.instanceFile != "" {
		read, err := readAndRemove(cfg.instanceFile)
		if err != nil {
			log.Fatalf("no registration token: %v", err)
		}
		token = read
	}
	if token == "" {
		log.Fatal("no registration token: set -registration-token-file")
	}
	if cfg.concurrency < 1 {
		cfg.concurrency = 1
	}
	mayClaim.Store(cfg.claim)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Read once, and the files go away. Job scripts run in this container, so a key left on
	// disk is a key a build script can read, and a client certificate is precisely what must
	// not be readable by the Dockerfile being built.
	credential, credentialErr := builder.LoadCredentials(cfg.buildkitCert, cfg.buildkitKey, cfg.buildkitCA)
	if credentialErr != nil {
		// Not fatal. A runner that cannot reach the builder is still a runner that should
		// say so on its heartbeat and wait for somebody to fix the volume — exiting would
		// turn a fixable problem into a crash loop whose log is a mount path.
		log.Printf("runner: no builder credential: %v", credentialErr)
	}

	// Cached here so that a core which goes away does not take the registration token down
	// with it: registering is the only call that needs that token, and re-registering is the
	// answer to a rejected credential, which is a different moment from the one that lost it.
	var registered atomic.Value
	registered.Store((*core.Client)(nil))

	log.Printf("runner %q starting: core %s, builder %s, %d at a time, claiming %v",
		cfg.name, cfg.coreURL, cfg.buildkit, cfg.concurrency, cfg.claim)

	// The channel is a counter, not the limit. The limit is a number somebody can change while
	// jobs are running, and a channel sized once would freeze it at whatever it was at
	// startup — which is the same "the panel says something false" bug as a setting nobody
	// reads, wearing a different hat.
	slots := make(chan struct{}, 64)
	runnerSlots = slots

	module := &settings.Settings{}
	module.Seed(cfg.concurrency, cfg.jobLimit)

	ticker := time.NewTicker(cfg.poll)
	defer ticker.Stop()

	for {
		beat(ctx, &cfg, &registered, credential, token, module)
		// One attempt straight away, rather than after the first tick: a runner that has just
		// started and has work waiting should not sit for a poll interval looking idle.
		if mayClaim.Load() {
			ask(ctx, &cfg, registered.Load().(*core.Client), slots, module, credential)
		}
		select {
		case <-ctx.Done():
			log.Print("runner: stopping")
			return
		case <-ticker.C:
		}
	}
}

// beat keeps the core informed, and re-registers when the credential is refused.
var self measure.Self

func beat(ctx context.Context, cfg *config, registered *atomic.Value, credential *builder.Credentials, token string, module *settings.Settings) {
	existing, _ := registered.Load().(*core.Client)
	if existing == nil {
		existing = register(ctx, cfg, registered, token)
		if existing == nil {
			return
		}
	}

	// Read before reporting, so that what the panel shows about this machine is what it is
	// doing rather than what it was doing.
	if err := module.Read(ctx, existing); err != nil {
		log.Printf("runner: read settings: %v", err)
	}
	// A setting the core holds that this build does not act on is reported rather than
	// ignored: somebody believes they have set it, and silence is the one answer that leaves
	// them believing it.
	unapplied := module.UnknownKeys()

	host := measure.ReadHost()
	stats := core.Stats{
		UptimeSeconds:        pointer(int64(time.Since(startedAt).Seconds())),
		ProcessCPUPercent:    self.ProcessCPUPercent(),
		ProcessMemoryBytes:   self.ProcessMemoryBytes(),
		HostMemoryTotalBytes: host.MemoryTotalBytes,
		HostMemoryUsedBytes:  host.MemoryUsedBytes,
		HostLoad1:            host.Load1,
		Extra:                map[string]string{},
	}

	// The module's own storage is the workspace, and the workspace is the only thing here a
	// job fills up. A path that does not exist yet — before the first checkout — comes back
	// absent rather than as an empty disk, so the panel says "not reported" and not "0 of 0".
	disk := measure.ReadDisk(cfg.workspace)
	stats.StorageTotalBytes, stats.StorageUsedBytes = disk.TotalBytes, disk.UsedBytes

	// The node's cores, and this container's own ceiling beside them. On cgroup v2 the file
	// behind that number is per container rather than per pod, so it is labelled as what it
	// is: a pod capped below the node is a fact the node's own numbers do not contain, and a
	// builder and a runner sharing 4 GB with Traefik is the sort of thing somebody looks at
	// when a build is slow.
	if host.Cores != nil {
		stats.Extra["cores"] = fmt.Sprint(*host.Cores)
	}
	if host.CgroupLimitBytes != nil {
		stats.Extra["memory_limit"] = humanBytes(*host.CgroupLimitBytes)
	}
	stats.Extra["workspace"] = cfg.workspace
	stats.Extra["queue"] = queueWord()
	// The limits actually in force, not the ones this process was started with. A panel
	// showing a stale number is the same lie as one showing a setting nobody applied.
	stats.Extra["at once"] = fmt.Sprint(module.Concurrency.Load())
	stats.Extra["keep checkouts"] = fmt.Sprint(module.KeepCheckout.Load())
	if len(unapplied) > 0 {
		stats.Extra["settings not applied"] = strings.Join(unapplied, " ")
	}
	stats.Extra["jobs"] = fmt.Sprintf("0 of %d running", cfg.concurrency)
	stats.Extra["builder"] = builderWord(ctx, cfg, credential)
	stats.Extra["claiming"] = fmt.Sprint(mayClaim.Load())
	if at := lastWorkAt.Load(); at > 0 {
		stats.Extra["last_work"] = time.Since(time.Unix(0, at)).Round(time.Second).String() + " ago"
	} else {
		stats.Extra["last_work"] = "none yet"
	}

	_, err := existing.Heartbeat(ctx, stats)
	if err == nil {
		return
	}
	if !errors.Is(err, core.ErrUnauthorized) {
		log.Printf("runner: heartbeat: %v", err)
		return
	}

	// A refused credential is a stale one, not a broken core. The registration token is the
	// way back in, and it is the only thing that has one.
	log.Print("runner: credential refused, registering again")
	registered.Store((*core.Client)(nil))
}

func register(ctx context.Context, cfg *config, registered *atomic.Value, token string) *core.Client {
	issued, err := core.New(cfg.coreURL, "").Register(ctx, token, runnerKind, cfg.name, cfg.endpoint, manifest())
	if err != nil {
		log.Printf("runner: register: %v", err)
		return nil
	}
	// The registration token has done its one job. Left in the environment it is readable by
	// every job script that runs in this container, and it is the one credential here that
	// can introduce a new module to the instance — a script that exfiltrates it is not
	// reading a build token with an expiry, it is holding the door key.
	client := core.New(cfg.coreURL, issued)
	registered.Store(client)
	log.Printf("runner: registered as %s", runnerKind)
	return client
}

// manifest is what the core shows an operator and what it uses to route to this module.
//
// The settings are declared here rather than in a database because a module is the only thing
// that knows what its own settings mean — but declaring them is a promise, and the runner has
// to actually read them. The old runner declared three and read none, which is a bug this one
// starts out not having: every setting below is read in -settings, and the loop that applies
// them is where concurrency would be applied.
func manifest() map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Builds images with BuildKit and runs jobs as pods. No Docker, no root, no host access.",
		"scopes":      []string{},
		"database":    false,
		// Declared, not defaulted. How much this machine will take at once is a fact about
		// the machine, and somebody watching a pipeline sit still is asking about the machine.
		"capacity": envInt("DOGIT_RUNNER_CONCURRENCY", 1),
		"settings": []map[string]any{
			{
				"key": "concurrency", "label": "Jobs at once", "type": "int", "default": 1,
				"description": "How many jobs this runner takes together. Takes effect straight away; " +
					"jobs already running are left to finish.",
			},
			{
				"key": "job_timeout", "label": "Job timeout", "type": "int", "default": 3600,
				"description": "Seconds a single job may run before it is stopped. A job that will " +
					"not end is holding a slot, and a queue stops draining while it does.",
			},
			{
				"key": "keep_checkout", "label": "Keep working copies", "type": "bool", "default": true,
				"description": "Leave each project's checkout in place between jobs, so the next job " +
					"of the same project fetches one commit instead of the whole history. Turn it off " +
					"to keep the disk from growing with the number of projects, at the price of a wait " +
					"on every build.",
			},
		},
	}
}

func ask(ctx context.Context, cfg *config, client *core.Client, slots chan struct{},
	module *settings.Settings, credential *builder.Credentials) {
	if client == nil {
		return
	}
	// Read the limit before the slot, and check it against what is already running. Checking
	// and taking are two steps, and only this goroutine does both — the releasing happens in
	// the goroutines jobs run on, which is safe because len() of a channel is always a
	// truthful count of what is in it.
	if int64(len(slots)) >= module.Concurrency.Load() {
		return
	}
	select {
	case slots <- struct{}{}:
	default:
		return
	}

	answer, err := client.Claim(ctx, nil)
	if err != nil {
		<-slots
		if !errors.Is(err, core.ErrUnauthorized) {
			log.Printf("runner: asking for work: %v", err)
		}
		return
	}
	if answer.Waiting != nil {
		queueDepth.Store(int64(*answer.Waiting))
		queueKnown.Store(true)
	}
	if answer.Job == nil {
		<-slots
		return
	}

	lastWorkAt.Store(time.Now().UnixNano())
	go func() {
		defer func() { <-slots }()
		runJob(ctx, cfg, client, answer, module, credential)
	}()
}

// runJob carries one claimed job all the way to finished.
//
// The job is finished here rather than inside the executor or the builder, because the job is
// both: a build job is a script and an image, and the core has to be told it is done only once
// the second half is in the registry. An earlier version finished it when the script ended,
// which meant the deploy stage started while the image was still being pushed, asked the
// registry for a tag that was not there yet, and deployed by tag instead of by digest — so a
// rollback would have brought back whatever else that name pointed at.
//
// A finish in a defer, over the whole of it, so that no path out of here leaves a job at
// "running" on a runner that has moved on. That is the one state a queue cannot recover from
// by itself.
func runJob(ctx context.Context, cfg *config, client *core.Client, answer core.Claim,
	module *settings.Settings, credential *builder.Credentials) {
	started := time.Now()
	claimed := answer.Job
	log.Printf("runner: job %d (%s, %s) on %s", claimed.ID, claimed.Name, claimed.Stage, claimed.ProjectPath)

	job := &jobs.Job{
		ID:          claimed.ID,
		Stage:       claimed.Stage,
		Name:        claimed.Name,
		ProjectPath: claimed.ProjectPath,
		Script:      claimed.Script,
		Variables:   claimed.Variables,
	}
	// From the answer, not from the job. See the note on core.Job.
	if answer.Registry != nil {
		job.Registry = &jobs.Registry{
			URL:   answer.Registry.URL,
			Image: answer.Registry.Image,
			Token: answer.Registry.Token,
		}
	}

	executor := jobs.New(cfg.workspace, "", time.Duration(module.JobTimeout.Load()), client,
		module.KeepCheckout.Load())
	finish := func(status, reason string) {
		if err := client.FinishJob(context.WithoutCancel(ctx), job.ID, status,
			time.Since(started), reason); err != nil {
			// Nothing useful to do about it, and saying so here would be the last line of a
			// job whose whole point was to be reported accurately.
			log.Printf("runner: job %d could not be finished: %v", job.ID, err)
		}
	}

	result := executor.Run(ctx, job)
	if result.Status != jobs.StatusSuccess {
		log.Printf("runner: job %d failed after %s: %s", job.ID, result.Took, result.Reason)
		finish(jobs.StatusFailed, result.Reason)
		return
	}
	log.Printf("runner: job %d script finished in %s, %d steps", job.ID, result.Took, result.Steps)

	if len(claimed.Build) == 0 {
		finish(jobs.StatusSuccess, "")
		return
	}
	if err := buildImage(ctx, cfg, client, job, claimed, credential); err != nil {
		log.Printf("runner: job %d: build failed: %s", job.ID, err)
		finish(jobs.StatusFailed, "the image this job builds did not build: "+err.Error())
		return
	}
	finish(jobs.StatusSuccess, "")
}

// buildImage builds the image a job asked for and pushes it, then says what it made.
//
// Only reached once the script has succeeded. A build on top of a failed script would be a
// second failure with a more expensive cause, and a pipeline page would show an image for a
// commit whose own checks did not pass.
//
// The tag comes from the project's own configuration, with $VARIABLES expanded from what the
// core recorded — and if there is no tag, the short commit. Never nothing: an image with no
// tag is a dangling manifest in a registry, and the deploy side addresses images by digest,
// so a build nobody can name is a build nobody can deploy.
func buildImage(ctx context.Context, cfg *config, client *core.Client, job *jobs.Job,
	claimed *core.Job, credential *builder.Credentials) error {
	build := claimed.Build

	contextDir := "."
	if value, ok := build["context"].(string); ok && value != "" {
		contextDir = value
	}
	dockerfile := "Dockerfile"
	if value, ok := build["dockerfile"].(string); ok && value != "" {
		dockerfile = value
	}

	// A project's tag is a template, and the core recorded the variables for exactly this.
	// The name is in the build itself, written onto the job by the core when the run was
	// filed — `image: registry.f220.ru/test/versions` — and the runner is not asked to work
	// it out. The tag is separate, and a job whose `tag` came out empty is normal: the
	// project's own CI says "the tag when there is one, the short commit otherwise", and a
	// push to a branch is not a push to a tag. So the fallback is the short commit, which is
	// what the project asked for and what the deploy side addresses by digest anyway.
	image, _ := build["image"].(string)
	if image == "" && job.Registry != nil {
		image = job.Registry.Image
	}
	tag := ""
	if value, ok := build["tag"].(string); ok {
		tag = expand(value, claimed.Variables)
	}
	if tag == "" {
		tag = claimed.Variables["CI_COMMIT_TAG"]
	}
	if tag == "" {
		tag = claimed.Variables["CI_COMMIT_SHORT_SHA"]
	}

	// Said and failed, not skipped: see runJob on why a build that produced nothing is worse
	// than a build that failed.
	if job.Registry == nil || job.Registry.Token == "" {
		_ = client.JobProgress(ctx, job.ID, "failed", "no registry credential for the push")
		_ = client.JobLog(ctx, job.ID, "stderr",
			"this job asks for an image, but the core offered no credential to push it with\n")
		return errors.New("the core offered no registry credential for this job's image")
	}
	if image == "" || tag == "" {
		_ = client.JobProgress(ctx, job.ID, "failed", "no name for the image")
		_ = client.JobLog(ctx, job.ID, "stderr",
			"this job asks for an image, but there is nothing to call it: no tag, no commit\n")
		return errors.New("this job's image has no name to be pushed under")
	}

	image = image + ":" + tag
	_ = client.JobProgress(ctx, job.ID, "build", "building "+image)

	// The credential this process already read at startup, from memory. Not a re-read of the
	// file, because there is no file: it was deleted on purpose so that a job script running
	// in this container cannot read it, and that leaves exactly one copy, which is here.
	if credential == nil {
		_ = client.JobProgress(ctx, job.ID, "failed", "no builder credential")
		_ = client.JobLog(ctx, job.ID, "stderr", "this runner has no builder credential\n")
		return errors.New("this runner has no credential for the builder")
	}
	built, err := builder.Connect(ctx, cfg.buildkitHost, cfg.serverName, credential)
	if err != nil {
		return failBuild(ctx, client, job, "cannot reach the builder: "+err.Error())
	}
	defer built.Close()

	_ = client.JobProgress(ctx, job.ID, "build", "pushing "+image)
	result, err := built.Build(ctx, builder.Request{
		ContextDir: filepath.Join(jobs.Directory(cfg.workspace, job.ProjectPath), contextDir),
		Dockerfile: filepath.Join(contextDir, dockerfile),
		Target:     stringOf(build, "target"),
		Args:       buildArgs(build, claimed.Variables),
		Image:      image,
		Push:       true,
		Registry: &builder.Credential{
			Server:   hostOf(job.Registry.URL),
			Username: builderName,
			Token:    job.Registry.Token,
		},
	}, func(progress builder.Progress) {
		switch {
		case progress.Failed:
			_ = client.JobLog(ctx, job.ID, "stderr", "FAILED "+progress.Step+"\n")
		case progress.Line != "":
			_ = client.JobLog(ctx, job.ID, "stdout", progress.Step+" | "+progress.Line+"\n")
		default:
			_ = client.JobLog(ctx, job.ID, "stdout", "done "+progress.Step+"\n")
		}
	})
	if err != nil {
		return failBuild(ctx, client, job, err.Error())
	}

	log.Printf("runner: job %d built %s in %s, %d steps (%d cached)",
		job.ID, result.Digest, result.Duration().Round(time.Millisecond), result.Steps, result.Cached)
	// The step is closed here, and it says what it closed over: the name the image was
	// pushed under. Not the digest — that is in the log line below, and a page that
	// showed both would say the same thing twice about one push.
	_ = client.JobProgress(ctx, job.ID, "done", "pushed "+image)

	// In the log rather than only in the feed, because the feed is only watched live and
	// this is what somebody reads in a month asking what went out. No leading newline: the
	// core takes this text as lines, and a leading one arrives as an empty line in the
	// middle of a build's output for no reason a reader could account for.
	_ = client.JobLog(ctx, job.ID, "stdout", "built "+result.Digest+"\n")
	return nil
}

// failBuild says a build failed and hands the reason back, so that the caller closes the job
// as failed. The alternative is the worst state this program can produce: a pipeline whose
// build stage is green, whose deploy stage then looks for an image that was never pushed, and
// whose page shows a run that succeeded. A build that failed has to be a failed build.
func failBuild(ctx context.Context, client *core.Client, job *jobs.Job, reason string) error {
	_ = client.JobProgress(ctx, job.ID, "failed", reason)
	_ = client.JobLog(ctx, job.ID, "stderr", "the build did not finish: "+reason+"\n")
	return errors.New(reason)
}

// expand fills $VARIABLE and ${VARIABLE} from what the core recorded.
//
// Written here rather than pulled in as a library for six lines: the alternative is a
// dependency that would be asked to do nothing else, in a program that is trying to have as
// few as possible.
func expand(template string, variables map[string]string) string {
	out := template
	for name, value := range variables {
		out = strings.ReplaceAll(out, "${"+name+"}", value)
		out = strings.ReplaceAll(out, "$"+name, value)
	}
	return strings.TrimSpace(out)
}

func stringOf(values map[string]any, key string) string {
	if text, ok := values[key].(string); ok {
		return text
	}
	return ""
}

func buildArgs(build map[string]any, variables map[string]string) map[string]string {
	arguments := map[string]string{}
	if raw, ok := build["args"].(map[string]any); ok {
		for name, value := range raw {
			arguments[name] = expand(fmt.Sprint(value), variables)
		}
	}
	return arguments
}

func hostOf(address string) string {
	address = strings.TrimPrefix(address, "https://")
	address = strings.TrimPrefix(address, "http://")
	if at := strings.Index(address, "/"); at >= 0 {
		address = address[:at]
	}
	return address
}

// buildAndExit is one build and out, for proving the path to the builder and to the registry
// without going near the job queue.
//
// Everything it does comes from flags, including the credential, and that is the one thing it
// must not do forever: the token a real build pushes with is minted per project by the core
// and lives for two hours, and a flag on a command line is a token in a process list.
func buildAndExit(cfg config, contextDir, dockerfile, image, target string, push bool,
	registryHost, user, password string) {

	if contextDir == "" || image == "" {
		log.Fatal("-build-once needs -build-context and -build-image")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	credential, err := builder.LoadCredentials(cfg.buildkitCert, cfg.buildkitKey, cfg.buildkitCA)
	if err != nil {
		log.Fatalf("runner: %v", err)
	}

	client, err := builder.Connect(ctx, cfg.buildkitHost, cfg.serverName, credential)
	if err != nil {
		log.Fatalf("runner: %v", err)
	}
	defer client.Close()

	request := builder.Request{
		ContextDir: contextDir,
		Dockerfile: dockerfile,
		Target:     target,
		Image:      image,
		Push:       push,
	}
	if registryHost != "" && user != "" && password != "" {
		request.Registry = &builder.Credential{
			Server: registryHost, Username: user, Token: password,
		}
	} else if push {
		// Said out loud rather than discovered at the push: a build with no credential
		// spends its whole time compiling and then fails on the last step, which is the
		// most expensive way to hear "you forgot".
		log.Print("runner: no -build-registry/-build-user/-build-password, so the push will fail at the end")
	}

	started := time.Now()
	result, err := client.Build(ctx, request, func(progress builder.Progress) {
		switch {
		case progress.Failed:
			log.Printf("runner: FAILED %s", progress.Step)
		case progress.Line != "":
			log.Printf("runner: %s | %s", progress.Step, progress.Line)
		default:
			note := ""
			if progress.Cached {
				note = " (cached)"
			}
			log.Printf("runner: done %s%s", progress.Step, note)
		}
	})

	if err != nil {
		log.Fatalf("runner: %v", err)
	}

	log.Printf("runner: built %s in %s, %d steps (%d cached)",
		result.Digest, time.Since(started).Round(time.Millisecond), result.Steps, result.Cached)
	log.Print(result.Digest)
}

// builderWord is what the runner can say about the builder right now.
//
// Asked on every heartbeat rather than once at startup, because a builder that comes up after
// the runner is a normal race and a one-shot check would report a failure that has since
// stopped being true. The wording says what was checked, not just that something answered:
// a runner that reported "connected" after dialling a TCP port would be right in exactly the
// case where the certificate was wrong.
func builderWord(ctx context.Context, cfg *config, credential *builder.Credentials) string {
	if credential == nil {
		return "no credential loaded"
	}
	ping, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	answer, err := credential.Ping(ping, cfg.buildkitHost, cfg.buildkitServerName())
	if err != nil {
		return "unreachable: " + err.Error()
	}
	return answer
}

// queueWord is the core's last answer, or a statement that there has not been one.
//
// "unknown" rather than zero. A runner that has not heard from the core must not look like
// one that has and found nothing: those mean opposite things to whoever is watching, and only
// one of them is true.
func queueWord() string {
	if !queueKnown.Load() {
		return "unknown"
	}
	return fmt.Sprint(queueDepth.Load())
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// buildkitServerName is the name to verify the builder's certificate against.
//
// Empty means "whatever host we dial", which is right for loopback — the certificate carries
// 127.0.0.1 as a name of its own. It is a flag rather than a constant because the day the
// daemon moves behind a Service the right answer stops being the address, and getting that
// wrong surfaces as a certificate error rather than as a name mismatch.
func (cfg config) buildkitServerName() string { return cfg.serverName }

// pointer, because every measurement in a Stats is a pointer on purpose: a module that did
// not measure something has to be able to say so, and there is no way to say so with a zero.
func pointer[T any](value T) *T { return &value }

func envInt(name string, fallback int) int {
	var value int
	if _, err := fmt.Sscanf(os.Getenv(name), "%d", &value); err != nil || value == 0 {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) bool {
	switch os.Getenv(name) {
	case "1", "true", "yes":
		return true
	case "0", "false", "no":
		return false
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(os.Getenv(name))
	if err != nil {
		return fallback
	}
	return value
}

// humanBytes in the units a person reads, because a limit stated in bytes is a number nobody
// compares against anything.
func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	div, exp := int64(unit), 0
	for size := value / unit; size >= unit && exp < 3; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(div), "KMGT"[exp])
}

// readAndRemove reads a file and deletes it, and the deletion is the reason this function
// exists rather than os.ReadFile.
//
// A credential in this container is readable by any job script that runs here, and the one
// below is the instance's registration token — the key that can introduce a new module. The
// volume it arrives on is a tmpfs, so there is nothing on disk to clean up afterwards; what
// has to be cleaned up is the file, so that the only thing it can be read by in the seconds
// between startup and the first job is this process.
func readAndRemove(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		// Not fatal — the value is already in memory — but the operator should hear it,
		// because a file that outlived its reading is a file a job script can open.
		log.Printf("runner: warning: could not remove %s: %v", path, err)
	}
	return strings.TrimSpace(string(raw)), nil
}
