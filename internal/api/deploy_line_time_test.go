package api

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// Every line of a deployment's log says when it was written.
//
// The point is not ordering — a stream already arrives in order — but that a hole becomes visible.
// A page fetches its list, then subscribes, and whatever happened between the two is lost. Without
// a time on each line the log simply begins in the middle and looks whole; with one, the first line
// a page receives that is older than the moment it subscribed is proof that it joined late.

// The stamp is on the stored line and not only on the live event, because a card read back and a
// card watched have to say the same thing about the same line.
func TestEveryStoredLineSaysWhenItWasWritten(t *testing.T) {
	before := time.Now().UnixMilli()
	line := markStream("out", "rollout: 2 of 3 ready")
	after := time.Now().UnixMilli()

	stamp, text := readStamp(t, line)
	if stamp < before || stamp > after {
		t.Errorf("the line is stamped %d, want between %d and %d", stamp, before, after)
	}
	if text != "rollout: 2 of 3 ready" {
		t.Errorf("the text after the stamp is %q, want the line as written", text)
	}
}

// Both streams are stamped. A failure written to stderr is the line somebody will come back for,
// so it is the one that most needs a moment on it.
func TestBothStreamsAreStamped(t *testing.T) {
	for _, stream := range []string{"out", "err"} {
		stamp, _ := readStamp(t, markStream(stream, "something said"))
		if stamp == 0 {
			t.Errorf("a %q line carries no time", stream)
		}
	}
}

// Several lines in one write all get stamped, because one write can be several lines — a rollout
// reports ready counts one line at a time and a phase can say three things at once.
func TestEveryLineOfAWriteIsStamped(t *testing.T) {
	written := markStream("out", "first\nsecond\nthird")

	lines := strings.Split(strings.TrimSuffix(written, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("wrote %d lines, want 3: %q", len(lines), written)
	}
	for i, one := range lines {
		if stamp, _ := readStamp(t, one); stamp == 0 {
			t.Errorf("line %d has no time on it: %q", i+1, one)
		}
	}
}

// An empty write stays empty rather than becoming a line with a time and nothing on it.
func TestNothingSaidIsNotALine(t *testing.T) {
	if got := markStream("out", ""); got != "" {
		t.Errorf("an empty write stored %q", got)
	}
}

// A line relayed as an event carries its own time, so a page watching live and a page reading the
// log afterwards agree about when each line happened.
func TestARelayedLineCarriesAMoment(t *testing.T) {
	before := time.Now().UnixMilli()
	payload := relayOf([]byte(`{"phase":"rollout","message":"2 of 3 ready"}`), 42, nil, "deploy")
	after := time.Now().UnixMilli()

	stamp, ok := payload["at"].(int64)
	if !ok {
		t.Fatalf("the relayed line has no time on it: %#v", payload)
	}
	if stamp < before || stamp > after {
		t.Errorf("the relayed line is stamped %d, want between %d and %d", stamp, before, after)
	}
	if payload["job_id"] != int64(42) {
		t.Errorf("the relayed line names job %v, want 42", payload["job_id"])
	}
}

// A module that says when a thing happened keeps its own answer. Two clocks on one line is worse
// than one clock, because nothing can then say which of them the ordering follows.
func TestAModuleThatStampsItsOwnLineIsNotOverridden(t *testing.T) {
	payload := relayOf([]byte(`{"message":"something happened","at":1234567890}`), 42, nil, "deploy")
	if payload["at"] != float64(1234567890) {
		t.Errorf("the core overwrote the module's own time: %#v", payload["at"])
	}
}

// A line that is not an object is still a line and still has a moment, or a module that narrates
// in plain sentences gets a log with no times in it.
func TestAPlainSentenceIsStillStamped(t *testing.T) {
	payload := relayOf([]byte(`the cluster said no`), 42, nil, "deploy")
	if stamp, ok := payload["at"].(int64); !ok || stamp == 0 {
		t.Errorf("a plain sentence was relayed with no time on it: %#v", payload)
	}
	if payload["message"] != "the cluster said no" {
		t.Errorf("the sentence came out as %v", payload["message"])
	}
}

// readStamp reads one stored line back into its time and its text, as a reader has to.
func readStamp(t *testing.T, line string) (int64, string) {
	t.Helper()

	for _, tag := range []string{"out| ", "err| "} {
		if !strings.HasPrefix(line, tag) {
			continue
		}
		rest := line[len(tag):]
		at, text, found := strings.Cut(rest, "| ")
		if !found {
			t.Fatalf("the line has a stream but no time: %q", line)
		}
		stamp, err := strconv.ParseInt(strings.TrimSpace(at), 10, 64)
		if err != nil {
			t.Fatalf("the time on the line is not a number: %q", line)
		}
		return stamp, strings.TrimSpace(text)
	}
	t.Fatalf("the line has no stream marker on it: %q", line)
	return 0, ""
}
