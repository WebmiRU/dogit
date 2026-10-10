package api

import "sync"

// deployPlace is where a deployment happens: the project, the place inside it, and the
// namespace inside that.
//
// The same three things the module holds a place for, and deliberately the same — a queue
// keyed on anything coarser would serialise deployments that never actually conflict, and
// anything finer would let two rollouts into one namespace overlap and leave the cluster
// somewhere neither of them was asked for.
type deployPlace struct {
	project   string
	cluster   string
	namespace string
}

// queuedDeploy is a deployment that wants a place and is not getting it yet.
type queuedDeploy struct {
	jobID      int64
	pipelineID int64
}

// deployQueue is one deployment per place at a time, with a queue behind it.
//
// Not a setting and not a knob, because there is no arrangement in which two rollouts
// into one namespace at the same moment is what anybody meant. What the module used to do
// was refuse the second, which loses a deployment somebody pushed for over a conflict that
// would have resolved itself in a minute. The rule here keeps the cluster's state honest
// instead: the deployment already under way is never interrupted — interrupting mid-rollout
// can leave fewer ready pods than there were before, because the replacements are not up
// yet — and only the newest of those waiting actually goes.
//
// In memory rather than in the table, because it is a claim about what this process is
// doing right now, not a fact about what happened. A restart empties it, which is right: a
// deployment this process was carrying out died with it, and its place is free.
//
// A value rather than a pointer, and ready at its zero value, so that it can be a field on
// Server and nobody constructing one has to remember to initialise it. Thirty-odd places
// build a Server in tests and one of them forgetting would be a queue that quietly does
// nothing — which is the old behaviour, and would pass every test that does not ask.
type deployQueue struct {
	mu    sync.Mutex
	slots map[deployPlace]*deploySlot
}

// deploySlot is one place: who has it, and who is behind them.
//
// running is a job id rather than a flag so that a release can tell whether the caller is
// the holder. A method that frees a place it does not hold would hand the place to two
// deployments at once, which is the one thing this file exists to prevent, and the caller
// that got it wrong would be a caller nobody had a test for.
type deploySlot struct {
	running int64
	waiting []queuedDeploy
}

func newDeployQueue() *deployQueue { return &deployQueue{} }

// take claims the place for a deployment, or joins the queue behind the one that has it.
//
// False means the deployment is waiting and must not be started: the caller leaves the job
// pending and writes down that it is waiting. That is not a refusal, and the difference is
// the whole point of this — a refused deployment is over and has to be pushed again, while
// this one has not happened yet and will, on its own, shortly.
//
// Two answers rather than one, because "may I start" and "did I take it" are different
// questions. This is asked every time a job finishes, which for the same run means more
// than once, so a second call arrives after the first has already claimed the place. That
// second call must be told it may start — it is the same deployment, and it is already
// running — without being told it owns the place, because the first call is the one that
// will have to give it back. Told otherwise, the second call would either queue the job
// behind itself or release a place it never took.
func (q *deployQueue) take(place deployPlace, jobID, pipelineID int64) (mayStart bool, holder bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	slot, found := q.slots[place]
	if !found {
		if q.slots == nil {
			q.slots = map[deployPlace]*deploySlot{}
		}
		slot = &deploySlot{}
		q.slots[place] = slot
	}
	switch {
	case slot.running == 0:
		slot.running = jobID
		return true, true
	case slot.running == jobID:
		return true, false
	default:
		slot.waiting = append(slot.waiting, queuedDeploy{jobID: jobID, pipelineID: pipelineID})
		return false, false
	}
}

// free hands the place back and says what becomes of the ones that were waiting.
//
// The newest waiting deployment is promoted, because the newest is the one somebody is
// waiting to see and the one whose code the cluster should end up on. Deploying the oldest
// of three would roll the namespace back to code that had already been replaced, and then
// the newer one would follow it, and the cluster would spend the minute walking backwards.
// The older ones are superseded: never started, so never interrupted, and not failed
// either, since nothing broke and nothing was attempted.
//
// The place stays held across the promotion. `take` is not called again for the promoted
// job, so the gap between giving the place back and the promoted deployment actually
// starting has to stay closed — otherwise two deployments arriving in that gap would both
// be told they may start, which is the failure this type exists to make impossible.
//
// A place held by nobody, or held by somebody who is not asking, is simply dropped. A
// deployment that ended by a path the holder did not expect must still free its place, or
// it is held for ever by a job that finished hours ago.
func (q *deployQueue) free(place deployPlace, jobID int64) (*queuedDeploy, []queuedDeploy) {
	q.mu.Lock()
	defer q.mu.Unlock()

	slot, found := q.slots[place]
	if !found {
		return nil, nil
	}
	if slot.running != jobID {
		return nil, nil
	}
	if len(slot.waiting) == 0 {
		slot.running = 0
		delete(q.slots, place)
		return nil, nil
	}

	newest := len(slot.waiting) - 1
	promoted := slot.waiting[newest]
	older := append([]queuedDeploy(nil), slot.waiting[:newest]...)

	slot.waiting = nil
	slot.running = promoted.jobID
	return &promoted, older
}
