package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
)

func newTestConfig() *config.Config { return &config.Config{} }
func testLogger() *slog.Logger      { return logger.Discard() }
func newRouteContext() *chi.Context { return chi.NewRouteContext() }

func TestValidateModuleKind(t *testing.T) {
	valid := []string{"registry:docker", "cache:npm", "builder:docker", "registry:composer"}
	for _, kind := range valid {
		if err := validateModuleKind(kind); err != nil {
			t.Errorf("expected %q to be valid, got %v", kind, err)
		}
	}

	// The kind is what scopes settings and namespaces endpoints, so a malformed one
	// has to be rejected before anything is stored.
	invalid := []string{"", "registry", ":docker", "registry:", "Registry:Docker",
		"registry:do cker", "registry:docker/../etc", "registry:докер"}
	for _, kind := range invalid {
		if err := validateModuleKind(kind); err == nil {
			t.Errorf("expected %q to be rejected", kind)
		}
	}
}

func TestValidateModuleEndpoint(t *testing.T) {
	valid := []string{
		"http://module-demo:8090",
		"https://registry.dogit.internal",
		"http://cache-npm.platform.svc.cluster.local:4873",
	}
	for _, endpoint := range valid {
		if err := validateModuleEndpoint(endpoint); err != nil {
			t.Errorf("expected %q to be valid, got %v", endpoint, err)
		}
	}

	// The core calls this address on registration, so anything that is not plain
	// HTTP has to be refused: a file path or a unix socket would turn a module
	// registration into a request forgery primitive.
	invalid := []string{
		"", "   ", "module-demo:8090", "ftp://module",
		"file:///etc/passwd", "unix:///var/run/docker.sock",
		"http://module host:8090", "http://module\nHost: evil",
	}
	for _, endpoint := range invalid {
		if err := validateModuleEndpoint(endpoint); err == nil {
			t.Errorf("expected %q to be rejected", endpoint)
		}
	}
}

func TestFilterScopesDropsUndeclaredScopes(t *testing.T) {
	integration := &models.Integration{
		Capabilities: models.Manifest{
			Scopes: []string{models.ScopeRegistryPull, models.ScopeRegistryPush},
		},
	}

	scopes, err := filterScopes(integration, []string{
		models.ScopeRegistryPull, "registry:delete", "", "  ", models.ScopeRegistryPull,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// A module never receives a scope it did not declare: a stale client or a
	// typo must not hand out a permission the module would not understand.
	if len(scopes) != 1 || scopes[0] != models.ScopeRegistryPull {
		t.Fatalf("got %v, want only registry:pull", scopes)
	}

	if _, err := filterScopes(integration, []string{"registry:delete"}); err == nil {
		t.Error("requesting only undeclared scopes must fail")
	}
	if _, err := filterScopes(integration, nil); err == nil {
		t.Error("an empty request must fail rather than minting an empty token")
	}
}

func TestRedactSettingsHidesSecrets(t *testing.T) {
	settings := map[string]json.RawMessage{
		"upstream":       json.RawMessage(`"https://registry.npmjs.org"`),
		"upstream_token": json.RawMessage(`"s3cr3t"`),
	}
	secrets := map[string]bool{"upstream_token": true}

	redacted := redactSettings(settings, secrets)

	if redacted["upstream_token"] == "s3cr3t" {
		t.Error("a secret setting was returned in clear text")
	}
	if redacted["upstream_token"] != "********" {
		t.Errorf("secret placeholder = %v", redacted["upstream_token"])
	}
	if redacted["upstream"] != "https://registry.npmjs.org" {
		t.Errorf("a non-secret value must be returned decoded, got %v", redacted["upstream"])
	}
}

func TestModuleDeclaresSetting(t *testing.T) {
	integration := &models.Integration{
		Capabilities: models.Manifest{
			Settings: []models.SettingSpec{{Key: "upstream"}, {Key: "token", Secret: true}},
		},
	}

	if !moduleDeclaresSetting(integration, "upstream") {
		t.Error("a declared setting was not recognised")
	}
	// Writing a setting the module never announced would leave it silently
	// ignored, which is worse than a rejection.
	if moduleDeclaresSetting(integration, "typo") {
		t.Error("an undeclared setting was accepted")
	}
}

func TestScopesForAccessLevelAlwaysIncludesRead(t *testing.T) {
	integration := &models.Integration{}

	member := scopesForAccessLevel(&models.User{Username: "bob"}, integration)
	if !containsScope(member, models.ScopeRegistryPull) {
		t.Errorf("an ordinary user must be able to pull, got %v", member)
	}

	admin := scopesForAccessLevel(&models.User{Username: "root", IsAdmin: true}, integration)
	for _, scope := range []string{models.ScopeRegistryPull, models.ScopeRegistryPush, models.ScopeRegistryDelete} {
		if !containsScope(admin, scope) {
			t.Errorf("an administrator should have %s, got %v", scope, admin)
		}
	}

	// The list must not contain duplicates, or a module would see the same scope
	// twice in a token.
	if len(admin) != len(unique(admin)) {
		t.Errorf("scopes contain duplicates: %v", admin)
	}
}

func containsScope(scopes []string, want string) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func unique(values []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func TestModuleSelfServiceIsSeparateFromAdminResources(t *testing.T) {
	// The two namespaces must not overlap. A module's self-service endpoints live
	// under "/module", while administrators address modules under "/modules/{id}".
	// Sharing one namespace would make a literal path such as /modules/me
	// indistinguishable from a module id, and the request would be answered by the
	// administrator handler instead of the module's own.
	srv := &Server{cfg: newTestConfig(), log: testLogger()}
	router := srv.Routes()

	self := newRouteContext()
	if !router.Match(self, http.MethodGet, "/module/settings") {
		t.Fatal("/module/settings should be reachable by a module")
	}
	if pattern := self.RoutePattern(); pattern != "/module/settings" {
		t.Errorf("/module/settings matched %q", pattern)
	}

	admin := newRouteContext()
	if !router.Match(admin, http.MethodGet, "/modules/aaa27c32-94af-45b7-9ea8-5cee3f724914") {
		t.Fatal("an administrator should be able to address a module by id")
	}
	if pattern := admin.RoutePattern(); pattern != "/modules/{integrationID}" {
		t.Errorf("a module id matched %q", pattern)
	}

	// A literal in the administrator namespace is rejected by the handler as an
	// invalid id rather than being served as if it were a module.
	if ctx := newRouteContext(); !router.Match(ctx, http.MethodGet, "/modules/settings") {
		t.Fatal("expected the wildcard route to match, so the handler validates the id")
	}
	if ctx := newRouteContext(); !router.Match(ctx, http.MethodGet, "/module/me") {
		t.Error("/module/me should be reachable by a module")
	}
}
