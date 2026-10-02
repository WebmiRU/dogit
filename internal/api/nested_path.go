package api

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
)

// handleNestedProjectPath serves a project addressed by a path that contains a
// slash, such as "platform/api", by rewriting the request onto the canonical
// /projects/{id}/... routes.
//
// The project is resolved here but not checked: permission lives in the handlers,
// which already know how to answer "you may not see this" correctly. Resolving
// without asking only decides which handler answers.
func (s *Server) handleNestedProjectPath(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(chi.URLParam(r, "*"), "/")
	if rest == "" {
		s.writeError(w, r, errNotFound("the requested resource does not exist"))
		return
	}

	// The project reference is whatever precedes the repository section, so
	// "platform/api/repository/file" splits into the project and the sub-path.
	reference, tail := rest, ""
	if index := strings.Index(rest, "/repository/"); index >= 0 {
		reference, tail = rest[:index], rest[index+1:]
	}

	project, err := s.resolveProjectRef(r.Context(), reference)
	if err != nil {
		s.writeError(w, r, errNotFound("the requested resource does not exist"))
		return
	}

	rewritten, err := url.Parse("/projects/" + project.String())
	if err != nil {
		s.writeError(w, r, errNotFound("the requested resource does not exist"))
		return
	}
	if tail != "" {
		rewritten.Path += "/" + tail
	}
	// The query carries the file path, the ref and the page number; it has to
	// survive the rewrite untouched.
	rewritten.RawQuery = r.URL.RawQuery

	replayed := r.Clone(context.WithValue(r.Context(), chi.RouteCtxKey, routeContextFor(rewritten.Path)))
	replayed.URL = rewritten
	replayed.RequestURI = rewritten.RequestURI()

	s.projectRoutes().ServeHTTP(w, replayed)
}

// routeContextFor returns a routing context for the rewritten path.
//
// A fresh one is required rather than the request's own: that context already
// carries the path chi matched on the way in, and chi reuses it in preference to
// the URL, so the replay would be routed by the original wildcard and find
// nothing.
func routeContextFor(path string) *chi.Context {
	routeContext := chi.NewRouteContext()
	routeContext.RoutePath = path
	return routeContext
}

// projectRoutes returns the router holding every per-project route.
//
// It holds exactly the same table that is mounted for direct id requests, so the
// two cannot drift apart. Building it per call costs a few map insertions, which
// is not worth caching for a path that is taken once per page load.
func (s *Server) projectRoutes() chi.Router {
	r := chi.NewRouter()
	s.mountProjectRoutes(r)
	return r
}
