// Command module-runner runs CI jobs on a machine that has a usable Docker.
//
// It is a module like any other, and that is the whole point: it registers,
// reports what it can see, and is told what to do by the core — but nothing about
// it requires the core to know where it is. A runner on a virtual machine nobody
// mentioned in the deployment does the work just as well as one in the same
// cluster, which is how an installation gets around a container runtime that will
// not let it build images.
//
// The heavy lifting is the Docker runtime the core already has; this process is
// the loop around it: claim a job, run it, send the output up as it arrives, say
// what happened.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ewolf/dogit/internal/coreerr"
	"github.com/ewolf/dogit/internal/modulechan"
	"github.com/ewolf/dogit/internal/runner"
)

const (
	defaultCoreURL  = "http://app:8080"
	defaultEndpoint = "http://module-runner:8092"
	defaultListen   = ":8092"
	defaultName     = "builder"
	runnerKind      = "runner:docker"
)

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL),
		"base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token", os.Getenv("DOGIT_MODULE_TOKEN"),
		"instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", defaultEndpoint),
		"address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", defaultName),
		"module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", defaultListen),
		"address this module listens on")
	flag.IntVar(&cfg.concurrency, "concurrency", envInt("DOGIT_RUNNER_CONCURRENCY", 1),
		"jobs to run at once")
	flag.StringVar(&cfg.dockerBinary, "docker", envOr("DOGIT_DOCKER_BINARY", "docker"),
		"the docker binary")
	flag.DurationVar(&cfg.poll, "poll", 3*time.Second, "how often to ask for work")
	flag.DurationVar(&cfg.heartbeat, "heartbeat", 30*time.Second, "heartbeat interval")
	flag.StringVar(&cfg.workspaceVolume, "workspace-volume",
		envOr("DOGIT_RUNNER_WORKSPACE_VOLUME", ""),
		"the Docker volume this runner's workspace is in, when it is in one")
	flag.StringVar(&cfg.workspaceMount, "workspace-mount",
		envOr("DOGIT_RUNNER_WORKSPACE_MOUNT", ""),
		"where that volume is mounted in this container, which the job is given too")
	flag.StringVar(&cfg.workspace, "workspace", envOr("DOGIT_RUNNER_WORKSPACE", "/data/work"),
		"where checkouts are made")
	flag.IntVar(&cfg.clonePort, "clone-port", envInt("DOGIT_RUNNER_SSH_PORT", 22),
		"the SSH port this runner connects to, from where this runner is")
	flag.StringVar(&cfg.sshHost, "ssh-host", envOr("DOGIT_SSH_HOST", "localhost"),
		"the instance's SSH host, for cloning over git-over-ssh")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module-runner: a registration token is required (DOGIT_MODULE_TOKEN)")
	}
	if cfg.concurrency < 1 {
		cfg.concurrency = 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := os.MkdirAll(cfg.workspace, 0o750); err != nil {
		log.Fatalf("module-runner: cannot create the workspace: %v", err)
	}

	runtime := runner.NewDocker(runner.DockerOptions{
		Binary: cfg.dockerBinary,
		Logger: logAdapter{},
		// A finished job is kept for a while, so that a failure that happens after
		// the script — a post-step, an upload — still has a container to look at.
		// Nothing is kept forever.
		KeepSeconds: 600,
	})

	if err := runtime.Available(ctx); err != nil {
		// Not fatal at start-up: a runner that comes up before Docker is ready is
		// common, and refusing to start would turn a moment's delay into a crash
		// loop. Each job checks again, and says so when it cannot.
		log.Printf("module-runner: docker is not ready yet: %v", err)
	}

	core := &coreClient{baseURL: strings.TrimRight(cfg.coreURL, "/")}

	token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg))
	if err != nil {
		log.Fatalf("module-runner: registration failed: %v", err)
	}
	core.token = token
	log.Printf("module-runner: registered as %s, %d at a time", cfg.name, cfg.concurrency)

	register := func() error {
		token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg))
		if err != nil {
			return err
		}
		core.token = token
		log.Printf("module-runner: re-registered")
		return nil
	}

	go heartbeat(ctx, core, cfg.heartbeat, register)

	// The listener exists so that the core and Kubernetes have something to probe.
	// Nothing is served over it that the core does not already ask for.
	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           ownEndpoints(core, runtime, cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		log.Printf("module-runner: listening on %s", cfg.listen)
		_ = server.ListenAndServe()
	}()

	log.Printf("module-runner: working in %s, docker %s", cfg.workspace, cfg.dockerBinary)

	// The channel, and what it changes: the runner no longer has to be asleep when the core
	// says there is work.
	//
	// The poll stays. It is not a fallback for a broken channel — a broken channel reconnects
	// by itself — it is the answer to an announcement that did not arrive, and an announcement
	// that does not arrive is exactly what a core that has just been restarted looks like. One
	// mechanism removed the wait; it did not make waiting impossible.
	wake := make(chan struct{}, 1)
	go channel(ctx, cfg, wake)

	loop(ctx, core, runtime, cfg, wake)

	log.Printf("module-runner: stopped")
}

// logAdapter is the small translation between this process's logger and the
// runtime's interface, which wants the level named as a method.
type logAdapter struct{}

func (logAdapter) Warn(msg string, args ...any)  { logRunner(msg, args...) }
func (logAdapter) Info(msg string, args ...any)  { logRunner(msg, args...) }
func (logAdapter) Debug(msg string, args ...any) { logRunner(msg, args...) }

// logRunner prints the message followed by its key/value pairs.
//
// The runtime hands over structured arguments, and handing those to a formatting
// logger as if they were a format string produces a line nobody can read: the
// pairs come out as one %!(EXTRA...) blob. They are joined as text instead.
func logRunner(msg string, args ...any) {
	var parts []string
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprintf("%v=%v", args[i], args[i+1]))
	}
	if len(args)%2 == 1 {
		parts = append(parts, fmt.Sprint(args[len(args)-1]))
	}
	log.Printf("runner: %s %s", msg, strings.Join(parts, " "))
}

// config is what this runner was told to be.
type config struct {
	coreURL           string
	registrationToken string
	endpoint          string
	name              string
	listen            string
	concurrency       int
	dockerBinary      string
	poll              time.Duration
	heartbeat         time.Duration
	workspace         string
	// workspaceVolume is the Docker volume this runner's workspace lives in, and
	// workspaceMount is where that volume is mounted in this container. Both are asked
	// for because the job's container has to be given the same view this runner has, and
	// the pair is what says what that view is. See whereTheJobLooks.
	workspaceVolume string
	workspaceMount  string
	// sshHost is the instance's SSH host as the outside world is given it: the address a
	// person clones from. It is not what this runner clones from — see cloneAddress.
	sshHost string
	// clonePort is the SSH port to connect to, from where this runner is. Twenty-two,
	// because that is what an sshd inside a container listens on, and the port published
	// to the outside is not that port in any deployment that maps one onto the other.
	clonePort int
}

// manifest is what this runner says it can do.
func manifest(cfg config) map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Runs CI jobs with Docker, and builds images on machines that are allowed to",
		"scopes":      []string{},
		// Declared rather than left for the core to guess: how many jobs this machine
		// takes at once is a fact about this machine, and an operator asking why a
		// pipeline is waiting is asking about the machine.
		"capacity": cfg.concurrency,

		"settings": []map[string]any{
			{
				"key":         "tags",
				"label":       "Tags",
				"type":        "string",
				"default":     "docker",
				"description": "What this runner calls itself when it asks for work. A job may ask for particular tags.",
			},
			{
				"key":         "job_timeout",
				"label":       "Job timeout",
				"type":        "int",
				"default":     3600,
				"description": "Seconds a single job may run before it is stopped. A build that has not finished by then is not going to.",
			},
			{
				"key":   modulechan.CommandTTLSetting,
				"label": "Command lifetime",
				"type":  "int",
				// Declared with no default rather than 300, and that is deliberate: an
				// empty field is what a runner registered before this setting existed
				// has, and both must mean the same thing. Putting 300 here would make
				// the two look different on the page while behaving alike, which is a
				// question an operator will ask and cannot answer from what they see.
				//
				// No "default" key at all rather than a nil one: absent and null are the
				// same thing to anything reading this, and a key written out is a key
				// somebody will eventually fill in without reading why it was empty.
				"description": "Seconds the core keeps a command for this runner, in case it " +
					"cannot be delivered while it is down. 0 means do not keep anything: a command " +
					"arrives once or not at all. Empty means five minutes. This runner is told about " +
					"work rather than commanded, so nothing is lost by leaving it alone.",
			},
			{
				"key":         "allow_privileged",
				"label":       "Run privileged jobs",
				"type":        "bool",
				"default":     false,
				"description": "Off by default: a privileged container can reach the host, and a job is somebody's shell script.",
			},
		},

		"uninstall": map[string]any{
			"options": []map[string]any{
				{
					"key":         "remove_workspaces",
					"label":       "Delete build checkouts",
					"description": "Working copies left on this machine. Container images built here are not removed.",
					"default":     true,
					"dangerous":   true,
				},
			},
		},
	}
}

// job is what the core hands over.
type job struct {
	ID          int64    `json:"id"`
	IID         int      `json:"iid"`
	Name        string   `json:"name"`
	Stage       string   `json:"stage"`
	Image       string   `json:"image"`
	Script      []string `json:"script"`
	ProjectPath string   `json:"project_path"`
	// SHA is the commit this job is about, and Ref the name it was reached by.
	//
	// Both are carried because a clone that is told neither lands on the repository's
	// default branch. A run created for a branch would then build the default branch's
	// code, succeed, and push an image of the wrong commit — a pipeline that reports
	// success about something it never built.
	SHA       string            `json:"sha"`
	Ref       string            `json:"ref"`
	Build     map[string]any    `json:"build,omitempty"`
	Variables map[string]string `json:"variables,omitempty"`
}

// claim is the core's answer to "is there anything for me".
type claim struct {
	// core is the client to ask for the project key with. Not part of the answer:
	// it is how a job, which knows only what the core sent it, gets at the core.
	core *coreClient

	Job *job `json:"job"`
	// Registry is what a job that builds an image needs: the address, the name the
	// registry's own rule gives it, and a credential scoped to that project alone.
	Registry map[string]any `json:"registry,omitempty"`
	// CloneURL and Key are how this machine gets the code. A runner is a machine the
	// deployment knows nothing about, so it cannot already hold a key on the project;
	// the core mints one for this job, read-only, and takes it away when the job is
	// over.
	CloneURL string     `json:"clone_url,omitempty"`
	Key      *deployKey `json:"key,omitempty"`
	LogKey   string     `json:"log_key,omitempty"`

	// Waiting is how many jobs were still queued at the core after this one was taken.
	// A pointer because the core says "not known" by leaving it out, and a runner that
	// cannot count the queue must say so rather than report a confident zero — the two
	// mean opposite things to whoever is watching, and only one of them is true.
	Waiting *int `json:"waiting,omitempty"`
}

// What this runner knows about itself between one heartbeat and the next.
//
// Package-level because the loop that does the work and the loop that reports on it are
// separate goroutines with nothing to hand a reading across, and because these are
// readings rather than state: a runner that starts over has simply not measured anything
// yet, which is worth saying and not worth failing over.
var (
	// queueDepth is the core's answer to the last claim, not a count of anything held
	// here — this runner holds no queue, and the number it passes on is the one that
	// tells a waiting job whether anybody is coming.
	queueDepth atomic.Int64
	// queueKnown is whether the core answered at all. False means the number is a
	// leftover, and a leftover shown as current is worse than nothing shown.
	queueKnown atomic.Bool
	lastWorkAt atomic.Int64 // unix nanoseconds; zero until work has actually arrived

	// runnerSlots is this runner's capacity, read by the heartbeat to say how busy it
	// is. A plain variable because it is set once, before any goroutine exists that
	// could read it, and because len() of a channel is the answer already — counting
	// slots separately would be a second answer that can disagree with the first.
	runnerSlots chan struct{}
)

// recordClaim remembers what the last answer to "is there anything for me" said.
func recordClaim(answer claim) {
	if answer.Waiting != nil {
		queueDepth.Store(int64(*answer.Waiting))
		queueKnown.Store(true)
	}
	if answer.Job != nil {
		lastWorkAt.Store(time.Now().UnixNano())
	}
}

// deployKey is the credential for one job: a private key and where it came from.
type deployKey struct {
	PrivateKey  string `json:"private_key"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expires_at"`
}

type registryAccess struct {
	URL         string `json:"url"`
	InternalURL string `json:"internal_url"`
	Image       string `json:"image"`
	Token       string `json:"token"`
}

// channel listens for the core saying there is work, and says so once.
//
// The signal carries nothing. What the runner does about it is to ask, over HTTP as it always
// has: the claim is atomic and belongs in the database where two runners cannot both be given
// the same job. All the channel removes is the wait before asking.
func channel(ctx context.Context, cfg config, wake chan<- struct{}) {
	client := &modulechan.Client{
		URL:   cfg.coreURL,
		Token: cfg.registrationToken,
		Log:   runnerLog{},
		OnMessage: func(_ context.Context, message modulechan.Message) {
			if message.Kind != modulechan.WorkAvailable {
				// Not an error: a core with more to say than this runner knows about is
				// still a core to listen to.
				return
			}
			// One wake is enough however many announcements arrive, because asking takes
			// everything on offer: a runner told "there is work" twice does not take two
			// jobs, it takes what there is and asks again when there is more.
			select {
			case wake <- struct{}{}:
			default:
			}
		},
	}
	client.Run(ctx)
}

// runnerLog is where this runner says what happened on the channel.
//
// The module is a program with log.Printf in it and a stdlib logger that is already told what
// the prefix is, so the channel's two-method interface is met by printing rather than by
// carrying a second logging setup through a binary that has no use for one.
type runnerLog struct{}

func (runnerLog) Info(msg string, args ...any) {
	log.Print("module-runner: ", msg, " ", attrs(args))
}

func (runnerLog) Warn(msg string, args ...any) {
	log.Print("module-runner: ", msg, " ", attrs(args))
}

// attrs renders the key/value pairs the channel logs with, in the slog spelling it uses: the
// keys arrive as plain values rather than as slog.Attr, so they are printed as they come.
func attrs(args []any) string {
	parts := make([]string, 0, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		parts = append(parts, fmt.Sprint(args[i]), "=", fmt.Sprint(args[i+1]))
	}
	return strings.Join(parts, " ")
}

// loop takes work until the context is cancelled.
func loop(ctx context.Context, core *coreClient, runtime *runner.Docker, cfg config, wake <-chan struct{}) {
	slots := make(chan struct{}, cfg.concurrency)
	runnerSlots = slots
	ticker := time.NewTicker(cfg.poll)
	defer ticker.Stop()

	// One immediate attempt: a runner that just started should not sit idle for a
	// whole poll interval while work is waiting.
	take(ctx, core, runtime, cfg, slots)

	for {
		select {
		case <-ctx.Done():
			// Wait for what is running rather than abandoning it: a job killed
			// halfway leaves a container behind and a build nobody can read.
			for len(slots) > 0 {
				time.Sleep(200 * time.Millisecond)
			}
			return
		case <-ticker.C:
			take(ctx, core, runtime, cfg, slots)
		case <-wake:
			take(ctx, core, runtime, cfg, slots)
		}
	}
}

// take claims a job if there is one and a free slot for it.
func take(ctx context.Context, core *coreClient, runtime *runner.Docker, cfg config, slots chan struct{}) {
	select {
	case slots <- struct{}{}:
	default:
		// Already as busy as this runner was told to be. Asking anyway would hand
		// the core work it cannot hand back.
		return
	}

	answer, err := core.claim(ctx, cfg.tags())
	answer.core = core
	recordClaim(answer)
	if err != nil {
		<-slots
		if !errors.Is(err, errNothingToDo) {
			log.Printf("module-runner: asking for work: %v", err)
		}
		return
	}
	if answer.Job == nil {
		<-slots
		return
	}
	answer.core = core

	go func() {
		defer func() { <-slots }()
		runJob(ctx, core, runtime, cfg, answer)
	}()
}

func (cfg config) tags() []string {
	return []string{"docker"}
}

// whereTheJobLooks is what a job's container is given to see its checkout, and where it is
// told to work.
//
// Two ways, and which one is right depends on where the daemon is. A job's container is
// created by the same docker this runner talks to, so a path in `-v` is resolved by the
// daemon on its own machine — which, whenever this runner is itself a container, is not this
// machine. The runner's /data/work is then a path the daemon has never heard of: docker
// makes a directory of that name on the host and mounts the empty one, and the job's script
// runs in a checkout that is not there. Nothing says so; `test -f k8s/Dockerfile` simply
// exits 1.
//
// So when the workspace is a Docker volume, the job is given that volume mounted exactly
// where this runner has it, and works where the checkout is. Both halves are asked for,
// because mounting it somewhere else is not a near miss: a volume mounted one level higher
// puts the checkout at a path one level shorter, the working directory does not exist, and
// the job's script fails on the first line with nothing in its log but its own echo.
func whereTheJobLooks(cfg config, workspace string) ([]runner.Volume, string) {
	volume := strings.TrimSpace(cfg.workspaceVolume)
	mount := strings.TrimRight(strings.TrimSpace(cfg.workspaceMount), "/")
	if volume != "" && mount != "" {
		return []runner.Volume{{
			Name:   "checkout",
			Source: volume,
			Target: mount,
		}}, workspace
	}
	return []runner.Volume{{
		Name:   "checkout",
		Source: workspace,
		Target: "/build",
	}}, "/build"
}

// runJob runs one job and reports what happened.
//
// The order matters: the log goes up as it is produced, so a runner that dies
// halfway still leaves behind everything it managed to say. Waiting until the end
// would lose exactly the part worth keeping.
/**
 * Says why a job failed, in the job's own log, before its verdict is recorded.
 *
 * The verdict is one line on a page and the log is where somebody goes to find out what
 * actually happened. A job that failed before it printed anything — a checkout that could
 * not authenticate, an environment that would not start — otherwise leaves a log with
 * nothing in it at all, which reads as though nobody tried.
 */
func failJob(ctx context.Context, core *coreClient, jobID int64, started time.Time, reason error) {
	core.log(ctx, jobID, "err", fmt.Sprintf("this job did not run: %v\n", reason))
	core.finish(ctx, jobID, "failed", time.Since(started), reason.Error())
	log.Printf("module-runner: job %d failed: %v", jobID, reason)
}

func runJob(ctx context.Context, core *coreClient, runtime *runner.Docker, cfg config, answer claim) {
	job := answer.Job
	started := time.Now()

	log.Printf("module-runner: job %d (%s) in %s", job.ID, job.Name, job.ProjectPath)

	// The checkout is what the script works in. Cloning per job costs a little and
	// saves a lot of confusion about which commit was built.
	workspace := fmt.Sprintf("%s/%s/job-%d", strings.TrimRight(cfg.workspace, "/"),
		strings.ReplaceAll(job.ProjectPath, "/", "-"), job.ID)

	volumes, workingDir := whereTheJobLooks(cfg, workspace)
	spec := runner.JobSpec{
		ID:          job.ID,
		Name:        job.Name,
		Image:       runner.ImageTag(job.Image),
		Script:      job.Script,
		Environment: jobEnvironment(job, answer.Registry),
		WorkingDir:  workingDir,
		Timeout:     cfg.jobTimeout(),
		Volumes:     volumes,
	}

	// The checkout happens on this machine rather than inside the job's container:
	// a container that is only allowed to run a script has no business being
	// allowed to clone.
	if err := checkout(ctx, cfg, answer, workspace); err != nil {
		failJob(ctx, core, job.ID, started, fmt.Errorf("cannot check out %s: %w", job.ProjectPath, err))
		return
	}

	handle, err := runtime.Prepare(ctx, spec)
	if err != nil {
		failJob(ctx, core, job.ID, started, fmt.Errorf("cannot prepare the job's environment: %w", err))
		return
	}
	defer func() {
		if err := runtime.Destroy(context.WithoutCancel(ctx), handle); err != nil {
			log.Printf("module-runner: could not remove the environment: %v", err)
		}
		_ = os.RemoveAll(workspace)
	}()

	// An image the job was asked to produce is built here and pushed to the
	// registry module, using the credential the core issued for this project.
	var pushed string
	// Why the build did not work, kept for the job's verdict rather than only for
	// the runner's log. A build that failed while the script succeeded is a failed
	// job: reporting it as a pass would leave a green build with no image behind it,
	// which is the one outcome nobody can notice until they deploy it.
	var buildErr error
	var buildWait chan struct{}

	// Made before the build starts, not after: docker's own progress is the build's
	// output, and a build that began before its writer existed would have its first
	// and longest part kept back until the end.
	out := newLogWriter(ctx, core, job.ID, "out")
	errs := newLogWriter(ctx, core, job.ID, "err")

	if len(job.Build) > 0 {
		buildDone := make(chan struct{})
		go func() {
			defer close(buildDone)
			tag, err := buildAndPush(ctx, cfg, workspace, answer.Registry, job.Build, out,
				func(phase, message string) {
					if err := core.progress(ctx, job.ID, phase, message); err != nil {
						log.Printf("module-runner: could not report %s: %v", phase, err)
					}
				}, jobEnvironment(job, answer.Registry))
			if err != nil {
				buildErr = err
				return
			}
			pushed = tag
		}()
		// Waited for below, once the script is done: the job's verdict covers both.
		buildWait = buildDone
	}

	result := runtime.Stream(ctx, handle, spec, out, errs)

	// Whatever is still buffered goes now. A job that finishes in less time than
	// the flush interval never reaches one, and its last lines — usually the ones
	// that say why it failed — would never be sent at all.
	out.Close()
	errs.Close()

	// The build finishes after the script, often long after it. Reporting the job
	// now would say "passed" about a build that is still running and may yet fail —
	// which is exactly how a green pipeline ends up with no image behind it.
	if buildWait != nil {
		select {
		case <-buildWait:
		case <-ctx.Done():
			buildErr = errors.New("the build was cut short when the job was stopping")
		}
	}

	status := "success"
	message := resultMessage(result)
	if result.Status != runner.StatusSuccess || buildErr != nil {
		status = "failed"
	}
	if buildErr != nil {
		// Said in the log too: the job's verdict is one line on a page, and this is
		// the line that says why.
		core.log(ctx, job.ID, "err", fmt.Sprintf("build failed: %v\n", buildErr))
		if result.Status == runner.StatusSuccess {
			message = buildErr.Error()
		}
		log.Printf("module-runner: build failed: %v", buildErr)
	}

	if pushed != "" {
		core.log(ctx, job.ID, "out", fmt.Sprintf("image pushed: %s\n", pushed))
	}

	core.finish(ctx, job.ID, status, time.Since(started), message)
	log.Printf("module-runner: job %d %s in %s", job.ID, status, time.Since(started).Round(time.Second))
}

func resultMessage(result runner.Result) string {
	if result.Error != nil {
		return result.Error.Error()
	}
	return ""
}

func (cfg config) jobTimeout() time.Duration {
	return time.Hour
}

// jobEnvironment is what the script sees.
func jobEnvironment(job *job, registry map[string]any) map[string]string {
	environment := map[string]string{
		"CI_PROJECT_PATH": job.ProjectPath,
		"CI_JOB_ID":       fmt.Sprintf("%d", job.ID),
		"CI_JOB_NAME":     job.Name,
		"CI_JOB_STAGE":    job.Stage,
	}
	// What the run is about, as the core recorded it. Passed through rather than
	// worked out here: a build script that had to decide for itself whether it is on a
	// tag would be one git call away from tagging the wrong image.
	for _, name := range []string{
		"CI_COMMIT_REF_NAME", "CI_COMMIT_SHA", "CI_COMMIT_SHORT_SHA",
		"CI_COMMIT_BRANCH", "CI_COMMIT_TAG",
	} {
		if value := job.Variables[name]; value != "" {
			environment[name] = value
		}
	}

	// A job that builds is told where to push and under what name, and gets a
	// credential scoped to that project alone. Nothing here is a general account.
	if registry != nil {
		address, _ := registry["url"].(string)
		image, _ := registry["image"].(string)
		environment["DOGIT_REGISTRY"] = address
		environment["DOGIT_IMAGE"] = image
		environment["DOGIT_REGISTRY_TOKEN"] = tokenOf(registry)
		environment["DOGIT_REGISTRY_USERNAME"] = "builder"
	}
	return environment
}

// What this host's docker build understands, asked once and remembered.
//
// Both flags this build passes beyond the plain ones are BuildKit's: --progress is what
// makes a build narrate itself line by line, and --provenance is how a build record is
// turned off. A host with only the legacy builder refuses the whole command over the first
// of them — "unknown flag: --progress", exit 125, no build started — and what it says names
// a flag rather than the thing that is actually missing, which is the buildx component that
// host does not have.
//
// So it is asked, from the builder's own help, and a flag this host has never heard of is
// not passed. That is the whole answer for an older docker: it builds what it can, which is
// the image, and narrates it in fewer lines.
var builderFlags struct {
	once    sync.Once
	known   map[string]bool
	checked bool
}

func buildFlagKnown(cfg config, flag string) bool {
	builderFlags.once.Do(func() {
		builderFlags.known = map[string]bool{}
		out, err := exec.CommandContext(context.Background(),
			cfg.dockerBinary, "build", "--help").CombinedOutput()
		if err != nil {
			// A docker that cannot be asked will be asked again by the build itself, and
			// its own refusal is a better sentence than a guess made here.
			return
		}
		builderFlags.checked = true
		for _, flag := range []string{"--progress", "--provenance"} {
			if strings.Contains(string(out), flag) {
				builderFlags.known[flag] = true
			}
		}
	})
	if !builderFlags.checked {
		// Nothing was learned, so nothing is claimed: pass no BuildKit flag, because a
		// flag passed blind is the failure this avoids.
		return false
	}
	return builderFlags.known[flag]
}

// cloneAddress is where this runner clones from, and it is not the address the core gives
// out.
//
// The core's address is written for a person: the host it is published under, on the port
// the outside world is given. A runner is usually not where that address points — it is a
// machine on the same network as the core, or across the world, and either way "the host
// the core publishes itself under" is not an answer about the host this runner can reach.
//
// So the host comes from where this runner already talks to the core, which it knows works,
// and the port is this runner's own to say. The default is twenty-two, because that is what
// an sshd in a container listens on: the port published to the outside is a different port
// whenever a deployment maps one onto the other, and naming that one here leaves every
// runner inside a network connecting to a port nothing is listening on.
func cloneAddress(cfg config) string {
	host := hostOf(cfg.coreURL)
	if host == "" {
		// No address to the core, which a runner cannot work at all; the host it was
		// configured with is the only thing left to try.
		host = cfg.sshHost
	}
	port := cfg.clonePort
	if port <= 0 {
		port = 22
	}
	if port == 22 {
		return "ssh://git@" + host
	}
	return fmt.Sprintf("ssh://git@%s:%d", host, port)
}

// hostOf is the host out of an address, without the scheme, the port or anything after it.
func hostOf(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if scheme, rest, found := strings.Cut(address, "://"); found && scheme != "" {
		address = rest
	}
	if slash := strings.IndexByte(address, '/'); slash >= 0 {
		address = address[:slash]
	}
	if host, _, found := strings.Cut(address, ":"); found {
		address = host
	}
	return address
}

func tokenOf(registry map[string]any) string {
	value, _ := registry["token"].(string)
	return value
}

// checkout makes a working copy for one job.
func checkout(ctx context.Context, cfg config, answer claim, workspace string) error {
	projectPath := answer.Job.ProjectPath

	// The key is asked for here, at the moment it is needed. The core may already
	// hold one for this job from an earlier attempt; if so it says so rather than
	// minting a second, and the private half the runner has is the one that works.
	// The address the core offers is not taken: it is the one a person clones from. The key
	// is the part of the answer a build cannot do without.
	_, privateKey, err := answer.core.jobKey(ctx, answer.Job.ID)
	if err != nil {
		return fmt.Errorf("ask for a project key: %w", err)
	}
	// Removed rather than emptied: git refuses to clone into a directory that
	// exists and is not empty, and a leftover from a previous attempt is not
	// something to merge into.
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(workspace), 0o750); err != nil {
		return err
	}

	// Over SSH, with a key the core minted for this job alone. The key is written to a
	// file with the permissions ssh insists on and removed with the workspace: it belongs
	// to one build and outlives nothing.
	//
	// The address is this runner's to work out rather than the core's to hand out, because
	// the core's address is the one a person uses. See cloneAddress.
	command := exec.CommandContext(ctx, "git", "clone", cloneAddress(cfg)+"/"+
		url.PathEscape(projectPath)+".git", workspace)

	environment := os.Environ()

	// The key lives in a file rather than on a command line, where any process on
	// the machine could read it out of the process list.
	keyPath := ""
	if privateKey != "" {
		// Outside the checkout: git creates that directory itself, and a key file
		// written into a path that does not exist yet is a key file nowhere.
		keyPath = filepath.Join(os.TempDir(), fmt.Sprintf("dogit-job-%d.key", answer.Job.ID))
		if err := os.WriteFile(keyPath, []byte(privateKey), 0o600); err != nil {
			return err
		}
		defer func() { _ = os.Remove(keyPath) }()

		environment = append(environment,
			"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -i "+keyPath)
	}
	command.Env = append(environment, "GIT_TERMINAL_PROMPT=0")

	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %w: %s", projectPath, err, strings.TrimSpace(string(output)))
	}

	// Onto the commit the run is about, not onto whatever the repository points at.
	//
	// The clone above landed on the default branch. For a run created against a
	// release tag or a working branch that is simply the wrong code, and nothing
	// downstream would notice: the build succeeds, an image is pushed, and the image
	// is of a commit nobody asked to build. Refused here rather than built from the
	// default branch, because a build of the wrong thing is worse than one that did
	// not happen — this one is reported as a pass.
	want := strings.TrimSpace(answer.Job.SHA)
	if want == "" {
		// No commit means an older core that does not say which one. Named rather than
		// guessed at: the checkout stays on the default branch, which is what this
		// runner did before commits were carried, and the log says so.
		log.Printf("module-runner: job %d carries no commit, building the default branch",
			answer.Job.ID)
		return nil
	}

	checkout := exec.CommandContext(ctx, "git", "checkout", "--detach", want)
	checkout.Dir = workspace
	checkout.Env = append(environment, "GIT_TERMINAL_PROMPT=0")
	if out, err := checkout.CombinedOutput(); err != nil {
		return fmt.Errorf("check out %s: %w: %s", want, err, strings.TrimSpace(string(out)))
	}

	// Said out loud, because a runner that quietly builds the wrong commit is the
	// worst kind of wrong: the build succeeds, an image is pushed, and everything
	// downstream reports a run that never happened.
	verify := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	verify.Dir = workspace
	verify.Env = append(environment, "GIT_TERMINAL_PROMPT=0")
	got, err := verify.Output()
	if err != nil {
		return fmt.Errorf("ask which commit was checked out: %w", err)
	}
	log.Printf("module-runner: job %d checked out %s (asked for %s)",
		answer.Job.ID, strings.TrimSpace(string(got)), want)
	return nil
}

// imageNamesFor is what an image is called here, in the order it should be named.
//
// Never "latest": that name means nothing, moves by itself, and is the usual way the
// wrong image reaches production. Every deploy here is addressed by digest anyway, so a
// tag is a label for a person, not an address for the system — which is exactly why
// having the commit's name beside it matters more than having "latest".
//
// A repository may name the image itself with `tag: $CI_COMMIT_TAG`. That name is
// honoured, and the commit is added beside it rather than replaced by it: a release
// that can only be found by a name somebody might reuse is harder to reason about later
// than one that also carries the commit it was built from.
func imageNamesFor(build map[string]any, environment map[string]string, atCommit []string) []string {
	short := environment["CI_COMMIT_SHORT_SHA"]
	tag := environment["CI_COMMIT_TAG"]

	wanted, _ := build["tag"].(string)
	wanted = strings.TrimSpace(wanted)

	names := []string{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		for _, existing := range names {
			if existing == name {
				return
			}
		}
		names = append(names, name)
	}

	// The file's own name first, because a repository that named its image expects to
	// find it under that name.
	add(wanted)

	// Every tag that points at this commit, which is what makes a release reachable by
	// the name it was released under. A repository tags a commit and then expects the
	// image to be there under that tag; an image pushed only under the short commit is
	// findable by nobody who remembers what they called the release.
	for _, name := range atCommit {
		add(name)
	}

	// And the commit itself, always: a tag can be moved or deleted, so a registry that
	// holds only tags cannot say afterwards what exactly was deployed. The short commit
	// is the one name that always exists, and it is what a rollback is addressed by.
	add(tag)
	add(short)

	// The common case: nothing named it and nothing was tagged, so the commit is the
	// name. Not an empty image and not "latest", which would let two different commits
	// answer to the same one.
	if len(names) == 0 && short != "" {
		return []string{short}
	}
	return names
}

// describeNames says what an image is being pushed as, in a form that fits on a line.
//
// A release pointed at by a dozen tags is pushed twelve times, and a reader of the log
// is entitled to know that before they watch it happen rather than after: "under 13
// names" and the first few of them is enough to understand both the count and the
// naming, where listing all thirteen would wrap the log into something unreadable.
func describeNames(names []string) string {
	if len(names) == 1 {
		return "one name (" + names[0] + ")"
	}
	const show = 4
	if len(names) <= show {
		return fmt.Sprintf("%d names (%s)", len(names), strings.Join(names, ", "))
	}
	return fmt.Sprintf("%d names (%s and %d more)", len(names),
		strings.Join(names[:show], ", "), len(names)-show)
}

// tagsAtCommit lists the tags pointing at the commit in this working copy.
//
// Asked of git rather than of the registry, because the registry can only say what
// happened to be pushed already and this is the question of what this commit is called.
// Sorted, so a build's list of names does not depend on the order git happened to
// return them in.
func tagsAtCommit(workspace string) []string {
	out, err := exec.Command("git", "-C", workspace, "tag", "--points-at", "HEAD").Output()
	if err != nil {
		// No git, no repository, or nothing tagged: all of which mean the same thing
		// here, which is that the commit is known only by itself.
		return nil
	}

	tags := []string{}
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			tags = append(tags, name)
		}
	}
	sort.Strings(tags)
	return tags
}

// buildAndPush builds the image a job asked for and pushes it to the registry.
//
// progress is where docker's own words go as they happen. It is not optional
// politeness: a build can take minutes, and without it the job's log is empty for
// the whole of it and then says one line. Somebody watching has nothing to watch.
func buildAndPush(ctx context.Context, cfg config, workspace string,
	registry map[string]any, build map[string]any, progress io.Writer,
	announce func(phase, message string), environment map[string]string) (string, error) {

	if registry == nil {
		return "", errors.New("this instance has no registry to push to")
	}

	image, _ := registry["image"].(string)
	if image == "" {
		return "", errors.New("the core did not say what the image is called")
	}
	// Two names for one image, when it has two.
	//
	// The tag says what the release is called and the short commit says what was
	// actually built. Both are pushed: a tag can be reused, so a registry holding only
	// tags cannot answer "what exactly was deployed then" a year later, and a name that
	// was built and never pushed is work for nothing. The second push costs one manifest
	// — the layers are addressed by their content and are already there.
	names := imageNamesFor(build, environment, tagsAtCommit(workspace))
	if len(names) == 0 {
		names = []string{"latest"}
	}
	full := image + ":" + names[0]

	contextPath := "."
	if value, ok := build["context"].(string); ok && value != "" {
		contextPath = value
	}
	file := "Dockerfile"
	if value, ok := build["dockerfile"].(string); ok && value != "" {
		file = value
	}

	// The build itself is plain docker: this is the machine that is allowed to run
	// it, which is the whole reason this process exists as its own binary.
	// One build, every name it will be known by. Building twice would be faster to
	// write and wrong: two builds of the same sources can differ, and a release whose
	// two names point at different images is a release nobody can reason about.
	args := []string{"build"}
	// Plain progress, asked for rather than inferred: docker decides between lines
	// and a spinner by whether it has a terminal, and what we want here is lines.
	if buildFlagKnown(cfg, "--progress") {
		args = append(args, "--progress=plain")
	}
	for _, name := range names {
		args = append(args, "-t", image+":"+name)
	}
	args = append(args, "-f", filepathJoin(workspace, file), workspace)
	// Provenance off.
	//
	// BuildKit attaches a record of how an image was built and pushes it beside the
	// image. The record names the repository it believes it is building, which is
	// not always the one being pushed, and the client then asks the registry for a
	// credential for that name as well. It is a build record, not part of the image,
	// and nothing here consumes it — so it is turned off rather than fought with.
	if buildFlagKnown(cfg, "--provenance") {
		args = append(args, "--provenance=false")
	}
	if contextPath != "." {
		args[len(args)-1] = filepathJoin(workspace, contextPath)
	}

	// Two things at once from one stream: the log for whoever is watching, and a
	// short tail for the error message. A build that fails says why in its last few
	// lines, and by then the whole of it has already gone to the log.
	buildTail := newTailWriter(20)
	if announce != nil {
		announce("build", "building "+file+" from "+contextPath)
	}
	if err := runStreaming(ctx, progress, buildTail, cfg.dockerBinary, args...); err != nil {
		if announce != nil {
			announce("build", "the build failed")
		}
		return "", fmt.Errorf("docker build: %w: %s", err, buildTail.String())
	}
	if announce != nil {
		announce("build", "the image is built")
	}

	address, _ := registry["url"].(string)
	if address == "" {
		return "", errors.New("the core did not say where the registry is")
	}

	// Signed in through the same token endpoint a person would use, so the module's
	// own checks apply to a build exactly as they do to a human.
	host := registryHost(address)
	token := tokenOf(registry)

	// A credential store of this job's own, and not the machine's.
	//
	// Docker keeps credentials per registry host in one file that belongs to the
	// machine, not to a build. Two jobs on one runner therefore share it: the
	// second finds the first project's token already there, logs in as that, and
	// pushes to its own repository with a credential for somebody else's project —
	// which is refused, correctly. It also means a build could inherit a login this
	// job was never given, which is exactly what a build must not be able to do.
	configDir, err := os.MkdirTemp("", "dogit-docker-config-")
	if err != nil {
		return "", fmt.Errorf("prepare a docker config: %w", err)
	}
	// Removed whatever happens, including on a crash: this file holds a credential.
	defer func() { _ = os.RemoveAll(configDir) }()

	if output, err := dockerWithConfig(ctx, configDir, cfg.dockerBinary, token, "login", host,
		"-u", "builder", "--password-stdin"); err != nil {
		return "", fmt.Errorf("docker login: %w: %s", err, strings.TrimSpace(output))
	}

	// No --progress here: it is a build flag, and push has never had one. With no
	// terminal attached docker writes plain lines for both, which is what a log
	// wants anyway — the spinner is for somebody sitting in front of it.
	pushTail := newTailWriter(20)
	if announce != nil {
		announce("push", "pushing to "+host+" under "+describeNames(names))
	}
	for i, name := range names {
		if err := runStreaming(ctx, progress, pushTail, cfg.dockerBinary,
			"--config", configDir, "push", image+":"+name); err != nil {
			if announce != nil {
				announce("push", "the push was refused")
			}
			return "", fmt.Errorf("docker push %s: %w: %s", name, err, pushTail.String())
		}
		if i < len(names)-1 {
			fmt.Fprintf(progress, "also as %s\n", image+":"+name)
		}
	}
	if announce != nil {
		// Named by digest, which is how a deployment refers to it and is the one name
		// here that cannot be reused or moved.
		//
		// And by the names it went out under, because a digest identifies an image to a
		// machine and to nobody else: the person watching wants to know that the thing
		// they released is the thing that was pushed, and "sha256:836f…" is not
		// something they can check that against.
		line := "the registry has it as " + digestOf(pushTail.String())
		if len(names) > 1 {
			line += " as " + strings.Join(names, ", ")
		}
		announce("push", line)
	}
	return full, nil
}

// dockerWithConfig runs a docker command against one credential store.
//
// The configuration directory is given as a flag rather than through the
// environment so that nothing else on the machine can be affected by where it
// points, and so that a build inside the container — which has its own view of the
// filesystem — cannot be handed a path it cannot reach.
func dockerWithConfig(ctx context.Context, configDir, binary, stdin string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, binary, append([]string{"--config", configDir}, args...)...)
	command.Env = append(os.Environ(), "DOCKER_CONFIG="+configDir)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	output, err := command.CombinedOutput()
	return string(output), err
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

// runStreaming runs a command and writes what it says as it says it.
//
// Both streams into one writer, because docker splits its output across them
// without meaning to: progress on stdout, the reason on stderr. Separated, the log
// reads as a build that printed nothing followed by an error out of nowhere.
func runStreaming(ctx context.Context, progress, tail io.Writer, name string, args ...string) error {
	command := exec.CommandContext(ctx, name, args...)
	both := io.MultiWriter(progress, tail)
	command.Stdout = both
	command.Stderr = both
	return command.Run()
}

// tailWriter keeps the last few lines and forgets the rest.
//
// A build prints thousands of lines; the reason it failed is always in the last
// few, and holding the whole of it to keep those would mean holding a large build
// in memory to answer for a sentence.
type tailWriter struct {
	lines []string
	limit int
}

func newTailWriter(limit int) *tailWriter { return &tailWriter{limit: limit} }

func (t *tailWriter) Write(data []byte) (int, error) {
	t.lines = append(t.lines, strings.SplitAfter(string(data), "\n")...)
	if len(t.lines) > t.limit {
		t.lines = t.lines[len(t.lines)-t.limit:]
	}
	return len(data), nil
}

// digestOf reads the digest out of what docker printed about a push.
//
// It is read from the output rather than asked of the registry: the push has just
// happened, the line is the authority on what was stored, and asking again would be
// a second request to learn something already in hand.
func digestOf(output string) string {
	match := regexp.MustCompile(`digest: (sha256:[0-9a-f]+)`).FindStringSubmatch(output)
	if len(match) < 2 {
		return "an unnamed image"
	}
	return match[1][:19]
}

func (t *tailWriter) String() string {
	return strings.TrimSpace(strings.Join(t.lines, ""))
}

// lastLines is the end of a build's output, which is where the reason is.
func lastLines(output string, count int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "; ")
}

// logWriter streams one of a job's output streams to the core as it is produced.
//
// Each writer flushes whole lines only: a half-written line sent on its own would
// arrive as two fragments, and a message cut in half is worse than one slightly
// late.
type logWriter struct {
	ctx    context.Context
	core   *coreClient
	jobID  int64
	stream string
	buffer strings.Builder
	last   time.Time
}

// The core is asked once every few seconds rather than once per line: a build
// prints thousands of lines, and a request each would cost more than the work.
const logFlushInterval = 2 * time.Second

func newLogWriter(ctx context.Context, core *coreClient, jobID int64, stream string) *logWriter {
	return &logWriter{ctx: ctx, core: core, jobID: jobID, stream: stream, last: time.Now()}
}

func (w *logWriter) Write(data []byte) (int, error) {
	w.buffer.Write(data)

	// A complete line goes as soon as it is complete; the rest waits for the next
	// flush so that a build printing without newlines still shows something.
	if strings.Contains(w.buffer.String(), "\n") || time.Since(w.last) >= logFlushInterval {
		w.flush()
	}
	return len(data), nil
}

// Close sends whatever is left, which is often the part that says why.
func (w *logWriter) Close() { w.flush() }

func (w *logWriter) flush() {
	text := w.buffer.String()
	if text == "" {
		return
	}

	// Only whole lines, so the core stores something it can colour line by line.
	lines := strings.SplitAfter(text, "\n")
	complete := lines[:len(lines)-1]
	if len(complete) == 0 {
		return
	}

	w.buffer.Reset()
	w.buffer.WriteString(lines[len(lines)-1])
	w.last = time.Now()

	if err := w.core.log(w.ctx, w.jobID, w.stream, strings.Join(complete, "")); err != nil {
		log.Printf("module-runner: could not send log output: %v", err)
	}
}

// ownEndpoints is what this module serves: enough for a probe, and nothing more.
func ownEndpoints(core *coreClient, runtime *runner.Docker, cfg config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/-/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, r *http.Request) {
		// Ready means this machine can actually run a job. Reporting ready without
		// checking would have the core hand work here that cannot start.
		if err := runtime.Available(r.Context()); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	})

	mux.HandleFunc("/module/manifest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, manifest(cfg))
	})

	mux.HandleFunc("/uninstall", uninstall(cfg))
	return mux
}

// uninstall removes this runner's leftovers, as the core asks.
func uninstall(cfg config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options []string `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)

		say := func(line map[string]any) {
			encoded, err := json.Marshal(line)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "%s\n", encoded)
			if flusher != nil {
				flusher.Flush()
			}
		}

		if contains(body.Options, "remove_workspaces") {
			say(map[string]any{"message": "removing build checkouts in " + cfg.workspace})
			removed, err := removeAll(cfg.workspace)
			if err != nil {
				say(map[string]any{"level": "error", "message": err.Error()})
				return
			}
			say(map[string]any{
				"summary": map[string]any{"removed_workspaces": removed},
			})
			return
		}

		say(map[string]any{"message": "leaving every checkout in place, as asked"})
		say(map[string]any{
			"message": "images built on this machine are not removed; prune them yourself if you mean it",
			"level":   "warn",
		})
		say(map[string]any{"summary": map[string]any{}})
	}
}

func removeAll(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}

	removed := 0
	for _, entry := range entries {
		if err := os.RemoveAll(dir + "/" + entry.Name()); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// coreClient talks to the dogit core.
type coreClient struct {
	baseURL string
	token   string
}

var errNothingToDo = errors.New("no work")

// post calls the core as the module itself.
func (c *coreClient) post(ctx context.Context, path string, body any, out any) error {
	return c.postAs(ctx, path, body, c.token, out)
}

// postAs calls the core with a particular credential, which is how registration
// presents the instance token rather than the module's own.
func (c *coreClient) postAs(ctx context.Context, path string, body any, token string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusUnauthorized {
		return errUnauthorized
	}
	// Any 2xx is the core saying yes. 204 in particular is what a call that has
	// nothing to return answers with, and treating it as a failure made every
	// progress report look like something that had gone wrong.
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return coreerr.Refusal(response)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

// coreRefusal is what the core said no, with the reason it gave.
//
// The status code on its own is a report about the server rather than about the problem, and
// this is the error a module prints when it cannot start — so an operator reading a crashloop
// sees "core said 400" and has nothing to act on. The core writes its reason into a small JSON
// object; the reason is taken out of it, and anything unparseable is passed through rather than
// dropped, because a body this code does not understand is still more than a number.
var errUnauthorized = errors.New("unauthorized")

func (c *coreClient) register(ctx context.Context, token, name, endpoint string, man map[string]any) (string, error) {
	var answer struct {
		Token string `json:"token"`
	}
	if err := c.postAs(ctx, "/api/v1/modules/register", map[string]any{
		"kind": runnerKind, "name": name, "endpoint": endpoint, "manifest": man,
	}, token, &answer); err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return answer.Token, nil
}

// claim asks for work. An empty queue is an answer, not a failure.
func (c *coreClient) claim(ctx context.Context, tags []string) (claim, error) {
	var answer claim
	if err := c.post(ctx, "/api/v1/module/runner/claim",
		map[string]any{"tags": tags}, &answer); err != nil {
		return answer, err
	}
	if answer.Job == nil {
		return answer, errNothingToDo
	}
	return answer, nil
}

// progress says which part of the work the runner is on.
//
// Sent because the log cannot: a build prints layer after layer, and a page
// watching it live cannot tell from those lines whether the image is being built
// or pushed. This names the phase, so the deploy page's own step list moves while
// the build is happening rather than after it.
//
// Best effort by design. A progress message that fails to arrive costs a page a
// step it would have shown a moment later; failing the job over it would mean the
// core's event feed could break a build, which is the wrong way round.
func (c *coreClient) progress(ctx context.Context, jobID int64, phase, message string) error {
	if phase == "" || message == "" {
		return nil
	}
	return c.post(ctx, fmt.Sprintf("/api/v1/module/runner/jobs/%d/progress", jobID),
		map[string]any{"phase": phase, "message": message}, nil)
}

// log sends part of a job's output, tagged with which stream it came from.
func (c *coreClient) log(ctx context.Context, jobID int64, stream, text string) error {
	if text == "" {
		return nil
	}
	return c.post(ctx, fmt.Sprintf("/api/v1/module/runner/jobs/%d/log", jobID),
		map[string]string{"stream": stream, "text": text}, nil)
}

// jobKey asks the core for the credential to clone with.
//
// Asked at the moment the clone is about to happen rather than when the job was
// claimed: a runner with one machine and several projects may hold a job for minutes
// before it begins, and a key whose short life was measured from the moment of
// claiming would be dead before the machine was ready.
func (c *coreClient) jobKey(ctx context.Context, jobID int64) (string, string, error) {
	var answer struct {
		CloneURL string `json:"clone_url"`
		Key      struct {
			PrivateKey  string `json:"private_key"`
			Fingerprint string `json:"fingerprint"`
		} `json:"key"`
	}

	if err := c.post(ctx, fmt.Sprintf("/api/v1/module/runner/jobs/%d/key", jobID),
		map[string]any{}, &answer); err != nil {
		return "", "", err
	}
	return answer.CloneURL, answer.Key.PrivateKey, nil
}

func (c *coreClient) finish(ctx context.Context, jobID int64, status string, duration time.Duration, reason string) {
	_ = c.post(ctx, fmt.Sprintf("/api/v1/module/runner/jobs/%d/finish", jobID), map[string]any{
		"status":      status,
		"duration_ms": duration.Milliseconds(),
		"error":       reason,
	}, nil)
}

// beat keeps the core informed the module is alive and what it can see.
func (c *coreClient) beat(ctx context.Context, stats map[string]any) error {
	return c.post(ctx, "/api/v1/module/heartbeat", map[string]any{"stats": stats}, nil)
}

// heartbeat is the same loop every module runs.
func heartbeat(ctx context.Context, core *coreClient, interval time.Duration, register func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := core.beat(ctx, stats())
			if err == nil {
				continue
			}
			if !errors.Is(err, errUnauthorized) {
				log.Printf("module-runner: heartbeat failed: %v", err)
				continue
			}
			log.Printf("module-runner: token rejected, re-registering")
			if err := register(); err != nil {
				log.Printf("module-runner: re-registration failed: %v", err)
			}
		}
	}
}

var startedAt = time.Now()

// stats is this runner's reading of the machine it is on.
//
// It is the only module that can report anything about the hardware, which is why
// it is a separate binary rather than part of the core: this is the machine, not
// the server.
func stats() map[string]any {
	reading := map[string]any{
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
	}

	// The workspace is the runner's own storage: it is what fills up, and it is what
	// gets cleaned. The node's disk is a fact about the machine rather than about
	// this module, so it goes in extra where the core shows it without pretending
	// it measured the thing the panel is about.
	if total, free, ok := diskFree(workspaceOf()); ok {
		reading["storage_total_bytes"] = total
		reading["storage_used_bytes"] = total - free
	}
	if total, free, ok := diskFree("/"); ok {
		reading["extra"] = map[string]string{
			"host_disk": humanBytes(total-free) + " of " + humanBytes(total),
		}
	}
	if memory := readMemTotal(); memory > 0 {
		reading["host_memory_total_bytes"] = memory
		reading["host_load1"] = load1()
	}

	extra := map[string]string{"docker": dockerVersion()}
	if cpus := countCPUs(); cpus > 0 {
		extra["cores"] = fmt.Sprint(cpus)
	}

	// The queue, and this runner's own use of it. Both are needed, and neither says
	// anything about the other: from inside the runner, a queue full of jobs and a queue
	// nobody has pushed to look exactly the same, and that is the whole difference between
	// "raise the concurrency" and "nothing is wrong".
	if queueKnown.Load() {
		extra["queue"] = fmt.Sprint(queueDepth.Load())
	} else {
		// Said plainly rather than left at zero, because a runner that has not heard
		// from the core must not look like one that has and found nothing.
		extra["queue"] = "unknown"
	}
	if runnerSlots != nil {
		extra["jobs"] = fmt.Sprintf("%d of %d running", len(runnerSlots), cap(runnerSlots))
	}
	// When work last arrived, because a runner idle for a minute and a runner idle for an
	// hour are different problems and look identical from the outside.
	if at := lastWorkAt.Load(); at > 0 {
		extra["last_work"] = time.Since(time.Unix(0, at)).Round(time.Second).String() + " ago"
	} else {
		extra["last_work"] = "none yet"
	}

	for key, value := range extra {
		if reading["extra"] == nil {
			reading["extra"] = map[string]string{}
		}
		reading["extra"].(map[string]string)[key] = value
	}
	return reading
}

/** Average load over the last minute, which is what people mean by "is it busy". */
func load1() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0
	}
	value, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return value
}

func workspaceOf() string { return envOr("DOGIT_RUNNER_WORKSPACE", "/data/work") }

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed := 0
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func registryHost(address string) string {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(address, "https://"), "http://")
	return strings.TrimSuffix(trimmed, "/")
}

func filepathJoin(parts ...string) string {
	return strings.Join(parts, "/")
}
