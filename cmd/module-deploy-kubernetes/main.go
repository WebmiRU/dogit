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

	// For the one setting this module names that the core also has to read by name. The
	// import is here for that constant alone, and it is still the right place: the key is
	// written down once rather than twice, and a module asking for a lifetime under a name
	// the core does not read is a setting that silently does nothing.
	"github.com/ewolf/dogit/internal/modulechan"
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

	// Where its history lives, from the setting an administrator filled in.
	//
	// Read on every start rather than remembered from registration. The credentials used to
	// arrive exactly once and be written down here forever, which meant a password nobody
	// could rotate: the core did not keep it, so changing it meant changing it in the
	// database and re-issuing it by hand to a module that would not ask again. A setting is
	// read, not remembered.
	core.databaseURL = core.databaseSetting(ctx)
	if core.databaseURL == "" {
		log.Printf("module-deploy: no database is configured, and this module keeps no history " +
			"without one — deployments will run and nothing will be remembered")
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
	} else {
		// Said out loud, because this is the one way to be found without waiting for
		// somebody to notice.
		//
		// The core hands the credentials over once, and hands over nothing after that,
		// so a module that has lost the file where it kept them will be given none and
		// cannot ask again: recovering is the administrator's job, and it has to start
		// with somebody knowing this happened. Everything else about this module still
		// works, and the deployments it does are recorded nowhere — which reads from the
		// outside exactly like a module that has simply never deployed anything. Silence
		// here would be the same as a healthy module that happens to have no history,
		// and those two are told apart only by this line.
		log.Printf("module-deploy: no saved database, deployments will not be remembered")
	}

	go heartbeat(ctx, core, cfg.interval, reRegister)

	// The channel to the core, for the questions a page has to have answered before it can
	// draw anything. A deployment still goes over HTTP, because a deploy narrates itself line
	// by line and that is a stream rather than an answer — see channel.go for why that one was
	// left alone.
	//
	// Started after registration because the channel authenticates with the token registration
	// hands back, and it reconnects on its own, so nothing else here has to notice.
	go serve(ctx, &modulechan.Client{
		URL:   cfg.coreURL,
		Token: core.token,
		Log:   channelLog{},
	}, core)

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

		// Its database is a setting, and an administrator fills it in.
		//
		// Not something the core provisions: a module can have no database, can have two,
		// or can keep everything in a file, and it can be somebody else's program on a host
		// this instance cannot reach. The one thing the core can honestly do is refuse to let
		// this module register until somebody has said where its data lives.
		//
		// Required for the reason the flag exists at all: without it this module still
		// deploys, still answers, and records nothing, which from the outside is exactly a
		// module that has never deployed anything. Secret because it is a password.
		//
		// The value is the connection string, whole, rather than five fields: what a driver
		// accepts is the driver's business, and a core that picked a shape would be picking
		// it on this module's behalf.

		"settings": []map[string]any{{
			"key":      databaseSettingKey,
			"type":     "text",
			"label":    "Database",
			"secret":   true,
			"required": true,
			"description": "Where this module keeps what it deployed. It deploys without one " +
				"and records nothing, which looks exactly like a module that has never deployed " +
				"anything — so it will not register until this is filled in. Any form the " +
				"driver accepts.",
		},

			{
				// The one setting on this page that is about the core rather than about a
				// place. It is here because this is the module that gets commands — a deploy
				// that arrives is a deploy that happens — and because a setting nobody can
				// find is a setting nobody can raise.
				//
				// No default on purpose: an empty field and a field saying five minutes have
				// to behave the same, and the empty one is what every module registered
				// before this setting existed already has. Writing 300 here would make the
				// two look different on the page while behaving alike.
				"key":   modulechan.CommandTTLSetting,
				"label": "Command lifetime",
				"type":  "int",
				"description": "Seconds the core keeps a command for this module, so one made " +
					"while it was down is still delivered when it comes back. 0 means keep nothing: " +
					"a command arrives once or not at all. Empty means five minutes.",
			},
			{
				// The value a new place starts with, and the only thing this setting is.
				//
				// It is a default for creation, not a pointer: nothing reads it during a
				// deployment, so changing it moves no place and a place that was created
				// while it said something keeps saying that. The alternative — a "the
				// default registry" switch that places follow — is a thing nobody can
				// point at afterwards: a deployment from March is looked up and its
				// registry is whatever the setting says today, which is a history that
				// rewrites itself.
				"key":   "registry",
				"label": "Default registry",
				"type":  "registry",
				"description": "What a place offered for the first time starts with. It applies to " +
					"rows added from now on and to nothing else: no deployment reads it, changing it " +
					"moves no place, and a place that names a registry keeps that one whatever this " +
					"says. Left empty, a new place starts empty too — which is a row somebody has to " +
					"fill in, on purpose.",
			},
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
							// Shown here, to an administrator, and never sent below this level.
							// Not marked secret: a masked kubeconfig is one nobody can check is
							// still there, and the person pasting a certificate bundle needs to
							// see what they pasted. Marked inheritable it would be nothing —
							// which is the point: a value goes to the levels below only when the
							// module says so, and a cluster's keys are not something it says.
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
							"key":         "default_namespace",
							"inheritable": true,
							"label":       "Default namespace",
							"type":        "string",
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
							"key":         "rollout_timeout",
							"inheritable": true,
							"label":       "Rollout timeout",
							"type":        "int",
							"description": "Seconds to wait for this cluster's rollout before a deployment is " +
								"called stuck. Empty means ten minutes. A project's own configuration may " +
								"ask for less, and does so for one run.",
						},
						{
							// Published downward, because where a cluster pulls from is a
							// fact about the cluster and not a secret: a project's page says
							// it, so that somebody reading a project can see where its images
							// come from without being an administrator. It is an address, not
							// a credential — the login for it stays in the core and is never
							// sent to a page below this one.
							"key":         "registry",
							"inheritable": true,
							"label":       "Registry",
							"type":        "registry",
							"description": "Which registry this place pulls its images from, and it has to be " +
								"named: an empty row is refused with a sentence saying so, rather than " +
								"deployed from wherever the image happened to be pushed. Name a mirror, " +
								"or another address for the same images, when this cluster cannot reach " +
								"the one this instance runs: the path and the digest stay exactly as they " +
								"were, so what is rolled out is still the thing that was built. One " +
								"registry per place on purpose — an image that lives in two registries " +
								"under one name is two images with one name, and only the first one " +
								"written is ever found. Pick one from the list of registries and it " +
								"brings its own login; an address that is not on that list is refused " +
								"until somebody writes it down, because dogit holds no credential for a " +
								"registry it has never heard of and a cluster left to find that out " +
								"waits at ImagePullBackOff. A new row starts with whatever the " +
								"Default registry above says.",
						},
						{
							"key":         "keep_jobs",
							"inheritable": true,
							"label":       "Keep finished jobs",
							"type":        "bool",
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
	mux.HandleFunc("POST /images-availability", core.handleImagesAvailability)
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
