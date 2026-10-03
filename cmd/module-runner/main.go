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
	"strings"
	"syscall"
	"time"

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
	flag.StringVar(&cfg.workspace, "workspace", envOr("DOGIT_RUNNER_WORKSPACE", "/data/work"),
		"where checkouts are made")
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
	loop(ctx, core, runtime, cfg)

	log.Printf("module-runner: stopped")
}

// logAdapter is the small translation between this process's logger and the
// runtime's interface, which wants the level named as a method.
type logAdapter struct{}

func (logAdapter) Warn(msg string, args ...any)  { log.Printf("runner: "+msg, args...) }
func (logAdapter) Info(msg string, args ...any)  { log.Printf("runner: "+msg, args...) }
func (logAdapter) Debug(msg string, args ...any) { log.Printf("runner: "+msg, args...) }

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
}

// manifest is what this runner says it can do.
func manifest(cfg config) map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Runs CI jobs with Docker, and builds images on machines that are allowed to",
		"scopes":      []string{},
		"database":    false,

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
	ID          int64          `json:"id"`
	IID         int            `json:"iid"`
	Name        string         `json:"name"`
	Stage       string         `json:"stage"`
	Image       string         `json:"image"`
	Script      []string       `json:"script"`
	ProjectPath string         `json:"project_path"`
	SHA         string         `json:"-"`
	Build       map[string]any `json:"build,omitempty"`
}

// claim is the core's answer to "is there anything for me".
type claim struct {
	Job *job `json:"job"`
	// Registry is what a job that builds an image needs: the address, the name the
	// registry's own rule gives it, and a credential scoped to that project alone.
	Registry map[string]any `json:"registry,omitempty"`
	// CloneURL and CloneToken are how this machine gets the code. A runner is a
	// machine the deployment knows nothing about, so it cannot already hold a key on
	// the project; the credential is minted for this job and for nothing else.
	CloneURL   string `json:"clone_url,omitempty"`
	CloneToken string `json:"clone_token,omitempty"`
	LogKey     string `json:"log_key,omitempty"`
}

type registryAccess struct {
	URL         string `json:"url"`
	InternalURL string `json:"internal_url"`
	Image       string `json:"image"`
	Token       string `json:"token"`
}

// loop takes work until the context is cancelled.
func loop(ctx context.Context, core *coreClient, runtime *runner.Docker, cfg config) {
	slots := make(chan struct{}, cfg.concurrency)
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

	go func() {
		defer func() { <-slots }()
		runJob(ctx, core, runtime, cfg, answer)
	}()
}

func (cfg config) tags() []string {
	return []string{"docker"}
}

// runJob runs one job and reports what happened.
//
// The order matters: the log goes up as it is produced, so a runner that dies
// halfway still leaves behind everything it managed to say. Waiting until the end
// would lose exactly the part worth keeping.
func runJob(ctx context.Context, core *coreClient, runtime *runner.Docker, cfg config, answer claim) {
	job := answer.Job
	started := time.Now()

	log.Printf("module-runner: job %d (%s) in %s", job.ID, job.Name, job.ProjectPath)

	// The checkout is what the script works in. Cloning per job costs a little and
	// saves a lot of confusion about which commit was built.
	workspace := fmt.Sprintf("%s/%s/job-%d", strings.TrimRight(cfg.workspace, "/"),
		strings.ReplaceAll(job.ProjectPath, "/", "-"), job.ID)

	spec := runner.JobSpec{
		ID:          job.ID,
		Name:        job.Name,
		Image:       runner.ImageTag(job.Image),
		Script:      job.Script,
		Environment: jobEnvironment(job, answer.Registry),
		WorkingDir:  "/build",
		Timeout:     cfg.jobTimeout(),
		Volumes: []runner.Volume{{
			Name:     "checkout",
			Source:   workspace,
			Target:   "/build",
			ReadOnly: false,
		}},
	}

	// The checkout happens on this machine rather than inside the job's container:
	// a container that is only allowed to run a script has no business being
	// allowed to clone.
	if err := checkout(ctx, cfg, answer, workspace); err != nil {
		log.Printf("module-runner: cannot check out %s: %v", job.ProjectPath, err)
		core.finish(ctx, job.ID, "failed", time.Since(started), err.Error())
		return
	}

	handle, err := runtime.Prepare(ctx, spec)
	if err != nil {
		core.finish(ctx, job.ID, "failed", time.Since(started), err.Error())
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
	if len(job.Build) > 0 {
		buildDone := make(chan struct{})
		go func() {
			defer close(buildDone)
			tag, err := buildAndPush(ctx, cfg, workspace, answer.Registry, job.Build)
			if err != nil {
				log.Printf("module-runner: build failed: %v", err)
				return
			}
			pushed = tag
		}()
		defer func() { <-buildDone }()
	}

	result := runtime.Stream(ctx, handle, spec, newLogWriter(ctx, core, job.ID))

	status := "success"
	if result.Status != runner.StatusSuccess {
		status = "failed"
	}

	if pushed != "" {
		core.log(ctx, job.ID, fmt.Sprintf("\nimage pushed: %s\n", pushed))
	}

	core.finish(ctx, job.ID, status, time.Since(started), resultMessage(result))
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

func tokenOf(registry map[string]any) string {
	value, _ := registry["token"].(string)
	return value
}

// checkout makes a working copy for one job.
func checkout(ctx context.Context, cfg config, answer claim, workspace string) error {
	projectPath, token := answer.Job.ProjectPath, answer.CloneToken
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, 0o750); err != nil {
		return err
	}

	// The core serves the repository over its own HTTP API, which is how a runner
	// outside the deployment network gets at code that lives nowhere else. The
	// credential goes in the URL because git has nowhere else to put it, and it is
	// this job's own: a runner that leaked it would leak access to one project for
	// two hours.
	base := strings.TrimRight(cfg.coreURL, "/")
	if address := strings.TrimSpace(answer.CloneURL); address != "" {
		base = strings.TrimRight(address, "/")
	}
	clone := fmt.Sprintf("%s/api/v1/projects/%s/repository/clone",
		base, url.PathEscape(projectPath))

	if token != "" {
		clone = strings.Replace(clone, "://", "://builder:"+token+"@", 1)
	}

	command := exec.CommandContext(ctx, "git", "clone", clone, workspace)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git clone %s: %w: %s", projectPath, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// buildAndPush builds the image a job asked for and pushes it to the registry.
func buildAndPush(ctx context.Context, cfg config, workspace string,
	registry map[string]any, build map[string]any) (string, error) {

	if registry == nil {
		return "", errors.New("this instance has no registry to push to")
	}

	image, _ := registry["image"].(string)
	if image == "" {
		return "", errors.New("the core did not say what the image is called")
	}
	tag, _ := build["tag"].(string)
	if tag == "" {
		tag = "latest"
	}
	full := image + ":" + tag

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
	args := []string{"build", "-t", full, "-f", filepathJoin(workspace, file), workspace}
	if contextPath != "." {
		args[len(args)-1] = filepathJoin(workspace, contextPath)
	}

	if output, err := run(ctx, cfg.dockerBinary, args...); err != nil {
		return "", fmt.Errorf("docker build: %w: %s", err, lastLines(output, 20))
	}

	address, _ := registry["url"].(string)
	if address == "" {
		return "", errors.New("the core did not say where the registry is")
	}

	// Signed in through the same token endpoint a person would use, so the module's
	// own checks apply to a build exactly as they do to a human.
	host := registryHost(address)
	token := tokenOf(registry)

	login := exec.CommandContext(ctx, cfg.dockerBinary, "login", host, "-u", "builder",
		"--password-stdin")
	login.Stdin = strings.NewReader(token)
	if output, err := login.CombinedOutput(); err != nil {
		return "", fmt.Errorf("docker login: %w: %s", err, strings.TrimSpace(string(output)))
	}
	defer func() {
		_, _ = run(context.WithoutCancel(ctx), cfg.dockerBinary, "logout", host)
	}()

	if output, err := run(ctx, cfg.dockerBinary, "push", full); err != nil {
		return "", fmt.Errorf("docker push: %w: %s", err, lastLines(output, 20))
	}
	return full, nil
}

func run(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	return string(output), err
}

// lastLines is the end of a build's output, which is where the reason is.
func lastLines(output string, count int) string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "; ")
}

// logWriter streams a job's output to the core as it is produced.
type logWriter struct {
	ctx    context.Context
	core   *coreClient
	jobID  int64
	buffer strings.Builder
	last   time.Time
}

// The core is asked once every few seconds rather than once per line: a build
// prints thousands of lines, and a request each would cost more than the work.
const logFlushInterval = 2 * time.Second

func newLogWriter(ctx context.Context, core *coreClient, jobID int64) *logWriter {
	return &logWriter{ctx: ctx, core: core, jobID: jobID, last: time.Now()}
}

func (w *logWriter) Write(data []byte) (int, error) {
	w.buffer.Write(data)

	if time.Since(w.last) >= logFlushInterval {
		w.flush()
	}
	return len(data), nil
}

// Close sends whatever is left, which is the part that usually says why.
func (w *logWriter) Close() { w.flush() }

func (w *logWriter) flush() {
	if w.buffer.Len() == 0 {
		return
	}
	text := w.buffer.String()
	w.buffer.Reset()
	w.last = time.Now()

	if err := w.core.log(w.ctx, w.jobID, text); err != nil {
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
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("core said %d", response.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

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

func (c *coreClient) log(ctx context.Context, jobID int64, text string) error {
	if text == "" {
		return nil
	}
	return c.post(ctx, fmt.Sprintf("/api/v1/module/runner/jobs/%d/log", jobID),
		map[string]string{"text": text}, nil)
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
		"extra": map[string]any{
			"docker": dockerVersion(),
		},
	}
	if total, free, ok := diskFree(workspaceOf()); ok {
		reading["host_disk_total_bytes"] = total
		reading["host_disk_used_bytes"] = total - free
	}
	if cpus, memory, ok := machine(); ok {
		reading["host_cpu_count"] = cpus
		reading["host_memory_total_bytes"] = memory
	}
	return reading
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
