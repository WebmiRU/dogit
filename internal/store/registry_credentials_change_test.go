package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
)

// Setting one half of a scope's answer must not throw away the other half.
//
// A scope's row is a set of overrides, not a copy of the credential: a group can set a
// password and inherit the login, or set a login and inherit the password, and both are
// arrangements people want. So a form that sets the login afterwards is not replacing the
// group's answer, it is completing it.
//
// It was replacing it. The row was deleted and rewritten from whatever the request
// happened to carry, so setting a login arrived with an empty password and wrote an empty
// password — and the group went from pushing with an account of its own to pushing with
// whatever it inherited, without anything having said so. Found by typing a login into
// the page for a group that already had a password, and watching the password go.
func TestSettingOneFieldKeepsTheOthersAtThatScope(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)
	groupID := uuid.New()

	first := "group-password"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Password:   &first,
	}); err != nil {
		t.Fatalf("set the group's password: %v", err)
	}

	// Now the login, on its own. Nothing asked about the password.
	second := "group-login"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Login:      &second,
	}); err != nil {
		t.Fatalf("set the group's login: %v", err)
	}

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "group-login" {
		t.Errorf("login: got %q, want the one this scope set", resolved.Login)
	}
	if resolved.Password != "group-password" {
		t.Errorf("password: got %q, want the one this scope set before. Setting a login "+
			"said nothing about the password, and a password that changes because a login "+
			"was typed is a password nobody chose.", resolved.Password)
	}
}

// And the other order, because a merge that only works one way round is half a merge.
func TestSettingAPasswordKeepsTheLoginSetBeforeIt(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)
	projectID := uuid.New()

	login := "project-login"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeProject,
		ScopeID:    &projectID,
		Login:      &login,
	}); err != nil {
		t.Fatalf("set the project's login: %v", err)
	}

	password := "project-password"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeProject,
		ScopeID:    &projectID,
		Password:   &password,
	}); err != nil {
		t.Fatalf("set the project's password: %v", err)
	}

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, nil, &projectID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "project-login" || resolved.Password != "project-password" {
		t.Errorf("resolved as %q/%q, want the pair this scope set", resolved.Login, resolved.Password)
	}
}

// A merge must still be able to say "inherit this one from above". An empty string is not
// the same as not mentioning it, and if it were treated as one, there would be no way
// back to inheriting once a scope had said otherwise.
func TestAnEmptyValueStillMeansInheritRatherThanKeep(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)
	groupID := uuid.New()

	own := "group-login"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Login:      &own,
	}); err != nil {
		t.Fatalf("set the group's login: %v", err)
	}

	empty := ""
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeGroup,
		ScopeID:    &groupID,
		Login:      &empty,
	}); err != nil {
		t.Fatalf("clear the group's login: %v", err)
	}

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "instance-login" {
		t.Errorf("login: got %q, want the instance's, because this scope said to inherit it",
			resolved.Login)
	}
}

// The instance scope is matched on its null scope_id, which the unique constraint cannot
// see. Worth saying out loud, because the merge above reads the existing row first and a
// lookup that missed the null would merge into nothing and lose every override the
// instance had been carrying.
func TestTheInstanceScopeSurvivesAChangeOnTheSameScope(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "instance-login", "instance-password", store.CredentialSourceStatic)

	first := "instance-own-login"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeInstance,
		Login:      &first,
	}); err != nil {
		t.Fatalf("set the instance login: %v", err)
	}

	second := "instance-own-password"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  store.ScopeInstance,
		Password:   &second,
	}); err != nil {
		t.Fatalf("set the instance password: %v", err)
	}

	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, nil, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "instance-own-login" || resolved.Password != "instance-own-password" {
		t.Errorf("resolved as %q/%q, want the pair the instance scope set",
			resolved.Login, resolved.Password)
	}
}

// Taking a scope's answer away should leave no answer, rather than a row that says nothing.
//
// A row with all three fields empty resolves to exactly what no row resolves to — it
// inherits everything — so it decides nothing while still reading as a scope that has
// written something down. The button that takes a scope's answer away would leave it
// there for ever, pressing itself again and going nowhere.
func TestTakingAScopesAnswerAwayLeavesNothingBehind(t *testing.T) {
	st := dbtest.Open(t)
	ctx := context.Background()

	reg := seedRegistry(t, st, "alice", "secret123", store.CredentialSourceStatic)
	groupID := uuid.New()

	login := "group-login"
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID: reg.ID, ScopeType: store.ScopeGroup, ScopeID: &groupID, Login: &login,
	}); err != nil {
		t.Fatalf("set the group's login: %v", err)
	}

	// What the page's "forget this scope" sends: every field, named, and empty.
	empty := ""
	if err := st.RegistryCredentials().Set(ctx, store.RegistryCredentialChange{
		RegistryID:       reg.ID,
		ScopeType:        store.ScopeGroup,
		ScopeID:          &groupID,
		CredentialSource: &empty,
		Login:            &empty,
		Password:         &empty,
	}); err != nil {
		t.Fatalf("take the scope's answer away: %v", err)
	}

	_, written, err := st.RegistryCredentials().Scoped(ctx, reg.ID, store.ScopeGroup, &groupID)
	if err != nil {
		t.Fatalf("read the scope back: %v", err)
	}
	if written {
		t.Error("the scope still reads as having written something, over an answer that " +
			"says nothing and inherits everything")
	}

	// And what a build uses is unchanged by having said nothing rather than nothing.
	resolved, err := st.RegistryCredentials().Resolve(ctx, reg, &groupID, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.Login != "alice" || resolved.Password != "secret123" {
		t.Errorf("resolved as %q/%q, want what it inherited", resolved.Login, resolved.Password)
	}
}
