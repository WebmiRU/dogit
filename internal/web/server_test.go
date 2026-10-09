package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/logger"
)

// recordingAPI captures the routes the web layer registers, so the mount point can
// be asserted without a database.
type recordingAPI struct {
	routes []string
}

func (a *recordingAPI) Register(r chi.Router) {
	a.routes = append(a.routes,
		"GET /projects",
		"GET /projects/{projectID}",
		"GET /projects/{projectID}/repository/tree",
	)
	r.Get("/projects", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Route("/projects/{projectID}", func(p chi.Router) {
		p.Get("/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		p.Get("/repository/tree", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})
}

func TestAPIRoutesAreReachableUnderTheVersionPrefix(t *testing.T) {
	api := &recordingAPI{}
	srv := NewServer(&config.Config{}, logger.Discard(), nil, api)
	router := srv.Routes()

	paths := []string{
		"/api/v1/projects",
		"/api/v1/projects/hello",
		"/api/v1/projects/hello/repository/tree",
		"/api/v1/projects/ba5e9dc6-56a6-4c10-9826-91dcc2eec947",
	}

	for _, path := range paths {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (routes registered: %v)", path, recorder.Code, api.routes)
		}
	}
}

func TestHealthProbesAreAlwaysMounted(t *testing.T) {
	srv := NewServer(&config.Config{}, logger.Discard(), nil, nil)
	router := srv.Routes()

	// The probes must answer without touching the database, so a nil store is
	// fine here.
	for _, path := range []string{"/-/health", "/api/v1/version"} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, recorder.Code)
		}
	}
}
