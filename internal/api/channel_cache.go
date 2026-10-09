package api

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulechan"
)

// The cache of commands that were not answered.
//
// Two things are cached and only two: a decision a module has to act on, and nothing else. A
// fact — "there is work" — corrects itself, because a module that acts on a stale one asks for
// work and is told the queue is empty. Caching that would be storing something that cannot be
// wrong, in exchange for the ability to deliver it later to a module that no longer needs it.
//
// # The core does not decide whether a command is still true
//
// A command lives here for a length of time and is then forgotten, whatever happened to it. The
// core has no standing to throw one away: if the deploy module was asleep when its cluster
// changed, the cluster it was told about may be exactly the one somebody is now looking at, and
// the only thing that knows is the module. So a command is delivered again on reconnect even if
// it was already delivered once, and dropped when its time is up rather than when somebody
// answers it.
//
// Which means delivery is at-least-once, and the id on the message is what makes that workable:
// a module that has already acted can refuse the command it has seen. A module that does not
// check is no worse off than one that received nothing at all.

// The default a module gets when it says nothing about how long to keep a command.
//
// Five minutes, and it is not a decision: it is the length of a deploy, which is what most
// commands are. Zero is spelled out as no caching rather than as an absence, so an
// administrator who wants no cache has a value to type.
const (
	// defaultCommandTTL is what an absent or empty setting means, and it is not a decision:
	// it is the length of a five-minute deploy, which is what most commands are. Zero is
	// spelled out as no caching rather than as an absence, so an administrator who wants no
	// cache has a value to type.
	defaultCommandTTL = 300 * time.Second
)

// Fact is a message that cannot go stale, so nothing is kept for it and nothing waits on it.
//
// Its own type rather than a flag on a message, so that sending one cannot be mistaken for
// sending the other: a caller who has a decision in hand and reaches for this has made a mistake
// the compiler will not catch but the page above will.
type Fact struct {
	Kind    string
	Payload any
}

// Decision is a message the module has to act on, and that is kept until its time is up.
//
// The whole meaning travels in one message. A command split into steps — first do this, then
// that — loses exactly what the split was for: step one arrives, step two is lost, and the
// module has been told to deploy something without being told what.
type Decision struct {
	Kind    string
	Payload any
}

// pendingCommand is a decision waiting to be delivered again.
type pendingCommand struct {
	message   modulechan.Message
	expiresAt time.Time
}

func (p pendingCommand) expired(now time.Time) bool {
	return !now.Before(p.expiresAt)
}

// Decide sends a decision to a module and keeps it until the module's own command lifetime runs
// out.
//
// An id is made up here and put on the message, because the module is the only thing that can
// tell a repeat from a new command, and it cannot do that without one. The command is kept
// whatever the delivery did — including when nothing was connected, which is the case the whole
// arrangement exists for.
func (ch *ModuleChannel) Decide(ctx context.Context, module *models.Integration,
	decision Decision) (int, error) {

	body, err := json.Marshal(decision.Payload)
	if err != nil {
		return 0, fmt.Errorf("encode a %q command: %w", decision.Kind, err)
	}

	message := modulechan.Message{
		ID:      uuid.NewString(),
		Kind:    decision.Kind,
		Payload: body,
	}

	lifetime := ch.s.commandLifetime(ctx, module)
	if lifetime > 0 {
		ch.keep(module.ID, pendingCommand{message: message,
			expiresAt: time.Now().Add(lifetime)})
	}

	delivered, err := ch.send(ctx, module.ID, message)
	if err != nil {
		return delivered, err
	}
	if lifetime == 0 {
		return delivered, nil
	}

	ch.s.log.Debug("a command is waiting to be delivered again", "module", module.Name,
		"kind", decision.Kind, "command", message.ID, "seconds", int(lifetime.Seconds()))
	return delivered, nil
}

// keep files a command for redelivery.
func (ch *ModuleChannel) keep(moduleID uuid.UUID, command pendingCommand) {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.pending[moduleID] = append(ch.pending[moduleID], command)
}

// takePending returns the commands that have not run out, dropping the ones that have.
//
// Called on every attach, so a module that reconnects is given what it missed. The expired are
// dropped on the way rather than by a timer of their own: a command nobody has reconnected to
// yet is not costing anything, and a ticker would be a goroutine kept alive only to delete
// things.
func (ch *ModuleChannel) takePending(moduleID uuid.UUID) []pendingCommand {
	ch.mu.Lock()
	defer ch.mu.Unlock()

	now := time.Now()
	kept := make([]pendingCommand, 0, len(ch.pending[moduleID]))
	for _, command := range ch.pending[moduleID] {
		if command.expired(now) {
			continue
		}
		kept = append(kept, command)
	}
	if len(kept) == 0 {
		delete(ch.pending, moduleID)
	} else {
		ch.pending[moduleID] = kept
	}
	return kept
}

// PendingCommands is how many commands are waiting to be delivered to a module.
//
// Shown on the module page, because "the core thinks it told the module to deploy and the
// module says it was never told" is otherwise indistinguishable from a module that ignored it.
func (ch *ModuleChannel) PendingCommands(moduleID uuid.UUID) int {
	ch.mu.Lock()
	defer ch.mu.Unlock()
	return len(ch.pending[moduleID])
}

// commandLifetime is how long this module's commands are kept.
//
// A read that failed is answered with the default rather than with zero. Zero means the
// administrator asked for no cache, and a database that would not answer is not somebody saying
// that; guessing "do not cache" here would silently turn every outage into a decision dropped.
func (s *Server) commandLifetime(ctx context.Context, module *models.Integration) time.Duration {
	settings, err := s.store.Integrations().SettingsFor(ctx, module.ID, nil, nil,
		module.Capabilities.Settings)
	if err != nil {
		s.log.Warn("a module's command lifetime could not be read, so the default is used",
			"module", module.Name, "error", err)
		return defaultCommandTTL
	}
	return commandLifetimeOf(settings[modulechan.CommandTTLSetting])
}

// commandLifetimeOf turns the setting's value into a duration.
//
// Absent, empty and unparsable all mean the default, because a field nobody filled in is a field
// nobody has an opinion about. A negative number is refused by the settings check before it gets
// here; if one arrives anyway it means no cache rather than a command kept for a length of time
// that cannot be written down.
func commandLifetimeOf(raw json.RawMessage) time.Duration {
	if len(raw) == 0 {
		return defaultCommandTTL
	}

	// Through a pointer because JSON's null is not a number that failed to parse: it
	// unmarshals into an int without complaint and leaves it at zero, which would turn a
	// cleared field into the one value that means no cache at all.
	var seconds *int
	if err := json.Unmarshal(raw, &seconds); err != nil || seconds == nil {
		return defaultCommandTTL
	}
	if *seconds <= 0 {
		return 0
	}
	return time.Duration(*seconds) * time.Second
}
