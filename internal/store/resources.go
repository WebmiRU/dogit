package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/resource"
	"github.com/ewolf/dogit/internal/secrets"
)

// ResourceRepo is what this instance has to give away, and what it is holding.
type ResourceRepo struct{ s *Store }

func (s *Store) Resources() *ResourceRepo { return &ResourceRepo{s: s} }

// The columns a resource is made of, in a fixed order.
//
// Facts first and then the sealed half, because that is the order they are read in: a page
// showing a shelf of resources never needs the secret, and every read that only wants to know
// where a thing is stops before it.
const resourceColumns = `r.id, r.kind, r.software, r.version, r.name, r.origin,
	r.host, r.port, r.database_name, r.username, r.endpoint, r.region, r.bucket,
	r.access_key, r.secret, r.integration_id, r.released_at, r.last_integration_id,
	r.last_integration_kind, r.created_at, r.updated_at`

// scanResource reads one row in the order resourceColumns names, with the sealed half left
// unopened unless the caller asked for it.
//
// The parameter is what makes this worth having: `withSecret` is false for the list and for the
// module's own settings, and true only where a resource is being handed to somebody who needs
// to connect to it. A function that returned the secret always would be one whose callers are
// all one forgotten argument away from printing a password.
func (r *ResourceRepo) scanResource(row pgx.Row, withSecret bool) (*models.Resource, error) {
	var (
		one    models.Resource
		port   *int
		sealed []byte
		plain  = partsScan{}
	)
	// Each part is scanned through a pointer to its pointer. The address of a nil *string is
	// how a nullable column is read; passing the *string itself is how you get a panic inside
	// the driver, because it writes through the pointer it was given and there is nothing
	// there yet. These columns are NOT NULL with a default, so a plain *string would do —
	// except that one day a column will not have a default, and this is the shape that is
	// right either way.
	if err := row.Scan(&one.ID, &one.Kind, &one.Software, &one.Version, &one.Name, &one.Origin,
		&plain.host, &port, &plain.databaseName, &plain.username, &plain.endpoint,
		&plain.region, &plain.bucket, &plain.accessKey, &sealed, &one.IntegrationID,
		&one.ReleasedAt, &one.LastIntegrationID, &one.LastIntegrationKind,
		&one.CreatedAt, &one.UpdatedAt); err != nil {
		return nil, err
	}
	if port != nil {
		plain.port = fmt.Sprintf("%d", *port)
	}

	// Whatever kind this is, the facts are collected under their own names so that a page can
	// show them without having to know which kind it is looking at. A kind this instance has
	// never heard of keeps its facts: the resource is still real, and refusing to show where
	// it is would be the page pretending the resource does not exist.
	one.Parts = plain.into()

	if withSecret {
		secret, err := r.openSecret(sealed)
		if err != nil {
			return nil, err
		}
		one.Secret = secret
	}
	return &one, nil
}

// partsScan gathers the fact columns, each into its own variable.
//
// Pointers into a map are not something pgx will fill for us, and the alternative — a struct
// with a field per column and a hand-written merge into Parts — is the place a new kind's field
// gets added to the query and forgotten in here, which is a column that reads back as empty and
// looks like nothing was ever written down.
type partsScan struct {
	host         *string
	port         string
	databaseName *string
	username     *string
	endpoint     *string
	region       *string
	bucket       *string
	accessKey    *string
}

func (p partsScan) into() resource.Parts {
	out := resource.Parts{}
	put := func(key string, value *string) {
		if value != nil && *value != "" {
			out[key] = *value
		}
	}
	put("host", p.host)
	if p.port != "" {
		out["port"] = p.port
	}
	put("database_name", p.databaseName)
	put("username", p.username)
	put("endpoint", p.endpoint)
	put("region", p.region)
	put("bucket", p.bucket)
	put("access_key", p.accessKey)
	return out
}

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
		var moduleKind, moduleName *string
		var (
			one    models.Resource
			port   *int
			sealed []byte
			plain  = partsScan{}
		)
		if err := rows.Scan(&one.ID, &one.Kind, &one.Software, &one.Version, &one.Name,
			&one.Origin, &plain.host, &port, &plain.databaseName, &plain.username,
			&plain.endpoint, &plain.region, &plain.bucket, &plain.accessKey, &sealed,
			&one.IntegrationID, &one.ReleasedAt, &one.LastIntegrationID,
			&one.LastIntegrationKind, &one.CreatedAt, &one.UpdatedAt,
			&moduleKind, &moduleName); err != nil {
			// Named rather than swallowed: a resource this instance cannot read is one it
			// cannot manage either, and a list that quietly drops it is a list that lies
			// about what exists.
			return nil, fmt.Errorf("read a resource: %w", err)
		}
		if port != nil {
			plain.port = fmt.Sprintf("%d", *port)
		}
		one.Parts = plain.into()
		// The sealed half is read and left shut. This is a page of twenty things and their
		// holders, and the secret of each is a password; a list that opened them could be
		// pasted into a ticket with them in it.
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

// ByID reads one resource.
//
// The secret is opened here and not in List: this is the one call that hands over something
// somebody has to connect with — an administrator filling in a lost address, or a module being
// given the one it is to keep — and it is behind an administrator's session, which is the only
// place a password should be read out loud.
func (r *ResourceRepo) ByID(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	one, err := r.scanResource(r.s.pool.QueryRow(ctx, `
		SELECT `+resourceColumns+`
		FROM resources r WHERE r.id = $1`, id), true)
	if err != nil {
		// "No such row" and "could not read the row" are told apart, because they are
		// different answers and collapsing them is how a broken query reads as an empty
		// shelf. This function used to return "not found" for anything at all, so a missing
		// column in the list above made every resource on the page claim it did not exist.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read resource %s: %w", id, err)
	}
	return one, nil
}

// Grant records a resource as held by a module.
//
// The update is the whole of the mutual exclusion: resources_one_per_module makes a second
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
func (r *ResourceRepo) Release(ctx context.Context, integrationID uuid.UUID,
	kind string) (*models.Resource, error) {
	// The columns in SET are unqualified and the ones in RETURNING are qualified, and that is
	// not an inconsistency. Postgres refuses to qualify a SET target ("SET target columns
	// cannot be qualified with the relation name") and equally refuses to leave a RETURNING
	// column unqualified when a FROM clause brings in a table with the same name. One list for
	// both is not available, which is why there are two above.
	one, err := r.scanResource(r.s.pool.QueryRow(ctx, `
		UPDATE resources SET integration_id = NULL, released_at = now(),
			last_integration_id = $1, last_integration_kind = $2, updated_at = now()
		FROM integrations m WHERE m.id = $1
		  AND resources.integration_id = $1
		RETURNING `+returnColumnsJoined, integrationID, kind), false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("give up the resource: %w", err)
	}
	// The kind is passed in rather than read from RETURNING, which cannot name the joined
	// table to take it from. Said here rather than in the query because the query cannot say
	// it.
	one.LastIntegrationID = &integrationID
	one.LastIntegrationKind = kind
	return one, nil
}

// ReleaseByID gives one named resource up, for an administrator withdrawing it from a module
// rather than removing the module.
func (r *ResourceRepo) ReleaseByID(ctx context.Context, id uuid.UUID) (*models.Resource, error) {
	// Named throughout, because the FROM clause below brings a second `id` into the statement
	// and an unqualified `id` is then ambiguous — which Postgres reports on the whole query
	// rather than on the column, so the message names a line where the reader is not looking.
	one, err := r.scanResource(r.s.pool.QueryRow(ctx, `
		UPDATE resources SET integration_id = NULL, released_at = now(),
			last_integration_id = integration_id,
			last_integration_kind = coalesce((
				SELECT m.kind FROM integrations m WHERE m.id = resources.integration_id), ''),
			updated_at = now()
		WHERE resources.id = $1 AND resources.integration_id IS NOT NULL
		RETURNING `+returnColumnsPlain, id), false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("give up the resource: %w", err)
	}
	return one, nil
}

// returnColumns is resourceColumns for a RETURNING clause.
//
// Two forms, because Postgres will not take one list for both. In an UPDATE ... FROM an
// unqualified column is ambiguous between the table being written and the one joined in, and it
// reports that against the whole statement — so the error points at a line rather than at the
// column. In an UPDATE with no FROM the qualification is not merely unnecessary but refused:
// `RETURNING resources.id` there is "column resources of relation resources does not exist".
//
// Written out in both forms rather than built from resourceColumns by a string rule, because a
// rule that knows when to qualify and when not to is a rule that has to be right, and a column
// added to one list and forgotten in the other is a row that reads back short.
const (
	returnColumnsPlain = `id, kind, software, version, name, origin,
		host, port, database_name, username, endpoint, region, bucket, access_key, secret,
		integration_id, released_at, last_integration_id, last_integration_kind,
		created_at, updated_at`

	returnColumnsJoined = `resources.id, resources.kind, resources.software, resources.version,
		resources.name, resources.origin, resources.host, resources.port,
		resources.database_name, resources.username, resources.endpoint, resources.region,
		resources.bucket, resources.access_key, resources.secret, resources.integration_id,
		resources.released_at, resources.last_integration_id,
		resources.last_integration_kind, resources.created_at, resources.updated_at`
)

// HeldBy reads the resource a module holds, if any, sealed half included.
//
// Asked at registration rather than assumed: a module that has one and is told to take another
// needs to be refused, and the only way to know is to look. This is the one read on the ordinary
// path that opens the secret, because registration is exactly when the module is given what it
// needs to connect.
func (r *ResourceRepo) HeldBy(ctx context.Context, integrationID uuid.UUID) (*models.Resource, error) {
	one, err := r.scanResource(r.s.pool.QueryRow(ctx, `
		SELECT `+resourceColumns+`
		FROM resources r WHERE r.integration_id = $1`, integrationID), true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read the resource of %s: %w", integrationID, err)
	}
	return one, nil
}

// ByIDFor reads the resource a module holds, with the secret left shut.
//
// Split from HeldBy because those two are wanted for different reasons and only one of them
// needs a password. Registration is given one; a settings page is not, and opening a password
// to render a hostname is how a password ends up in a screenshot.
func (r *ResourceRepo) ByIDFor(ctx context.Context, integrationID uuid.UUID) (*models.Resource, error) {
	one, err := r.scanResource(r.s.pool.QueryRow(ctx, `
		SELECT `+resourceColumns+`
		FROM resources r WHERE r.integration_id = $1`, integrationID), false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read the resource of %s: %w", integrationID, err)
	}
	return one, nil
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

// Taken tells whether a resource naming this place is already recorded.
//
// Compared in SQL, on the fact columns, and that is a change from how it used to be done. The
// address was sealed with a nonce of its own, so no two of them were ever byte-identical and the
// check had to open every row on the instance and compare in the clear. What it bought was that
// the password could not be matched by reading the table. The password is not in the key now:
// identity is the place, the user and the database, and a record whose password has gone stale
// is still a record of the same database — refusing it would leave somebody unable to describe a
// database they have rather than holding one they do not.
//
// Every part of the identity is compared, and not only those that were given: an empty part is
// an empty part. Describing the same database twice with the port given once and omitted once
// is the same mistake twice, and the default fills the second one in anyway.
func (r *ResourceRepo) Taken(ctx context.Context, kind resource.Kind, parts resource.Parts) (bool, error) {
	plain := kind.Plain(parts)

	var host, databaseName, username, endpoint, region, bucket, accessKey *string
	var port *int
	if err := r.s.pool.QueryRow(ctx, `
		SELECT host, port, database_name, username, endpoint, region, bucket, access_key
		FROM resources
		WHERE kind = $1 AND coalesce(host, '') = coalesce($2, '')
			AND coalesce(port, 0) = coalesce($3, 0)
			AND coalesce(database_name, '') = coalesce($4, '')
			AND coalesce(username, '') = coalesce($5, '')
			AND coalesce(endpoint, '') = coalesce($6, '')
			AND coalesce(region, '') = coalesce($7, '')
			AND coalesce(bucket, '') = coalesce($8, '')
			AND coalesce(access_key, '') = coalesce($9, '')
		LIMIT 1`, kind.Key, plain["host"], numberOrNil(plain["port"]),
		plain["database_name"], plain["username"], plain["endpoint"], plain["region"],
		plain["bucket"], plain["access_key"]).Scan(&host, &port, &databaseName, &username,
		&endpoint, &region, &bucket, &accessKey); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("look for a resource by where it is: %w", err)
	}
	return true, nil
}

// numberOrNil turns a typed port into the number the column holds, or nothing when it is empty.
func numberOrNil(value string) *int {
	if value == "" {
		return nil
	}
	var number int
	if _, err := fmt.Sscanf(value, "%d", &number); err != nil {
		return nil
	}
	return &number
}

// Put writes a new resource, sealed, and free for somebody to be given.
//
// The split between columns and envelope happens here rather than by the caller, because a
// password that reaches the database unsealed is a password in the backup, and there is no
// reason for one caller to remember and every other to not.
func (r *ResourceRepo) Put(ctx context.Context, one models.Resource) (*models.Resource, error) {
	if one.ID == uuid.Nil {
		one.ID = uuid.New()
	}
	if one.Origin == "" {
		one.Origin = models.OriginManaged
	}
	kind := one.Descriptor()
	plain, secret := kind.Split(one.Parts)
	sealed, err := r.s.sealSecret(secret)
	if err != nil {
		return nil, err
	}

	// Returned rather than echoed, so that the row the caller gets is the row that is there:
	// the timestamps are the database's, and a caller that made them up would be told a
	// resource was written before it was.
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO resources (id, kind, software, version, name, origin,
			host, port, database_name, username, endpoint, region, bucket, access_key,
			secret, integration_id, last_integration_kind)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING created_at, updated_at`,
		one.ID, one.Kind, one.Software, one.Version, one.Name, one.Origin,
		plain["host"], numberOrNil(plain["port"]), plain["database_name"], plain["username"],
		plain["endpoint"], plain["region"], plain["bucket"], plain["access_key"],
		sealed, one.IntegrationID, one.LastIntegrationKind).Scan(&one.CreatedAt, &one.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("record the resource: %w", err)
	}
	// The secret is not echoed back to the caller. It was given once, on the way in, and the
	// row the caller now holds is the one a page can show.
	one.Secret = nil
	return &one, nil
}

// IsHeldBy says whether a module currently holds a resource.
//
// Not ByIDFor: this is asked on every registration, and a registration is not a place to pay
// for reading a resource's facts. It is one lookup on a partial index, and the answer is a yes
// or a no.
//
// An error is answered "no", and that is the dangerous half of this function. A database that
// cannot be reached is not evidence that nothing is held, and treating it as such would let a
// module be given a second database while the first is still its own — the exact outcome the
// one-resource-per-module rule exists to prevent. The caller logs what it saw and the module
// keeps what it had, which is recoverable; the alternative is two databases and an orphaned
// history, which is not.
func (r *ResourceRepo) IsHeldBy(ctx context.Context, integrationID uuid.UUID) (bool, error) {
	var exists bool
	err := r.s.pool.QueryRow(ctx,
		`SELECT true FROM resources WHERE integration_id = $1 LIMIT 1`, integrationID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("ask whether %s holds a resource: %w", integrationID, err)
	}
	return exists, nil
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
	one.Secret = nil
	return one, nil
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

// sealSecret encrypts a resource's secrets, refusing when there is no key.
func (s *Store) sealSecret(secret resource.Parts) ([]byte, error) {
	// An empty slice rather than nil. The column is NOT NULL, and a resource with no secret
	// is a resource this instance writes perfectly well — so the absence of a secret is an
	// empty byte string, not a missing value.
	if len(secret) == 0 {
		return []byte{}, nil
	}
	if s.sealer == nil {
		return nil, fmt.Errorf("%s; set DOGIT_SECRET_KEY before recording a resource, "+
			"whose secret is a password", secrets.ErrNoKey)
	}
	plain, err := json.Marshal(secret)
	if err != nil {
		return nil, fmt.Errorf("write down the resource's secret: %w", err)
	}
	return s.sealer.Seal(plain)
}

// openSecret decrypts what a resource row keeps sealed.
func (r *ResourceRepo) openSecret(stored []byte) (resource.Parts, error) {
	if len(stored) == 0 {
		return nil, nil
	}
	plain, err := r.s.Integrations().open(json.RawMessage(stored))
	if err != nil {
		return nil, err
	}
	out := resource.Parts{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("read the resource's secret: %w", err)
	}
	return out, nil
}
