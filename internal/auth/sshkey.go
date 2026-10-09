package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHKey is a parsed authorized-keys entry.
type SSHKey struct {
	Type        ssh.PublicKey
	Fingerprint string
	Comment     string
	PEM         string // normalised "type base64 comment" form for storage
}

// ParsePublicKey parses an OpenSSH public key line. It accepts the three common
// formats: authorized_keys ("ssh-rsa AAAA... comment"), RFC 4716 ("---- BEGIN
// SSH2 PUBLIC KEY ----") and the plain "<type> <base64>" form.
func ParsePublicKey(input string) (*SSHKey, error) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return nil, fmt.Errorf("empty public key")
	}

	var (
		key     ssh.PublicKey
		comment string
	)

	switch {
	case strings.HasPrefix(trimmed, "---- BEGIN SSH2 PUBLIC KEY ----"):
		// RFC 4716: convert to OpenSSH format first.
		block, rest, _ := strings.Cut(trimmed, "---- END SSH2 PUBLIC KEY ----")
		body := strings.Join(strings.Split(block, "\n")[1:], "")
		converted, err := convertRFC4716(strings.TrimSpace(body))
		if err != nil {
			return nil, err
		}
		_ = rest
		key, comment, _, _, err = ssh.ParseAuthorizedKey([]byte(converted))
		if err != nil {
			return nil, fmt.Errorf("parse rfc4716 key: %w", err)
		}

	case strings.Contains(trimmed, " "):
		var err error
		key, comment, _, _, err = ssh.ParseAuthorizedKey([]byte(trimmed))
		if err != nil {
			return nil, fmt.Errorf("parse public key: %w", err)
		}

	default:
		return nil, fmt.Errorf("unrecognised public key format")
	}

	return &SSHKey{
		Type:        key,
		Fingerprint: ssh.FingerprintSHA256(key),
		Comment:     comment,
		PEM:         strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))),
	}, nil
}

// convertRFC4716 turns base64 RFC 4716 body into "ssh-rsa AAAA... comment".
func convertRFC4716(body string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(body)
	if err != nil {
		return "", fmt.Errorf("decode rfc4716 body: %w", err)
	}
	key, err := ssh.ParsePublicKey(decoded)
	if err != nil {
		return "", fmt.Errorf("parse rfc4716 key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))), nil
}

// FingerprintBytes returns the SHA256 fingerprint of raw key bytes, used when
// matching a key presented during the SSH handshake against stored rows.
func FingerprintBytes(key []byte) string {
	sum := sha256.Sum256(key)
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}
