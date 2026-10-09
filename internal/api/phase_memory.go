package api

// phaseMemory is what a page needs to reconstruct a deployment it did not watch.
//
// The set of phases still open, and the last line of each phase in the order the phases
// happened. The second is what turns a list of steps with nothing on them into a list with
// marks: a page opened during a rollout has seen none of it, and the last line per phase is the
// only account of it that survives.
//
// One type for both places that write these, and that is the whole point. A deployment's lines
// reach a page along two roads — a module narrating a deployment, and a runner reporting the
// building of the image that deployment will put somewhere — and each road built its own copy of
// this. They did not agree: the second wrote a phase and a message and nothing else, so a page
// that reloaded during an image build was handed a snapshot with no history in it, drew an arrow
// on the phase being built, and had nothing that could mark it finished. Two implementations of
// one question is how they came to disagree in the first place.
type phaseMemory struct {
	// open are the phases begun and not yet ended, in the order they began.
	open []string
	// said is every phase this has heard of, in the order it heard them, kept whatever has since
	// finished: the open set is pruned as phases close and cannot say what came before.
	said []string
	// lines is the last line of each phase.
	lines map[string]map[string]any
}

func newPhaseMemory() *phaseMemory {
	return &phaseMemory{lines: map[string]map[string]any{}}
}

// note records one line and stamps what it knows onto it.
func (m *phaseMemory) note(payload map[string]any, phase string, finished bool) {
	if phase != "" {
		if _, named := m.lines[phase]; !named {
			m.said = append(m.said, phase)
		}
		if finished {
			m.open = dropPhase(m.open, phase)
		} else if !holdsPhase(m.open, phase) {
			m.open = append(m.open, phase)
		}

		// Copied before the two lists are added below, and holding `payload` itself would be a
		// cycle: the snapshot is part of the very line it is copied out of.
		kept := make(map[string]any, len(payload))
		for key, value := range payload {
			kept[key] = value
		}
		m.lines[phase] = kept
	}

	m.stamp(payload)
}

// stamp writes what this memory currently knows onto a line.
//
// Not only from note, because two lines about one moment have to describe that moment the same
// way. The closing of a phase and the phase that follows it are published back to back, and a
// page that receives the first between them should not be told that nothing is under way —
// which is what the closing would say if it were stamped before the next phase was added. The
// state that is real is the state after both, and it goes on both.
func (m *phaseMemory) stamp(payload map[string]any) {
	payload["active_phases"] = append([]string(nil), m.open...)
	payload["phase_history"] = phaseHistoryInOrder(m.lines, m.said, phaseOrder)
}

// phaseOrder is the order phases are shown in when a page is rebuilt from a snapshot.
//
// A hint and not knowledge: this core deploys through whichever module a project names, and the
// phases below are the ones the kubernetes module happens to use. It is here to put the familiar
// ones in a sensible order, not to decide what a deployment went through — a phase it does not
// name is kept too, after these, in the order it was said.
//
// Written down once, because a second copy of a list of names is a list that will be edited in
// one place.
var phaseOrder = []string{
	"build", "push", "prepare", "pre", "pull", "apply", "rollout", "retire", "post",
}

func holdsPhase(list []string, phase string) bool {
	for _, one := range list {
		if one == phase {
			return true
		}
	}
	return false
}

func dropPhase(list []string, phase string) []string {
	kept := list[:0]
	for _, one := range list {
		if one != phase {
			kept = append(kept, one)
		}
	}
	return kept
}
