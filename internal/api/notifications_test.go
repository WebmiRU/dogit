package api

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// One thing happened, and it goes to everybody who is switched on for it.
//
// Before recipients existed the core chose a single module and wrote one record, so
// "the shared chat and my own topic" was not something anybody could ask for. These
// check that the queue is now written once per recipient and that each record says
// where it is going — because a module that works out the address at delivery time
// sends yesterday's message to today's settings.

// setupRecipients is a project, a telegram module and whatever recipients the test
// adds itself.
//
// The module is one the test made: several fixtures register a telegram module, and
// addressing this by kind would hand the test somebody else's module — and
// everybody else's recipients with it.
func setupRecipients(t *testing.T) (*moduleFixture, *models.Project, *models.Integration) {
	t.Helper()

	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "recipients", nil)

	telegram, err := f.store.Integrations().Register(t.Context(), "notify:telegram",
		dbtest.Unique("telegram"), "http://module-notify:8093", models.Manifest{})
	if err != nil {
		t.Fatalf("register the notification module: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, telegram.ID)
	})

	// The module is switched on: one that is merely installed delivers nothing,
	// which is the state it is in between being installed and being set up.
	if err := f.store.Integrations().SetEnabled(t.Context(), telegram.ID, true); err != nil {
		t.Fatalf("switch the module on: %v", err)
	}

	return f, project, telegram
}

// add is one recipient at the instance level.
func addRecipient(t *testing.T, f *moduleFixture, moduleID uuid.UUID, label, chat string,
	enabled *bool) *store.ModuleTarget {
	t.Helper()

	target, err := f.store.ModuleTargets().Create(t.Context(), &store.ModuleTarget{
		IntegrationID: moduleID,
		ScopeType:     store.ScopeInstance,
		Label:         label,
		Enabled:       enabled,
		Values:        map[string]json.RawMessage{"chat_id": json.RawMessage(mustJSON(chat))},
	})
	if err != nil {
		t.Fatalf("add recipient: %v", err)
	}
	return target
}

// lastID is where the queue got to.
//
// The test database is shared, so a test says "what was written from here on"
// rather than reading the whole table and working out what is its own.
func lastID(t *testing.T, f *moduleFixture) int64 {
	t.Helper()

	var id int64
	if err := f.store.Pool().QueryRow(t.Context(),
		`SELECT COALESCE(max(id), 0) FROM notifications`).Scan(&id); err != nil {
		t.Fatalf("read the end of the queue: %v", err)
	}
	return id
}

func queuedSince(t *testing.T, f *moduleFixture, kind string, after int64) []store.Notification {
	t.Helper()

	notes, err := f.store.Notifications().Since(t.Context(), kind, after, 500)
	if err != nil {
		t.Fatalf("read the %s queue: %v", kind, err)
	}
	return notes
}

// forThese keeps only the records addressed to the given recipients.
func forThese(notes []store.Notification, ids ...uuid.UUID) []store.Notification {
	wanted := map[uuid.UUID]bool{}
	for _, id := range ids {
		wanted[id] = true
	}

	out := []store.Notification{}
	for _, note := range notes {
		if wanted[note.TargetID] {
			out = append(out, note)
		}
	}
	return out
}

// A notification reaches every switched-on recipient, each as its own record.
func TestANotificationIsQueuedOnceForEveryRecipient(t *testing.T) {
	f, project, module := setupRecipients(t)

	shared := addRecipient(t, f, module.ID, "everyone", "-100a", nil)
	deploys := addRecipient(t, f, module.ID, "deploys", "-100b", nil)
	off := false
	noisy := addRecipient(t, f, module.ID, "noisy", "-100c", &off)

	before := lastID(t, f)
	f.server.notify(t.Context(), project.Path, "job.finished", "build failed", "failure",
		map[string]any{"job": map[string]any{"name": "build"}})

	notes := queuedSince(t, f, "notify:telegram", before)
	mine := forThese(notes, shared.ID, deploys.ID, noisy.ID)
	if len(mine) != 2 {
		t.Fatalf("the queue has %d records for these recipients, want one per switched-on one (2)",
			len(mine))
	}

	got := map[string]bool{}
	for _, note := range mine {
		chat, ok := note.TargetValues["chat_id"].(string)
		if !ok {
			t.Fatalf("a record does not carry its recipient: %v", note.TargetValues)
		}
		got[chat] = true
		if note.Text != "build failed" || len(note.Levels) != 1 || note.Levels[0] != "failure" {
			t.Errorf("a record lost what happened: %q %v", note.Text, note.Levels)
		}
		if len(note.Data) == 0 {
			t.Error("a record lost the facts")
		}
	}
	if !got["-100a"] || !got["-100b"] {
		t.Errorf("the message went to %v, want both switched-on chats", got)
	}
	if got["-100c"] {
		t.Error("the message went to a recipient that is switched off")
	}

	// Queueing is not editing: the recipient list is the same afterwards.
	rows, err := f.store.ModuleTargets().At(t.Context(), module.ID, store.ScopeInstance, nil)
	if err != nil || len(rows) != 3 {
		t.Errorf("queueing a notification changed the recipients: %d rows, %v", len(rows), err)
	}
}

// Two channels mean two messages, each addressed to its own place — not one message
// read by every module, and not every module sending to every place.
func TestEachChannelIsToldOnce(t *testing.T) {
	f, project, telegram := setupRecipients(t)
	toChat := addRecipient(t, f, telegram.ID, "everyone", "-100a", nil)

	mail, err := f.store.Integrations().Register(t.Context(), "notify:email",
		dbtest.Unique("email"), "http://module-notify-email:8094",
		models.Manifest{})
	if err != nil {
		t.Fatalf("register a second channel: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, mail.ID)
	})
	if err := f.store.Integrations().SetEnabled(t.Context(), mail.ID, true); err != nil {
		t.Fatalf("switch the second channel on: %v", err)
	}

	before := lastID(t, f)
	f.server.notify(t.Context(), project.Path, "job.finished", "build failed", "failure", nil)

	// One event, one record in the chat's queue — and nothing in the mailbox's yet,
	// because a channel nobody has given an address cannot be told anything.
	if got := forThese(queuedSince(t, f, "notify:telegram", before), toChat.ID); len(got) != 1 {
		t.Errorf("the chat got %d records, want 1", len(got))
	}
	if got := forThese(queuedSince(t, f, "notify:email", before), mail.ID); len(got) != 0 {
		t.Errorf("the mailbox got %d records although it has no recipient", len(got))
	}

	// Now give the email module a recipient of its own and say the same thing again.
	toMail := addRecipient(t, f, mail.ID, "oncall", "oncall@example.com", nil)

	before = lastID(t, f)
	f.server.notify(t.Context(), project.Path, "job.finished", "build failed", "failure", nil)

	if got := forThese(queuedSince(t, f, "notify:telegram", before), toChat.ID); len(got) != 1 {
		t.Errorf("the chat got %d records, want 1", len(got))
	}
	if got := forThese(queuedSince(t, f, "notify:email", before), toMail.ID); len(got) != 1 {
		t.Errorf("the mailbox got %d records, want 1", len(got))
	}
}
