// Command module-deploy-kubernetes rolls a project's built image onto a Kubernetes
// cluster.
//
// It is a module rather than a part of the core for the same reason every other
// module is one: the core has no idea Kubernetes exists, and the day somebody deploys
// to Nomad or unpacks an archive over SSH, that is another module rather than another
// branch in the core. What this module does is apply what a repository wrote, read
// back what the cluster says, and keep the history that makes an undo possible.
//
// What it decides nothing about: what should be deployed, and whether it should be.
// That is the repository's, in .dogit-ci.yml, and the core's job before it gets here.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCoreURL = "http://app:8080"
	defaultListen  = ":8094"
	defaultName    = "kubernetes"
	deployKind     = "deploy:kubernetes"

	// maxBody is how much of a request is read. A deploy carries manifests and images;
	// anything much larger than this is not a deploy.
	maxBody = 4 << 20
)

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL),
		"base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token", os.Getenv("DOGIT_MODULE_TOKEN"),
		"instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", "http://module-deploy:8094"),
		"address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", defaultName),
		"module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", defaultListen),
		"address this module listens on")
	flag.DurationVar(&cfg.interval, "heartbeat", 60*time.Second, "heartbeat interval")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module-deploy: a registration token is required (DOGIT_MODULE_TOKEN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	core := &coreClient{baseURL: strings.TrimRight(cfg.coreURL, "/")}

	token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint)
	if err != nil {
		log.Fatalf("module-deploy: registration failed: %v", err)
	}
	core.token = token
	log.Printf("module-deploy: registered, heartbeat every %s", cfg.interval)

	reRegister := func() error {
		token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint)
		if err != nil {
			return err
		}
		core.token = token
		log.Printf("module-deploy: re-registered")
		return nil
	}

	// The credentials arrive exactly once, at registration, and the module keeps them.
	// There is nowhere else to keep them: a module is a separate process with its own
	// secret, and a history that lives only in memory is a history that is empty after
	// a restart — which is exactly when somebody asks what was deployed.
	if core.databaseURL != "" {
		history, err := openHistory(ctx, core.databaseURL)
		if err != nil {
			// Not fatal, and deliberately so: refusing to start means a module that
			// cannot reach its database looks like a module that does not work at all,
			// and the deployments it could do are the ones somebody needs right now.
			log.Printf("module-deploy: no history store, deployments will not be remembered: %v", err)
		} else {
			defer history.Close(ctx)
			core.history = history
			log.Printf("module-deploy: history store ready")
		}
	}

	go heartbeat(ctx, core, cfg.interval, reRegister)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           ownEndpoints(core),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("module-deploy: listening on %s", cfg.listen)
		_ = server.ListenAndServe()
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	<-ctx.Done()
	log.Printf("module-deploy: stopped")
}

type config struct {
	coreURL           string
	registrationToken string
	endpoint          string
	name              string
	listen            string
	interval          time.Duration
}

// manifest is what this module says it does.
//
// The settings are the clusters, as a list rather than a flat set of keys: one cluster
// is a set of values that belong together, and a second cluster of a different kind —
// one in the cluster itself, one reached through a kubeconfig somebody copied — cannot
// be expressed as "the" chat id or "the" kubeconfig.
func manifest() map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Deploys a project's built image onto a Kubernetes cluster",
		"scopes":      []string{},

		// It asks the core for a database of its own. The history is this module's
		// business and nobody else's, and a module without one would be asking the core
		// to keep deployment records — which is how the core starts knowing what a
		// cluster is.
		"database": true,

		"settings": []map[string]any{
			{
				"key":   "clusters",
				"label": "Clusters",
				"type":  "list",
				"description": "Where this module may deploy. A project picks one of these by name, " +
					"so the address lives here once rather than in every repository that deploys there.",
				"items": map[string]any{
					"add_label": "Add a cluster",
					"fields": []map[string]any{
						{
							"key":   "name",
							"label": "Name",
							"type":  "string",
							"description": "What the project's configuration calls this cluster, such as " +
								"production-eu. Changing it does not move anything already deployed.",
						},
						{
							"key":     "in_cluster",
							"label":   "Runs inside this cluster",
							"type":    "bool",
							"default": false,
							"description": "On when this module is deployed into the cluster itself: it then " +
								"uses the pod's own service account and no credentials are stored anywhere. " +
								"Off means a kubeconfig is needed.",
						},
						{
							"key":   "kubeconfig",
							"label": "Kubeconfig",
							// A multi-line field rather than a password box: a kubeconfig is a
							// document, and pasting one into a single-line input would trim it.
							"type":   "text",
							"secret": true,
							"description": "The contents of a kubeconfig, pasted in, for when this module runs " +
								"outside the cluster. `kubectl config view --raw` prints one. It is stored " +
								"and never shown again, so keep the original file somewhere.",
						},
						{
							"key":         "context",
							"label":       "Context",
							"type":        "string",
							"description": "Which context in that file to use. Empty means the file's current one.",
						},
						{
							"key":   "default_namespace",
							"label": "Default namespace",
							"type":  "string",
							"description": "Where a project that does not name a namespace deploys to. dogit " +
								"never creates namespaces: one is somebody's decision, made where they can see " +
								"what is already in it.",
						},
					},
				},
			},
			{
				"key":         "default_rollout_timeout",
				"label":       "Rollout timeout",
				"type":        "int",
				"default":     600,
				"description": "Seconds to wait for a rollout before a deployment is called stuck. A project's own configuration may ask for less.",
			},
			{
				"key":         "keep_jobs",
				"label":       "Keep finished jobs",
				"type":        "bool",
				"default":     false,
				"description": "Leave the migration and check Jobs in place after they finish. Off by default: a namespace full of finished Jobs is a namespace nobody can read.",
			},
		},
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// ownEndpoints is what this module answers to.
//
// Only its own address, and only these: a module serves its own page and its own
// requests, and the browser is given a credential to reach it rather than the core
// relaying on its behalf.
func ownEndpoints(core *coreClient) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /deploy", core.handleDeploy)
	mux.HandleFunc("POST /rollback", core.handleRollback)
	mux.HandleFunc("GET /deployments", core.handleDeployments)
	mux.HandleFunc("POST /clusters/test", core.handleTestCluster)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	return withLogging(mux)
}

// withLogging is one line per request, because a module that says nothing is a module
// that cannot be diagnosed.
func withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("module-deploy: %s %s in %s", r.Method, r.URL.Path, time.Since(started).Round(time.Millisecond))
	})
}

func decode(r *http.Request, into any) error {
	body := http.MaxBytesReader(nil, r.Body, maxBody)
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Errorf("read the request: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, into); err != nil {
		return fmt.Errorf("the request is not valid JSON: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message}})
}

var errNoHistory = errors.New("this module has no history store, so it cannot remember deployments")
