package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

/** contains is a substring test, so a refusal can be checked by what it says. */
func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

// A recipient is a destination, and a destination is not a module's own business.
//
// The form on a project's page renders what the module declared, and the first
// version of it rendered all of it — which put "Bot token" on the page of somebody's
// repository, with a box to type into. These are the rules that keep it out: a module
// says which of its settings make a recipient, and anything else is refused by name
// rather than quietly dropped.

func recipientModule(t *testing.T, f *moduleFixture) *models.Integration {
	t.Helper()

	integration, err := f.store.Integrations().Register(t.Context(), "notify:telegram",
		dbtest.Unique("telegram"), "http://module-notify:8093", []byte("hash"), models.Manifest{
			Settings: []models.SettingSpec{
				{Key: "bot_token", Label: "Bot token", Type: "string", Secret: true},
				{Key: "chat_id", Label: "Chat id", Type: "string"},
			},
			Target: models.TargetSpec{
				Settings: []string{"chat_id"},
				Identify: []string{"chat_id"},
			},
		})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, integration.ID)
	})
	return integration
}

// A row is made of the settings the module named, and nothing else.
func TestARecipientRefusesSettingsThatAreNotOne(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "secrets", nil)
	module := recipientModule(t, f)

	body := `{"module_id":"` + module.ID.String() + `","label":"mine","enabled":true,` +
		`"values":{"chat_id":"-100a","bot_token":"secret"},"overrides":""}`

	recorder := f.asAdmin(t, "POST", "/module-targets?scope=project&projectID="+
		project.ID.String(), body)
	if recorder.Code != 400 {
		t.Fatalf("a recipient with a bot token was accepted: %d %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); !contains(got, "Bot token") {
		t.Errorf("the refusal does not name the setting: %s", got)
	}
}

// The same module, asked only for its destination.
func TestARecipientTakesTheSettingsTheModuleNamed(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "destinations", nil)
	module := recipientModule(t, f)

	body := `{"module_id":"` + module.ID.String() + `","label":"mine","enabled":true,` +
		`"values":{"chat_id":"-100a"},"overrides":""}`

	recorder := f.asAdmin(t, "POST", "/module-targets?scope=project&projectID="+
		project.ID.String(), body)
	if recorder.Code != 201 {
		t.Fatalf("a recipient of the named settings was refused: %d %s", recorder.Code, recorder.Body.String())
	}
}

// A setting nobody declared is refused too: a form and a core that have drifted apart
// should say so rather than store something nobody will read.
func TestARecipientRefusesSettingsNobodyDeclared(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "unknown", nil)
	module := recipientModule(t, f)

	body := `{"module_id":"` + module.ID.String() + `","enabled":true,` +
		`"values":{"webhook_url":"https://example.com"},"overrides":""}`

	recorder := f.asAdmin(t, "POST", "/module-targets?scope=project&projectID="+
		project.ID.String(), body)
	if recorder.Code != 400 {
		t.Fatalf("a setting the module does not have was accepted: %d", recorder.Code)
	}
}

// The list tells the interface which fields a recipient has, so the form on a
// project's page cannot ask for a secret even by accident.
func TestTheListSaysWhatARowIsMadeOf(t *testing.T) {
	f := newModuleFixture(t)
	recipientModule(t, f)

	recorder := f.asAdmin(t, "GET", "/module-targets?scope=instance", "")
	if recorder.Code != 200 {
		t.Fatalf("read the list: %d %s", recorder.Code, recorder.Body.String())
	}

	body := recorder.Body.String()
	if !contains(body, `"target"`) || !contains(body, `"settings":["chat_id"]`) {
		t.Errorf("the list does not say what a recipient is made of: %s", body)
	}
	if contains(body, "bot_token\"") && !contains(body, `"settings":["chat_id"]`) {
		t.Errorf("the list offers a secret as part of a destination: %s", body)
	}
}

// A switch pressed on a project must not switch the instance's row off.
//
// The row in the list is the one everybody sees, at every level below the one that
// wrote it. A project that presses "off" means off here, and writing that on the row
// itself switches it off for every project that inherits it — a change to the whole
// instance made from one project's page. An administrator pressing it made it
// silently; anybody else would have got a permission error and been left not
// understanding why their own project could not be changed at all.
// enabledOf is what one level's list says about the row called label, and nil when
// that list does not contain it.
//
// Found by name rather than by id, because a list does not answer to the id of the row
// the writer created: the id it shows is the deepest level's, which is a different row
// from the one the instance wrote and is exactly the difference this test is about.
func enabledOf(t *testing.T, f *moduleFixture, scope, moduleID, label string) *bool {
	t.Helper()

	recorder := f.asAdmin(t, "GET", "/module-targets?"+scope, "")
	if recorder.Code != 200 {
		t.Fatalf("read %s: %d %s", scope, recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Targets []struct {
			ModuleID string `json:"module_id"`
			Label    string `json:"label"`
			Enabled  bool   `json:"enabled"`
		} `json:"targets"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("read the list: %v", err)
	}
	for _, one := range answer.Targets {
		if one.ModuleID == moduleID && one.Label == label {
			enabled := one.Enabled
			return &enabled
		}
	}
	return nil
}

func TestASwitchOnAnInheritedRowBecomesAnOverride(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "switching", nil)
	module := recipientModule(t, f)

	// The instance's row, switched on.
	shared := f.asAdmin(t, "POST", "/module-targets?scope=instance", `{"module_id":"`+
		module.ID.String()+`","label":"shared","enabled":true,"values":{"chat_id":"-100"},"overrides":""}`)
	if shared.Code != 201 {
		t.Fatalf("add a shared row: %d %s", shared.Code, shared.Body.String())
	}
	var added struct {
		Target struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	if err := json.Unmarshal(shared.Body.Bytes(), &added); err != nil {
		t.Fatalf("read the row id: %v", err)
	}

	// A project switches it off, naming the row it was shown.
	off := f.asAdmin(t, "PATCH", "/module-targets/"+added.Target.ID+
		"?scope=project&projectID="+project.ID.String(), `{"enabled":false}`)
	if off.Code != 200 {
		t.Fatalf("switch the row off for a project: %d %s", off.Code, off.Body.String())
	}

	// The instance's own row is untouched.
	var scope string
	var overrides *uuid.UUID
	if err := f.store.Pool().QueryRow(t.Context(),
		`SELECT scope_type, overrides FROM module_targets WHERE id = $1`, added.Target.ID).
		Scan(&scope, &overrides); err != nil {
		t.Fatalf("read the shared row: %v", err)
	}
	if scope != "instance" || overrides != nil {
		t.Errorf("the shared row is now %q overriding %v, and should not have been touched",
			scope, overrides)
	}

	// And the project has a row of its own, saying what it decided.
	var enabled bool
	if err := f.store.Pool().QueryRow(t.Context(),
		`SELECT enabled FROM module_targets
		 WHERE integration_id = $1 AND scope_type = 'project'`, module.ID).Scan(&enabled); err != nil {
		t.Fatalf("read the project's own row: %v", err)
	}
	if enabled {
		t.Error("the project's own row does not say it is switched off")
	}

	// Which is what each level's own view says. Read by this row's id rather than by
	// searching the body for a word: the test database is shared, so "enabled": false
	// appears in it for rows this test never wrote.
	projectScope := "scope=project&projectID=" + project.ID.String()
	if on := enabledOf(t, f, projectScope, module.ID.String(), "shared"); on == nil || *on {
		t.Errorf("the project does not see its row as switched off: %v", on)
	}
	if off := enabledOf(t, f, "scope=instance", module.ID.String(), "shared"); off == nil || !*off {
		t.Errorf("the instance does not still see the row as switched on: %v", off)
	}
}

// deployModule is a module whose rows carry a switch of their own, and the target that
// reaches it.
//
// A kind of its own on purpose. Which deploy module does the work is decided by the
// oldest installed module of that kind, so a test sharing the kubernetes kind with
// whatever else the database holds would be asking about somebody else's rows — which
// is exactly the mistake this gate was rewritten to stop making.
func deployModule(t *testing.T, f *moduleFixture) (*models.Integration, string) {
	t.Helper()

	target := dbtest.Unique("places")
	integration, err := f.store.Integrations().Register(t.Context(), "deploy:"+target,
		dbtest.Unique("k8s"), "http://module-deploy-kubernetes:8094", []byte("hash"),
		models.Manifest{
			Settings: []models.SettingSpec{
				{Key: "name", Label: "Name", Type: "string"},
			},
			Target: models.TargetSpec{
				Settings: []string{"name"},
				Identify: []string{"name"},
				Flags: []models.TargetFlag{
					{Key: "auto_deploy", Label: "Autodeploy", Default: true},
				},
			},
		})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(t.Context(),
			`DELETE FROM integrations WHERE id = $1`, integration.ID)
	})
	return integration, target
}

// A switch nobody has touched stands where the module says it stands.
//
// Not at false, and this is the whole reason a key that is absent is not the same as a
// key that is false: a flag that defaulted to off would leave every existing cluster
// switched off by a field that had never been in the database at all, and the first
// push after the upgrade would quietly deploy nothing anywhere.
func TestASwitchNobodyHasTouchedStandsAtTheModulesDefault(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "flags", nil)
	module, _ := deployModule(t, f)

	added := f.asAdmin(t, "POST", "/module-targets?scope=instance", `{"module_id":"`+
		module.ID.String()+`","label":"prod","enabled":true,"values":{"name":"prod"},"overrides":""}`)
	if added.Code != 201 {
		t.Fatalf("add a row: %d %s", added.Code, added.Body.String())
	}

	var answer struct {
		Targets []struct {
			Label string          `json:"label"`
			Flags map[string]bool `json:"flags"`
		} `json:"targets"`
	}
	list := f.asAdmin(t, "GET", "/module-targets?kind=deploy:&scope=project&projectID="+
		project.ID.String(), "")
	if err := json.Unmarshal(list.Body.Bytes(), &answer); err != nil {
		t.Fatalf("read the list: %v", err)
	}
	if len(answer.Targets) != 1 {
		t.Fatalf("the project sees %d rows", len(answer.Targets))
	}
	if _, said := answer.Targets[0].Flags["auto_deploy"]; said {
		t.Error("the list claims a decision nobody made: " + list.Body.String())
	}
}

// A project may switch one place off and leave the others on, and that is the reason
// the switch is on the row rather than on the project.
func TestSwitchingOnePlaceOffLeavesTheOthersAlone(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "per-place", nil)
	module, target := deployModule(t, f)

	ids := map[string]string{}
	for _, name := range []string{"staging", "production"} {
		added := f.asAdmin(t, "POST", "/module-targets?scope=instance", `{"module_id":"`+
			module.ID.String()+`","label":"`+name+`","enabled":true,"values":{"name":"`+name+
			`"},"overrides":""}`)
		if added.Code != 201 {
			t.Fatalf("add %s: %d %s", name, added.Code, added.Body.String())
		}
		var one struct {
			Target struct {
				ID string `json:"id"`
			} `json:"target"`
		}
		if err := json.Unmarshal(added.Body.Bytes(), &one); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		ids[name] = one.Target.ID
	}

	off := f.asAdmin(t, "PATCH", "/module-targets/"+ids["production"]+
		"?scope=project&projectID="+project.ID.String(), `{"flags":{"auto_deploy":false}}`)
	if off.Code != 200 {
		t.Fatalf("switch production off: %d %s", off.Code, off.Body.String())
	}

	if !f.server.autodeployAllowed(t.Context(), project, target, "staging") {
		t.Error("staging was switched off by a change to production")
	}
	if f.server.autodeployAllowed(t.Context(), project, target, "production") {
		t.Error("production is still deploying itself after being switched off")
	}
}

// A flag the module never declared is refused by name rather than written down: the
// column is a map, and a map accepts anything.
func TestASwitchNobodyDeclaredIsRefused(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "undeclared", nil)
	module, _ := deployModule(t, f)

	added := f.asAdmin(t, "POST", "/module-targets?scope=instance", `{"module_id":"`+
		module.ID.String()+`","label":"prod","enabled":true,"values":{"name":"prod"},"overrides":""}`)
	var one struct {
		Target struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	if err := json.Unmarshal(added.Body.Bytes(), &one); err != nil {
		t.Fatalf("read the row: %v", err)
	}

	refused := f.asAdmin(t, "PATCH", "/module-targets/"+one.Target.ID+
		"?scope=project&projectID="+project.ID.String(), `{"flags":{"make_it_fast":true}}`)
	if refused.Code != 400 {
		t.Fatalf("a switch nobody declared was accepted: %d %s", refused.Code, refused.Body.String())
	}
	if !contains(refused.Body.String(), "make_it_fast") {
		t.Errorf("the refusal does not name the switch: %s", refused.Body.String())
	}
}

// The gate itself: a run that started by itself does not carry a deployment to a place
// that does not deploy by itself, and carries everything else.
//
// Checked here because this installation has no git hook, so a push never starts a run
// and the automatic path cannot be walked by pushing at it. The decision is the whole
// of what that path adds, and it is worth a test that says what it does to the jobs.
func TestARunThatStartedByItselfSkipsAPlaceThatDoesNotDeployByItself(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "gate", nil)
	module, target := deployModule(t, f)

	added := f.asAdmin(t, "POST", "/module-targets?scope=instance", `{"module_id":"`+
		module.ID.String()+`","label":"prod","enabled":true,"values":{"name":"prod"},"overrides":""}`)
	var one struct {
		Target struct {
			ID string `json:"id"`
		} `json:"target"`
	}
	if err := json.Unmarshal(added.Body.Bytes(), &one); err != nil {
		t.Fatalf("read the row: %v", err)
	}
	off := f.asAdmin(t, "PATCH", "/module-targets/"+one.Target.ID+
		"?scope=project&projectID="+project.ID.String(), `{"flags":{"auto_deploy":false}}`)
	if off.Code != 200 {
		t.Fatalf("switch it off: %d %s", off.Code, off.Body.String())
	}

	jobs := []store.Job{
		{Name: "image", Stage: "build"},
		{Name: "deploy:prod", Stage: "deploy", Deploy: map[string]any{
			"target": target, "cluster": "prod"}},
	}

	kept := f.server.withoutAutodeployJobs(t.Context(), project, jobs)
	if len(kept) != 1 || kept[0].Name != "image" {
		names := make([]string, 0, len(kept))
		for _, one := range kept {
			names = append(names, one.Name)
		}
		t.Errorf("the run kept %v, want the build and not the deployment", names)
	}
}
