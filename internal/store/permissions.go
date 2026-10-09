package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ewolf/dogit/internal/models"
)

// Action is a permission check performed by the API and SSH layers.
type Action string

const (
	ActionReadProject       Action = "read_project"   // view project, browse code, clone
	ActionReadIssues        Action = "read_issues"    // view issues and MRs
	ActionCreateIssue       Action = "create_issue"   // open issues and MRs
	ActionPush              Action = "push"           // push to any branch of a project
	ActionPushProtected     Action = "push_protected" // push to a protected branch
	ActionMergeMR           Action = "merge_mr"       // merge a merge request
	ActionTriggerCI         Action = "trigger_ci"     // run a pipeline manually
	ActionReadCI            Action = "read_ci"        // view pipelines and job logs
	ActionDownloadArtifacts Action = "download_artifacts"
	ActionManageProject     Action = "manage_project" // settings, members, delete
	ActionManageGroup       Action = "manage_group"
	ActionAdmin             Action = "admin"

	// Registry access. Pulling an image is reading the project's code; pushing one
	// is writing it; deleting is managing. A module asks the core these questions
	// rather than keeping its own member list, so an account removed here stops
	// pushing images at the very next request.
	ActionRegistryPull   Action = "registry_pull"
	ActionRegistryPush   Action = "registry_push"
	ActionRegistryDelete Action = "registry_delete"
)

// MinLevel maps an action to the lowest access level that satisfies it.
var MinLevel = map[Action]int{
	ActionReadProject:       models.AccessLevelGuest,
	ActionReadCI:            models.AccessLevelReporter,
	ActionReadIssues:        models.AccessLevelGuest,
	ActionDownloadArtifacts: models.AccessLevelReporter,
	ActionCreateIssue:       models.AccessLevelDeveloper,
	ActionPush:              models.AccessLevelDeveloper,
	ActionTriggerCI:         models.AccessLevelDeveloper,
	ActionPushProtected:     models.AccessLevelMaintainer,
	ActionMergeMR:           models.AccessLevelMaintainer,
	ActionRegistryPull:      models.AccessLevelReporter,
	ActionRegistryPush:      models.AccessLevelDeveloper,
	ActionRegistryDelete:    models.AccessLevelMaintainer,
	ActionManageProject:     models.AccessLevelMaintainer,
	ActionManageGroup:       models.AccessLevelOwner,
	ActionAdmin:             models.AccessLevelOwner,
}

// NoAccess is the level assigned when the user has no access at all.
const NoAccess = 0

// AccessLevel returns the effective level of user on project, combining direct
// project roles, project roles inherited from the parent group, and group
// membership. The result is the maximum across every applicable role.
func (r *PermissionRepo) AccessLevel(ctx context.Context, userID, projectID uuid.UUID) (int, error) {
	var level int
	err := r.s.pool.QueryRow(ctx, levelQuery, userID, projectID).Scan(&level)
	if err == pgx.ErrNoRows {
		return NoAccess, nil
	}
	if err != nil {
		return NoAccess, fmt.Errorf("compute access level: %w", err)
	}
	return level, nil
}

// levelQuery is the single source of truth for access levels. It is also
// reused in bulk by LevelsForProjects.
//
// Two independent sources are combined and the higher wins:
//
//	project roles — attached to the project, granted to a user or to a group
//	group roles   — attached to the project's group, granted to a user or to a
//	                group they belong to
//
// A group role alone is enough to reach a project: a group whose members are all
// developers must not require a role to be duplicated onto every project.
//
// The project's creator receives an explicit "Owner" project role at creation
// time, so no special case is needed here.
const levelQuery = `
	SELECT GREATEST(
		COALESCE((
			SELECT max(r.max_access_level)
			FROM project_roles r
			WHERE r.project_id = $2
			  AND (
			      r.source_user_id = $1
			      OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
			  )
		), 0),
		COALESCE((
			SELECT max(r.max_access_level)
			FROM group_roles r
			WHERE r.group_id = (SELECT group_id FROM projects WHERE id = $2)
			  AND r.group_id IS NOT NULL
			  AND (
			      r.source_user_id = $1
			      OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
			  )
		), 0)
	)`

// LevelsForProjects computes access levels for many projects in one round trip.
func (r *PermissionRepo) LevelsForProjects(ctx context.Context, userID uuid.UUID, projects []*models.Project) (map[uuid.UUID]int, error) {
	if len(projects) == 0 {
		return map[uuid.UUID]int{}, nil
	}
	ids := make([]uuid.UUID, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT p.id, GREATEST(
			COALESCE((
				SELECT max(r.max_access_level)
				FROM project_roles r
				WHERE r.project_id = p.id
				  AND (
				      r.source_user_id = $1
				      OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
				  )
			), 0),
			COALESCE((
				SELECT max(r.max_access_level)
				FROM group_roles r
				WHERE r.group_id = p.group_id
				  AND p.group_id IS NOT NULL
				  AND (
				      r.source_user_id = $1
				      OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
				  )
			), 0)
		) AS level
		FROM projects p WHERE p.id = ANY($2)`, userID, ids)
	if err != nil {
		return nil, fmt.Errorf("compute access levels: %w", err)
	}
	defer rows.Close()

	out := make(map[uuid.UUID]int, len(ids))
	for rows.Next() {
		var id uuid.UUID
		var level int
		if err := rows.Scan(&id, &level); err != nil {
			return nil, err
		}
		out[id] = level
	}
	return out, rows.Err()
}

// GroupAccessLevel returns the effective level of user on a group.
//
// Unlike a project's level, a group's level is not clamped: the clamp exists so a
// group role cannot grant more than the project allows, and a group has no project
// to clamp against. A user who is not a member and holds no role has no access.
func (r *PermissionRepo) GroupAccessLevel(ctx context.Context, userID, groupID uuid.UUID) (int, error) {
	var level int
	err := r.s.pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT max(r.max_access_level)
			FROM group_roles r
			WHERE r.group_id = $2
			  AND (
			      r.source_user_id = $1
			      OR r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)
			  )
		), 0)`, userID, groupID).Scan(&level)
	if err == pgx.ErrNoRows {
		return NoAccess, nil
	}
	if err != nil {
		return NoAccess, fmt.Errorf("compute group access level: %w", err)
	}
	return level, nil
}

// Can reports whether user may perform action on project. Extra project-level
// flags (allow_merge, allow_pipeline_trigger) are honoured here so that the
// whole permission surface funnels through one function.
func (r *PermissionRepo) Can(ctx context.Context, user *models.User, project *models.Project, action Action) (bool, error) {
	if user == nil {
		return false, nil
	}
	if user.IsAdmin {
		return true, nil
	}

	level, err := r.AccessLevel(ctx, user.ID, project.ID)
	if err != nil {
		return false, err
	}

	min, ok := MinLevel[action]
	if !ok {
		return false, fmt.Errorf("unknown action %q", action)
	}
	if level < min {
		return false, nil
	}

	switch action {
	case ActionMergeMR:
		return project.AllowMerge, nil
	case ActionTriggerCI:
		return project.AllowPipelineTrigger, nil
	}
	return true, nil
}

// CanGroup reports whether user may perform action on a group.
func (r *PermissionRepo) CanGroup(ctx context.Context, user *models.User, groupID uuid.UUID, action Action) (bool, error) {
	if user == nil {
		return false, nil
	}
	if user.IsAdmin {
		return true, nil
	}
	level, err := r.GroupAccessLevel(ctx, user.ID, groupID)
	if err != nil {
		return false, err
	}
	min, ok := MinLevel[action]
	if !ok {
		return false, fmt.Errorf("unknown action %q", action)
	}
	return level >= min, nil
}

// AtLeast is the boolean form of AccessLevel, used by the SSH layer.
func (r *PermissionRepo) AtLeast(ctx context.Context, userID, projectID uuid.UUID, min int) (bool, error) {
	level, err := r.AccessLevel(ctx, userID, projectID)
	if err != nil {
		return false, err
	}
	return level >= min, nil
}

type PermissionRepo struct{ s *Store }

func (s *Store) Permissions() *PermissionRepo { return &PermissionRepo{s: s} }

// AssignProjectRole creates a project role bound to a user or a group.
// maxAccess is capped by minAccess so that a role can never grant more than
// the target role allows (the same clamp as GitLab).
func (r *PermissionRepo) AssignProjectRole(ctx context.Context, projectID uuid.UUID, name string, minAccess, maxAccess int, sourceUserID, sourceGroupID *uuid.UUID) (*models.ProjectRole, error) {
	if minAccess < models.AccessLevelGuest || maxAccess < minAccess {
		return nil, fmt.Errorf("invalid access levels: min=%d max=%d", minAccess, maxAccess)
	}
	role := &models.ProjectRole{
		ProjectID: projectID, Name: name,
		MinAccessLevel: minAccess, MaxAccessLevel: maxAccess,
		SourceUserID: sourceUserID, SourceGroupID: sourceGroupID,
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO project_roles (project_id, name, min_access_level, max_access_level, source_user_id, source_group_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		projectID, name, minAccess, maxAccess, sourceUserID, sourceGroupID).Scan(&role.ID)
	if err != nil {
		return nil, fmt.Errorf("assign project role: %w", err)
	}
	return role, nil
}

func (r *PermissionRepo) DeleteProjectRole(ctx context.Context, projectID uuid.UUID, roleID uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM project_roles WHERE id = $1 AND project_id = $2`, roleID, projectID)
	if err != nil {
		return fmt.Errorf("delete project role: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *PermissionRepo) AssignGroupRole(ctx context.Context, groupID uuid.UUID, name string, minAccess, maxAccess int, sourceUserID, sourceGroupID *uuid.UUID) (*models.GroupRole, error) {
	if minAccess < models.AccessLevelGuest || maxAccess < minAccess {
		return nil, fmt.Errorf("invalid access levels: min=%d max=%d", minAccess, maxAccess)
	}
	role := &models.GroupRole{
		GroupID: groupID, Name: name,
		MinAccessLevel: minAccess, MaxAccessLevel: maxAccess,
		SourceUserID: sourceUserID, SourceGroupID: sourceGroupID,
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO group_roles (group_id, name, min_access_level, max_access_level, source_user_id, source_group_id)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		groupID, name, minAccess, maxAccess, sourceUserID, sourceGroupID).Scan(&role.ID)
	if err != nil {
		return nil, fmt.Errorf("assign group role: %w", err)
	}
	return role, nil
}

// ListProjectRoles returns every role configured on a project.
func (r *PermissionRepo) ListProjectRoles(ctx context.Context, projectID uuid.UUID) ([]*models.ProjectRole, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, project_id, name, min_access_level, max_access_level, source_user_id, source_group_id
		FROM project_roles WHERE project_id = $1 ORDER BY min_access_level DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*models.ProjectRole{}
	for rows.Next() {
		var pr models.ProjectRole
		if err := rows.Scan(&pr.ID, &pr.ProjectID, &pr.Name, &pr.MinAccessLevel, &pr.MaxAccessLevel,
			&pr.SourceUserID, &pr.SourceGroupID); err != nil {
			return nil, err
		}
		out = append(out, &pr)
	}
	return out, rows.Err()
}
