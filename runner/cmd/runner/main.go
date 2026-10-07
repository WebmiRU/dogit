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
	"sync/atomic"
	"syscall"
	"time"

	"github.com/ewolf/runner/internal/core"
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
	coreURL     string
	instance    string
	endpoint    string
	name        string
	buildkit    string
	concurrency int
	poll        time.Duration
	workspace   string
	claim       bool
}

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", env("DOGIT_CORE_URL", "http://app:8080"),
		"the dogit core to talk to")
	flag.StringVar(&cfg.instance, "registration-token", os.Getenv("DOGIT_REGISTRATION_TOKEN"),
		"the instance's registration token, which is how a module introduces itself")
	flag.StringVar(&cfg.endpoint, "endpoint", env("DOGIT_ENDPOINT", "http://runner:8092"),
		"the address the core and an operator will reach this runner at")
	flag.StringVar(&cfg.name, "name", env("DOGIT_RUNNER_NAME", "buildkit"),
		"what this runner calls itself when it asks for work")
	flag.StringVar(&cfg.buildkit, "buildkit", env("DOGIT_BUILDKIT_ADDR", "tcp://127.0.0.1:1234"),
		"the builder. Loopback, because it is in this pod: the runner and the daemon share a network namespace, and so does every RUN step the daemon executes — which is why the listener is still asked for a client certificate.")
	flag.StringVar(&cfg.workspace, "workspace", env("DOGIT_RUNNER_WORKSPACE", "/data/work"),
		"where checkouts live")
	flag.IntVar(&cfg.concurrency, "concurrency", envInt("DOGIT_RUNNER_CONCURRENCY", 1),
		"how many jobs at once")
	flag.DurationVar(&cfg.poll, "poll", envDuration("DOGIT_RUNNER_POLL", 3*time.Second),
		"how often to ask for work")
	flag.BoolVar(&cfg.claim, "claim", envBool("DOGIT_RUNNER_CLAIM", false),
		"ask for work. Off by default: a claim that cannot be finished strands the job.")
	flag.Parse()

	if cfg.instance == "" {
		log.Fatal("no registration token: set DOGIT_REGISTRATION_TOKEN or -registration-token")
	}
	if cfg.concurrency < 1 {
		cfg.concurrency = 1
	}
	mayClaim.Store(cfg.claim)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		beat(ctx, &cfg, &registered)
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
func beat(ctx context.Context, cfg *config, registered *atomic.Value) {
	existing, _ := registered.Load().(*core.Client)
	if existing == nil {
		existing = register(ctx, cfg, registered)
		if existing == nil {
			return
		}
	}

	stats := core.Stats{
		UptimeSeconds: pointer(int64(time.Since(startedAt).Seconds())),
		Extra:         map[string]string{},
	}
	stats.Extra["queue"] = queueWord()
	stats.Extra["jobs"] = fmt.Sprintf("0 of %d running", cfg.concurrency)
	stats.Extra["builder"] = cfg.buildkit
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

func register(ctx context.Context, cfg *config, registered *atomic.Value) *core.Client {
	token, err := core.New(cfg.coreURL, "").Register(ctx, cfg.instance, runnerKind, cfg.name, cfg.endpoint, manifest())
	if err != nil {
		log.Printf("runner: register: %v", err)
		return nil
	}
	client := core.New(cfg.coreURL, token)
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
