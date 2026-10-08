package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
)

// Asking a module a question and waiting for what it says.
//
// This is not the same thing as announcing or deciding, and it is here rather than folded into
// either because it is the third thing: a message with an id that comes back with an answer
// carrying the same id. The work it replaces was an HTTP request whose response body was read
// before the caller could go on — seven places that could not do anything else until a module
// on another machine had finished.
//
// # The id is what makes waiting possible
//
// A connection carries messages in both directions at once, so "the answer" is found by the id
// the core made up rather than by being the next thing to arrive. That is also the same id the
// module uses to refuse a command it has already acted on, so the answer and the refusal are
// the same mechanism rather than two that have to agree.

// ErrNoAnswer is what a caller gets when the module did not answer in time.
//
// Not the same as a refusal, and the difference matters to whoever reads the message: a module
// that said no told the core something, and a module that said nothing may well be doing the
// work. The command is still held, so it will be delivered again — this caller's answer is that
// it did not get one.
var ErrNoAnswer = errors.New("the module did not answer in time")

// answerWait is one caller waiting for one answer.
type answerWait struct {
	// buffered, so the module's read loop never blocks on a caller that gave up: a module
	// answering into a full channel would stop answering anybody at all, which is the one
	// failure mode a channel must not have.
	answer chan json.RawMessage
}

func (w *answerWait) offer(payload json.RawMessage) bool {
	select {
	case w.answer <- payload:
		return true
	default:
		return false
	}
}

// Call sends a command and waits for the module's answer.
//
// Kept for redelivery like any other decision, so a module that was asleep when the question
// was asked is asked again when it comes back. The caller here does not get that second chance
// — it has a timeout and moves on — and the two do not conflict: the command outlives the call.
//
// The timeout is the caller's, through the context, and a context with no deadline gets
// defaultAnswerTimeout rather than none. A request with no deadline on a channel that can go
// down silently is a goroutine that waits forever, and the deploy path already has one of those
// deliberately.
func (ch *ModuleChannel) Call(ctx context.Context, module *models.Integration,
	decision Decision, timeout time.Duration) (json.RawMessage, error) {

	if timeout <= 0 {
		timeout = defaultAnswerTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Registered before the command goes out, so an answer that arrives before Call has
	// finished registering is not dropped on the floor. The cost of registering first is one
	// message sent into a closed channel if the caller is cancelled in between, and that is
	// answered by nobody rather than by the wrong caller.
	wait := &answerWait{answer: make(chan json.RawMessage, 1)}
	id := ch.expect(wait)
	defer ch.forget(id)

	body, err := json.Marshal(decision.Payload)
	if err != nil {
		return nil, fmt.Errorf("encode a %q command: %w", decision.Kind, err)
	}

	message := modulechan.Message{ID: id, Kind: decision.Kind, Payload: body}
	lifetime := ch.s.commandLifetime(ctx, module)
	if lifetime > 0 {
		ch.keep(module.ID, pendingCommand{message: message, expiresAt: time.Now().Add(lifetime)})
	}

	if _, err := ch.send(ctx, module.ID, message); err != nil {
		return nil, err
	}

	select {
	case answer := <-wait.answer:
		return answer, nil
	case <-waitCtx.Done():
		if ctx.Err() != nil {
			// The caller went away rather than the module going quiet. Saying "no answer"
			// would blame the module for somebody closing a page.
			return nil, ctx.Err()
		}
		ch.s.log.Warn("a module did not answer a command in time", "module", module.Name,
			"kind", decision.Kind, "command", id, "timeout", timeout)
		return nil, ErrNoAnswer
	}
}

// defaultAnswerTimeout is how long a caller waits when it says nothing itself.
//
// Thirty seconds, the same answer the HTTP call this replaces gave: a module that cannot
// respond to a question inside half a minute is not responding at all, and a page waiting longer
// than that is a page with a spinner on it.
const defaultAnswerTimeout = 30 * time.Second

// expect registers a caller and says which id to wait on.
func (ch *ModuleChannel) expect(wait *answerWait) string {
	ch.answersMu.Lock()
	defer ch.answersMu.Unlock()
	if ch.answers == nil {
		ch.answers = map[string]*answerWait{}
	}
	id := uuid.NewString()
	ch.answers[id] = wait
	return id
}

// forget drops a caller that has been answered, timed out, or given up on.
//
// Idempotent, because a caller that timed out and was then answered arrives here twice and the
// second arrival must not take a later caller's place.
func (ch *ModuleChannel) forget(id string) {
	ch.answersMu.Lock()
	defer ch.answersMu.Unlock()
	delete(ch.answers, id)
}

// answerTo hands a payload to whoever is waiting on this id, and says whether anybody was.
//
// Read from the module's own message loop, which is the only place an answer is noticed, and it
// must not block: the loop belongs to every module, not to one caller.
func (ch *ModuleChannel) answerTo(id string, payload json.RawMessage) bool {
	ch.answersMu.Lock()
	wait, found := ch.answers[id]
	delete(ch.answers, id)
	ch.answersMu.Unlock()

	if !found {
		return false
	}
	return wait.offer(payload)
}

// PendingAnswers is how many callers are waiting for an answer right now.
//
// Shown nowhere yet, and kept because the number is the difference between "the module is
// ignoring the core" and "the core is waiting on the module", and an operator reading a page
// that is stuck has no way to tell those apart from the outside.
func (ch *ModuleChannel) PendingAnswers() int {
	ch.answersMu.Lock()
	defer ch.answersMu.Unlock()
	return len(ch.answers)
}
