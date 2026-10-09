package api

import "testing"

// A job that has moved on has left the phase it was in.
//
// This is the rule the deploy module applies to its own phases and that this endpoint did not,
// so a page keeping track of what is happening now kept every phase a build ever named open.
// It was seen on a stand as arrows on "Build the image" and "Push the image" at the same time as
// "Read the manifests" drawn finished and "Run the pre-step jobs" drawn running — four steps of a
// sequence saying four different things about the same moment.
//
// The wording matters: not two things happening at once, which is fine and expected, but a first
// step still running under a third that is already done. Nobody can read that as anything but a
// page that has lost track.
func TestANewPhaseClosesTheOneBefore(t *testing.T) {
	s := &Server{}

	if got := s.phaseBefore(7, "build"); got != "" {
		t.Errorf("the first phase of a job closed something (%q), want nothing to close", got)
	}
	if got := s.phaseBefore(7, "push"); got != "build" {
		t.Errorf("moving from build to push closed %q, want build", got)
	}
	if got := s.phaseBefore(7, "prepare"); got != "push" {
		t.Errorf("moving from push to prepare closed %q, want push", got)
	}
}

// Saying the same thing twice is not moving on.
//
// A phase that counts — "3 of 3 running the new image" — is said once per tick, and a rule that
// treated the second tick as the beginning of a new stage would close a phase that is still going.
func TestRepeatingAPhaseClosesNothing(t *testing.T) {
	s := &Server{}

	s.phaseBefore(8, "rollout")
	if got := s.phaseBefore(8, "rollout"); got != "" {
		t.Errorf("a second line about rollout closed %q, want nothing", got)
	}
	if got := s.phaseBefore(8, "retire"); got != "rollout" {
		t.Errorf("moving from rollout to retire closed %q, want rollout", got)
	}
}

// Two jobs of one run are two unrelated things, and closing one must not touch the other.
//
// A pipeline builds and deploys at once, and the phases of each mean nothing to the other.
func TestPhasesOfTwoJobsAreTheirOwn(t *testing.T) {
	s := &Server{}

	s.phaseBefore(9, "build")
	s.phaseBefore(10, "build")
	if got := s.phaseBefore(9, "push"); got != "build" {
		t.Errorf("the build job closed %q, want build", got)
	}
	if got := s.phaseBefore(10, "push"); got != "build" {
		t.Errorf("the second job closed %q, want its own build", got)
	}
}

// A line that names no phase names nothing to close, and changes nothing about where we are.
//
// A module says a great many things without naming a phase. Treating one of those as a new phase
// would close whatever was genuinely in progress.
func TestAPhaseThatIsNotNamedClosesNothing(t *testing.T) {
	s := &Server{}

	s.phaseBefore(11, "rollout")
	if got := s.phaseBefore(11, ""); got != "" {
		t.Errorf("an unnamed line closed %q, want nothing", got)
	}
	if got := s.phaseBefore(11, "retire"); got != "rollout" {
		t.Errorf("after an unnamed line the job was not where it was, closed %q, want rollout", got)
	}
}

// A finished job takes its phase with it.
//
// The map is keyed by a job id that is never reused, so a server that is left running collects
// what every build it ever ran was in the middle of, in order to answer a question about a job
// that is still going.
func TestAFinishedJobTakesItsPhaseWithIt(t *testing.T) {
	s := &Server{}

	s.phaseBefore(12, "build")
	if got := s.closePhases(12); got != "build" {
		t.Errorf("a finished job said it was in %q, want build", got)
	}
	if got := s.phaseBefore(12, "push"); got != "" {
		t.Errorf("a job id that was closed still had a phase to close (%q)", got)
	}
}

// The last phase of a job is closed by the job ending, because nothing follows it.
//
// A build says "build", then "push", then the job is over — and a rule that closes a phase when
// the next one begins has nothing to close "push" with. Left open, it is an arrow on a step of an
// image that was pushed and was sitting on the cluster minutes earlier, and it stays there as
// long as the card is open: seen on a stand as an arrow on "Push the image" while "Read the
// manifests" beside it was already done.
func TestTheLastPhaseOfAJobIsClosedWhenTheJobEnds(t *testing.T) {
	s := &Server{}

	s.phaseBefore(13, "build")
	if got := s.phaseBefore(13, "push"); got != "build" {
		t.Fatalf("moving from build to push closed %q, want build", got)
	}
	// Nothing follows push — the job simply finishes.
	if got := s.closePhases(13); got != "push" {
		t.Errorf("a job that ended on push closed %q, want push", got)
	}
}

// A job that never named a phase has nothing to close, and saying so is not an error.
func TestAJobThatNeverSaidAPhaseClosesNothing(t *testing.T) {
	s := &Server{}

	if got := s.closePhases(14); got != "" {
		t.Errorf("a job that never reported a phase closed %q, want nothing", got)
	}
	s.phaseBefore(14, "build")
	s.closePhases(14)
	if got := s.closePhases(14); got != "" {
		t.Errorf("closing twice said %q the second time, want nothing", got)
	}
}
