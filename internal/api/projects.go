package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

func parseTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339, value)
}

// projectView is the JSON shape of a project in the API.
type projectView struct {
	ID            uuid.UUID  `json:"id"`
	Path          string     `json:"path"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Visibility    string     `json:"visibility"`
	DefaultBranch string     `json:"default_branch"`
	GroupID       *uuid.UUID `json:"group_id,omitempty"`
	AccessLevel   int        `json:"access_level"`
	AccessName    string     `json:"access_name"`

	AllowMerge           bool   `json:"allow_merge"`
	MergeMethod          string `json:"merge_method"`
	RemoveSourceBranch   bool   `json:"remove_source_branch"`
	AllowPipelineTrigger bool   `json:"allow_pipeline_trigger"`

	SSHURL  string `json:"ssh_url"`
	HTTPURL string `json:"http_url"`
	WebURL  string `json:"web_url"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

func (s *Server) projectToView(p *models.Project, level int) projectView {
	host := s.cfg.SSHHost
	return projectView{
		ID:            p.ID,
		Path:          p.Path,
		Name:          p.Name,
		Description:   p.Description,
		Visibility:    p.Visibility,
		DefaultBranch: p.DefaultBranch,
		GroupID:       p.GroupID,
		AccessLevel:   level,
		AccessName:    models.AccessLevelName(level),

		AllowMerge:           p.AllowMerge,
		MergeMethod:          p.MergeMethod,
		RemoveSourceBranch:   p.RemoveSourceBranch,
		AllowPipelineTrigger: p.AllowPipelineTrigger,

		// The SSH URL is the canonical one: the system sshd terminates the
		// connection, and the port appears only when it is not the standard 22.
		SSHURL:  s.cfg.CloneURL(p.Path),
		HTTPURL: "http://" + host + "/" + p.Path + ".git",
		WebURL:  "/" + p.Path,

		CreatedAt:  p.CreatedAt,
		UpdatedAt:  p.UpdatedAt,
		ArchivedAt: p.ArchivedAt,
	}
}

// handleListPlaces is the one list of where to work.
//
// Projects and groups are rows in the same table, because to somebody looking for
// somewhere to work they are the same kind of thing: a name, a path, an address. Two
// lists would mean two searches and a page to find twice.
//
// Search, filters and a page size are all here rather than in the interface, because
// the list a person is shown has to be a list the database chose. Loading everything
// and filtering it in the browser is how a page stops opening after a year.
func (s *Server) handleListPlaces(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	query := store.ListingQuery{
		Search:     r.URL.Query().Get("search"),
		Kind:       r.URL.Query().Get("type"),
		Visibility: r.URL.Query().Get("visibility"),
		Scope:      r.URL.Query().Get("scope"),
		Sort:       r.URL.Query().Get("sort"),
		Direction:  r.URL.Query().Get("direction"),
		Page:       atoiOr(r.URL.Query().Get("page"), 1),
		PerPage:    atoiOr(r.URL.Query().Get("per_page"), store.DefaultPageSize),
	}

	rows, total, err := s.store.ListVisible(r.Context(), user.ID, query)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	places := make([]placeView, 0, len(rows))
	for _, row := range rows {
		places = append(places, placeView{
			ID:           row.ID,
			Kind:         row.Kind,
			Path:         row.Path,
			Name:         row.Name,
			Description:  row.Description,
			Visibility:   row.Visibility,
			GroupID:      row.GroupID,
			ProjectCount: row.ProjectCount,
			AccessLevel:  row.AccessLevel,
			AccessName:   models.AccessLevelName(row.AccessLevel),
			LastCommitAt: row.LastCommitAt,
			LastCommitMessage: row.LastCommitMessage,
			LatestPipelineStatus: row.LatestPipelineStatus,
			LatestPipelineAt: row.LatestPipelineAt,
			OpenMergeRequests: row.OpenMergeRequests,
			OpenIssues: row.OpenIssues,
		})
	}

	// A page past the end says where the end is, rather than showing nothing and
	// leaving a person to conclude the list is empty.
	pages := (total + query.PerPage - 1) / query.PerPage
	if pages < 1 {
		pages = 1
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"places":   places,
		"total":    total,
		"page":     query.Page,
		"pages":    pages,
		"per_page": query.PerPage,
	})
}

// placeView is one row of that list, of either kind.
type placeView struct {
	ID           uuid.UUID  `json:"id"`
	Kind         string     `json:"kind"`
	Path         string     `json:"path"`
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Visibility   string     `json:"visibility"`
	GroupID      *uuid.UUID `json:"group_id,omitempty"`
	ProjectCount *int       `json:"project_count,omitempty"`
	AccessLevel  int        `json:"access_level"`
	AccessName   string     `json:"access_name"`
	LastCommitAt *time.Time `json:"last_commit_at,omitempty"`
	LastCommitMessage string `json:"last_commit_message,omitempty"`
	LatestPipelineStatus string `json:"latest_pipeline_status,omitempty"`
	LatestPipelineAt *time.Time `json:"latest_pipeline_at,omitempty"`
	OpenMergeRequests int `json:"open_merge_requests"`
	OpenIssues int `json:"open_issues"`
}

// atoiOr reads a number from a query, falling back when it is absent or nonsense.
//
// A page that is not a number is a page that cannot be requested, and answering with
// the first page rather than an error is what a person typing into a box wants.
func atoiOr(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	number, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return number
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	projects, levels, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]projectView, 0, len(projects))
	for _, p := range projects {
		views = append(views, s.projectToView(p, levels[p.ID]))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"projects": views})
}

type createProjectRequest struct {
	Path                 string `json:"path"`
	Name                 string `json:"name"`
	Description          string `json:"description"`
	Visibility           string `json:"visibility"`
	GroupPath            string `json:"group_path"`
	InitializeWithReadme bool   `json:"initialize_with_readme"`
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	path := strings.ToLower(strings.Trim(strings.TrimSpace(req.Path), "/"))
	if path == "" {
		s.writeError(w, r, errBadRequest("a project path is required"))
		return
	}
	if strings.HasSuffix(path, ".git") {
		path = strings.TrimSuffix(path, ".git")
	}
	if !repos.ValidPath(path) {
		s.writeError(w, r, errBadRequest(
			"a project path may contain lowercase letters, digits, '.', '_' and '-', separated by slashes"))
		return
	}

	user := userFrom(r.Context())
	params := repos.CreateParams{
		Path:        path,
		Name:        req.Name,
		Description: req.Description,
		Visibility:  req.Visibility,
		OwnerID:     user.ID,
	}

	if req.GroupPath != "" {
		group, err := s.store.Groups().ByPath(r.Context(), strings.Trim(req.GroupPath, "/"))
		if err != nil {
			s.writeError(w, r, errNotFound("group does not exist"))
			return
		}
		allowed, err := s.store.Permissions().CanGroup(r.Context(), user, group.ID, store.ActionManageGroup)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !allowed {
			s.writeError(w, r, errForbidden("you cannot create projects in this group"))
			return
		}
		params.GroupID = &group.ID
		params.Path = group.FullPath + "/" + path
	}

	project, err := s.repos.Create(r.Context(), params)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.writeError(w, r, errConflict("a project with this path already exists"))
			return
		}
		s.writeError(w, r, err)
		return
	}

	// The creator becomes owner; this is what grants them manage_project.
	if _, err := s.store.Permissions().AssignProjectRole(r.Context(), project.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &user.ID, nil); err != nil {
		s.writeError(w, r, err)
		return
	}

	if req.InitializeWithReadme {
		if err := s.seedReadme(r, project); err != nil {
			s.log.Warn("seed repository", "project", project.Path, "error", err)
		}
	}

	s.log.Info("project created", "path", project.Path, "owner", user.Username)
	s.writeJSON(w, r, http.StatusCreated,
		map[string]any{"project": s.projectToView(project, models.AccessLevelOwner)})
}

// handleMoveProject moves a project into a group, or out of one.
func (s *Server) handleMoveProject(w http.ResponseWriter, r *http.Request) {
	rc, err := s.repoWithAccess(r, store.ActionManageProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Moving changes a project's address and the directory its history lives in,
	// which is a bigger step than renaming it. It is left to owners on both sides:
	// whoever owns the project, and whoever owns the group it is going into.
	if rc.Level < models.AccessLevelOwner && !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("moving a project needs the owner role"))
		return
	}

	var req struct {
		GroupPath string `json:"group_path"`
		// Name overrides the last segment of the path, for moving into a group
		// under a different name. Empty keeps it.
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	var (
		groupID *uuid.UUID
		target  *models.Group
	)

	groupPath := strings.ToLower(strings.Trim(strings.TrimSpace(req.GroupPath), "/"))
	if groupPath != "" {
		group, err := s.store.Groups().ByPath(r.Context(), groupPath)
		if err != nil {
			s.writeError(w, r, errNotFoundf("group %q does not exist", groupPath))
			return
		}

		// Being able to read a group is not being able to put a project in it: the
		// group's members gain access to the project, so this is an owner's
		// decision on the group as well as on the project.
		level, err := s.store.Permissions().GroupAccessLevel(r.Context(), userFrom(r.Context()).ID, group.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if level < models.AccessLevelOwner && !userFrom(r.Context()).IsAdmin {
			s.writeError(w, r, errForbidden("moving a project into a group needs the owner role in that group"))
			return
		}
		groupID, target = &group.ID, group
	}

	name := strings.ToLower(strings.Trim(strings.TrimSpace(req.Name), "/"))
	if name == "" {
		name = lastSegmentOf(rc.Project.Path)
	}

	newPath := name
	if target != nil {
		newPath = target.FullPath + "/" + name
	}
	if newPath == rc.Project.Path {
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"project": s.projectToView(rc.Project, models.AccessLevelOwner),
		})
		return
	}
	if !repos.ValidPath(newPath) {
		s.writeError(w, r, errBadRequestf("%q is not a valid project path", newPath))
		return
	}

	moved, err := s.repos.Move(r.Context(), rc.Project, newPath, groupID)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			s.writeError(w, r, errConflictf("a project at %s already exists", newPath))
			return
		}
		s.writeError(w, r, err)
		return
	}

	// A push that arrives between reading and writing the row would report the
	// old path, so the event carries the new one.
	_ = s.git.UpdateServerInfo(r.Context(), rc.RepoDir)
	s.log.Info("project moved",
		"project", rc.Project.Path, "from", moved.FromPath, "to", moved.ToPath,
		"user", userFrom(r.Context()).Username)

	s.publish(r.Context(), rc, userFrom(r.Context()), models.EventProjectUpdated, map[string]any{
		"action": "moved",
		"from":   moved.FromPath,
		"to":     moved.ToPath,
	})

	updated, err := s.store.Projects().ByID(r.Context(), rc.Project.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"project": s.projectToView(updated, models.AccessLevelOwner),
		"web_url": "/p/" + moved.ToPath,
	})
}

// lastSegmentOf is the part of a path after the last slash.
func lastSegmentOf(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	project, level, err := s.projectWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"project": s.projectToView(project, level)})
}

type updateProjectRequest struct {
	Name                 *string `json:"name"`
	Description          *string `json:"description"`
	Visibility           *string `json:"visibility"`
	DefaultBranch        *string `json:"default_branch"`
	AllowMerge           *bool   `json:"allow_merge"`
	MergeMethod          *string `json:"merge_method"`
	RemoveSourceBranch   *bool   `json:"remove_source_branch"`
	AllowPipelineTrigger *bool   `json:"allow_pipeline_trigger"`
	// MoveToGroup moves the project into a group, or out of one when it is empty.
	// Moving changes the project's path, and therefore its address and the place
	// its repository lives, so it is a separate request rather than another field
	// of the same one.
	MoveToGroup *string `json:"move_to_group"`
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	project, level, err := s.projectWithAccess(r, store.ActionManageProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req updateProjectRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	// Only fields present in the request are changed, which keeps concurrent
	// edits from different pages from clobbering each other.
	if req.Name != nil {
		project.Name = *req.Name
	}
	if req.Description != nil {
		project.Description = *req.Description
	}
	if req.Visibility != nil {
		switch *req.Visibility {
		case "private", "internal", "public":
			project.Visibility = *req.Visibility
		default:
			s.writeError(w, r, errBadRequest("visibility must be private, internal or public"))
			return
		}
	}
	if req.DefaultBranch != nil {
		project.DefaultBranch = *req.DefaultBranch
	}
	if req.AllowMerge != nil {
		project.AllowMerge = *req.AllowMerge
	}
	if req.MergeMethod != nil {
		switch *req.MergeMethod {
		case "merge", "ff", "squash":
			project.MergeMethod = *req.MergeMethod
		default:
			s.writeError(w, r, errBadRequest("merge_method must be merge, ff or squash"))
			return
		}
	}
	if req.RemoveSourceBranch != nil {
		project.RemoveSourceBranch = *req.RemoveSourceBranch
	}
	if req.AllowPipelineTrigger != nil {
		project.AllowPipelineTrigger = *req.AllowPipelineTrigger
	}

	if err := s.store.Projects().Update(r.Context(), project); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"project": s.projectToView(project, level)})
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionManageProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.repos.Delete(r.Context(), project.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.log.Info("project deleted", "path", project.Path)
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

func (s *Server) handleListProjectMembers(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	members, err := s.store.Projects().Members(r.Context(), project.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]map[string]any, 0, len(members))
	for _, id := range members {
		user, err := s.store.Users().ByID(r.Context(), id)
		if err != nil {
			continue
		}
		level, err := s.store.Permissions().AccessLevel(r.Context(), id, project.ID)
		if err != nil {
			continue
		}
		views = append(views, map[string]any{
			"user":         user,
			"access_level": level,
			"access_name":  models.AccessLevelName(level),
		})
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"members": views})
}

func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	// Groups the user can see are those they belong to, plus those where they
	// hold a role.
	rows, err := s.store.Pool().Query(r.Context(), `
		SELECT DISTINCT g.id, g.slug, g.name, g.full_path, g.created_at
		FROM groups g
		LEFT JOIN group_members m ON m.group_id = g.id AND m.user_id = $1
		LEFT JOIN group_roles r ON r.group_id = g.id
		WHERE m.user_id = $1 OR r.source_user_id = $1
		   OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
		ORDER BY g.full_path`, user.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	defer rows.Close()

	type groupView struct {
		ID          uuid.UUID `json:"id"`
		Slug        string    `json:"slug"`
		Name        string    `json:"name"`
		FullPath    string    `json:"full_path"`
		AccessLevel int       `json:"access_level"`
		AccessName  string    `json:"access_name"`
	}

	groups := []groupView{}
	for rows.Next() {
		var g models.Group
		if err := rows.Scan(&g.ID, &g.Slug, &g.Name, &g.FullPath, &g.CreatedAt); err != nil {
			s.writeError(w, r, err)
			return
		}
		level, err := s.store.Permissions().GroupAccessLevel(r.Context(), user.ID, g.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		groups = append(groups, groupView{
			ID: g.ID, Slug: g.Slug, Name: g.Name, FullPath: g.FullPath,
			AccessLevel: level, AccessName: models.AccessLevelName(level),
		})
	}
	if err := rows.Err(); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"groups": groups})
}

type createGroupRequest struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	var req createGroupRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	slug := strings.ToLower(strings.Trim(strings.TrimSpace(req.Slug), "/"))
	if strings.Contains(slug, "/") || !repos.ValidPath(slug) {
		s.writeError(w, r, errBadRequest("a group slug may contain lowercase letters, digits, '.', '_' and '-'"))
		return
	}

	user := userFrom(r.Context())
	group, err := s.store.Groups().Create(r.Context(), slug, req.Name)
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			s.writeError(w, r, errConflict("this group already exists"))
			return
		}
		s.writeError(w, r, err)
		return
	}

	if err := s.store.Groups().AddMember(r.Context(), group.ID, user.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	if _, err := s.store.Permissions().AssignGroupRole(r.Context(), group.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &user.ID, nil); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"group": map[string]any{
			"id": group.ID, "slug": group.Slug, "name": group.Name,
			"full_path": group.FullPath, "access_level": models.AccessLevelOwner,
		},
	})
}

func (s *Server) handleGetGroup(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "groupID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("invalid group id"))
		return
	}

	group, err := s.store.Groups().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	level, err := s.store.Permissions().GroupAccessLevel(r.Context(), userFrom(r.Context()).ID, id)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if level == store.NoAccess && !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errNotFound("group does not exist"))
		return
	}

	// Whether the reader may change anything here, said once here rather than worked
	// out again by the page: the page has a Notifications tab, and a tab that shows a
	// form nobody may fill in is worse than a tab that says what is allowed.
	canManage := userFrom(r.Context()).IsAdmin
	if !canManage {
		canManage, err = s.store.Permissions().CanGroup(r.Context(), userFrom(r.Context()),
			id, store.ActionManageGroup)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"group": map[string]any{
			"id": group.ID, "slug": group.Slug, "name": group.Name,
			"description": group.Description,
			"full_path":   group.FullPath, "access_level": level,
			"access_name": models.AccessLevelName(level),
		},
		"can_manage": canManage,
	})
}

func (s *Server) handleListGroupProjects(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "groupID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("invalid group id"))
		return
	}
	if _, err := s.store.Groups().ByID(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}

	user := userFrom(r.Context())
	projects, levels, err := s.store.Projects().ListVisible(r.Context(), user.ID, "")
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := []projectView{}
	for _, p := range projects {
		if p.GroupID != nil && *p.GroupID == id {
			views = append(views, s.projectToView(p, levels[p.ID]))
		}
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"projects": views})
}

// projectWithAccess loads the project named in the URL and checks one action
// against it. Every project-scoped handler goes through here, so authorisation
// cannot be forgotten in a new endpoint.
func (s *Server) projectWithAccess(r *http.Request, action store.Action) (*models.Project, int, error) {
	project, err := s.resolveProject(r)
	if err != nil {
		return nil, 0, err
	}

	user := userFrom(r.Context())
	allowed, err := s.store.Permissions().Can(r.Context(), user, project, action)
	if err != nil {
		return nil, 0, err
	}
	if !allowed {
		return nil, 0, errNotFound("project does not exist")
	}

	level, err := s.store.Permissions().AccessLevel(r.Context(), user.ID, project.ID)
	if err != nil {
		return nil, 0, err
	}
	return project, level, nil
}

// resolveProject loads the project named in the URL. The path parameter accepts
// either a UUID or a project path such as "group/project", because the frontend
// navigates by path while internal links use identifiers.
func (s *Server) resolveProject(r *http.Request) (*models.Project, error) {
	param := strings.Trim(strings.TrimSpace(pathParam(r, "projectID")), "/")
	if param == "" {
		return nil, errBadRequest("a project is required")
	}

	if id, err := uuid.Parse(param); err == nil {
		return s.store.Projects().ByID(r.Context(), id)
	}
	if !repos.ValidPath(strings.ToLower(param)) {
		return nil, errBadRequest("invalid project id or path")
	}
	return s.store.Projects().ByPath(r.Context(), strings.ToLower(param))
}

// repoContext bundles everything the repository handlers need.
type repoContext struct {
	Project *models.Project
	RepoDir string
	Level   int
}

// repoWithAccess resolves the project and its on-disk repository, optionally
// checking a write action.
func (s *Server) repoWithAccess(r *http.Request, action store.Action) (*repoContext, error) {
	project, level, err := s.projectWithAccess(r, action)
	if err != nil {
		return nil, err
	}

	repoDir := s.repos.PathFor(project)
	project.RepoPath = repoDir

	if !s.git.Exists(r.Context(), repoDir, "HEAD") {
		// An empty repository is a valid state, not an error: the UI shows an
		// empty-state page with the clone URL.
		return &repoContext{Project: project, RepoDir: repoDir, Level: level}, nil
	}
	return &repoContext{Project: project, RepoDir: repoDir, Level: level}, nil
}
