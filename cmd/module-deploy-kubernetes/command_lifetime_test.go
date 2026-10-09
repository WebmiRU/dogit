package main

import (
	"testing"

	"github.com/ewolf/dogit/internal/modulechan"
)

// What this module tells the core about how long to keep a command for it.
//
// This is the module that actually gets commands — a deploy that arrives is a deploy that
// happens — so its page is where an administrator has to be able to find the setting. And the
// core reads the key by name out of this manifest, so the two ends can disagree without either
// of them noticing.

func TestThisModuleDeclaresTheCommandLifetimeSetting(t *testing.T) {
	settings, ok := manifest()["settings"].([]map[string]any)
	if !ok {
		t.Fatal("the manifest carries no settings")
	}

	for _, spec := range settings {
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
	t.Errorf("the manifest never declares %q, so an administrator cannot find it and the "+
		"core's cache cannot be told what to do", modulechan.CommandTTLSetting)
}

// No default, for the reason in the runner's own manifest: an empty field and a field saying
// five minutes must behave the same, and the empty one is what every module registered before
// this setting existed already has.
func TestTheCommandLifetimeIsDeclaredWithNoDefault(t *testing.T) {
	settings, ok := manifest()["settings"].([]map[string]any)
	if !ok {
		t.Fatal("the manifest carries no settings")
	}

	for _, spec := range settings {
		if spec["key"] != modulechan.CommandTTLSetting {
			continue
		}
		// Absent rather than nil: both marshal to nothing and behave the same, so nil is
		// not refused — but an absent key is not a thing to fill in, and a nil one is.
		if value, declared := spec["default"]; declared && value != nil {
			t.Errorf("the command lifetime has the default %v, want none", value)
		}
		return
	}
	t.Fatalf("the manifest never declares %q", modulechan.CommandTTLSetting)
}

// Declaring a setting the core refuses would be a field on a page that cannot be saved, and the
// save is what tells an operator it went wrong.
func TestTheCommandLifetimeIsNotRequired(t *testing.T) {
	settings, ok := manifest()["settings"].([]map[string]any)
	if !ok {
		t.Fatal("the manifest carries no settings")
	}

	for _, spec := range settings {
		if spec["key"] == modulechan.CommandTTLSetting && spec["required"] == true {
			t.Error("the command lifetime is required, so a module registered before this setting " +
				"existed could not start — and this module must keep deploying while it does")
		}
	}
}
