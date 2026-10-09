package app

import (
	"encoding/pem"
	"fmt"
	"os"

	"crypto/ed25519"
	"crypto/rand"

	"golang.org/x/crypto/ssh"
)

// generateEd25519Key writes a new PEM-encoded ed25519 host key.
func generateEd25519Key(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate host key: %w", err)
	}
	der, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("marshal host key: %w", err)
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("write host key: %w", err)
	}
	defer f.Close()

	block := &pem.Block{Type: der.Type, Bytes: der.Bytes}
	if err := pem.Encode(f, block); err != nil {
		return fmt.Errorf("encode host key: %w", err)
	}
	return nil
}
