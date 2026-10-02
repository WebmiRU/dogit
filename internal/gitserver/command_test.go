package gitserver

import (
	"path/filepath"
	"testing"
)

func TestParseGitCommandAcceptsClientFormats(t *testing.T) {
	// Git clients differ in quoting and in the flags they append; all of these
	// appear in the wild and must map to the same repository.
	tests := []struct {
		name     string
		command  string
		wantVerb string
		wantPath string
	}{
		{
			name:     "scp-like with quotes",
			command:  "git-receive-pack '/group/project.git'",
			wantVerb: VerbReceivePack,
			wantPath: "/group/project.git",
		},
		{
			name:     "stateless-rpc flag is ignored",
			command:  "git-upload-pack '/group/project.git' --stateless-rpc",
			wantVerb: VerbUploadPack,
			wantPath: "/group/project.git",
		},
		{
			name:     "double quotes",
			command:  `git-upload-pack "hello.git"`,
			wantVerb: VerbUploadPack,
			wantPath: "hello.git",
		},
		{
			name:     "no quotes",
			command:  "git-upload-pack hello.git",
			wantVerb: VerbUploadPack,
			wantPath: "hello.git",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verb, path, err := ParseGitCommand(tc.command)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if verb != tc.wantVerb {
				t.Errorf("verb = %q, want %q", verb, tc.wantVerb)
			}
			if path != tc.wantPath {
				t.Errorf("path = %q, want %q", path, tc.wantPath)
			}
		})
	}
}

func TestParseGitCommandRejectsNonGitCommands(t *testing.T) {
	// A forced command that only runs git means anything else is an attack or a
	// misconfiguration, and must never reach an exec.
	for _, command := range []string{
		"bash",
		"/bin/sh",
		"git-upload-pack /x.git; rm -rf /",
		"git-upload-pack '/x.git$(id)'",
		"git-upload-pack '/../../etc/passwd'",
		"git-upload-pack",
		"",
	} {
		if _, _, err := ParseGitCommand(command); err == nil {
			t.Errorf("expected %q to be rejected", command)
		}
	}
}

func TestResolveRepoStaysInsideRoot(t *testing.T) {
	root := t.TempDir()

	got, err := ResolveRepo(root, "group/project.git")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(root, "group", "project.git")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	for _, bad := range []string{
		"../escape.git",
		"group/../../escape.git",
		"/../escape.git",
		".git/config",
		"group/.hidden/project.git",
		"",
	} {
		if _, err := ResolveRepo(root, bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestAuthorizedKeyLineCarriesUsernameAndRestriction(t *testing.T) {
	line := AuthorizedKeyLine("alice", "ssh-ed25519 AAAA... alice@laptop", "/usr/local/bin/dogit-hook")

	want := `restrict,command="/usr/local/bin/dogit-hook git alice" ssh-ed25519 AAAA... alice@laptop`
	if line != want {
		t.Errorf("got  %q\nwant %q", line, want)
	}
}

func TestNormalizePath(t *testing.T) {
	tests := map[string]string{
		"/group/project.git": "group/project",
		"group/project.git":  "group/project",
		"project":            "project",
		`"project.git"`:      "project",
	}
	for in, want := range tests {
		if got := NormalizePath(in); got != want {
			t.Errorf("NormalizePath(%q) = %q, want %q", in, got, want)
		}
	}
}
