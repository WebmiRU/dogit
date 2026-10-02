package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
)

// DashboardStats holds the counters shown on the home page.
type DashboardStats struct {
	Projects           int `json:"projects"`
	Groups             int `json:"groups"`
	OwnedProjects      int `json:"owned_projects"`
	MaintainerProjects int `json:"mainer_projects"`
	SSHKeys            int `json:"ssh_keys"`
}

// DashboardStatsFor computes the counters in a single query.
//
// One round trip matters here: the dashboard is the first request after login, so
// each extra query would show up as a slower first paint.
func (r *PermissionRepo) DashboardStatsFor(ctx context.Context, user *models.User) (*DashboardStats, error) {
	stats := &DashboardStats{}
	if user == nil {
		return stats, nil
	}

	row := r.s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM projects p
			 WHERE p.archived_at IS NULL
			   AND (p.visibility <> 'private'
			     OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
			     OR EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1))),
			(SELECT count(*) FROM groups g
			 WHERE EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = $1)
			    OR EXISTS (SELECT 1 FROM group_roles r WHERE r.group_id = g.id AND r.source_user_id = $1)),
			(SELECT count(*) FROM project_roles WHERE source_user_id = $1 AND max_access_level >= $2),
			(SELECT count(DISTINCT project_id) FROM project_roles
			 WHERE source_user_id = $1 AND max_access_level >= $3),
			(SELECT count(*) FROM ssh_keys WHERE user_id = $1)`,
		user.ID, models.AccessLevelOwner, models.AccessLevelMaintainer)

	if err := row.Scan(
		&stats.Projects, &stats.Groups, &stats.OwnedProjects,
		&stats.MaintainerProjects, &stats.SSHKeys,
	); err != nil {
		return nil, fmt.Errorf("dashboard stats: %w", err)
	}
	return stats, nil
}

// ActivityEntry is one item on the dashboard activity feed, already joined with
// the project and the actor so the frontend does not have to follow up.
type ActivityEntry struct {
	ID          int64            `json:"id"`
	Kind        models.EventKind `json:"kind"`
	CreatedAt   time.Time        `json:"created_at"`
	ProjectID   *uuid.UUID       `json:"project_id,omitempty"`
	ProjectPath string           `json:"project_path"`
	ProjectName string           `json:"project_name"`
	ActorID     *uuid.UUID       `json:"actor_id,omitempty"`
	ActorName   string           `json:"actor_name"`
	Summary     string           `json:"summary"`
	Detail      string           `json:"detail,omitempty"`
	Ref         string           `json:"ref,omitempty"`
}

// RecentActivity returns the newest events across every project the user can see.
func (r *EventRepo) RecentActivity(ctx context.Context, userID uuid.UUID, limit int) ([]ActivityEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT e.id, e.kind, e.created_at, e.project_id, p.path, p.name,
		       e.actor_id, COALESCE(u.username, 'unknown'), e.payload
		FROM events e
		LEFT JOIN projects p ON p.id = e.project_id
		LEFT JOIN users u ON u.id = e.actor_id
		WHERE e.project_id IS NULL
		   OR p.visibility <> 'private'
		   OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
		   OR EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1)
		   OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1)
		ORDER BY e.id DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent activity: %w", err)
	}
	defer rows.Close()

	entries := []ActivityEntry{}
	for rows.Next() {
		var (
			e       ActivityEntry
			payload []byte
		)
		if err := rows.Scan(&e.ID, &e.Kind, &e.CreatedAt, &e.ProjectID, &e.ProjectPath,
			&e.ProjectName, &e.ActorID, &e.ActorName, &payload); err != nil {
			return nil, err
		}
		describeEvent(&e, payload)
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// ProjectActivityEntry is a commit shown in the dashboard's commit feed.
type ProjectActivityEntry struct {
	ProjectID   uuid.UUID `json:"project_id"`
	ProjectPath string    `json:"project_path"`
	ProjectName string    `json:"project_name"`
	SHA         string    `json:"sha"`
	Branch      string    `json:"branch"`
	AuthorName  string    `json:"author_name"`
	Message     string    `json:"message"`
	Timestamp   time.Time `json:"timestamp"`
}

// RecentCommits returns the newest commits across all visible projects, newest
// first. It reads the snapshot table filled by the post-receive hook, so the cost
// does not grow with the size of the repositories.
func (r *CommitRepo) RecentCommitsForUser(ctx context.Context, userID uuid.UUID, limit int) ([]ProjectActivityEntry, error) {
	if limit <= 0 || limit > 200 {
		limit = 20
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT c.project_id, p.path, p.name, c.sha, c.branch, c.author_name, c.message, c.timestamp
		FROM commits c
		JOIN projects p ON p.id = c.project_id
		WHERE p.archived_at IS NULL
		  AND (p.visibility <> 'private'
		    OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
		    OR EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1)
		    OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1))
		ORDER BY c.timestamp DESC, c.sha
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent commits: %w", err)
	}
	defer rows.Close()

	out := []ProjectActivityEntry{}
	for rows.Next() {
		var e ProjectActivityEntry
		if err := rows.Scan(&e.ProjectID, &e.ProjectPath, &e.ProjectName, &e.SHA,
			&e.Branch, &e.AuthorName, &e.Message, &e.Timestamp); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
