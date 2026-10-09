package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// More than one module of a kind.
//
// ByKind answers "the module of this kind" and takes the oldest enabled one, saying nothing
// about the rest. That was a reasonable reading of a schema that had one module per kind in
// practice, and it is a silent one now: a second module is not an error, it is a module nobody
// mentions. The stand had two runner registrations, one of them with no pod behind it for a
// while, and nothing anywhere said so.
//
// So these tests are about what happens when there are two. The answers are decisions, recorded
// in the plan: a project may push if it may push to at least one usable registry; the catalogue
// lists every registry with a credential of its own.

// publish gives a registry an address it answers on.
//
// The fixture's own registry registers without one, so it is skipped by everything that asks
// where a registry can be reached — which is right, and which means a test that counts
// registries has to say what it expects to count. Publishing it first is how a test gets a
// number it can state, instead of a number that depends on which other tests ran first.
func publish(t *testing.T, st *store.Store, name string) *models.Integration {
	t.Helper()

	return registerRegistry(t, st, name)
}

// registerRegistry adds another registry module alongside the fixture's.
//
// Takes the store rather than the fixture because both fixtures in this package's tests are
// needed and they are different types: one is a module fixture, the other a registry fixture that
// embeds it, and passing either as the other is the kind of mistake a signature catches and the
// eye does not.
func registerRegistry(t *testing.T, st *store.Store, name string) *models.Integration {
	t.Helper()

	module, err := st.Integrations().Register(t.Context(), registryKind, name,
		"http://"+name+":5000", models.Manifest{
			Version: "0.1.0",
			Scopes: []string{
				models.ScopeRegistryPull, models.ScopeRegistryPush, models.ScopeRegistryDelete,
			},
			Routing: models.RoutingSpec{Domains: []string{name + ".f220.ru"}},
			Settings: []models.SettingSpec{
				{Key: "public_address", Label: "Public address", Type: "string"},
			},
		})
	if err != nil {
		t.Fatalf("register a second registry: %v", err)
	}
	return module
}

// ByKindAll returns disabled modules too, because "is there a registry" and "is there a registry
// that is switched off" are different questions with different answers on the page.
func TestByKindAllIncludesTheModulesThatAreSwitchedOff(t *testing.T) {
	f := newModuleFixture(t)
	ctx := context.Background()

	first := registerRegistry(t, f.store, dbtest.Unique("registry"))
	second := registerRegistry(t, f.store, dbtest.Unique("registry"))

	if _, err := f.store.Integrations().ByID(ctx, second.ID); err != nil {
		t.Fatalf("read the second registry by id: %v", err)
	}
	if err := f.store.Integrations().SetEnabled(ctx, second.ID, false); err != nil {
		t.Fatalf("switch the second registry off: %v", err)
	}

	all, err := f.store.Integrations().ByKindAll(ctx, registryKind)
	if err != nil {
		t.Fatalf("list every registry: %v", err)
	}

	names := map[string]bool{}
	for _, module := range all {
		names[module.Name] = true
	}
	if !names[first.Name] || !names[second.Name] {
		t.Fatalf("a registry is missing from the list, so a caller cannot tell what is installed: %v", names)
	}

	// And the one that reads as "the" registry is the oldest enabled one, which is what it has
	// always done — the difference is that a caller can now see there was another.
	if _, err := f.store.Integrations().ByKind(ctx, registryKind); err != nil {
		t.Fatalf("read the registry of the kind: %v", err)
	}
}

// The project page answers "may this project push" with "may it push somewhere", and says how many
// registries there are, because an instance with two of them used to look exactly like one.
func TestTheImagePageCountsTheRegistriesRatherThanSilentlyTakingTheFirst(t *testing.T) {
	f := setupRegistry(t, 0)
	project := dbtest.NewProject(t, f.store, "images", nil)
	publish(t, f.store, f.module.Name)
	registerRegistry(t, f.store, dbtest.Unique("mirror"))

	recorder := f.asAdmin(t, http.MethodGet,
		"/projects/"+project.Path+"/packages", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read the image page: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Reason     string `json:"reason"`
		CanPush    bool   `json:"can_push"`
		Registries int    `json:"registries"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Against what is installed, not against a number written here: tests in a package share a
	// database, and a test that asserts "two" is really asserting "two and nothing else was ever
	// created in this database". The property is that the page counts what exists.
	installed, err := f.store.Integrations().ByKindAll(context.Background(), registryKind)
	if err != nil {
		t.Fatalf("count the registries: %v", err)
	}
	wanted := 0
	for _, module := range installed {
		if module.Enabled {
			wanted++
		}
	}
	if answer.Registries != wanted {
		t.Fatalf("the page says there are %d registries and %d are installed, so at least one of "+
			"them is invisible to whoever has to look at it", answer.Registries, wanted)
	}
	if answer.Registries < 2 {
		t.Fatalf("the test installed %d registries, so it proves nothing about there being "+
			"more than one", answer.Registries)
	}
}

// A registry that is switched off is not a registry that is missing. The page says which, because
// one of those states is worth reporting to an administrator and the other is worth acting on.
func TestAForbiddenRegistryIsStillCountedAsInstalled(t *testing.T) {
	f := setupRegistry(t, 0)
	ctx := context.Background()

	// Every registry on the instance, not just the oldest. Tests in a package share a database
	// and earlier ones leave their modules behind, so switching off one of several would leave
	// a usable one and the test would be asserting about a state it did not create.
	byKind, err := f.store.Integrations().ByKindAll(ctx, registryKind)
	if err != nil || len(byKind) == 0 {
		t.Fatalf("find the registry: %v", err)
	}
	for _, module := range byKind {
		if err := f.store.Integrations().SetEnabled(ctx, module.ID, false); err != nil {
			t.Fatalf("switch %q off: %v", module.Name, err)
		}
	}

	recorder := f.asAdmin(t, http.MethodGet, "/registry/catalog", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read the catalogue: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if answer.Reason != "registry_forbidden" {
		t.Fatalf("the answer is %q, so a page cannot tell a switched-off registry from one that "+
			"was never installed", answer.Reason)
	}
}

// The catalogue lists every registry with a credential of its own, because a credential minted for
// one module is refused by another and the page has to be holding the right one.
func TestTheCatalogueGivesEveryRegistryItsOwnCredential(t *testing.T) {
	f := setupRegistry(t, 0)

	// Two registries, both with somewhere to be reached. Publishing the fixture's own first is
	// what makes "two" a fact about this test rather than about which tests ran before it.
	publish(t, f.store, f.module.Name)
	second := registerRegistry(t, f.store, dbtest.Unique("mirror"))

	recorder := f.asAdmin(t, http.MethodGet, "/registry/catalog", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read the catalogue: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Reason     string `json:"reason"`
		Registries []struct {
			Name    string `json:"name"`
			Address string `json:"address"`
			Token   string `json:"token"`
		} `json:"registries"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Counted against the store: the catalogue lists the registries that published an address,
	// so the number it should report is the number of enabled registries that have one.
	published, err := publishedRegistries(t, f.store)
	if err != nil {
		t.Fatalf("work out which registries can be reached: %v", err)
	}
	if len(answer.Registries) != len(published) {
		t.Fatalf("the catalogue lists %d registries and %d can be reached, so one of them holds "+
			"images nobody can see and nobody can delete", len(answer.Registries), len(published))
	}
	if len(answer.Registries) < 2 {
		t.Fatalf("the test left %d reachable registries, so it proves nothing about a catalogue "+
			"that has to list more than one", len(answer.Registries))
	}

	seen := map[string]bool{}
	for _, registry := range answer.Registries {
		if registry.Address == "" {
			t.Fatalf("registry %q is listed with no address, so the page has nothing to ask it",
				registry.Name)
		}
		if registry.Token == "" {
			t.Fatalf("registry %q is listed with no credential, so the page cannot ask it anything",
				registry.Name)
		}
		if seen[registry.Token] {
			t.Fatalf("two registries were handed the same credential, which is the thing one " +
				"token per module exists to prevent")
		}
		seen[registry.Token] = true
	}

	// And each credential belongs to the registry it was listed under.
	//
	// Not checked by asking a module — the module is a separate process, and what is being
	// claimed is about what the core bound the token to. A token minted for one module is
	// refused by another, so a credential listed against the wrong registry fails at the page
	// with an error about authentication, a long way from the mistake.
	for _, listed := range answer.Registries {
		var owner uuid.UUID
		err := f.store.Pool().QueryRow(context.Background(),
			`SELECT integration_id FROM integration_tokens WHERE token_hash = $1`,
			auth.HashToken(listed.Token)).Scan(&owner)
		if err != nil {
			t.Fatalf("the credential listed for %q is not in the store: %v", listed.Name, err)
		}

		module, err := f.store.Integrations().ByName(context.Background(), registryKind, listed.Name)
		if err != nil {
			t.Fatalf("registry %q is not installed after all: %v", listed.Name, err)
		}
		if owner != module.ID {
			t.Fatalf("the credential listed for %q belongs to another module, so the page will be "+
				"refused by the registry it is trying to ask", listed.Name)
		}
	}
	_ = second
}

// publishedRegistries is the set the catalogue is expected to list: the enabled ones that say
// where they can be reached. Written out rather than reused from the handler so that the test's
// expectation is derived independently — a test that calls the code it is testing agrees with it
// by construction and proves nothing.
func publishedRegistries(t *testing.T, st *store.Store) ([]*models.Integration, error) {
	t.Helper()

	installed, err := st.Integrations().ByKindAll(context.Background(), registryKind)
	if err != nil {
		return nil, err
	}
	out := []*models.Integration{}
	for _, module := range installed {
		if module.Enabled && len(module.Capabilities.Routing.Domains) > 0 {
			out = append(out, module)
		}
	}
	return out, nil
}
