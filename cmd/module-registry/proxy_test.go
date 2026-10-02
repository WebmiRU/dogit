package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The registry API puts a repository in the path and everything else in the
// query, so the repository is the first two segments after /v2. Getting this
// wrong means asking the core about the wrong project, which means letting the
// wrong project's images be pushed.
func TestRegistryProjectFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/v2/grp1/prj1/tags/list", "grp1/prj1"},
		{"/v2/grp1/prj1/manifests/latest", "grp1/prj1"},
		{"/v2/prj1/blobs/uploads/", "prj1"},
		{"/v2/grp1/prj1", "grp1/prj1"},
		{"/v2/grp1/prj1/", "grp1/prj1"},
		// A three-segment repository name is not something the template can produce,
		// but the split must not quietly mangle it into a different project either.
		// With no operation to end it, the name is however long it is. The rule
		// that turns it into a project belongs to the core, not here.
		{"/v2/a/b/c/d", "a/b/c/d"},
		// A name that ends in a command's letters is still a name.
		{"/v2/grp1/manifests-club/tags/list", "grp1/manifests-club"},
		// With no operation after it, the whole remainder is the name.
		{"/v2/grp1/prj1", "grp1/prj1"},
		{"/v2/prj1", "prj1"},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			got, err := registryProject(tc.path)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("project = %q, want %q", got, tc.want)
			}
		})
	}

	for _, path := range []string{"/v2/", "/v2"} {
		if _, err := registryProject(path); err == nil {
			t.Errorf("%q was read as a project", path)
		}
	}
}

// Reading a manifest is reading. A client checks a manifest before it pulls a
// layer, and treating that as a write would break ordinary pulls for anybody who
// is not a developer.
func TestActionFollowsTheMethod(t *testing.T) {
	cases := []struct {
		method string
		want   string
	}{
		{http.MethodGet, "pull"},
		{http.MethodHead, "pull"},
		{http.MethodPost, "push"},
		{http.MethodPut, "push"},
		{http.MethodPatch, "push"},
		{http.MethodDelete, "delete"},
	}

	for _, tc := range cases {
		request := httptest.NewRequest(tc.method, "/v2/grp1/prj1/manifests/latest", nil)
		if got := actionOf(request); got != tc.want {
			t.Errorf("%s is %q, want %q", tc.method, got, tc.want)
		}
	}
}

// Without a credential nothing is decided by the module; it asks the client for
// one, the way the registry specification requires, or a docker client will not
// know to log in.
func TestProxyRefusesWithoutCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an unauthenticated request reached the registry")
	}))
	defer upstream.Close()

	core := &coreClient{baseURL: "http://core.invalid"}
	handler := newHandler(core, upstream.URL, "http://registry.test")

	request := httptest.NewRequest(http.MethodGet, "/v2/grp1/prj1/tags/list", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", recorder.Code)
	}
	// The realm has to be an address the client can fetch from. A bare name reads
	// as a relative path, and every docker client answers that with "unsupported
	// protocol scheme" — a failure that looks like a network problem and is not.
	if challenge := recorder.Header().Get("WWW-Authenticate"); !strings.Contains(challenge, `realm="http://`) {
		t.Errorf("no challenge a client can act on: %q", challenge)
	}
}

// The version check happens before a client has said anything about a repository,
// and before it has credentials. It is answered here for that reason.
func TestVersionCheckIsAnsweredWithoutCredentials(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("the version check reached the registry, which is not wrong but is not needed")
	}))
	defer upstream.Close()

	handler := newHandler(&coreClient{baseURL: "http://core.invalid"}, upstream.URL, "http://registry.test")

	request := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", recorder.Code)
	}
	if version := recorder.Header().Get("Docker-Distribution-API-Version"); version != "registry/2.0" {
		t.Errorf("API version = %q", version)
	}
}

// A refusal has to be readable by a client that only looks at the status, and
// has to carry the challenge or docker will not ask for credentials again.
func TestRefusalsUseRegistryErrorShape(t *testing.T) {
	recorder := httptest.NewRecorder()
	registryError(recorder, http.StatusForbidden, "not_permitted", "no rights here")

	var body struct {
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Errors) != 1 || body.Errors[0].Code != "DENIED" {
		t.Errorf("errors = %+v, want one DENIED", body.Errors)
	}
	if body.Errors[0].Message == "" {
		t.Error("the refusal carries no wording")
	}

	recorder = httptest.NewRecorder()
	registryError(recorder, http.StatusUnauthorized, "authentication_required", "sign in")
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Errors[0].Code != "UNAUTHORIZED" {
		t.Errorf("code = %q, want UNAUTHORIZED", body.Errors[0].Code)
	}
}

// The module's own credential goes upstream; a user's token must not reach the
// registry, which could otherwise be asked to make decisions of its own.
func TestUserTokenIsStrippedBeforeTheRegistry(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	// The core answers two questions: whose image is this, and may they push it.
	core := &coreClient{baseURL: coreServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			_, _ = w.Write([]byte(`{"image":"grp1/prj1","project":"grp1/prj1"}`))
		default:
			_, _ = w.Write([]byte(`{"allowed":true,"level":30}`))
		}
	})}
	core.token = "module-token"

	handler := newHandler(core, upstream.URL, "http://registry.test")

	request := httptest.NewRequest(http.MethodGet, "/v2/grp1/prj1/tags/list", nil)
	request.Header.Set("Authorization", "Bearer user's-secret-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if seen != "" {
		t.Errorf("the registry was given %q", seen)
	}
}

// A name this module's rule cannot produce belongs to nobody here, and must not be
// attributed to whichever project fits.
func TestAnUnknownImageNameBelongsToNobody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("an image belonging to nobody reached the registry")
	}))
	defer upstream.Close()

	core := &coreClient{baseURL: coreServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"image":"someone/elses-image","project":null}`))
	})}
	core.token = "module-token"

	handler := newHandler(core, upstream.URL, "http://registry.test")

	request := httptest.NewRequest(http.MethodGet, "/v2/someone/elses-image/tags/list", nil)
	request.Header.Set("Authorization", "Bearer a-token")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404: an image of nobody's is not this registry's to serve", recorder.Code)
	}
}

// The module asks the core to resolve the name rather than working it out itself:
// the project list is an administrator's view, not something a module may page
// through.
func TestTheCoreIsAskedWhichProjectAnImageBelongsTo(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	asked := ""
	core := &coreClient{baseURL: coreServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			var body struct {
				Image string `json:"image"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			asked = body.Image
			_, _ = w.Write([]byte(`{"project":"grp1/prj1"}`))
		default:
			_, _ = w.Write([]byte(`{"allowed":true}`))
		}
	})}
	core.token = "module-token"

	handler := newHandler(core, upstream.URL, "http://registry.test")

	request := httptest.NewRequest(http.MethodGet, "/v2/grp1/prj1/tags/list", nil)
	request.Header.Set("Authorization", "Bearer a-token")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if asked != "grp1/prj1" {
		t.Errorf("the core was asked about %q, want grp1/prj1", asked)
	}
}

// coreServer is a stand-in for the core that answers however a test says.
func coreServer(t *testing.T, answer http.HandlerFunc) string {
	t.Helper()

	server := httptest.NewServer(answer)
	t.Cleanup(server.Close)
	return server.URL
}
