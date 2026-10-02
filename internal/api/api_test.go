package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/repos"
)

// The project lookup must accept a path as well as a UUID, because the
// frontend navigates by path while internal links use identifiers.
func TestProjectPathValidation(t *testing.T) {
	valid := []string{
		"hello",
		"group/project",
		"my-project",
		"team.sub/project_1",
	}
	for _, path := range valid {
		if !repos.ValidPath(path) {
			t.Errorf("expected %q to be a valid project path", path)
		}
	}

	invalid := []string{
		"",
		"Hello",                          // uppercase
		"group//project",                 // empty component
		"../escape",                      // traversal
		"group/../x",                     // traversal
		".hidden/x",                      // dotfile
		"project.git",                    // repository suffix
		"with space",                     // spaces
		strings.Repeat("directory/", 40), // over the total length limit
	}
	for _, path := range invalid {
		if repos.ValidPath(path) {
			t.Errorf("expected %q to be rejected", path)
		}
	}
}

// chi routes are configured without a database in these tests; this checks that
// the route table the frontend depends on is wired up.
func TestRouteTableContainsFrontendEndpoints(t *testing.T) {
	cfg := &config.Config{}
	srv := &Server{cfg: cfg, log: slog.Default()}

	router := chi.NewRouter()
	srv.Register(router)

	wantRoutes := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/auth/login"},
		{http.MethodPost, "/auth/register"},
		{http.MethodGet, "/auth/me"},
		{http.MethodGet, "/user"},
		{http.MethodPatch, "/user"},
		{http.MethodGet, "/user/keys"},
		{http.MethodPost, "/user/keys"},
		{http.MethodGet, "/dashboard"},
		{http.MethodGet, "/events"},
		{http.MethodGet, "/admin/overview"},
		{http.MethodGet, "/groups"},
		{http.MethodGet, "/projects"},
		{http.MethodPost, "/projects"},
		{http.MethodGet, "/projects/{projectID}"},
		{http.MethodPatch, "/projects/{projectID}"},
		{http.MethodGet, "/projects/{projectID}/members"},
		{http.MethodGet, "/projects/{projectID}/repository/tree"},
		{http.MethodGet, "/projects/{projectID}/repository/file"},
		{http.MethodPost, "/projects/{projectID}/repository/files"},
		{http.MethodGet, "/projects/{projectID}/repository/raw"},
		{http.MethodGet, "/projects/{projectID}/repository/blame"},
		{http.MethodGet, "/projects/{projectID}/repository/refs"},
		{http.MethodGet, "/projects/{projectID}/repository/branches"},
		{http.MethodPost, "/projects/{projectID}/repository/branches"},
		{http.MethodDelete, "/projects/{projectID}/repository/branches/{name}"},
		{http.MethodGet, "/projects/{projectID}/repository/tags"},
		{http.MethodPost, "/projects/{projectID}/repository/tags"},
		{http.MethodDelete, "/projects/{projectID}/repository/tags/{name}"},
		{http.MethodGet, "/projects/{projectID}/repository/commits"},
		{http.MethodGet, "/projects/{projectID}/repository/commits/{sha}"},
		{http.MethodGet, "/projects/{projectID}/repository/commits/{sha}/diff"},
		{http.MethodGet, "/projects/{projectID}/repository/compare"},
	}

	// Matching against a concrete path is what actually matters: it verifies both
	// that the route exists and that it resolves, which walking the trie does not.
	for _, want := range wantRoutes {
		concrete := strings.NewReplacer(
			"{projectID}", "11111111-1111-1111-1111-111111111111",
			"{sha}", "1ad5c24dab0a0a287bfb78d577e99141bc910825",
			"{name}", "main",
		).Replace(want.path)

		routeCtx := chi.NewRouteContext()
		if !router.Match(routeCtx, want.method, concrete) {
			t.Errorf("route %s %s is not registered", want.method, want.path)
			continue
		}
		if matched := routeCtx.RoutePattern(); matched != want.path {
			t.Errorf("route %s %s matched pattern %q", want.method, concrete, matched)
		}
	}
}

// Unauthenticated requests must be rejected before any handler runs, and must do
// so without touching the database.
func TestProtectedRoutesRejectAnonymousRequests(t *testing.T) {
	srv := &Server{cfg: &config.Config{}, log: slog.Default()}
	router := srv.Routes()

	request := httptest.NewRequest(http.MethodGet, "/projects", nil)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"code":"unauthorized"`) {
		t.Errorf("unexpected error body: %s", body)
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	type payload struct {
		Name string `json:"name"`
	}

	// Strict decoding turns a client typo into a clear error instead of a
	// silently ignored field.
	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x","nmae":"typo"}`))
	var target payload
	if err := decodeJSON(request, &target); err == nil {
		t.Error("expected unknown fields to be rejected")
	}

	request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"x"}`))
	if err := decodeJSON(request, &target); err != nil {
		t.Errorf("valid body rejected: %v", err)
	}
	if target.Name != "x" {
		t.Errorf("Name = %q, want x", target.Name)
	}
}
