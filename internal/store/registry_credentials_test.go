package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// A registry written down can be pushed to, and what it is pushed with is an answer
// several places are allowed to give: the instance first, then a group, then a project.
// These tests are about the order and about each scope changing only what it names,
// because that is the whole of what makes an override usable — an override that had to
// restate everything would be a second copy of the registry rather than a correction of
// the first.

func seedRegistry(t *testing.T, st *store.Store, login, password, source string) *store.DockerRegistry {
	t.Helper()
	ctx := context.Background()
	reg := &store.DockerRegistry{
		Name:             "written-" + uuid.NewString()[:8],
		URL:              "https://registry.example.test",
		Login:            login,
		Password:         password,
		CredentialSource: source,
		Enabled:          true,
	}
	if err := st.DockerRegistries().Create(ctx, reg); err != nil {
		t.Fatalf("create registry: %v", err)
	}
	t.Cleanup(func() {
		_ = st.DockerRegistries().Delete(context.Background(), reg.ID)
	})
	return reg
}

func TestARegistryPushesWithTheAccountOnItsOwnRow(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Source != store.CredentialSourceStatic {
		t.Errorf("source: got %q, want %q", resolved.Source, store.CredentialSourceStatic)
	}
	if resolved.Login != "instance-login" || resolved.Password != "instance-password" {
		t.Errorf("credential: got %q/%q, want the pair on the registry's own row",
			resolved.Login, resolved.Password)
	}
}

func TestAnEmptySourceMeansThePairOnTheRow(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	// A record written before the column existed, and one an administrator left blank:
	// both have already been pushing with the login beside them.
	reg := seedRegistry(t, st, "alice", "secret123", "")

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Source != store.CredentialSourceStatic {
		t.Errorf("a registry that was never told otherwise pushes as %q, want %q",
			resolved.Source, store.CredentialSourceStatic)
	}
}

func TestAGroupOverridesTheInstanceAndAProjectOverridesTheGroup(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)
	groupID, projectID := uuid.New(), uuid.New()

	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredential{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Login:      "group-login",
	}); err != nil {
		t.Fatalf("set group: %v", err)
	}
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredential{
		RegistryID:       reg.ID,
		ScopeType:        store.ScopeProject,
		ScopeID:          &projectID,
		CredentialSource: store.CredentialSourceUser,
		Login:            "project-login",
	}); err != nil {
		t.Fatalf("set project: %v", err)
	}

	// The project says two things and leaves a third alone, which is the case that
	// matters: it inherits the password rather than having to be handed it again.
	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, &projectID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Source != store.CredentialSourceUser {
		t.Errorf("source: got %q, want the project's %q", resolved.Source, store.CredentialSourceUser)
	}
	if resolved.Login != "project-login" {
		t.Errorf("login: got %q, want the project's", resolved.Login)
	}
	if resolved.Password != "instance-password" {
		t.Errorf("password: got %q, want the one inherited from the instance — a project "+
			"that sets a login must not have to restate the password", resolved.Password)
	}

	// The group alone, which is what a group pushes with for its other projects.
	forGroup, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, nil)
	if err != nil {
		t.Fatalf("resolve for the group: %v", err)
	}
	if forGroup.Source != store.CredentialSourceStatic {
		t.Errorf("source for the group: got %q, want the instance's %q",
			forGroup.Source, store.CredentialSourceStatic)
	}
	if forGroup.Login != "group-login" {
		t.Errorf("login for the group: got %q, want the group's", forGroup.Login)
	}
}

func TestAGroupOutsideTheRegistryDoesNotChangeIt(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)
	groupID, otherGroup := uuid.New(), uuid.New()
	projectID := uuid.New()

	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredential{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Login:      "group-login",
		Password:   "group-password",
	}); err != nil {
		t.Fatalf("set group: %v", err)
	}

	// A project in a group nobody has said anything about, and a project in no group at
	// all, must both still see what the instance said.
	for _, tc := range []struct {
		name      string
		groupID   *uuid.UUID
		projectID *uuid.UUID
	}{
		{"another group", &otherGroup, &projectID},
		{"no group", nil, &projectID},
	} {
		resolved, err := st.RegistryCredentials().Resolve(ctx, reg, tc.groupID, tc.projectID)
		if err != nil {
			t.Fatalf("resolve for %s: %v", tc.name, err)
		}
		if resolved.Login != "instance-login" || resolved.Password != "instance-password" {
			t.Errorf("%s: got %q/%q, want the instance's — a group must not speak for a "+
				"project outside it", tc.name, resolved.Login, resolved.Password)
		}
	}
}

func TestAReadOnlyRegistryIsNotSomewhereToPush(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "alice", "secret123", store.CredentialSourceStatic)
	reg.ReadOnly = true
	if err := st.DockerRegistries().Update(ctx, reg); err != nil {
		t.Fatalf("mark read-only: %v", err)
	}

	if _, err := st.RegistryCredentials().Resolve(ctx, reg, nil, nil); err == nil {
		t.Fatal("a registry that may only be pulled from was accepted as somewhere to push, " +
			"so the failure would surface at the push instead of where it was written down")
	}
}

func TestSettingTheSameScopeTwiceReplacesRatherThanFails(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "alice", "secret123", store.CredentialSourceStatic)
	groupID := uuid.New()

	for _, login := range []string{"first", "second"} {
		if err := st.RegistryCredentials().Set(ctx, store.RegistryCredential{
			RegistryID: reg.ID,
			ScopeType:  store.ScopeGroup,
			ScopeID:    &groupID,
			Login:      login,
		}); err != nil {
			t.Fatalf("set %q: %v", login, err)
		}
	}

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "second" {
		t.Errorf("login: got %q, want the one saved last — a form saved twice must not "+
			"need a page reload in between", resolved.Login)
	}
}
