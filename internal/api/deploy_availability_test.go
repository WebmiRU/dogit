package api

// What the core does with a question about whether an image is still in a registry: what it
// asks, what it sends with the question, and what it does with an answer it did not get.
//
// The module is a real HTTP server in these tests rather than a stub, because the thing that
// can be wrong here is what crosses the wire: the credential resolved for the place, the
// image rewritten to the address a pull would use, and the order of the answers matched back
// to the rows they belong to.

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// An image is asked about at the place's registry, by the path a pull would use and by the
// digest it is wanted at — and the credential that place pulls with goes with the question,
// because a registry that will not serve an image without a login will not say whether it
// has one either.
func TestTheQuestionCarriesThePlacesRegistryAndItsCredential(t *testing.T) {
	f := newAvailabilityFixture(t)
	harbor := f.unique("harbor.example.com")
	if err := f.store.DockerRegistries().Create(t.Context(),
		&store.DockerRegistry{URL: harbor, Login: "robot$ci", Password: "s3cret",
			InsecureTLS: true}); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	f.setPlace(t, map[string]string{"registry": harbor})

	code, body := f.ask(t, map[string]any{
		"cluster": "local-k3s",
		"images":  []string{harbor + "/test/versions@sha256:aaa"},
	})
	if code != http.StatusOK {
		t.Fatalf("asking: %d %s", code, body)
	}

	var seen struct {
		Registry struct {
			Address     string `json:"address"`
			Username    string `json:"username"`
			Token       string `json:"token"`
			InsecureTLS bool   `json:"insecure_tls"`
		} `json:"registry"`
		Images []struct {
			Path      string `json:"path"`
			Reference string `json:"reference"`
		} `json:"images"`
	}
	if err := json.Unmarshal(f.module.last(t), &seen); err != nil {
		t.Fatalf("read what the module was asked: %v", err)
	}
	if seen.Registry.Address != harbor || seen.Registry.Username != "robot$ci" ||
		seen.Registry.Token != "s3cret" || !seen.Registry.InsecureTLS {
		t.Errorf("the question carried %+v, want the registry's own credential", seen.Registry)
	}
	if len(seen.Images) != 1 || seen.Images[0].Path != "test/versions" ||
		seen.Images[0].Reference != "sha256:aaa" {
		t.Errorf("the question was about %+v, want the repository path and the digest",
			seen.Images)
	}
}

// The image in the catalogue is the one that was pushed, and it may name an address this
// place no longer pulls from. Asking about the name it was pushed under would report as
// missing an image that is there under the name a pull uses — so the question is put to the
// place's registry about the same path and digest.
func TestTheImageIsAskedForAtThePlacesRegistryNotWhereItWasPushed(t *testing.T) {
	f := newAvailabilityFixture(t)
	harbor := f.unique("harbor.example.com")
	if err := f.store.DockerRegistries().Create(t.Context(),
		&store.DockerRegistry{URL: harbor}); err != nil {
		t.Fatalf("write the registry down: %v", err)
	}
	f.setPlace(t, map[string]string{"registry": harbor})

	if _, body := f.ask(t, map[string]any{
		"cluster": "local-k3s",
		"images":  []string{"192.168.1.103:8091/team/app@sha256:aaa"},
	}); body == "" {
		t.Fatal("no answer at all")
	}

	var seen struct {
		Registry struct {
			Address string `json:"address"`
		} `json:"registry"`
		Images []struct {
			Path      string `json:"path"`
			Reference string `json:"reference"`
		} `json:"images"`
	}
	if err := json.Unmarshal(f.module.last(t), &seen); err != nil {
		t.Fatalf("read what the module was asked: %v", err)
	}
	if seen.Registry.Address != harbor {
		t.Errorf("the question went to %q, want the place's registry %q", seen.Registry.Address, harbor)
	}
	if seen.Images[0].Path != "team/app" || seen.Images[0].Reference != "sha256:aaa" {
		t.Errorf("the question was about %+v, want team/app at the same digest", seen.Images[0])
	}
}

// A place that has named no registry cannot be asked either, and it is refused in the same
// words a deployment is refused in — the same thing is wrong, and one sentence for it beats
// a page of images every one of them saying "not known".
func TestAPlaceWithNoRegistryCannotBeAskedAbout(t *testing.T) {
	f := newAvailabilityFixture(t)
	f.setPlace(t, map[string]string{"registry": ""})

	code, body := f.ask(t, map[string]any{
		"cluster": "local-k3s", "images": []string{"192.168.1.103:8091/a@sha256:aaa"},
	})
	if code != http.StatusBadRequest {
		t.Fatalf("asking about a place with no registry: %d %s", code, body)
	}
	if want := "local-k3s has no registry chosen — choose one in the deploy module's settings"; !strings.Contains(body, want) {
		t.Errorf("the refusal says %s, want %q", body, want)
	}
	if len(f.module.asked(t)) != 0 {
		t.Error("the module was asked anyway")
	}
}

// Each answer belongs to the image that was asked about, in the order they were asked. An
// answer that does not line up leaves that image "unknown" rather than giving it somebody
// else's, and an image nobody has asked about is shown as not known.
func TestAnAnswerGoesToTheImageItIsAbout(t *testing.T) {
	f := newAvailabilityFixture(t)
	f.setPlace(t, map[string]string{"registry": "192.168.1.103:8091"})

	// A module that answers about fewer images than it was asked about: one of the three
	// gets no answer, and it must not be given the first one's.
	f.module.answers = []map[string]string{
		{"path": "test/a", "reference": "sha256:aaa", "state": "missing"},
		{"path": "test/b", "reference": "sha256:bbb", "state": "present"},
	}
	code, body := f.ask(t, map[string]any{
		"cluster": "local-k3s",
		"images": []string{
			"192.168.1.103:8091/test/a@sha256:aaa",
			"192.168.1.103:8091/test/b@sha256:bbb",
			"192.168.1.103:8091/test/c@sha256:ccc",
		},
	})
	if code != http.StatusOK {
		t.Fatalf("asking: %d %s", code, body)
	}

	var answer struct {
		Images []imageAvailability `json:"images"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if len(answer.Images) != 3 {
		t.Fatalf("the answer carries %d rows, want one per image asked about", len(answer.Images))
	}
	if answer.Images[0].State != "missing" || answer.Images[1].State != "present" {
		t.Errorf("the first two rows are %+v", answer.Images)
	}
	if answer.Images[2].State != "unknown" {
		t.Errorf("the image nobody answered about is %q, want that it is not known",
			answer.Images[2].State)
	}
	if answer.Images[2].Image != "192.168.1.103:8091/test/c@sha256:ccc" {
		t.Errorf("the row is %q, want the name the catalogue has", answer.Images[2].Image)
	}
}

// A page is ten images and this endpoint is asked about them: fifty is where it stops being
// a page. Past that it is refused, because the cost of the question lands on somebody else's
// registry.
func TestMoreImagesThanAPageIsRefused(t *testing.T) {
	f := newAvailabilityFixture(t)
	f.setPlace(t, map[string]string{"registry": "192.168.1.103:8091"})

	images := make([]string, 0, availabilityLimit+1)
	for i := range availabilityLimit + 1 {
		images = append(images, "192.168.1.103:8091/test/app@sha256:"+strings.Repeat("a", i%4+1))
	}
	code, body := f.ask(t, map[string]any{"cluster": "local-k3s", "images": images})
	if code != http.StatusBadRequest {
		t.Fatalf("%d images: %d %s, want a refusal", len(images), code, body)
	}
	if len(f.module.asked(t)) != 0 {
		t.Error("the module was asked anyway")
	}

	// And exactly fifty is fine, which is the boundary a limit is for.
	code, _ = f.ask(t, map[string]any{"cluster": "local-k3s", "images": images[:availabilityLimit]})
	if code != http.StatusOK {
		t.Errorf("%d images: %d, want it asked", availabilityLimit, code)
	}
}

// An image with neither a digest nor a tag is not asked about at all, and is reported as not
// known rather than as gone. A registry asked about such a name can only say no, and that no
// would be read as "this image was deleted" — which is not what happened: nobody ever said
// which image the name was for.
func TestAnImageWithNoDigestAndNoTagIsNotAskedAboutAndIsNotCalledGone(t *testing.T) {
	f := newAvailabilityFixture(t)
	f.setPlace(t, map[string]string{"registry": "192.168.1.103:8091"})

	code, body := f.ask(t, map[string]any{
		"cluster": "local-k3s", "images": []string{"192.168.1.103:8091/test/app"},
	})
	if code != http.StatusOK {
		t.Fatalf("asking: %d %s", code, body)
	}
	if len(f.module.asked(t)) != 0 {
		t.Errorf("the module was asked about a name nobody could pull: %s", f.module.last(t))
	}
	var answer struct {
		Images []imageAvailability `json:"images"`
	}
	if err := json.Unmarshal([]byte(body), &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if len(answer.Images) != 1 || answer.Images[0].State != "unknown" {
		t.Errorf("the row is %+v, want the image reported as not known", answer.Images)
	}
}

// availabilityFixture is one project, one deploy module that answers questions like a
// server, and one place.
//
// The router is built the way the web server builds it — inside a group mounted at /api/v1 —
// because that is the only way the project routes resolve at all: mounting changes how chi
// reads the patterns, and a test against the bare router answers 404 for every request while
// looking perfectly well built.
type availabilityFixture struct {
	store    *store.Store
	router   chi.Router
	session  string
	module   *fakeDeployModule
	project  *models.Project
	deployer *models.Integration
}

func newAvailabilityFixture(t *testing.T) *availabilityFixture {
	t.Helper()

	st := dbtest.Open(t)
	for _, kind := range []string{registryKind, "deploy:kubernetes"} {
		if _, err := st.Pool().Exec(t.Context(),
			`DELETE FROM integrations WHERE kind = $1`, kind); err != nil {
			t.Fatalf("clear the modules left by earlier tests: %v", err)
		}
	}

	module := newFakeDeployModule(t)
	deployer, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("deploy"), module.URL,
		models.Manifest{Settings: []models.SettingSpec{{
			Key: "clusters", Type: "list",
			Items: &models.SettingItems{
				Identify: []string{"name"},
				Fields: []models.SettingSpec{
					{Key: "name", Type: "string"},
					{Key: "registry", Type: "registry"},
				},
			},
		}}})
	if err != nil {
		t.Fatalf("register the deploy module: %v", err)
	}
	if _, err := st.Integrations().Register(t.Context(), registryKind,
		dbtest.Unique("registry"), "http://module-registry:8091",
		models.Manifest{
			Routing:  models.RoutingSpec{Domains: []string{modulePublishedAddress}},
			Settings: []models.SettingSpec{{Key: "public_address", Type: "string"}},
		}); err != nil {
		t.Fatalf("register the registry module: %v", err)
	}

	user := dbtest.NewUser(t, st, "availability", false)
	project := dbtest.NewProject(t, st, "availability", nil)
	dbtest.GrantRole(t, st, project.ID, user.ID, models.AccessLevelOwner, "Owner")

	git := gitx.New(gitx.Options{})
	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, t.TempDir()),
	}
	router := chi.NewRouter()
	router.Route("/api/v1", func(v1 chi.Router) { srv.Register(v1) })

	return &availabilityFixture{
		store: st, router: router, session: dbtest.NewSession(t, st, user.ID),
		module: module, project: project, deployer: deployer,
	}
}

// unique is an address of this test's own, so a fixture that writes one does not depend on
// what another test has written.
func (f *availabilityFixture) unique(address string) string {
	return dbtest.Unique(address)
}

// setPlace writes what the one place's row says.
func (f *availabilityFixture) setPlace(t *testing.T, fields map[string]string) {
	t.Helper()
	if _, err := f.store.Pool().Exec(t.Context(),
		`DELETE FROM integration_settings WHERE integration_id = $1`, f.deployer.ID); err != nil {
		t.Fatalf("clear the place rows: %v", err)
	}
	row := map[string]any{"name": "local-k3s"}
	for key, value := range fields {
		row[key] = value
	}
	rows, err := json.Marshal([]map[string]any{row})
	if err != nil {
		t.Fatalf("describe the place row: %v", err)
	}
	if _, err := f.store.Pool().Exec(t.Context(),
		`INSERT INTO integration_settings (integration_id, scope_type, key, value)
		 VALUES ($1, 'instance', 'clusters', $2)`, f.deployer.ID, rows); err != nil {
		t.Fatalf("write the place row: %v", err)
	}
}

// ask is the request a page makes: this place's images, one page of them.
func (f *availabilityFixture) ask(t *testing.T, body map[string]any) (int, string) {
	t.Helper()

	payload := ""
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding the request: %v", err)
		}
		payload = string(raw)
	}
	// The target names the kind of module, as the page sends it: "kubernetes" for the
	// module of kind "deploy:kubernetes".
	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/projects/"+f.project.Path+"/deploy-images-availability?target=kubernetes",
		strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session})

	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// fakeDeployModule is the module as a real server: it records what it was asked and answers
// what the test says it should.
type fakeDeployModule struct {
	*httptest.Server
	mu      sync.Mutex
	asked_  [][]byte
	answers []map[string]string
}

func newFakeDeployModule(t *testing.T) *fakeDeployModule {
	t.Helper()
	fake := &fakeDeployModule{}
	fake.Server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.Close)
	return fake
}

func (f *fakeDeployModule) serve(w http.ResponseWriter, r *http.Request) {
	// What is running in a place: a place the core has not already answered for reaches
	// here, so the fake has to answer it or the question never gets that far.
	if strings.HasSuffix(r.URL.Path, "/current") {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"known":true,"asked":"the cluster","desired":1}`))
		return
	}
	if !strings.HasSuffix(r.URL.Path, "/images-availability") {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.asked_ = append(f.asked_, body)
	answers := f.answers
	f.mu.Unlock()

	if len(answers) == 0 {
		// The default: everything asked about is there, so a test about something else
		// does not fail on this. A manifest with no repository in it is refused, because
		// that is what the real module does and the core has to pass the refusal on.
		var question struct {
			Images []struct {
				Path      string `json:"path"`
				Reference string `json:"reference"`
			} `json:"images"`
		}
		_ = json.Unmarshal(body, &question)
		if len(question.Images) == 0 {
			http.Error(w, "no manifest was asked about", http.StatusBadRequest)
			return
		}
		answers = make([]map[string]string, 0, len(question.Images))
		for _, one := range question.Images {
			answers = append(answers, map[string]string{
				"path": one.Path, "reference": one.Reference, "state": "present",
			})
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"images": answers})
}

// asked is every question this module was asked.
func (f *fakeDeployModule) asked(t *testing.T) [][]byte {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked_
}

// last is the most recent question, as it crossed the wire.
func (f *fakeDeployModule) last(t *testing.T) []byte {
	t.Helper()
	all := f.asked(t)
	if len(all) == 0 {
		t.Fatal("the module was not asked anything")
	}
	return all[len(all)-1]
}

// A place this project has switched off is answered by the core, which is where the switch
// lives, and answered as a fact: the module would refuse, and a refusal that arrives as a
// failed request is a fact the page cannot show and the browser can only log.
func TestAPlaceThatIsSwitchedOffIsAnsweredRatherThanRefused(t *testing.T) {
	f := newAvailabilityFixture(t)
	f.setPlace(t, map[string]string{"registry": "192.168.1.103:8091"})
	// The project's own row says the place is off, which is where a project's switch is
	// kept: the module's row says the place exists, the project's row says this project
	// may not deploy to it.
	if _, err := f.store.Pool().Exec(t.Context(),
		`INSERT INTO integration_settings (integration_id, scope_type, scope_id, key, value)
		 VALUES ($1, 'project', $2, 'clusters', $3)`, f.deployer.ID, f.project.ID,
		`[{"name":"local-k3s","enabled":false}]`); err != nil {
		t.Fatalf("write the project's own row: %v", err)
	}

	code, body := f.askCurrent(t, "local-k3s")
	if code != http.StatusOK {
		t.Fatalf("a place that is switched off: %d %s, want an answer, not a refusal", code, body)
	}
	if !strings.Contains(body, "switched off for this project") {
		t.Errorf("the answer does not say why nothing is known: %s", body)
	}
	// And the module was never asked: the switch is the core's own fact and asking
	// somebody else about it is asking a question the core can answer.
	if len(f.module.asked(t)) != 0 {
		t.Errorf("the module was asked about a place the core knows is off: %s", f.module.last(t))
	}

	// A place that is on is asked about as before, because there is nothing to answer
	// without asking.
	code, body = f.askCurrent(t, "deploy2")
	if code != http.StatusOK {
		t.Fatalf("a place that is in use: %d %s", code, body)
	}
}

// askCurrent is the question a card asks about what is running in one place.
func (f *availabilityFixture) askCurrent(t *testing.T, cluster string) (int, string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/projects/"+f.project.Path+
			"/deploy-current?target=kubernetes&cluster="+cluster, nil)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session})

	recorder := httptest.NewRecorder()
	f.router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}
