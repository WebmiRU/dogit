package api

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// "enabled" is the core's word rather than the module's, so a module that never
// declared it can still have it written into one of its rows: it is how a scope below
// the one that wrote the row says "not here" without rewriting it.
func TestTheSwitchIsAcceptedInARowNobodyDeclaredItFor(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "switching", nil)

	if r := f.asAdmin(t, http.MethodPut,
		"/modules/"+module.ID.String()+"/settings?key=clusters&scope=instance",
		`{"value":[{"name":"prod","kubeconfig":"K"}]}`); r.Code != http.StatusOK {
		t.Fatalf("save at the instance: %s", r.Body.String())
	}

	r := f.asAdmin(t, http.MethodPut,
		"/modules/"+module.ID.String()+"/settings?key=clusters&scope=project&projectID="+
			project.ID.String(),
		`{"value":[{"name":"prod","enabled":false}]}`)
	if r.Code != http.StatusOK {
		t.Fatalf("switch it off: %d %s", r.Code, r.Body.String())
	}

	var stored struct {
		Settings []struct {
			Value json.RawMessage `json:"value"`
		} `json:"settings"`
	}
	read := f.asAdmin(t, http.MethodGet,
		"/modules/"+module.ID.String()+"/settings?scope=project&projectID="+project.ID.String(), "")
	if err := json.Unmarshal(read.Body.Bytes(), &stored); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(stored.Settings) != 1 {
		t.Fatalf("the project stored %d rows, want the one it switched off", len(stored.Settings))
	}
	if !containsAll(string(stored.Settings[0].Value), `"enabled":false`) {
		t.Errorf("what was stored is %s, want the switch", stored.Settings[0].Value)
	}
	// And it did not carry the kubeconfig down with it: a switch says "not here", not
	// "here is my copy of your cluster".
	if containsAll(string(stored.Settings[0].Value), "kubeconfig") {
		t.Errorf("the switch copied the cluster down: %s", stored.Settings[0].Value)
	}
}

// A row of a list nobody can identify cannot be switched off by name, so a switch in it
// is refused rather than written where nothing would ever find it.
func TestTheSwitchIsRefusedInAListWithNoNames(t *testing.T) {
	f := newModuleFixture(t)
	module, err := f.store.Integrations().Register(t.Context(), "registry:mirror",
		dbtest.Unique("mirror"), "http://module-registry:8095",
		models.Manifest{Settings: []models.SettingSpec{{
			Key:  "mirrors",
			Type: "list",
			Items: &models.SettingItems{Fields: []models.SettingSpec{
				{Key: "url", Label: "URL", Type: "string"},
			}},
		}}})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() { deleteIntegration(t, f, module.ID) })

	r := f.asAdmin(t, http.MethodPut,
		"/modules/"+module.ID.String()+"/settings?key=mirrors&scope=instance",
		`{"value":[{"url":"https://a","enabled":false}]}`)
	if r.Code == http.StatusOK {
		t.Error("a switch was accepted in a list whose entries have no names")
	}
}
