package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

// NotificationTargetRepo stores where notifications go.
//
// A row is a set of settings belonging to a module, defined at one level and
// inherited downwards. The core stores and resolves these rows without knowing
// what any of the values are: it does not know what a chat id is, and the day
// somebody writes a module for a channel nobody has thought of yet, it keeps
// working for the same reason the settings forms did.
type NotificationTargetRepo struct{ s *Store }

func (s *Store) NotificationTargets() *NotificationTargetRepo {
	return &NotificationTargetRepo{s: s}
}

// NotificationTarget is one recipient, as it was defined at one level.
type NotificationTarget struct {
	ID            uuid.UUID
	IntegrationID uuid.UUID
	ScopeType     string
	ScopeID       *uuid.UUID
	// Label is a name for the human only. Nothing resolves through it.
	Label string
	// Enabled is nil when this level did not decide. See the migration for why
	// that is not the same as false.
	Enabled *bool
	// Position keeps a shared channel from being overtaken by whatever was built
	// first, and it is inherited with the row.
	Position int
	// Overrides names the inherited row this one changes, if it changes one rather
	// than adding a recipient of its own.
	Overrides *uuid.UUID
	Values    map[string]json.RawMessage

	CreatedAt time.Time
	UpdatedAt time.Time
}

// EffectiveTarget is a recipient as it applies to one place.
//
// It answers three questions at once: which rows in the hierarchy were involved,
// what the values work out to when the unspecified ones are inherited, and whether
// it is switched on at all.
type EffectiveTarget struct {
	// Own is the most specific row in the chain: the one that decides the label and
	// the order, and the one an edit would change.
	Own NotificationTarget
	// Root is the row this descends from, which is where the label and the position
	// came from unless something below changed them.
	Root NotificationTarget
	// Values is Own's values over Root's and the levels between: a key is only
	// overridden when a row actually set it.
	Values map[string]json.RawMessage
	// Enabled is the switch, resolved. See NotificationTarget.Enabled.
	Enabled bool
	// DefinedAt is the level of Own — where a change to this recipient belongs.
	DefinedAt string
	// SetHere are the keys Own actually set, which is what lets the interface mark
	// them as overridden rather than as inherited.
	SetHere map[string]bool
}

// TargetResolution is the answer for one place: who gets told, and what no longer
// applies.
//
// Stale is not part of the list on purpose. These rows are the leftovers of a
// project that has been moved to another group, and the case that matters most is
// when there is nothing left to deliver to — if the count rode along with the rows
// there would be nowhere to report it from.
type TargetResolution struct {
	Targets []EffectiveTarget
	// Stale are rows at this scope that override something no longer in scope.
	Stale []uuid.UUID
}

const targetColumns = `id, integration_id, scope_type, scope_id, label, enabled,
	position, overrides, values, created_at, updated_at`

func scanTarget(row interface {
	Scan(dest ...any) error
}) (*NotificationTarget, error) {
	var (
		t         NotificationTarget
		valuesRaw []byte
	)
	if err := row.Scan(&t.ID, &t.IntegrationID, &t.ScopeType, &t.ScopeID, &t.Label,
		&t.Enabled, &t.Position, &t.Overrides, &valuesRaw, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return nil, err
	}
	t.Values = map[string]json.RawMessage{}
	if len(valuesRaw) > 0 {
		if err := json.Unmarshal(valuesRaw, &t.Values); err != nil {
			return nil, fmt.Errorf("decode target values: %w", err)
		}
	}
	return &t, nil
}

// Create adds one row.
func (r *NotificationTargetRepo) Create(ctx context.Context, t *NotificationTarget) (*NotificationTarget, error) {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	if t.Values == nil {
		t.Values = map[string]json.RawMessage{}
	}

	var (
		valuesRaw []byte
		err       error
	)
	if valuesRaw, err = json.Marshal(t.Values); err != nil {
		return nil, fmt.Errorf("encode target values: %w", err)
	}

	row := r.s.pool.QueryRow(ctx, `
		INSERT INTO notification_targets
			(id, integration_id, scope_type, scope_id, label, enabled, position, overrides, values)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING `+targetColumns,
		t.ID, t.IntegrationID, t.ScopeType, t.ScopeID, t.Label, t.Enabled,
		t.Position, t.Overrides, valuesRaw)

	created, err := scanTarget(row)
	if err != nil {
		return nil, fmt.Errorf("create notification target: %w", err)
	}
	return created, nil
}

// Update rewrites one row in place.
func (r *NotificationTargetRepo) Update(ctx context.Context, t *NotificationTarget) (*NotificationTarget, error) {
	if t.Values == nil {
		t.Values = map[string]json.RawMessage{}
	}
	valuesRaw, err := json.Marshal(t.Values)
	if err != nil {
		return nil, fmt.Errorf("encode target values: %w", err)
	}

	row := r.s.pool.QueryRow(ctx, `
		UPDATE notification_targets
		SET label = $2, enabled = $3, position = $4, overrides = $5, values = $6, updated_at = now()
		WHERE id = $1
		RETURNING `+targetColumns,
		t.ID, t.Label, t.Enabled, t.Position, t.Overrides, valuesRaw)

	updated, err := scanTarget(row)
	if err != nil {
		if errors.Is(err, pgxNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("update notification target: %w", err)
	}
	return updated, nil
}

// Delete removes one row, and any rows below it that were overriding it: an
// override with nothing to override is not a recipient, it is a leftover.
func (r *NotificationTargetRepo) Delete(ctx context.Context, id uuid.UUID) error {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM notification_targets WHERE id = $1 OR overrides = $1`, id)
	if err != nil {
		return fmt.Errorf("delete notification target: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ByID reads one row, wherever it is defined.
func (r *NotificationTargetRepo) ByID(ctx context.Context, id uuid.UUID) (*NotificationTarget, error) {
	target, err := scanTarget(r.s.pool.QueryRow(ctx,
		`SELECT `+targetColumns+` FROM notification_targets WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgxNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read notification target: %w", err)
	}
	return target, nil
}

// At returns the rows defined at exactly one level.
func (r *NotificationTargetRepo) At(ctx context.Context, integrationID uuid.UUID,
	scopeType string, scopeID *uuid.UUID) ([]NotificationTarget, error) {

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+targetColumns+` FROM notification_targets
		WHERE integration_id = $1 AND scope_type = $2 AND scope_id IS NOT DISTINCT FROM $3
		ORDER BY position, created_at`, integrationID, scopeType, scopeID)
	if err != nil {
		return nil, fmt.Errorf("read notification targets: %w", err)
	}
	defer rows.Close()

	out := []NotificationTarget{}
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *target)
	}
	return out, rows.Err()
}

// Effective resolves the hierarchy for one place: the rows that apply, what their
// values work out to, and whether each is switched on.
//
// An override is only an override while the row it names is in scope. A project
// moved to another group has rows referring to a channel that no longer applies to
// it, and those are reported as stale rather than treated as new recipients —
// silently turning them into extra channels is exactly the kind of accident that
// makes somebody stop reading the messages.
func (r *NotificationTargetRepo) Effective(ctx context.Context, integrationID uuid.UUID,
	groupID, projectID *uuid.UUID) (TargetResolution, error) {

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+targetColumns+` FROM notification_targets WHERE integration_id = $1`, integrationID)
	if err != nil {
		return TargetResolution{}, fmt.Errorf("read notification targets: %w", err)
	}
	defer rows.Close()

	var all []NotificationTarget
	for rows.Next() {
		target, err := scanTarget(rows)
		if err != nil {
			return TargetResolution{}, err
		}
		all = append(all, *target)
	}
	if err := rows.Err(); err != nil {
		return TargetResolution{}, err
	}

	byID := make(map[uuid.UUID]*NotificationTarget, len(all))
	for i := range all {
		byID[all[i].ID] = &all[i]
	}

	// Which levels are in scope, least specific first.
	var visible []*NotificationTarget
	for i := range all {
		t := &all[i]
		switch t.ScopeType {
		case ScopeInstance:
			visible = append(visible, t)
		case "group":
			if groupID != nil && t.ScopeID != nil && *t.ScopeID == *groupID {
				visible = append(visible, t)
			}
		case "project":
			if projectID != nil && t.ScopeID != nil && *t.ScopeID == *projectID {
				visible = append(visible, t)
			}
		}
	}

	byScope := map[string]int{ScopeInstance: 0, ScopeGroup: 1, ScopeProject: 2}
	sort.SliceStable(visible, func(i, j int) bool {
		return byScope[visible[i].ScopeType] < byScope[visible[j].ScopeType]
	})

	inScope := make(map[uuid.UUID]bool, len(visible))
	for _, t := range visible {
		inScope[t.ID] = true
	}

	// Rows that override something: the winner is the most specific one, and every
	// row in between contributes the values it did not override.
	winner := map[uuid.UUID]*NotificationTarget{}
	chain := map[uuid.UUID][]*NotificationTarget{}
	stale := []uuid.UUID{}

	for _, t := range visible {
		if t.Overrides == nil {
			continue
		}
		if !inScope[*t.Overrides] {
			// Only reported where it is actually used; the same row sitting in
			// another project's list is nobody's problem.
			if t.ScopeType == ScopeProject && projectID != nil && t.ScopeID != nil && *t.ScopeID == *projectID {
				stale = append(stale, t.ID)
			}
			continue
		}
		chain[*t.Overrides] = append(chain[*t.Overrides], t)
		if current, ok := winner[*t.Overrides]; !ok || byScope[t.ScopeType] > byScope[current.ScopeType] {
			winner[*t.Overrides] = t
		}
	}

	out := []EffectiveTarget{}
	for _, root := range visible {
		// A row that is itself an override appears through the row it overrides, and a
		// row overriding something out of scope is not a recipient here at all: it was
		// a change of a channel this place no longer has, and keeping it would quietly
		// turn it into a channel of its own.
		if root.Overrides != nil {
			continue
		}

		levels := append([]*NotificationTarget{root}, chain[root.ID]...)
		sort.SliceStable(levels, func(i, j int) bool {
			return byScope[levels[i].ScopeType] < byScope[levels[j].ScopeType]
		})

		values := map[string]json.RawMessage{}
		enabled := true
		own := root
		for _, level := range levels {
			for key, value := range level.Values {
				values[key] = value
			}
			// The deepest level that said anything decides where a change belongs,
			// even if all it said was to inherit.
			own = level
			if level.Enabled != nil {
				enabled = *level.Enabled
			}
		}

		setHere := map[string]bool{}
		for key := range own.Values {
			setHere[key] = true
		}

		out = append(out, EffectiveTarget{
			Own:       *own,
			Root:      *root,
			Values:    values,
			Enabled:   enabled,
			DefinedAt: own.ScopeType,
			SetHere:   setHere,
		})
	}

	sort.SliceStable(out, func(i, j int) bool {
		left, right := out[i].Root, out[j].Root
		if left.Position != right.Position {
			return left.Position < right.Position
		}
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.Before(right.CreatedAt)
		}
		return left.ID.String() < right.ID.String()
	})

	return TargetResolution{Targets: out, Stale: stale}, nil
}

// PruneStale removes a project's rows that override something no longer in scope,
// and reports which ones those were.
//
// Called when a project changes group, because that is the moment one of its
// settings stops meaning anything. The ids come back so the caller can say so: a
// setting that vanishes without a word is a setting somebody will look for.
func (r *NotificationTargetRepo) PruneStale(ctx context.Context, projectID uuid.UUID) ([]uuid.UUID, error) {
	var groupID *uuid.UUID
	if err := r.s.pool.QueryRow(ctx,
		`SELECT group_id FROM projects WHERE id = $1`, projectID).Scan(&groupID); err != nil {
		if errors.Is(err, pgxNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("read the project's group: %w", err)
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT t.id FROM notification_targets t
		WHERE t.scope_type = 'project' AND t.scope_id = $1
		  AND t.overrides IS NOT NULL
		  AND NOT EXISTS (
			SELECT 1 FROM notification_targets parent
			WHERE parent.id = t.overrides
			  AND (parent.scope_type = 'instance'
			       OR (parent.scope_type = 'group' AND parent.scope_id IS NOT DISTINCT FROM $2)
			       OR (parent.scope_type = 'project' AND parent.scope_id = $1))
		  )`, projectID, groupID)
	if err != nil {
		return nil, fmt.Errorf("read stale notification targets: %w", err)
	}
	defer rows.Close()

	stale := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		stale = append(stale, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(stale) == 0 {
		return nil, nil
	}

	if _, err := r.s.pool.Exec(ctx,
		`DELETE FROM notification_targets WHERE id = ANY($1)`, stale); err != nil {
		return nil, fmt.Errorf("delete stale notification targets: %w", err)
	}
	return stale, nil
}

// Address is this row as the queue wants it: the settings decoded, so they travel
// with the message instead of being looked up again when it is finally sent.
func (e EffectiveTarget) Address() NotificationAddress {
	values := map[string]any{}
	for key, raw := range e.Values {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		values[key] = value
	}
	return NotificationAddress{ID: e.Own.ID, Values: values}
}
