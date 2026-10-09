package main

import (
	"encoding/json"
	"testing"

	"github.com/ewolf/dogit/internal/modulechan"
)

// What this module tells the core about how long to keep a command for it.
//
// The setting exists in the core's cache and is read from here, by name, so the two ends can
// disagree without either of them noticing. These tests are the place that disagreement would
// show up: a module that renames the key keeps working perfectly and stops controlling anything.

// runnerSettings is what this module announces, read out of its own manifest.
func runnerSettings(t *testing.T) []map[string]any {
	t.Helper()

	cfg := config{}
	declared := manifest(cfg)["settings"]
	settings, ok := declared.([]map[string]any)
	if !ok {
		t.Fatalf("the manifest carries no settings, or they are not the list of keys it is "+
			"meant to be: %T", declared)
	}
	return settings
}

func TestTheRunnerDeclaresTheCommandLifetimeSetting(t *testing.T) {
	for _, spec := range runnerSettings(t) {
		if spec["key"] == modulechan.CommandTTLSetting {
			if spec["type"] != "int" {
				t.Errorf("the command lifetime is declared as %q, want int — the core reads a number",
					spec["type"])
			}
			if spec["label"] == "" || spec["description"] == "" {
				t.Error("the command lifetime is a field on a page with no label or no description")
			}
			return
		}
	}
	t.Errorf("the manifest never declares %q, so the core's cache cannot be told what to do",
		modulechan.CommandTTLSetting)
}

// No default, deliberately: an empty field and a field saying five minutes have to behave the
// same, and the empty one is what every runner registered before this setting existed has.
func TestTheCommandLifetimeIsDeclaredWithNoDefault(t *testing.T) {
	for _, spec := range runnerSettings(t) {
		if spec["key"] != modulechan.CommandTTLSetting {
			continue
		}
		// Absent is what it must be. An explicit null would also marshal to nothing and
		// behave identically, so it is not refused here — the reason to prefer absent is
		// that a key written out is a key somebody fills in later without reading why.
		if value, declared := spec["default"]; declared && value != nil {
			t.Errorf("the command lifetime has the default %v, want none: a field showing 300 and a "+
				"field left empty would look different while meaning the same", value)
		}
		return
	}
	t.Fatalf("the manifest never declares %q", modulechan.CommandTTLSetting)
}

// The key is a name in a JSON document the core reads, so its exact spelling is part of what this
// module promises rather than an implementation detail.
func TestTheCommandLifetimeKeyIsTheOneTheCoreReads(t *testing.T) {
	const expected = "command_ttl_seconds"
	if modulechan.CommandTTLSetting != expected {
		t.Errorf("the core reads the command lifetime under %q, and something has changed it to %q",
			expected, modulechan.CommandTTLSetting)
	}
	encoded, err := json.Marshal(modulechan.CommandTTLSetting)
	if err != nil {
		t.Fatalf("the key cannot be written out: %v", err)
	}
	if string(encoded) != `"`+expected+`"` {
		t.Errorf("the key encodes as %s, which the core's settings map will not find", encoded)
	}
}
