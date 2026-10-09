// Package auth handles password hashing, session cookies, personal access
// tokens and SSH public key parsing.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	saltLen      = 16
)

// HashPassword returns an argon2id hash suitable for storage.
func HashPassword(password string) ([]byte, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	// PHC string format: $argon2id$v=19$m=65536,t=3,p=2$<salt>$<key>
	return []byte(fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	)), nil
}

// VerifyPassword reports whether password matches the stored argon2id hash.
func VerifyPassword(password string, hash []byte) bool {
	parts := strings.Split(string(hash), "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	// Each parameter has its own key inside one comma-separated field, so they
	// are read individually: a single Sscanf would stop at the first separator.
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return false
	}

	var memory uint32
	var timeCost uint32
	var threads uint8
	for _, param := range strings.Split(parts[3], ",") {
		key, value, ok := strings.Cut(param, "=")
		if !ok {
			return false
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return false
		}
		switch key {
		case "m":
			memory = uint32(n)
		case "t":
			timeCost = uint32(n)
		case "p":
			threads = uint8(n)
		default:
			return false
		}
	}
	if memory == 0 || timeCost == 0 || threads == 0 {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	got := argon2.IDKey([]byte(password), salt, timeCost, memory, threads, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ErrWeakPassword signals a password that fails the length requirement.
var ErrWeakPassword = errors.New("password must be at least 8 characters")

// ValidatePassword applies the password policy.
func ValidatePassword(password string) error {
	if len([]rune(password)) < 8 {
		return ErrWeakPassword
	}
	return nil
}

// GenerateToken returns a random token string and its SHA-256 hash. Only the
// hash is stored; the plaintext is shown to the user once.
func GenerateToken() (plaintext string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	plaintext = "glpat-" + base64.RawURLEncoding.EncodeToString(buf)
	return plaintext, HashToken(plaintext), nil
}

// HashToken returns the stored representation of a personal access token.
func HashToken(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// ValidScopes filters the requested scopes down to the known set, preserving
// order and removing duplicates.
func ValidScopes(requested []string) []string {
	known := map[string]bool{
		"read_user": true, "read_repository": true, "write_repository": true,
		"read_api": true, "api": true, "ci": true, "manage_tokens": true,
	}
	seen := map[string]bool{}
	out := []string{}
	for _, s := range requested {
		s = strings.TrimSpace(s)
		if known[s] && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// HasScope reports whether the granted scopes satisfy want.
func HasScope(granted []string, want string) bool {
	for _, s := range granted {
		if s == want || s == "api" || (s == "read_api" && strings.HasPrefix(want, "read_")) {
			return true
		}
	}
	return false
}

// RandomID returns a URL-safe random identifier, used for session ids and
// runner tokens.
func RandomID(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
