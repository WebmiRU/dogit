package models

import (
	"time"

	"github.com/google/uuid"
)

// ResourceOrigin says how a resource came to be here.
type ResourceOrigin string

const (
	// OriginManaged is a resource this instance created: a database it provisioned, with a
	// role it made. The only kind this instance may destroy.
	OriginManaged ResourceOrigin = "managed"

	// OriginManual is a resource an administrator described — a database on a host we do not
	// run, reached with credentials that are not ours to create or to revoke. Never
	// destroyed by us, only forgotten.
	OriginManual ResourceOrigin = "manual"
)

// Resource is one thing this instance gave to a module, or is holding for one.
//
// Held rather than derived: what a resource *is* is recorded at the moment it is made, so a
// requirement can be compared against it and so an administrator looking at an orphan can see
// what it was without asking a running server anything.
type Resource struct {
	ID       uuid.UUID      `json:"id"`
	Kind     string         `json:"kind"`
	Software string         `json:"software"`
	Version  string         `json:"version"`
	Name     string         `json:"name"`
	Origin   ResourceOrigin `json:"origin"`

	// Address is what a module is handed: a connection string, an endpoint, whatever that
	// kind of resource is reached by. Never sent to a page that only lists resources — the
	// listing says what a thing is, not how to connect to it.
	Address string `json:"address,omitempty"`

	// IntegrationID is the module holding it, and nil is nobody: given and given up, or
	// never given.
	IntegrationID *uuid.UUID `json:"integration_id,omitempty"`

	// ReleasedAt is when it was given up, and LastIntegrationID is who had it, kept after the
	// link is gone. Both exist for the orphan page: "which module used this" is the question
	// asked about a resource nobody holds, and it is otherwise lost with the module.
	ReleasedAt        *time.Time `json:"released_at,omitempty"`
	LastIntegrationID *uuid.UUID `json:"last_integration_id,omitempty"`

	// ModuleKind and ModuleName are the holder's, read for the list so that a page showing
	// twenty resources does not become twenty requests. Not stored: a module can be renamed.
	ModuleKind string `json:"module_kind,omitempty"`
	ModuleName string `json:"module_name,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Held says whether anybody holds this resource.
func (r Resource) Held() bool { return r.IntegrationID != nil }

// Class is what a requirement calls this resource: db, s3.
func (r Resource) Class() string { return r.Kind }

// Coordinate is how this resource is named when compared or shown: `db:postgresql:19.1`.
//
// Three parts because a requirement has three — a class, the software inside it, and a number
// — and the parts are joined by colons. The name an administrator gave is deliberately not in
// it: a name is for a person, and this is for a comparison.
func (r Resource) Coordinate() string {
	out := r.Kind
	if r.Software != "" {
		out += ":" + r.Software
	}
	if r.Version != "" {
		out += ":" + r.Version
	}
	return out
}
