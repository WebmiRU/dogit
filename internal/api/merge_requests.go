package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// mergeRequestRequest opens or edits a merge request.
type mergeRequestRequest struct {
	SourceBranch       string      `json:"source_branch"`
	TargetBranch       string      `json:"target_branch"`
	Title              string      `json:"title"`
	Description        string      `json:"description"`
	Squash             bool        `json:"squash"`
	IsDraft            bool        `json:"is_draft"`
	AssigneeID         *uuid.UUID  `json:"assignee_id"`
	ReviewerID         *uuid.UUID  `json:"reviewer_id"`
	Milestone          string      `json:"milestone"`
	Labels             []string    `json:"labels"`
	RemoveSourceBranch bool        `json:"remove_source_branch"`
	PipelineRequired   bool        `json:"pipeline_required"`
}

// handleCreateMergeRequest opens a merge request.
//
// The branch tips are read at open time so the request records what it was opened
// against; a later push on the source branch changes what the diff shows, but the
// request keeps its original base.
func (s *Server) handleCreateMergeRequest(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionPush)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req mergeRequestRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	source := strings.TrimSpace(req.SourceBranch)
	target := strings.TrimSpace(req.TargetBranch)
	if target == "" {
		target = rc.Project.DefaultBranch
	}

	if source == "" {
		s.writeError(w, r, errBadRequest("a source branch is required"))
		return
	}
	if source == target {
		s.writeError(w, r, errBadRequest("the source and target branches are the same"))
		return
	}
	if err := gitx.ValidateRefName(source); err != nil {
		s.writeError(w, r, errBadRequestf("invalid source branch: %v", err))
		return
	}
	if err := gitx.ValidateRefName(target); err != nil {
		s.writeError(w, r, errBadRequestf("invalid target branch: %v", err))
		return
	}

	for _, branch := range []string{source, target} {
		if _, err := s.git.RevParse(r.Context(), rc.RepoDir, branch); err != nil {
			s.writeError(w, r, errNotFoundf("branch %q does not exist", branch))
			return
		}
	}

	// A branch with nothing new in it has nothing to review, and saying so is more
	// useful than opening a request whose diff is always empty.
	if merged, _ := s.git.IsAncestor(r.Context(), rc.RepoDir, source, target); merged {
		s.writeError(w, r, errBadRequest("the source branch has no commits the target is missing"))
		return
	}

	// Two open requests for one branch pair make the merge button ambiguous:
	// either one merges the same commits. The existing one is returned instead.
	if existing, err := s.store.MergeRequests().OpenByBranch(r.Context(), rc.Project.ID, source, target); err != nil {
		s.writeError(w, r, err)
		return
	} else if existing != nil {
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"merge_request": s.mergeRequestToView(r, existing),
			"existing":      true,
		})
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = store.TitleFromBranch(source)
	}

	user := userFrom(r.Context())
	mergeable, conflicts, err := s.mergeability(r, rc, source, target)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Assignment targets must be able to access this project. Checking at the
	// boundary prevents a caller from assigning requests to unrelated accounts.
	for _, assigned := range []*uuid.UUID{req.AssigneeID, req.ReviewerID} {
		if assigned == nil {
			continue
		}
		level, levelErr := s.store.Permissions().AccessLevel(r.Context(), *assigned, rc.Project.ID)
		if levelErr != nil {
			s.writeError(w, r, levelErr)
			return
		}
		if level == store.NoAccess && *assigned != user.ID {
			s.writeError(w, r, errBadRequest("assignee and reviewer must have access to this project"))
			return
		}
	}

	mr, err := s.store.MergeRequests().Create(r.Context(), store.CreateParams{
		ProjectID: rc.Project.ID, AuthorID: user.ID,
		SourceBranch: source, TargetBranch: target,
		Title: title, Description: req.Description, Squash: req.Squash,
		IsDraft: req.IsDraft, AssigneeID: req.AssigneeID, ReviewerID: req.ReviewerID,
		Milestone: strings.TrimSpace(req.Milestone), Labels: req.Labels,
		RemoveSourceBranch: req.RemoveSourceBranch, PipelineRequired: req.PipelineRequired,
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.writeError(w, r, errConflict("a merge request for these branches already exists"))
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.publishMergeRequest(r, rc, mr, "created")
	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"merge_request": s.mergeRequestToView(r, mr),
		"mergeable":     mergeable,
		"conflicts":     conflicts,
	})
}

// handleListProjectMergeRequests lists the merge requests of one project.
func (s *Server) handleListProjectMergeRequests(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	state := strings.TrimSpace(r.URL.Query().Get("state"))
	if state == "all" {
		state = ""
	}

	list, err := s.store.MergeRequests().List(r.Context(), store.ListFilter{
		ProjectID: &rc.Project.ID,
		State:     state,
		Limit:     queryInt(r, "limit", 50, 100),
		Offset:    queryInt(r, "offset", 0, 10000),
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	out := []*models.MergeRequest{}
	for _, mr := range list {
		out = append(out, s.mergeRequestToView(r, mr))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"merge_requests": out})
}

// handleListMergeRequests returns merge requests, filtered by state and project.
func (s *Server) handleListMergeRequests(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	_, levels, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	filter := store.ListFilter{
		State:  strings.TrimSpace(r.URL.Query().Get("state")),
		Limit:  queryInt(r, "limit", 50, 100),
		Offset: queryInt(r, "offset", 0, 10000),
	}
	switch filter.State {
	case "", string(models.MRStateOpened), string(models.MRStateMerged), string(models.MRStateClosed), "all":
	default:
		s.writeError(w, r, errBadRequest("state must be one of opened, merged, closed or all"))
		return
	}
	if filter.State == "all" {
		filter.State = ""
	}

	if requested := strings.TrimSpace(r.URL.Query().Get("project")); requested != "" {
		project, err := s.resolveProjectRef(r.Context(), requested)
		if err != nil {
			s.writeError(w, r, errNotFoundf("project %q does not exist", requested))
			return
		}
		if levels[*project] == store.NoAccess {
			s.writeError(w, r, errNotFound("project does not exist"))
			return
		}
		filter.ProjectID = project
	}

	list, err := s.store.MergeRequests().List(r.Context(), filter)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The list is filtered to what the caller may read: a merge request inherits
	// the visibility of its project, so a private project's requests never appear
	// in a global list.
	out := []*models.MergeRequest{}
	for _, mr := range list {
		if levels[mr.ProjectID] == store.NoAccess {
			continue
		}
		out = append(out, s.mergeRequestToView(r, mr))
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"merge_requests": out})
}

// mergeRequestContext loads a merge request and checks the caller may see it.
func (s *Server) mergeRequestContext(r *http.Request, action store.Action) (*repoContext, *models.MergeRequest, error) {
	rc, err := s.repoWithAccess(r, action)
	if err != nil {
		return nil, nil, err
	}

	// The number is the project-internal one, from the path.
	iid, err := strconv.Atoi(pathParam(r, "iid"))
	if err != nil || iid <= 0 {
		return nil, nil, errBadRequest("a merge request number is required")
	}

	mr, err := s.store.MergeRequests().ByIID(r.Context(), rc.Project.ID, iid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, errNotFound("merge request does not exist")
		}
		return nil, nil, err
	}
	return rc, mr, nil
}

// handleGetMergeRequest returns one merge request with its diff summary.
func (s *Server) handleGetMergeRequest(w http.ResponseWriter, r *http.Request) {
	rc, mr, err := s.mergeRequestContext(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	view := s.mergeRequestToView(r, mr)

	// A merge request that is still open is only meaningful next to the state of
	// the branches right now, so the answer carries it. A merged or closed
	// request is a record of something that happened and does not depend on the
	// branches still being there — which they may not be, since merging can
	// remove the source branch.
	diffURL := ""
	branchesExist := true

	// Whether the branches are still there is asked for every request, not only
	// the open ones: merging can remove the source branch, and a merged request
	// whose branch has gone has no diff left to show.
	branchesExist = s.git.Exists(r.Context(), rc.RepoDir, mr.SourceBranch) &&
		s.git.Exists(r.Context(), rc.RepoDir, mr.TargetBranch)

	if mr.IsOpen() && branchesExist {
		mergeable, conflicts, err := s.mergeability(r, rc, mr.SourceBranch, mr.TargetBranch)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		view.MergeStatus = mergeStatusName(mergeable, len(conflicts) > 0)
		view.HasConflicts = len(conflicts) > 0

		// Three-dot so the numbers describe what this request adds, not what the
		// target branch has done since the two diverged.
		stats, err := s.git.Stat(r.Context(), rc.RepoDir, mr.TargetBranch, mr.SourceBranch, true)
		if err == nil && stats != nil {
			view.DiffStats = &models.DiffStat{
				FilesChanged: stats.FilesChanged,
				Additions:    stats.Additions,
				Deletions:    stats.Deletions,
			}
		}
	} else if branchesExist {
		diffURL = fmt.Sprintf("/projects/%s/repository/compare?from=%s&to=%s",
			rc.Project.ID, url.QueryEscape(mr.TargetBranch), url.QueryEscape(mr.SourceBranch))
	}

	if !branchesExist {
		// The branches are gone, so there is no diff to show. Saying so is better
		// than an error page: the request itself is still perfectly readable, and
		// there is nothing left to merge.
		view.MergeStatus = "branches_gone"
	}

	notes, err := s.store.MergeRequests().Notes(r.Context(), mr.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The diff address is relative to the API root, without the version prefix:
	// the client prepends that itself, and a prefix here would end up doubled.
	response := map[string]any{
		"merge_request": view,
		"notes":         notes,
		"diff_url":      diffURL,
	}
	if !branchesExist {
		response["branches_gone"] = true
	}

	s.writeJSON(w, r, http.StatusOK, response)
}

// handleUpdateMergeRequest edits an open merge request.
func (s *Server) handleUpdateMergeRequest(w http.ResponseWriter, r *http.Request) {
	rc, mr, err := s.mergeRequestContext(r, store.ActionPush)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !mr.IsOpen() {
		s.writeError(w, r, errBadRequestf("this merge request is %s and can no longer be edited", mr.State))
		return
	}

	var req mergeRequestRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	fields := store.UpdateFields{}
	if req.Title != "" {
		title := strings.TrimSpace(req.Title)
		fields.Title = &title
	}
	if strings.TrimSpace(req.Description) != mr.Description {
		description := req.Description
		fields.Description = &description
	}
	squash := req.Squash
	fields.Squash = &squash

	updated, err := s.store.MergeRequests().Update(r.Context(), mr.ID, fields)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.publishMergeRequest(r, rc, updated, "updated")
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"merge_request": s.mergeRequestToView(r, updated),
	})
}

// handleMergeMergeRequest performs the merge.
func (s *Server) handleMergeMergeRequest(w http.ResponseWriter, r *http.Request) {
	// The request is looked up with read access and the merge permission is checked
	// afterwards, so that "you may read this project but may not merge here" is
	// answered with the reason. Asking for merge access up front would answer a
	// stranger and a maintainer identically, and neither learns anything.
	rc, mr, err := s.mergeRequestContext(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	allowed, err := s.store.Permissions().Can(r.Context(), userFrom(r.Context()), rc.Project, store.ActionMergeMR)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !allowed {
		if !rc.Project.AllowMerge {
			s.writeError(w, r, errForbidden("merging is disabled for this project"))
			return
		}
		s.writeError(w, r, errForbidden("merging a merge request needs the maintainer role"))
		return
	}

	// The project's own merge policy is the default; the request may override it
	// per merge.
	method := gitx.MergeMethod(rc.Project.MergeMethod)
	if method == "" {
		method = gitx.MergeMethodMerge
	}
	if mr.Squash {
		method = gitx.MergeMethodSquash
	}

	var body struct {
		Method       string            `json:"method"`
		ShouldRemove bool              `json:"should_remove_source_branch"`
		Resolutions  []gitx.Resolution `json:"resolutions"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			s.writeError(w, r, err)
			return
		}
		if body.Method != "" {
			method = gitx.MergeMethod(body.Method)
		}
	}
	switch method {
	case gitx.MergeMethodMerge, gitx.MergeMethodFF, gitx.MergeMethodSquash:
	default:
		s.writeError(w, r, errBadRequestf("unknown merge method %q", method))
		return
	}

	if !mr.IsOpen() {
		s.writeError(w, r, errBadRequestf("this merge request is already %s", mr.State))
		return
	}
	if mr.PipelineRequired {
		var status string
		err := s.store.Pool().QueryRow(r.Context(),
			`SELECT status FROM pipelines WHERE project_id = $1 AND ref = $2 ORDER BY id DESC LIMIT 1`,
				rc.Project.ID, mr.SourceBranch).Scan(&status)
		if err != nil || status != "success" {
			s.writeError(w, r, errConflict("this merge request can be merged after its source branch pipeline succeeds"))
			return
		}
	}
	user := userFrom(r.Context())
	author := displayName(user)

	var (
		result *gitx.MergeResult
		merged *models.MergeRequest
	)

	// The merge and the state change are one decision: the lock keeps a second
	// merge from starting between the git operation and the record of it, and the
	// conditional ref update inside the merge catches a push that lands in
	// between.
	err = s.store.WithProjectLock(r.Context(), rc.Project.ID.String(), func() error {
		current, err := s.store.MergeRequests().ByIID(r.Context(), rc.Project.ID, mr.IID)
		if err != nil {
			return err
		}
		if !current.IsOpen() {
			return errBadRequestf("this merge request is already %s", current.State)
		}

		options := gitx.MergeOptions{
			TargetBranch: mr.TargetBranch, SourceBranch: mr.SourceBranch,
			Method: method,
			Message: fmt.Sprintf("Merge branch '%s' into '%s'\n\n%s",
				mr.SourceBranch, mr.TargetBranch, firstLine(mr.Title)),
			AuthorName: author, AuthorEmail: user.Email,
			CommitterName: author, CommitterEmail: user.Email,
		}

		if len(body.Resolutions) > 0 {
			// A resolution is only accepted for a merge that actually conflicts:
			// silently ignoring extra resolutions would let a caller believe a
			// conflict was resolved when it never was.
			conflictedTree, probe, err := s.git.PeekConflicts(r.Context(), rc.RepoDir,
				resolveOrEmpty(r, s, rc, mr.TargetBranch), resolveOrEmpty(r, s, rc, mr.SourceBranch))
			if err != nil {
				return err
			}
			if len(probe) == 0 {
				return errBadRequest("these branches do not conflict, so there is nothing to resolve")
			}
			// A named stage is turned into a blob here, where the three versions
			// are known: the tree merge-tree produced has no stages left in it, so
			// "take theirs" has to be resolved against the tips rather than against
			// the tree.
			base, err := s.git.MergeBase(r.Context(), rc.RepoDir,
				resolveOrEmpty(r, s, rc, mr.TargetBranch), resolveOrEmpty(r, s, rc, mr.SourceBranch))
			if err != nil {
				return err
			}
			versions, err := s.git.ConflictVersions(r.Context(), rc.RepoDir, base,
				resolveOrEmpty(r, s, rc, mr.TargetBranch), resolveOrEmpty(r, s, rc, mr.SourceBranch), probe)
			if err != nil {
				return err
			}

			byPath := map[string]gitx.ConflictStage{}
			for _, version := range versions {
				byPath[version.Path] = version
			}

			resolved := make([]gitx.Resolution, 0, len(body.Resolutions))
			for _, resolution := range body.Resolutions {
				// Written text is passed through: the merge writes it as a blob.
				if resolution.Text != "" {
					if _, known := byPath[resolution.Path]; !known {
						return errBadRequestf("%q is not one of the conflicted files", resolution.Path)
					}
					resolved = append(resolved, resolution)
					continue
				}
				if resolution.Blob == "" {
					version, known := byPath[resolution.Path]
					if !known {
						return errBadRequestf("%q is not one of the conflicted files", resolution.Path)
					}
					switch resolution.Stage {
					case "base":
						resolution.Blob = version.Base
					case "ours":
						resolution.Blob = version.Ours
					case "theirs":
						resolution.Blob = version.Theirs
					default:
						return errBadRequestf("resolution for %q must name base, ours or theirs", resolution.Path)
					}
					if resolution.Blob == "" {
						return errBadRequestf("%q has no %s version", resolution.Path, resolution.Stage)
					}
				}
				resolved = append(resolved, resolution)
			}
			body.Resolutions = resolved
			result, err = s.git.CompleteMerge(r.Context(), rc.RepoDir, options, conflictedTree, body.Resolutions)
			if err != nil {
				return err
			}
		} else {
			result, err = s.git.Merge(r.Context(), rc.RepoDir, options)
			if err != nil {
				return err
			}
			if len(result.Conflicts) > 0 {
				return errConflictf("the branches conflict in %d %s: %s",
					len(result.Conflicts), pluralFiles(len(result.Conflicts)),
					strings.Join(result.Conflicts, ", "))
			}
		}

		merged, err = s.store.MergeRequests().MarkMerged(r.Context(), current.ID, result.CommitSHA, user.ID)
		if err != nil {
			return err
		}

		// Removing the source branch is part of the merge, not a separate action:
		// leaving it behind after the work is on the target branch is how a
		// repository fills up with branches that are already merged.
		if rc.Project.RemoveSourceBranch || body.ShouldRemove || mr.RemoveSourceBranch {
			if err := s.git.DeleteRef(r.Context(), rc.RepoDir, "refs/heads/"+mr.SourceBranch); err != nil {
				// The merge itself succeeded; failing the whole request now would
				// report a failure for work that is already done.
				s.log.Warn("remove the merged source branch",
					"project", rc.Project.Path, "branch", mr.SourceBranch, "error", err)
			}
		}
		return nil
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("merge request merged", "project", rc.Project.Path, "iid", mr.IID,
		"method", method, "commit", result.CommitSHA)

	s.publishMergeRequest(r, rc, merged, "merged")
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"merge_request": s.mergeRequestToView(r, merged),
		"commit_sha":    result.CommitSHA,
	})
}

// handleMergeRequestConflicts returns the three versions of every conflicted
// file, so the interface can offer a choice per file.
//
// Nothing is written: the merge is computed in memory and thrown away, which is
// the only way to answer "what would conflict" without touching the repository.
func (s *Server) handleMergeRequestConflicts(w http.ResponseWriter, r *http.Request) {
	rc, mr, err := s.mergeRequestContext(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !mr.IsOpen() {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"conflicts": []any{}})
		return
	}

	target, err := s.git.RevParse(r.Context(), rc.RepoDir, mr.TargetBranch)
	if err != nil {
		s.writeError(w, r, errNotFoundf("branch %q does not exist", mr.TargetBranch))
		return
	}
	source, err := s.git.RevParse(r.Context(), rc.RepoDir, mr.SourceBranch)
	if err != nil {
		s.writeError(w, r, errNotFoundf("branch %q does not exist", mr.SourceBranch))
		return
	}

	conflictedTree, conflictNames, err := s.git.PeekConflicts(r.Context(), rc.RepoDir, target, source)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(conflictNames) == 0 {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"conflicts": []any{}})
		return
	}

	base, err := s.git.MergeBase(r.Context(), rc.RepoDir, target, source)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	stages, err := s.git.ConflictVersions(r.Context(), rc.RepoDir, base, target, source, conflictNames)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	type conflictView struct {
		Path   string `json:"path"`
		Status string `json:"status"`
		Base   string `json:"base"`
		Ours   string `json:"ours"`
		Theirs string `json:"theirs"`
		// The content of each side, so the interface can show them without a
		// request per file and per side.
		BaseText   string `json:"base_text,omitempty"`
		OursText   string `json:"ours_text,omitempty"`
		TheirsText string `json:"theirs_text,omitempty"`
	}

	out := []conflictView{}
	for _, stage := range stages {
		item := conflictView{
			Path: stage.Path, Status: stage.Status,
			Base: stage.Base, Ours: stage.Ours, Theirs: stage.Theirs,
		}
		// A side that does not exist is an addition, not an empty file, and the
		// interface has to be able to tell those apart.
		item.BaseText = s.blobText(r, rc, stage.Base)
		item.OursText = s.blobText(r, rc, stage.Ours)
		item.TheirsText = s.blobText(r, rc, stage.Theirs)
		out = append(out, item)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"conflicts":       out,
		"conflicted_tree": conflictedTree,
		"target_branch":   mr.TargetBranch,
		"source_branch":   mr.SourceBranch,
	})
}

// blobText returns the content of a blob, or an empty string when there is none.
func (s *Server) blobText(r *http.Request, rc *repoContext, blobSHA string) string {
	if blobSHA == "" {
		return ""
	}
	content, _, _, err := s.git.CatFileBlob(r.Context(), rc.RepoDir, blobSHA)
	if err != nil {
		return ""
	}
	return string(content)
}

// handleCloseMergeRequest closes a request without merging, or reopens one.
func (s *Server) handleCloseMergeRequest(w http.ResponseWriter, r *http.Request) {
	rc, mr, err := s.mergeRequestContext(r, store.ActionPush)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body struct {
		State string `json:"state"`
	}
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &body); err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	var result *models.MergeRequest
	switch body.State {
	case string(models.MRStateClosed):
		result, err = s.store.MergeRequests().MarkClosed(r.Context(), mr.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	case string(models.MRStateOpened):
		result, err = s.store.MergeRequests().Reopen(r.Context(), mr.ID)
		if err != nil {
			if errors.Is(err, store.ErrConflict) {
				s.writeError(w, r, errConflict(
					"a merged merge request cannot be reopened: its commits are already on the target branch"))
				return
			}
			s.writeError(w, r, err)
			return
		}
	default:
		s.writeError(w, r, errBadRequest(`state must be "closed" or "opened"`))
		return
	}

	s.publishMergeRequest(r, rc, result, body.State)
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"merge_request": s.mergeRequestToView(r, result),
	})
}

// handleAddMergeRequestNote posts a comment.
func (s *Server) handleAddMergeRequestNote(w http.ResponseWriter, r *http.Request) {
	_, mr, err := s.mergeRequestContext(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body struct {
		Body string `json:"body"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" {
		s.writeError(w, r, errBadRequest("a comment cannot be empty"))
		return
	}

	note, err := s.store.MergeRequests().AddNote(r.Context(), mr.ID, userFrom(r.Context()).ID, body.Body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"note": note})
}

// mergeability reports whether the branches can be merged, without touching them.
//
// A three-way merge is computed in memory and thrown away: the answer is needed
// on every page view, and it must not have side effects on the repository.
func (s *Server) mergeability(r *http.Request, rc *repoContext, source, target string) (string, []string, error) {
	base, err := s.git.MergeBase(r.Context(), rc.RepoDir, target, source)
	if err != nil || base == "" {
		return "unknown", nil, nil
	}

	// An unrelated history has no common ancestor to merge from, so no diff can
	// be shown and no merge can be trusted.
	targetSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, target)
	if err != nil {
		return "unknown", nil, nil
	}
	sourceSHA, err := s.git.RevParse(r.Context(), rc.RepoDir, source)
	if err != nil {
		return "unknown", nil, nil
	}
	if targetSHA == sourceSHA {
		return "up_to_date", nil, nil
	}

	if ff, err := s.git.IsAncestor(r.Context(), rc.RepoDir, target, source); err == nil && ff {
		return "can_fast_forward", nil, nil
	}
	if ff, err := s.git.IsAncestor(r.Context(), rc.RepoDir, source, target); err == nil && ff {
		return "up_to_date", nil, nil
	}

	_, conflicts, err := s.git.PeekConflicts(r.Context(), rc.RepoDir, target, source)
	if err != nil {
		return "unknown", nil, nil
	}
	return "can_merge", conflicts, nil
}

// mergeRequestToView adds the computed fields a page needs.
func (s *Server) mergeRequestToView(r *http.Request, mr *models.MergeRequest) *models.MergeRequest {
	view := *mr
	if mr.Project != nil {
		// The web address of the request, so a client does not have to build it
		// out of the project path and the number in two different places.
		view.URL = "/p/" + mr.Project.Path + "/-/merge_requests/" + fmt.Sprint(mr.IID)
	}
	return &view
}

// publishMergeRequest records what happened to a merge request.
func (s *Server) publishMergeRequest(r *http.Request, rc *repoContext, mr *models.MergeRequest, action string) {
	user := userFrom(r.Context())
	s.publish(r.Context(), rc, user, models.EventMergeRequestChanged, map[string]any{
		"action":        action,
		"merge_request": mr.IID,
		"title":         mr.Title,
		"source_branch": mr.SourceBranch,
		"target_branch": mr.TargetBranch,
		"state":         mr.State,
	})
}

// mustRev resolves a ref, returning an empty string when it does not exist so the
// caller reports a conflict rather than dereferencing nothing.
func resolveOrEmpty(r *http.Request, s *Server, rc *repoContext, ref string) string {
	sha, err := s.git.RevParse(r.Context(), rc.RepoDir, ref)
	if err != nil {
		return ""
	}
	return sha
}

func mergeStatusName(mergeable string, hasConflicts bool) string {
	switch {
	case hasConflicts:
		return "conflicts"
	case mergeable == "can_fast_forward":
		return "can_fast_forward"
	case mergeable == "up_to_date":
		return "up_to_date"
	case mergeable == "can_merge":
		return "can_merge"
	default:
		return "unknown"
	}
}

func pluralFiles(n int) string {
	if n == 1 {
		return "file"
	}
	return "files"
}
