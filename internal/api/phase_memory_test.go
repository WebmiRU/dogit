package api

import "testing"

// A phase that has been left is not open, in the snapshot as well as on the page.
//
// The snapshot is written as each line arrives, and a page opened after a refresh reads only the
// snapshot. A closing that went out on the socket but was never written into that snapshot left
// the page showing the end of a phase and, a moment later, its arrow back — the same operation
// drawing both because the two readers were being told different things.
func TestALeftPhaseIsNotLeftOpenInTheSnapshot(t *testing.T) {
	memory := newPhaseMemory()

	build := lineOf("build", "the image is built")
	memory.note(build, "build", false)
	assertOpen(t, build, "build")

	// The phase before this one ended, said before the new one is noted.
	closing := lineOf("build", "finished")
	closing["finished"] = true
	memory.note(closing, "build", true)

	push := lineOf("push", "the registry has it")
	memory.note(push, "push", false)

	assertOpen(t, push, "push")
}

// And the closing is a line the snapshot can still account for afterwards.
//
// Otherwise a page restoring itself has an arrow on a step that finished and no record of why it
// stopped, which is a step it has to guess about.
func TestTheClosingSurvivesIntoTheSnapshot(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("build", "the image is built"), "build", false)
	closing := lineOf("build", "finished")
	closing["finished"] = true
	memory.note(closing, "build", true)
	push := lineOf("push", "the registry has it")
	memory.note(push, "push", false)

	history, _ := push["phase_history"].([]map[string]any)
	if len(history) != 2 {
		t.Fatalf("the snapshot carries %d lines of history, want 2", len(history))
	}
	last := history[0]
	if last["phase"] != "build" || last["message"] != "finished" || last["finished"] != true {
		t.Errorf("the first line in the history is %v, want the closing of build", last)
	}
	if history[1]["phase"] != "push" {
		t.Errorf("the second line is phase %v, want push", history[1]["phase"])
	}
}

// Two phases at once are two open phases, because a rollout and the retiring that goes with it
// really do overlap.
//
// A rule that closes everything a new phase begins is what reduces a rollout to one arrow, which
// is the whole reason the open set is a list and not a single value.
func TestTwoPhasesAtOnceAreBothOpen(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("rollout", "3 of 3 running the new image"), "rollout", false)
	retire := lineOf("retire", "3 pods still running the previous image")
	memory.note(retire, "retire", false)

	assertOpen(t, retire, "rollout", "retire")
}

// A phase said again is not two phases.
func TestARepeatedPhaseIsOnePhase(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("rollout", "1 of 3"), "rollout", false)
	second := lineOf("rollout", "2 of 3")
	memory.note(second, "rollout", false)

	assertOpen(t, second, "rollout")
}

// A line that names no phase says nothing about which phases are open, and changes nothing.
func TestAPhaseThatIsNotNamedIsCarriedAlong(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("rollout", "3 of 3"), "rollout", false)
	bare := lineOf("", "something said without a phase")
	memory.note(bare, "", false)

	assertOpen(t, bare, "rollout")
}

func lineOf(phase, message string) map[string]any {
	return map[string]any{"phase": phase, "message": message}
}

func assertOpen(t *testing.T, payload map[string]any, want ...string) {
	t.Helper()
	open, _ := payload["active_phases"].([]string)
	if len(open) != len(want) {
		t.Fatalf("the snapshot says %d phases open (%v), want %d (%v)", len(open), open, len(want), want)
	}
	for index, phase := range want {
		if open[index] != phase {
			t.Errorf("open phase %d is %q, want %q", index, open[index], phase)
		}
	}
}
