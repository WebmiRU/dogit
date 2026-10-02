package api

import (
	"net/http"
	"strings"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/store"
)

// branchRequest creates or renames a branch.
type branchRequest struct {
	Name  string `json:"name"`
	SHA   string `json:"sha"`
	Start string `json:"start_point"`
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPush)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req branchRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := validateBranchName(name); err != nil {
		s.writeError(w, r, err)
		return
	}

	start := req.Start
	if start == "" {
		start = rc.Project.DefaultBranch
	}
	startSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, start)
	if err != nil {
		s.writeError(w, r, errNotFoundf("start point %q does not exist", start))
		return
	}
	// An explicit SHA lets the editor offer "commit to a new branch".
	if req.SHA != "" {
		startSHA, err = s.git.RevParse(r.Context(), rc.RepoDir, req.SHA)
		if err != nil {
			s.writeError(w, r, errNotFoundf("commit %q does not exist", req.SHA))
			return
		}
	}

	if err := s.git.CreateBranch(r.Context(), rc.RepoDir, name, startSHA); err != nil {
		s.writeError(w, r, errConflictf("branch %q already exists", name))
		return
	}

	s.log.Info("branch created", "project", rc.Project.Path, "branch", name, "user", userFrom(r.Context()).Username)
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"branch": map[string]any{"name": name, "target": startSHA, "type": "branch"},
	})
}

func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPushProtected)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	name, err := refParam(r, "name")
	if err != nil {
		s.writeError(w, r, errBadRequest(err.Error()))
		return
	}
	if err := validateBranchName(name); err != nil {
		s.writeError(w, r, err)
		return
	}
	if name == rc.Project.DefaultBranch {
		s.writeError(w, r, errBadRequest("the default branch cannot be deleted"))
		return
	}

	if err := s.git.DeleteRef(r.Context(), rc.RepoDir, "refs/heads/"+name); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.log.Info("branch deleted", "project", rc.Project.Path, "branch", name)
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

type tagRequest struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPushProtected)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req tagRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	name := strings.TrimSpace(req.Name)
	if err := validateTagName(name); err != nil {
		s.writeError(w, r, err)
		return
	}

	ref := rc.Project.DefaultBranch
	if req.SHA != "" {
		ref = req.SHA
	}
	sha, err := s.git.RevParse(r.Context(), rc.RepoDir, ref)
	if err != nil {
		s.writeError(w, r, errNotFoundf("revision %q does not exist", ref))
		return
	}

	if err := s.git.CreateTag(r.Context(), rc.RepoDir, name, sha, req.Message, gitx.CommitIdentity{
		Name:  displayName(userFrom(r.Context())),
		Email: userFrom(r.Context()).Email,
	}); err != nil {
		// git says "already exists" in its own words; the caller asked for a new
		// tag and silently moving an existing one would detach a release from the
		// commit it named.
		if strings.Contains(err.Error(), "already exists") {
			s.writeError(w, r, errConflictf("tag %q already exists", name))
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.log.Info("tag created", "project", rc.Project.Path, "tag", name,
		"user", userFrom(r.Context()).Username)

	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"tag": map[string]any{"name": name, "target": sha, "type": "tag"},
	})
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPushProtected)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	name, err := refParam(r, "name")
	if err != nil {
		s.writeError(w, r, errBadRequest(err.Error()))
		return
	}
	if err := validateTagName(name); err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.git.DeleteTag(r.Context(), rc.RepoDir, name); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

func validateBranchName(name string) error {
	if name == "" {
		return errBadRequest("a branch name is required")
	}
	if err := validateGitRefName(name); err != nil {
		return errBadRequest("invalid branch name: " + err.Error())
	}
	return nil
}

func validateTagName(name string) error {
	if name == "" {
		return errBadRequest("a tag name is required")
	}
	if err := validateGitRefName(name); err != nil {
		return errBadRequest("invalid tag name: " + err.Error())
	}
	if strings.Contains(name, "/") {
		return errBadRequest("tag names must not contain slashes")
	}
	return nil
}
