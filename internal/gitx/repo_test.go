package gitx

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newTestRepo creates a bare repository with two commits on "main" plus one tag,
// and returns the handle and the repository path.
func newTestRepo(t *testing.T) (*Git, string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	run("init", "-q", "-b", "main")
	for i := 0; i < 2; i++ {
		name := filepath.Join(work, fmt.Sprintf("file%d.txt", i))
		if err := os.WriteFile(name, []byte("line one\nline two\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		run("add", "-A")
		run("commit", "-m", fmt.Sprintf("commit %d", i))
	}
	run("tag", "v1.0")

	repoPath := filepath.Join(dir, "repo.git")
	if out, err := exec.Command("git", "clone", "--bare", "-q", work, repoPath).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}

	return New(Options{}), repoPath
}

func TestBranchesAndTagsAreListed(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	branches, err := git.Branches(ctx, repoPath)
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if len(branches) != 1 || branches[0].Name != "main" {
		t.Fatalf("got branches %+v, want a single main", branches)
	}
	if len(branches[0].Target) != 40 {
		t.Errorf("branch target %q is not a full object id", branches[0].Target)
	}

	tags, err := git.Tags(ctx, repoPath)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 1 || tags[0].Name != "v1.0" {
		t.Fatalf("got tags %+v, want a single v1.0", tags)
	}
	if tags[0].Target != branches[0].Target {
		t.Errorf("tag points at %s but branch points at %s", tags[0].Target, branches[0].Target)
	}
}

func TestRevParseAndExists(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	sha, err := git.RevParse(ctx, repoPath, "main")
	if err != nil {
		t.Fatalf("rev-parse main: %v", err)
	}
	if !IsHexSHA(sha) {
		t.Errorf("got %q, want a full object id", sha)
	}

	// A tag must resolve to the same commit as the branch it marks.
	viaTag, err := git.RevParse(ctx, repoPath, "v1.0")
	if err != nil {
		t.Fatalf("rev-parse v1.0: %v", err)
	}
	if viaTag != sha {
		t.Errorf("tag resolved to %s, branch to %s", viaTag, sha)
	}

	if !git.Exists(ctx, repoPath, "main") {
		t.Error("main should exist")
	}
	if git.Exists(ctx, repoPath, "no-such-branch") {
		t.Error("a missing branch must not be reported as existing")
	}
	if _, err := git.RevParse(ctx, repoPath, "no-such-branch"); err == nil {
		t.Error("rev-parse must fail for a missing revision")
	}
}

func TestListTreeAndCatFile(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	entries, err := git.ListTree(ctx, repoPath, "main", "", false)
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
	}
	if entries[0].Path != "file0.txt" || entries[0].Type != "blob" {
		t.Errorf("unexpected first entry: %+v", entries[0])
	}

	content, size, binary, err := git.CatFile(ctx, repoPath, "main", "file1.txt")
	if err != nil {
		t.Fatalf("cat file: %v", err)
	}
	if binary {
		t.Error("text content reported as binary")
	}
	if int64(len(content)) != size {
		t.Errorf("size %d does not match %d bytes of content", size, len(content))
	}
	if !strings.Contains(string(content), "line one") {
		t.Errorf("unexpected content: %q", content)
	}

	if _, _, _, err := git.CatFile(ctx, repoPath, "main", "missing.txt"); err == nil {
		t.Error("reading a missing file must fail")
	}
}

func TestListTreeInsideDirectory(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	// Build a subdirectory so the tree navigation has something to descend into.
	dir := filepath.Join(filepath.Dir(repoPath), "work", "pkg")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lib.txt"), []byte("lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(filepath.Dir(repoPath), "work")
	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-m", "add pkg"},
	} {
		cmd := exec.Command("git", append([]string{"-C", work}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("git", "-C", work, "push", "-q", repoPath, "main").CombinedOutput(); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}

	// Listing the directory itself must yield its children, not the directory.
	entries, err := git.ListTree(ctx, repoPath, "main", "pkg", false)
	if err != nil {
		t.Fatalf("list tree: %v", err)
	}
	if len(entries) != 1 || entries[0].Path != "lib.txt" {
		t.Fatalf("got %+v, want a single lib.txt entry", entries)
	}

	// A recursive listing keeps the full path from the root.
	all, err := git.ListTree(ctx, repoPath, "main", "", true)
	if err != nil {
		t.Fatalf("list tree recursive: %v", err)
	}
	var found bool
	for _, entry := range all {
		if entry.Path == "pkg/lib.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("recursive listing does not contain pkg/lib.txt: %+v", all)
	}
}

func TestLogReturnsNewestFirst(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	commits, err := git.Log(ctx, repoPath, "main", 10, 0)
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2", len(commits))
	}
	if commits[0].Subject != "commit 1" {
		t.Errorf("newest commit is %q, want %q", commits[0].Subject, "commit 1")
	}
	if len(commits[0].Parents) != 1 {
		t.Errorf("got %d parents, want 1", len(commits[0].Parents))
	}
	if commits[0].AuthorName != "Tester" {
		t.Errorf("author is %q, want Tester", commits[0].AuthorName)
	}
	if commits[0].Timestamp.IsZero() {
		t.Error("timestamp was not parsed")
	}

	// Paging must not repeat commits.
	page2, err := git.Log(ctx, repoPath, "main", 1, 1)
	if err != nil {
		t.Fatalf("log page 2: %v", err)
	}
	if len(page2) != 1 || page2[0].SHA == commits[0].SHA {
		t.Errorf("second page returned the wrong commit: %+v", page2)
	}

	count, err := git.CommitCount(ctx, repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("commit count %d, want 2", count)
	}
}

func TestBranchLifecycle(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	sha, err := git.RevParse(ctx, repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}

	if err := git.CreateBranch(ctx, repoPath, "feature/login", sha); err != nil {
		t.Fatalf("create branch: %v", err)
	}
	feature, err := git.RevParse(ctx, repoPath, "feature/login")
	if err != nil {
		t.Fatalf("resolve new branch: %v", err)
	}
	if feature != sha {
		t.Error("new branch points elsewhere")
	}

	if err := git.DeleteRef(ctx, repoPath, "refs/heads/feature/login"); err != nil {
		t.Fatalf("delete branch: %v", err)
	}
	if git.Exists(ctx, repoPath, "feature/login") {
		t.Error("branch still exists after deletion")
	}

	// Invalid names are rejected before git is invoked.
	if err := git.CreateBranch(ctx, repoPath, "../escape", sha); err == nil {
		t.Error("expected a traversal branch name to be rejected")
	}
}

func TestUpdateRefGuardsAgainstLostUpdates(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	oldSHA, err := git.RevParse(ctx, repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	first, err := git.RevParse(ctx, repoPath, "main~1")
	if err != nil {
		t.Fatal(err)
	}

	// A conditional update against the current value succeeds.
	if err := git.UpdateRef(ctx, repoPath, "refs/heads/main", first, oldSHA); err != nil {
		t.Fatalf("conditional update: %v", err)
	}

	// Replaying the same update must fail: this is what stops two merges from
	// silently overwriting each other.
	err = git.UpdateRef(ctx, repoPath, "refs/heads/main", oldSHA, oldSHA)
	if err == nil {
		t.Fatal("expected the stale conditional update to fail")
	}
}

func TestIsAncestorAndMergeBase(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	newest, err := git.RevParse(ctx, repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	oldest, err := git.RevParse(ctx, repoPath, "main~1")
	if err != nil {
		t.Fatal(err)
	}

	forward, err := git.IsAncestor(ctx, repoPath, oldest, newest)
	if err != nil {
		t.Fatal(err)
	}
	if !forward {
		t.Error("the older commit should be an ancestor of the newer one")
	}

	backward, err := git.IsAncestor(ctx, repoPath, newest, oldest)
	if err != nil {
		t.Fatal(err)
	}
	if backward {
		t.Error("the newer commit must not be an ancestor of the older one")
	}

	base, err := git.MergeBase(ctx, repoPath, newest, oldest)
	if err != nil {
		t.Fatal(err)
	}
	if base != oldest {
		t.Errorf("merge base is %s, want %s", base, oldest)
	}
}

func TestDiffProducesFileChanges(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	newest, err := git.RevParse(ctx, repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	oldest, err := git.RevParse(ctx, repoPath, "main~1")
	if err != nil {
		t.Fatal(err)
	}

	changes, err := git.Diff(ctx, repoPath, oldest, newest, DiffOptions{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if len(changes) != 1 {
		t.Fatalf("got %d changed files, want 1: %+v", len(changes), changes)
	}
	if changes[0].Path != "file1.txt" {
		t.Errorf("changed file is %q, want file1.txt", changes[0].Path)
	}
	if changes[0].Status != "added" {
		t.Errorf("status is %q, want added", changes[0].Status)
	}
	if !strings.Contains(changes[0].Patch, "line one") {
		t.Errorf("patch does not contain the added content: %q", changes[0].Patch)
	}
}

func TestBlameAttributesLines(t *testing.T) {
	ctx := context.Background()
	git, repoPath := newTestRepo(t)

	lines, err := git.Blame(ctx, repoPath, "main", "file0.txt")
	if err != nil {
		t.Fatalf("blame: %v", err)
	}
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for i, line := range lines {
		if line.LineNo != i+1 {
			t.Errorf("line %d numbered %d", i+1, line.LineNo)
		}
		if !IsHexSHA(line.CommitSHA) {
			t.Errorf("line %d has no commit: %q", i+1, line.CommitSHA)
		}
		if line.Content == "" {
			t.Errorf("line %d has no content", i+1)
		}
	}
}

func TestEmptyRepoHasNoCommits(t *testing.T) {
	ctx := context.Background()

	dir := t.TempDir()
	repoPath := filepath.Join(dir, "empty.git")
	git := New(Options{})

	if err := git.InitBare(ctx, repoPath); err != nil {
		t.Fatalf("init: %v", err)
	}
	if !git.IsEmpty(ctx, repoPath) {
		t.Error("a freshly initialised repository should be empty")
	}
	if _, err := git.RevParse(ctx, repoPath, "main"); err == nil {
		t.Error("main must not resolve in an empty repository")
	}
}
