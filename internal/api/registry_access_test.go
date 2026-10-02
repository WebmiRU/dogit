package api

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
)

// registryFixture is a registered registry module and a project to push to.
type registryFixture struct {
	*moduleFixture
	project *models.Project
}

// setupRegistry registers a registry module and gives the plain user developer
// rights on one project.
func setupRegistry(t *testing.T, level int) *registryFixture {
	t.Helper()

	f := newModuleFixture(t)

	// Re-register as a registry, so the manifest matches the kind being tested.
	hash := sha256.Sum256([]byte(f.module.Kind + "-instance-token"))
	if _, err := f.store.Integrations().Register(t.Context(), f.module.Kind, f.module.Name,
		"http://registry:5000", hash[:], models.Manifest{
			Version: "0.1.0",
			Scopes: []string{
				models.ScopeRegistryPull, models.ScopeRegistryPush, models.ScopeRegistryDelete,
			},
			// Declared the way the registry module declares it: as a rule about the
			// value, with the reason in its own words.
			Settings: []models.SettingSpec{{
				Key:         "image_name_template",
				Label:       "Image name template",
				Type:        "string",
				Default:     "{{group}}/{{project}}",
				MustContain: []string{"{{project}}"},
				WhyContains: "an image name that does not contain its project cannot be traced back to one",
			}},
		}); err != nil {
		t.Fatalf("register the registry module: %v", err)
	}

	project := dbtest.NewProject(t, f.store, "registrydemo", nil)
	if level > 0 {
		dbtest.GrantRole(t, f.store, project.ID, f.plain.ID, level, "tester")
	}

	return &registryFixture{moduleFixture: f, project: project}
}

// asModule calls a module endpoint with the module's own credential.
func (rf *registryFixture) asModule(t *testing.T, moduleToken, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+moduleToken)

	recorder := httptest.NewRecorder()
	rf.server.Routes().ServeHTTP(recorder, request)
	return recorder
}

func pathModule(rf *registryFixture, suffix string) string {
	return suffix
}

// ask is the module asking the core about a caller.
func (rf *registryFixture) ask(t *testing.T, token, project, action string) models.RegistryAccess {
	t.Helper()

	body, _ := json.Marshal(map[string]string{
		"token": token, "project": project, "action": action,
	})

	request := httptest.NewRequest(http.MethodPost, "/module/registry/access", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	// The module authenticates as itself; the caller it asks about is in the body.
	request.Header.Set("Authorization", "Bearer "+rf.moduleToken)

	recorder := httptest.NewRecorder()
	rf.server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("ask: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer models.RegistryAccess
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return answer
}

// A registry takes its permissions from the project's, because there is nowhere
// else for them to come from.
func TestRegistryAccessFollowsProjectRights(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)
	mintToken(t, rf.store, rf.module, rf.plain, "developer-token")

	if answer := rf.ask(t, "developer-token", rf.project.Path, "push"); !answer.Allowed {
		t.Fatalf("a developer cannot push to their project: %+v", answer)
	}
	if answer := rf.ask(t, "developer-token", rf.project.Path, "pull"); !answer.Allowed {
		t.Errorf("a developer cannot pull: %+v", answer)
	}
	// Deleting images is managing, not writing.
	if answer := rf.ask(t, "developer-token", rf.project.Path, "delete"); answer.Allowed {
		t.Error("a developer may delete images")
	} else if answer.Minimum == 0 {
		t.Errorf("the refusal does not say what would be needed: %+v", answer)
	}

	// A maintainer may delete.
	dbtest.GrantRole(t, rf.store, rf.project.ID, rf.plain.ID, models.AccessLevelMaintainer, "keeper")
	if answer := rf.ask(t, "developer-token", rf.project.Path, "delete"); !answer.Allowed {
		t.Errorf("a maintainer may not delete images: %+v", answer)
	}
}

// Somebody with nothing on the project has nothing on its images either.
func TestRegistryAccessWithoutMembershipIsRefused(t *testing.T) {
	rf := setupRegistry(t, 0)
	mintToken(t, rf.store, rf.module, rf.plain, "stranger-token")

	answer := rf.ask(t, "stranger-token", rf.project.Path, "pull")
	if answer.Allowed {
		t.Fatalf("a stranger pulled from a project: %+v", answer)
	}
	// The answer says the project does not exist rather than that access was
	// refused: only somebody who could see the project should learn that it is
	// there at all.
	if answer.Reason != "no_such_project" {
		t.Errorf("reason = %q, want no_such_project", answer.Reason)
	}
}

// One project cannot push into another's images. This is the boundary the image
// name exists to hold, so it is checked directly.
func TestRegistryCannotCrossProjects(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)
	other := dbtest.NewProject(t, rf.store, "somebodyelse", nil)

	mintToken(t, rf.store, rf.module, rf.plain, "one-project-token")

	if answer := rf.ask(t, "one-project-token", rf.project.Path, "push"); !answer.Allowed {
		t.Fatalf("cannot push to its own project: %+v", answer)
	}

	answer := rf.ask(t, "one-project-token", other.Path, "push")
	if answer.Allowed {
		t.Fatal("a project pushed into another project's images")
	}
	// Nor may it learn that the other project exists: the registry is otherwise a
	// way to enumerate every project on the instance.
	if answer.Reason != "no_such_project" {
		t.Errorf("reason = %q, want no_such_project", answer.Reason)
	}
}

// Anything the core cannot vouch for is refused, and the refusal is not an error:
// the module has to be able to tell "no" from "broken".
func TestRegistryAccessNeedsARealToken(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)

	for _, token := range []string{"", "no-such-token"} {
		answer := rf.ask(t, token, rf.project.Path, "push")
		if answer.Allowed {
			t.Errorf("token %q was let in: %+v", token, answer)
		}
	}
}

// The action is part of the question, and a nonsense one is a mistake worth
// reporting rather than defaulting to something plausible.
func TestRegistryAccessChecksTheAction(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)
	mintToken(t, rf.store, rf.module, rf.plain, "action-token")

	body, _ := json.Marshal(map[string]string{
		"token": "action-token", "project": rf.project.Path, "action": "detonate",
	})
	request := httptest.NewRequest(http.MethodPost, "/module/registry/access", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+rf.moduleToken)

	recorder := httptest.NewRecorder()
	rf.server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Errorf("an unknown action: status %d, want 400", recorder.Code)
	}
}

// Forbidding a module closes this door at the door itself.
//
// The module is refused before it can ask, rather than being allowed to ask and
// being told nothing: a module nobody trusts should not be able to keep using the
// core's knowledge of its callers at all.
func TestRegistryAccessStopsWhenTheModuleIsForbidden(t *testing.T) {
	rf := setupRegistry(t, models.AccessLevelDeveloper)
	mintToken(t, rf.store, rf.module, rf.plain, "forbidden-token")

	if answer := rf.ask(t, "forbidden-token", rf.project.Path, "push"); !answer.Allowed {
		t.Fatal("the module cannot push even while allowed")
	}

	path := "/modules/" + rf.module.ID.String() + "/state"
	if recorder := rf.asAdmin(t, http.MethodPut, path, `{"enabled":false}`); recorder.Code != http.StatusOK {
		t.Fatalf("forbid: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	body, _ := json.Marshal(map[string]string{
		"token": "forbidden-token", "project": rf.project.Path, "action": "push",
	})
	request := httptest.NewRequest(http.MethodPost, "/module/registry/access", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+rf.moduleToken)

	recorder := httptest.NewRecorder()
	rf.server.Routes().ServeHTTP(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatalf("a forbidden module was answered: %s", recorder.Body.String())
	}
}
