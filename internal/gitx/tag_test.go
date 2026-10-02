package gitx

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// A tag has to be attributed to the person who asked for it, and git refuses to
// guess: without an explicit identity the command fails outright, so tag
// creation was impossible on a server with no global git configuration.
func TestCreateTagUsesTheGivenTagger(t *testing.T) {
	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "tagged.git")

	git := New(Options{})
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	_, tree, err := git.WriteBlobAndTree(ctx, repo, "", "README.md", []byte("hi\n"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := git.CommitTree(ctx, repo, CommitTreeOptions{
		Tree: tree, Message: "Initial commit",
		AuthorName: "Seed", AuthorEmail: "seed@example.test",
		CommitterName: "Seed", CommitterEmail: "seed@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateRef(ctx, repo, "refs/heads/main", commit, ""); err != nil {
		t.Fatal(err)
	}

	tagger := CommitIdentity{Name: "alice", Email: "alice@example.test"}
	if err := git.CreateTag(ctx, repo, "v1.0.0", commit, "the first release", tagger); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	out, err := git.run(ctx, repo, nil, "for-each-ref", "refs/tags/v1.0.0",
		"--format=%(taggername)|%(taggeremail)|%(contents:subject)")
	if err != nil {
		t.Fatalf("read the tag: %v", err)
	}

	got := strings.TrimSpace(string(out))
	// git renders the tagger email as <addr>; the angle brackets are git's, not
	// part of the address.
	want := "alice|<alice@example.test>|the first release"
	if got != want {
		t.Errorf("tag = %q, want %q", got, want)
	}

	// An existing tag must not be moved silently: a release name that follows a
	// commit is a promise, and re-pointing it makes history unreadable.
	err = git.CreateTag(ctx, repo, "v1.0.0", commit, "again", tagger)
	if err == nil {
		t.Error("creating the same tag twice succeeded, want a refusal")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention the tag already exists", err)
	}
}
