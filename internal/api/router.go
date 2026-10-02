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
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// Server holds the dependencies shared by all handlers.
type Server struct {
	cfg   *config.Config
	log   *slog.Logger
	store *store.Store
	git   *gitx.Git
	repos *repos.Service
}

// New creates the API server.
func New(cfg *config.Config, log *slog.Logger, st *store.Store, git *gitx.Git, repoSvc *repos.Service) *Server {
	return &Server{cfg: cfg, log: log, store: st, git: git, repos: repoSvc}
}

// contextUserKey is where the authenticated user is stored on the request
// context.
type contextUserKey struct{}

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
		authenticated.Get("/admin/overview", s.handleAdminOverview)

		authenticated.Get("/groups", s.handleListGroups)
		authenticated.Post("/groups", s.handleCreateGroup)
		authenticated.Get("/groups/{groupID}", s.handleGetGroup)
		authenticated.Get("/groups/{groupID}/projects", s.handleListGroupProjects)

		authenticated.Get("/projects", s.handleListProjects)
		authenticated.Post("/projects", s.handleCreateProject)

		s.mountProjectRoutes(authenticated)
	})
}

// mountProjectRoutes registers every route scoped to a single project.
//
// The project handlers and the repository handlers share one Route block on
// purpose: registering chi.Route at a pattern replaces any handler already
// registered for that exact path, so a separate Get("/projects/{projectID}")
// would be silently dropped.
func (s *Server) mountProjectRoutes(r chi.Router) {
	r.Route("/projects/{projectID}", func(p chi.Router) {
		p.Get("/", s.handleGetProject)
		p.Patch("/", s.handleUpdateProject)
		p.Delete("/", s.handleDeleteProject)
		p.Get("/members", s.handleListProjectMembers)

		p.Get("/repository/tree", s.handleTree)
		p.Get("/repository/file", s.handleFile)
		p.Get("/repository/raw", s.handleRawFile)
		p.Get("/repository/blame", s.handleBlame)
		p.Get("/repository/refs", s.handleRefs)
		p.Get("/repository/branches", s.handleBranches)
		p.Post("/repository/branches", s.handleCreateBranch)
		p.Delete("/repository/branches/{name}", s.handleDeleteBranch)
		p.Get("/repository/tags", s.handleTags)
		p.Post("/repository/tags", s.handleCreateTag)
		p.Delete("/repository/tags/{name}", s.handleDeleteTag)
		p.Get("/repository/commits", s.handleCommits)
		p.Get("/repository/commits/{sha}", s.handleCommit)
		p.Get("/repository/commits/{sha}/diff", s.handleCommitDiff)
		p.Get("/repository/compare", s.handleCompare)
		p.Get("/repository/commits_feed", s.handleCommitFeed)
	})
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
