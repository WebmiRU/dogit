package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
)

// What one scope has written down about a registry, and what a build for that scope would
// use, are two questions and the page needs both.
//
// A form given only the second would show an inherited login as though the group had set
// it. Saving that back turns an inheritance into a copy: it looks right, it works, and it
// silently stops following the thing it was inheriting from — until somebody changes the
// instance's login and finds one group quietly still on the old account.

func TestAScopeWithNothingWrittenIsEmptyRatherThanInherited(t *testing.T) {
	env := newRegistryEnv(t, true)

	_, body := env.mustCreate(t, map[string]any{
		"url": env.host(), "login": "instance-login", "password": "instance-password",
	})
	id := env.id(t, body)
	group, err := env.store.Groups().Create(t.Context(), dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatalf("create a group: %v", err)
	}

	code, answer := env.do(t, http.MethodGet,
		"/registry/docker/"+id.String()+"/credentials?scope=group&groupID="+group.ID.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("reading a scope: %d %s", code, answer)
	}

	var view struct {
		Written     bool `json:"written"`
		Login       string
		HasPassword bool `json:"has_password"`
		Resolved    struct {
			Login       string `json:"login"`
			HasPassword bool   `json:"has_password"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(answer), &view); err != nil {
		t.Fatalf("the answer is not a scope: %v (%s)", err, answer)
	}

	if view.Written {
		t.Error("a group that has written nothing says it has written something")
	}
	if view.Login != "" || view.HasPassword {
		t.Errorf("a group that has written nothing shows %q/%v as its own, which is the "+
			"inherited pair wearing this group's name", view.Login, view.HasPassword)
	}

	// And it still answers with what a build would use, because that is the other half of
	// the question and a form that cannot show it is a form nobody can check a change
	// against.
	if view.Resolved.Login != "instance-login" || !view.Resolved.HasPassword {
		t.Errorf("resolved as %q/%v, want the pair from the registry's own row",
			view.Resolved.Login, view.Resolved.HasPassword)
	}
}

// The point of a scoped credential: a group that sets a password and inherits the login.
func TestAScopeThatSetsOnlyAPasswordIsNotGivenTheLoginAsItsOwn(t *testing.T) {
	env := newRegistryEnv(t, true)

	_, body := env.mustCreate(t, map[string]any{
		"url": env.host(), "login": "instance-login", "password": "instance-password",
	})
	id := env.id(t, body)
	group, err := env.store.Groups().Create(t.Context(), dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatalf("create a group: %v", err)
	}

	code, answer := env.do(t, http.MethodPut,
		"/registry/docker/"+id.String()+"/credentials?scope=group&groupID="+group.ID.String(),
		map[string]any{"password": "group-password"})
	if code != http.StatusOK {
		t.Fatalf("writing the group's password: %d %s", code, answer)
	}

	code, answer = env.do(t, http.MethodGet,
		"/registry/docker/"+id.String()+"/credentials?scope=group&groupID="+group.ID.String(), nil)
	if code != http.StatusOK {
		t.Fatalf("reading the scope back: %d %s", code, answer)
	}

	var view struct {
		Written     bool   `json:"written"`
		Login       string `json:"login"`
		HasPassword bool   `json:"has_password"`
		Resolved    struct {
			Login       string `json:"login"`
			HasPassword bool   `json:"has_password"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(answer), &view); err != nil {
		t.Fatalf("the answer is not a scope: %v (%s)", err, answer)
	}

	if !view.Written {
		t.Error("a group that wrote a password says it wrote nothing")
	}
	if !view.HasPassword {
		t.Error("the group does not say it set a password, so a form could not show it")
	}
	if view.Login != "" {
		t.Errorf("the group shows login %q as its own; it set a password only, and a form "+
			"saving that back would freeze the inherited login into a copy", view.Login)
	}
	if view.Resolved.Login != "instance-login" || !view.Resolved.HasPassword {
		t.Errorf("resolved as %q/%v, want the instance's login with this group's password",
			view.Resolved.Login, view.Resolved.HasPassword)
	}

	// The password itself never comes back, at any scope: this endpoint is the one a
	// browser reads, and a password in it is a password in the page.
	if contains(answer, "group-password") || contains(answer, "instance-password") {
		t.Errorf("the answer to a read carried a password back: %s", answer)
	}
}

// A registry cannot be marked pull-only, because there is no such thing here any more.
//
// It is refused rather than ignored, and that is the whole point. A field accepted and
// quietly dropped leaves whoever ticked it believing a rule is in force — the list still
// shows a registry that may only be pulled from, somebody reads that and concludes the
// image in it is safe to depend on — and the only way anybody finds out is a build that
// pushes where they were told it must not.
func TestARegistryCannotBeMarkedPullOnly(t *testing.T) {
	env := newRegistryEnv(t, true)

	code, answer := env.do(t, http.MethodPost, "/registry/docker", map[string]any{
		"url": env.host(), "login": "robot", "password": "hunter2", "read_only": true,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("a registry was written down as pull-only with %d: %s", code, answer)
	}
	if !strings.Contains(answer, "read_only") {
		t.Errorf("the refusal does not name the field, so whoever sent it is left to guess: %s", answer)
	}
}

// And the one that was written down works in both directions, which is the other half of
// removing the flag: not that nobody can say "pull only" any more, but that every registry
// on the list is somewhere a build may push.
//
// A registry's scopes are still read one scope at a time and an unwritten one still comes
// back as unwritten, because those are facts about the credentials rather than about the
// registry.
func TestARegistryOnTheListIsSomewhereToPush(t *testing.T) {
	env := newRegistryEnv(t, true)

	_, body := env.mustCreate(t, map[string]any{
		"url": env.host(), "login": "robot", "password": "hunter2",
	})
	id := env.id(t, body)

	code, answer := env.do(t, http.MethodGet,
		"/registry/docker/"+id.String()+"/credentials", nil)
	if code != http.StatusOK {
		t.Fatalf("reading the instance scope of a registry: %d %s", code, answer)
	}

	var view struct {
		ResolvedError string `json:"resolved_error"`
		Resolved      struct {
			Login string `json:"login"`
		} `json:"resolved"`
	}
	if err := json.Unmarshal([]byte(answer), &view); err != nil {
		t.Fatalf("the answer is not a scope: %v (%s)", err, answer)
	}
	if view.ResolvedError != "" {
		t.Errorf("nothing on the list is refused a push any more, but this one is: %s",
			view.ResolvedError)
	}
	if view.Resolved.Login != "robot" {
		t.Errorf("resolved login %q, want the row's own — a registry a build may push to has "+
			"to come back with the login that lets it", view.Resolved.Login)
	}
}

// The read is an administrator's, like the write beside it.
func TestAScopedCredentialIsNotReadableByAnyoneElse(t *testing.T) {
	env := newRegistryEnv(t, false)

	// No registry is needed, and that is the point of asking for one that was never
	// written: the rights are checked before the record is looked up, so this answers
	// about the reader rather than about whether the id happens to exist. A check that
	// read the record first would pass a test like this one for the wrong reason.
	code, _ := env.do(t, http.MethodGet,
		"/registry/docker/"+uuid.NewString()+"/credentials", nil)
	if code != http.StatusForbidden {
		t.Errorf("a non-administrator reading a scoped credential got %d, want %d",
			code, http.StatusForbidden)
	}
}
