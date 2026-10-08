// Package modulechan is the channel a module keeps open to the core.
//
// One connection per module, opened by the module. Outgoing passes NAT, so a module behind a
// firewall can talk to the core without the core reaching for it — which is the whole reason the
// channel exists, since the core calling a module at its endpoint works only when the module
// happens to have an address the core can dial.
//
// This file is the protocol and the client. The core's side is in internal/api, because it needs
// the store, the logger and the settings; everything that can be shared without those is here, so
// that a module and the core agree on the bytes by construction rather than by two descriptions
// of them that drift apart.
//
// # The token is in every message
//
// Not in a handshake. Every message carries the token that identifies the module, which is what
// makes the connection stateless: there is nothing to restore after a reconnect, and nothing the
// core has to remember about a socket that has been open for six hours.
//
// Two things follow, and both are load-bearing:
//
//   - The token is checked on every message, not once at connect. A long-lived connection is
//     exactly where a connect-time check goes stale: a module whose token ended yesterday keeps
//     a socket open indefinitely and keeps receiving commands the core is obliged to withdraw.
//     A refused token ends the connection, with the reason in the last message, because a module
//     being disconnected for no stated reason cannot fix itself.
//
//   - The connection is not an identity. Nothing about "who is this" may be cached for the life
//     of the socket — not its rights, not which client a reply belongs to, not who has already
//     answered. A cached identity is state, and state is what the token in every message exists
//     to avoid.
package modulechan

import (
	"encoding/json"
	"fmt"
)

// Message is what travels in both directions.
//
// Same shape either way, so there is one thing to read and one thing to write rather than a
// request format and a response format that have to be kept in step.
type Message struct {
	// ID ties an answer to the message that asked for it. A core-to-module command carries
	// the one it made up; the module's answer carries the same one back. Empty on a message
	// that expects no answer, such as a notification.
	ID string `json:"id,omitempty"`

	// Kind says what this is. It is a closed vocabulary, not a namespace somebody extends:
	// a module that does not recognise one is expected to ignore it, and that only works if
	// the set is knowable. Unknown kinds are never an error here — they are somebody else's
	// business, on a version of the core that has heard of more than this one.
	Kind string `json:"kind"`

	// Token is the module's own credential, and it is checked against the core on every
	// message it appears in.
	Token string `json:"token"`

	// Payload is whatever the kind carries. Left raw so that a module which has never heard
	// of a kind can still read the envelope and skip it, which is what lets an old module
	// keep working against a newer core.
	Payload json.RawMessage `json:"payload,omitempty"`
}

// The vocabulary. Short names on purpose: these appear in logs and on the wire, and a
// notification that says "modulechan.command.failed" tells nobody anything.
const (
	// WorkAvailable says a job is waiting for a runner. It is a notification, not a command:
	// the runner still claims the work over HTTP, because the claim is atomic and belongs in
	// the database where it can be.
	WorkAvailable = "work.available"

	// Answer says what came of a message the module was sent. Carries the ID of that
	// message.
	Answer = "answer"

	// Refused says the core will not deal with this message, and the connection is about to
	// end. Separate from Answer because it is not an answer to anything: there is no ID to
	// tie it to when the message that carried the bad token was never read.
	Refused = "refused"
)

// Refusal is the payload of a Refused message.
//
// The reason is written out rather than reduced to a status code, because the whole point of
// ending a connection is that the module on the other end can act on it: "this token ended on
// the 12th" and "this token is not one I know" call for different things from the author of a
// module, and neither of them is served by "authentication failed".
type Refusal struct {
	Reason string `json:"reason"`

	// EndedOn is set when the token had a date and it has passed, and is the only thing in
	// the refusal that tells somebody what to write in their manifest.
	EndedOn string `json:"ended_on,omitempty"`
}

// CommandTTLSetting is the setting in which a module says how long the core keeps a command
// for it, in seconds.
//
// Here rather than in the core because a module has to be able to announce the setting in its
// own manifest and read the same name back: a key written out in two places is two places to
// change, and the failure is silent — a module asking for a lifetime under one name while the
// core reads another, and neither of them ever saying so.
const CommandTTLSetting = "command_ttl_seconds"

// Common reasons. Written down because they are the ones a module author will search for.
const (
	ReasonUnknownToken = "this token is not one this instance knows"
	ReasonTokenEnded   = "this token ended"
	ReasonTokenRevoked = "this token has been cancelled"
	ReasonUnboundToken = "this token has not authenticated a module yet"
	ReasonBadToken     = "this message carried no usable token"
)

// Envelope returns the message as it should go on the wire.
//
// Goes through here rather than being marshalled at each call site, because a body that is
// encoded by hand is a body whose errors are read by hand too, and the first question about a
// malformed frame should be answered by the same code in both directions.
func (m Message) Envelope() ([]byte, error) {
	encoded, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("encode a %q message: %w", m.Kind, err)
	}
	return encoded, nil
}

// Decode reads a message off the wire.
//
// A frame that will not parse is an error rather than something to skip: the connection is a
// sequence of messages with nothing to resynchronise on, so a frame that is not a message means
// the two ends disagree about the protocol, and continuing would mean guessing which of them is
// wrong.
func Decode(data []byte) (Message, error) {
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, fmt.Errorf("this is not a message: %w", err)
	}
	if message.Kind == "" {
		return Message{}, fmt.Errorf("a message with no kind in it")
	}
	return message, nil
}

// PayloadInto reads the payload of a message of a kind this build knows about.
//
// A payload that will not unmarshal is an error naming both sides, because the usual cause is
// that one of them is older than the other and neither is wrong — and a message saying only
// "decode failed" is what makes that take an afternoon instead of a minute.
func (m Message) PayloadInto(what any) error {
	if len(m.Payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(m.Payload, what); err != nil {
		return fmt.Errorf("this core sends a %q payload this module does not understand: %w", m.Kind, err)
	}
	return nil
}
