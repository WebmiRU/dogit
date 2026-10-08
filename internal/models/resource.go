package models

import (
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/resource"
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

	// Parts is where this resource is: a host, a database, a user, a bucket. Every part this
	// kind has, filled with whatever storage holds, and never including a secret.
	//
	// Readable on purpose. It is what a page shows to somebody deciding what to do next —
	// which host, which database — and getting it required unsealing a password, because the
	// password used to live in the same sealed string as the hostname. A page that unseals
	// anything eventually prints one.
	Parts resource.Parts `json:"parts,omitempty"`

	// Secret is the sealed half: a database's password, an object store's secret key. Never
	// rendered into a page, and handed to a module only at the moment it is given one.
	Secret resource.Parts `json:"-"`

	// IntegrationID is the module holding it, and nil is nobody: given and given up, or
	// never given.
	IntegrationID *uuid.UUID `json:"integration_id,omitempty"`

	// ReleasedAt is when it was given up, and LastIntegrationKind is what kind of module had
	// it, both kept after the link is gone.
	//
	// The kind and not the id, because the id does not outlive the module: removing a module
	// deletes the row the id pointed at, and after that there is nothing to compare against —
	// so the one case these exist for, a module of the same kind coming back and being given
	// the database it had with its data in it, would be the one case that never fires.
	ReleasedAt *time.Time `json:"released_at,omitempty"`
	// LastIntegrationID is who had it, for the orphan page: "which module used this" is the
	// question asked about a resource nobody holds. Its row may be gone; the kind is not.
	LastIntegrationID   *uuid.UUID `json:"last_integration_id,omitempty"`
	LastIntegrationKind string     `json:"last_integration_kind,omitempty"`

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

// Descriptor is what kind of thing this is, or an empty one if this instance has never heard
// of it. A resource of a kind nothing describes is still a resource, and the page shows it
// rather than refusing to open.
func (r Resource) Descriptor() resource.Kind {
	kind, ok := resource.ByKey(r.Kind)
	if !ok {
		return resource.Kind{}
	}
	return kind
}

// Where is a one-line description of where this resource is, for a page that has one line to
// say it in. Empty when there is nothing to say.
func (r Resource) Where() string {
	parts := r.Descriptor()
	if parts.Key == "" {
		return ""
	}
	plain := parts.Plain(r.Parts)
	switch r.Kind {
	case "db":
		out := plain["host"]
		if plain["database_name"] != "" {
			out += "/" + plain["database_name"]
		}
		return out
	case "s3":
		out := plain["endpoint"]
		if plain["bucket"] != "" {
			out += "/" + plain["bucket"]
		}
		return out
	default:
		return plain["endpoint"]
	}
}
