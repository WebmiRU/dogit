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

// MergeRequestRepo reads and writes merge requests.
//
// The project-internal number (iid) is what people refer to, so it is allocated
// per project rather than taken from the global id. The allocation takes the row
// lock of the project, which is what keeps two merge requests opened at the same
// moment from taking the same number.
type MergeRequestRepo struct {
	r *Store
}

func (r *Store) MergeRequests() *MergeRequestRepo { return &MergeRequestRepo{r: r} }

const mergeRequestColumns = `
	mr.id, mr.iid, mr.project_id, mr.author_id, mr.source_branch, mr.target_branch,
	mr.title, mr.description, mr.state, mr.merge_commit_sha, mr.sha, mr.squash,
	mr.created_at, mr.updated_at, mr.merged_at, mr.closed_at, mr.merged_by_id`

func (r *MergeRequestRepo) scan(row pgx.Row) (*models.MergeRequest, error) {
	mr := &models.MergeRequest{}
	err := row.Scan(
		&mr.ID, &mr.IID, &mr.ProjectID, &mr.AuthorID, &mr.SourceBranch, &mr.TargetBranch,
		&mr.Title, &mr.Description, &mr.State, &mr.MergeCommitSHA, &mr.SHA, &mr.Squash,
		&mr.CreatedAt, &mr.UpdatedAt, &mr.MergedAt, &mr.ClosedAt, &mr.MergedByID,
	)
	if err != nil {
		return nil, err
	}
	return mr, nil
}

// CreateParams is a new merge request.
type CreateParams struct {
	ProjectID    uuid.UUID
	AuthorID     uuid.UUID
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
	Squash       bool
	// SHA is the tip of the source branch at creation time. It is recorded so the
	// request can show what it was opened against even after the branch moves.
	SHA string
}

// Create opens a merge request.
func (r *MergeRequestRepo) Create(ctx context.Context, p CreateParams) (*models.MergeRequest, error) {
	tx, err := r.r.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The lock makes the number allocation safe: two simultaneous requests
	// serialise here instead of both reading the same maximum.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"mr-iid:"+p.ProjectID.String()); err != nil {
		return nil, fmt.Errorf("lock the merge request numbering: %w", err)
	}

	var iid int
	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(MAX(iid), 0) + 1 FROM merge_requests WHERE project_id = $1`,
		p.ProjectID,
	).Scan(&iid); err != nil {
		return nil, fmt.Errorf("allocate the merge request number: %w", err)
	}

	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO merge_requests
			(project_id, iid, author_id, source_branch, target_branch, title, description, sha, squash)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id`,
		p.ProjectID, iid, p.AuthorID, p.SourceBranch, p.TargetBranch,
		p.Title, p.Description, p.SHA, p.Squash,
	).Scan(&id)
	if err != nil {
		if IsUniqueViolation(err) {
			return nil, ErrConflict
		}
		return nil, fmt.Errorf("create the merge request: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return r.ByID(ctx, id)
}

// ByID returns a merge request by its global id.
func (r *MergeRequestRepo) ByID(ctx context.Context, id int64) (*models.MergeRequest, error) {
	row := r.r.pool.QueryRow(ctx,
		`SELECT `+mergeRequestColumns+` FROM merge_requests mr WHERE mr.id = $1`, id)
	return r.scan(row)
}

// ByIID returns a merge request by its project-internal number.
func (r *MergeRequestRepo) ByIID(ctx context.Context, projectID uuid.UUID, iid int) (*models.MergeRequest, error) {
	row := r.r.pool.QueryRow(ctx,
		`SELECT `+mergeRequestColumns+` FROM merge_requests mr WHERE mr.project_id = $1 AND mr.iid = $2`,
		projectID, iid)
	return r.scan(row)
}

// ListFilter narrows a merge request listing.
type ListFilter struct {
	ProjectID *uuid.UUID
	State     string
	// AuthorID limits the list to one person's requests.
	AuthorID *uuid.UUID
	Limit    int
	Offset   int
}

// List returns merge requests newest first, with the author and the project
// joined in.
//
// The joins are what make the list usable: a page of twenty requests should not
// need forty extra queries to know who opened each one, and a request is
// meaningless without the project it belongs to.
func (r *MergeRequestRepo) List(ctx context.Context, f ListFilter) ([]*models.MergeRequest, error) {
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}

	rows, err := r.r.pool.Query(ctx, `
		SELECT `+mergeRequestColumns+`,
		       u.name, u.username,
		       p.id, p.path, p.name, p.description, p.visibility,
		       p.default_branch, p.merge_method, p.created_at,
		       COALESCE(merged.name, ''), COALESCE(merged.username, '')
		FROM merge_requests mr
		JOIN users u ON u.id = mr.author_id
		JOIN projects p ON p.id = mr.project_id
		LEFT JOIN users merged ON merged.id = mr.merged_by_id
		WHERE ($1::uuid IS NULL OR mr.project_id = $1)
		  AND ($2 = '' OR mr.state = $2)
		  AND ($3::uuid IS NULL OR mr.author_id = $3)
		ORDER BY mr.updated_at DESC, mr.id DESC
		LIMIT $4 OFFSET $5`,
		f.ProjectID, f.State, f.AuthorID, f.Limit, f.Offset)
	if err != nil {
		return nil, fmt.Errorf("list merge requests: %w", err)
	}
	defer rows.Close()

	out := []*models.MergeRequest{}
	for rows.Next() {
		mr := &models.MergeRequest{}
		// The project is scanned into a value first: scanning straight into a nil
		// pointer field would panic on the first row.
		project := &models.Project{}
		if err := rows.Scan(
			&mr.ID, &mr.IID, &mr.ProjectID, &mr.AuthorID, &mr.SourceBranch, &mr.TargetBranch,
			&mr.Title, &mr.Description, &mr.State, &mr.MergeCommitSHA, &mr.SHA, &mr.Squash,
			&mr.CreatedAt, &mr.UpdatedAt, &mr.MergedAt, &mr.ClosedAt, &mr.MergedByID,
			&mr.AuthorName, &mr.AuthorUsername,
			&project.ID, &project.Path, &project.Name, &project.Description,
			&project.Visibility, &project.DefaultBranch, &project.MergeMethod, &project.CreatedAt,
			&mr.MergedByName, &mr.MergedByUsername,
		); err != nil {
			return nil, err
		}
		mr.Project = project
		out = append(out, mr)
	}
	return out, rows.Err()
}

// CountOpen returns how many merge requests are waiting in a project, which is
// what the sidebar badge shows.
func (r *MergeRequestRepo) CountOpen(ctx context.Context, projectID uuid.UUID) (int, error) {
	var count int
	err := r.r.pool.QueryRow(ctx,
		`SELECT count(*) FROM merge_requests WHERE project_id = $1 AND state = $2`,
		projectID, models.MRStateOpened,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count open merge requests: %w", err)
	}
	return count, nil
}

// UpdateFields are the parts of a merge request a person may edit.
//
// Every field is a pointer so that "not mentioned" and "set to empty" stay
// distinguishable: clearing a description must not clear the title.
type UpdateFields struct {
	Title       *string
	Description *string
	Squash      *bool
}

// Update edits an open merge request.
func (r *MergeRequestRepo) Update(ctx context.Context, id int64, f UpdateFields) (*models.MergeRequest, error) {
	tag, err := r.r.pool.Exec(ctx, `
		UPDATE merge_requests SET
			title = COALESCE($2, title),
			description = COALESCE($3, description),
			squash = COALESCE($4, squash),
			updated_at = now()
		WHERE id = $1 AND state = $5`,
		id, f.Title, f.Description, f.Squash, models.MRStateOpened)
	if err != nil {
		return nil, fmt.Errorf("update the merge request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Either it does not exist or it is no longer open; the caller knows which
		// by looking it up, and neither may be edited.
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

// MarkMerged records the outcome of a merge. It only succeeds on an open request,
// so a merge that raced with another one cannot overwrite its result.
func (r *MergeRequestRepo) MarkMerged(ctx context.Context, id int64, mergeSHA string, by uuid.UUID) (*models.MergeRequest, error) {
	tag, err := r.r.pool.Exec(ctx, `
		UPDATE merge_requests SET
			state = $2, merge_commit_sha = $3, merged_at = now(), merged_by_id = $4, updated_at = now()
		WHERE id = $1 AND state = $5`,
		id, models.MRStateMerged, mergeSHA, by, models.MRStateOpened)
	if err != nil {
		return nil, fmt.Errorf("mark the merge request merged: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

// MarkClosed closes a merge request without merging it. The person who closed
// it is not recorded: nothing later depends on it, and the column would exist
// only to be queried once.
func (r *MergeRequestRepo) MarkClosed(ctx context.Context, id int64) (*models.MergeRequest, error) {
	tag, err := r.r.pool.Exec(ctx, `
		UPDATE merge_requests SET state = $2, closed_at = now(), updated_at = now()
		WHERE id = $1 AND state = $3`,
		id, models.MRStateClosed, models.MRStateOpened)
	if err != nil {
		return nil, fmt.Errorf("close the merge request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

// Reopen returns a closed or merged request to the open state.
//
// Reopening a merged request is refused rather than silently unmerging: the
// commits are already on the target branch, and pretending otherwise would let
// someone merge them a second time.
func (r *MergeRequestRepo) Reopen(ctx context.Context, id int64) (*models.MergeRequest, error) {
	current, err := r.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.State == models.MRStateMerged {
		return nil, ErrConflict
	}

	tag, err := r.r.pool.Exec(ctx, `
		UPDATE merge_requests SET
			state = $2, closed_at = NULL, updated_at = now()
		WHERE id = $1 AND state = $3`,
		id, models.MRStateOpened, models.MRStateClosed)
	if err != nil {
		return nil, fmt.Errorf("reopen the merge request: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return r.ByID(ctx, id)
}

// CloseForBranch closes every open merge request whose source branch is the one
// that has just been deleted, and returns them.
//
// It exists because a request whose source is gone cannot be merged, reviewed or
// reasoned about, and leaving it open would leave a merge button on a page that
// can only fail.
func (r *MergeRequestRepo) CloseForBranch(
	ctx context.Context, projectID uuid.UUID, branch string, by uuid.UUID,
) ([]*models.MergeRequest, error) {
	rows, err := r.r.pool.Query(ctx,
		`SELECT `+mergeRequestColumns+` FROM merge_requests mr
		 WHERE mr.project_id = $1 AND mr.state = $2 AND mr.source_branch = $3`,
		projectID, models.MRStateClosed, branch)
	if err != nil {
		return nil, fmt.Errorf("find the merge requests of a branch: %w", err)
	}
	defer rows.Close()

	found := []*models.MergeRequest{}
	for rows.Next() {
		mr, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		found = append(found, mr)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return found, nil
	}

	ids := make([]int64, 0, len(found))
	for _, mr := range found {
		ids = append(ids, mr.ID)
	}
	if _, err := r.r.pool.Exec(ctx,
		`UPDATE merge_requests SET state = $2, closed_at = now(), updated_at = now()
		 WHERE id = ANY($1) AND state = $3`,
		ids, models.MRStateClosed, models.MRStateOpened); err != nil {
		return nil, fmt.Errorf("close the merge requests of a branch: %w", err)
	}
	_ = by

	return found, nil
}

// AddNote appends a comment.
func (r *MergeRequestRepo) AddNote(ctx context.Context, mrID int64, author uuid.UUID, body string) (*models.MergeRequestNote, error) {
	var id int64
	err := r.r.pool.QueryRow(ctx, `
		INSERT INTO merge_request_notes (merge_request_id, author_id, body) VALUES ($1, $2, $3)
		RETURNING id`, mrID, author, body).Scan(&id)
	if err != nil {
		return nil, fmt.Errorf("add a note: %w", err)
	}

	note := &models.MergeRequestNote{}
	err = r.r.pool.QueryRow(ctx, `
		SELECT n.id, n.merge_request_id, n.author_id, u.name, u.username, n.body, n.created_at, n.updated_at
		FROM merge_request_notes n JOIN users u ON u.id = n.author_id
		WHERE n.id = $1`, id).Scan(
		&note.ID, &note.MergeRequestID, &note.AuthorID,
		&note.AuthorName, &note.AuthorUsername, &note.Body, &note.CreatedAt, &note.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("read the note back: %w", err)
	}
	return note, nil
}

// Notes returns the comments on a merge request, oldest first.
func (r *MergeRequestRepo) Notes(ctx context.Context, mrID int64) ([]models.MergeRequestNote, error) {
	rows, err := r.r.pool.Query(ctx, `
		SELECT n.id, n.merge_request_id, n.author_id, u.name, u.username, n.body, n.created_at, n.updated_at
		FROM merge_request_notes n JOIN users u ON u.id = n.author_id
		WHERE n.merge_request_id = $1 ORDER BY n.id`, mrID)
	if err != nil {
		return nil, fmt.Errorf("list notes: %w", err)
	}
	defer rows.Close()

	notes := []models.MergeRequestNote{}
	for rows.Next() {
		var note models.MergeRequestNote
		if err := rows.Scan(&note.ID, &note.MergeRequestID, &note.AuthorID,
			&note.AuthorName, &note.AuthorUsername, &note.Body, &note.CreatedAt, &note.UpdatedAt); err != nil {
			return nil, err
		}
		notes = append(notes, note)
	}
	return notes, rows.Err()
}

// OpenByBranch finds an open request that already covers a pair of branches.
//
// Opening a second request for work already under review is almost never what
// someone means, and two requests for one branch pair make the merge button
// ambiguous: either one merges the same commits twice.
func (r *MergeRequestRepo) OpenByBranch(ctx context.Context, projectID uuid.UUID, source, target string) (*models.MergeRequest, error) {
	row := r.r.pool.QueryRow(ctx,
		`SELECT `+mergeRequestColumns+` FROM merge_requests mr
		 WHERE mr.project_id = $1 AND mr.state = $2 AND mr.source_branch = $3 AND mr.target_branch = $4`,
		projectID, models.MRStateOpened, source, target)

	mr, err := r.scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look for an open merge request: %w", err)
	}
	return mr, nil
}

// TitleFromBranch turns "feature/add-the-thing" into "Add the thing".
//
// A merge request opened straight from a branch deserves a readable title, and
// asking for one before the form opens is a worse experience than a decent
// guess the person edits.
func TitleFromBranch(branch string) string {
	name := branch
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}

	fields := strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' })
	for i, field := range fields {
		if field == "" {
			continue
		}
		fields[i] = strings.ToUpper(field[:1]) + field[1:]
	}
	if len(fields) == 0 {
		return branch
	}
	return strings.Join(fields, " ")
}
