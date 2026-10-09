package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// What is here, as one list.
//
// A project and a group are the same kind of thing to somebody looking for where to
// work: a place with a name, a path and an address. Listing them separately means two
// searches, two lists, and a page somebody has to find twice — so they are listed
// together here and each row says which it is.
//
// The visibility rules are the ones the two separate lists already had, applied inside
// one query: a private project to its members, a group to the people in it. The search
// narrows that list and never widens it, because a search that finds something you may
// not see is worse than a search that finds nothing.

// WhatKind is what a row of the list is.
const (
	KindProject = "project"
	KindGroup   = "group"
)

// Listing is one row: a place to work.
type Listing struct {
	ID          uuid.UUID
	Kind        string
	Path        string
	Name        string
	Description string
	Visibility  string
	// GroupID is nil for a group and for a project that is not in one.
	GroupID *uuid.UUID
	// ProjectCount is how many projects a group holds, and nil for a project: a
	// project holding projects has no such number.
	ProjectCount *int
	LastCommitAt *time.Time
	LastCommitMessage string
	LatestPipelineStatus string
	LatestPipelineAt *time.Time
	OpenMergeRequests int
	OpenIssues int
	// AccessLevel is what the reader may do here, in the words the rest of the
	// instance uses for it.
	AccessLevel int
	CreatedAt   time.Time
}

// ListingQuery is what to look for and how much to return.
type ListingQuery struct {
	// Search matches the path, the name and the description, case-insensitively.
	Search string
	// Kind is "project", "group", or empty for both.
	Kind string
	// Visibility narrows to one visibility. Empty means any.
	Visibility string
	Scope string
	Sort string
	Direction string
	// Page is one-based. Zero means the first page.
	Page int
	// PerPage is how many rows to return. Zero means a full page.
	PerPage int
}

// DefaultPageSize is how many rows a page holds when nobody says.
//
// Twenty fills a laptop screen without scrolling, and a person who wants more has a
// page control rather than a scroll bar through four thousand rows.
const DefaultPageSize = 20

// MaxPageSize is the most rows one request may ask for.
//
// A cap rather than a shrug: "per_page=100000" is what a script says when it wants
// everything, and handing it everything is how one page takes the instance down.
const MaxPageSize = 100

func (q ListingQuery) normalise() ListingQuery {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 {
		q.PerPage = DefaultPageSize
	}
	if q.PerPage > MaxPageSize {
		q.PerPage = MaxPageSize
	}
	if q.Kind != KindProject && q.Kind != KindGroup {
		q.Kind = ""
	}
	q.Search = strings.TrimSpace(q.Search)
	switch q.Scope { case "contributed", "personal", "member", "inactive": default: q.Scope = "" }
	switch q.Sort { case "name", "created", "last_activity": default: q.Sort = "name" }
	if q.Direction != "desc" { q.Direction = "asc" }
	return q
}

// searchPattern is a search term as a pattern, or "" when there is no search.
//
// The wildcards in it are escaped: somebody looking for "50%" should find a project
// called "50% done", not every project there is.
func (q ListingQuery) searchPattern() string {
	if q.Search == "" {
		return ""
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q.Search))
	return "%" + escaped + "%"
}

// ListVisible returns one page of what is here, and how many rows there are in total.
//
// The count is asked for on every request rather than estimated: a control that stops
// at twenty with "about 200" next to it is worse than no control at all.
func (s *Store) ListVisible(ctx context.Context, userID uuid.UUID, q ListingQuery) ([]Listing, int, error) {
	q = q.normalise()
	pattern := q.searchPattern()

	// One query, and the count in the same round trip.
	//
	// The access level is not selected here: it is computed from the role tables in Go,
	// the way it is everywhere else, because a level computed by a different set of SQL
	// than the rest of the instance uses is a level that will eventually disagree with
	// what a person is actually allowed to do. It is computed for one page of rows,
	// which is at most a hundred.
	const statement = `
		WITH visible AS (
			SELECT 'project' AS kind, p.id, p.path, p.name,
			       COALESCE(p.description, '') AS description, p.visibility,
			       p.group_id, NULL::int AS project_count, p.created_at,
			       (SELECT c.timestamp FROM commits c WHERE c.project_id = p.id ORDER BY c.timestamp DESC LIMIT 1) AS last_commit_at,
			       COALESCE((SELECT left(c.message, 120) FROM commits c WHERE c.project_id = p.id ORDER BY c.timestamp DESC LIMIT 1), '') AS last_commit_message,
			       COALESCE((SELECT pl.status FROM pipelines pl WHERE pl.project_id = p.id ORDER BY pl.id DESC LIMIT 1), '') AS latest_pipeline_status,
			       (SELECT pl.created_at FROM pipelines pl WHERE pl.project_id = p.id ORDER BY pl.id DESC LIMIT 1) AS latest_pipeline_at,
			       (SELECT count(*)::int FROM merge_requests mr WHERE mr.project_id = p.id AND mr.state = 'opened') AS open_merge_requests,
			       (SELECT count(*)::int FROM issues i WHERE i.project_id = p.id AND i.state = 'opened') AS open_issues
			FROM projects p
			WHERE (($5 = 'inactive' AND p.archived_at IS NOT NULL) OR ($5 <> 'inactive' AND p.archived_at IS NULL))
			  AND (p.visibility <> 'private'
			       OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
			       OR EXISTS (SELECT 1 FROM project_roles pr
		                  WHERE pr.project_id = p.id AND pr.source_user_id = $1)
			       OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1))
			  AND ($2 = '' OR p.visibility = $2)
			  AND ($3 = '' OR $3 = 'project')
			  AND ($4 = '' OR p.path ILIKE $4 ESCAPE '\'
			                  OR p.name ILIKE $4 ESCAPE '\'
			                  OR COALESCE(p.description, '') ILIKE $4 ESCAPE '\')
			  AND ($5 NOT IN ('contributed', 'personal', 'member') OR
			       ($5 = 'contributed' AND EXISTS (SELECT 1 FROM commits c JOIN users u ON lower(u.email) = lower(c.author_email) WHERE c.project_id = p.id AND u.id = $1)) OR
			       ($5 = 'personal' AND p.group_id IS NULL AND EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1 AND pr.max_access_level >= 50)) OR
			       ($5 = 'member' AND (EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $1) OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1) OR EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1))))

			UNION ALL

			SELECT 'group', g.id, g.full_path, g.name, COALESCE(g.description, ''),
			       'private', NULL::uuid, (
			           SELECT count(*)::int FROM projects gp
			           WHERE gp.group_id = g.id AND gp.archived_at IS NULL
			       ), g.created_at, NULL::timestamptz, '', '', NULL::timestamptz, 0::int, 0::int
			FROM groups g
			WHERE (EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = $1)
			    OR EXISTS (SELECT 1 FROM group_roles r WHERE r.group_id = g.id AND r.source_user_id = $1)
			    OR EXISTS (SELECT 1 FROM group_roles r WHERE r.group_id = g.id
			               AND r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)))
			  AND ($2 = '' OR $2 = 'group')
			  AND ($4 = '' OR g.full_path ILIKE $4 ESCAPE '\'
			                  OR g.name ILIKE $4 ESCAPE '\'
			                  OR COALESCE(g.description, '') ILIKE $4 ESCAPE '\')
		), totals AS (
			-- Counted before the page is taken off. Counting after would count the page
			-- again and report twenty for a list of four thousand, which is the one
			-- number a page control most needs to be right.
			SELECT count(*) AS total FROM visible
		), page AS (
			SELECT * FROM visible
			ORDER BY CASE WHEN $8 = 'name' AND $9 = 'asc' THEN lower(name) END ASC NULLS LAST,
			         CASE WHEN $8 = 'name' AND $9 = 'desc' THEN lower(name) END DESC NULLS LAST,
			         CASE WHEN $8 = 'created' AND $9 = 'asc' THEN created_at END ASC NULLS LAST,
			         CASE WHEN $8 = 'created' AND $9 = 'desc' THEN created_at END DESC NULLS LAST,
			         CASE WHEN $8 = 'last_activity' AND $9 = 'asc' THEN last_commit_at END ASC NULLS LAST,
			         CASE WHEN $8 = 'last_activity' AND $9 = 'desc' THEN last_commit_at END DESC NULLS LAST,
			         CASE WHEN $9 = 'desc' THEN lower(path) END DESC,
			         CASE WHEN $9 = 'asc' THEN lower(path) END ASC
			LIMIT $6 OFFSET $7
		)
		SELECT page.*, totals.total FROM page CROSS JOIN totals`

	rows, err := s.pool.Query(ctx, statement, userID, q.Visibility, q.Kind, pattern,
		q.Scope, q.PerPage, (q.Page-1)*q.PerPage, q.Sort, q.Direction)
	if err != nil {
		return nil, 0, fmt.Errorf("list what is here: %w", err)
	}
	defer rows.Close()

	out := []Listing{}
	total := 0
	for rows.Next() {
		var row Listing
		if err := rows.Scan(&row.Kind, &row.ID, &row.Path, &row.Name, &row.Description,
			&row.Visibility, &row.GroupID, &row.ProjectCount, &row.CreatedAt,
			&row.LastCommitAt, &row.LastCommitMessage, &row.LatestPipelineStatus, &row.LatestPipelineAt,
			&row.OpenMergeRequests, &row.OpenIssues, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// No rows means an empty page, which may be the last one: the count is unknown then
	// and is asked for separately, so that "page 9 of 3" is said rather than guessed.
	if total == 0 && q.Page > 1 {
		counted, err := s.countVisible(ctx, userID, q)
		if err != nil {
			return nil, 0, err
		}
		total = counted
	}

	if err := s.fillAccessLevels(ctx, userID, out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// fillAccessLevels says what the reader may do in each place, in Go and from the same
// role tables the rest of the instance decides from.
func (s *Store) fillAccessLevels(ctx context.Context, userID uuid.UUID, rows []Listing) error {
	for index := range rows {
		var (
			level int
			err   error
		)
		switch rows[index].Kind {
		case KindGroup:
			level, err = s.Permissions().GroupAccessLevel(ctx, userID, rows[index].ID)
		default:
			level, err = s.Permissions().AccessLevel(ctx, userID, rows[index].ID)
		}
		if err != nil {
			return err
		}
		rows[index].AccessLevel = level
	}
	return nil
}

// countVisible is the same list with no page on it.
func (s *Store) countVisible(ctx context.Context, userID uuid.UUID, q ListingQuery) (int, error) {
	q = q.normalise()

	const count = `
		SELECT count(*) FROM (
			SELECT p.id
			FROM projects p
			WHERE (($5 = 'inactive' AND p.archived_at IS NOT NULL) OR ($5 <> 'inactive' AND p.archived_at IS NULL))
			  AND (p.visibility <> 'private'
			       OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id = p.id AND m.user_id = $1)
			       OR EXISTS (SELECT 1 FROM project_roles pr
		                  WHERE pr.project_id = p.id AND pr.source_user_id = $1)
			       OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1))
			  AND ($2 = '' OR p.visibility = $2)
			  AND ($3 = '' OR $3 = 'project')
			  AND ($4 = '' OR p.path ILIKE $4 ESCAPE '\' OR p.name ILIKE $4 ESCAPE '\'
			                  OR COALESCE(p.description, '') ILIKE $4 ESCAPE '\')
			  AND ($5 NOT IN ('contributed', 'personal', 'member') OR
			       ($5 = 'contributed' AND EXISTS (SELECT 1 FROM commits c JOIN users u ON lower(u.email) = lower(c.author_email) WHERE c.project_id = p.id AND u.id = $1)) OR
			       ($5 = 'personal' AND p.group_id IS NULL AND EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1 AND pr.max_access_level >= 50)) OR
			       ($5 = 'member' AND (EXISTS (SELECT 1 FROM project_members pm WHERE pm.project_id = p.id AND pm.user_id = $1) OR EXISTS (SELECT 1 FROM group_members gm WHERE gm.group_id = p.group_id AND gm.user_id = $1) OR EXISTS (SELECT 1 FROM project_roles pr WHERE pr.project_id = p.id AND pr.source_user_id = $1))))

			UNION ALL

			SELECT g.id
			FROM groups g
			WHERE (EXISTS (SELECT 1 FROM group_members m WHERE m.group_id = g.id AND m.user_id = $1)
			    OR EXISTS (SELECT 1 FROM group_roles r WHERE r.group_id = g.id AND r.source_user_id = $1)
			    OR EXISTS (SELECT 1 FROM group_roles r WHERE r.group_id = g.id
			               AND r.source_group_id IN (SELECT group_id FROM group_members WHERE user_id = $1)))
			  AND ($2 = '' OR $2 = 'group')
			  AND ($4 = '' OR g.full_path ILIKE $4 ESCAPE '\' OR g.name ILIKE $4 ESCAPE '\'
			                  OR COALESCE(g.description, '') ILIKE $4 ESCAPE '\')
		) visible`

	var total int
	if err := s.pool.QueryRow(ctx, count, userID, q.Visibility, q.Kind, q.searchPattern(), q.Scope).Scan(&total); err != nil {
		return 0, fmt.Errorf("count what is here: %w", err)
	}
	return total, nil
}
