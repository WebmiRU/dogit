package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/secrets"
)

// ResourceRepo is what this instance has to give away, and what it is holding.
type ResourceRepo struct{ s *Store }

func (s *Store) Resources() *ResourceRepo { return &ResourceRepo{s: s} }

const resourceColumns = `r.id, r.kind, r.software, r.version, r.name, r.origin,
	r.address, r.integration_id, r.released_at, r.last_integration_id,
	r.created_at, r.updated_at`

// A resource as the list shows it, with the holder's kind and name beside it.
//
// Read in one query with a left join rather than one request per row: a page of twenty
// resources that costs twenty round trips to render is a page that feels like it is broken,
// and the name is the only thing the join adds.
func (r *ResourceRepo) List(ctx context.Context) ([]models.Resource, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT `+resourceColumns+`, m.kind, m.name
		FROM resources r
		LEFT JOIN integrations m ON m.id = r.integration_id
		ORDER BY r.integration_id IS NULL DESC, r.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list resources: %w", err)
	}
	defer rows.Close()

	out := []models.Resource{}
	for rows.Next() {
		var (
			one        models.Resource
			address    []byte
			moduleKind *string
			moduleName *string
		)
		if err := rows.Scan(&one.ID, &one.Kind, &one.Software, &one.Version, &one.Name,
			&one.Origin, &address, &one.IntegrationID, &one.ReleasedAt,
			&one.LastIntegrationID, &one.CreatedAt, &one.UpdatedAt,
			&moduleKind, &moduleName); err != nil {
			return nil, fmt.Errorf("read a resource: %w", err)
		}
		opened, err := r.openAddress(address)
		if err != nil {
			// Named rather than swallowed: a resource this instance cannot open is one it
			// cannot manage either, and a list that quietly drops it is a list that lies
			// about what exists.
			return nil, fmt.Errorf("read resource %s (%s): %w", one.Name, one.Coordinate(), err)
		}
		one.Address = string(opened)
		if moduleKind != nil {
			one.ModuleKind = *moduleKind
		}
		if moduleName != nil {
			one.ModuleName = *moduleName
		}
		out = append(out, one)
	}
	return out, rows.Err()
}

// ByID reads one resource, address and all.
//
// The address is filled in, unlike in List: a page that shows one resource needs to be able
// to show what it is reached by, and that page is behind an administrator's session.
func (r *ResourceRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	var (
		one     models.Resource
		address []byte
	)
	err := r.s.pool.QueryRow(ctx, `
		SELECT `+resourceColumns+`
		FROM resources r WHERE r.id = $1`, id).Scan(&one.ID, &one.Kind, &one.Software,
		&one.Version, &one.Name, &one.Origin, &address, &one.IntegrationID, &one.ReleasedAt,
		&one.LastIntegrationID, &one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	opened, err := r.openAddress(address)
	if err != nil {
		return nil, fmt.Errorf("read resource %s (%s): %w", one.Name, one.Coordinate(), err)
	}
	one.Address = string(opened)
	return &one, nil
}

// Grant records a resource as held by a module.
//
// The insert is the whole of the mutual exclusion: resources_one_per_module makes a second
// resource for the same module impossible, so the caller finds out from the constraint rather
// than from having read the table first and been wrong. A module with a resource and asking
// for another is told so; it is not given one and a spare.
func (r *ResourceRepo) Grant(ctx context.Context, id, integrationID uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE resources SET integration_id = $2, released_at = NULL, updated_at = now()
		WHERE id = $1 AND (integration_id IS NULL OR integration_id = $2)`, id, integrationID)
	if err != nil {
		return fmt.Errorf("grant resource %s: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("this resource is not free to give, or does not exist")
	}
	return nil
}

// Release gives a resource up without destroying it.
//
// This is what removing a module does. The resource stays, sealed, with nothing holding it,
// and the module it belonged to is written down beside it: somebody looking at an orphan
// months later is going to ask whose it was, and the answer is not in the module any more.
func (r *ResourceRepo) Release(ctx context.Context, integrationID uuid.UUID) (*models.Resource, error) {
	var one models.Resource
	err := r.s.pool.QueryRow(ctx, `
		UPDATE resources SET integration_id = NULL, released_at = now(),
			last_integration_id = $1, updated_at = now()
		WHERE integration_id = $1
		RETURNING id, kind, software, version, name, origin, released_at, last_integration_id,
			created_at, updated_at`, integrationID).Scan(&one.ID, &one.Kind, &one.Software,
		&one.Version, &one.Name, &one.Origin, &one.ReleasedAt, &one.LastIntegrationID,
		&one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	return &one, nil
}

// ReleaseByID gives one named resource up, for an administrator withdrawing it from a module
// rather than removing the module.
func (r *ResourceRepo) ReleaseByID(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	var one models.Resource
	err := r.s.pool.QueryRow(ctx, `
		UPDATE resources SET integration_id = NULL, released_at = now(),
			last_integration_id = integration_id, updated_at = now()
		WHERE id = $1 AND integration_id IS NOT NULL
		RETURNING id, kind, software, version, name, origin, released_at, last_integration_id,
			created_at, updated_at`, id).Scan(&one.ID, &one.Kind, &one.Software,
		&one.Version, &one.Name, &one.Origin, &one.ReleasedAt, &one.LastIntegrationID,
		&one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	return &one, nil
}

// HeldBy reads the resource a module holds, if any.
//
// Asked at registration rather than assumed: a module that has one and is told to take another
// needs to be refused, and the only way to know is to look.
func (r *ResourceRepo) HeldBy(ctx context.Context, integrationID uuid.UUID) (*models.Resource, error) {
	var (
		one     models.Resource
		address []byte
	)
	err := r.s.pool.QueryRow(ctx, `
		SELECT `+resourceColumns+`
		FROM resources r WHERE r.integration_id = $1`, integrationID).Scan(&one.ID, &one.Kind,
		&one.Software, &one.Version, &one.Name, &one.Origin, &address, &one.IntegrationID,
		&one.ReleasedAt, &one.LastIntegrationID, &one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, ErrNotFound
	}
	opened, err := r.openAddress(address)
	if err != nil {
		return nil, fmt.Errorf("read the resource of %s: %w", integrationID, err)
	}
	one.Address = string(opened)
	return &one, nil
}

// Free lists resources nobody holds: what the removal of a module left behind, and what an
// administrator may look at and delete.
func (r *ResourceRepo) Free(ctx context.Context) ([]models.Resource, error) {
	all, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := []models.Resource{}
	for _, one := range all {
		if !one.Held() {
			out = append(out, one)
		}
	}
	return out, nil
}

// Taken tells whether an address is already ours.
//
// Read and opened, every one, and compared in the clear — and it has to be done that way.
// Each address is sealed under a nonce of its own, so the same address sealed twice is two
// different byte strings and no amount of SQL will find them equal. That is the property that
// stops a reader of the table from telling that two modules were given the same password, and
// it is paid for here: the table cannot be searched by address, so an administrator adding one
// reads them all. The list is small and this happens once per resource.
//
// Said before the write rather than after: an address with a password in it is the only copy
// there will be, and a row deleted over a duplicate has taken it with it.
func (r *ResourceRepo) Taken(ctx context.Context, address string) (bool, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT address FROM resources`)
	if err != nil {
		return false, fmt.Errorf("look for a resource by its address: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var stored []byte
		if err := rows.Scan(&stored); err != nil {
			return false, fmt.Errorf("read a resource address: %w", err)
		}
		if len(stored) == 0 {
			continue
		}
		opened, err := r.openAddress(stored)
		if err != nil {
			// An address this instance cannot open is one it cannot claim to be free, and
			// saying "yes, free" about a resource whose contents are unknown is exactly the
			// answer that leads to two records for one thing.
			return false, fmt.Errorf("one of this instance's resources cannot be opened, so "+
				"it cannot be said whether this address is already here: %w", err)
		}
		if opened == address {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Put writes a new resource, sealed, and free for somebody to be given.
//
// Sealed on the way in rather than by the caller: an address that reaches the database
// unsealed is an address that is in the backup, and there is no reason for one caller to
// remember and every other to not.
func (r *ResourceRepo) Put(ctx context.Context, one models.Resource) (*models.Resource, error) {
	if one.ID == uuid.Nil {
		one.ID = uuid.New()
	}
	if one.Origin == "" {
		one.Origin = models.OriginManaged
	}
	sealed, err := r.s.sealAddress([]byte(one.Address))
	if err != nil {
		return nil, err
	}
	// Returned rather than echoed, so that the row the caller gets is the row that is there:
	// the timestamps are the database's, and a caller that made them up would be told a
	// resource was written before it was.
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO resources (id, kind, software, version, name, origin, address,
			integration_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at`, one.ID, one.Kind, one.Software, one.Version,
		one.Name, one.Origin, sealed, one.IntegrationID).Scan(&one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("record the resource: %w", err)
	}
	one.Address = ""
	return &one, nil
}

// Forget deletes a resource from the table.
//
// It deletes the record, not the thing. A managed resource's database is dropped by whoever
// created it, and a manual one cannot be dropped at all — this is the bookkeeping going away
// so that the page stops offering it, which is a different act from destroying it and is
// named differently for that reason.
func (r *ResourceRepo) Forget(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	one, err := r.ByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if one.Held() {
		return nil, fmt.Errorf("this resource is still in use by %s; give it up first",
			holderName(*one))
	}
	if _, err := r.s.pool.Exec(ctx, `DELETE FROM resources WHERE id = $1`, id); err != nil {
		return nil, fmt.Errorf("forget the resource: %w", err)
	}
	one.Address = ""
	return one, nil
}

// setIntegrationDatabase points a module at a resource, keeping the two columns the core
// already had so that nothing that reads them starts reading nothing.
func (r *ResourceRepo) setIntegrationDatabase(ctx context.Context, integrationID uuid.UUID,
	name, role string) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE integrations SET database_name = $2, database_role = $3, updated_at = now()
		WHERE id = $1`, integrationID, name, role)
	if err != nil {
		return fmt.Errorf("record the module's database: %w", err)
	}
	return nil
}

func holderName(one models.Resource) string {
	if one.ModuleName != "" {
		return one.ModuleName
	}
	if one.ModuleKind != "" {
		return one.ModuleKind
	}
	return "a module"
}

// sealAddress encrypts an address, refusing when there is no key.
func (s *Store) sealAddress(plain []byte) ([]byte, error) {
	if s.sealer == nil {
		return nil, fmt.Errorf("%s; set DOGIT_SECRET_KEY before recording a resource, "+
			"whose address carries its password", secrets.ErrNoKey)
	}
	return s.sealer.Seal(plain)
}

// openAddress decrypts an address stored in a resource row.
func (r *ResourceRepo) openAddress(stored []byte) (string, error) {
	if len(stored) == 0 {
		return "", nil
	}
	plain, err := r.s.Integrations().open(json.RawMessage(stored))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
