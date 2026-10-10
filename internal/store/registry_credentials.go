package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RegistryCredentialRepo holds who pushes to a registry written down by an administrator,
// and who says so.
//
// The address and the account on the registry's own row answer for the whole instance.
// This table holds everything narrower than that: a group that pushes somewhere else, a
// project that has an account of its own, a password that was rotated for one team and
// not the rest. The scopes run in the same order the module settings use — instance, then
// group, then project — and each one changes only what it names, so a project can set a
// password without restating the login and inherit the rest.
//
// A registry module has no rows here. It mints its own tokens and knows who its projects
// are; this table is for the addresses that do not.
type RegistryCredentialRepo struct{ s *Store }

func (s *Store) RegistryCredentials() *RegistryCredentialRepo {
	return &RegistryCredentialRepo{s: s}
}

// RegistryCredential is one scope's answer about one registry.
//
// Every field is a partial answer. An empty CredentialSource means the scope above says
// which source this is; a set Login with an empty Password is an account whose password
// is inherited; an empty Login with a set Password is a password for the inherited
// account. That is what makes an override of one field possible, which is what a group
// or a project usually wants.
type RegistryCredential struct {
	RegistryID       uuid.UUID
	ScopeType        string
	ScopeID          *uuid.UUID
	CredentialSource string
	Login            string
	Password         string
}

// ResolvedCredential is what a build should authenticate with, after every scope that
// speaks about this registry has spoken.
type ResolvedCredential struct {
	// Source is CredentialSourceStatic or CredentialSourceUser. Empty only when
	// nothing anywhere said, which Resolve treats as static.
	Source   string
	Login    string
	Password string
}

// RegistryCredentialChange is one scope's answer on its way in, with what was not said
// kept apart from what was said to be nothing.
//
// The distinction is the whole reason this is not RegistryCredential. A scope's row is a
// set of overrides: an empty field means "inherit from above", so a write that arrived
// with an empty field could mean either "leave that one alone" or "inherit that one". The
// two are opposites, and a store that cannot tell them apart will do whichever it was
// written to do to everybody — which is how setting a login for a group quietly threw away
// the password that group had set, and the page then reported the password as inherited
// while a build carried on failing against it. Found by using the page.
type RegistryCredentialChange struct {
	RegistryID uuid.UUID
	ScopeType  string
	ScopeID    *uuid.UUID
	// Nil leaves the stored value alone. A pointer to an empty string clears it, which is
	// how a scope says "inherit this one from above".
	CredentialSource *string
	Login            *string
	Password         *string
}

// Set writes one scope's answer down, changing only what the change names.
//
// Not a replacement, and that is the point of the pointers. A scope's row is a set of
// overrides rather than a copy of the credential, so a form that sets a password and a
// form that later sets a login are two halves of the same arrangement — and replacing the
// row on the second would leave the arrangement as one half, having said nothing about
// removing it. Reading what was there and changing only what was named is also why the
// instance scope is matched on its null scope_id rather than through the unique
// constraint, which cannot see a null and so cannot enforce it.
func (r *RegistryCredentialRepo) Set(ctx context.Context, c RegistryCredentialChange) error {
	scopeID := c.ScopeID
	if c.ScopeType == ScopeInstance {
		scopeID = nil
	}

	kept, found, err := r.one(ctx, c.RegistryID, c.ScopeType, scopeID)
	if err != nil {
		return err
	}

	row := RegistryCredential{}
	if found {
		row = *kept
	}
	if c.CredentialSource != nil {
		row.CredentialSource = strings.TrimSpace(*c.CredentialSource)
	}
	if c.Login != nil {
		row.Login = *c.Login
	}
	if c.Password != nil {
		row.Password = *c.Password
	}

	_, err = r.s.pool.Exec(ctx, `
		DELETE FROM docker_registry_credentials
		WHERE registry_id = $1 AND scope_type = $2 AND scope_id IS NOT DISTINCT FROM $3`,
		c.RegistryID, c.ScopeType, scopeID)
	if err != nil {
		return fmt.Errorf("clear a registry credential: %w", err)
	}

	_, err = r.s.pool.Exec(ctx, `
		INSERT INTO docker_registry_credentials
			(registry_id, scope_type, scope_id, credential_source, login, password)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		c.RegistryID, c.ScopeType, scopeID,
		row.CredentialSource, row.Login, row.Password)
	if err != nil {
		return fmt.Errorf("write a registry credential: %w", err)
	}
	return nil
}

// Resolve is the account a build for this project should authenticate with.
//
// The registry's own row is the base, and each scope that speaks about it replaces only
// what it names. A group row is read with the project's own row absent, so the two never
// shadow each other; they are applied in order instead, which is the same answer and one
// fewer query.
func (r *RegistryCredentialRepo) Resolve(ctx context.Context, reg *DockerRegistry,
	groupID, projectID *uuid.UUID) (ResolvedCredential, error) {

	resolved := ResolvedCredential{
		Source:   registryCredentialSource(reg.CredentialSource),
		Login:    reg.Login,
		Password: reg.Password,
	}
	if reg.ReadOnly {
		// A registry that may only be pulled from is not somewhere a build can push,
		// and saying so here rather than at the push keeps the failure where the
		// decision was written down.
		return resolved, fmt.Errorf("%w: the registry %q is marked read-only, so nothing can be pushed to it",
			ErrConflict, reg.URL)
	}

	scopes := []struct {
		scopeType string
		scopeID   *uuid.UUID
	}{
		{ScopeInstance, nil},
		{ScopeGroup, groupID},
		{ScopeProject, projectID},
	}

	for _, scope := range scopes {
		if scope.scopeType != ScopeInstance && scope.scopeID == nil {
			continue
		}
		row, found, err := r.one(ctx, reg.ID, scope.scopeType, scope.scopeID)
		if err != nil {
			return resolved, err
		}
		if !found {
			continue
		}
		if source := strings.TrimSpace(row.CredentialSource); source != "" {
			resolved.Source = source
		}
		if row.Login != "" {
			resolved.Login = row.Login
		}
		if row.Password != "" {
			resolved.Password = row.Password
		}
	}
	return resolved, nil
}

// Scoped is what one scope has written down about one registry, and whether it wrote
// anything at all.
//
// Read by scope rather than resolved, because the two answer different questions and a
// form needs both: "what does this group have written" is what it can edit, and "what
// would a build for it use" is what the answer has to keep meaning. A form given only
// the second would show a login inherited from the instance as though the group had set
// it, and saving it back would turn an inheritance into a copy — which looks the same
// right up until the instance's login is changed and this group's quietly does not.
func (r *RegistryCredentialRepo) Scoped(ctx context.Context, registryID uuid.UUID,
	scopeType string, scopeID *uuid.UUID) (*RegistryCredential, bool, error) {
	if scopeType == ScopeInstance {
		scopeID = nil
	}
	return r.one(ctx, registryID, scopeType, scopeID)
}

// one is a single scope's row, and whether there was one.
func (r *RegistryCredentialRepo) one(ctx context.Context, registryID uuid.UUID,
	scopeType string, scopeID *uuid.UUID) (*RegistryCredential, bool, error) {

	var row RegistryCredential
	err := r.s.pool.QueryRow(ctx, `
		SELECT credential_source, login, password
		FROM docker_registry_credentials
		WHERE registry_id = $1 AND scope_type = $2 AND scope_id IS NOT DISTINCT FROM $3`,
		registryID, scopeType, scopeID,
	).Scan(&row.CredentialSource, &row.Login, &row.Password)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("read a registry credential: %w", err)
	}
	return &row, true, nil
}
