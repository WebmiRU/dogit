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
// Changing one switch must not change the other.
//
// A save replaces a level's whole list of rows, so a page sends the row it has — which
// carries this scope's other decisions too. They survive only if the core compares the
// row against the level above and not against what this level said last time: an
// Autodeploy that is off is not a copy of anything above it, and dropping it as one
// turns a switch off by hand back on by itself.
func TestChangingOneSwitchLeavesTheOtherAlone(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "two-switches", nil)
	scope := "scope=project&projectID=" + project.ID.String()

	put := func(query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save at %s: %d %s", query, recorder.Code, recorder.Body.String())
		}
	}
	read := func() string {
		t.Helper()
		recorder := f.asAdmin(t, http.MethodGet,
			"/modules/"+module.ID.String()+"/settings?"+scope, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("read: %d %s", recorder.Code, recorder.Body.String())
		}
		var answer struct {
			Own map[string]json.RawMessage `json:"own"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("read the answer: %v", err)
		}
		return string(answer.Own["clusters"])
	}

	put("scope=instance", `[{"name":"prod","kubeconfig":"K"}]`)
	put(scope, `[{"name":"prod","auto_deploy":false}]`)
	// Then somebody switches In use off, and the row the page sends still carries the
	// Autodeploy it was sent.
	put(scope, `[{"name":"prod","enabled":false,"auto_deploy":false}]`)

	if got := read(); !strings.Contains(got, `"auto_deploy":false`) {
		t.Errorf("the project has %s, want its Autodeploy off still", got)
	}

	// And it stays off when the page is not even the one saving: what applies is what
	// the switches say.
	effective, err := f.store.Integrations().SettingsFor(t.Context(), module.ID, nil,
		&project.ID, module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := string(effective["clusters"]); !strings.Contains(got, `"auto_deploy":false`) {
		t.Errorf("what applies is %s, want Autodeploy off", got)
	}
}

// A name is a decision like any other field, and an empty one is no decision.
//
// The core used to send the inherited name as though this scope had written it, so a
// page that cleared the name and saved was handed the same name back — which reads as
// "clearing a field does nothing" and teaches that about every field on the page. The
// name comes as itself, and a name of this scope's own comes as a field like any other:
// a rename, and one field overridden rather than a second row.
func TestANameIsDecidedHereOrItIsNotDecided(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "names", nil)
	scope := "scope=project&projectID=" + project.ID.String()

	put := func(query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save at %s: %d %s", query, recorder.Code, recorder.Body.String())
		}
	}
	ownOf := func(query string) string {
		t.Helper()
		recorder := f.asAdmin(t, http.MethodGet,
			"/modules/"+module.ID.String()+"/settings?"+query, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", query, recorder.Code, recorder.Body.String())
		}
		var answer struct {
			Own map[string]json.RawMessage `json:"own"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("read the answer: %v", err)
		}
		return string(answer.Own["clusters"])
	}

	put("scope=instance", `[{"name":"prod","kubeconfig":"K"}]`)

	// The instance named it, so the project is shown the name as the row's address and
	// nothing of it as an answer: the name is how the row is found, not something this
	// project decided. Clearing the name below is what has to look like no decision —
	// with the name as an ordinary field, emptying it and saving was indistinguishable
	// from saving the name again, because the field was always there to be filled in.
	var rows []map[string]any
	if err := json.Unmarshal([]byte(ownOf(scope)), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("the project is shown %v, want the one place", err)
	}
	if rows[0]["name"] != "prod" {
		t.Errorf("the project is shown %v, want the place addressed by its name", rows[0])
	}
	if _, given := rows[0]["kubeconfig"]; given {
		t.Errorf("the project is handed a kubeconfig for a place it decided nothing of")
	}

	// Writing a name of its own is a rename of one row, not a second row of the same
	// cluster — and it is written down as a field, because this scope decided it. The row
	// carries the identity the core gave it, which is how the rename is found to be the
	// row it renames.
	id, _ := rows[0]["dogit_row_id"].(string)
	put(scope, `[{"dogit_row_id":"`+id+`","name":"prod-eu"}]`)
	got := ownOf(scope)
	if !strings.Contains(got, `"name":"prod-eu"`) {
		t.Errorf("the project is shown %s, want its own name", got)
	}

	// And what applies is one cluster still, under the name this project gave it.
	effective, err := f.store.Integrations().SettingsFor(t.Context(), module.ID, nil,
		&project.ID, module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rows := string(effective["clusters"]); strings.Count(rows, "dogit_row_id") != 1 {
		t.Errorf("what applies is %s, want one row", rows)
	}

	// Clearing it is no decision again, and the row goes back to being inherited — with
	// its switches intact, which is what an empty field must not take with it. The name
	// comes back, because that is the name of the row and not a decision: what this
	// project no longer says is anything about it.
	put(scope, `[{"name":"","auto_deploy":false}]`)
	got = ownOf(scope)
	if !strings.Contains(got, `"name":"prod"`) {
		t.Errorf("the project is shown %s, want the place back under the name it has", got)
	}
	if strings.Contains(got, `"prod-eu"`) {
		t.Errorf("the project is shown %s, want its own name gone again", got)
	}
	if !strings.Contains(got, `"auto_deploy":false`) {
		t.Errorf("the project is shown %s, want its Autodeploy still off", got)
	}
}

// A row says whether it was written here, because only the core can know.
//
// A row comes to a page stripped down to what this scope decided, so a cluster the
// instance configured arrives as a name and nothing else — indistinguishable from a row
// somebody added here and left alone. Which decides two things a page must not get
// wrong: whether the row may be deleted here, and whether saving a shorter list is a
// deletion or a no-op.
func TestARowSaysWhetherThisScopeWroteIt(t *testing.T) {
	f := newModuleFixture(t)
	module := registerDeployModule(t, f)
	project := dbtest.NewProject(t, f.store, "own-rows", nil)

	put := func(query, value string) {
		t.Helper()
		path := "/modules/" + module.ID.String() + "/settings?key=clusters&" + query
		if recorder := f.asAdmin(t, http.MethodPut, path, `{"value":`+value+`}`); recorder.Code != http.StatusOK {
			t.Fatalf("save at %s: %d %s", query, recorder.Code, recorder.Body.String())
		}
	}
	ownOf := func(query string) []map[string]any {
		t.Helper()
		recorder := f.asAdmin(t, http.MethodGet,
			"/modules/"+module.ID.String()+"/settings?"+query, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("read %s: %d %s", query, recorder.Code, recorder.Body.String())
		}
		var answer struct {
			Own map[string]any `json:"own"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
			t.Fatalf("read the answer: %v", err)
		}
		rows, _ := answer.Own["clusters"].([]any)
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.(map[string]any))
		}
		return out
	}

	put("scope=instance", `[{"name":"prod","kubeconfig":"K"}]`)

	// Nothing is above the instance, so everything on its page was written here.
	instanceRows := ownOf("scope=instance")
	if len(instanceRows) != 1 || instanceRows[0]["dogit_row_own"] != true {
		t.Errorf("the instance is shown %v, want its row said to be its own", instanceRows)
	}

	// The project's page: the instance's cluster is not its own, and a cluster it writes
	// down itself is.
	scope := "scope=project&projectID=" + project.ID.String()
	put(scope, `[{"name":"spare","kubeconfig":"K2"}]`)

	projectRows := ownOf(scope)
	if len(projectRows) != 2 {
		t.Fatalf("the project is shown %v, want both places", projectRows)
	}
	for _, row := range projectRows {
		wrote := row["dogit_row_own"] == true
		if row["name"] == "prod" && wrote {
			t.Errorf("a place written at the instance is claimed as the project's own: %v", row)
		}
		if row["name"] == "spare" && !wrote {
			t.Errorf("a place the project wrote is not said to be its own: %v", row)
		}
	}

	// And a page cannot claim one: ownership is the core's answer, so a row that says so
	// is not stored with it, and the next read tells the truth again.
	put(scope, `[{"name":"prod","dogit_row_own":true,"auto_deploy":false}]`)
	if rows := ownOf(scope); len(rows) == 0 || rows[0]["dogit_row_own"] == true {
		t.Errorf("the project is shown %v, want its row not claimed", rows)
	}
}

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
	if prod["dogit_row_name"] != "prod" {
		t.Errorf("the first place is %v, want it called prod", prod)
	}
	if prod["default_namespace"] != "dogit" {
		t.Errorf("the namespace it overrode is %v, want its own", prod["default_namespace"])
	}
	if _, given := prod["kubeconfig"]; given {
		t.Errorf("the page was handed a kubeconfig: %v", prod)
	}
	stage := rows[1].(map[string]any)
	if stage["dogit_row_name"] != "stage" {
		t.Errorf("the second place is %v, want it called stage too", stage)
	}
	// A place this project overrode nothing of comes as the core's own word for what it
	// is called, its identity, and the fields that identify it — the name and the
	// namespace it is addressed by. Those are not answers this scope gave and not
	// pretending to be: they are where the place is, and a page that cannot say where a
	// place is cannot ask about it. With the namespace missing, a card drawn for one
	// place was sent the history of every place of that name — one row, two clusters'
	// rollouts under it.
	//
	// Every other field is still this scope's business alone.
	for key := range stage {
		if key != "dogit_row_id" && key != "dogit_row_name" &&
			key != "name" && key != "default_namespace" {
			t.Errorf("the page was handed %s=%v for a place this project overrode nothing of",
				key, stage[key])
		}
	}
	if stage["name"] != "stage" {
		t.Errorf("the second place is addressed as %v, want it called stage", stage["name"])
	}
	if stage["default_namespace"] != nil {
		// This module identifies its rows by name alone, so the namespace is an ordinary
		// field: this project overrode nothing of it and is told nothing about it. The
		// deploy module on a real instance identifies rows by name and namespace, and
		// sends both as the row's address.
		t.Errorf("the page was handed a namespace this project decided nothing of: %v",
			stage["default_namespace"])
	}
	if stage["dogit_row_name"] != "stage" {
		t.Errorf("the second place is called %v, want it called stage", stage["dogit_row_name"])
	}

	// And nothing anywhere in the answer is a credential of the instance's: not in "own",
	// not in the raw rows, and not in the section that carries what the levels above
	// decided. That last part used to be enforced by the section not existing at all —
	// every value from above was refused the page outright — and now it is enforced by
	// what the module publishes downward: this module's manifest marks nothing as
	// inheritable, so a field it did not declare as safe stays above. A page that was sent
	// this instance's kubeconfig would be a page in every project that inherits a row of
	// clusters, and a module author forgetting a mark is exactly how that happens.
	body := recorder.Body.String()
	for _, secret := range []string{"INSTANCE-KUBECONFIG", "STAGE-KUBECONFIG"} {
		if strings.Contains(body, secret) {
			t.Errorf("the answer carries %q: %s", secret, body)
		}
	}
	if strings.Contains(body, `"effective"`) {
		t.Errorf("the answer still carries the effective values: %s", body)
	}

	var above struct {
		Inherited map[string][]map[string]any `json:"inherited"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &above); err != nil {
		t.Fatalf("read the inherited section: %v", err)
	}
	for key, rows := range above.Inherited {
		for _, row := range rows {
			for field := range row {
				if field == "dogit_row_id" || field == "name" {
					continue
				}
				t.Errorf("the inherited %s carries %q, which this module never published: %v",
					key, field, row)
			}
		}
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
