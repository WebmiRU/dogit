package api

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/models"
)

// The check that refuses a module which has not been given what it says it needs.
//
// It is one boolean on a setting, read out of a manifest a module author wrote, and the whole
// of it depends on that boolean arriving. If it does not, the check is empty and every module
// registers — which is the state this whole thing exists to prevent, and it would look like
// the feature simply not being switched on.
func TestRequiredSurvivesAManifestWrittenByHand(t *testing.T) {
	var caps models.Manifest
	raw := `{"version":"0.1.0","settings":[
		{"key":"database_url","type":"text","label":"Database","secret":true,"required":true},
		{"key":"registry","type":"registry","label":"Default registry"}
	]}`
	if err := json.Unmarshal([]byte(raw), &caps); err != nil {
		t.Fatalf("the manifest a module writes did not parse: %v", err)
	}
	if len(caps.Settings) != 2 {
		t.Fatalf("wanted two settings, read %d", len(caps.Settings))
	}
	if !caps.Settings[0].Required {
		t.Fatal("required did not survive: the check would be empty and every module would register")
	}
	if caps.Settings[1].Required {
		t.Fatal("a setting nobody marked came back required, so a module would be refused for it")
	}
	if !caps.Settings[0].Secret {
		t.Fatal("secret did not survive, so a database connection string would come back readable")
	}
}

// What counts as having a value.
//
// The three cases that matter, and the third is the one that would let a module register with
// a setting nobody filled in: an explicit null and an empty string are the shapes a cleared
// field takes on the way here, and both are "nothing", not a value.
func TestWhatCountsAsHavingAValue(t *testing.T) {
	spec := models.SettingSpec{Key: "database_url"}

	// The absent case is the one that got this wrong once: a key missing from the map gives
	// a nil byte slice inside a non-nil interface, which is neither nil nor empty until
	// somebody asks about its length.
	var absent json.RawMessage
	if settingHasValue(absent, spec) {
		t.Fatal("an absent setting was read as a value")
	}
	if settingHasValue(json.RawMessage(``), spec) {
		t.Fatal("an empty body was read as a value")
	}
	if settingHasValue(json.RawMessage(`""`), spec) {
		t.Fatal("an empty string was read as a value")
	}
	if settingHasValue(json.RawMessage(`"   "`), spec) {
		t.Fatal("whitespace was read as a value")
	}
	if settingHasValue(json.RawMessage(`null`), spec) {
		t.Fatal("an explicit null was read as a value")
	}
	if !settingHasValue(json.RawMessage(`"host=db user=x"`), spec) {
		t.Fatal("a real value was read as nothing")
	}

	// A setting that ships with the right value has it, so asking again would be wrong.
	withDefault := models.SettingSpec{Key: "concurrency", Default: 1}
	if !settingHasValue(json.RawMessage(nil), withDefault) {
		t.Fatal("a setting with a default was read as missing, and a module would be refused for " +
			"something it already has")
	}
}
