// Package api implements the REST API consumed by the frontend.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/events"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	cfg    *config.Config
	log    *slog.Logger
	store  *store.Store
	git    *gitx.Git
	repos  *repos.Service
	events *events.Bus
}

// New creates the API server.
func New(
	cfg *config.Config, log *slog.Logger, st *store.Store,
	git *gitx.Git, repoSvc *repos.Service, bus *events.Bus,
) *Server {
	return &Server{cfg: cfg, log: log, store: st, git: git, repos: repoSvc, events: bus}
}

// contextUserKey and contextIntegrationKey hold the authenticated caller on the
// request context: a user for browser and API requests, a module for requests a
// module makes about itself.
type (
	contextUserKey        struct{}
	contextIntegrationKey struct{}
)

// Routes registers every API route on a new router. It is mounted at /api/v1 by
// the web server.
func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()
	s.Register(r)
	return r
}

// Register adds the API routes to an existing router.
//
// Nesting chi wildcard mounts is unreliable for routes that carry path
// parameters, so the web server hands its /api/v1 router in directly instead of
// mounting one router inside another.
func (s *Server) Register(r chi.Router) {
	// Authentication is open: a fresh instance has no users, so registration
	// must be possible before anyone can log in.
	r.Route("/auth", func(auth chi.Router) {
		auth.Get("/config", s.handleAuthConfig)
		auth.Post("/register", s.handleRegister)
		auth.Post("/login", s.handleLogin)
		auth.Post("/logout", s.handleAuthenticated(s.handleLogout))
		auth.Get("/me", s.handleAuthenticated(s.handleCurrentUser))

		// Introspection is how a module asks who a caller is. It carries no
		// credentials of its own: the bearer token in the request is the user's
		// token, and the answer decides whether the request is allowed.
		auth.Post("/introspect", s.handleIntrospect)
	})

	// Registration is the way in, so it authenticates with an instance token
	// rather than a module token, and validates it inside the handler.
	r.Post("/modules/register", s.handleModuleRegister)

	// Everything else a module calls authenticates as a module, not as a user: it
	// sits outside the session-protected group on purpose, because no module has
	// a browser session and none of them may act as a user.
	//
	// The prefix is singular on purpose. "/modules/{id}" is an administrator's
	// resource, while "/module/..." is what the module itself calls, and mixing
	// the two in one namespace makes a literal path such as /modules/me
	// indistinguishable from a module id.
	moduleRoutes := chi.NewRouter()
	moduleRoutes.Use(s.authenticateModule)
	moduleRoutes.Post("/heartbeat", s.handleModuleHeartbeat)
	moduleRoutes.Get("/me", s.handleModuleSelf)
	moduleRoutes.Get("/settings", s.handleModuleSelfSettings)
	r.Route("/module", func(m chi.Router) {
		m.Mount("/", moduleRoutes)
	})

	// Personal access tokens let the CLI and other clients authenticate with
	// the same endpoints.
	r.Group(func(authenticated chi.Router) {
		authenticated.Use(s.authenticate)

		authenticated.Get("/user", s.handleCurrentUser)
		authenticated.Patch("/user", s.handleUpdateProfile)
		authenticated.Get("/user/keys", s.handleListSSHKeys)
		authenticated.Post("/user/keys", s.handleCreateSSHKey)
		authenticated.Delete("/user/keys/{keyID}", s.handleDeleteSSHKey)
		authenticated.Get("/user/tokens", s.handleListTokens)
		authenticated.Post("/user/tokens", s.handleCreateToken)
		authenticated.Delete("/user/tokens/{tokenID}", s.handleRevokeToken)

		authenticated.Get("/dashboard", s.handleDashboard)

		// The event feed the frontend follows. A WebSocket will replace the polling
		// without changing what a page has to do.
		authenticated.Get("/events", s.handleEventStream)

		// Module administration: an administrator sees and configures modules,
		// and a user mints the tokens they present to them.
		authenticated.Post("/modules/{kind}/token", s.handleMintModuleToken)
		authenticated.Get("/modules", s.handleListModules)
		authenticated.Get("/modules/routes", s.handleModuleRoutes)
		authenticated.Get("/modules/{integrationID}", s.handleGetModule)
		authenticated.Put("/modules/{integrationID}/state", s.handleSetModuleState)
		authenticated.Delete("/modules/{integrationID}", s.handleDeleteModule)
		authenticated.Post("/modules/{integrationID}/uninstall", s.handleStartModuleUninstall)
		authenticated.Get("/modules/{integrationID}/uninstall", s.handleGetModuleUninstall)
		authenticated.Get("/modules/{integrationID}/uninstall/log", s.handleModuleUninstallLog)
		authenticated.Get("/modules/{integrationID}/stats", s.handleGetModuleStats)
		authenticated.Get("/modules/{integrationID}/settings", s.handleGetModuleSettings)
		authenticated.Put("/modules/{integrationID}/settings", s.handleSetModuleSettings)

		authenticated.Get("/admin/overview", s.handleAdminOverview)

		authenticated.Get("/groups", s.handleListGroups)
		authenticated.Post("/groups", s.handleCreateGroup)
		authenticated.Get("/groups/{groupID}", s.handleGetGroup)
		authenticated.Get("/groups/{groupID}/projects", s.handleListGroupProjects)

		// Every merge request the caller may read, across projects.
		authenticated.Get("/merge_requests", s.handleListMergeRequests)

		s.mountProjectRoutes(authenticated)
	})
}

// mountProjectRoutes registers every route scoped to a single project.
//
// The routes live on a sub-router mounted at /projects, and a project inside a
// group — "group/project" — is picked up by that router's NotFound handler. Two
// other arrangements were tried and both break something invisible:
//
//   - A chi.Route block mounts at the pattern plus "/*", so it claims every path
//     underneath "/projects/{projectID}", including a grouped project's own
//     address. Grouped projects answered 404 while their id-addressed twins
//     worked.
//   - A "/projects/*" wildcard registered next to the parameter routes wins over
//     them, and then "/projects/{id}/compare" stops resolving — a route that has
//     existed all along stops being reachable.
//
// NotFound is the one place that is only consulted when nothing else matched, so
// the two can no longer compete.
func (s *Server) mountProjectRoutes(r chi.Router) {
	r.Mount("/projects", s.projectRoutes())
}

// projectRoutes builds the per-project router. It is built per call rather than
// cached because it holds nothing but closures over the server, and the cost is a
// few map insertions on a path that is walked once per request.
func (s *Server) projectRoutes() chi.Router {
	projects := chi.NewRouter()
	// Paths here are relative to the mount point.
	const base = "/{projectID}"

	// The collection sits next to the individual projects so that "/" and
	// "/{projectID}" are resolved by one router: registering the collection on the
	// outer router while mounting a sub-router on the same prefix let the mount
	// shadow it.
	projects.Get("/", s.handleListProjects)
	projects.Post("/", s.handleCreateProject)

	projects.Get(base, s.handleGetProject)
	projects.Patch(base, s.handleUpdateProject)
	projects.Delete(base, s.handleDeleteProject)
	projects.Get(base+"/members", s.handleListProjectMembers)

	projects.Get(base+"/repository/tree", s.handleTree)
	projects.Get(base+"/repository/file", s.handleFile)
	// Writing from the browser: a file becomes a blob, a tree, a commit and a
	// conditional ref update, without checking anything out.
	projects.Post(base+"/repository/files", s.handleCommitFile)
	projects.Get(base+"/repository/raw", s.handleRawFile)
	projects.Get(base+"/repository/blame", s.handleBlame)
	projects.Get(base+"/repository/refs", s.handleRefs)
	projects.Get(base+"/repository/branches", s.handleBranches)
	projects.Post(base+"/repository/branches", s.handleCreateBranch)
	projects.Delete(base+"/repository/branches/{name}", s.handleDeleteBranch)
	projects.Get(base+"/repository/tags", s.handleTags)
	projects.Post(base+"/repository/tags", s.handleCreateTag)
	projects.Delete(base+"/repository/tags/{name}", s.handleDeleteTag)
	projects.Get(base+"/repository/commits", s.handleCommits)
	projects.Get(base+"/repository/commits/{sha}", s.handleCommit)
	projects.Get(base+"/repository/commits/{sha}/diff", s.handleCommitDiff)
	projects.Get(base+"/repository/compare", s.handleCompare)
	projects.Get(base+"/repository/commits_feed", s.handleCommitFeed)

	// Merge requests. A global listing lives outside this router, because it spans
	// every project the caller may read.
	projects.Post(base+"/move", s.handleMoveProject)

	projects.Get(base+"/merge_requests", s.handleListProjectMergeRequests)
	projects.Post(base+"/merge_requests", s.handleCreateMergeRequest)
	projects.Get(base+"/merge_requests/{iid}", s.handleGetMergeRequest)
	projects.Put(base+"/merge_requests/{iid}", s.handleUpdateMergeRequest)
	projects.Put(base+"/merge_requests/{iid}/state", s.handleCloseMergeRequest)
	projects.Post(base+"/merge_requests/{iid}/merge", s.handleMergeMergeRequest)
	projects.Get(base+"/merge_requests/{iid}/conflicts", s.handleMergeRequestConflicts)
	projects.Post(base+"/merge_requests/{iid}/notes", s.handleAddMergeRequestNote)

	// Anything unmatched is a project whose path contains a slash.
	projects.NotFound(s.handleNestedProjectPath)

	return projects
}

// userFrom returns the authenticated user stored by the authenticate middleware.
func userFrom(ctx context.Context) *models.User {
	u, _ := ctx.Value(contextUserKey{}).(*models.User)
	return u
}

// sessionCookieName identifies the browser session.
const sessionCookieName = "dogit_session"

// authenticate resolves the caller from either a session cookie or a personal
// access token, and rejects anonymous access to everything that needs an
// identity.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.identify(r)
		if err != nil || user == nil {
			s.writeError(w, r, errUnauthorized("authentication is required"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextUserKey{}, user)))
	})
}

// authenticateModule authenticates a request coming from a module rather than a
// user: the module presents the token it received at registration.
func (s *Server) authenticateModule(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		integration, err := s.integrationFromRequest(r)
		if err != nil {
			s.writeError(w, r, errUnauthorized("module authentication is required"))
			return
		}
		if !integration.Enabled {
			s.writeError(w, r, errForbiddenf("module %q is disabled", integration.Kind))
			return
		}
		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), contextIntegrationKey{}, integration)))
	})
}

// integrationFrom returns the module that made the request.
func integrationFrom(ctx context.Context) *models.Integration {
	integration, _ := ctx.Value(contextIntegrationKey{}).(*models.Integration)
	return integration
}

// identify reads the credentials from a request. A cookie session is only
// honoured for a same-origin request: browsers attach cookies automatically, so
// accepting one on a cross-site request would let any page act as the signed-in
// user. API clients authenticate with a bearer token instead, which browsers
// never attach on their own.
func (s *Server) identify(r *http.Request) (*models.User, error) {
	if token := bearerToken(r); token != "" {
		return s.userFromToken(r, token)
	}

	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return nil, nil
	}
	if !s.cookieAllowedFor(r) {
		return nil, errCrossSiteRequest
	}
	return s.userFromSession(r, cookie.Value)
}

// errCrossSiteRequest is returned when a cookie arrives on a request another
// site could have caused.
var errCrossSiteRequest = errors.New("cookie session rejected for a cross-site request")

// safeMethods never change state, so they need no origin check.
func safeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return true
	}
	return false
}

// cookieAllowedFor decides whether a request carrying our session cookie may be
// trusted.
//
// Two signals are combined, because each alone is incomplete:
//
//   - Sec-Fetch-Site is set by every current browser and cannot be forged by a
//     page, but is absent from curl and older clients.
//   - Origin, or Referer when Origin is missing, covers those clients and must
//     match a configured origin exactly.
//
// When neither signal is present the request is treated as non-browser and
// refused. Trusting it silently would reintroduce CSRF for any client that
// strips headers.
func (s *Server) cookieAllowedFor(r *http.Request) bool {
	if safeMethod(r.Method) {
		return true
	}

	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "cross-site":
		return false
	default:
		// "same-site" is a different site on the same registrable domain, which a
		// sibling subdomain may control; only an exact Origin match may allow it.
		// The empty case falls through to the Origin check as well.
	}

	return s.originAllowed(r)
}

// originAllowed compares Origin, or Referer when Origin is absent, with the
// configured list. An empty list means no cross-origin browser access is
// accepted, which is the safe default for a self-hosted instance.
func (s *Server) originAllowed(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		return s.matchesOrigin(origin)
	}

	// Referer carries a full URL, so only its scheme and authority are
	// compared; the path identifies a page, not an origin.
	referer := r.Header.Get("Referer")
	if referer == "" {
		return false
	}
	parsed, err := url.Parse(referer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	return s.matchesOrigin(parsed.Scheme + "://" + parsed.Host)
}

// matchesOrigin compares an origin exactly. A prefix comparison would accept
// "https://dogit.example.evil.com" for an allowed "https://dogit.example".
func (s *Server) matchesOrigin(source string) bool {
	for _, allowed := range s.cfg.AllowedOrigins {
		if strings.EqualFold(source, allowed) {
			return true
		}
	}
	return false
}

func (s *Server) userFromSession(r *http.Request, sessionID string) (*models.User, error) {
	id, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, errors.New("malformed session cookie")
	}

	sess, err := s.store.Sessions().ByID(r.Context(), id)
	if err != nil {
		return nil, err
	}
	user, err := s.store.Users().ByID(r.Context(), sess.UserID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *Server) userFromToken(r *http.Request, token string) (*models.User, error) {
	hash := auth.HashToken(token)
	tok, err := s.store.Tokens().ByHash(r.Context(), hash)
	if err != nil {
		return nil, errors.New("invalid or revoked token")
	}
	if err := s.store.Tokens().TouchUsed(r.Context(), tok.ID); err != nil {
		// A failed bookkeeping write must not block the request.
		s.log.Warn("record token usage", "token_id", tok.ID, "error", err)
	}
	return s.store.Users().ByID(r.Context(), tok.UserID)
}

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) > len(prefix) && header[:len(prefix)] == prefix {
		return header[len(prefix):]
	}
	// Git over HTTP sends the token in this header; harmless to accept here
	// because password authentication is not offered.
	return r.Header.Get("PRIVATE-TOKEN")
}

// expiryFrom builds a cookie lifetime from the configured token TTL.
func (s *Server) sessionTTL() time.Duration { return s.cfg.AuthTokenTTL }
