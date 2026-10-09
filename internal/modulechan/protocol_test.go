package modulechan

import (
	"strings"
	"testing"
)

// A frame that is not a message ends the connection rather than being skipped.
//
// There is nothing to resynchronise on: a channel is a sequence of messages with no framing of
// its own, so a frame that does not parse means the two ends disagree about what this is. Skipping
// it and carrying on would mean guessing which end is right, and a guess in a protocol is how a
// core and a module end up each convinced the other is broken.
func TestAFrameThatIsNotAMessageIsAnError(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{"not JSON at all", "hello"},
		{"JSON of the wrong shape", `["work.available"]`},
		// The one that would be easiest to let through: valid JSON, valid envelope, and
		// nothing in it that says what it is.
		{"a message with no kind", `{"token":"glpat-x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Decode([]byte(c.frame)); err == nil {
				t.Fatalf("%s was accepted as a message", c.name)
			}
		})
	}
}

// A token is in every frame, not in a handshake.
//
// This is the property that makes a reconnect complete rather than a negotiation, and it is
// enforced here by the shape of the type: there is no field for a session, and no field that
// would hold one.
func TestEveryMessageCarriesAToken(t *testing.T) {
	frame, err := Message{Kind: WorkAvailable, Token: "glpat-x"}.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(frame), `"token":"glpat-x"`) {
		t.Fatalf("a message went out without its token: %s", frame)
	}

	back, err := Decode(frame)
	if err != nil {
		t.Fatal(err)
	}
	if back.Token != "glpat-x" {
		t.Fatalf("the token did not survive the round trip: %q", back.Token)
	}
}

// A payload the other end cannot read says which kind it was, because the usual cause is one of
// the two being older than the other and neither being wrong.
func TestAPayloadThatWillNotUnmarshalNamesTheKind(t *testing.T) {
	message := Message{Kind: WorkAvailable, Token: "glpat-x", Payload: []byte(`{"waiting":"lots"}`)}

	var payload struct {
		Waiting int `json:"waiting"`
	}
	err := message.PayloadInto(&payload)
	if err == nil {
		t.Fatal("a payload that does not fit was accepted")
	}
	if !strings.Contains(err.Error(), WorkAvailable) {
		t.Fatalf("the error does not name the kind, so it cannot be matched against a manifest: %v", err)
	}
}

// An absent payload is not a failure. A notification carries none, and a message with an empty
// body is the normal case rather than something a module got wrong.
func TestAMessageWithNoPayloadIsNotAnError(t *testing.T) {
	var payload struct {
		Waiting int `json:"waiting"`
	}
	if err := (Message{Kind: WorkAvailable, Token: "glpat-x"}).PayloadInto(&payload); err != nil {
		t.Fatalf("a message with no payload was treated as broken: %v", err)
	}
	if payload.Waiting != 0 {
		t.Fatal("an absent payload produced a value")
	}
}
