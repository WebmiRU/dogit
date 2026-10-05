package api

import (
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
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

	recorder := f.asAdmin(t, "POST", "/notification-targets?scope=project&projectID="+
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

	recorder := f.asAdmin(t, "POST", "/notification-targets?scope=project&projectID="+
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

	recorder := f.asAdmin(t, "POST", "/notification-targets?scope=project&projectID="+
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

	recorder := f.asAdmin(t, "GET", "/notification-targets?scope=instance", "")
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
