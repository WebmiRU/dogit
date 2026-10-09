package modulechan

import (
	"encoding/json"
	"testing"
)

// A payload that is already JSON goes on the wire as itself.
//
// Both ends of the protocol build their payloads and hand over the finished bytes, and encoding
// a []byte produces a base64 string inside a JSON string. The far end is then asked to read an
// object and finds a string, and reports it in terms it can see: "this is not a question I can
// read", and on the way back "cannot unmarshal string into Go value of type struct".
//
// Both of those are true. Neither says that the caller built the right thing and lost it in
// transit, and a reader of either has no way to get back to the mistake. Here it cost a
// deployment: the core asked a registry what a tag pointed at, was refused, logged a line about
// a tag it could not resolve, and applied the image by name — so the manifest was identical
// between two runs of two different commits, nothing rolled out, and the page said every pod was
// running the new image.
func TestBytesAlreadyEncodedAreNotEncodedAgain(t *testing.T) {
	built, err := json.Marshal(map[string]string{"digest": "sha256:abc"})
	if err != nil {
		t.Fatalf("build the payload: %v", err)
	}

	sent, err := Payload(built)
	if err != nil {
		t.Fatalf("Payload: %v", err)
	}
	if string(sent) != string(built) {
		t.Fatalf("the payload changed on the way out:\n got %s\nwant %s", sent, built)
	}

	// The question a module actually asks of it.
	var got struct {
		Digest string `json:"digest"`
	}
	if err := json.Unmarshal(sent, &got); err != nil {
		t.Fatalf("the far end could not read the payload: %v", err)
	}
	if got.Digest != "sha256:abc" {
		t.Errorf("arrived as %#v", got)
	}
}

// Every form callers use, and none of them a mistake: a value is encoded, bytes and raw messages
// are not, and a command with no payload sends none rather than an empty string.
func TestEveryPayloadFormGoesOutReadable(t *testing.T) {
	for name, payload := range map[string]any{
		"a value":     map[string]string{"digest": "sha256:abc"},
		"raw message": json.RawMessage(`{"digest":"sha256:abc"}`),
		"bytes":       []byte(`{"digest":"sha256:abc"}`),
		"nothing":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			sent, err := Payload(payload)
			if err != nil {
				t.Fatalf("Payload: %v", err)
			}
			if name == "nothing" {
				// An empty payload and a payload of "null" are different: the first is a
				// command with nothing in it, the second is a command carrying a null.
				if len(sent) != 0 {
					t.Errorf("a command with no payload sent %s", sent)
				}
				return
			}
			var got struct {
				Digest string `json:"digest"`
			}
			if err := json.Unmarshal(sent, &got); err != nil {
				t.Fatalf("unreadable: %s", sent)
			}
			if got.Digest != "sha256:abc" {
				t.Errorf("arrived as %#v", got)
			}
		})
	}
}
