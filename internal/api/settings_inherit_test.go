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

// A project's settings page has to be able to tell its own rows from the ones it
// inherits, because otherwise saving it writes down somebody else's clusters as if this
// project had decided them.
//
// The page is given three things: what is stored here, what applies, and what would
// apply without this scope. Without the third there is no telling the difference.
func TestASettingsPageSeesWhatThisScopeDecidedAndWhatItInherited(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "inheriting", nil)

	put := func(scope, query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save at %s: %d %s", scope, recorder.Code, recorder.Body.String())
		}
	}

	put("the instance", "scope=instance",
		`[{"name":"prod","kubeconfig":"K","default_namespace":"apps"},{"name":"stage","kubeconfig":"S"}]`)
	put("the project", "scope=project&projectID="+project.ID.String(),
		`[{"name":"prod","default_namespace":"dogit"}]`)

	recorder := f.asAdmin(t, http.MethodGet,
		"/modules/"+module.ID.String()+"/settings?scope=project&projectID="+project.ID.String(), "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Effective  map[string]any `json:"effective"`
		Inherited  map[string]any `json:"inherited"`
		SettingsAt []struct {
			Value json.RawMessage `json:"value"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("read the answer: %v", err)
	}

	effective, ok := answer.Effective["clusters"].([]any)
	if !ok || len(effective) != 2 {
		t.Fatalf("what applies here is %v, want both clusters", answer.Effective["clusters"])
	}
	prod := effective[0].(map[string]any)
	if prod["default_namespace"] != "dogit" || prod["kubeconfig"] != "K" {
		t.Errorf("the first cluster is %v, want this project's namespace and the inherited kubeconfig", prod)
	}

	inherited, ok := answer.Inherited["clusters"].([]any)
	if !ok || len(inherited) != 2 {
		t.Fatalf("what this scope inherited is %v, want both clusters", answer.Inherited["clusters"])
	}
	if inherited[0].(map[string]any)["default_namespace"] != "apps" {
		t.Errorf("the inherited namespace is %v, want the instance's", inherited[0])
	}

	// And what is stored here is one row, not the two it ends up with.
	if len(answer.SettingsAt) != 1 {
		t.Errorf("this scope has %d rows stored, want the one it decided", len(answer.SettingsAt))
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
