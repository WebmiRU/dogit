package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ewolf/dogit/internal/models"
)

type GroupRepo struct{ s *Store }

func (s *Store) Groups() *GroupRepo { return &GroupRepo{s: s} }

const groupColumns = `id, slug, name, full_path, created_at`

func scanGroup(row interface{ Scan(...any) error }) (*models.Group, error) {
	var g models.Group
	err := row.Scan(&g.ID, &g.Slug, &g.Name, &g.FullPath, &g.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func (r *GroupRepo) Create(ctx context.Context, slug, name string) (*models.Group, error) {
	g := &models.Group{Slug: slug, Name: name, FullPath: slug}
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO groups (slug, name, full_path) VALUES ($1, $2, $1) RETURNING id, created_at`,
		slug, name).Scan(&g.ID, &g.CreatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return nil, fmt.Errorf("%w: group %q already exists", ErrConflict, slug)
		}
		return nil, fmt.Errorf("create group: %w", err)
	}
	return g, nil
}

func (r *GroupRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Group, error) {
	return scanGroup(r.s.pool.QueryRow(ctx, `SELECT `+groupColumns+` FROM groups WHERE id = $1`, id))
}

func (r *GroupRepo) ByPath(ctx context.Context, fullPath string) (*models.Group, error) {
	return scanGroup(r.s.pool.QueryRow(ctx, `SELECT `+groupColumns+` FROM groups WHERE full_path = $1`, fullPath))
}

func (r *GroupRepo) AddMember(ctx context.Context, groupID, userID uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`INSERT INTO group_members (group_id, user_id) VALUES ($1, $2)
		 ON CONFLICT DO NOTHING`, groupID, userID)
	if err != nil {
		return fmt.Errorf("add group member: %w", err)
	}
	return nil
}

func (r *GroupRepo) RemoveMember(ctx context.Context, groupID, userID uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`DELETE FROM group_members WHERE group_id = $1 AND user_id = $2`, groupID, userID)
	return err
}

func (r *GroupRepo) MemberIDs(ctx context.Context, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT user_id FROM group_members WHERE group_id = $1`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

type ProjectRepo struct{ s *Store }

func (s *Store) Projects() *ProjectRepo { return &ProjectRepo{s: s} }

const projectColumns = `id, group_id, path, name, description, visibility, default_branch,
	allow_pipeline_trigger, allow_merge, merge_method, remove_source_branch, public_emails,
	created_at, updated_at, archived_at`

func scanProject(row interface{ Scan(...any) error }) (*models.Project, error) {
	var p models.Project
	err := row.Scan(&p.ID, &p.GroupID, &p.Path, &p.Name, &p.Description, &p.Visibility,
		&p.DefaultBranch, &p.AllowPipelineTrigger, &p.AllowMerge, &p.MergeMethod,
		&p.RemoveSourceBranch, &p.PublicEmails, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Create inserts a project row. groupID may be nil for a personal project.
func (r *ProjectRepo) Create(ctx context.Context, p *models.Project) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO projects (group_id, path, name, description, visibility, default_branch,
			merge_method, public_emails)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`,
		p.GroupID, p.Path, p.Name, p.Description, p.Visibility,
		p.DefaultBranch, p.MergeMethod, p.PublicEmails,
	).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return fmt.Errorf("%w: project path %q already taken", ErrConflict, p.Path)
		}
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (r *ProjectRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Project, error) {
	return scanProject(r.s.pool.QueryRow(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
}

// ByPath looks a project up by its path. Group-scoped projects are stored with
// the "group/path" full path so that URL and SSH lookups share one key.
func (r *ProjectRepo) ByPath(ctx context.Context, path string) (*models.Project, error) {
	return scanProject(r.s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE path = $1`, strings.TrimSuffix(path, ".git")))
}

func (r *ProjectRepo) Update(ctx context.Context, p *models.Project) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE projects SET name = $2, description = $3, visibility = $4, default_branch = $5,
			allow_pipeline_trigger = $6, allow_merge = $7, merge_method = $8,
			remove_source_branch = $9, public_emails = $10, updated_at = now()
		WHERE id = $1`,
		p.ID, p.Name, p.Description, p.Visibility, p.DefaultBranch,
		p.AllowPipelineTrigger, p.AllowMerge, p.MergeMethod,
		p.RemoveSourceBranch, p.PublicEmails)
	if err != nil {
		return fmt.Errorf("update project: %w", err)
	}
	return nil
}

func (r *ProjectRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM projects WHERE id = $1`, id)
	return err
}

func (r *ProjectRepo) AddMember(ctx context.Context, projectID, userID uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`INSERT INTO project_members (project_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		projectID, userID)
	if err != nil {
		return fmt.Errorf("add project member: %w", err)
	}
	return nil
}

func (r *ProjectRepo) RemoveMember(ctx context.Context, projectID, userID uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx,
		`DELETE FROM project_members WHERE project_id = $1 AND user_id = $2`, projectID, userID)
	return err
}

func (r *ProjectRepo) Members(ctx context.Context, projectID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT user_id FROM project_members WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListVisible returns projects the user may see, together with the computed
// access level for each. Public and internal projects are included for any
// authenticated user; the rest require membership.
func (r *ProjectRepo) ListVisible(ctx context.Context, userID uuid.UUID, visibilityLimit string) ([]*models.Project, map[uuid.UUID]int, error) {
	const q = `
		SELECT p.id, p.group_id, p.path, p.name, p.description, p.visibility, p.default_branch,
		       p.allow_pipeline_trigger, p.allow_merge, p.merge_method, p.remove_source_branch,
		       p.public_emails, p.created_at, p.updated_at, p.archived_at
		FROM projects p
		WHERE p.archived_at IS NULL
		  AND (p.visibility <> 'private'
		       OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
		       OR EXISTS (SELECT 1 FROM project_roles pr
		                  WHERE pr.project_id = p.id AND pr.source_user_id = $1)
		       OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1))
		ORDER BY p.path`

	rows, err := r.s.pool.Query(ctx, q, userID)
	if err != nil {
		return nil, nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	out := []*models.Project{}
	for rows.Next() {
		var p models.Project
		if err := rows.Scan(&p.ID, &p.GroupID, &p.Path, &p.Name, &p.Description, &p.Visibility,
			&p.DefaultBranch, &p.AllowPipelineTrigger, &p.AllowMerge, &p.MergeMethod,
			&p.RemoveSourceBranch, &p.PublicEmails, &p.CreatedAt, &p.UpdatedAt, &p.ArchivedAt); err != nil {
			return nil, nil, err
		}
		out = append(out, &p)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	levels, err := r.s.Permissions().LevelsForProjects(ctx, userID, out)
	if err != nil {
		return nil, nil, err
	}
	return out, levels, nil
}

type CommitRepo struct{ s *Store }

func (s *Store) Commits() *CommitRepo { return &CommitRepo{s: s} }

// Upsert stores commit snapshots produced by the post-receive hook.
func (r *CommitRepo) Upsert(ctx context.Context, commits []models.Commit) error {
	if len(commits) == 0 {
		return nil
	}
	batch := &pgx.Batch{}
	for _, c := range commits {
		batch.Queue(`
			INSERT INTO commits (sha, project_id, ref, branch, author_name, author_email,
				committer_name, committer_email, message, timestamp)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (project_id, sha) DO NOTHING`,
			c.SHA, c.ProjectID, c.Ref, c.Branch, c.AuthorName, c.AuthorEmail,
			c.CommitterName, c.CommitterEmail, c.Message, c.Timestamp)
	}
	results := r.s.pool.SendBatch(ctx, batch)
	defer results.Close()
	for i := 0; i < len(commits); i++ {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("upsert commit %s: %w", commits[i].SHA, err)
		}
	}
	return nil
}

// ByBranch returns the snapshot feed for a branch, newest first.
func (r *CommitRepo) ByBranch(ctx context.Context, projectID uuid.UUID, branch string, limit int) ([]models.Commit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.s.pool.Query(ctx, `
		SELECT sha, project_id, ref, branch, author_name, author_email, committer_name,
		       committer_email, message, timestamp, added, removed
		FROM commits WHERE project_id = $1 AND branch = $2
		ORDER BY timestamp DESC, sha LIMIT $3`, projectID, branch, limit)
	if err != nil {
		return nil, fmt.Errorf("list commits: %w", err)
	}
	defer rows.Close()

	out := []models.Commit{}
	for rows.Next() {
		var c models.Commit
		if err := rows.Scan(&c.SHA, &c.ProjectID, &c.Ref, &c.Branch, &c.AuthorName, &c.AuthorEmail,
			&c.CommitterName, &c.CommitterEmail, &c.Message, &c.Timestamp, &c.Added, &c.Removed); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type EventRepo struct{ s *Store }

func (s *Store) Events() *EventRepo { return &EventRepo{s: s} }

func (r *EventRepo) Append(ctx context.Context, e *models.Event) error {
	err := r.s.pool.QueryRow(ctx,
		`INSERT INTO events (kind, project_id, actor_id, payload) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		e.Kind, e.ProjectID, e.ActorID, e.Payload).Scan(&e.ID, &e.CreatedAt)
	if err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	return nil
}

// Since returns events with an ID greater than afterID, for polling consumers.
func (r *EventRepo) Since(ctx context.Context, afterID int64, limit int) ([]models.Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, kind, project_id, actor_id, payload, created_at
		FROM events WHERE id > $1 ORDER BY id LIMIT $2`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Event{}
	for rows.Next() {
		var e models.Event
		if err := rows.Scan(&e.ID, &e.Kind, &e.ProjectID, &e.ActorID, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
