package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// What a message may say about itself, and what happens when it asks for something
// that does not exist.
//
// The template is written by whoever wrote the pipeline, and it is read at three in
// the morning by nobody. So a name that does not exist has to be an error rather than
// an empty space: a message saying "failed" where the job's name should be looks
// delivered and tells nobody anything.

// A run with no notify block in its configuration is silent, however the instance is
// set up. This is the case that matters most: it is what stops a project with a
// hundred pipelines from announcing every experiment.
func TestARunWhoseConfigurationSaysNothingIsSilent(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "silent", nil)

	withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "job.finished",
		Project:  map[string]any{"path": project.Path, "name": project.Name},
		Pipeline: map[string]any{"iid": 1, "ref": "main", "status": "success"},
		Job:      map[string]any{"name": "build", "status": "success"},
	}
	f.server.notifyEvent(t.Context(), "job.finished", context, "build succeeded")

	if got := queuedSince(t, f, "notify:telegram", before); len(got) != 0 {
		t.Errorf("a pipeline that said nothing announced %d times", len(got))
	}
}

// The same run, with a block that speaks at this level.
func TestABlockThatSpeaksProducesOneMessagePerRecipient(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "speaks", nil)

	target := withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "job.finished",
		Project:  map[string]any{"path": project.Path, "name": project.Name},
		Pipeline: map[string]any{"iid": 1, "ref": "main", "status": "success"},
		Job:      map[string]any{"name": "build", "status": "success"},
	}
	entry := pipelineNotifyEntry{Title: "It worked", Text: "all good"}
	f.server.announce(t.Context(), "job.finished", entry, context, "build succeeded")

	got := forThese(queuedSince(t, f, "notify:telegram", before), target.ID)
	if len(got) != 1 {
		t.Fatalf("the message was written %d times, want once for the one recipient", len(got))
	}
	if title, _ := got[0].Data["title"].(string); title != "It worked" {
		t.Errorf("the title the pipeline wrote is missing: %v", got[0].Data)
	}
	if got[0].TargetID != target.ID {
		t.Errorf("the message went to %s, want the configured recipient", got[0].TargetID)
	}
}

// The pipeline's own words, with the substitutions filled in.
func TestTheMessagesOwnWordsAreUsedAndFilledIn(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "words", nil)
	target := withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "job.finished",
		Project:  map[string]any{"path": project.Path, "name": project.Name},
		Pipeline: map[string]any{"iid": 42, "ref": "main", "status": "failure", "url": "/p/x/-/pipelines/42"},
		Job:      map[string]any{"name": "deploy", "stage": "deploy", "status": "failure"},
		Images:   []map[string]any{{"name": "registry/www", "tag": "dev"}},
	}
	entry := pipelineNotifyEntry{
		Title: "Deploy failed",
		Text:  "${project.path} #${pipeline.iid}: ${job.name} failed, image ${image.name}:${image.tag}\n${pipeline.url}",
	}
	f.server.announce(t.Context(), "job.finished", entry, context, "the core's own words")

	got := forThese(queuedSince(t, f, "notify:telegram", before), target.ID)
	if len(got) != 1 {
		t.Fatalf("the message was written %d times", len(got))
	}

	want := project.Path + " #42: deploy failed, image registry/www:dev\n/p/x/-/pipelines/42"
	if got[0].Text != want {
		t.Errorf("the message is\n%q\nwant\n%q", got[0].Text, want)
	}
}

// A name that does not exist stops the message. Sending it with a hole in it would be
// a delivery that says nothing.
func TestAnUnknownNameStopsTheMessage(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "holes", nil)
	withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "job.finished",
		Project:  map[string]any{"path": project.Path},
		Pipeline: map[string]any{"iid": 1, "ref": "main", "status": "failure"},
		Job:      map[string]any{"name": "deploy", "status": "failure"},
	}
	entry := pipelineNotifyEntry{Text: "${job.nmae} failed"}
	f.server.announce(t.Context(), "job.finished", entry, context, "deploy failed")

	if got := queuedSince(t, f, "notify:telegram", before); len(got) != 0 {
		t.Errorf("a message asking for something that does not exist was sent: %q", got[0].Text)
	}
}

// A job that built several images mentions all of them: a template that says
// ${image.name} should say what was built, not the first thing that was.
func TestImagesAreAllMentioned(t *testing.T) {
	context := notifyContext{
		Images: []map[string]any{
			{"name": "registry/a", "tag": "one"},
			{"name": "registry/b", "tag": "two"},
		},
	}

	values := substitutionValues(context)
	if values["image.name"] != "registry/a, registry/b" {
		t.Errorf("image.name is %q, want both names", values["image.name"])
	}
	if values["image.tag"] != "one, two" {
		t.Errorf("image.tag is %q, want both tags", values["image.tag"])
	}
}

// A pipeline may say nothing and let the core's wording stand: deciding to speak and
// writing the message are two different jobs.
func TestABlockWithNoTextKeepsTheFallback(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "fallback", nil)
	target := withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "job.finished",
		Project:  map[string]any{"path": project.Path},
		Pipeline: map[string]any{"iid": 1, "ref": "main", "status": "success"},
		Job:      map[string]any{"name": "build", "status": "success"},
	}
	f.server.announce(t.Context(), "job.finished", pipelineNotifyEntry{}, context, "the core's own words")

	got := forThese(queuedSince(t, f, "notify:telegram", before), target.ID)
	if len(got) != 1 || got[0].Text != "the core's own words" {
		t.Errorf("a block without text did not keep the fallback wording: %v", got)
	}
}

// withRecipients gives a project one telegram recipient and returns it.
func withRecipients(t *testing.T, f *moduleFixture) store.NotificationAddress {
	t.Helper()

	module, err := f.store.Integrations().Register(t.Context(), "notify:telegram",
		dbtest.Unique("telegram"), "http://module-notify:8093", []byte("hash"), models.Manifest{
			Settings: []models.SettingSpec{{Key: "chat_id", Label: "Chat id", Type: "string"}},
			Target:   models.TargetSpec{Settings: []string{"chat_id"}, Identify: []string{"chat_id"}},
		})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(t.Context(), `DELETE FROM integrations WHERE id = $1`, module.ID)
	})
	if err := f.store.Integrations().SetEnabled(t.Context(), module.ID, true); err != nil {
		t.Fatalf("switch on: %v", err)
	}

	row, err := f.store.NotificationTargets().Create(t.Context(), &store.NotificationTarget{
		IntegrationID: module.ID,
		ScopeType:     store.ScopeInstance,
		Label:         "everyone",
		Values:        map[string]json.RawMessage{"chat_id": json.RawMessage(mustJSON("-100a"))},
	})
	if err != nil {
		t.Fatalf("add a recipient: %v", err)
	}

	return store.NotificationAddress{ID: row.ID, Values: map[string]any{"chat_id": "-100a"}}
}

// A template that asks a run about a job's name must not produce "home-store/www #21
// — :". It is the same hole as a name that does not exist, and it stops the message
// the same way.
func TestAMessageWithNothingToSayIsNotSent(t *testing.T) {
	f := newModuleFixture(t)
	project := dbtest.NewProject(t, f.store, "nothing", nil)
	target := withRecipients(t, f)

	before := lastID(t, f)
	context := notifyContext{
		Event:    "pipeline.finished",
		Project:  map[string]any{"path": project.Path},
		Pipeline: map[string]any{"iid": 1, "ref": "main", "status": "success"},
	}
	// No job, so there is no job name and no image.
	entry := pipelineNotifyEntry{Text: "${project.path} #${pipeline.iid} — ${image.name}:${image.tag}"}
	f.server.announce(t.Context(), "pipeline.finished", entry, context, "the core's own words")

	if got := forThese(queuedSince(t, f, "notify:telegram", before), target.ID); len(got) != 0 {
		t.Errorf("a message with nothing to say was sent: %q", got[0].Text)
	}
}

// substitute is exercised on its own, because its whole job is refusing.
func TestSubstitutionRefusesWhatItCannotFill(t *testing.T) {
	got, missing := substitute("${a} and ${b}", map[string]string{"a": "one"})
	if got != "one and ${b}" {
		t.Errorf("the template came out as %q", got)
	}
	if len(missing) != 1 || missing[0] != "b" {
		t.Errorf("the missing names are %v, want [b]", missing)
	}

	// A name that exists but has nothing in it is the same as one that does not:
	// "home-store/www #21 — :" is delivered and says nothing.
	if _, missing := substitute("${a}", map[string]string{"a": ""}); len(missing) != 1 {
		t.Errorf("an empty value was accepted: %v", missing)
	}

	if got, missing := substitute("${a", map[string]string{}); len(missing) != 1 || !strings.Contains(missing[0], "${a") {
		t.Errorf("an unclosed ${ was accepted: %q %v", got, missing)
	}
}
