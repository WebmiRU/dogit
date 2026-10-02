package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// FileChange describes one file in a diff.
type FileChange struct {
	Path      string `json:"path"`
	OldPath   string `json:"old_path,omitempty"`
	Status    string `json:"status"` // added, modified, deleted, renamed, copied, typechange
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Mode      string `json:"mode,omitempty"`
	// Patch holds the unified diff for this file, truncated to PatchLimit bytes.
	Patch string `json:"patch"`
	// Truncated reports that Patch was cut short and should be fetched separately.
	Truncated bool `json:"truncated"`
}

// DiffOptions controls how a diff is produced.
type DiffOptions struct {
	// ThreeDot selects "a...b" semantics: diff against the merge base, which is
	// what merge requests show.
	ThreeDot bool
	Paths    []string
	// StatOnly returns file names and counts without patch content.
	StatOnly bool
	// PatchLimit truncates per-file patch content. Zero means no truncation.
	PatchLimit int
	// ContextLines is the number of unchanged lines around a hunk.
	ContextLines int
}

// DefaultPatchLimit bounds the patch returned for a single file. Larger diffs
// are fetched on demand by the file-diff endpoint.
const DefaultPatchLimit = 512 * 1024

// Diff returns the file-level changes between two revisions.
func (g *Git) Diff(ctx context.Context, repoPath, from, to string, opts DiffOptions) ([]FileChange, error) {
	if opts.ContextLines <= 0 {
		opts.ContextLines = 3
	}

	args := []string{"diff", "--no-color", "--find-renames", "-z"}
	switch {
	case opts.StatOnly:
		args = append(args, "--name-status", "--numstat")
	default:
		args = append(args, fmt.Sprintf("--unified=%d", opts.ContextLines), "--patch")
	}

	if opts.ThreeDot {
		args = append(args, from+"..."+to)
	} else {
		args = append(args, from, to)
	}
	if len(opts.Paths) > 0 {
		args = append(args, "--")
		args = append(args, opts.Paths...)
	}

	out, err := g.run(ctx, repoPath, nil, args...)
	if err != nil {
		return nil, err
	}
	if opts.StatOnly {
		return parseNameStatusZ(string(out)), nil
	}
	return g.parsePatch(string(out), opts.PatchLimit), nil
}

// CommitDiff returns the changes introduced by a single commit.
func (g *Git) CommitDiff(ctx context.Context, repoPath, sha string, patchLimit int) ([]FileChange, error) {
	if patchLimit <= 0 {
		patchLimit = DefaultPatchLimit
	}
	args := []string{
		"show", "--no-color", "--find-renames",
		fmt.Sprintf("--unified=%d", 3),
		fmt.Sprintf("--patch"), sha,
	}
	if patchLimit <= 0 {
		args = append(args, "--stat")
	}

	out, err := g.run(ctx, repoPath, nil, args...)
	if err != nil {
		return nil, err
	}
	changes := g.parsePatch(string(out), patchLimit)
	return changes, nil
}

// parsePatch parses a -z-free `git diff` patch stream into per-file records.
func (g *Git) parsePatch(raw string, patchLimit int) []FileChange {
	if patchLimit <= 0 {
		patchLimit = DefaultPatchLimit
	}
	changes := []FileChange{}
	lines := strings.Split(raw, "\n")

	var cur *FileChange
	flush := func() {
		if cur == nil {
			return
		}
		if len(cur.Patch) > patchLimit {
			cur.Patch = cur.Patch[:patchLimit]
			cur.Truncated = true
		}
		changes = append(changes, *cur)
		cur = nil
	}

	inPatch := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			inPatch = false
			cur = &FileChange{Status: "modified"}
			cur.Path, cur.OldPath = parseDiffHeaderPaths(line)

		case strings.HasPrefix(line, "new file mode "):
			if cur != nil {
				cur.Status = "added"
				cur.Mode = strings.TrimPrefix(line, "new file mode ")
			}

		case strings.HasPrefix(line, "deleted file mode "):
			if cur != nil {
				cur.Status = "deleted"
				cur.Mode = strings.TrimPrefix(line, "deleted file mode ")
			}

		case strings.HasPrefix(line, "rename from "):
			if cur != nil {
				cur.Status = "renamed"
				cur.OldPath = strings.TrimPrefix(line, "rename from ")
			}

		case strings.HasPrefix(line, "rename to "):
			if cur != nil {
				cur.Status = "renamed"
				cur.Path = strings.TrimPrefix(line, "rename to ")
			}

		case strings.HasPrefix(line, "Binary files "):
			if cur != nil {
				cur.Binary = true
				cur.Status = "modified"
			}

		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			// Path lines before the first hunk refine the paths we already have.
			if cur != nil && strings.HasPrefix(line, "--- ") {
				if p := stripDiffPrefix(cleanDiffPath(line[4:])); p != "" && p != "/dev/null" {
					cur.OldPath = p
				}
			}
			if cur != nil && strings.HasPrefix(line, "+++ ") {
				if p := stripDiffPrefix(cleanDiffPath(line[4:])); p != "" && p != "/dev/null" {
					cur.Path = p
				}
			}
			inPatch = true
		}

		if cur == nil {
			continue
		}
		if inPatch {
			cur.Patch += line + "\n"
		}
		switch {
		case strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++"):
			cur.Additions++
		case strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			cur.Deletions++
		}
	}
	flush()
	return changes
}

func parseDiffHeaderPaths(line string) (newPath, oldPath string) {
	// "diff --git a/foo b/bar"; quoted paths are handled by the a/ b/ stripping.
	rest := strings.TrimPrefix(line, "diff --git ")
	fields := strings.Fields(rest)
	switch len(fields) {
	case 2:
		return stripDiffPrefix(fields[1]), stripDiffPrefix(fields[0])
	case 1:
		p := stripDiffPrefix(fields[0])
		return p, p
	}
	return "", ""
}

// stripDiffPrefix removes the "a/" and "b/" prefixes git puts on the two sides
// of a diff header.
func stripDiffPrefix(p string) string {
	if rest, ok := strings.CutPrefix(p, "a/"); ok {
		return rest
	}
	if rest, ok := strings.CutPrefix(p, "b/"); ok {
		return rest
	}
	return p
}

func cleanDiffPath(p string) string {
	p = strings.TrimSpace(p)
	// Strip a trailing tab-separated timestamp, which git may append.
	if i := strings.IndexByte(p, '\t'); i >= 0 {
		p = p[:i]
	}
	if strings.HasPrefix(p, "\"") && strings.HasSuffix(p, "\"") {
		if unquoted, err := strconv.Unquote(p); err == nil {
			p = unquoted
		}
	}
	return p
}

// parseNameStatusZ parses `git diff --name-status -z` output.
func parseNameStatusZ(raw string) []FileChange {
	fields := strings.Split(raw, "\x00")
	changes := []FileChange{}
	for i := 0; i < len(fields); i++ {
		status := strings.TrimSpace(fields[i])
		if status == "" {
			continue
		}
		code := status[0]
		// Renames and copies carry the old path as the next field.
		extra := (code == 'R' || code == 'C')
		if i+1 >= len(fields) {
			break
		}
		path := fields[i+1]
		i++
		change := FileChange{Path: path, Status: statusName(code)}
		if extra && i+1 < len(fields) {
			change.OldPath = path
			i++
			change.Path = fields[i+1]
			i++
		}
		// Optional numstat: "<adds>\t<dels>\t<path>" without -z alignment is
		// skipped here; Stat is filled in by StatFor.
		changes = append(changes, change)
	}
	return changes
}

func statusName(code byte) string {
	switch code {
	case 'A':
		return "added"
	case 'M':
		return "modified"
	case 'D':
		return "deleted"
	case 'R':
		return "renamed"
	case 'C':
		return "copied"
	case 'T':
		return "typechange"
	}
	return "modified"
}

// Stat returns the summary of a diff between two revisions.
func (g *Git) Stat(ctx context.Context, repoPath, from, to string, threeDot bool) (*DiffStat, error) {
	spec := from + ".."
	if threeDot {
		spec = from + "..."
	}
	out, err := g.run(ctx, repoPath, nil, "diff", "--numstat", spec, to, "--")
	if err != nil {
		return nil, err
	}

	stat := &DiffStat{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 3 {
			continue
		}
		stat.FilesChanged++
		if f[0] != "-" {
			stat.Additions += atoi(f[0])
		}
		if f[1] != "-" {
			stat.Deletions += atoi(f[1])
		}
	}
	return stat, nil
}

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

// BlameLine is one line of `git blame` output.
type BlameLine struct {
	CommitSHA  string `json:"commit_sha"`
	AuthorName string `json:"author_name"`
	Timestamp  string `json:"timestamp"`
	LineNo     int    `json:"line_no"`
	Content    string `json:"content"`
}

// Blame returns per-line authorship for a file at a revision.
func (g *Git) Blame(ctx context.Context, repoPath, rev, path string) ([]BlameLine, error) {
	if err := ValidateRepoPath(path); err != nil {
		return nil, err
	}
	// --porcelain is stable across git versions and easy to parse.
	out, err := g.run(ctx, repoPath, nil, "blame", "-w", "--porcelain", rev, "--", path)
	if err != nil {
		return nil, err
	}
	return parseBlame(string(out)), nil
}

func parseBlame(raw string) []BlameLine {
	lines := []BlameLine{}
	pending := BlameLine{}
	lineNo := 0

	for _, l := range strings.Split(raw, "\n") {
		switch {
		case strings.HasPrefix(l, "author "):
			pending.AuthorName = strings.TrimPrefix(l, "author ")
		case strings.HasPrefix(l, "author-time "):
			pending.Timestamp = strings.TrimPrefix(l, "author-time ")
		case strings.HasPrefix(l, "author-tz "), strings.HasPrefix(l, "committer "),
			strings.HasPrefix(l, "committer-time "), strings.HasPrefix(l, "committer-tz "),
			strings.HasPrefix(l, "boundary"), strings.HasPrefix(l, "previous "):
			// Ignored metadata.
		case strings.HasPrefix(l, "\t"):
			lineNo++
			lines = append(lines, BlameLine{
				CommitSHA:  pending.CommitSHA,
				AuthorName: pending.AuthorName,
				Timestamp:  pending.Timestamp,
				LineNo:     lineNo,
				Content:    strings.TrimPrefix(l, "\t"),
			})
			pending = BlameLine{}
		case len(l) > 0 && IsHexSHA(strings.Fields(l)[0]):
			// "<sha> <origLine> <finalLine> [numLines]"
			f := strings.Fields(l)
			pending.CommitSHA = f[0]
			if len(f) >= 3 {
				lineNo, _ = strconv.Atoi(f[2])
				lineNo--
			}
		}
	}
	return lines
}
