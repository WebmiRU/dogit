package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/modulehost"
)

// A proxy reading a half-written configuration will refuse to start, so the file
// is replaced in one step rather than truncated and filled.
func TestRoutesFileIsReplacedAtomically(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "modules", "modules.conf")

	if err := writeFileAtomically(path, "first\n"); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The directory did not exist before: a fresh deployment has no such path.
	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Fatalf("the directory was not created: %v", err)
	}

	if err := writeFileAtomically(path, "second\n"); err != nil {
		t.Fatalf("rewrite: %v", err)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "second\n" {
		t.Errorf("the file holds %q, want the second write", content)
	}

	// Nothing of the temporary file is left behind for a reload to pick up.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "modules.conf" {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("the directory holds %v, want just the configuration", names)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// The proxy reads it as another user.
	if info.Mode().Perm()&0o044 == 0 {
		t.Errorf("the file is mode %v, unreadable by the proxy", info.Mode().Perm())
	}
}

// The output is an include fragment, not a whole configuration: it has to sit
// inside a file the image already provides, so it must not bring its own
// http or events blocks along.
func TestGeneratedRoutesAreAnIncludeFragment(t *testing.T) {
	rendered := modulehost.NginxServerBlocks([]modulehost.Route{{
		Kind:     "registry:docker",
		Upstream: "http://registry:5000",
		Domains:  []string{"registry.git.example.com"},
	}}, "")

	for _, forbidden := range []string{"http {", "events {", "include "} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("the fragment carries %q, which belongs to the main file:\n%s",
				forbidden, rendered)
		}
	}

	// No routes means no fragment at all, rather than a file of whitespace that a
	// reload reads for nothing.
	if empty := modulehost.NginxServerBlocks(nil, ""); strings.TrimSpace(empty) != "" {
		t.Errorf("an empty route list rendered %q", empty)
	}
}
