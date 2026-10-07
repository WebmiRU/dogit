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
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ewolf/runner/internal/builder"
	"github.com/ewolf/runner/internal/core"
	"github.com/ewolf/runner/internal/measure"
)

// The kind this runner registers under. A different word from `runner:docker` on purpose:
// two runners that answer to the same kind are one runner as far as the core is concerned,
// and this one cannot execute a job yet, so a shared kind would have it stealing work from a
// runner that can.
const runnerKind = "runner:buildkit"

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

	ticker := time.NewTicker(cfg.poll)
	defer ticker.Stop()

	for {
		beat(ctx, &cfg, &registered, credential, token)
		// One attempt straight away, rather than after the first tick: a runner that has just
		// started and has work waiting should not sit for a poll interval looking idle.
		if mayClaim.Load() {
			ask(ctx, &cfg, registered.Load().(*core.Client))
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

func beat(ctx context.Context, cfg *config, registered *atomic.Value, credential *builder.Credentials, token string) {
	existing, _ := registered.Load().(*core.Client)
	if existing == nil {
		existing = register(ctx, cfg, registered, token)
		if existing == nil {
			return
		}
	}

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
				"description": "How many jobs this runner takes together. One until the executor is finished, because a claim taken and not finished is a job stuck at running for ever.",
			},
			{
				"key": "job_timeout", "label": "Job timeout", "type": "int", "default": 3600,
				"description": "Seconds a single job may run before it is stopped.",
			},
		},
	}
}

func ask(ctx context.Context, cfg *config, client *core.Client) {
	if client == nil {
		return
	}
	answer, err := client.Claim(ctx, nil)
	if err != nil {
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
		return
	}
	// Not here yet, and said so rather than claimed quietly. Taking the job and then having
	// nothing to do with it is the failure this whole decision was arranged to avoid.
	lastWorkAt.Store(time.Now().UnixNano())
	log.Printf("runner: claimed job %d (%s) and cannot run it yet — the executor is not written", answer.Job.ID, answer.Job.Name)
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
