package api

import (
	"fmt"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/store"
)

// refOrDefault resolves the requested revision, falling back to the project's
// default branch when the client did not ask for one.
func (rc *repoContext) refOrDefault(r *http.Request) string {
	ref := strings.TrimSpace(r.URL.Query().Get("ref"))
	if ref == "" {
		ref = rc.Project.DefaultBranch
	}
	if ref == "" {
		ref = "HEAD"
	}
	return ref
}

// resolveRef validates a caller-supplied revision and returns its commit SHA.
func (s *Server) resolveRef(r *http.Request, rc *repoContext) (ref, sha string, err error) {
	ref = rc.refOrDefault(r)

	if err := gitx.ValidateRepoPath(ref); err != nil && !gitx.IsHexSHA(ref) {
		// Refs are not paths, so path traversal rules do not apply; only reject
		// values that could reach outside the repository.
		if strings.Contains(ref, "..") || strings.HasPrefix(ref, "/") {
			return "", "", errBadRequest("invalid revision")
		}
	}

	sha, err = s.git.RevParse(r.Context(), rc.RepoDir, ref)
	if err == nil {
		return ref, sha, nil
	}

	// A ref that is not there is not always a mistake. A project can have no
	// branch called "main" at all, and answering "revision main does not exist"
	// describes the request rather than the repository. The caller gets the ref
	// that does exist, or — if the repository is genuinely empty — an answer that
	// says so.
	fallback, sha, err := s.fallbackRef(r, rc)
	if err != nil {
		return ref, "", err
	}
	if sha == "" {
		return "", "", newError(http.StatusNotFound, "repository_empty",
			"this repository has no commits yet")
	}
	return fallback, sha, nil
}

// fallbackRef picks something to show when the requested ref is missing: the
// project's default branch if it exists, otherwise the first branch there is.
// The returned sha is empty when the repository has no branches at all.
func (s *Server) fallbackRef(r *http.Request, rc *repoContext) (string, string, error) {
	branches, err := s.git.Branches(r.Context(), rc.RepoDir)
	if err != nil {
		return "", "", err
	}
	if len(branches) == 0 {
		return "", "", nil
	}

	if rc.Project.DefaultBranch != "" {
		for _, branch := range branches {
			if branch.Name == rc.Project.DefaultBranch {
				return branch.Name, branch.Target, nil
			}
		}
	}
	return branches[0].Name, branches[0].Target, nil
}

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ref, sha, err := s.resolveRef(r, rc)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	dirPath := strings.Trim(r.URL.Query().Get("path"), "/")
	if err := gitx.ValidateRepoPath(dirPath); err != nil && dirPath != "" {
		s.writeError(w, r, errBadRequest("invalid path"))
		return
	}

	entries, err := s.git.ListTree(r.Context(), rc.RepoDir, sha, dirPath, false)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Directories first, then files, each alphabetically: the order a file
	// listing is expected to have.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Path < entries[j].Path
	})

	breadcrumbs := []map[string]string{{"name": rc.Project.Name, "path": ""}}
	if dirPath != "" {
		acc := ""
		for _, part := range strings.Split(dirPath, "/") {
			if acc == "" {
				acc = part
			} else {
				acc += "/" + part
			}
			breadcrumbs = append(breadcrumbs, map[string]string{"name": part, "path": acc})
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"ref":         ref,
		"sha":         sha,
		"path":        dirPath,
		"entries":     entries,
		"breadcrumbs": breadcrumbs,
		"is_dir":      dirPath != "",
	})
}

type fileResponse struct {
	Ref string `json:"ref"`
	// SHA identifies the file itself: it is the blob id of its content at this
	// revision. A client that read the file and wants to save it back sends this
	// back, and the server refuses the save if the content moved on. It is
	// deliberately not the commit: the commit says where the file is, the blob
	// says what it says.
	SHA           string `json:"sha"`
	Path          string `json:"path"`
	Content       string `json:"content"`
	Size          int64  `json:"size"`
	Binary        bool   `json:"binary"`
	TooLarge      bool   `json:"too_large"`
	Language      string `json:"language"`
	TooLargeBytes int64  `json:"too_large_bytes,omitempty"`
	CommitSHA     string `json:"last_commit_sha,omitempty"`
	BlameURL      string `json:"blame_url,omitempty"`
	RawURL        string `json:"raw_url,omitempty"`
	Lines         int    `json:"lines"`
}

// maxInlineFileSize bounds what the API returns inline. Larger files are
// offered for download instead, so one request cannot pull hundreds of
// megabytes into the browser.
const maxInlineFileSize = 1 << 20 // 1 MiB

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ref, sha, err := s.resolveRef(r, rc)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	filePath := strings.Trim(r.URL.Query().Get("path"), "/")
	if filePath == "" {
		s.writeError(w, r, errBadRequest("a path is required"))
		return
	}
	if err := gitx.ValidateRepoPath(filePath); err != nil {
		s.writeError(w, r, errBadRequest("invalid path"))
		return
	}

	content, size, binary, err := s.git.CatFile(r.Context(), rc.RepoDir, sha, filePath)
	if err != nil {
		s.writeError(w, r, errNotFoundf("file %q does not exist in this revision", filePath))
		return
	}

	// The blob id is what a later save is compared against, so it is resolved here
	// rather than left to the client to derive from the content.
	blobSHA, err := s.git.RevParseBlob(r.Context(), rc.RepoDir, sha, filePath)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	resp := fileResponse{
		Ref:      ref,
		SHA:      blobSHA,
		Path:     filePath,
		Size:     size,
		Binary:   binary,
		Language: detectLanguage(filePath),
		RawURL:   fmt.Sprintf("/api/v1/projects/%s/repository/raw?ref=%s&path=%s", rc.Project.ID, ref, filePath),
		BlameURL: fmt.Sprintf("/api/v1/projects/%s/repository/blame?ref=%s&path=%s", rc.Project.ID, ref, filePath),
	}

	// Text files are returned whole; binary or oversized ones are not.
	if binary {
		s.writeJSON(w, r, http.StatusOK, resp)
		return
	}
	if size > maxInlineFileSize {
		resp.TooLarge = true
		resp.TooLargeBytes = size
		s.writeJSON(w, r, http.StatusOK, resp)
		return
	}

	resp.Content = string(content)
	resp.Lines = strings.Count(string(content), "\n") + 1

	// The last commit that touched this file powers the "last change" link. It has
	// to be the commit that changed *this path*, not the tip of the branch: on an
	// active branch those are different commits, and the tip would send the reader
	// to a change that has nothing to do with the file they were looking at.
	if commits, err := s.git.LogPath(r.Context(), rc.RepoDir, ref, filePath, 1); err == nil && len(commits) > 0 {
		resp.CommitSHA = commits[0].SHA
	}

	s.writeJSON(w, r, http.StatusOK, resp)
}

// handleRawFile streams file bytes untouched, for download and for the "open in
// new tab" link.
func (s *Server) handleRawFile(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	_, sha, err := s.resolveRef(r, rc)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	filePath := strings.Trim(r.URL.Query().Get("path"), "/")
	if err := gitx.ValidateRepoPath(filePath); err != nil {
		s.writeError(w, r, errBadRequest("invalid path"))
		return
	}

	content, _, binary, err := s.git.CatFile(r.Context(), rc.RepoDir, sha, filePath)
	if err != nil {
		s.writeError(w, r, errNotFoundf("file %q does not exist in this revision", filePath))
		return
	}

	if binary {
		w.Header().Set("Content-Type", "application/octet-stream")
	} else {
		w.Header().Set("Content-Type", contentTypeFor(filePath))
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", path.Base(filePath)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

func (s *Server) handleBlame(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ref, sha, err := s.resolveRef(r, rc)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	filePath := strings.Trim(r.URL.Query().Get("path"), "/")
	if err := gitx.ValidateRepoPath(filePath); err != nil {
		s.writeError(w, r, errBadRequest("invalid path"))
		return
	}

	lines, err := s.git.Blame(r.Context(), rc.RepoDir, sha, filePath)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// git reports timestamps as Unix seconds; render them the way the UI shows
	// them so the frontend does not need a date library for this.
	for i := range lines {
		lines[i].CommitSHA = gitx.ShortSHA(lines[i].CommitSHA)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"ref": ref, "path": filePath, "lines": lines,
	})
}

func (s *Server) handleRefs(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeRefs(w, r, rc)
}

func (s *Server) handleBranches(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	branches, err := s.git.Branches(r.Context(), rc.RepoDir)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if branches == nil {
		branches = []gitx.Ref{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"branches":       defaultBranchFirst(branches, rc.Project.DefaultBranch),
		"default_branch": rc.Project.DefaultBranch,
	})
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	tags, err := s.git.Tags(r.Context(), rc.RepoDir)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if tags == nil {
		tags = []gitx.Ref{}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"tags": tags})
}

func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ref, sha, err := s.resolveRef(r, rc)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	limit := queryInt(r, "limit", 30, 200)
	page := queryInt(r, "page", 1, 10_000)
	skip := (page - 1) * limit

	commits, err := s.git.Log(r.Context(), rc.RepoDir, sha, limit, skip)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	total, err := s.git.CommitCount(r.Context(), rc.RepoDir, sha)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	if commits == nil {
		commits = []gitx.CommitInfo{}
	}
	for i := range commits {
		commits[i].SHA = commits[i].ShortSHA
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"ref": ref, "commits": commits, "total": total,
		"page": page, "limit": limit,
		"has_more": skip+len(commits) < total,
	})
}

// handleCommitFeed serves the snapshot table filled by the post-receive hook.
// It is cheap for long histories, so the frontend can poll it for "new commits"
// without walking git.
func (s *Server) handleCommitFeed(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	branch := strings.Trim(r.URL.Query().Get("branch"), "/")
	if branch == "" {
		branch = rc.Project.DefaultBranch
	}
	limit := queryInt(r, "limit", 30, 200)

	commits, err := s.store.Commits().ByBranch(r.Context(), rc.Project.ID, branch, limit)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"branch": branch, "commits": commits,
	})
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	sha := pathParam(r, "sha")
	sha, err = s.resolveSHA(r, rc, sha)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	commits, err := s.git.Log(r.Context(), rc.RepoDir, sha, 1, 0)
	if err != nil || len(commits) == 0 {
		s.writeError(w, r, errNotFound("commit does not exist"))
		return
	}
	commit := commits[0]

	branches, err := s.git.BranchesContaining(r.Context(), rc.RepoDir, sha)
	if err != nil {
		branches = []string{}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"commit":   commit,
		"branches": branches,
		"diff_url": fmt.Sprintf("/api/v1/projects/%s/repository/commits/%s/diff", rc.Project.ID, sha),
	})
}

func (s *Server) handleCommitDiff(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	sha, err := s.resolveSHA(r, rc, pathParam(r, "sha"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	changes, stat, err := s.diffFor(r, rc, sha, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"sha": sha, "files": changes, "stats": stat,
	})
}

// handleCompare diffs two revisions, using three-dot semantics when a merge
// request asks for it (diff against the merge base).
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	from := strings.TrimSpace(r.URL.Query().Get("from"))
	to := strings.TrimSpace(r.URL.Query().Get("to"))
	if from == "" || to == "" {
		s.writeError(w, r, errBadRequest("both from and to are required"))
		return
	}

	fromSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, from)
	if err != nil {
		s.writeError(w, r, errNotFoundf("revision %q does not exist", from))
		return
	}
	toSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, to)
	if err != nil {
		s.writeError(w, r, errNotFoundf("revision %q does not exist", to))
		return
	}

	threeDot := r.URL.Query().Get("three_dot") == "true"
	paths := splitPaths(r.URL.Query().Get("paths"))

	changes, stat, err := s.diffBetween(r, rc, fromSHA, toSHA, threeDot, paths)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	// Source commits that are not already in the target drive the creation preview.
	// Both branch names have been resolved to commit IDs above, so the range is safe.
	commits, err := s.git.Log(r.Context(), rc.RepoDir, fromSHA+".."+toSHA, 250, 0)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Ahead/behind counts drive the "N commits ahead" badge.
	ahead, behind := 0, 0
	if out, err := s.git.Run(r.Context(), rc.RepoDir, nil,
		"rev-list", "--left-right", "--count", fromSHA+"..."+toSHA); err == nil {
		fmt.Sscanf(strings.TrimSpace(string(out)), "%d\t%d", &behind, &ahead)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"from": from, "to": to, "from_sha": fromSHA, "to_sha": toSHA,
		"files": changes, "stats": stat, "commits": commits,
		"commits_ahead": ahead, "commits_behind": behind,
		"three_dot": threeDot,
	})
}

// diffFor produces the diff introduced by a single commit, against its first
// parent (or against the empty tree for a root commit).
func (s *Server) diffFor(r *http.Request, rc *repoContext, sha string, paths []string) ([]gitx.FileChange, *gitx.DiffStat, error) {
	commits, err := s.git.Log(r.Context(), rc.RepoDir, sha, 1, 0)
	if err != nil || len(commits) == 0 {
		return nil, nil, errNotFound("commit does not exist")
	}

	var fromSHA string
	if len(commits[0].Parents) > 0 {
		fromSHA = commits[0].Parents[0]
	} else {
		// Root commit: diff against the empty tree so the initial import shows
		// every file as added.
		fromSHA = gitx.EmptyTree
	}

	changes, stat, err := s.diffBetween(r, rc, fromSHA, sha, false, paths)
	if err != nil {
		return nil, nil, err
	}
	return changes, stat, nil
}

func (s *Server) diffBetween(r *http.Request, rc *repoContext, fromSHA, toSHA string, threeDot bool, paths []string) ([]gitx.FileChange, *gitx.DiffStat, error) {
	limit := queryInt(r, "patch_limit", gitx.DefaultPatchLimit, 8<<20)

	changes, err := s.git.Diff(r.Context(), rc.RepoDir, fromSHA, toSHA, gitx.DiffOptions{
		ThreeDot:   threeDot,
		Paths:      paths,
		PatchLimit: limit,
	})
	if err != nil {
		return nil, nil, err
	}

	stat := &gitx.DiffStat{FilesChanged: len(changes)}
	for _, c := range changes {
		stat.Additions += c.Additions
		stat.Deletions += c.Deletions
	}
	return changes, stat, nil
}

// resolveSHA expands a short SHA into a full one.
func (s *Server) resolveSHA(r *http.Request, rc *repoContext, sha string) (string, error) {
	sha = strings.TrimSpace(sha)
	if sha == "" {
		return "", errBadRequest("a revision is required")
	}
	if gitx.IsHexSHA(sha) {
		full, err := s.git.RevParse(r.Context(), rc.RepoDir, sha)
		if err != nil {
			return "", errNotFound("commit does not exist")
		}
		return full, nil
	}
	full, err := s.git.RevParse(r.Context(), rc.RepoDir, sha)
	if err != nil {
		return "", errNotFoundf("revision %q does not exist", sha)
	}
	return full, nil
}

func splitPaths(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// defaultBranchFirst pins the project's default branch to the top of the list.
//
// Git has no opinion about which branch a project considers default, and the
// order it returns is by date: the branch people work on most often ends up in
// the middle of a dropdown, which is the opposite of what they expect when they
// open it.
func defaultBranchFirst(branches []gitx.Ref, defaultBranch string) []gitx.Ref {
	out := make([]gitx.Ref, 0, len(branches))
	for _, branch := range branches {
		if branch.Name == defaultBranch {
			out = append(out, branch)
			break
		}
	}
	for _, branch := range branches {
		if branch.Name != defaultBranch {
			out = append(out, branch)
		}
	}
	return out
}

func (s *Server) writeRefs(w http.ResponseWriter, r *http.Request, rc *repoContext) {
	branches, err := s.git.Branches(r.Context(), rc.RepoDir)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	tags, err := s.git.Tags(r.Context(), rc.RepoDir)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if branches == nil {
		branches = []gitx.Ref{}
	}
	if tags == nil {
		tags = []gitx.Ref{}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"branches":       defaultBranchFirst(branches, rc.Project.DefaultBranch),
		"tags":           tags,
		"default_branch": rc.Project.DefaultBranch,
	})
}
