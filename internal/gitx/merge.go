package gitx

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MergeMethod selects how a merge request is integrated.
type MergeMethod string

const (
	MergeMethodMerge  MergeMethod = "merge"  // create a merge commit
	MergeMethodFF     MergeMethod = "ff"     // fast-forward only
	MergeMethodSquash MergeMethod = "squash" // single commit, no second parent
)

// MergeResult describes the outcome of a merge attempt.
type MergeResult struct {
	Method    MergeMethod `json:"method"`
	CommitSHA string      `json:"commit_sha"`
	TreeSHA   string      `json:"tree_sha"`
	// MergeBase is the common ancestor the merge was computed against.
	MergeBase string `json:"merge_base,omitempty"`
	// Conflicts lists files needing manual resolution; non-empty means the
	// merge was not performed.
	Conflicts []string `json:"conflicts"`
}

// ConflictStage holds the three versions of a conflicted file, ready to be
// rendered by the conflict resolver UI.
type ConflictStage struct {
	Path   string `json:"path"`
	Base   string `json:"base"`   // blob OID, stage 1
	Ours   string `json:"ours"`   // blob OID, stage 2
	Theirs string `json:"theirs"` // blob OID, stage 3
	Status string `json:"status"` // both modified, added by us, ...
}

// MergeOptions parameterises Merge.
type MergeOptions struct {
	TargetBranch   string
	SourceBranch   string
	Method         MergeMethod
	Message        string
	AuthorName     string
	AuthorEmail    string
	CommitterName  string
	CommitterEmail string
}

// Merge integrates source into target and returns the resulting commit SHA.
//
// The whole operation uses plumbing commands only:
//  1. `git merge-tree --write-tree` performs the three-way merge in memory and
//     returns the resulting tree, so no index or working tree is touched.
//  2. `git commit-tree` records the commit.
//  3. `git update-ref` moves the branch atomically, optionally asserting the old
//     value so a concurrent push cannot be silently overwritten.
//
// When conflicts remain, the conflicted tree is left in place and the caller
// can fetch the individual stages to build a resolution; it must then call
// CompleteMerge with the resolutions.
func (g *Git) Merge(ctx context.Context, repoPath string, opts MergeOptions) (*MergeResult, error) {
	if !g.SupportsMergeTree(ctx) {
		return nil, fmt.Errorf("git 2.38 or newer is required for merging (found %q)", g.Version(ctx))
	}

	target, err := g.RevParse(ctx, repoPath, opts.TargetBranch)
	if err != nil {
		return nil, fmt.Errorf("resolve target branch %q: %w", opts.TargetBranch, err)
	}
	source, err := g.RevParse(ctx, repoPath, opts.SourceBranch)
	if err != nil {
		return nil, fmt.Errorf("resolve source branch %q: %w", opts.SourceBranch, err)
	}

	result := &MergeResult{Method: opts.Method}

	if target == source {
		result.Method = MergeMethodFF
		result.CommitSHA, result.TreeSHA = target, ""
		return result, nil
	}

	// Fast-forward: the target is already an ancestor of the source.
	if ff, err := g.IsAncestor(ctx, repoPath, target, source); err == nil && ff {
		if opts.Method == MergeMethodMerge || opts.Method == MergeMethodSquash {
			// Explicitly requested a commit, so build a merge commit anyway.
			result.Method = opts.Method
			return g.mergeCommit(ctx, repoPath, opts, target, source, result)
		}
		result.Method = MergeMethodFF
		result.CommitSHA = source
		if err := g.UpdateRef(ctx, repoPath, "refs/heads/"+opts.TargetBranch, source, target); err != nil {
			return nil, fmt.Errorf("fast-forward %s: %w", opts.TargetBranch, err)
		}
		return result, nil
	}

	base, _ := g.MergeBase(ctx, repoPath, target, source)
	result.MergeBase = base

	tree, conflicts, err := g.mergeTree(ctx, repoPath, target, source)
	if err != nil {
		return nil, err
	}
	result.TreeSHA = tree

	if len(conflicts) > 0 {
		result.Conflicts = conflicts
		return result, nil
	}

	// Fast-forward-only refuses to create a merge commit.
	if opts.Method == MergeMethodFF {
		return nil, fmt.Errorf("branch %s is not up to date with %s", opts.TargetBranch, opts.SourceBranch)
	}

	if opts.Method == MergeMethodSquash {
		// Squash drops the source history: the new commit's tree is the merged
		// tree and it has the target as its only parent.
		sha, err := g.CommitTree(ctx, repoPath, CommitTreeOptions{
			Tree:           tree,
			Parents:        []string{target},
			Message:        opts.Message,
			AuthorName:     opts.AuthorName,
			AuthorEmail:    opts.AuthorEmail,
			CommitterName:  opts.CommitterName,
			CommitterEmail: opts.CommitterEmail,
		})
		if err != nil {
			return nil, fmt.Errorf("create squash commit: %w", err)
		}
		if err := g.UpdateRef(ctx, repoPath, "refs/heads/"+opts.TargetBranch, sha, target); err != nil {
			return nil, fmt.Errorf("update %s: %w", opts.TargetBranch, err)
		}
		result.CommitSHA = sha
		return result, nil
	}

	return g.mergeCommit(ctx, repoPath, opts, target, source, result)
}

func (g *Git) mergeCommit(ctx context.Context, repoPath string, opts MergeOptions, target, source string, result *MergeResult) (*MergeResult, error) {
	result.Method = MergeMethodMerge

	tree := result.TreeSHA
	if tree == "" {
		t, conflicts, err := g.mergeTree(ctx, repoPath, target, source)
		if err != nil {
			return nil, err
		}
		if len(conflicts) > 0 {
			result.Conflicts = conflicts
			return result, nil
		}
		tree = t
	}

	sha, err := g.CommitTree(ctx, repoPath, CommitTreeOptions{
		Tree:           tree,
		Parents:        []string{target, source},
		Message:        opts.Message,
		AuthorName:     opts.AuthorName,
		AuthorEmail:    opts.AuthorEmail,
		CommitterName:  opts.CommitterName,
		CommitterEmail: opts.CommitterEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("create merge commit: %w", err)
	}
	if err := g.UpdateRef(ctx, repoPath, "refs/heads/"+opts.TargetBranch, sha, target); err != nil {
		return nil, fmt.Errorf("update %s: %w", opts.TargetBranch, err)
	}
	result.CommitSHA = sha
	return result, nil
}

// mergeTree runs the three-way merge and returns the resulting tree OID plus
// the list of conflicted paths.
func (g *Git) mergeTree(ctx context.Context, repoPath, ours, theirs string) (string, []string, error) {
	args := []string{"merge-tree", "--write-tree", "--name-only", "-z", ours, theirs}
	out, err := g.run(ctx, repoPath, nil, args...)

	// Exit code 1 means "merged with conflicts"; the tree is still printed.
	var ce *CommandError
	if err != nil && !(errorsAs(err, &ce) && ce.ExitCode == 1) {
		return "", nil, fmt.Errorf("merge-tree: %w", err)
	}

	fields := strings.Split(string(out), "\x00")
	if len(fields) == 0 || fields[0] == "" {
		return "", nil, fmt.Errorf("merge-tree returned no tree")
	}
	tree := fields[0]
	conflicts := []string{}
	for _, f := range fields[1:] {
		if strings.TrimSpace(f) != "" {
			conflicts = append(conflicts, f)
		}
	}
	return tree, conflicts, nil
}

// Conflicts returns the stage-1/2/3 blob IDs for every conflicted file in a
// merge-tree result. The stages are read with `git ls-files --stage` against a
// temporary index, which keeps the repository itself untouched.
func (g *Git) Conflicts(ctx context.Context, repoPath, conflictedTree string) ([]ConflictStage, error) {
	idx, cleanup, err := g.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := g.runEnv(ctx, repoPath, env, nil, "read-tree", conflictedTree); err != nil {
		return nil, fmt.Errorf("read conflicted tree: %w", err)
	}

	out, err := g.runEnv(ctx, repoPath, env, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("read conflict stages: %w", err)
	}

	stages := map[string]*ConflictStage{}
	var order []string
	for _, rec := range strings.Split(string(out), "\x00") {
		// "<mode> <oid> <stage>\t<path>"
		meta, path, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 3 {
			continue
		}
		stage := f[2]
		if stage == "0" {
			continue // unconflicted
		}
		cs, ok := stages[path]
		if !ok {
			cs = &ConflictStage{Path: path, Status: "both modified"}
			stages[path] = cs
			order = append(order, path)
		}
		switch stage {
		case "1":
			cs.Base = f[1]
		case "2":
			cs.Ours = f[1]
		case "3":
			cs.Theirs = f[1]
		}
	}

	out2 := make([]ConflictStage, 0, len(order))
	for _, p := range order {
		out2 = append(out2, *stages[p])
	}
	return out2, nil
}

// Resolution maps a conflicted path to its resolved blob OID, or to the stage
// to take verbatim ("base", "ours", "theirs") when the user did not edit.
type Resolution struct {
	Path  string `json:"path"`
	Blob  string `json:"blob,omitempty"`
	Stage string `json:"stage,omitempty"` // base | ours | theirs
}

// CompleteMerge finishes a conflicted merge using the resolutions collected by
// the UI. It rebuilds the tree through a temporary index, then creates the merge
// commit and moves the branch, exactly like the conflict-free path.
func (g *Git) CompleteMerge(ctx context.Context, repoPath string, opts MergeOptions, conflictedTree string, resolutions []Resolution) (*MergeResult, error) {
	idx, cleanup, err := g.tempIndex()
	if err != nil {
		return nil, err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}

	if _, err := g.runEnv(ctx, repoPath, env, nil, "read-tree", conflictedTree); err != nil {
		return nil, fmt.Errorf("read conflicted tree: %w", err)
	}

	for _, r := range resolutions {
		if err := ValidateRepoPath(r.Path); err != nil {
			return nil, err
		}
		blob := r.Blob
		if blob == "" && r.Stage != "" {
			s, err := g.ConflictBlob(ctx, repoPath, conflictedTree, r.Path, r.Stage)
			if err != nil {
				return nil, err
			}
			blob = s
		}
		if !IsHexSHA(blob) {
			return nil, fmt.Errorf("resolution for %q: missing or invalid blob", r.Path)
		}
		// --cacheinfo mode,object,path resolves the unmerged index entry.
		if _, err := g.runEnv(ctx, repoPath, env, nil,
			"update-index", "--add", "--cacheinfo", "100644,"+blob+","+r.Path); err != nil {
			return nil, fmt.Errorf("apply resolution for %q: %w", r.Path, err)
		}
	}

	tree, err := g.runEnv(ctx, repoPath, env, nil, "write-tree")
	if err != nil {
		return nil, fmt.Errorf("write resolved tree: %w", err)
	}
	treeSHA := strings.TrimSpace(string(tree))

	target, err := g.RevParse(ctx, repoPath, opts.TargetBranch)
	if err != nil {
		return nil, err
	}
	source, err := g.RevParse(ctx, repoPath, opts.SourceBranch)
	if err != nil {
		return nil, err
	}

	sha, err := g.CommitTree(ctx, repoPath, CommitTreeOptions{
		Tree:           treeSHA,
		Parents:        []string{target, source},
		Message:        opts.Message,
		AuthorName:     opts.AuthorName,
		AuthorEmail:    opts.AuthorEmail,
		CommitterName:  opts.CommitterName,
		CommitterEmail: opts.CommitterEmail,
	})
	if err != nil {
		return nil, fmt.Errorf("create merge commit: %w", err)
	}
	if err := g.UpdateRef(ctx, repoPath, "refs/heads/"+opts.TargetBranch, sha, target); err != nil {
		return nil, fmt.Errorf("update %s: %w", opts.TargetBranch, err)
	}

	base, _ := g.MergeBase(ctx, repoPath, target, source)
	return &MergeResult{Method: MergeMethodMerge, CommitSHA: sha, TreeSHA: treeSHA, MergeBase: base}, nil
}

// ConflictBlob returns the blob OID of one stage of a conflicted path.
func (g *Git) ConflictBlob(ctx context.Context, repoPath, conflictedTree, path, stage string) (string, error) {
	idx, cleanup, err := g.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}

	if _, err := g.runEnv(ctx, repoPath, env, nil, "read-tree", conflictedTree); err != nil {
		return "", err
	}
	stageNum := map[string]string{"base": "1", "ours": "2", "theirs": "3"}[stage]
	if stageNum == "" {
		return "", fmt.Errorf("unknown conflict stage %q", stage)
	}

	out, err := g.runEnv(ctx, repoPath, env, nil,
		"ls-files", "--stage", "-z", "--", path)
	if err != nil {
		return "", err
	}
	for _, rec := range strings.Split(string(out), "\x00") {
		meta, _, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) == 3 && f[2] == stageNum {
			return f[1], nil
		}
	}
	return "", fmt.Errorf("%w: no %s version of %s", ErrNotFound, stage, path)
}

// tempIndex returns the path of a fresh index file plus a cleanup function.
// Using a scratch index keeps merge resolution completely stateless.
func (g *Git) tempIndex() (string, func(), error) {
	f, err := os.CreateTemp("", "dogit-index-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("create temp index: %w", err)
	}
	path := f.Name()
	// git expects the index not to exist yet.
	_ = f.Close()
	_ = os.Remove(path)

	return path, func() { _ = os.Remove(path) }, nil
}

// MergeFileResult is the three-way merge of a single file's contents,
// computed in memory for the conflict editor.
type MergeFileResult struct {
	Merged       string `json:"merged"`
	HasConflicts bool   `json:"has_conflicts"`
	// ConflictCount is the number of conflict hunks in Merged.
	ConflictCount int `json:"conflict_count"`
}

// MergeFile runs git-merge-file over three blobs to produce the conflicted
// text with standard conflict markers.
func (g *Git) MergeFile(ctx context.Context, base, ours, theirs []byte) (*MergeFileResult, error) {
	dir, err := os.MkdirTemp("", "dogit-merge-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	write := func(name string, data []byte) (string, error) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			return "", err
		}
		return p, nil
	}

	basePath, err := write("base", base)
	if err != nil {
		return nil, err
	}
	oursPath, err := write("ours", ours)
	if err != nil {
		return nil, err
	}
	theirsPath, err := write("theirs", theirs)
	if err != nil {
		return nil, err
	}

	out, err := g.run(ctx, dir, nil,
		"merge-file", "-p", "--diff3", "-L", "ours", "-L", "base", "-L", "theirs",
		oursPath, basePath, theirsPath)

	// git-merge-file exits 1 when conflicts remain and >1 on error.
	var ce *CommandError
	hasConflicts := false
	if err != nil && !(errorsAs(err, &ce) && (ce.ExitCode == 1 || ce.ExitCode == 0)) {
		return nil, fmt.Errorf("merge-file: %w", err)
	} else if err != nil {
		hasConflicts = true
	}

	merged := string(out)
	return &MergeFileResult{
		Merged:        merged,
		HasConflicts:  hasConflicts || strings.Contains(merged, "<<<<<<<"),
		ConflictCount: strings.Count(merged, "<<<<<<<"),
	}, nil
}

// WriteBlobAndTree is used by the web editor: it hashes content as a blob and
// returns a tree that replaces path inside baseTree. An empty baseTree builds a
// tree from scratch containing only that path.
func (g *Git) WriteBlobAndTree(ctx context.Context, repoPath, baseTree, path string, content []byte) (blobSHA, treeSHA string, err error) {
	blobSHA, err = g.HashObject(ctx, repoPath, content, true, path)
	if err != nil {
		return "", "", fmt.Errorf("write blob: %w", err)
	}

	idx, cleanup, err := g.tempIndex()
	if err != nil {
		return "", "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}

	if baseTree != "" {
		if _, err := g.runEnv(ctx, repoPath, env, nil, "read-tree", baseTree); err != nil {
			return "", "", fmt.Errorf("read base tree: %w", err)
		}
	}
	if _, err := g.runEnv(ctx, repoPath, env, nil,
		"update-index", "--add", "--cacheinfo", "100644,"+blobSHA+","+path); err != nil {
		return "", "", fmt.Errorf("stage file: %w", err)
	}
	out, err := g.runEnv(ctx, repoPath, env, nil, "write-tree")
	if err != nil {
		return "", "", fmt.Errorf("write tree: %w", err)
	}
	return blobSHA, strings.TrimSpace(string(out)), nil
}

// RemovePathFromTree returns a tree identical to baseTree without path, which
// is how the editor implements file deletion.
func (g *Git) RemovePathFromTree(ctx context.Context, repoPath, baseTree, path string) (string, error) {
	idx, cleanup, err := g.tempIndex()
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}

	if _, err := g.runEnv(ctx, repoPath, env, nil, "read-tree", baseTree); err != nil {
		return "", fmt.Errorf("read base tree: %w", err)
	}
	if _, err := g.runEnv(ctx, repoPath, env, nil, "update-index", "--force-remove", "--", path); err != nil {
		return "", fmt.Errorf("remove %s: %w", path, err)
	}
	out, err := g.runEnv(ctx, repoPath, env, nil, "write-tree")
	if err != nil {
		return "", fmt.Errorf("write tree: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// TreeOf returns the tree OID of a revision's root.
func (g *Git) TreeOf(ctx context.Context, repoPath, rev string) (string, error) {
	out, err := g.run(ctx, repoPath, nil, "rev-parse", "--verify", rev+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve tree of %q: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Archive streams a repository at rev as a tar.gz. It is used to hand a
// directory of source to the CI runner.
func (g *Git) Archive(ctx context.Context, repoPath, rev string, prefix string) ([]byte, error) {
	args := []string{"archive", "--format=tar"}
	if prefix != "" {
		args = append(args, "--prefix="+strings.TrimSuffix(prefix, "/")+"/")
	}
	args = append(args, rev)
	return g.run(ctx, repoPath, nil, args...)
}

// CommitSnapshot is the hook-facing summary of a single commit, used to fill
// the commits table without re-reading history later.
type CommitSnapshot struct {
	SHA            string    `json:"sha"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	Message        string    `json:"message"`
	Timestamp      time.Time `json:"timestamp"`
}

// SnapshotCommits reads metadata for the given SHAs.
func (g *Git) SnapshotCommits(ctx context.Context, repoPath string, shas []string) ([]CommitSnapshot, error) {
	if len(shas) == 0 {
		return nil, nil
	}
	const format = "%H%x1f%an%x1f%ae%x1f%cn%x1f%ce%x1f%aI%x1f%B%x1e"
	// Each SHA must be its own argument: joining them into a single string makes
	// git read the whole thing as one invalid revision.
	args := []string{"show", "--no-patch", "--format=" + format}
	args = append(args, shas...)

	out, err := g.run(ctx, repoPath, nil, args...)
	if err != nil {
		return nil, err
	}

	snaps := []CommitSnapshot{}
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		// The format above yields exactly seven fields: sha, an, ae, cn, ce, aI, B.
		f := strings.Split(rec, "\x1f")
		if len(f) < 7 {
			continue
		}
		ts, _ := time.Parse(time.RFC3339, f[5])
		snaps = append(snaps, CommitSnapshot{
			SHA: f[0], AuthorName: f[1], AuthorEmail: f[2],
			CommitterName: f[3], CommitterEmail: f[4],
			Message: strings.TrimRight(f[6], "\n"), Timestamp: ts,
		})
	}
	return snaps, nil
}

// JSONStat is a helper for embedding stats into JSON payloads.
func (s *DiffStat) JSON() ([]byte, error) { return json.Marshal(s) }
