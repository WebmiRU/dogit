package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// commitFileRequest is a save from the web editor.
type commitFileRequest struct {
	// Branch is where the commit lands. A new branch may be created by setting
	// NewBranch, which is how an editor offers "commit to a fresh branch" without a
	// second form.
	Branch    string `json:"branch"`
	NewBranch string `json:"new_branch"`
	// Path is the file to write, relative to the repository root.
	Path string `json:"path"`
	// Content is the new file content. A missing file is created.
	Content string `json:"content"`
	// Message is the commit message.
	Message string `json:"message"`
	// StartSHA is what the editor loaded. When the file changed since then the save
	// is refused instead of overwriting someone else's work.
	StartSHA string `json:"start_sha"`
	// Force commits even when the file moved on. Deliberately explicit.
	Force bool `json:"force"`
}

// handleCommitFile writes a file and commits it.
//
// The implementation never checks anything out: content becomes a blob, the blob
// replaces the path in a tree, the tree becomes a commit, and the ref moves. That
// is safe to run while other jobs are pushing, because the ref update can be made
// conditional on the value it had when the edit started.
func (s *Server) handleCommitFile(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPush)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req commitFileRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	user := userFrom(r.Context())

	branch := strings.TrimSpace(req.NewBranch)
	if branch == "" {
		branch = strings.TrimSpace(req.Branch)
	}
	if branch == "" {
		branch = rc.Project.DefaultBranch
	}
	if err := gitx.ValidateRefName(branch); err != nil {
		s.writeError(w, r, errBadRequestf("invalid branch name: %v", err))
		return
	}

	path := strings.Trim(strings.TrimSpace(req.Path), "/")
	if path == "" {
		s.writeError(w, r, errBadRequest("a file path is required"))
		return
	}
	if err := gitx.ValidateRepoPath(path); err != nil {
		s.writeError(w, r, errBadRequest("invalid file path"))
		return
	}

	message := strings.TrimSpace(req.Message)
	if message == "" {
		message = "Update " + path
	}

	// The whole read-decide-write sequence runs under a per-project lock, so two
	// editors saving at once cannot both start from the same revision.
	var status = http.StatusCreated
	var body any

	err = s.store.WithProjectLock(r.Context(), rc.Project.ID.String(), func() error {
		code, payload, err := s.commitFile(r, rc, user, branch, path, req, message)
		status, body = code, payload
		return err
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, status, body)
}

// commitFile performs the save. It runs while the project lock is held.
func (s *Server) commitFile(
	r *http.Request, rc *repoContext, user *models.User,
	branch, path string, req commitFileRequest, message string,
) (int, any, error) {
	oldSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, branch)
	if errors.Is(err, gitx.ErrNotFound) {
		// Creating a new branch: start from the default branch when it exists.
		if base := rc.Project.DefaultBranch; base != "" && base != branch {
			oldSHA, err = s.git.RevParse(r.Context(), rc.RepoDir, base)
		}
		if err != nil && !errors.Is(err, gitx.ErrNotFound) {
			return 0, nil, err
		}
		return s.finishCommit(r, rc, user, branch, path, req, message, oldSHA, "", true)
	}
	if err != nil {
		return 0, nil, err
	}

	if err := s.checkStale(r, rc, branch, oldSHA, path, req); err != nil {
		return 0, nil, err
	}

	return s.finishCommit(r, rc, user, branch, path, req, message, oldSHA, oldSHA, false)
}

// checkStale refuses a save whose base file is not what the editor loaded.
func (s *Server) checkStale(r *http.Request, rc *repoContext, branch, oldSHA, path string, req commitFileRequest) error {
	if req.StartSHA == "" || req.Force {
		return nil
	}

	currentBlob, err := s.git.RevParseBlob(r.Context(), rc.RepoDir, oldSHA, path)
	if err != nil {
		// The file does not exist on this branch: a save that claims to have started
		// from something is contradictory, but a plain create is fine.
		if errors.Is(err, gitx.ErrNotFound) {
			return nil
		}
		return err
	}
	if currentBlob == req.StartSHA {
		return nil
	}
	return newError(http.StatusConflict, "file_changed",
		fmt.Sprintf("%s changed on %s since it was opened; reload and apply your changes again", path, branch))
}

// finishCommit builds the commit and moves the branch.
func (s *Server) finishCommit(
	r *http.Request,
	rc *repoContext, user *models.User,
	branch, path string,
	req commitFileRequest, message, oldSHA, expectedOldSHA string,
	newBranch bool,
) (int, any, error) {
	baseTree := ""
	if oldSHA != "" {
		tree, err := s.git.TreeOf(r.Context(), rc.RepoDir, oldSHA)
		if err != nil {
			return 0, nil, err
		}
		baseTree = tree
	}

	_, treeSHA, err := s.git.WriteBlobAndTree(r.Context(), rc.RepoDir, baseTree, path, []byte(req.Content))
	if err != nil {
		return 0, nil, err
	}

	now := time.Now()

	// A save adds to history; it never starts a new one. The parent is the tip the
	// edit was based on, so the file's previous versions stay reachable.
	var parents []string
	if oldSHA != "" {
		parents = []string{oldSHA}
	}

	commit, err := s.git.CommitTree(r.Context(), rc.RepoDir, gitx.CommitTreeOptions{
		Tree:    treeSHA,
		Parents: parents,
		Message: message,
		// The commit is attributed to the person who pressed save, not to the
		// server account, which is what the UI shows afterwards.
		AuthorName:     displayName(user),
		AuthorEmail:    user.Email,
		CommitterName:  displayName(user),
		CommitterEmail: user.Email,
		AuthorDate:     now,
		CommitterDate:  now,
	})
	if err != nil {
		return 0, nil, err
	}

	// The conditional update is the important part: the ref moves only if it is
	// still where it was when the edit started. A branch that did not exist yet is
	// expected to still not exist, so a creation cannot clobber a branch someone
	// pushed in the meantime.
	ref := "refs/heads/" + branch
	err = s.git.UpdateRef(r.Context(), rc.RepoDir, ref, commit, expectedOldSHA)
	if err != nil {
		return 0, nil, newError(http.StatusConflict, "branch_moved",
			fmt.Sprintf("%s moved while the file was being saved; nothing was committed", branch))
	}

	_ = s.git.UpdateServerInfo(r.Context(), rc.RepoDir)

	s.log.Info("file committed from the web",
		"project", rc.Project.Path, "branch", branch, "path", path, "user", user.Username)

	s.publishCommitEvent(r, rc, user, branch, commit, message)

	return http.StatusCreated, map[string]any{
		"commit_sha": commit,
		"branch":     branch,
		"path":       path,
		"new_branch": newBranch,
		"web_url":    fmt.Sprintf("/p/%s/-/commit/%s", rc.Project.Path, commit),
	}, nil
}

// displayName is what a commit is attributed to in the repository: the display
// name when it is set, otherwise the login.
func displayName(user *models.User) string {
	if strings.TrimSpace(user.Name) != "" {
		return user.Name
	}
	return user.Username
}
