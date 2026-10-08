package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/secrets"
)

// A key of the right length, made rather than written out: a key of 31 bytes is refused by
// secrets.New, and a test that fails because somebody counted wrong says nothing about
// sealing.
func testKey(fill byte) string {
	return strings.Repeat(fmt.Sprintf("%02x", fill), 32)
}

// A store with nothing behind it: no key at all, which is what an instance configured
// without DOGIT_SECRET_KEY gives.
func keylessStore() *Store { return &Store{} }

func TestAStoreWithoutAKeyHandsBackAPlainValue(t *testing.T) {
	plain := json.RawMessage(`{"registry":"registry.f220.ru"}`)

	got, err := keylessStore().Integrations().open(plain)
	if err != nil {
		t.Fatalf("a value that is not a credential must read on an instance with no key: %v", err)
	}
	if string(got) != string(plain) {
		t.Fatalf("a plain value came back changed: %s", got)
	}
}

func TestAStoreWithoutAKeyRefusesASealedValue(t *testing.T) {
	sealer, err := secrets.New("abababababababababababababababababababababababababababababababab")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealed, err := sealer.Seal([]byte(`"hunter2"`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Handing back the ciphertext as though it were a password is the failure worth naming:
	// a module would take those bytes for a password and fail somewhere else entirely.
	if _, err := keylessStore().Integrations().open(sealed); err == nil {
		t.Fatal("a sealed value read on an instance with no key, as though the key did not matter")
	}
}

func TestAStoreWithAKeyReadsBoth(t *testing.T) {
	sealer, err := secrets.New(testKey(0xcd))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	sealed, err := sealer.Seal([]byte(`"hunter2"`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	s := &Store{sealer: sealer}
	got, err := s.Integrations().open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if string(got) != `"hunter2"` {
		t.Fatalf("the credential did not come back: %s", got)
	}

	plain := json.RawMessage(`4`)
	got, err = s.Integrations().open(plain)
	if err != nil || string(got) != "4" {
		t.Fatalf("a plain value must pass through a store that has a key: %v %s", err, got)
	}
}

func TestSealedValueIsRecognisedWithoutTheKey(t *testing.T) {
	if !sealedValue(json.RawMessage(`{"__sealed":"v1","box":"AAAA"}`)) {
		t.Fatal("a sealed value was not recognised")
	}
	for _, plain := range []string{`{"a":1}`, `[1,2]`, `"text"`, `null`, `{}`} {
		if sealedValue(json.RawMessage(plain)) {
			t.Fatalf("a plain value was taken for a sealed one: %s", plain)
		}
	}
}
