package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// moduleFixture is a registered module with a user token minted for it, which is
// the state every other module test needs.
type moduleFixture struct {
	server   *Server
	store    *store.Store
	module   *models.Integration
	admin    *models.User
	plain    *models.User
	token    string
	rawToken []byte

	// moduleToken is the instance token the module itself presents.
	moduleToken string
}

// registerModule records a module the way a real one does.
func registerModule(t *testing.T, st *store.Store, kind string) (*models.Integration, string, []byte) {
	t.Helper()

	ctx := context.Background()
	raw := kind + "-instance-token"
	hash := sha256.Sum256([]byte(raw))

	integration, err := st.Integrations().Register(ctx, kind, kind+" module",
		"http://"+kind+":9000", hash[:], models.Manifest{
			Version: "0.1.0",
			Scopes: []string{
				models.ScopeRegistryPull, models.ScopeRegistryPush, models.ScopeRegistryDelete,
			},
		})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM integrations WHERE id = $1`, integration.ID)
	})

	return integration, raw, hash[:]
}

// mintToken issues the token a module would be handed for a user.
func mintToken(t *testing.T, st *store.Store, integration *models.Integration, user *models.User, raw string) []byte {
	t.Helper()

	ctx := context.Background()
	hash := sha256.Sum256([]byte(raw))

	token := &models.IntegrationToken{
		IntegrationID: integration.ID,
		UserID:        &user.ID,
		Scopes:        []string{models.ScopeRegistryPush},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	if err := st.IntegrationTokens().Create(ctx, token, hash[:]); err != nil {
		t.Fatalf("mint a token: %v", err)
	}
	return hash[:]
}

func newModuleFixture(t *testing.T) *moduleFixture {
	t.Helper()

	st := dbtest.Open(t)
	admin := dbtest.NewUser(t, st, "module-admin", true)
	// The password is real because some tests sign in rather than holding a
	// session: anything that goes through the API's own credential check.
	plain := dbtest.NewUserWithPassword(t, st, "module-user", "secret123", false)

	integration, rawModuleToken, _ := registerModule(t, st, "registry:docker")
	userHash := mintToken(t, st, integration, plain, "user-token-value")

	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   gitx.New(gitx.Options{}),
		repos: repos.New(st, gitx.New(gitx.Options{}), t.TempDir()),
	}

	return &moduleFixture{
		server:      srv,
		store:       st,
		module:      integration,
		admin:       admin,
		plain:       plain,
		token:       "user-token-value",
		moduleToken: rawModuleToken,
		rawToken:    userHash,
	}
}

// introspect asks the core who the presented token belongs to, the way a module
// does on every request it serves.
func (f *moduleFixture) introspect(t *testing.T, presented string) models.Introspection {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, "/auth/introspect", strings.NewReader("{}"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+presented)

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("introspect: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var result models.Introspection
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// asAdmin calls an endpoint as the fixture's administrator.
func (f *moduleFixture) asAdmin(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	session := dbtest.NewSession(t, f.store, f.admin.ID)
	return f.as(t, session, method, path, body)
}

// as calls an endpoint as a given session.
func (f *moduleFixture) as(t *testing.T, session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)
	return recorder
}

// Forbidding a module has to stop it working immediately, without asking the
// module to do anything: the core simply stops saying who the caller is.
func TestForbiddingAModuleStopsItIdentifyingCallers(t *testing.T) {
	f := newModuleFixture(t)

	// While it is allowed, the module is told who the caller is.
	if result := f.introspect(t, f.token); !result.Active {
		t.Fatalf("introspection answered %+v for an allowed module", result)
	}

	path := "/modules/" + f.module.ID.String() + "/state"
	if recorder := f.asAdmin(t, http.MethodPut, path, `{"enabled": false}`); recorder.Code != http.StatusOK {
		t.Fatalf("forbid: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	// After being forbidden, the same token tells the module nothing, and a module
	// that cannot identify anyone has nothing to let through.
	result := f.introspect(t, f.token)
	if result.Active {
		t.Error("a forbidden module is still being introduced to callers")
	}
	if result.Username != "" || result.UserID != nil {
		t.Errorf("the answer still names the caller: %+v", result)
	}

	// Allowing it again restores the previous state, with nothing to re-register.
	if recorder := f.asAdmin(t, http.MethodPut, path, `{"enabled": true}`); recorder.Code != http.StatusOK {
		t.Fatalf("allow: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if result := f.introspect(t, f.token); !result.Active || result.Username != f.plain.Username {
		t.Errorf("after allowing, introspection answered %+v", result)
	}
}

// Forbidding a module is an administrator's decision and nobody else's.
func TestForbiddingAModuleNeedsAnAdministrator(t *testing.T) {
	f := newModuleFixture(t)

	path := "/modules/" + f.module.ID.String() + "/state"

	session := dbtest.NewSession(t, f.store, f.plain.ID)
	if recorder := f.as(t, session, http.MethodPut, path, `{"enabled": false}`); recorder.Code == http.StatusOK {
		t.Error("an ordinary user forbade a module")
	}

	// And nothing changed: the module still works.
	if result := f.introspect(t, f.token); !result.Active {
		t.Error("a refused request still disabled the module")
	}
}

// A token cannot outlive the module it was minted for, and a missing token is
// still simply "nobody" rather than an error.
func TestModuleStateRequestIsValidated(t *testing.T) {
	f := newModuleFixture(t)
	path := "/modules/" + f.module.ID.String() + "/state"

	if recorder := f.asAdmin(t, http.MethodPut, path, `{}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("a request without enabled: status %d, want 400", recorder.Code)
	}
	if recorder := f.asAdmin(t, http.MethodPut, "/modules/not-a-uuid/state", `{"enabled":false}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("a malformed id: status %d, want 400", recorder.Code)
	}

	// An unknown token is not an error: it is an inactive one, and the module
	// decides what to do about it.
	result := f.introspect(t, "no-such-token")
	if result.Active || len(result.Scopes) != 0 {
		t.Errorf("an unknown token answered %+v", result)
	}
}
