// Package secrets seals the values this instance must not be readable about: a module's
// registry password, a database's credentials, anything a person typed in and would have to
// type again.
//
// The threat is specific and worth naming, because "we should encrypt" is usually aimed at
// somebody walking off with a disk. Here it is a backup of the database, a replica, an
// `pg_dump` pasted into a ticket, a support engineer with read access to a table they were
// never meant to read. The value has to stop being readable at rest, in the places that keep
// copies, not at the point somebody types it into a form.
//
// How it is sealed:
//
//   - AES-256-GCM, so a reader who learns the key's shape from the ciphertext still cannot
//     write: the tag is checked and a change fails loudly rather than decrypting to
//     nonsense.
//   - A random nonce per value, kept beside the ciphertext. Two modules with the same
//     password produce two different ciphertexts, so a reader who can see both cannot tell
//     that they are the same password, and neither can a pattern over a table of them.
//   - An envelope, not a bare blob. Every value this instance stores is JSON and every one
//     of them is read back by somebody, so what is sealed has to be recognisable without
//     being told: a value that is not an envelope is a value that was never sealed and is
//     handed back as it is.
//
// What this is not: it does not stop somebody who can read the running instance. The key is
// in its memory, and a process that can read the key can read everything it protects. It
// stops a value being readable in a copy of the store, which is where it used to be readable.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

// EnvelopeVersion is what the sealed form says about itself.
//
// A version is in it because a scheme that cannot be changed without losing the values it
// has already written is a scheme that will not be changed, and it should be.
const EnvelopeVersion = "v1"

// ErrNoKey is what a caller is told when there is nothing to seal with.
var ErrNoKey = errors.New("no secret key is configured, so this value cannot be stored safely")

// envelope is the shape a sealed value takes on disk. Short keys, because this is written
// once per value and read on every read of it.
type envelope struct {
	Sealed string `json:"__sealed"`
	Box    string `json:"box"`
}

// Sealer seals and opens. One per instance, holding the key.
//
// The zero value is not usable: a Sealer without a key is a promise rather than a
// capability, and a caller that got one has been handed something that looks like it works.
type Sealer struct {
	aead cipher.AEAD
}

// New makes a Sealer from a key.
//
// The key is 32 bytes, from base64 or hex, and nothing else is accepted: a key of another
// length would be padded or truncated, and both are ways of storing the same secret twice
// and reading one of the copies.
func New(key string) (*Sealer, error) {
	raw, err := decodeKey(key)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("the secret key is not a key this can use: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("prepare the secret key: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Configured says whether there is a key at all.
//
// Asked before offering to store anything secret, so that the answer is "this instance has
// no key, so type it nowhere" rather than a failure after somebody has typed it.
func (s *Sealer) Configured() bool {
	return s != nil && s.aead != nil
}

// Seal encrypts one value.
func (s *Sealer) Seal(plain []byte) ([]byte, error) {
	if !s.Configured() {
		return nil, ErrNoKey
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read a nonce: %w", err)
	}
	box := s.aead.Seal(nonce, nonce, plain, nil)
	wrapper, err := json.Marshal(envelope{
		Sealed: EnvelopeVersion,
		Box:    base64.StdEncoding.EncodeToString(box),
	})
	if err != nil {
		return nil, fmt.Errorf("wrap the sealed value: %w", err)
	}
	return wrapper, nil
}

// Open decrypts a value that is sealed, and hands back a value that is not exactly as it is.
//
// That asymmetry is deliberate and is why this can be called on everything this store hands
// out: a plain value comes back untouched, a sealed one comes back readable, and a caller
// cannot get it wrong by forgetting which it has.
func (s *Sealer) Open(stored []byte) ([]byte, error) {
	var wrapper envelope
	if err := json.Unmarshal(stored, &wrapper); err != nil {
		return stored, nil
	}
	if wrapper.Sealed == "" || wrapper.Box == "" {
		return stored, nil
	}
	if wrapper.Sealed != EnvelopeVersion {
		return nil, fmt.Errorf("this value was sealed by another version (%s), which this one cannot read",
			wrapper.Sealed)
	}
	if !s.Configured() {
		return nil, ErrNoKey
	}
	box, err := base64.StdEncoding.DecodeString(wrapper.Box)
	if err != nil {
		return nil, fmt.Errorf("this sealed value is not something this can read: %w", err)
	}
	size := s.aead.NonceSize()
	if len(box) < size {
		return nil, errors.New("this sealed value is too short to hold what it must")
	}
	plain, err := s.aead.Open(nil, box[:size], box[size:], nil)
	if err != nil {
		return nil, errors.New("this sealed value does not open: the key is not the one it was sealed with")
	}
	return plain, nil
}

// decodeKey reads a key as base64 or as hex, and refuses anything else.
func decodeKey(key string) ([]byte, error) {
	if key == "" {
		return nil, ErrNoKey
	}
	if raw, err := base64.StdEncoding.DecodeString(key); err == nil && len(raw) == 32 {
		return raw, nil
	}
	if raw, err := hexDecode(key); err == nil && len(raw) == 32 {
		return raw, nil
	}
	return nil, fmt.Errorf("the secret key must be 32 bytes, as base64 or as hex; this one is not")
}

func hexDecode(s string) ([]byte, error) {
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi, ok1 := hexNibble(s[i*2])
		lo, ok2 := hexNibble(s[i*2+1])
		if !ok1 || !ok2 {
			return nil, errors.New("not hex")
		}
		out[i] = hi<<4 | lo
	}
	return out, nil
}

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}
