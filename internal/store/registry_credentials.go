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

// Set writes one scope's answer down, replacing what was there for that scope.
//
// An upsert rather than an insert-or-error: this is a form somebody saves twice, and a
// form that fails the second time is a form that makes people reload the page first.
// The instance scope is matched on its null scope_id rather than through the unique
// constraint, which cannot see a null and so cannot enforce it.
func (r *RegistryCredentialRepo) Set(ctx context.Context, c RegistryCredential) error {
	scopeID := c.ScopeID
	if c.ScopeType == ScopeInstance {
		scopeID = nil
	}

	_, err := r.s.pool.Exec(ctx, `
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
		strings.TrimSpace(c.CredentialSource), c.Login, c.Password)
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
