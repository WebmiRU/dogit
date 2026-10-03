package pipeline

import (
	"strings"
	"testing"
)

// What a pipeline says about being announced.
//
// Every case here is about silence being deliberate. A pipeline that says nothing
// must stay silent, and a pipeline that says something must say exactly that — a
// condition read as nothing is a channel that goes quiet on the night it mattered.

// A file with no notify block says nothing, and saying nothing means silence.
func TestAPipelineWithNoNotifyBlockIsSilent(t *testing.T) {
	config, err := Parse([]byte("stages: [build]\nimage:\n  stage: build\n  script: [true]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, announced := config.Notify.Announces(EventJobFinished, "failure"); announced {
		t.Error("a pipeline with no notify block announced something")
	}
	if config.Notify.Present {
		t.Error("a file that never mentioned notifications claims to have")
	}
}

// A block with no conditions speaks whatever happened: that is the block having said
// something, not a default filling in for silence.
func TestABlockWithNoConditionsSpeaksEveryLevel(t *testing.T) {
	config, err := Parse([]byte(`
notify:
  - text: "a run finished"
image:
  script: [true]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, level := range []string{"success", "failure", "canceled", "running"} {
		entry, announced := config.Notify.Announces(EventJobFinished, level)
		if !announced {
			t.Errorf("nothing was said at %s", level)
			continue
		}
		if entry.Text != "a run finished" {
			t.Errorf("at %s the text is %q", level, entry.Text)
		}
	}
}

// Conditions are levels, and only levels.
func TestConditionsAreLevelsOnly(t *testing.T) {
	config, err := Parse([]byte(`
notify:
  - on: [failure]
    text: "it broke"
image:
  script: [true]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, announced := config.Notify.Announces(EventJobFinished, "failure"); !announced {
		t.Error("a failure was not announced")
	}
	for _, level := range []string{"success", "canceled", "running"} {
		if _, announced := config.Notify.Announces(EventJobFinished, level); announced {
			t.Errorf("%s was announced although only failures were", level)
		}
	}
}

// `always` means the entry was written to be sent whatever happened.
func TestAlwaysSpeaksAtEveryLevel(t *testing.T) {
	config, err := Parse([]byte(`
notify:
  - on: [always]
    text: "a run started"
image:
  script: [true]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, announced := config.Notify.Announces(EventPipelineStarted, "running"); !announced {
		t.Error("an entry that says always did not speak at a running level")
	}
}

// Silence said out loud, for a pipeline that used to announce itself.
func TestFalseIsSilenceWrittenDown(t *testing.T) {
	config, err := Parse([]byte("notify: false\nimage:\n  script: [true]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !config.Notify.Silent || !config.Notify.Present {
		t.Errorf("notify: false was read as %+v", config.Notify)
	}
	if _, announced := config.Notify.Announces(EventJobFinished, "failure"); announced {
		t.Error("a pipeline that said notify: false announced something")
	}
}

// A single entry without the dashes is the same thing, because both are natural to
// write.
func TestOneEntryNeedsNoDashes(t *testing.T) {
	config, err := Parse([]byte("notify:\n  on: [failure]\n  text: it broke\nimage:\n  script: [true]\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if _, announced := config.Notify.Announces(EventJobFinished, "failure"); !announced {
		t.Error("a single entry was not read")
	}
}

// A misspelt field is refused. Silently dropping `titel:` leaves somebody looking for
// a message that was never going to be sent.
func TestAnUnknownFieldIsRefused(t *testing.T) {
	_, err := Parse([]byte(`
notify:
  - titel: "the wrong spelling"
    text: "the right text"
image:
  script: [true]
`))
	if err == nil {
		t.Fatal("a misspelt field was accepted")
	}
	if !strings.Contains(err.Error(), "titel") {
		t.Errorf("the refusal does not name the field: %v", err)
	}
}

// A level nobody here knows is refused rather than matching nothing: a condition
// that quietly matches nothing is a channel that goes quiet.
func TestAnUnknownLevelIsRefused(t *testing.T) {
	_, err := Parse([]byte("notify:\n  - on: [fail]\n    text: it broke\nimage:\n  script: [true]\n"))
	if err == nil {
		t.Fatal("a level that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "fail") {
		t.Errorf("the refusal does not name the level: %v", err)
	}
}

// An entry with nothing to say is a half-written one, and sending it would teach
// people that messages can be empty.
func TestAnEntryWithNothingToSayIsRefused(t *testing.T) {
	_, err := Parse([]byte("notify:\n  - on: [failure]\nimage:\n  script: [true]\n"))
	if err == nil {
		t.Fatal("an empty entry was accepted")
	}
}

// The first entry whose conditions match is the one that speaks.
func TestTheFirstMatchingEntryWins(t *testing.T) {
	config, err := Parse([]byte(`
notify:
  - on: [failure]
    text: "the deployment failed"
  - on: [failure]
    text: "never read"
image:
  script: [true]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	entry, announced := config.Notify.Announces(EventJobFinished, "failure")
	if !announced || entry.Text != "the deployment failed" {
		t.Errorf("the entry that spoke was %q", entry.Text)
	}
}

// A failed job and a failed run are different news, and a level cannot tell them
// apart. Naming the event is how one block says both.
func TestEventNarrowsAnEntryToOneKindOfThing(t *testing.T) {
	config, err := Parse([]byte(`
notify:
  - event: job.finished
    on: [failure]
    title: A job failed
    text: "${job.name} did not finish"
  - event: pipeline.finished
    on: [failure]
    title: The run failed
    text: "The run did not finish"
image:
  script: [true]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	job, announced := config.Notify.Announces(EventJobFinished, "failure")
	if !announced || job.Title != "A job failed" {
		t.Errorf("the job's entry was %q", job.Title)
	}

	run, announced := config.Notify.Announces(EventPipelineFinished, "failure")
	if !announced || run.Title != "The run failed" {
		t.Errorf("the run's entry was %q", run.Title)
	}

	if _, announced := config.Notify.Announces(EventPipelineStarted, "running"); announced {
		t.Error("an entry about failures spoke about a run starting")
	}
}

// An event nobody here produces is refused rather than matching nothing.
func TestAnUnknownEventIsRefused(t *testing.T) {
	_, err := Parse([]byte("notify:\n  - event: job.flaked\n    text: it broke\nimage:\n  script: [true]\n"))
	if err == nil {
		t.Fatal("an event that does not happen was accepted")
	}
	if !strings.Contains(err.Error(), "job.flaked") {
		t.Errorf("the refusal does not name the event: %v", err)
	}
}
