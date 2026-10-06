package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// A registry is an address an administrator wrote down, and the address is the only thing
// every registry has. So a record with nothing but an address has to be savable: the mirror
// that needs no credential is half of what this table is for, and a form that demanded a
// login would be describing private registries and calling them registries.
func TestARegistryNeedsNothingButAnAddress(t *testing.T) {
	env := newRegistryEnv(t, true)
	// The table is shared with every other test in this package and with the packages that
	// run against the same database, so this checks that one record was written rather than
	// that the table went from empty to holding one.
	before := env.total(t)
	env.mustCreate(t, map[string]any{"url": env.host()})

	if after := env.total(t); after != before+1 {
		t.Fatalf("the list went from %d records to %d, want one written", before, after)
	}
}

// The address is checked rather than merely required, because a record whose address has
// a space in it cannot be used by anything and can only be found again by scrolling.
func TestAnAddressThatIsNotAnAddressIsRefused(t *testing.T) {
	env := newRegistryEnv(t, true)
	// Every test in this package writes to the one table, so what this test checks is that
	// the count does not move — not that the table is empty.
	before := env.total(t)

	for _, bad := range []string{
		"",                       // nothing
		"registry example.com",   // a space in the middle
		"ftp://registry.test",    // not a scheme a registry is reached over
		"https://",               // a scheme and nothing else
		"registry.test:",         // a colon with no port after it
		"registry.test:notaport", // a colon with something that is not a port
		"::1:5000",               // IPv6 without brackets, unreadable as host:port
		"[::1:5000",              // a bracket that is never closed
	} {
		code, body := env.do(t, http.MethodPost, "/registry/docker",
			map[string]any{"url": bad})
		if code != http.StatusBadRequest {
			t.Errorf("%q: got %d %s, want 400", bad, code, strings.TrimSpace(body))
		}
	}
	if after := env.total(t); after != before {
		t.Errorf("the list went from %d records to %d after only bad addresses were sent", before, after)
	}
}

// And the addresses that are legal are accepted, including the ones people actually write:
// with a port, with a scheme, under a path prefix, over IPv6, and the private registry this
// instance itself pushes to.
func TestTheAddressesRealRegistriesAreReachedAt(t *testing.T) {
	env := newRegistryEnv(t, true)

	for _, good := range []string{
		"registry.example.com",
		"registry.example.com:5000",
		"https://registry.example.com",
		"http://192.168.1.103:8091",
		"harbor.example.com/v2",
		"https://harbor.example.com:443/v2",
		"[::1]:5000",
		env.host(),
	} {
		code, body := env.do(t, http.MethodPost, "/registry/docker",
			map[string]any{"url": good})
		if code != http.StatusCreated {
			t.Errorf("%q: got %d %s, want 201", good, code, strings.TrimSpace(body))
		}
	}
}

// The password is stored and editable and never returned. Not from the list, where ten
// registries would otherwise put ten passwords into the page, and not from the edit, where
// it would have to be put into a form for something to overwrite it with.
func TestAPasswordIsWrittenDownAndNeverSentBack(t *testing.T) {
	env := newRegistryEnv(t, true)

	code, body := env.do(t, http.MethodPost, "/registry/docker", map[string]any{
		"url": env.host(), "login": "robot", "password": "hunter2",
	})
	if code != http.StatusCreated {
		t.Fatalf("creating it: %d %s", code, body)
	}
	id := env.id(t, body)

	if strings.Contains(body, "hunter2") {
		t.Error("the answer to a create carried the password back")
	}

	_, list := env.do(t, http.MethodGet, "/registry/docker", nil)
	if strings.Contains(list, "hunter2") {
		t.Error("the list carried the password")
	}

	// The record itself, out of the answer as a browser reads it, is what says whether a
	// password is set — looked up by id rather than by counting occurrences, because the
	// list holds every registry on the table and another record's password is not this
	// record's password.
	_, fetched := env.do(t, http.MethodGet, "/registry/docker/"+id.String(), nil)
	if strings.Contains(fetched, "hunter2") {
		t.Error("the answer to a read of one registry carried the password")
	}
	var one struct {
		HasPassword bool   `json:"has_password"`
		Login       string `json:"login"`
		URL         string `json:"url"`
	}
	if err := json.Unmarshal([]byte(fetched), &one); err != nil {
		t.Fatalf("the answer to a read is not a registry: %v (%s)", err, fetched)
	}
	if !one.HasPassword {
		t.Errorf("the answer does not say that a password is set: %s", fetched)
	}
	if one.Login != "robot" || one.URL != env.host() {
		t.Errorf("the answer describes %q at %q", one.Login, one.URL)
	}

	// And it is still in the table, which is the whole point of storing it: the check
	// below is the only place in this test that may see the value.
	stored, err := env.store.DockerRegistries().ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if stored.Password != "hunter2" {
		t.Errorf("the table holds %q where the form sent hunter2", stored.Password)
	}
}

// A form is never given the password to hold, so editing a name cannot blank the
// credential. And an empty password sent on purpose clears it, because somebody who
// pasted the wrong one has to be able to say so.
func TestEditingARegistryKeepsThePasswordUnlessTheFormSaysOtherwise(t *testing.T) {
	env := newRegistryEnv(t, true)

	_, body := env.do(t, http.MethodPost, "/registry/docker", map[string]any{
		"url": env.host(), "login": "robot", "password": "hunter2",
	})
	id := env.id(t, body)

	// A form that sends what it was given, which is everything except the password.
	code, edit := env.do(t, http.MethodPatch, "/registry/docker/"+id.String(), map[string]any{
		"name": "internal mirror", "url": env.host(), "login": "robot",
		"insecure_tls": true, "read_only": true, "is_default": false, "enabled": true,
	})
	if code != http.StatusOK {
		t.Fatalf("editing it: %d %s", code, edit)
	}
	if !strings.Contains(edit, "internal mirror") || !strings.Contains(edit, `"insecure_tls":true`) {
		t.Errorf("the edit did not take: %s", edit)
	}

	stored, err := env.store.DockerRegistries().ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if stored.Password != "hunter2" {
		t.Errorf("editing a name cleared the password: %q", stored.Password)
	}
	if !stored.InsecureTLS || !stored.ReadOnly {
		t.Error("the switches did not take")
	}

	// And the way out of a wrong password.
	code, cleared := env.do(t, http.MethodPatch, "/registry/docker/"+id.String(),
		map[string]any{"password": ""})
	if code != http.StatusOK {
		t.Fatalf("clearing the password: %d %s", code, cleared)
	}
	if strings.Contains(cleared, `"has_password":true`) {
		t.Errorf("the answer still says a password is set: %s", cleared)
	}
	stored, err = env.store.DockerRegistries().ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if stored.Password != "" {
		t.Errorf("the password survived an empty one: %q", stored.Password)
	}
}

// The list is ten to a page and says how much of it there is, so the pager can say which
// ten of how many.
func TestTheListIsTenToAPageAndKnowsTheRest(t *testing.T) {
	env := newRegistryEnv(t, true)
	before := env.total(t)

	const written = 23
	for i := 0; i < written; i++ {
		env.mustCreate(t, map[string]any{"url": registryAddr(env.host(), i)})
	}
	// Read through every page of the table so that the count this test checks is a count
	// of this test's own records and not of whatever else the package wrote.
	_, last := env.do(t, http.MethodGet, "/registry/docker?page=99", nil)
	var all struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(last), &all); err != nil {
		t.Fatalf("the answer is not the list: %v (%s)", err, last)
	}
	wantTotal := before + written
	wantPages := (wantTotal + 9) / 10

	_, body := env.do(t, http.MethodGet, "/registry/docker?page=2", nil)
	var page struct {
		Total      int `json:"total"`
		Page       int `json:"page"`
		Pages      int `json:"pages"`
		PerPage    int `json:"per_page"`
		Registries []struct {
			ID string `json:"id"`
		} `json:"registries"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("the answer is not the list: %v (%s)", err, body)
	}
	if page.PerPage != 10 {
		t.Errorf("a page holds %d records, want 10", page.PerPage)
	}
	if len(page.Registries) != 10 {
		t.Errorf("the second page held %d records, want 10", len(page.Registries))
	}
	if page.Total != wantTotal {
		t.Errorf("counted %d records, %d were written", page.Total, written)
	}
	if page.Pages != wantPages {
		t.Errorf("says there are %d pages, want %d for %d records at ten to a page",
			page.Pages, wantPages, wantTotal)
	}
	if page.Page != 2 {
		t.Errorf("says it is page %d, want 2", page.Page)
	}
}

// A list of addresses an administrator wrote down is not everybody's business: it says which
// registries exist, and where they are, which is the beginning of somebody's infrastructure.
func TestTheListIsTheAdministratorsOnly(t *testing.T) {
	env := newRegistryEnv(t, true)
	env.mustCreate(t, map[string]any{"url": env.host()})

	notAdmin := newRegistryEnv(t, false)
	code, body := notAdmin.do(t, http.MethodGet, "/registry/docker", nil)
	if code != http.StatusForbidden {
		t.Fatalf("a member asked for the list and got %d %s", code, body)
	}
	if strings.Contains(body, "registry.example.com") {
		t.Error("the refusal carried the address anyway")
	}

	// And writing one is the administrator's alone as well, in both directions.
	if code, _ := notAdmin.do(t, http.MethodPost, "/registry/docker",
		map[string]any{"url": "somewhere.example.com"}); code != http.StatusForbidden {
		t.Errorf("a member created a registry and got %d", code)
	}
	if code, _ := notAdmin.do(t, http.MethodDelete,
		"/registry/docker/"+uuid.NewString(), nil); code != http.StatusForbidden {
		t.Errorf("a member deleted a registry and got %d", code)
	}
}

// The default is one record's flag. Marking another registry the default moves it, because
// an administrator pressing that button means it, and having to clear the old one first is
// a step nobody will remember.
func TestMarkingOneRegistryDefaultMovesItFromTheLast(t *testing.T) {
	env := newRegistryEnv(t, true)
	// Both records are written plainly and then marked, rather than one of them being
	// created as the default: the table permits one default, so creating one would depend
	// on there not being one already, and whether there is depends on which other tests
	// have run against this database.
	_, one := env.mustCreate(t, map[string]any{"url": env.host() + "-one"})
	first := env.id(t, one)
	_, two := env.mustCreate(t, map[string]any{"url": env.host() + "-two"})
	id := env.id(t, two)

	code, body := env.do(t, http.MethodPatch, "/registry/docker/"+first.String(),
		map[string]any{"is_default": true})
	if code != http.StatusOK {
		t.Fatalf("marking the first one the default: %d %s", code, body)
	}

	code, body = env.do(t, http.MethodPatch, "/registry/docker/"+id.String(),
		map[string]any{"is_default": true})
	if code != http.StatusOK {
		t.Fatalf("marking it the default: %d %s", code, body)
	}

	// Both records read back by id: what matters is where the flag ended up on these two,
	// not how many records on the whole table happen to carry it.
	_, moved := env.do(t, http.MethodGet, "/registry/docker/"+id.String(), nil)
	if !strings.Contains(moved, `"is_default":true`) {
		t.Errorf("the registry that was marked is not marked: %s", moved)
	}
	_, before := env.do(t, http.MethodGet, "/registry/docker/"+first.String(), nil)
	if strings.Contains(before, `"is_default":true`) {
		t.Errorf("the registry that was the default still is: %s", before)
	}
}

// The address is the identity of a record, so the same address written down twice is a
// refusal with a sentence in it rather than a second row and a list nobody trusts.
func TestTheSameAddressTwiceIsRefusedWithASentence(t *testing.T) {
	env := newRegistryEnv(t, true)
	env.mustCreate(t, map[string]any{"url": env.host()})

	code, body := env.do(t, http.MethodPost, "/registry/docker",
		map[string]any{"url": env.host()})
	if code != http.StatusConflict && code != http.StatusBadRequest {
		t.Fatalf("the same address twice: %d %s", code, body)
	}
	if !strings.Contains(body, env.host()) {
		t.Errorf("the refusal does not name the address: %s", body)
	}
	if strings.Contains(body, `"error"`) && strings.Count(body, `"message"`) == 0 {
		t.Errorf("the refusal is an envelope with nothing in it: %s", body)
	}
}

// An id that was never written is a 404 and a path that could never be an id is a 400.
// Answering both the same way sends a reader looking for a record somebody deleted.
func TestEditingARegistryThatIsNotThere(t *testing.T) {
	env := newRegistryEnv(t, true)

	code, body := env.do(t, http.MethodPatch,
		"/registry/docker/"+uuid.NewString(), map[string]any{"name": "x"})
	if code != http.StatusNotFound {
		t.Errorf("editing a registry that is not there: %d %s", code, body)
	}
	code, body = env.do(t, http.MethodPatch, "/registry/docker/not-an-id",
		map[string]any{"name": "x"})
	if code != http.StatusBadRequest {
		t.Errorf("editing something that is not an id: %d %s", code, body)
	}
	code, _ = env.do(t, http.MethodDelete, "/registry/docker/"+uuid.NewString(), nil)
	if code != http.StatusNotFound {
		t.Errorf("deleting a registry that is not there: %d", code)
	}
}

// Deleting takes the record off the list and nothing else — and says so, because a button
// that emptied a table when it looked as though it would empty a registry is the sort of
// surprise an administrator finds out about from a customer's missing tag.
func TestDeletingRemovesTheRecordFromTheList(t *testing.T) {
	env := newRegistryEnv(t, true)
	_, body := env.mustCreate(t, map[string]any{"url": env.host(), "name": "temporary"})
	id := env.id(t, body)

	code, deleted := env.do(t, http.MethodDelete, "/registry/docker/"+id.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("deleting it: %d %s", code, deleted)
	}

	_, list := env.do(t, http.MethodGet, "/registry/docker", nil)
	if strings.Contains(list, env.host()) {
		t.Errorf("the address is still on the list: %s", list)
	}
}

// Every field of a record is a switch and a string, and a form sends what it was given —
// so editing one field must leave the rest exactly as they were. This is the test for a
// handler that cannot tell "not sent" from "sent as false": every switch would come back
// off the moment somebody changed a name.
func TestEditingOneFieldLeavesTheOthersAlone(t *testing.T) {
	env := newRegistryEnv(t, true)
	_, body := env.mustCreate(t, map[string]any{
		"url": env.host(), "name": "mirror", "login": "robot", "password": "hunter2",
		"insecure_tls": true, "read_only": true, "note": "owned by the platform team", "enabled": true,
	})
	id := env.id(t, body)

	code, edited := env.do(t, http.MethodPatch, "/registry/docker/"+id.String(),
		map[string]any{"name": "the mirror"})
	if code != http.StatusOK {
		t.Fatalf("editing the name: %d %s", code, edited)
	}

	stored, err := env.store.DockerRegistries().ByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading the record back: %v", err)
	}
	if stored.Name != "the mirror" {
		t.Errorf("the name is %q", stored.Name)
	}
	if !stored.InsecureTLS || !stored.ReadOnly || !stored.Enabled {
		t.Error("editing the name turned a switch off")
	}
	if stored.Login != "robot" || stored.Password != "hunter2" {
		t.Errorf("editing the name lost the credential: login %q, password set %v",
			stored.Login, stored.Password != "")
	}
	if stored.Note != "owned by the platform team" {
		t.Errorf("editing the name lost the note: %q", stored.Note)
	}
}

// registryEnv is an instance with one administrator (or one member) and a router to ask
// through.
type registryEnv struct {
	store   *store.Store
	router  http.Handler
	session string
	prefix  string
}

// newRegistryEnv builds the smallest thing that can answer these endpoints: a database, a
// git, a session for a user who is or is not an administrator, and the routes.
func newRegistryEnv(t *testing.T, admin bool) *registryEnv {
	t.Helper()

	st := dbtest.Open(t)

	// This table is shared with every other test in this package, and a list is a list of a
	// known size: "ten to a page", "the module's registry is on the first page and nine
	// written-down ones are beside it". Those sentences are only checkable against a list
	// this test built, so the fixture starts from an empty one rather than from whatever
	// an earlier test left behind — the same reason a page's own list is not a page's.
	if _, err := st.Pool().Exec(t.Context(), `DELETE FROM docker_registries`); err != nil {
		t.Fatalf("empty the table of registries: %v", err)
	}
	if _, err := st.Pool().Exec(t.Context(),
		`DELETE FROM integrations WHERE kind = $1`, registryKind); err != nil {
		t.Fatalf("remove the registry modules left by earlier tests: %v", err)
	}

	git := gitx.New(gitx.Options{})
	user := dbtest.NewUser(t, st, "registries", admin)
	session := dbtest.NewSession(t, st, user.ID)

	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, t.TempDir()),
	}

	return &registryEnv{
		store:   st,
		router:  srv.Routes(),
		session: session,
		prefix:  dbtest.Unique("registry.example.com"),
	}
}

func (e *registryEnv) host() string { return e.prefix }

// do asks the router as this user and answers with the status and the body.
func (e *registryEnv) do(t *testing.T, method, path string, body map[string]any) (int, string) {
	t.Helper()

	var payload string
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding the request: %v", err)
		}
		payload = string(raw)
	}

	request := httptest.NewRequest(method, path, strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: e.session})

	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	return recorder.Code, recorder.Body.String()
}

// mustCreate writes a registry down and fails the test if it did not happen.
func (e *registryEnv) mustCreate(t *testing.T, body map[string]any) (int, string) {
	t.Helper()
	code, answer := e.do(t, http.MethodPost, "/registry/docker", body)
	if code != http.StatusCreated {
		t.Fatalf("writing %v down: %d %s", body, code, answer)
	}
	return code, answer
}

// id is the id of the record the answer describes.
func (e *registryEnv) id(t *testing.T, body string) uuid.UUID {
	t.Helper()
	var created struct {
		ID uuid.UUID `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("the answer is not a registry: %v (%s)", err, body)
	}
	if created.ID == uuid.Nil {
		t.Fatalf("the answer has no id: %s", body)
	}
	return created.ID
}

// total is how many records the list says there are.
// total is how many addresses have been written down — the table, not the list the page
// draws. The list counts the registries a module brought as well, and a test that wants to
// know whether anything was written down has to ask the table.
func (e *registryEnv) total(t *testing.T) int {
	t.Helper()
	_, total, err := e.store.DockerRegistries().List(t.Context(),
		store.DockerRegistryListFilter{Limit: 1})
	if err != nil {
		t.Fatalf("counting what is on the list: %v", err)
	}
	return total
}

// itoa is strconv.Itoa under a name this package does not already use, so the addresses in
// these tests read as one thing rather than as a formatting call in the middle of them.
func registryAddr(base string, n int) string {
	return base + "-" + strconv.Itoa(n)
}
