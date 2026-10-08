// Command module-registry is the container image registry.
//
// It is a proxy, not a registry: registry:2 does the storage, and this process
// stands in front of it deciding who may do what. The decision is not its own —
// every request is settled by asking the core, which is the only place that knows
// what a user may do with a project. The module keeps no users of its own, so an
// account removed in dogit stops pushing images at the very next request.
//
//	image:docker  →  this module  →  registry:2
//
// The module writes nothing but its manifest, its log and its own answer to the
// core. Everything else belongs to somebody else.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ewolf/dogit/internal/coreerr"
	"github.com/ewolf/dogit/internal/modulechan"
)

const (
	defaultCoreURL     = "http://app:8080"
	defaultListen      = ":8091"
	defaultName        = "registry"
	defaultUpstream    = "http://registry:5000"
	defaultImageName   = "{{group}}/{{project}}"
	imageNameMandatory = "{{project}}"
)

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL),
		"base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token", os.Getenv("DOGIT_MODULE_TOKEN"),
		"instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", "http://module-registry:8091"),
		"address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", defaultName),
		"module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", defaultListen),
		"address this module listens on")
	flag.StringVar(&cfg.upstream, "registry", envOr("REGISTRY_URL", defaultUpstream),
		"address of the registry that stores the images")
	flag.DurationVar(&cfg.interval, "heartbeat", 30*time.Second, "heartbeat interval")
	// Written into the manifest's default rather than into the settings store: a
	// deployment knows at start-up where it publishes itself, and the administrator
	// can still change it in the interface afterwards.
	flag.StringVar(&cfg.publicAddress, "public-address", envOr("DOGIT_REGISTRY_PUBLIC_ADDRESS", ""),
		"default public address of this registry")
	flag.StringVar(&cfg.publicURL, "public-url", envOr("DOGIT_MODULE_PUBLIC_URL", ""),
		"address clients use to reach this module; the token endpoint is named by it")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module-registry: a registration token is required (DOGIT_MODULE_TOKEN)")
	}
	if _, err := url.Parse(cfg.upstream); err != nil {
		log.Fatalf("module-registry: the registry address %q is not a URL: %v", cfg.upstream, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	core := &coreClient{baseURL: strings.TrimRight(cfg.coreURL, "/")}

	token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg.publicAddress))
	if err != nil {
		log.Fatalf("module-registry: registration failed: %v", err)
	}
	core.token = token
	log.Printf("module-registry: registered, heartbeat every %s, upstream %s",
		cfg.interval, cfg.upstream)

	// The same call on recovery: a module that restarts comes back with a new
	// address and must not appear as a second module.
	register := func() error {
		token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg.publicAddress))
		if err != nil {
			return err
		}
		core.token = token
		log.Printf("module-registry: re-registered")
		return nil
	}

	go heartbeat(ctx, core, cfg.interval, register)

	// One registry for both halves of this module. The proxy and the channel consult the same
	// policy — a resolve answered over the socket and the same resolve answered over HTTP must
	// reach the same conclusion about who may pull what — and two objects would be two policies.
	registry := newRegistry(core, cfg.endpoint)

	// The channel to the core, and what it changes: the core can ask this module a question
	// without holding a request open against an endpoint it has to be able to reach.
	//
	// Started after registration because the channel authenticates with the token registration
	// hands back.
	go serve(ctx, &modulechan.Client{
		URL:   cfg.coreURL,
		Token: core.token,
		Log:   channelLog{},
	}, registry)

	// The address a client is told to fetch tokens from has to be one the client can
	// resolve. The address the core uses is the internal one, which a docker client
	// on somebody's laptop cannot reach; an installation that has both sets both.
	public := cfg.publicURL
	if public == "" {
		public = cfg.endpoint
	}

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           newHandler(registry, cfg.upstream),
		ReadHeaderTimeout: 10 * time.Second,
		// A layer is uploaded as one long request, and a manifest is read as a
		// stream. The defaults are tuned for forms and time them both out.
		WriteTimeout: 0,
		IdleTimeout:  5 * time.Minute,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("module-registry: listening on %s", cfg.listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("module-registry: %v", err)
	}
}

// manifest is what this module says it is and what it needs.
//
// The wording here is the operator's: "delete all images" comes from the registry,
// and the core that renders it has never heard of a layer.
func manifest(publicAddress string) map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Container image registry, served on its own name on the instance's ports",
		"scopes":      []string{"registry:pull", "registry:push", "registry:delete"},

		// A registry client reads everything before the first slash as a hostname
		// and nothing after it as a path, so a registry cannot live under a prefix
		// on the main name. It asks for a name of its own instead, and the core
		// works out what that means here.
		"routing": map[string]any{
			"domains": []string{"registry.{host}"},
		},

		"settings": []map[string]any{
			{
				"key":   "image_name_template",
				"label": "Image name template",
				"type":  "string",
				// Stated here rather than enforced by the core: it is the registry's
				// rule, and the core has no business knowing what an image name is.
				"must_contain": []string{imageNameMandatory},
				"why_contains": "an image name that does not contain its project cannot be traced back to one, and two projects could then push to the same name.",
				// {{project}} is required: an image name that does not contain the
				// project cannot be traced back to one, and the whole permission
				// boundary rests on being able to say which project an image belongs
				// to. Without it, two projects could push to the same name.
				"default":     defaultImageName,
				"description": "How a project path becomes the image name. {{project}} is required; {{group}} and {{branch}} are optional.",
			},
			{
				"key":         "public_address",
				"label":       "Public address",
				"type":        "url",
				"description": "Where clients actually reach this registry, when it is not derived from the instance's name. Set it when the proxy serves the module somewhere else — a published port during development, for instance.",
				"default":     publicAddress,
			},
			{
				"key":         "read_timeout",
				"label":       "Request timeout",
				"type":        "int",
				"default":     300,
				"description": "Seconds a single registry request may take. A large layer push may need more.",
			},
			{
				"key":         "storage_backend",
				"label":       "Storage",
				"type":        "enum",
				"options":     []string{"filesystem", "s3"},
				"default":     "filesystem",
				"description": "Where images are kept. Changing this rewrites the registry configuration and restarts the registry container, which is not something a running push survives.",
			},
			{
				"key":         "s3_endpoint",
				"label":       "S3 endpoint",
				"type":        "url",
				"description": "Only for the s3 storage backend.",
			},
			{
				"key":         "s3_bucket",
				"label":       "S3 bucket",
				"type":        "string",
				"description": "Only for the s3 storage backend.",
			},
			{
				"key":         "s3_region",
				"label":       "S3 region",
				"type":        "string",
				"default":     "us-east-1",
				"description": "Only for the s3 storage backend.",
			},
			{
				"key":         "s3_access_key",
				"label":       "S3 access key",
				"type":        "string",
				"description": "Only for the s3 storage backend.",
			},
			{
				"key":         "s3_secret_key",
				"label":       "S3 secret key",
				"type":        "string",
				"secret":      true,
				"description": "Never returned by the core once written.",
			},
		},

		"uninstall": map[string]any{
			"options": []map[string]any{
				{
					"key":         "purge_images",
					"label":       "Delete every image",
					"description": "All repositories and tags created through dogit. Without this the registry is left holding what it had.",
					"default":     true,
					"dangerous":   true,
				},
				{
					"key":         "drop_database",
					"label":       "Delete the module database",
					"description": "The core drops it; the images are not in there.",
					"default":     false,
					"dangerous":   true,
				},
			},
		},
	}
}

// coreClient talks to the dogit core.
type coreClient struct {
	baseURL string
	token   string
}

type registrationResponse struct {
	Token string `json:"token"`
}

// report tells the core that something this module holds has changed.
//
// The core cannot watch for it. A browser deletes an image by asking this module
// directly, with a credential the core minted and cannot present, so the core never
// sees the request. Without this, an administrator's page would keep showing an
// image that is already gone until somebody pressed something.
//
// It is said as this module's own account of what it did. The core records that and
// nothing else: it does not learn what an image is, only that something changed.
func (c *coreClient) report(ctx context.Context, change, subject string, detail any) error {
	return c.post(ctx, "/api/v1/module/report", map[string]any{
		"change": change, "subject": subject, "detail": detail,
	}, c.token, nil)
}

func (c *coreClient) post(ctx context.Context, path string, body any, token string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path,
		strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusOK, http.StatusCreated, http.StatusAccepted, http.StatusNoContent:
	default:
		return coreerr.Refusal(response)
	}

	if out == nil {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *coreClient) get(ctx context.Context, path, token string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return coreerr.Refusal(response)
	}
	return json.NewDecoder(response.Body).Decode(out)
}

func (c *coreClient) register(ctx context.Context, registrationToken, name, endpoint string, man map[string]any) (string, error) {
	var answer registrationResponse
	if err := c.post(ctx, "/api/v1/modules/register", map[string]any{
		"kind":     "registry:docker",
		"name":     name,
		"endpoint": endpoint,
		"manifest": man,
	}, registrationToken, &answer); err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return answer.Token, nil
}

// registryAccess is the core's answer about a caller.
type registryAccess struct {
	Allowed  bool   `json:"allowed"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
	Level    int    `json:"level"`
	Minimum  int    `json:"minimum"`
	Username string `json:"username"`
}

// ask is how the module asks who is pushing and whether they may.
//
// The core answers from its own user and permission tables, so this module has
// nothing to keep in step with: a member removed there fails here on the next
// request, with no revocation step and no copy to clean up.
func (c *coreClient) ask(ctx context.Context, token, project, action string) (registryAccess, error) {
	var answer registryAccess
	err := c.post(ctx, "/api/v1/module/registry/access", map[string]string{
		"token": token, "project": project, "action": action,
	}, c.token, &answer)
	return answer, err
}

// resolveImage asks which project an image name belongs to.
//
// The core answers from its own projects and this module's naming rule. A registry
// can hold images pushed by something else entirely, and those must not be
// attributed to whichever project happens to produce a similar name.
func (c *coreClient) resolveImage(ctx context.Context, name string) (string, error) {
	var answer struct {
		Project string `json:"project"`
	}
	body, err := json.Marshal(map[string]string{"image": name})
	if err != nil {
		return "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/module/registry/resolve", strings.NewReader(string(body)))
	if err != nil {
		return "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", coreerr.Refusal(response)
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", err
	}
	return answer.Project, nil
}

// signIn exchanges an account's credentials for a token scoped to one repository.
//
// The password is checked by the core and kept nowhere: this module has no user
// store, no hash to compare against and nothing to forget to purge.
func (c *coreClient) signIn(ctx context.Context, login, password, project string, scopes []string) (string, string, error) {
	body, err := json.Marshal(map[string]any{
		// Sent as a credential, not as a password: what a registry client types here
		// is sometimes an account's password and sometimes a token the core issued
		// for this project. The core decides which it is.
		"login": login, "credential": password, "project": project, "scopes": scopes,
	})
	if err != nil {
		return "", "", err
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/api/v1/module/registry/authenticate", strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+c.token)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("core refused the login with %d", response.StatusCode)
	}

	var answer struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return "", "", err
	}
	return answer.Token, answer.Username, nil
}

// effectiveSettings asks what this module is configured to do for a project.
//
// The core decides the layering — instance defaults, group overrides, project
// overrides — so the module has one place to read rather than three.
func (c *coreClient) effectiveSettings(ctx context.Context) (map[string]any, error) {
	var answer struct {
		Effective map[string]any `json:"effective"`
	}
	if err := c.get(ctx, "/api/v1/module/settings", c.token, &answer); err != nil {
		return nil, err
	}
	return answer.Effective, nil
}

// beat keeps the core informed the module is alive, and reports what it can see.
func (c *coreClient) beat(ctx context.Context, stats map[string]any) error {
	return c.post(ctx, "/api/v1/module/heartbeat", map[string]any{"stats": stats}, c.token, nil)
}

func (c *coreClient) introspect(ctx context.Context, userToken string) (map[string]any, error) {
	var result map[string]any
	if err := c.post(ctx, "/api/v1/auth/introspect", map[string]any{}, userToken, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
