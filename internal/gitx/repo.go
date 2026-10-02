package gitx

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TreeEntry is one entry from `git ls-tree`.
type TreeEntry struct {
	Mode string `json:"mode"`
	Type string `json:"type"` // blob, tree, commit (submodule)
	OID  string `json:"oid"`
	Size int64  `json:"size"`
	Path string `json:"path"`
}

// IsDir reports whether the entry is a subdirectory.
func (e TreeEntry) IsDir() bool { return e.Type == "tree" }

// IsSubmodule reports whether the entry is a gitlink.
func (e TreeEntry) IsSubmodule() bool { return e.Type == "commit" }

// Ref is a branch or tag pointing at a commit.
type Ref struct {
	Name   string `json:"name"`
	Short  string `json:"short"`
	Target string `json:"target"`
	Type   string `json:"type"` // branch or tag
}

// CommitInfo is the subset of commit metadata the UI needs.
type CommitInfo struct {
	SHA            string    `json:"sha"`
	ShortSHA       string    `json:"short_sha"`
	Parents        []string  `json:"parents"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	Message        string    `json:"message"`
	Subject        string    `json:"subject"`
	Timestamp      time.Time `json:"timestamp"`
	CommittedAt    time.Time `json:"committed_at"`
}

// DiffStat summarises a change set.
type DiffStat struct {
	FilesChanged int `json:"files_changed"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
}

// RevParse resolves a revision (branch, tag or SHA) to a full object ID.
// Returns ErrNotFound if the revision does not exist.
func (g *Git) RevParse(ctx context.Context, repoPath, rev string) (string, error) {
	out, err := g.run(ctx, repoPath, nil, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("resolve revision %q: %w", rev, ErrNotFound)
	}
	return strings.TrimSpace(string(out)), nil
}

// Exists reports whether a revision resolves in the repository.
func (g *Git) Exists(ctx context.Context, repoPath, rev string) bool {
	_, err := g.RevParse(ctx, repoPath, rev)
	return err == nil
}

// IsEmpty reports whether the repository has no commits at all.
func (g *Git) IsEmpty(ctx context.Context, repoPath string) bool {
	_, err := g.run(ctx, repoPath, nil, "rev-list", "-n", "1", "--all")
	return err != nil
}

// ListTree returns the contents of path at rev. With recursive set, the result
// is a flat list of every blob below path.
func (g *Git) ListTree(ctx context.Context, repoPath, rev, path string, recursive bool) ([]TreeEntry, error) {
	args := []string{"ls-tree", "--long", "-z"}
	if recursive {
		args = append(args, "-r")
	}
	args = append(args, rev)
	if path != "" {
		args = append(args, "--", path)
	}

	out, err := g.run(ctx, repoPath, nil, args...)
	if err != nil {
		return nil, err
	}

	entries := []TreeEntry{}
	for _, rec := range strings.Split(string(out), "\x00") {
		if rec == "" {
			continue
		}
		// Format: "<mode> <type> <oid> <size>\t<path>"
		meta, entryPath, ok := strings.Cut(rec, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) < 4 {
			continue
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64)
		entries = append(entries, TreeEntry{
			Mode: fields[0], Type: fields[1], OID: fields[2],
			Size: size, Path: entryPath,
		})
	}
	return entries, nil
}

// CatFile returns the raw bytes of path at rev, plus its blob size. The bool
// reports whether the content looks binary.
func (g *Git) CatFile(ctx context.Context, repoPath, rev, path string) (content []byte, size int64, binary bool, err error) {
	oid, err := g.RevParseBlob(ctx, repoPath, rev, path)
	if err != nil {
		return nil, 0, false, err
	}
	out, err := g.run(ctx, repoPath, nil, "cat-file", "blob", oid)
	if err != nil {
		return nil, 0, false, err
	}
	return out, int64(len(out)), looksBinary(out), nil
}

// RevParseBlob resolves "rev:path" to a blob object ID.
func (g *Git) RevParseBlob(ctx context.Context, repoPath, rev, path string) (string, error) {
	spec := rev + ":" + path
	out, err := g.run(ctx, repoPath, nil, "rev-parse", "--verify", "--quiet", spec)
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "", fmt.Errorf("%w: %s not found in %s", ErrNotFound, path, rev)
	}
	return strings.TrimSpace(string(out)), nil
}

func looksBinary(b []byte) bool {
	limit := min(len(b), 8000)
	return bytes.IndexByte(b[:limit], 0) >= 0
}

// Log returns commits reachable from rev, newest first.
func (g *Git) Log(ctx context.Context, repoPath, rev string, limit int, skip int) ([]CommitInfo, error) {
	if limit <= 0 {
		limit = 50
	}
	format := "%H%x1f%h%x1f%P%x1f%an%x1f%ae%x1f%cn%x1f%ce%x1f%aI%x1f%cI%x1f%B%x1e"
	out, err := g.run(ctx, repoPath, nil,
		"log", "--format="+format, fmt.Sprintf("--max-count=%d", limit),
		fmt.Sprintf("--skip=%d", skip), rev, "--")
	if err != nil {
		return nil, err
	}

	commits := []CommitInfo{}
	for _, rec := range strings.Split(string(out), "\x1e") {
		rec = strings.TrimLeft(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		f := strings.Split(rec, "\x1f")
		if len(f) < 10 {
			continue
		}
		author, _ := time.Parse(time.RFC3339, f[7])
		committed, _ := time.Parse(time.RFC3339, f[8])
		message := strings.TrimRight(f[9], "\n")
		subject := message
		if i := strings.IndexByte(message, '\n'); i >= 0 {
			subject = message[:i]
		}
		parents := []string{}
		if p := strings.TrimSpace(f[2]); p != "" {
			parents = strings.Fields(p)
		}
		commits = append(commits, CommitInfo{
			SHA: f[0], ShortSHA: f[1], Parents: parents,
			AuthorName: f[3], AuthorEmail: f[4],
			CommitterName: f[5], CommitterEmail: f[6],
			Message: message, Subject: subject,
			Timestamp: author, CommittedAt: committed,
		})
	}
	return commits, nil
}

// CommitCount returns the number of commits reachable from rev.
func (g *Git) CommitCount(ctx context.Context, repoPath, rev string) (int, error) {
	out, err := g.run(ctx, repoPath, nil, "rev-list", "--count", rev)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

// Branches lists local branches, newest first. Remote-tracking branches are
// ignored: this server is the only origin.
func (g *Git) Branches(ctx context.Context, repoPath string) ([]Ref, error) {
	const format = "%(refname:short)%x1f%(objectname)%x1f%(committerdate:unix)"
	out, err := g.run(ctx, repoPath, nil, "for-each-ref", "--sort=-committerdate",
		"--format="+format, "refs/heads/")
	if err != nil {
		return nil, err
	}
	refs := []Ref{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) != 3 {
			continue
		}
		refs = append(refs, Ref{Name: f[0], Short: f[0], Target: f[1], Type: "branch"})
	}
	return refs, nil
}

// Tags lists tags with their dereferenced commit target.
func (g *Git) Tags(ctx context.Context, repoPath string) ([]Ref, error) {
	const format = "%(refname:short)%x1f%(objectname)%x1f%(*objectname)%x1f%(objecttype)"
	out, err := g.run(ctx, repoPath, nil, "for-each-ref", "--sort=-creatordate",
		"--format="+format, "refs/tags/")
	if err != nil {
		return nil, err
	}
	refs := []Ref{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\x1f")
		if len(f) < 3 {
			continue
		}
		target := f[1]
		if f[2] != "" {
			target = f[2] // annotated tag: use the commit it points to
		}
		refs = append(refs, Ref{Name: f[0], Short: f[0], Target: target, Type: "tag"})
	}
	return refs, nil
}

// Refs lists branches and tags together.
func (g *Git) Refs(ctx context.Context, repoPath string) ([]Ref, error) {
	branches, err := g.Branches(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	tags, err := g.Tags(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return append(branches, tags...), nil
}

// DefaultBranch reports the branch HEAD points at.
func (g *Git) DefaultBranch(ctx context.Context, repoPath string) (string, error) {
	out, err := g.run(ctx, repoPath, nil, "symbolic-ref", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// BranchesContaining lists branches that contain the given commit.
func (g *Git) BranchesContaining(ctx context.Context, repoPath, sha string) ([]string, error) {
	out, err := g.run(ctx, repoPath, nil, "branch", "--format=%(refname:short)", "--contains", sha)
	if err != nil {
		return nil, err
	}
	return nonEmptyLines(string(out)), nil
}

// HashObject writes content as a blob and returns its object ID. With write set
// the object is added to the object database.
func (g *Git) HashObject(ctx context.Context, repoPath string, content []byte, write bool, filePath string) (string, error) {
	args := []string{"hash-object", "-t", "blob"}
	if write {
		args = append(args, "-w")
	}
	if filePath != "" {
		args = append(args, "--path="+filePath)
	}
	out, err := g.run(ctx, repoPath, content, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CommitTree creates a commit object from tree with the given parents and does
// not move any ref. The caller is responsible for update-ref, which keeps the
// "object first, ref second" ordering that makes failures recoverable.
func (g *Git) CommitTree(ctx context.Context, repoPath string, opts CommitTreeOptions) (string, error) {
	env := []string{
		"GIT_AUTHOR_NAME=" + opts.AuthorName,
		"GIT_AUTHOR_EMAIL=" + opts.AuthorEmail,
		"GIT_AUTHOR_DATE=" + opts.AuthorDate.UTC().Format(time.RFC3339),
		"GIT_COMMITTER_NAME=" + opts.CommitterName,
		"GIT_COMMITTER_EMAIL=" + opts.CommitterEmail,
		"GIT_COMMITTER_DATE=" + opts.CommitterDate.UTC().Format(time.RFC3339),
	}
	args := []string{"commit-tree", opts.Tree}
	for _, p := range opts.Parents {
		args = append(args, "-p", p)
	}

	out, err := g.runEnv(ctx, repoPath, env, []byte(opts.Message), args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type CommitTreeOptions struct {
	Tree           string
	Parents        []string
	Message        string
	AuthorName     string
	AuthorEmail    string
	AuthorDate     time.Time
	CommitterName  string
	CommitterEmail string
	CommitterDate  time.Time
}

// UpdateRef atomically points ref at newSHA. When oldSHA is non-empty the ref
// is only updated if it currently points there, which is what makes concurrent
// merges safe.
func (g *Git) UpdateRef(ctx context.Context, repoPath, ref, newSHA, oldSHA string) error {
	args := []string{"update-ref", ref, newSHA}
	if oldSHA != "" {
		args = append(args, oldSHA)
	}
	_, err := g.run(ctx, repoPath, nil, args...)
	return err
}

// DeleteRef removes a branch or tag.
func (g *Git) DeleteRef(ctx context.Context, repoPath, ref string) error {
	_, err := g.run(ctx, repoPath, nil, "update-ref", "-d", ref)
	return err
}

// CreateBranch points refs/heads/<name> at sha.
func (g *Git) CreateBranch(ctx context.Context, repoPath, name, sha string) error {
	if err := ValidateRefName(name); err != nil {
		return err
	}
	return g.UpdateRef(ctx, repoPath, "refs/heads/"+name, sha, "")
}

// CreateTag creates a lightweight tag at sha, or an annotated tag when message
// is non-empty.
func (g *Git) CreateTag(ctx context.Context, repoPath, name, sha, message string) error {
	if err := ValidateRefName(name); err != nil {
		return err
	}
	args := []string{"tag", "-f", name}
	if message != "" {
		args = append(args, "-m", message)
	}
	args = append(args, sha)
	_, err := g.run(ctx, repoPath, nil, args...)
	return err
}

// DeleteTag removes a tag ref.
func (g *Git) DeleteTag(ctx context.Context, repoPath, name string) error {
	return g.DeleteRef(ctx, repoPath, "refs/tags/"+name)
}

// IsAncestor reports whether ancestor is reachable from descendant.
func (g *Git) IsAncestor(ctx context.Context, repoPath, ancestor, descendant string) (bool, error) {
	_, err := g.run(ctx, repoPath, nil, "merge-base", "--is-ancestor", ancestor, descendant)
	if err == nil {
		return true, nil
	}
	var ce *CommandError
	if errors.As(err, &ce) && ce.ExitCode == 1 {
		return false, nil
	}
	return false, err
}

// MergeBase returns the best common ancestor of two revisions.
func (g *Git) MergeBase(ctx context.Context, repoPath, a, b string) (string, error) {
	out, err := g.run(ctx, repoPath, nil, "merge-base", a, b)
	if err != nil {
		if IsConflict(err) {
			return "", fmt.Errorf("%w: %s and %s have no common ancestor", ErrNotFound, a, b)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func nonEmptyLines(s string) []string {
	out := []string{}
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ShortSHA abbreviates a full object ID.
func ShortSHA(sha string) string {
	if len(sha) <= 8 {
		return sha
	}
	return sha[:8]
}

// IsHexSHA reports whether s looks like a full 40-character object ID.
func IsHexSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
