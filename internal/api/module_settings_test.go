package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// A settings page submits a form, not a series of unrelated actions, so the write
// is one request and one decision.
func TestSettingsAreSavedAsAForm(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	path := "/modules/" + rf.module.ID.String() + "/settings/bulk"
	body := `{"values":{"image_name_template":"{{project}}","read_timeout":600}}`

	recorder := rf.asAdmin(t, http.MethodPut, path, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("save: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	effective, err := rf.store.Integrations().SettingsFor(t.Context(), rf.module.ID, nil, nil, rf.module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}

	var template string
	if raw := effective["image_name_template"]; raw != nil {
		_ = json.Unmarshal(raw, &template)
	}
	if template != "{{project}}" {
		t.Errorf("the template stored is %q", template)
	}
	if !strings.Contains(string(effective["read_timeout"]), "600") {
		t.Errorf("the timeout stored is %s", effective["read_timeout"])
	}
}

// One bad value stops the whole form.
//
// A form saved in one go should either land or leave nothing half-written, or the
// operator is left guessing which half took — and the only way to know is to
// remember which fields they filled in.
func TestARefusedValueStopsTheWholeForm(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	path := "/modules/" + rf.module.ID.String() + "/settings/bulk"

	// A good value and a bad one, in the same request.
	body := `{"values":{"image_name_template":"{{project}}","read_timeout":"not a number"}}`
	recorder := rf.asAdmin(t, http.MethodPut, path, body)
	if recorder.Code == http.StatusOK {
		t.Fatal("a form with a bad value was accepted")
	}

	// Nothing was written — not even the good value, which went first in the map.
	effective, err := rf.store.Integrations().SettingsFor(t.Context(), rf.module.ID, nil, nil, rf.module.Capabilities.Settings)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if _, stored := effective["image_name_template"]; stored {
		t.Error("half the form was written")
	}
}

// A setting the module never announced is refused rather than silently ignored,
// because a stored setting nobody reads is a setting that looks like it works.
func TestSettingsTheModuleNeverDeclaredAreRefused(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	path := "/modules/" + rf.module.ID.String() + "/settings/bulk"

	recorder := rf.asAdmin(t, http.MethodPut, path, `{"values":{"upstream":"https://example.test"}}`)
	if recorder.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "upstream") {
		t.Errorf("the refusal does not name the setting: %s", recorder.Body.String())
	}

	if recorder := rf.asAdmin(t, http.MethodPut, path, `{"values":{}}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("an empty form: status %d, want 400", recorder.Code)
	}
}

// An operator who set a value they did not mean needs a way back that is not
// "remember what it was".
func TestASettingCanBePutBack(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	bulk := "/modules/" + rf.module.ID.String() + "/settings/bulk"
	if recorder := rf.asAdmin(t, http.MethodPut, bulk,
		`{"values":{"image_name_template":"{{project}}-test"}}`); recorder.Code != http.StatusOK {
		t.Fatalf("save: %s", recorder.Body.String())
	}

	reset := "/modules/" + rf.module.ID.String() + "/settings?key=image_name_template"
	if recorder := rf.asAdmin(t, http.MethodDelete, reset, ""); recorder.Code != http.StatusOK {
		t.Fatalf("reset: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	effective, _ := rf.store.Integrations().SettingsFor(t.Context(), rf.module.ID, nil, nil, rf.module.Capabilities.Settings)
	if _, stored := effective["image_name_template"]; stored {
		t.Error("the value survived the reset")
	}

	// Which means the module's own declared default applies again.
	detail := rf.asAdmin(t, http.MethodGet, "/modules/"+rf.module.ID.String(), "")
	if strings.Contains(detail.Body.String(), "-test") {
		t.Error("the page still shows the value that was reset")
	}

	// Resetting something already at its default is the state that was asked for,
	// not a failure: the button must not become an error for something the operator
	// has already done.
	if recorder := rf.asAdmin(t, http.MethodDelete, reset, ""); recorder.Code != http.StatusOK {
		t.Errorf("resetting a setting twice: status %d, want 200", recorder.Code)
	}
}

// Saving the same setting twice updates it rather than adding a row.
//
// The old uniqueness check listed scope_id, which is NULL for the instance scope,
// and NULLs are distinct in a PostgreSQL unique index: every save inserted another
// row instead of replacing the old one. Reading took the newest, so nothing looked
// wrong until something counted.
func TestSavingTwiceUpdatesRatherThanDuplicates(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	bulk := "/modules/" + rf.module.ID.String() + "/settings/bulk"
	for _, value := range []string{`"{{project}}"`, `"{{project}}-second"`, `"{{project}}-third"`} {
		if recorder := rf.asAdmin(t, http.MethodPut, bulk,
			`{"values":{"image_name_template":`+value+`}}`); recorder.Code != http.StatusOK {
			t.Fatalf("save %s: %s", value, recorder.Body.String())
		}
	}

	var rows int
	err := rf.store.Pool().QueryRow(t.Context(),
		`SELECT count(*) FROM integration_settings
		 WHERE integration_id = $1 AND scope_type = 'instance' AND scope_id IS NULL`,
		rf.module.ID).Scan(&rows)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		// One row for the one setting that was written. Three writes, one value.
		t.Errorf("%d rows for one setting written three times", rows)
	}

	effective, _ := rf.store.Integrations().SettingsFor(t.Context(), rf.module.ID, nil, nil, rf.module.Capabilities.Settings)
	var template string
	_ = json.Unmarshal(effective["image_name_template"], &template)
	if template != "{{project}}-third" {
		t.Errorf("the value in force is %q, want the last one written", template)
	}
}

// Only an administrator changes what a module is configured to do.
func TestSettingsNeedAnAdministrator(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	session := dbtest.NewSession(t, rf.store, rf.plain.ID)
	bulk := "/modules/" + rf.module.ID.String() + "/settings/bulk"

	if recorder := rf.as(t, session, http.MethodPut, bulk,
		`{"values":{"image_name_template":"{{project}}"}}`); recorder.Code == http.StatusOK {
		t.Error("an ordinary user changed a module setting")
	}
	if recorder := rf.as(t, session, http.MethodDelete,
		"/modules/"+rf.module.ID.String()+"/settings?key=image_name_template", ""); recorder.Code == http.StatusOK {
		t.Error("an ordinary user reset a module setting")
	}
}

// A module reading its own configuration gets the values, not a mask.
//
// This was a mask once, and the telegram module spent a while taking its bot token
// from the environment instead and treating the stored value as unusable. A module
// cannot work from a value it is not allowed to see, and there is no session on this
// path to protect: masking belongs where a browser is reading.
func TestAModuleIsGivenItsOwnSettingsUnmasked(t *testing.T) {
	stored := map[string]json.RawMessage{"bot_token": json.RawMessage(`"123456:ABC"`)}

	spec := models.SettingSpec{Key: "bot_token", Label: "Bot token", Type: "string", Secret: true}
	secret := map[string]bool{"bot_token": true}
	describe := func(key string) (models.SettingSpec, bool) { return spec, key == "bot_token" }

	if got, _ := unmaskedSettings(stored)["bot_token"].(string); got != "123456:ABC" {
		t.Errorf("the module was given %q, want the value it was configured with", got)
	}

	// The other path still masks, which is the point of having two.
	if got := redactSettings(stored, secret, describe)["bot_token"]; got != "********" {
		t.Errorf("the browser path returned %#v, want a mask", got)
	}
}
