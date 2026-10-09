package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"

	"github.com/ewolf/dogit/internal/models"
)

// The name an image is stored under is what the permission check is made
// against, so it is rendered in exactly one place and used for both deciding and
// checking.
func TestImageNameFor(t *testing.T) {
	cases := []struct {
		template string
		path     string
		branch   string
		want     string
	}{
		{"{{group}}/{{project}}", "grp1/prj1", "", "grp1/prj1"},
		{"{{project}}", "grp1/prj1", "", "prj1"},
		{"{{project}}-{{branch}}", "grp1/prj1", "dev", "prj1-dev"},
		{"{{project}}", "prj1", "", "prj1"},
		// A project with no group has no group to put in the name, and the name must
		// not start with a slash: the registry would read that as an empty group.
		{"{{group}}/{{project}}", "prj1", "", "prj1"},
		{"registry/{{group}}/{{project}}", "grp1/prj1", "", "registry/grp1/prj1"},
		{"{{path}}", "grp1/prj1", "", "grp1/prj1"},
	}

	for _, tc := range cases {
		got := imageNameFor(tc.template, tc.path, tc.branch)
		if got != tc.want {
			t.Errorf("%q on %q gave %q, want %q", tc.template, tc.path, got, tc.want)
		}
	}
}

// A name that does not name the project cannot be traced back to one, and two
// projects could then write to the same name. The template is rejected for that,
// wherever it is set.
func TestImageNameTemplateMustNameTheProject(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	settingsPath := "/modules/" + rf.module.ID.String() + "/settings?key=image_name_template"

	// The default, and the variants that keep the project in the name, are fine.
	for _, template := range []string{"{{group}}/{{project}}", "{{project}}", "registry/{{project}}"} {
		if recorder := rf.asAdmin(t, http.MethodPut, settingsPath,
			`{"value":`+mustJSON(template)+`}`); recorder.Code != http.StatusOK {
			t.Errorf("template %q was rejected: %s", template, recorder.Body.String())
		}
	}

	// One without it is not, and the refusal says what is missing.
	recorder := rf.asAdmin(t, http.MethodPut, settingsPath, `{"value":"{{group}}"}`)
	if recorder.Code == http.StatusOK {
		t.Fatal("a template that does not name the project was accepted")
	}
	if !strings.Contains(recorder.Body.String(), "{{project}}") {
		t.Errorf("the refusal does not say what is missing: %s", recorder.Body.String())
	}
}

// The core resolves an image name to the project that owns it, because it holds
// both the projects and the naming rule.
func TestRegistryResolvesAnImageToItsProject(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	// Default template: the project's full path is the image name.
	body := `{"image":` + mustJSON(rf.project.Path) + `}`
	if answer := rf.resolve(t, body); answer["project"] != rf.project.Path {
		t.Errorf("the core answered %v for %q", answer["project"], rf.project.Path)
	}

	// A name produced by no rule of ours belongs to something else, and must not be
	// attributed to whichever project fits.
	if answer := rf.resolve(t, `{"image":"someone/else"}`); answer["project"] != nil {
		t.Errorf("an unknown name was attributed to %v", answer["project"])
	}

	// With a narrower template, a project's images live somewhere else — and the
	// difference only exists for a project inside a group. Outside one, "{{group}}"
	// is empty and both templates produce the same name, which is correct rather
	// than a coincidence: there is no group to tell them apart.
	grouped, err := rf.store.Groups().Create(t.Context(), dbtest.Unique("reggrp"), "Registry group")
	if err != nil {
		t.Fatalf("create a group: %v", err)
	}
	inside := dbtest.NewProject(t, rf.store, "inside", &grouped.ID)
	// A project's path is its whole address, so a project inside a group is stored
	// as "group/project" — which is what the image name is built from.
	if err := rf.store.Projects().Rename(t.Context(), inside.ID, grouped.FullPath+"/"+inside.Path); err != nil {
		t.Fatalf("move the project into the group: %v", err)
	}
	inside.Path = grouped.FullPath + "/" + inside.Path
	setTemplate(t, rf, "{{project}}")

	_, name := splitPath(inside.Path)
	if answer := rf.resolve(t, `{"image":`+mustJSON(name)+`}`); answer["project"] != inside.Path {
		t.Errorf("under {{project}} the name %q resolved to %v, want %s",
			name, answer["project"], inside.Path)
	}
	// The old name is now nobody's. Narrowing the rule leaves the images pushed
	// under the old one unattributable, and saying so is better than attributing
	// them to whichever project fits.
	if answer := rf.resolve(t, `{"image":`+mustJSON(inside.Path)+`}`); answer["project"] != nil {
		t.Errorf("the old name still resolves to %v", answer["project"])
	}
}

// An empty or missing name is a mistake worth reporting.
func TestRegistryResolveNeedsAName(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	for _, body := range []string{`{"image":""}`, `{"image":"   "}`, `{}`} {
		if recorder := rf.asModule(t, rf.moduleToken, pathModule(rf, "/module/registry/resolve"), body); recorder.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, recorder.Code)
		}
	}
}

func (rf *registryFixture) resolve(t *testing.T, body string) map[string]any {
	t.Helper()

	recorder := rf.asModule(t, rf.moduleToken, pathModule(rf, "/module/registry/resolve"), body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("resolve: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return answer
}

func setTemplate(t *testing.T, rf *registryFixture, template string) {
	t.Helper()

	settingsPath := "/modules/" + rf.module.ID.String() + "/settings?key=image_name_template"
	if recorder := rf.asAdmin(t, http.MethodPut, settingsPath, `{"value":`+mustJSON(template)+`}`); recorder.Code != http.StatusOK {
		t.Fatalf("set the template: %s", recorder.Body.String())
	}
}

func mustJSON(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return `""`
	}
	return string(encoded)
}

// splitPath separates a group from a project, as the image name template does.
func splitPath(projectPath string) (string, string) {
	index := strings.Index(projectPath, "/")
	if index == -1 {
		return "", projectPath
	}
	return projectPath[:index], projectPath[index+1:]
}
