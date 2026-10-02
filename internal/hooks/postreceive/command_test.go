package postreceive

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/gitx"
)

// newTestRepo creates a bare repository with two commits and returns its path.
func newTestRepo(t *testing.T) (git *gitx.Git, repoPath, head string) {
	t.Helper()

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", work, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}

	// Build history in a normal checkout, then reuse it as a bare repo: the
	// quickest way to get real objects without touching a working tree later.
	for i := 0; i < 2; i++ {
		cmd := exec.Command("git", "-C", work, "commit", "--allow-empty", "-m", "commit")
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Tester", "GIT_AUTHOR_EMAIL=tester@example.com",
			"GIT_COMMITTER_NAME=Tester", "GIT_COMMITTER_EMAIL=tester@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit: %v\n%s", err, out)
		}
	}

	repoPath = filepath.Join(dir, "repo.git")
	if out, err := exec.Command("git", "clone", "--bare", work, repoPath).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}

	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return gitx.New(gitx.Options{}), repoPath, strings.TrimSpace(string(out))
}

func TestNewCommitsListsReachableCommits(t *testing.T) {
	ctx := context.Background()
	git, repoPath, head := newTestRepo(t)

	shas := newCommits(ctx, git, repoPath, head, "")
	if len(shas) != 2 {
		t.Fatalf("expected 2 commits for a new ref, got %d (%v)", len(shas), shas)
	}
	if shas[0] != head {
		t.Errorf("expected the newest commit first, got %s want %s", shas[0], head)
	}
}

func TestNewCommitsExcludesOldHistory(t *testing.T) {
	ctx := context.Background()
	git, repoPath, head := newTestRepo(t)

	// The parent is the only older commit; excluding it must leave one entry.
	parent := shasParent(t, repoPath)
	shas := newCommits(ctx, git, repoPath, head, parent)
	if len(shas) != 1 || shas[0] != head {
		t.Fatalf("expected only %s, got %v", head, shas)
	}
}

func shasParent(t *testing.T, repoPath string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", "HEAD~1").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestSnapshotCommitsReadsMetadata(t *testing.T) {
	ctx := context.Background()
	git, repoPath, head := newTestRepo(t)

	snaps, err := git.SnapshotCommits(ctx, repoPath, newCommits(ctx, git, repoPath, head, ""))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	if snaps[0].AuthorName != "Tester" || snaps[0].AuthorEmail != "tester@example.com" {
		t.Errorf("unexpected author: %q <%q>", snaps[0].AuthorName, snaps[0].AuthorEmail)
	}
	if !strings.Contains(snaps[0].Message, "commit") {
		t.Errorf("unexpected message: %q", snaps[0].Message)
	}
	if snaps[0].Timestamp.IsZero() {
		t.Error("expected a parsed timestamp")
	}
}
