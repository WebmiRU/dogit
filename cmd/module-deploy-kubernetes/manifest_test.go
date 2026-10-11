package main

import (
	"slices"
	"testing"
)

// A place does not name a registry, and this is the test that says so.
//
// The registry moved up to the group or the project: one level, one address, so that "where
// does this deploy from" has as many answers as there are levels rather than as many as
// there are places. That left the field in two states at once — a form still offering it,
// and a resolver still reading it — which is the worst of both. A form that offers a second
// answer invites somebody to write it, and a value written there is one a deployment will
// not use: the level was lifted, and the level is what answers.
//
// So the field goes out of the form here, and the resolver keeps it only as the answer for a
// level that never chose. That path is the one reader it still has, and dropping it before
// the levels it was written for have chosen would stop them deploying at all.

// clusterFieldKeys are the field keys one place's row carries, in the order the form draws
// them.
func clusterFieldKeys(t *testing.T) []string {
	t.Helper()
	for _, spec := range manifest()["settings"].([]map[string]any) {
		if spec["key"] != "clusters" {
			continue
		}
		items := spec["items"].(map[string]any)
		keys := make([]string, 0, len(items["fields"].([]map[string]any)))
		for _, field := range items["fields"].([]map[string]any) {
			keys = append(keys, field["key"].(string))
		}
		return keys
	}
	t.Fatal("the manifest does not declare a clusters list")
	return nil
}

func TestAPlaceDoesNotNameARegistry(t *testing.T) {
	if keys := clusterFieldKeys(t); slices.Contains(keys, "registry") {
		t.Errorf("a place's row still carries %q: %v", "registry", keys)
	}
}

// The field that went away, and what replaced it, in one place. Removing one without the
// other would leave a module with no way to say where its images live at all, which is the
// opposite of what the removal was for.
func TestTheRegistryIsNamedByTheLevelAndNotByItsPlaces(t *testing.T) {
	for _, key := range []string{"registry_source", "image_registry"} {
		found := false
		for _, spec := range manifest()["settings"].([]map[string]any) {
			if spec["key"] == key {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("the manifest does not declare %q, so nothing names a registry any more", key)
		}
	}
}

// The address is drawn only for the choice that needs one, and hiding it is not the same as
// dropping it: a level on "the group" still deploys, from whatever the group chose.
func TestTheAddressIsShownOnlyForALevelThatHasChosen(t *testing.T) {
	for _, spec := range manifest()["settings"].([]map[string]any) {
		if spec["key"] != "image_registry" {
			continue
		}
		condition, ok := spec["show_if"].(map[string]any)
		if !ok {
			t.Fatalf("image_registry is drawn always, so a level taking the group's registry "+
				"is also offered an address it does not use")
		}
		if condition["key"] != "registry_source" {
			t.Errorf("image_registry waits on %q, want registry_source", condition["key"])
		}
		return
	}
	t.Fatal("the manifest does not declare image_registry")
}