package secrets

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// A key of the right length, in both forms, and a wrong one.
func aKey() string { return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)) }

func TestSealAndOpen(t *testing.T) {
	s, err := New(aKey())
	if err != nil {
		t.Fatalf("a key of 32 bytes must be accepted: %v", err)
	}
	if !s.Configured() {
		t.Fatal("a Sealer made from a key must say it is configured")
	}

	want := []byte(`{"password":"hunter2"}`)
	sealed, err := s.Seal(want)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed, want) {
		t.Fatalf("the sealed value carries the plain one: %s", sealed)
	}

	got, err := s.Open(sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("round trip changed the value: got %s want %s", got, want)
	}
}

func TestTwoValuesOfTheSameTextDiffer(t *testing.T) {
	s, _ := New(aKey())
	one, err := s.Seal([]byte("same password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	two, err := s.Seal([]byte("same password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Equal(one, two) {
		t.Fatal("two seals of the same text are the same bytes, so a reader can tell that they are equal")
	}
}

func TestOpenHandsBackAValueThatWasNeverSealed(t *testing.T) {
	s, _ := New(aKey())
	plain := []byte(`{"host":"db.example.com","port":5432}`)

	got, err := s.Open(plain)
	if err != nil {
		t.Fatalf("a plain value must come back without complaint: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatalf("a plain value came back changed: %s", got)
	}
}

func TestOpenRefusesWithTheWrongKey(t *testing.T) {
	mine, _ := New(aKey())
	sealed, err := mine.Seal([]byte("a password"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	other, _ := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if _, err := other.Open(sealed); err == nil {
		t.Fatal("a value opened with a key that is not its own, which means the tag is not being checked")
	}
}

func TestTamperingIsNoticed(t *testing.T) {
	s, _ := New(aKey())
	sealed, err := s.Seal([]byte(`{"password":"hunter2"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	var wrapper envelope
	if err := json.Unmarshal(sealed, &wrapper); err != nil {
		t.Fatalf("the sealed value is not the shape this wrote: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(wrapper.Box)
	raw[len(raw)-1] ^= 0xff

	wrapper.Box = base64.StdEncoding.EncodeToString(raw)
	changed, _ := json.Marshal(wrapper)
	if _, err := s.Open(changed); err == nil {
		t.Fatal("a changed ciphertext opened, which is what a tag is there to prevent")
	}
}

func TestNoKeyRefusesRatherThanWritingPlain(t *testing.T) {
	var s *Sealer
	if s.Configured() {
		t.Fatal("no sealer must say it is not configured")
	}
	if _, err := s.Seal([]byte("secret")); !errors.Is(err, ErrNoKey) {
		t.Fatalf("sealing without a key must say so, got %v", err)
	}
	if _, err := New(""); !errors.Is(err, ErrNoKey) {
		t.Fatalf("an empty key must be refused as no key at all, got %v", err)
	}
}

func TestSealWithoutAKeyIsRefused(t *testing.T) {
	// A Sealer exists but has nothing behind it, which is what a misconfigured key gives.
	var s Sealer
	if _, err := s.Seal([]byte("secret")); !errors.Is(err, ErrNoKey) {
		t.Fatalf("sealing without a key must refuse, got %v", err)
	}
	if _, err := s.Open([]byte(`{"__sealed":"v1","box":"AAAA"}`)); !errors.Is(err, ErrNoKey) {
		t.Fatalf("opening a sealed value without a key must refuse, got %v", err)
	}
}

func TestKeyMustBeTheRightLength(t *testing.T) {
	for _, key := range []string{
		base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16)),
		strings.Repeat("z", 44),
	} {
		if _, err := New(key); err == nil {
			t.Fatalf("a key of the wrong length was accepted: %q", key)
		}
	}
}

func TestKeyIsAcceptedAsHex(t *testing.T) {
	if _, err := New(strings.Repeat("ab", 32)); err != nil {
		t.Fatalf("a 32-byte hex key must be accepted: %v", err)
	}
}

func TestAnotherVersionIsNamedRatherThanMangled(t *testing.T) {
	s, _ := New(aKey())
	other, _ := json.Marshal(envelope{Sealed: "v7", Box: "AAAA"})
	_, err := s.Open(other)
	if err == nil || !strings.Contains(err.Error(), "v7") {
		t.Fatalf("a value from another version must name that version, got %v", err)
	}
}
