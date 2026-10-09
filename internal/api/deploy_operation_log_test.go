package api

import (
	"encoding/json"
	"testing"
)

// A stored log is a storage format, and a page wants its parts as fields.
//
// Getting this wrong is quiet: a reader that takes the stamp off and hands it on as part of the
// text shows somebody "1791382456281| rollout: 2 of 3 ready" as their deploy output, and a reader
// that assumes a stamp exists shows every line written before this one as an error.

// A line with a moment on it comes apart into the two things it is made of.
func TestAStampedLineIsTakenApart(t *testing.T) {
	entry := parseLogEntry("out| 1791382456281| rollout: 2 of 3 ready")

	if entry["stream"] != "out" {
		t.Errorf("the line is from %v, want out", entry["stream"])
	}
	if entry["at"] != int64(1791382456281) {
		t.Errorf("the line says %v, want 1791382456281", entry["at"])
	}
	if entry["text"] != "rollout: 2 of 3 ready" {
		t.Errorf("the text came out as %q", entry["text"])
	}
}

// A line written before times existed is read the same way, minus the time.
//
// Not as an error and not as a broken line: it is a good line that happens not to say when, and a
// parser that made a fuss would report damage where there is none.
func TestAStamplessLineIsReadAsASentence(t *testing.T) {
	entry := parseLogEntry("out| cloning https://example.test/repo.git")

	if entry["stream"] != "out" {
		t.Errorf("the line is from %v, want out", entry["stream"])
	}
	if _, stamped := entry["at"]; stamped {
		t.Error("a line with no time on it was given one")
	}
	if entry["text"] != "cloning https://example.test/repo.git" {
		t.Errorf("the text came out as %q", entry["text"])
	}
}

// A line with no marker at all is ordinary output. It came from somewhere that did not write a
// marker — a runner, an older core — and dropping it would lose a line, which is worse than
// showing one in the wrong colour.
func TestAnUnmarkedLineIsOrdinaryOutput(t *testing.T) {
	entry := parseLogEntry("just a sentence")
	if entry["stream"] != "out" {
		t.Errorf("an unmarked line came from %v, want out", entry["stream"])
	}
	if entry["text"] != "just a sentence" {
		t.Errorf("the text came out as %q", entry["text"])
	}
}

// The stamp is a number and a separator. A line whose text begins with digits and a pipe is a
// sentence, and reading it as a stamp would take a word out of somebody's deploy output.
func TestALineThatMerelyLooksStampedIsLeftAlone(t *testing.T) {
	entry := parseLogEntry("out| 2026-10-08 | the registry answered | that is all")

	if _, stamped := entry["at"]; stamped {
		t.Errorf("a sentence was read as a stamp: %#v", entry)
	}
	if entry["text"] != "2026-10-08 | the registry answered | that is all" {
		t.Errorf("the text came out as %q", entry["text"])
	}
}

// An error line keeps its stream, with or without a time. A failure is the line somebody comes
// back for, so it is the one that must not lose its colour.
func TestAnErrorLineKeepsItsStream(t *testing.T) {
	for _, line := range []string{
		"err| 1791382456281| the cluster refused",
		"err| the cluster refused",
	} {
		entry := parseLogEntry(line)
		if entry["stream"] != "err" {
			t.Errorf("%q came from %v, want err", line, entry["stream"])
		}
	}
}

// The whole log, in order, and empty rather than absent: a page that draws "nothing was recorded"
// and one that draws an empty box are saying different things, and only one of them is true.
func TestAWholeLogComesApartInOrder(t *testing.T) {
	entries := parseLogLines("out| 1| first\nout| 2| second\nerr| 3| third\n")
	if len(entries) != 3 {
		t.Fatalf("read %d lines, want 3: %#v", len(entries), entries)
	}

	for i, want := range []string{"first", "second", "third"} {
		if entries[i]["text"] != want {
			t.Errorf("line %d is %v, want %q", i+1, entries[i]["text"], want)
		}
	}
	if entries[2]["stream"] != "err" {
		t.Errorf("the third line is from %v, want err", entries[2]["stream"])
	}
	if empty := parseLogLines(""); len(empty) != 0 {
		t.Errorf("an empty log came back as %d lines", len(empty))
	}
}

// Every entry has to be JSON, because that is how it reaches a page. A map with a nil in it
// marshals to null and a reader is left with a hole where a log line was.
func TestEveryEntryIsWrittenAsJSON(t *testing.T) {
	for _, line := range []string{"out| 1| said", "out| said", "said", "err| 2| refused"} {
		if _, err := json.Marshal(parseLogEntry(line)); err != nil {
			t.Errorf("%q cannot be written as JSON: %v", line, err)
		}
	}
}
