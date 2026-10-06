package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// A list whose entries have names is inherited entry by entry, the way a module's rows
// are: a project that changes one cluster's namespace has not stopped using the others,
// and without that a lower level could only ever restate the whole thing.
func TestAListWithNamesIsInheritedEntryByEntry(t *testing.T) {
	f := setupSettings(t)

	clusterSpec := []models.SettingSpec{{
		Key:   "clusters",
		Type:  "list",
		Items: &models.SettingItems{Identify: []string{"name"}},
	}}

	f.set(t, store.ScopeInstance, nil, "clusters", `[
		{"name":"prod","kubeconfig":"K","context":"eu","default_namespace":"apps"},
		{"name":"stage","kubeconfig":"S","context":"eu","default_namespace":"apps"}
	]`)

	// One field, one cluster.
	f.set(t, store.ScopeProject, &f.proj, "clusters", `[
		{"name":"prod","default_namespace":"dogit"}
	]`)

	effective, err := f.store.Integrations().SettingsFor(t.Context(), f.module,
		nil, &f.proj, clusterSpec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	rows := entries(t, effective["clusters"])
	if len(rows) != 2 {
		t.Fatalf("the project sees %d clusters, want both of the instance's", len(rows))
	}

	prod := rows[0]
	if prod["default_namespace"] != "dogit" {
		t.Errorf("the namespace it changed is %v, want its own", prod["default_namespace"])
	}
	if prod["context"] != "eu" || prod["kubeconfig"] != "K" {
		t.Errorf("the fields it said nothing about are %v, want the inherited ones", prod)
	}
	if rows[1]["default_namespace"] != "apps" || rows[1]["kubeconfig"] != "S" {
		t.Errorf("the other cluster was disturbed: %v", rows[1])
	}
}

// A name is what says which entry an override is about, and a row that has not got one
// is not a reference to any row: it is kept as a row of its own rather than merged into
// a stranger.
func TestARowWithNoNameIsNotMergedIntoAnother(t *testing.T) {
	f := setupSettings(t)

	spec := []models.SettingSpec{{
		Key:   "clusters",
		Type:  "list",
		Items: &models.SettingItems{Identify: []string{"name"}},
	}}

	f.set(t, store.ScopeInstance, nil, "clusters", `[{"name":"prod","context":"eu"}]`)
	f.set(t, store.ScopeProject, &f.proj, "clusters", `[{"kubeconfig":"K"}]`)

	effective, err := f.store.Integrations().SettingsFor(t.Context(), f.module, nil, &f.proj, spec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rows := entries(t, effective["clusters"]); len(rows) != 2 {
		t.Errorf("the project sees %v, want the inherited row and the new one", rows)
	}
}

// A list the module names no fields of is replaced whole, because there is no telling
// which entry the smaller list meant, and a half-merged answer would be a guess.
func TestAListWithNoNamesIsReplacedWhole(t *testing.T) {
	f := setupSettings(t)

	spec := []models.SettingSpec{{Key: "mirrors", Type: "list"}}

	f.set(t, store.ScopeInstance, nil, "mirrors", `[{"url":"a"},{"url":"b"}]`)
	f.set(t, store.ScopeProject, &f.proj, "mirrors", `[{"url":"c"}]`)

	effective, err := f.store.Integrations().SettingsFor(t.Context(), f.module, nil, &f.proj, spec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rows := entries(t, effective["mirrors"]); len(rows) != 1 || rows[0]["url"] != "c" {
		t.Errorf("the project sees %v, want only what it set", rows)
	}
}

// Everything that is not a list keeps behaving as before: the lower level is the whole
// value.
func TestAPlainSettingStillReplaces(t *testing.T) {
	f := setupSettings(t)

	spec := []models.SettingSpec{{Key: "keep_jobs", Type: "bool"}}

	f.set(t, store.ScopeInstance, nil, "keep_jobs", `true`)
	f.set(t, store.ScopeProject, &f.proj, "keep_jobs", `false`)

	effective, err := f.store.Integrations().SettingsFor(t.Context(), f.module, nil, &f.proj, spec)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if string(effective["keep_jobs"]) != "false" {
		t.Errorf("the project's own value is %s, want false", effective["keep_jobs"])
	}
}

func entries(t *testing.T, raw json.RawMessage) []map[string]any {
	t.Helper()
	var out []map[string]any
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("read the list %s: %v", raw, err)
	}
	return out
}

type settingsFixture struct {
	store  *store.Store
	module uuid.UUID
	proj   uuid.UUID
}

func setupSettings(t *testing.T) *settingsFixture {
	t.Helper()

	st := dbtest.Open(t)

	// A module per test: registration is idempotent on (kind, name), so one name
	// would hand every test the clusters the last one left behind.
	module, err := st.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("kubernetes"), "http://module-deploy:8094", []byte("hash"),
		models.Manifest{Settings: []models.SettingSpec{{Key: "clusters", Type: "list"}}})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(),
			`DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	return &settingsFixture{
		store:  st,
		module: module.ID,
		proj:   dbtest.NewProject(t, st, "settings", nil).ID,
	}
}

func (f *settingsFixture) set(t *testing.T, scope string, scopeID *uuid.UUID, key, value string) {
	t.Helper()
	if err := f.store.Integrations().SetSetting(t.Context(), f.module, scope, scopeID,
		key, json.RawMessage(value)); err != nil {
		t.Fatalf("set %s at %s: %v", key, scope, err)
	}
}

// Switching a cluster off below the level that wrote it takes it out of what applies
// there, and only there. Off is not deleted: the row is still written down, and the
// level above still has it.
func TestASwitchedOffRowAppliesOnlyWhereItWasSwitchedOff(t *testing.T) {
	f := setupSettings(t)

	spec := []models.SettingSpec{{
		Key:   "clusters",
		Type:  "list",
		Items: &models.SettingItems{Identify: []string{"name"}},
	}}

	f.set(t, store.ScopeInstance, nil, "clusters", `[
		{"name":"prod","kubeconfig":"K"},
		{"name":"stage","kubeconfig":"S"}
	]`)
	f.set(t, store.ScopeProject, &f.proj, "clusters", `[{"name":"stage","enabled":false}]`)

	effective, err := f.store.Integrations().SettingsFor(t.Context(), f.module, nil, &f.proj, spec)
	if err != nil {
		t.Fatalf("resolve for the project: %v", err)
	}
	rows := entries(t, effective["clusters"])
	if len(rows) != 2 {
		t.Fatalf("the project sees %d clusters, want both, one of them switched off", len(rows))
	}
	if rows[1]["enabled"] != false {
		t.Errorf("the second cluster is %v, want it switched off here", rows[1])
	}

	// Another project is unaffected: the switch was written down at this project's
	// scope and belongs to it alone.
	other := dbtest.NewProject(t, f.store, "other", nil)
	above, err := f.store.Integrations().SettingsFor(t.Context(), f.module, nil, &other.ID, spec)
	if err != nil {
		t.Fatalf("resolve for another project: %v", err)
	}
	for _, row := range entries(t, above["clusters"]) {
		if _, said := row["enabled"]; said {
			t.Errorf("another project sees %v, which was decided by somebody else", row)
		}
	}
}
