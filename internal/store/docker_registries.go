package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DockerRegistryRepo stores the Docker registries an administrator wrote down.
//
// A registry is an address, a credential and a few decisions about it. Nothing in the
// core reads this repository yet, and the type says nothing about images, manifests or
// tags: a record here is a place, and the fewer things it pretends to be, the less there
// is to be wrong about it when something does start reading it.
type DockerRegistryRepo struct{ s *Store }

func (s *Store) DockerRegistries() *DockerRegistryRepo { return &DockerRegistryRepo{s: s} }

// DockerRegistry is one registry, as an administrator described it.
type DockerRegistry struct {
	ID   uuid.UUID
	Name string
	// URL is the address as written, with the port and any path prefix. Compared without
	// regard to case, which is done in SQL because that is where the constraint lives.
	URL      string
	Login    string
	Password string
	// InsecureTLS accepts a certificate that does not verify. False is the ordinary
	// case and the only correct default; a registry behind a self-signed certificate
	// says so here rather than being quietly unreachable.
	InsecureTLS bool
	// ReadOnly marks a registry images may be pulled from and never pushed to.
	ReadOnly bool
	// Default marks the one registry pushes go to when nothing has said otherwise. At
	// most one record in the table may carry it.
	Default bool
	// Note is free text for the administrator. Nothing reads it.
	Note string
	// Enabled false keeps the record but takes it out of use.
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

const dockerRegistryColumns = `id, name, url, login, password, insecure_tls, read_only,
	is_default, note, enabled, created_at, updated_at`

// dockerRegistryRows is one round trip for a page of the list and for the size of the
// whole list: a page that says "3 of 47" counts 47 rows and reads 10 of them, and two
// queries are two answers that can disagree.
//
// The count is a subquery over the table rather than a window over the page, because a
// window is evaluated after LIMIT and so counts the page — which returns a total equal to
// the length of the page whenever the page is short, and a total that never grows past
// the page size. A list that says "3 of 3" on its last page and "10 of 10" on its first
// is a list whose paging cannot be trusted.
const dockerRegistryRows = `
	WITH page AS (
		SELECT ` + dockerRegistryColumns + ` FROM docker_registries
		ORDER BY is_default DESC, lower(name), lower(url)
		LIMIT $1 OFFSET $2
	)
	SELECT (SELECT count(*) FROM docker_registries), page.* FROM page`

// DockerRegistryListFilter is which ten of the list, and which ten of the whole list.
type DockerRegistryListFilter struct {
	Limit  int
	Offset int
}

// List returns one page of the list and how many records there are altogether.
func (r *DockerRegistryRepo) List(ctx context.Context, f DockerRegistryListFilter) ([]DockerRegistry, int, error) {
	// A limit of zero is a limit: it means the first page of the list is full of the
	// module's own registries and there is no room for any written-down address on it.
	// Only a limit nobody could have meant — negative, or larger than any page is ever —
	// is replaced with the default, because answering that with ten rows would be a
	// library that ignores its caller rather than a default.
	if f.Limit < 0 || f.Limit > 100 {
		f.Limit = 10
	}
	if f.Offset < 0 {
		f.Offset = 0
	}

	rows, err := r.s.pool.Query(ctx, dockerRegistryRows, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list docker registries: %w", err)
	}
	defer rows.Close()

	out := []DockerRegistry{}
	total := 0
	for rows.Next() {
		var reg DockerRegistry
		if err := rows.Scan(&total, &reg.ID, &reg.Name, &reg.URL, &reg.Login, &reg.Password,
			&reg.InsecureTLS, &reg.ReadOnly, &reg.Default, &reg.Note, &reg.Enabled,
			&reg.CreatedAt, &reg.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("read a docker registry: %w", err)
		}
		out = append(out, reg)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("list docker registries: %w", err)
	}

	// A page past the end has no rows, and the count arrives on the first of them — so a
	// reader who asked for page nine of a two-page list would be told there is nothing
	// here at all, rather than told what there is and how far past the end of it they
	// have gone. The count is asked for again in that one case, which is the case where
	// the first answer carried none.
	if len(out) == 0 {
		if err := r.s.pool.QueryRow(ctx, `SELECT count(*) FROM docker_registries`).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count docker registries: %w", err)
		}
	}
	return out, total, nil
}

// Create writes a new registry down and fills in what the database decided: its id and
// the two timestamps. A URL that is already written down is a conflict rather than a
// second row — two records for one address are one registry and two opinions about it,
// and the table refuses to hold the second.
func (r *DockerRegistryRepo) Create(ctx context.Context, reg *DockerRegistry) error {
	reg.URL = cleanDockerURL(reg.URL)
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO docker_registries
			(name, url, login, password, insecure_tls, read_only, is_default, note, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING id, created_at, updated_at`,
		reg.Name, reg.URL, reg.Login, reg.Password,
		reg.InsecureTLS, reg.ReadOnly, reg.Default, reg.Note, reg.Enabled,
	).Scan(&reg.ID, &reg.CreatedAt, &reg.UpdatedAt)
	if err != nil {
		if IsUniqueViolation(err) {
			return dockerRegistryConflict(err, reg.URL)
		}
		return fmt.Errorf("create docker registry: %w", err)
	}
	return nil
}

// dockerRegistryConflict says which of the two things the table refuses was refused.
//
// The table has two unique constraints and they are refused for opposite reasons, so one
// sentence cannot cover both: told "that address is already on the list" for a record whose
// address is not on the list, an administrator goes looking for a duplicate they did not
// write, and the registry they were trying to add stays unwritten. The constraint that
// said no is named by the database, so it can be read rather than guessed.
func dockerRegistryConflict(err error, url string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == "docker_registries_default_key" {
		return fmt.Errorf("%w: another registry is already the default, so this one cannot be",
			ErrConflict)
	}
	return fmt.Errorf("%w: the registry %q is already on the list", ErrConflict, url)
}

// Update writes an edited registry back.
//
// One transaction, because "make this the default" is two changes and not one: the
// record that was the default has to stop being it in the same breath that the edited
// record starts. Between the two statements there is a moment with two defaults, which
// the table's index does not permit — so the moment does not exist. And if the edited
// record stops being the default, nobody is left holding the flag: a default nobody can
// name is worse than none, since every reader would have to guess.
func (r *DockerRegistryRepo) Update(ctx context.Context, reg *DockerRegistry) error {
	reg.URL = cleanDockerURL(reg.URL)
	return r.s.Tx(ctx, func(tx pgx.Tx) error {
		if reg.Default {
			if _, err := tx.Exec(ctx,
				`UPDATE docker_registries SET is_default = false, updated_at = now()
				 WHERE is_default`); err != nil {
				return fmt.Errorf("clear the default docker registry: %w", err)
			}
		}

		err := tx.QueryRow(ctx, `
			UPDATE docker_registries SET
				name = $2, url = $3, login = $4, password = $5,
				insecure_tls = $6, read_only = $7, is_default = $8,
				note = $9, enabled = $10, updated_at = now()
			WHERE id = $1
			RETURNING updated_at`, reg.ID,
			reg.Name, reg.URL, reg.Login, reg.Password,
			reg.InsecureTLS, reg.ReadOnly, reg.Default, reg.Note, reg.Enabled,
		).Scan(&reg.UpdatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			if IsUniqueViolation(err) {
				return dockerRegistryConflict(err, reg.URL)
			}
			return fmt.Errorf("update docker registry: %w", err)
		}
		return nil
	})
}

// ByID is one registry by its id, or ErrNotFound.
func (r *DockerRegistryRepo) ByID(ctx context.Context, id uuid.UUID) (*DockerRegistry, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+dockerRegistryColumns+` FROM docker_registries WHERE id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("read docker registry: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read docker registry: %w", err)
		}
		return nil, ErrNotFound
	}
	reg, err := scanDockerRegistry(rows)
	if err != nil {
		return nil, err
	}
	return reg, rows.Err()
}

// ByURL is one registry by the address it was written down under, compared without regard
// to case, which is the same comparison the unique constraint makes.
func (r *DockerRegistryRepo) ByURL(ctx context.Context, url string) (*DockerRegistry, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+dockerRegistryColumns+` FROM docker_registries WHERE lower(btrim(url)) = lower(btrim($1))`, url)
	if err != nil {
		return nil, fmt.Errorf("read docker registry by url: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read docker registry by url: %w", err)
		}
		return nil, ErrNotFound
	}
	reg, err := scanDockerRegistry(rows)
	if err != nil {
		return nil, err
	}
	return reg, rows.Err()
}

// Delete takes a registry off the list.
//
// The record and nothing else: a registry is an address somebody wrote down, and
// forgetting it must not reach outside this table. What lives inside that registry is
// somebody else's problem and is not this repository's to answer.
func (r *DockerRegistryRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM docker_registries WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete docker registry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanDockerRegistry(row interface{ Scan(...any) error }) (*DockerRegistry, error) {
	var reg DockerRegistry
	err := row.Scan(&reg.ID, &reg.Name, &reg.URL, &reg.Login, &reg.Password,
		&reg.InsecureTLS, &reg.ReadOnly, &reg.Default, &reg.Note, &reg.Enabled,
		&reg.CreatedAt, &reg.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("read a docker registry: %w", err)
	}
	return &reg, nil
}

// cleanDockerURL is the address as this repository stores it.
//
// Trimmed, and with any trailing slash taken off, because "registry.example.com/" and
// "registry.example.com" are one address written two ways and a list that holds both is a
// list with a duplicate on it that no constraint can see. The scheme is left off when
// there is none, and kept when there is: a record may say https:// and mean it.
func cleanDockerURL(url string) string {
	url = strings.TrimSpace(url)
	for strings.HasSuffix(url, "/") {
		url = strings.TrimSuffix(url, "/")
	}
	return url
}
