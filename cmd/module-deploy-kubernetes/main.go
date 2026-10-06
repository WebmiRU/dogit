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
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCoreURL = "http://app:8080"
	defaultListen  = ":8094"
	defaultName    = "kubernetes"
	deployKind     = "deploy:kubernetes"

	// defaultStateDir is where this module keeps the credentials it was handed once.
	defaultStateDir = "/var/lib/dogit-deploy"

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
	flag.StringVar(&cfg.stateDir, "state", envOr("DOGIT_MODULE_STATE", defaultStateDir),
		"directory this module keeps its own state in, including the database credentials "+
			"it was handed at registration and never given again")
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
	// The credentials arrive exactly once, at registration, so they are written down
	// here. Nothing else in the process keeps them, and a history that is empty after
	// a restart is a history that is empty exactly when somebody asks what was
	// deployed — a question nobody asks at the moment they are deploying.
	if core.databaseURL != "" {
		if err := rememberDatabase(cfg.stateDir+"/database", core.databaseURL); err != nil {
			log.Printf("module-deploy: the database could not be saved: %v", err)
		}
	} else if saved, ok := recallDatabase(cfg.stateDir + "/database"); ok {
		core.databaseURL = saved
		log.Printf("module-deploy: using the database saved earlier")
	}

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
	/** Where the database credentials handed over at registration are kept. */
	stateDir string
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
		// Declared, because the core decides who may look at what is deployed and who may
		// undo it, and it can only decide that about scopes a module says it has.
		"scopes": []string{"deploy:read", "deploy:write"},

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
					// What says which place an entry is: its name and the namespace it is
					// in. Both, because names need not be unique — two places called
					// "staging" in two namespaces are two places, and a list that could
					// only tell them apart by name could hold one of them. It is also what
					// lets a group or a project change one place without restating the
					// rest: the list is inherited entry by entry from the level above.
					"identify":  []string{"name", "default_namespace"},
					"add_label": "Add a cluster",
					"fields": []map[string]any{
						{
							"key":   "name",
							"label": "Name",
							"type":  "string",
							// What a deployment refers to. Names need not be unique: a
							// configuration that says `place: staging` deploys into every
							// place with that name, so two of them in two namespaces is a
							// way of saying "both of these", not a conflict to be prevented.
							"description": "What this place is called. A deployment names the place it " +
								"goes to, and every place with this name is deployed to — so two " +
								"places may share a name, as long as they are in different " +
								"namespaces. Which cluster this is comes from the kubeconfig " +
								"below, not from here.",
						},
						{
							"key":   "kubeconfig",
							"label": "Kubeconfig",
							// A multi-line field rather than a password box: a kubeconfig is a
							// document, and pasting one into a single-line input would trim it.
							"type": "text",
							"description": "The contents of a kubeconfig, pasted in, for when this module runs " +
								"outside the cluster. `kubectl config view --raw` prints one. Only an " +
								"administrator sees this page, so it is shown back like any other " +
								"setting: a masked credential is one nobody can check is still there.",
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
						{
							// Both of these used to be one setting each for the whole module,
							// which is the wrong size for either: a cluster that rolls out in
							// twenty seconds and one that takes four minutes are both normal,
							// and a single number is either an endless wait on the fast one or a
							// rollout declared stuck on the slow one.
							"key":   "rollout_timeout",
							"label": "Rollout timeout",
							"type":  "int",
							"description": "Seconds to wait for this cluster's rollout before a deployment is " +
								"called stuck. Empty means ten minutes. A project's own configuration may " +
								"ask for less, and does so for one run.",
						},
						{
							"key":   "registry",
							"label": "Registry",
							"type":  "registry",
							"description": "Which registry this place pulls its images from. Empty means the " +
								"registry this instance runs, which is where the images were pushed — the " +
								"ordinary case, and the one to leave alone unless this cluster cannot " +
								"reach that address. Name a mirror, or another address for the same images, " +
								"when it cannot: the path and the digest stay exactly as they were, so what " +
								"is rolled out is still the thing that was built. One registry per place " +
								"on purpose — an image that lives in two registries under one name is two " +
								"images with one name, and only the first one written is ever found. A " +
								"registry picked from the list of registries brings its own login; an address " +
								"that is not on that list is pulled from without a credential, which is what " +
								"a public mirror needs.",
						},
						{
							"key":   "keep_jobs",
							"label": "Keep finished jobs",
							"type":  "bool",
							"description": "Leave this cluster's migration and check Jobs in place after they " +
								"finish. Off by default: a namespace full of finished Jobs is a namespace " +
								"nobody can read. It does nothing at all where the project declares no " +
								"pre or post steps, because then no Jobs are made.",
						},
					},
				},
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
	mux.HandleFunc("POST /revert", core.handleRevert)
	mux.HandleFunc("GET /deployments", core.handleDeployments)
	mux.HandleFunc("GET /images", core.handleImages)
	mux.HandleFunc("GET /current", core.handleCurrent)
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

// rememberDatabase writes the connection string to the module's own state.
//
// The core hands it over exactly once, at registration, and never again — that is the
// whole point of not storing a password in a table everybody can read. So the module
// keeps it, in a file on a volume that is its own, with permissions only it can read.
func rememberDatabase(path, url string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(url), 0o600)
}

// recallDatabase is what was written earlier, if it is still there.
//
// A missing file is not a failure: a module being run without a volume is a module
// whose history lasts until it restarts, which is better than a module that refuses
// to deploy anything at all.
func recallDatabase(path string) (string, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	url := strings.TrimSpace(string(contents))
	return url, url != ""
}
