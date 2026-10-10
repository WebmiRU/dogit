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
	// project is for logs and display only. The queue is keyed by identity, the physical
	// cluster endpoint/context and namespace, so aliases in different projects still meet.
	project    string
	cluster    string
	namespace  string
	identity   string   // stable local queue key for the complete target set
	identities []string // each physical API-server/namespace pair that this job will mutate
	logicalKey string
}

// queueKey is the shared physical identity within this process. Tests and older callers may
// construct a place without a resolved identity; those use the old tuple, never a blank key.
func (p deployPlace) queueKey() string {
	if p.identity != "" {
		return p.identity
	}
	return p.project + "\x00" + p.cluster + "\x00" + p.namespace
}

// candidateKeys are checked together in PostgreSQL: the physical identity arbitrates across
// projects and aliases, while the logical identity also invalidates an unresolved target from
// a newer pipeline instead of letting an older candidate sneak through.
func (p deployPlace) candidateKeys() []string {
	keys := append([]string(nil), p.identities...)
	// Preserve direct queue tests and any older caller that represents a single physical
	// target with identity but has not populated the identities slice.
	if len(keys) == 0 && p.identity != "" {
		keys = append(keys, p.identity)
	}
	if p.logicalKey != "" {
		keys = append(keys, p.logicalKey)
	}
	return uniqueSorted(keys)
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
	slots map[string]*deploySlot
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
	mayStart, holder, _ = q.takeWithStatus(place, jobID, pipelineID)
	return mayStart, holder
}

// takeWithStatus also says whether this call actually added a new queue entry.
//
// A pipeline can ask whether its deployment may start after more than one job finishes.
// Repeated calls for the same pending deployment must not append the same job several times:
// when the place is freed, one copy could be promoted while another copy supersedes it, and
// the promoted copy would then discover it had already been marked over. The flag also keeps
// repeated calls from writing the same "waiting" line over and over.
func (q *deployQueue) takeWithStatus(place deployPlace, jobID, pipelineID int64) (
	mayStart bool, holder bool, enqueued bool,
) {
	q.mu.Lock()
	defer q.mu.Unlock()

	key := place.queueKey()\n\tslot, found := q.slots[key]
	if !found {
		if q.slots == nil {
			q.slots = map[string]*deploySlot{}
		}
		slot = &deploySlot{}
		q.slots[key] = slot
	}
	switch {
	case slot.running == 0:
		slot.running = jobID
		return true, true, false
	case slot.running == jobID:
		return true, false, false
	default:
		for _, waiting := range slot.waiting {
			if waiting.jobID == jobID {
				return false, false, false
			}
		}
		slot.waiting = append(slot.waiting, queuedDeploy{jobID: jobID, pipelineID: pipelineID})
		return false, false, true
	}
}

// waitingFor says whether this job is standing in a queue right now.
//
// The queue's own answer rather than the row's, because the row cannot tell the two apart. A
// deploy job that is still building its image and pushing it to the registry has not started,
// and "has not started" is also what every queued deployment looks like from the table. Told
// from there, a deployment that had not yet reached the queue was reported as waiting its turn
// for as long as its build took — half a minute on an ordinary deploy, which is most of the
// time anybody watches the page — and the page said so in the header while the run was plainly
// under way.
//
// False after a restart, and that is right rather than a gap: the queue is emptied by the
// restart, and a deployment this process was carrying out died with it.
func (q *deployQueue) waitingFor(jobID int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, slot := range q.slots {
		for _, waiting := range slot.waiting {
			if waiting.jobID == jobID {
				return true
			}
		}
	}
	return false
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

	key := place.queueKey()\n\tslot, found := q.slots[key]
	if !found {
		return nil, nil
	}
	if slot.running != jobID {
		return nil, nil
	}
	if len(slot.waiting) == 0 {
		slot.running = 0
		delete(q.slots, key)
		return nil, nil
	}

	newest := len(slot.waiting) - 1
	promoted := slot.waiting[newest]
	older := append([]queuedDeploy(nil), slot.waiting[:newest]...)

	slot.waiting = nil
	slot.running = promoted.jobID
	return &promoted, older
}
