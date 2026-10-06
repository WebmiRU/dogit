package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// registerDeployModule puts a deploy module in place whose clusters say what identifies
// a cluster, which is what makes them inherit entry by entry.
func registerDeployModule(t *testing.T, f *moduleFixture) *models.Integration {
	t.Helper()

	module, err := f.store.Integrations().Register(t.Context(), "deploy:kubernetes",
		dbtest.Unique("kubernetes"), "http://module-deploy:8094", []byte("hash"),
		models.Manifest{Settings: []models.SettingSpec{{
			Key:  "clusters",
			Type: "list",
			Items: &models.SettingItems{
				Identify: []string{"name"},
				Fields: []models.SettingSpec{
					{Key: "name", Label: "Name", Type: "string"},
					{Key: "kubeconfig", Label: "Kubeconfig", Type: "text"},
					{Key: "default_namespace", Label: "Namespace", Type: "string"},
				},
			},
		}}})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	t.Cleanup(func() { deleteIntegration(t, f, module.ID) })
	return module
}

func deleteIntegration(t *testing.T, f *moduleFixture, id uuid.UUID) {
	t.Helper()
	_, _ = f.store.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, id)
}

// A settings page is told what its own scope decided and nothing else.
//
// A value the level above holds is not sent at all — not masked, not blanked: a browser
// holding it is holding it whatever the page does with it. A row arrives as its name and
// the fields this scope overrode, so that a project can be shown a place it uses without
// being handed the instance's credential for it.
func TestASettingsPageIsToldOnlyWhatItsOwnScopeDecided(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "deciding", nil)

	put := func(scope, query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save at %s: %d %s", scope, recorder.Code, recorder.Body.String())
		}
	}
	put("the instance", "scope=instance", `[
		{"name":"prod","kubeconfig":"INSTANCE-KUBECONFIG","default_namespace":"apps"},
		{"name":"stage","kubeconfig":"STAGE-KUBECONFIG","default_namespace":"apps"}
	]`)
	put("the project", "scope=project&projectID="+project.ID.String(),
		`[{"name":"prod","default_namespace":"dogit"}]`)

	recorder := f.asAdmin(t, http.MethodGet,
		"/modules/"+module.ID.String()+"/settings?scope=project&projectID="+project.ID.String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Own      map[string]any `json:"own"`
		Settings []struct {
			Value json.RawMessage `json:"value"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}

	rows, ok := answer.Own["clusters"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("the project is shown %v, want both of its places", answer.Own["clusters"])
	}

	// The name is how the page says "this place exists"; everything else is this
	// project's own business.
	prod := rows[0].(map[string]any)
	if prod["name"] != "prod" {
		t.Errorf("the first place is %v, want it named", prod)
	}
	if prod["default_namespace"] != "dogit" {
		t.Errorf("the namespace it overrode is %v, want its own", prod["default_namespace"])
	}
	if _, given := prod["kubeconfig"]; given {
		t.Errorf("the page was handed a kubeconfig: %v", prod)
	}
	stage := rows[1].(map[string]any)
	if stage["name"] != "stage" {
		t.Errorf("the second place is %v, want it named too", stage)
	}
	// A place this project overrode nothing of comes as a name and its identity: the
	// name is how the page says "this place exists", and the identity is the core's own
	// bookkeeping, which is how the row is recognised after its name is changed here.
	for key := range stage {
		if key != "name" && key != "dogit_row_id" {
			t.Errorf("the page was handed %s=%v for a place this project overrode nothing of",
				key, stage[key])
		}
	}

	// And nothing anywhere in the answer is the instance's: not in "own", not in the
	// raw rows, and not under a key this build does not even read.
	body := recorder.Body.String()
	for _, secret := range []string{"INSTANCE-KUBECONFIG", "STAGE-KUBECONFIG", "inherited"} {
		if strings.Contains(body, secret) {
			t.Errorf("the answer carries %q: %s", secret, body)
		}
	}
	if strings.Contains(body, `"effective"`) {
		t.Errorf("the answer still carries the effective values: %s", body)
	}
}

// An emptied field is a decision to stop overriding: what it goes back to is the level
// above's, which the core knows.
func TestClearingAFieldLeavesNothingBehindForIt(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "clearing", nil)

	put := func(query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	put("scope=instance", `[{"name":"prod","default_namespace":"apps"}]`)
	put("scope=project&projectID="+project.ID.String(), `[{"name":"prod","default_namespace":""}]`)

	stored, err := f.store.Integrations().SettingsAt(t.Context(), module.ID, "project", &project.ID)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored) != 1 {
		t.Fatalf("the project has %d rows stored, want one", len(stored))
	}
	if strings.Contains(string(stored[0].Value), "default_namespace") {
		t.Errorf("the cleared field is still written down: %s", stored[0].Value)
	}

	// And what applies is the instance's again, because nothing here overrode it.
	effective, err := f.store.Integrations().SettingsFor(t.Context(), module.ID, nil,
		&project.ID, module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.Contains(string(effective["clusters"]), `"apps"`) {
		t.Errorf("after the override was lifted the project has %s, want the instance's namespace",
			effective["clusters"])
	}
}

// Deleting what a scope decided is how it stops deciding it, and the clusters above it
// come back — nothing is deleted at the level above.
func TestResettingOneScopeLeavesTheInheritedClusters(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "reset", nil)

	instance := "/modules/" + module.ID.String() + "/settings?key=clusters&scope=instance"
	if r := f.asAdmin(t, http.MethodPut, instance,
		`{"value":[{"name":"prod","kubeconfig":"K"}]}`); r.Code != http.StatusOK {
		t.Fatalf("save at the instance: %s", r.Body.String())
	}

	scope := "scope=project&projectID=" + project.ID.String()
	if r := f.asAdmin(t, http.MethodPut,
		"/modules/"+module.ID.String()+"/settings?key=clusters&"+scope,
		`{"value":[{"name":"prod","kubeconfig":"OTHER"}]}`); r.Code != http.StatusOK {
		t.Fatalf("save at the project: %s", r.Body.String())
	}

	if r := f.asAdmin(t, http.MethodDelete,
		"/modules/"+module.ID.String()+"/settings?key=clusters&"+scope, ""); r.Code != http.StatusOK {
		t.Fatalf("reset at the project: %s", r.Body.String())
	}

	effective, err := f.store.Integrations().SettingsFor(t.Context(), module.ID, nil,
		&project.ID, module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := string(effective["clusters"]); !containsAll(got, "prod", "K") || containsAll(got, "OTHER") {
		t.Errorf("after the reset the project has %s, want the instance's cluster back", got)
	}
}

// containsAll is strings.Contains for several needles at once, so the assertions above
// read as what they mean rather than as index arithmetic.
func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

// Nothing is above the instance. Its page must therefore say so, or every row on it
// reads as somebody else's row that this scope may not delete.
func TestTheInstanceHasNothingInherited(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)

	put := func(query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save: %d %s", recorder.Code, recorder.Body.String())
		}
	}
	put("scope=instance", `[{"name":"prod","kubeconfig":"K"}]`)

	recorder := f.asAdmin(t, http.MethodGet,
		"/modules/"+module.ID.String()+"/settings?scope=instance", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Inherited map[string]any `json:"inherited"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}
	if rows, ok := answer.Inherited["clusters"]; ok && rows != nil {
		t.Errorf("the instance is shown %v as inherited, want nothing at all", rows)
	}
}
