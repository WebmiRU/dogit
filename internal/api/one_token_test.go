package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// One token per module, and it is the same token throughout.
//
// Before this, a module held two: the one an administrator registered it with, which the core
// checked once and forgot, and a second the core handed back at that moment and the module used
// for everything afterwards. Every one of the tests below is a property of removing that second
// one, and each of them is a thing that was either untrue before or had no meaning at all.

func oneTokenServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()

	st := dbtest.Open(t)
	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   gitx.New(gitx.Options{}),
		repos: repos.New(st, gitx.New(gitx.Options{}), t.TempDir()),
	}
	return srv, st
}

// mintModuleToken is the administrator's side: a token, created and never seen again.
func mintModuleToken(t *testing.T, st *store.Store, name string, expiresAt *time.Time) string {
	t.Helper()

	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	if _, err := st.ModuleTokens().Create(context.Background(), name, "", hash, expiresAt); err != nil {
		t.Fatalf("create a token: %v", err)
	}
	return plaintext
}

// register presents a token to the core the way a module does, and returns the answer.
func register(t *testing.T, srv *Server, token, kind, name string) *httptest.ResponseRecorder {
	t.Helper()

	body, err := json.Marshal(map[string]any{
		"kind": kind, "name": name, "endpoint": "http://" + kind + ":8091",
		"manifest": map[string]any{"version": "0.1.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/modules/register", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	recorder := httptest.NewRecorder()
	srv.Routes().ServeHTTP(recorder, request)
	return recorder
}

// call makes an authenticated request as a module, with whatever secret it holds.
func call(t *testing.T, srv *Server, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)

	recorder := httptest.NewRecorder()
	srv.Routes().ServeHTTP(recorder, request)
	return recorder
}

// The token a module registers with is the token it works with, and the core hands back that same
// secret rather than a new one.
//
// This is the property the whole arrangement rests on. Echoing rather than replacing is what lets
// a module store the answer and keep working without knowing anything changed — and it means
// there is never a moment when the core holds a credential the operator does not.
func TestTheTokenAModuleRegistersWithIsTheTokenItKeepsWorkingWith(t *testing.T) {
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	token := mintModuleToken(t, st, "runner", nil)
	recorder := register(t, srv, token, "runner:docker", name)

	if recorder.Code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode the answer: %v", err)
	}
	if answer.Token != token {
		t.Fatalf("the core handed back a different secret (%d characters against %d), so there "+
			"are still two tokens and the operator holds only one of them",
			len(answer.Token), len(token))
	}

	// And the secret in the module's hand is the one the core recognises as its credential.
	//
	// Asked with what the core *handed back*, not with what the test sent. Those are the same
	// string if the echo is right and different strings if it is wrong, and the difference is
	// invisible any other way: the response is a JSON string that parses, the registration
	// returns 200, and the module discovers the problem on its next request to the core, some
	// minutes later and one restart away. Asking with the echoed value is the only version of
	// this test that would have noticed.
	if got := call(t, srv, answer.Token, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusOK {
		t.Fatalf("the token the core handed back does not authenticate the module: status %d, body %s",
			got.Code, got.Body.String())
	}
}

// A token with an end stops working when it is reached — at registration and on every request
// afterwards.
//
// The second half is the half that is easy to leave out, and leaving it out makes the date a
// decoration: a core that reads the end date only when a module introduces itself keeps a module
// running on a credential that expired last week until the day it happens to restart, which for a
// module that registers on start-up is most of the time. A date nobody is stopped by is a comment.
func TestATokenIsRefusedOnceItsDateIsReached(t *testing.T) {
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	ended := time.Now().Add(-time.Hour)
	token := mintModuleToken(t, st, "already ended", &ended)

	recorder := register(t, srv, token, "runner:docker", name)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("an ended token registered a module: status %d, body %s",
			recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), ended.Format("2006")) {
		t.Fatalf("the refusal does not say when the token ended, so an operator has nothing to act "+
			"on beyond a status code: %s", recorder.Body.String())
	}

	// A module that registered while its token was good keeps going, and does not.
	later := time.Now().Add(time.Hour)
	live := mintModuleToken(t, st, "live for now", &later)
	if ok := register(t, srv, live, "runner:docker", name); ok.Code != http.StatusOK {
		t.Fatalf("a token with an end in the future did not register: status %d, body %s",
			ok.Code, ok.Body.String())
	}

	// Time passes. Written as an update rather than a wait, because a test that sleeps to reach
	// its own conclusion is a test that takes as long as the thing it is waiting for.
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE module_tokens SET expires_at = now() - interval '1 minute' WHERE name = 'live for now'`); err != nil {
		t.Fatalf("age the token: %v", err)
	}

	got := call(t, srv, live, http.MethodPost, "/module/heartbeat", "{}")
	if got.Code != http.StatusUnauthorized {
		t.Fatalf("a module kept working on a token that had ended: status %d, body %s",
			got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), "ended") {
		t.Fatalf("the module is refused without being told its token ran out: %s", got.Body.String())
	}
}

// No date is not a date that has passed.
func TestATokenWithNoDateDoesNotStopWorking(t *testing.T) {
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	token := mintModuleToken(t, st, "kept somewhere safe", nil)
	if got := register(t, srv, token, "runner:docker", name); got.Code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", got.Code, got.Body.String())
	}
	if got := call(t, srv, token, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusOK {
		t.Fatalf("a token with no date stopped working: status %d, body %s", got.Code, got.Body.String())
	}

	stored, err := st.ModuleTokens().ByHash(context.Background(),
		sha256Of(t, token))
	if err != nil {
		t.Fatalf("the token is not there: %v", err)
	}
	if stored.Expired(time.Now()) {
		t.Fatal("a token nobody put a date on reads as expired, which would mean every module on an " +
			"instance that did not want to think about it had to be reissued before it could start")
	}
}

// One token is one module's. A module presenting somebody else's is refused rather than given a
// second identity.
//
// Without this, one credential would authenticate any number of modules and every log line, every
// setting and every audit record would attribute itself to whichever of them presented it last.
func TestOneTokenDoesNotAuthenticateTwoModules(t *testing.T) {
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	token := mintModuleToken(t, st, "one module's", nil)
	if got := register(t, srv, token, "runner:docker", name); got.Code != http.StatusOK {
		t.Fatalf("register the first module: status %d, body %s", got.Code, got.Body.String())
	}

	other := dbtest.Unique("other")
	second := register(t, srv, token, "notify:telegram", other)
	if second.Code != http.StatusUnauthorized {
		t.Fatalf("one token registered a second module: status %d, body %s",
			second.Code, second.Body.String())
	}
	if !strings.Contains(second.Body.String(), fmt.Sprintf("runner:docker/%s", name)) {
		t.Fatalf("the refusal does not say whose token it is, so the module author has to guess "+
			"which of their tokens they used: %s", second.Body.String())
	}

	if _, err := st.Integrations().ByName(context.Background(), "notify:telegram", other); err == nil {
		t.Fatal("the second module is registered despite the refusal")
	}
}

// Registration is idempotent on (kind, name) so a module restarting in Kubernetes comes back as
// itself. That is what makes the name worth guarding: a fresh token must not be able to introduce
// itself as a module that already exists and take over everything under its name.
func TestANewTokenCannotTakeTheNameOfAModuleThatAlreadyExists(t *testing.T) {
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	first := mintModuleToken(t, st, "the module's own", nil)
	if got := register(t, srv, first, "runner:docker", name); got.Code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", got.Code, got.Body.String())
	}

	impostor := mintModuleToken(t, st, "somebody else's", nil)
	taken := register(t, srv, impostor, "runner:docker", name)
	if taken.Code != http.StatusUnauthorized {
		t.Fatalf("a new token took over an existing module's name: status %d, body %s",
			taken.Code, taken.Body.String())
	}

	// The module that was there is still the one that is there, and still the module its own
	// token authenticates.
	module, err := st.Integrations().ByName(context.Background(), "runner:docker", name)
	if err != nil {
		t.Fatalf("the module is gone: %v", err)
	}
	if got := call(t, srv, first, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusOK {
		t.Fatalf("the original module's token stopped working after somebody tried to take its "+
			"name: status %d, body %s", got.Code, got.Body.String())
	}
	if got := call(t, srv, impostor, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusUnauthorized {
		t.Fatalf("the token that tried to take the name can now act as the module: status %d", got.Code)
	}
	_ = module
}

// Cancelling a token and deleting a module are one act, because a module has one credential and
// there is no state in which it exists without it.
//
// The core could not tell such a module from a healthy one — it would answer requests it could no
// longer authenticate, and an operator reading the module list would see a module that looks
// installed.
func TestCancellingAModulesTokenRemovesTheModule(t *testing.T) {
	ctx := context.Background()
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	token := mintModuleToken(t, st, "the module's", nil)
	if got := register(t, srv, token, "runner:docker", name); got.Code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", got.Code, got.Body.String())
	}

	stored, err := st.ModuleTokens().ByHash(ctx, sha256Of(t, token))
	if err != nil {
		t.Fatalf("the token is not there: %v", err)
	}
	module, err := st.Integrations().ByID(ctx, *stored.IntegrationID)
	if err != nil {
		t.Fatalf("the module is not there: %v", err)
	}

	removed, err := st.ModuleTokens().Revoke(ctx, stored.ID)
	if err != nil {
		t.Fatalf("cancel the token: %v", err)
	}
	if removed == nil {
		t.Fatal("cancelling a module's token left the module in place, so there is now a module " +
			"whose credential cannot authenticate it")
	}
	if removed.ID != module.ID {
		t.Fatalf("cancelling the token removed a different module: %s", removed.Name)
	}
	if _, err := st.Integrations().ByID(ctx, module.ID); err == nil {
		t.Fatal("the module is still there after its only credential was cancelled")
	}
	if got := call(t, srv, token, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusUnauthorized {
		t.Fatalf("a cancelled token still authenticates: status %d", got.Code)
	}
}

// An invitation nobody used is cancelled and kept, because the row is also the record that it
// existed and is not to be used — and because there is no module to remove.
func TestCancellingATokenNobodyUsedTouchesNothing(t *testing.T) {
	ctx := context.Background()
	_, st := oneTokenServer(t)

	token := mintModuleToken(t, st, "never presented", nil)
	stored, err := st.ModuleTokens().ByHash(ctx, sha256Of(t, token))
	if err != nil {
		t.Fatalf("the token is not there: %v", err)
	}

	removed, err := st.ModuleTokens().Revoke(ctx, stored.ID)
	if err != nil {
		t.Fatalf("cancel an unused token: %v", err)
	}
	if removed != nil {
		t.Fatalf("cancelling an unused token removed a module: %s", removed.Name)
	}

	// Read directly rather than through ByHash, which deliberately refuses to find a cancelled
	// token — that is the behaviour that stops it being used, and it is exactly why this
	// question cannot be asked through it.
	var revokedAt *time.Time
	if err := st.Pool().QueryRow(ctx,
		`SELECT revoked_at FROM module_tokens WHERE id = $1`, stored.ID).Scan(&revokedAt); err != nil {
		t.Fatalf("the record of the cancelled token was thrown away: %v", err)
	}
	if revokedAt == nil {
		t.Fatal("the cancelled token is not marked as cancelled, so it will be offered again")
	}
	if _, err := st.ModuleTokens().ByHash(ctx, sha256Of(t, token)); err == nil {
		t.Fatal("a cancelled token is still accepted for use, so cancelling it changed nothing")
	}
}

// Deleting a module takes its token with it, which is what makes the arrangement impossible to get
// half-way: there is no operation that removes one and leaves the other.
func TestDeletingAModuleTakesItsTokenWithIt(t *testing.T) {
	ctx := context.Background()
	srv, st := oneTokenServer(t)
	name := dbtest.Unique("builder")

	token := mintModuleToken(t, st, "the module's", nil)
	if got := register(t, srv, token, "runner:docker", name); got.Code != http.StatusOK {
		t.Fatalf("register: status %d, body %s", got.Code, got.Body.String())
	}
	module, err := st.Integrations().ByName(ctx, "runner:docker", name)
	if err != nil {
		t.Fatalf("the module is not there: %v", err)
	}

	if err := st.Integrations().Delete(ctx, module.ID); err != nil {
		t.Fatalf("delete the module: %v", err)
	}

	if _, err := st.ModuleTokens().ByHash(ctx, sha256Of(t, token)); err == nil {
		t.Fatal("the module is gone and its token still authenticates, so a deleted module can be " +
			"brought back by whoever holds the old credential")
	}
	if got := call(t, srv, token, http.MethodPost, "/module/heartbeat", "{}"); got.Code != http.StatusUnauthorized {
		t.Fatalf("a deleted module's token still authenticates: status %d", got.Code)
	}
}

func sha256Of(t *testing.T, plaintext string) []byte {
	t.Helper()
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}
