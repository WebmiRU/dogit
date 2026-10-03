package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The registry API's own paths. They are recognised rather than passed through so
// that the ones which are not about images can be handled without a project at
// all: the API version check answers "who are you" before a client says anything
// about a repository.
const (
	apiVersionPath = "/v2/"
	apiVersionRoot = "/v2"
	catalogPath    = "/v2/_catalog"
)

// newHandler builds everything this module serves: its own endpoints, and an
// authorisation decision in front of a registry that does the work.
//
// One port and one process, so an installation has a module to deploy rather than
// two, and so the proxy can never be run without the module's own bookkeeping.
func newHandler(core *coreClient, upstream, endpoint string) http.Handler {
	target, err := url.Parse(upstream)
	if err != nil {
		log.Fatalf("module-registry: the registry address %q is not a URL: %v", upstream, err)
	}

	registry := &registry{
		core:      core,
		endpoint:  endpoint,
		imageName: defaultImageName,
	}

	// The upstream is written as an address and never as an upstream block: an
	// upstream name is resolved once at start-up, and a recreated registry
	// container usually gets a different address, leaving the proxy talking to
	// something that is no longer there.
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		// A client that saw a 502 would blame the module. It is more honest — and
		// more useful to whoever is holding the login — to say the registry itself
		// is not answering.
		log.Printf("module-registry: upstream %s: %v", target, err)
		registryError(w, http.StatusBadGateway, "registry_unavailable", "the registry is not answering")
	}
	proxy.FlushInterval = -1 // stream responses as they arrive

	// The realm is where a client is told to get a token, and it has to be an
	// address it can fetch: a name that is not a URL makes every docker client
	// fail with "unsupported protocol scheme".
	registry.realm = strings.TrimRight(registry.endpoint, "/") + tokenPath

	// Where the registry itself lives, for the parts of the module that speak to it
	// directly: listing what it holds, describing a tag, removing one. The reverse
	// proxy below does not go through this, but everything else does.
	upstreamURL = upstream

	proxied := registry.logging(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == tokenPath:
			registry.serveToken(w, r)

		case r.URL.Path == apiVersionPath || r.URL.Path == apiVersionRoot:
			// The version check. A client does this first, before it has a repository
			// and sometimes before it has credentials, so it is answered here
			// rather than refused.
			w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "{}")

		case r.URL.Path == catalogPath:
			registry.serveCatalog(w, r, proxy)

		default:
			registry.guard(proxy).ServeHTTP(w, r)
		}
	}))

	// The module's own endpoints are registered first, so a path like /-/health can
	// never be read as an image name.
	mux := handler(core, registry)
	// A request for /v2 with no trailing slash is the client checking the API
	// version, and must reach the same place either way.
	mux.Handle("/v2", proxied)
	mux.Handle("/v2/", proxied)
	return registry.logging(mux)
}

// guard answers "may this caller do this to this project?" before the request
// reaches the registry.
//
// The registry behind the proxy has no idea who is calling — by the time a request
// arrives there, the credential is gone. Everything that decides anything is
// decided here.
func (registry *registry) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			// No credential at all. The standard answer, with the challenge a client
			// needs to go and get one.
			w.Header().Set("WWW-Authenticate", registry.challenge())
			registryError(w, http.StatusUnauthorized, "authentication_required",
				"this registry requires a dogit account")
			return
		}

		project, err := registryProject(r.URL.Path)
		if err != nil {
			registryError(w, http.StatusNotFound, "no_such_project", "unknown image name")
			return
		}

		// A name is either a project path or the image name a project's images are
		// stored under. The core is asked about the project that owns it.
		owner, err := registry.ownerOf(ctxOf(r), project)
		if err != nil {
			log.Printf("module-registry: cannot resolve %q: %v", project, err)
			registryError(w, http.StatusBadGateway, "core_unavailable",
				"dogit is not answering, so the registry cannot tell who you are")
			return
		}
		if owner == "" {
			registryError(w, http.StatusNotFound, "no_such_project", "unknown image name")
			return
		}

		answer, err := registry.core.ask(ctxOf(r), token, owner, actionOf(r))
		if err != nil {
			log.Printf("module-registry: asking about %s: %v", owner, err)
			registryError(w, http.StatusBadGateway, "core_unavailable",
				"dogit is not answering, so the registry cannot tell who you are")
			return
		}

		if !answer.Allowed {
			registry.deny(w, r, answer, owner)
			return
		}

		// The module's own credential goes upstream: the registry must not see a
		// user's token, and must not be able to do anything with it either.
		r.Header.Set("Authorization", "")
		next.ServeHTTP(w, r)
	})
}

// deny answers a refusal in the shape a client expects.
//
// The WWW-Authenticate header matters as much as the status: a docker client that
// gets a bare 401 without it will not ask for credentials, and the user will see
// a failure where there was only an answer to give.
func (registry *registry) deny(w http.ResponseWriter, r *http.Request, answer registryAccess, owner string) {
	log.Printf("module-registry: refused %s on %s (%s, level %d, needs %d)",
		r.Method, owner, answer.Reason, answer.Level, answer.Minimum)

	w.Header().Set("WWW-Authenticate", registry.challenge()+`,error="insufficient_scope"`)
	registryError(w, http.StatusForbidden, answer.Reason, describeRefusal(answer, owner))
}

func describeRefusal(answer registryAccess, owner string) string {
	switch answer.Reason {
	case "unknown_token", "no_token":
		return "the account this login belongs to no longer exists"
	case "not_permitted":
		return fmt.Sprintf("you do not have rights on %s", owner)
	case "no_such_project":
		// The project is not named. Saying "you may not" would confirm it is there,
		// and a registry is an unusually easy place to enumerate every project on
		// the instance by asking whether you may pull from each.
		return "no such repository"
	case "module_forbidden":
		return "this registry is not accepting requests"
	default:
		return fmt.Sprintf("not allowed to do that on %s", owner)
	}
}

// registryCommands are the operations the registry API defines, longest first.
//
// A repository name is any number of path segments — one for a project without a
// group, two for one with — so the only way to tell where the name stops is to
// find the operation that follows it. Counting segments would work right up to
// the first project outside a group, and then ask the core about the wrong
// project, which is how one project's images end up another project's to push.
var registryCommands = []string{
	"blobs/uploads",
	"blobs",
	"manifests",
	"referrers",
	"tags/list",
	"tags",
}

// registryProject is the image name a request is about.
func registryProject(requestPath string) (string, error) {
	trimmed := strings.Trim(strings.TrimPrefix(requestPath, apiVersionRoot), "/")
	if trimmed == "" {
		return "", fmt.Errorf("no repository in %q", requestPath)
	}

	for _, command := range registryCommands {
		marker := "/" + command
		index := strings.LastIndex(trimmed, marker)
		if index < 0 {
			continue
		}

		// Only a whole segment counts: "mymanifests" is part of a name, not the
		// manifests operation.
		if index+len(marker) < len(trimmed) && trimmed[index+len(marker)] != '/' {
			continue
		}

		name := strings.Trim(trimmed[:index], "/")
		if name == "" {
			return "", fmt.Errorf("no repository in %q", requestPath)
		}
		return name, nil
	}

	// /v2/<name> with no operation after it is how a client checks one particular
	// repository: the whole remainder is the name, however many segments it has.
	if trimmed != "" {
		return trimmed, nil
	}
	return "", fmt.Errorf("no repository in %q", requestPath)
}

// actionOf is what the request is asking to do, as far as permissions go.
//
// Read is read: listing tags, fetching a manifest and downloading a blob are all
// pulling, and treating any of them as a write would break a client that checks a
// manifest before it pulls a layer.
func actionOf(r *http.Request) string {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		return "pull"
	case http.MethodDelete:
		return "delete"
	default:
		// POST starts an upload and PUT finishes a manifest. Both write.
		return "push"
	}
}

// ownerOf works out which project an image name belongs to.
//
// The name is produced from a project path by a rule, so the reverse is a search,
// and the search happens in the core: it holds the projects, the rule is its
// setting, and a module has no business paging through the project list.
//
// A name this rule cannot produce belongs to something else entirely. The registry
// holds those too, and attributing one to whichever project happens to fit would
// hand somebody else's images to somebody else's members.
func (registry *registry) ownerOf(ctx context.Context, name string) (string, error) {
	return registry.core.resolveImage(ctx, name)
}

// serveCatalog lists every repository the registry holds.
//
// Only an administrator may see it. The registry behind the proxy would answer
// this without asking anybody, so the question is asked here first, for the
// instance as a whole rather than for any project.
func (registry *registry) serveCatalog(w http.ResponseWriter, r *http.Request, upstream http.Handler) {
	token := bearerToken(r)
	if token == "" {
		w.Header().Set("WWW-Authenticate", registry.challenge())
		registryError(w, http.StatusUnauthorized, "authentication_required",
			"this registry requires a dogit account")
		return
	}

	// A catalog spans every project, so it is settled per project rather than for
	// one: the first project the caller cannot read ends the listing.
	answer, err := registry.core.ask(ctxOf(r), token, "", "pull")
	if err != nil || !answer.Allowed {
		registryError(w, http.StatusForbidden, "not_permitted",
			"listing every repository needs rights on every project")
		return
	}

	// The module's own credential goes upstream here too: the registry must never
	// see a user's token.
	r.Header.Set("Authorization", "")
	upstream.ServeHTTP(w, r)
}

// tokenPath is where a client fetches a credential from, as the challenge tells
// it to.
const tokenPath = "/v2/token"

// challenge is the header that starts a login.
//
// It names the realm as an address rather than as a name: a client that cannot
// parse it will not retry with credentials, and the user is left with a failure
// where there was only an answer to give.
func (registry *registry) challenge() string {
	return fmt.Sprintf(`Bearer realm=%q,service="dogit-registry"`, registry.realm)
}

// serveToken signs a registry client in.
//
// The client sends the account's own credentials here, and they go straight to
// the core: the core is where passwords are checked, and a module that checked
// them would have to keep a copy of every hash to do it. What comes back is a
// short-lived token scoped to the one repository the client asked about.
func (registry *registry) serveToken(w http.ResponseWriter, r *http.Request) {
	login, password, ok := r.BasicAuth()
	if !ok {
		registryError(w, http.StatusUnauthorized, "authentication_required", "an account is required")
		return
	}

	project, scopes := parseScope(r.URL.Query().Get("scope"))
	if project == "" {
		registryError(w, http.StatusBadRequest, "no_such_project", "no repository was asked for")
		return
	}

	token, username, err := registry.core.signIn(r.Context(), login, password, project, scopes)
	if err != nil {
		registryError(w, http.StatusUnauthorized, "authentication_required", "invalid login or password")
		return
	}

	writeJSON(w, map[string]any{
		"token":        token,
		"access_token": token,
		"expires_in":   900,
		"username":     username,
	})
}

// parseScope reads what a client asked for.
//
// A client asks in the form "repository:<name>:pull,push", once per repository.
// Several are allowed and all are honoured: a login that pulls from one project
// and pushes to another should not need a second round trip.
func parseScope(raw string) (project string, scopes []string) {
	seen := map[string]bool{}

	for _, entry := range strings.Fields(raw) {
		parts := strings.SplitN(entry, ":", 3)
		if len(parts) != 3 || parts[0] != "repository" {
			continue
		}
		if project == "" {
			project = parts[1]
		}
		for _, action := range strings.Split(parts[2], ",") {
			seen[action] = true
		}
	}

	for _, action := range []string{"pull", "push", "delete"} {
		if seen[action] {
			scopes = append(scopes, action)
		}
	}
	if len(scopes) == 0 {
		scopes = []string{"pull"}
	}
	return project, scopes
}

// registry keeps what one instance of this module knows.
type registry struct {
	core *coreClient

	// realm is where a client is sent to get a credential.
	realm string

	// endpoint is the address this module advertised to the core, which is also the
	// one a client can reach: it is what a client is told to fetch tokens from.
	endpoint string

	mu        sync.RWMutex
	imageName string
}

func (registry *registry) setImageName(template string) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.imageName = template
}

func (registry *registry) currentImageName() string {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	return registry.imageName
}

// logging records what the module did, without recording credentials.
func (registry *registry) logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		next.ServeHTTP(recorder, r)

		// The path is the useful part; the credential never appears.
		log.Printf("module-registry: %s %s -> %d in %s", r.Method, r.URL.Path,
			recorder.status, time.Since(start).Round(time.Millisecond))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if flusher, ok := s.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// registryError answers in the shape the registry specification defines, because
// a client that cannot read the body will simply retry or give up.
//
// Errors carry the code the specification uses where one exists, so a docker
// client recognises "unauthorized" and "denied" without reading our wording.
func registryError(w http.ResponseWriter, status int, code, message string) {
	// The specification names these two; the rest are ours and read as errors.
	switch code {
	case "authentication_required":
		code = "UNAUTHORIZED"
	case "not_permitted", "no_such_project":
		code = "DENIED"
	default:
		code = strings.ToUpper(code)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]any{{"code": code, "message": message}},
	})
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// ctxOf is the context this module's own calls are made under.
//
// The client's request context would be the obvious choice and is wrong here: a
// docker client opens many requests at once and cancels the ones it does not need,
// and a permission check that dies with them fails the push for reasons that have
// nothing to do with permission. The question "may this caller push?" is asked on
// this module's own time, and bounded, rather than on the client's.
func ctxOf(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}
