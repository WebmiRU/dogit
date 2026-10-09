package api

import "testing"

// A page that was not watching a deployment gets the phases back in the order they happened.
//
// It used to get them in the order a map happens to walk, which is stable within one run and
// arbitrary between two — so a restored log rearranged itself when nothing about the deployment
// had changed. Every other ordering on this page is kept for the same reason, and the time on
// each line exists so a reader can see when it was said.
func TestPhasesComeBackInTheOrderTheyHappened(t *testing.T) {
	history := map[string]map[string]any{
		"build":   {"phase": "build", "message": "the image is built"},
		"push":    {"phase": "push", "message": "the registry has it"},
		"prepare": {"phase": "prepare", "message": "3 manifests prepared"},
	}
	// Said in a different order from the walk, because a module is not obliged to follow one.
	said := []string{"prepare", "push", "build"}

	got := phaseHistoryInOrder(history, said, planOfPhases())
	assertPhases(t, got, "build", "push", "prepare")
}

// A phase this core does not name is kept too, after the ones it knows, in the order it was said.
//
// The list of phase names is a hint about the module that happens to be in front of us and not a
// rule about deployments: another module says other things, and dropping them would leave a
// restored page quietly missing part of what happened.
func TestAPhaseNobodyListedIsStillKept(t *testing.T) {
	history := map[string]map[string]any{
		"prepare": {"phase": "prepare", "message": "prepared"},
		"migrate": {"phase": "migrate", "message": "the schema is current"},
		"warm":    {"phase": "warm", "message": "the cache is warm"},
	}
	said := []string{"prepare", "warm", "migrate"}

	got := phaseHistoryInOrder(history, said, planOfPhases())
	assertPhases(t, got, "prepare", "warm", "migrate")
}

// And it appears once, however many times it was said.
//
// A phase counts pods up over many lines. The last line is what is kept, and it is kept once.
func TestARepeatedPhaseIsKeptOnce(t *testing.T) {
	history := map[string]map[string]any{
		"rollout": {"phase": "rollout", "message": "3 of 3 running the new image"},
	}
	said := []string{"rollout", "rollout", "rollout"}

	got := phaseHistoryInOrder(history, said, planOfPhases())
	assertPhases(t, got, "rollout")
}

// The order the list names wins over the order the lines came in, for the phases it names.
//
// A module that goes back to a phase it has already left — retrying a rollout, say — is not
// describing a deployment that unrolled: the walk is the order, and this is the list saying so.
func TestTheOrderTheListNamesWins(t *testing.T) {
	history := map[string]map[string]any{
		"rollout": {"phase": "rollout", "message": "3 of 3"},
		"apply":   {"phase": "apply", "message": "3 of 3 applied"},
	}
	said := []string{"rollout", "apply", "rollout"}

	got := phaseHistoryInOrder(history, said, planOfPhases())
	assertPhases(t, got, "apply", "rollout")
}

// Nothing said, nothing restored.
func TestNothingSaidRestoresNothing(t *testing.T) {
	got := phaseHistoryInOrder(map[string]map[string]any{}, nil, planOfPhases())
	if len(got) != 0 {
		t.Errorf("restored %d lines out of nothing", len(got))
	}
}

func assertPhases(t *testing.T, lines []map[string]any, want ...string) {
	t.Helper()
	if len(lines) != len(want) {
		got := make([]string, 0, len(lines))
		for _, one := range lines {
			got = append(got, one["phase"].(string))
		}
		t.Fatalf("restored %d lines (%v), want %d (%v)", len(lines), got, len(want), want)
	}
	for index, phase := range want {
		if lines[index]["phase"] != phase {
			t.Errorf("line %d is phase %q, want %q", index, lines[index]["phase"], phase)
		}
	}
}

// The list the walk is taken from, as a caller supplies it.
func planOfPhases() []string {
	return []string{"build", "push", "prepare", "pre", "pull", "apply", "rollout", "retire", "post"}
}
