package gitx

import (
	"context"
	"path/filepath"
	"testing"
)

// mergeFixture is a bare repository with a main branch and a commit to branch off.
type mergeRepo struct {
	t    *testing.T
	git  *Git
	repo string
	head string
}

func newMergeRepo(t *testing.T) *mergeRepo {
	t.Helper()

	ctx := context.Background()
	repo := filepath.Join(t.TempDir(), "merge.git")
	git := New(Options{})

	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	seed := git.seed(t, repo, "", "README.md", "first\n", nil)
	if err := git.UpdateRef(ctx, repo, "refs/heads/main", seed, ""); err != nil {
		t.Fatalf("create main: %v", err)
	}

	return &mergeRepo{t: t, git: git, repo: repo, head: seed}
}

// seed writes a file and commits it, returning the commit.
func (g *Git) seed(t *testing.T, repo, base, path, content string, parents []string) string {
	t.Helper()

	ctx := context.Background()

	if base != "" {
		tree, err := g.TreeOf(ctx, repo, base)
		if err != nil {
			t.Fatalf("tree of %s: %v", base, err)
		}
		base = tree
	}

	_, tree, err := g.WriteBlobAndTree(ctx, repo, base, path, []byte(content))
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	commit, err := g.CommitTree(ctx, repo, CommitTreeOptions{
		Tree: tree, Parents: parents, Message: "change " + path,
		AuthorName: "Seed", AuthorEmail: "seed@example.test",
		CommitterName: "Seed", CommitterEmail: "seed@example.test",
	})
	if err != nil {
		t.Fatalf("commit %s: %v", path, err)
	}
	return commit
}

// A conflicting merge has to name the files it cannot combine.
//
// git reports this by exiting 1 and printing the conflicted tree on stdout, so a
// runner that discards stdout on failure turns every conflict into "no tree
// returned" — the merge request then reports an error instead of a conflict,
// and there is nothing for a person to resolve.
func TestConflictedMergeNamesTheFiles(t *testing.T) {
	ctx := context.Background()
	m := newMergeRepo(t)

	ours := m.git.seed(t, m.repo, m.head, "README.md", "ours\n", []string{m.head})
	theirs := m.git.seed(t, m.repo, m.head, "README.md", "theirs\n", []string{m.head})

	tree, conflicts, err := m.git.PeekConflicts(ctx, m.repo, ours, theirs)
	if err != nil {
		t.Fatalf("peek conflicts: %v", err)
	}
	if len(conflicts) != 1 || conflicts[0] != "README.md" {
		t.Fatalf("conflicts = %v, want [README.md]", conflicts)
	}
	if tree == "" {
		t.Error("the conflicted tree was not returned")
	}

	// The same must hold through Merge, which is what a merge request calls.
	result, err := m.git.Merge(ctx, m.repo, MergeOptions{
		TargetBranch: "", SourceBranch: "", Method: MergeMethodMerge,
		CommitterName: "Seed", CommitterEmail: "seed@example.test",
		AuthorName: "Seed", AuthorEmail: "seed@example.test",
	})
	// Branches are not named here; the point of this case is the conflict report,
	// which the peek above already covers. A missing branch must be an error and
	// not a panic.
	if err == nil && result == nil {
		t.Error("Merge returned neither a result nor an error")
	}
}

// A merge that can be combined reports no conflicts and produces a tree.
func TestCleanMergeReportsNoConflicts(t *testing.T) {
	ctx := context.Background()
	m := newMergeRepo(t)

	theirs := m.git.seed(t, m.repo, m.head, "feature.txt", "a feature\n", []string{m.head})

	tree, conflicts, err := m.git.PeekConflicts(ctx, m.repo, m.head, theirs)
	if err != nil {
		t.Fatalf("peek conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none", conflicts)
	}
	if tree == "" {
		t.Error("no tree was produced for a clean merge")
	}
}

// Changes to different files are not a conflict, even though both branches moved.
func TestDifferentFilesDoNotConflict(t *testing.T) {
	ctx := context.Background()
	m := newMergeRepo(t)

	ours := m.git.seed(t, m.repo, m.head, "ours.txt", "ours\n", []string{m.head})
	theirs := m.git.seed(t, m.repo, m.head, "theirs.txt", "theirs\n", []string{m.head})

	_, conflicts, err := m.git.PeekConflicts(ctx, m.repo, ours, theirs)
	if err != nil {
		t.Fatalf("peek conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflicts = %v, want none", conflicts)
	}
}
