// The tests live in an external test package so they can use dbtest, which
// imports store itself.
package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// Who gets told where, now that a module can be pointed at more than one place.
//
// Everything here is about one thing: a recipient defined at the instance is
// inherited downwards, changed or added at each level, and which of the two happened
// is decided by the row it points at. A system that guesses here either mutes a
// channel somebody relies on or posts to two chats at once, and both are found out
// on the day something breaks.

// targetFixture is one module, two groups and two projects.
//
// The second of each is not decoration: "this project" and "everybody else" is the
// difference between a setting that applies and one that was set by accident.
type targetFixture struct {
	store  *store.Store
	module uuid.UUID
	group  uuid.UUID
	group2 uuid.UUID
	proj   uuid.UUID
	proj2  uuid.UUID
}

func setupTargets(t *testing.T) *targetFixture {
	t.Helper()

	st := dbtest.Open(t)
	group := dbtest.NewGroup(t, st, "targets")

	// A module of its own per test: registration is idempotent on (kind, name), so
	// reusing one name would hand every test the recipients the last one left behind.
	module, err := st.Integrations().Register(t.Context(), "notify:telegram",
		dbtest.Unique("telegram"), "http://module-notify:8093", []byte("hash"), models.Manifest{})
	if err != nil {
		t.Fatalf("register the module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(),
			`DELETE FROM integrations WHERE id = $1`, module.ID)
	})

	return &targetFixture{
		store:  st,
		module: module.ID,
		group:  group.ID,
		group2: uuid.New(),
		proj:   dbtest.NewProject(t, st, "in-group", &group.ID).ID,
		proj2:  dbtest.NewProject(t, st, "standalone", nil).ID,
	}
}

// row adds one recipient and returns it.
func (f *targetFixture) row(t *testing.T, scopeType string, scopeID *uuid.UUID, label string,
	enabled *bool, overrides *uuid.UUID, values map[string]any) *store.ModuleTarget {
	t.Helper()

	raw := map[string]json.RawMessage{}
	for key, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode %s: %v", key, err)
		}
		raw[key] = encoded
	}

	target, err := f.store.ModuleTargets().Create(t.Context(), &store.ModuleTarget{
		IntegrationID: f.module,
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Label:         label,
		Enabled:       enabled,
		Overrides:     overrides,
		Values:        raw,
	})
	if err != nil {
		t.Fatalf("create recipient: %v", err)
	}
	return target
}

// effective is what a project actually gets.
func (f *targetFixture) effective(t *testing.T, groupID *uuid.UUID, projectID uuid.UUID) []store.EffectiveTarget {
	t.Helper()
	return f.resolved(t, groupID, projectID).Targets
}

func (f *targetFixture) resolved(t *testing.T, groupID *uuid.UUID, projectID uuid.UUID) store.TargetResolution {
	t.Helper()

	resolved, err := f.store.ModuleTargets().Effective(t.Context(), f.module, groupID, &projectID)
	if err != nil {
		t.Fatalf("resolve recipients: %v", err)
	}
	return resolved
}

func chats(targets []store.EffectiveTarget) map[string]bool {
	out := map[string]bool{}
	for _, target := range targets {
		var chat string
		if err := json.Unmarshal(target.Values["chat_id"], &chat); err == nil {
			out[chat] = true
		}
	}
	return out
}

func on() *bool  { v := true; return &v }
func off() *bool { v := false; return &v }

// A project gets what the instance configured, without repeating any of it.
func TestARecipientIsInheritedWithoutBeingRepeated(t *testing.T) {
	f := setupTargets(t)
	chat := f.row(t, store.ScopeInstance, nil, "everyone", nil, nil, map[string]any{"chat_id": "-100a"})

	inherited := f.effective(t, &f.group, f.proj)
	if len(inherited) != 1 {
		t.Fatalf("the project got %d recipients, want 1", len(inherited))
	}
	if inherited[0].Own.ID != chat.ID {
		t.Error("the project received a recipient of its own instead of the instance's")
	}
	if !chats(inherited)["-100a"] {
		t.Errorf("the project gets %v, want the instance's chat", chats(inherited))
	}
	if inherited[0].DefinedAt != store.ScopeInstance {
		t.Errorf("the recipient is defined at %q, want the instance", inherited[0].DefinedAt)
	}
}

// Setting the address again at the project level is a change of that one value: what
// nobody touched still comes from above.
func TestOverridingOneValueLeavesTheRestInherited(t *testing.T) {
	f := setupTargets(t)
	base := f.row(t, store.ScopeInstance, nil, "everyone", nil, nil,
		map[string]any{"chat_id": "-100a", "bot_token": "secret"})
	f.row(t, store.ScopeProject, &f.proj, "", nil, &base.ID, map[string]any{"chat_id": "-100own"})

	got := f.effective(t, &f.group, f.proj)
	if len(got) != 1 {
		t.Fatalf("an override became a second recipient: %d rows", len(got))
	}
	if !chats(got)["-100own"] {
		t.Errorf("the chat id is %v, want the project's own", chats(got))
	}

	var token string
	if err := json.Unmarshal(got[0].Values["bot_token"], &token); err != nil || token != "secret" {
		t.Errorf("the token is %v, want the one from above", got[0].Values["bot_token"])
	}
	if !got[0].SetHere["chat_id"] || got[0].SetHere["bot_token"] {
		t.Errorf("the row claims to set %v, want only the chat id", got[0].SetHere)
	}
	if got[0].DefinedAt != store.ScopeProject {
		t.Errorf("the recipient is defined at %q, want the project", got[0].DefinedAt)
	}
}

// A different address at the project level is a second chat, not a change of the
// first. This is the case one chat per module cannot express.
func TestASecondChatIsASecondRecipient(t *testing.T) {
	f := setupTargets(t)
	f.row(t, store.ScopeInstance, nil, "everyone", nil, nil, map[string]any{"chat_id": "-100a"})
	f.row(t, store.ScopeProject, &f.proj, "deploys", nil, nil, map[string]any{"chat_id": "-100b"})

	got := f.effective(t, &f.group, f.proj)
	if len(got) != 2 {
		t.Fatalf("the project gets %d recipients, want 2", len(got))
	}
	if !chats(got)["-100a"] || !chats(got)["-100b"] {
		t.Errorf("the project gets %v, want both chats", chats(got))
	}
}

// A project that switches off an inherited recipient keeps its settings but stops
// getting the messages, which is how a repository stays out of the shared chat.
func TestSwitchingOffAtTheProjectOverridesTheInstance(t *testing.T) {
	f := setupTargets(t)
	base := f.row(t, store.ScopeInstance, nil, "the dump", on(), nil, map[string]any{"chat_id": "-100a"})
	f.row(t, store.ScopeProject, &f.proj, "", off(), &base.ID, nil)

	got := f.effective(t, &f.group, f.proj)
	if len(got) != 1 {
		t.Fatalf("the project got %d recipients, want the one it switched off", len(got))
	}
	if got[0].Enabled {
		t.Error("the recipient is on although the project switched it off")
	}

	// The switch belongs to the project that made it.
	other := f.effective(t, nil, f.proj2)
	if len(other) != 1 || !other[0].Enabled {
		t.Errorf("a repository nobody touched got %v", other)
	}
}

// A group switching something off applies to everything in it and to nothing else.
func TestTheGroupSwitchesWhatTheInstanceSet(t *testing.T) {
	f := setupTargets(t)
	base := f.row(t, store.ScopeInstance, nil, "the dump", on(), nil, map[string]any{"chat_id": "-100a"})
	f.row(t, store.ScopeGroup, &f.group, "", off(), &base.ID, nil)

	grouped := f.effective(t, &f.group, f.proj)
	if len(grouped) != 1 {
		t.Fatalf("the group got %d recipients, want 1", len(grouped))
	}
	if grouped[0].Enabled {
		t.Error("the group switched it off and the project still gets it")
	}
	if grouped[0].DefinedAt != store.ScopeGroup {
		t.Errorf("the recipient is defined at %q, want the group", grouped[0].DefinedAt)
	}

	elsewhere := f.effective(t, &f.group2, f.proj2)
	if len(elsewhere) != 1 || !elsewhere[0].Enabled {
		t.Errorf("the group's switch reached a project outside the group: %v", elsewhere)
	}
}

// A group that only sets an address has not switched anything off. Getting this
// wrong mutes a channel the instance meant to have on, and it looks like a typo.
func TestAPartialOverrideDoesNotSwitchAnythingOff(t *testing.T) {
	f := setupTargets(t)
	base := f.row(t, store.ScopeInstance, nil, "on by default", on(), nil, map[string]any{"chat_id": "-100a"})
	f.row(t, store.ScopeGroup, &f.group, "", nil, &base.ID, map[string]any{"chat_id": "-100group"})

	got := f.effective(t, &f.group, f.proj)
	if len(got) != 1 {
		t.Fatalf("got %d recipients, want 1", len(got))
	}
	if !got[0].Enabled {
		t.Error("setting one value at the group level turned the recipient off")
	}
	if !chats(got)["-100group"] {
		t.Errorf("the chat id is %v, want the group's", chats(got))
	}
}

// A project moved to another group no longer inherits what the first group
// configured. The setting that pointed at it is reported rather than quietly turned
// into a recipient of its own, which would restore a channel the move was meant to
// have got rid of.
func TestSettingsThatStopApplyingAreReported(t *testing.T) {
	f := setupTargets(t)
	fromGroup := f.row(t, store.ScopeGroup, &f.group, "team chat", on(), nil,
		map[string]any{"chat_id": "-100a"})
	override := f.row(t, store.ScopeProject, &f.proj, "", nil, &fromGroup.ID,
		map[string]any{"chat_id": "-100own"})

	if inGroup := f.effective(t, &f.group, f.proj); len(inGroup) != 1 {
		t.Fatalf("in the group the project gets %d recipients, want 1", len(inGroup))
	}

	// After the move the group's chat is not one of this project's recipients, and
	// neither is the setting that was only ever about changing it.
	moved := f.resolved(t, &f.group2, f.proj)
	if len(moved.Targets) != 0 {
		t.Errorf("after the move the project still gets %v, want nothing", chats(moved.Targets))
	}
	if len(moved.Stale) != 1 || moved.Stale[0] != override.ID {
		t.Errorf("the stale rows are %v, want the one override %s", moved.Stale, override.ID)
	}
}

// Deleting a recipient removes the settings that were changing it: an override with
// nothing to override is not a recipient.
func TestDeletingARecipientTakesItsOverridesWithIt(t *testing.T) {
	f := setupTargets(t)
	base := f.row(t, store.ScopeInstance, nil, "everyone", nil, nil, map[string]any{"chat_id": "-100a"})
	override := f.row(t, store.ScopeProject, &f.proj, "", nil, &base.ID, map[string]any{"chat_id": "-100own"})

	if err := f.store.ModuleTargets().Delete(t.Context(), base.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	if _, err := f.store.ModuleTargets().ByID(t.Context(), override.ID); err == nil {
		t.Error("the override survived the row it was changing")
	}
}

// Two rows pointing at the same chat are two messages, on purpose: somebody may
// want one bot in two topics of one group.
func TestTheSameAddressTwiceIsTwoMessages(t *testing.T) {
	f := setupTargets(t)
	f.row(t, store.ScopeInstance, nil, "deploys", nil, nil,
		map[string]any{"chat_id": "-100a", "thread_id": "7"})
	f.row(t, store.ScopeInstance, nil, "experiments", nil, nil,
		map[string]any{"chat_id": "-100a", "thread_id": "9"})

	if got := f.effective(t, nil, f.proj2); len(got) != 2 {
		t.Fatalf("the project gets %d recipients, want 2", len(got))
	}
}

// The order people put recipients in is kept, so a shared channel does not have its
// deployments overtaken by whatever was built first.
func TestRecipientsKeepTheOrderTheyWerePutIn(t *testing.T) {
	f := setupTargets(t)
	targets := f.store.ModuleTargets()

	for _, row := range []struct {
		label    string
		position int
	}{
		{"deploys", 1},
		{"everything else", 0},
	} {
		if _, err := targets.Create(context.Background(), &store.ModuleTarget{
			IntegrationID: f.module, ScopeType: store.ScopeInstance,
			Label: row.label, Position: row.position,
		}); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	got := f.effective(t, nil, f.proj2)
	if len(got) != 2 {
		t.Fatalf("got %d recipients, want 2", len(got))
	}
	if got[0].Own.Label != "everything else" || got[1].Own.Label != "deploys" {
		t.Errorf("the order is %q then %q, want \"everything else\" first",
			got[0].Own.Label, got[1].Own.Label)
	}
}
