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

// The snapshot that gets stored, after a phase changes.
//
// The test above is about the order of two calls. This one is about the thing the page actually
// reads: `RememberDeployProgress` replaces the whole document rather than merging fields, so a
// closing saved on its own survives in nothing at all — the next line overwrites it. What has to
// hold is the snapshot left behind after the new phase is written, and it has to hold on its own.
//
// Modelled as the handler runs it: note the first phase, note its closing, note the next. What
// comes out is what `rememberDeployProgress` would store.
func TestTheStoredSnapshotAfterAPhaseChanges(t *testing.T) {
	memory := newPhaseMemory()

	build := lineOf("build", "the image is built")
	memory.note(build, "build", false)

	closing := lineOf("build", "finished")
	closing["finished"] = true
	memory.note(closing, "build", true)

	push := lineOf("push", "the registry has it as sha256:0abc")
	memory.note(push, "push", false)

	stored, ok := push["phase_history"].([]map[string]any)
	if !ok {
		t.Fatalf("the stored snapshot carries no phase history: %v", push)
	}

	byPhase := map[string]map[string]any{}
	for _, line := range stored {
		byPhase[line["phase"].(string)] = line
	}

	if byPhase["build"]["finished"] != true {
		t.Errorf("build is stored as %v, want finished: true — the closing was overwritten", byPhase["build"])
	}
	if byPhase["build"]["message"] != "finished" {
		t.Errorf("build is stored as saying %q, want the closing", byPhase["build"]["message"])
	}
	if byPhase["push"] == nil {
		t.Fatalf("the phase that is under way is not in the snapshot: %v", stored)
	}

	assertOpen(t, push, "push")
	if _, stillThere := byPhase["push"]; !stillThere {
		t.Error("the snapshot does not keep the phase being worked on")
	}
}

// And a rollout overlapping its own retiring keeps both, which is the case a rule that closes
// everything a new phase begins throws away.
//
// Two arrows rather than one, because one arrow for a rollout says one of the two things is not
// happening — and the new pods really are coming up while the old ones are going.
func TestAStoredSnapshotKeepsBothOfTwoOverlappingPhases(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("rollout", "3 of 3 running the new image"), "rollout", false)
	retire := lineOf("retire", "3 pods still running the previous image")
	memory.note(retire, "retire", false)

	// And then the rollout says it is done, which must not take the retiring with it.
	done := lineOf("rollout", "finished")
	done["finished"] = true
	memory.note(done, "rollout", true)

	assertOpen(t, done, "retire")
}

// Nothing is open once it has stopped, even the phase that was open when it did.
//
// A module closes a phase when the next one begins, which leaves the last to be closed by
// whatever ends it. For a deployment that is the bare "finished" line it writes at the end — and
// it names no phase, so it closes nothing. The phase a deployment ended on then stayed open in
// the snapshot, and a page opened afterwards read a completed deployment as still rolling out.
//
// This is the state the closing loop in the handler produces, and it is the state a page reads.
func TestNothingIsOpenAfterTheStreamEnds(t *testing.T) {
	memory := newPhaseMemory()

	memory.note(lineOf("rollout", "3 of 3 running the new image"), "rollout", false)

	// The last line a deployment says: done, and naming no phase at all.
	settled := lineOf("", "finished")
	settled["job_id"] = 9
	memory.note(settled, "", false)

	// Nothing has closed it yet, which is the bug: it is still open with the module finished.
	assertOpen(t, settled, "rollout")

	for _, phase := range append([]string(nil), memory.open...) {
		memory.note(lineOf(phase, "finished"), phase, true)
	}
	memory.stamp(settled)

	assertOpen(t, settled)

	// And the closing says what it closed, so the history explains why the arrow went.
	history, _ := settled["phase_history"].([]map[string]any)
	last := history[len(history)-1]
	if last["phase"] != "rollout" || last["message"] != "finished" {
		t.Errorf("the last line of the history is %v, want the closing of rollout", last)
	}
}
