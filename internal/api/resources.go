package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/resource"
	"github.com/ewolf/dogit/internal/store"
)

// handleListResources answers what this instance has given away and what it is holding.
//
// One list rather than two endpoints, because the question behind both is one: what exists.
// A page that asked twice would show two things that disagree the moment something changes
// between the calls, and the reader would have to work out which of them to believe.
func (s *Server) handleListResources(w http.ResponseWriter, r *http.Request) {
	all, err := s.store.Resources().List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The store already left the secrets shut. Said here as well because this is the response
	// that gets pasted into a ticket: a page that answers "what do you have" with every
	// password on the instance thrown in is a page that cannot be shared.
	s.writeJSON(w, r, http.StatusOK, map[string]any{"resources": all})
}

// handleGetResource reads one resource, address and all.
//
// Separate from the list because this is the one page that needs it: an administrator filling
// in an address that was lost, or handing a module the connection string it should have. That
// is behind an administrator's session, which is the only place a password should be read
// out loud, and even here it is only read and never written back.
func (s *Server) handleGetResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "resourceID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("that is not a resource"))
		return
	}
	one, err := s.store.Resources().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such resource"))
		return
	}
	s.writeJSON(w, r, http.StatusOK, one)
}

type createResourceRequest struct {
	Kind     string `json:"kind"`
	Software string `json:"software"`
	Version  string `json:"version"`
	Name     string `json:"name"`
	Origin   string `json:"origin"`

	// Parts is the resource as the form asked for it: host, port, database, user, password for
	// a database; endpoint, region, bucket, keys for an object store. One map rather than a
	// field per part, because the parts belong to the kind and not to this request — and a
	// struct here would need editing for every kind added later, along with the form, the
	// check and the columns, which is four places to forget one.
	//
	// Secrets arrive here too and are sealed on the way in. The core never assembles them into
	// a connection string: what a driver accepts is the driver's business, and a resource of a
	// kind this instance has no template for can still be written down this way.
	Parts map[string]string `json:"parts,omitempty"`

	// ForModuleKind names the kind of module this is for, so that the next one of that kind
	// to register is given this rather than a database created inside this cluster.
	//
	// Optional, and empty means "nobody yet". A resource nobody has claimed is not handed to
	// whatever module arrives next: somebody described it for a module they had in mind, and
	// guessing which one is how the wrong module ends up on somebody's production database.
	ForModuleKind string `json:"for_module_kind,omitempty"`
}

// handleResourceKinds answers what kinds of resource there are and what each is made of.
//
// This is the list the form describing a resource is built from, and the reason it exists rather
// than a list written in the form: a database is host, port, database, user, password, and that
// list is written down in four places — here, in the check that refuses an incomplete one, in the
// columns it is stored in, and in what a module is handed. Four copies drift, and the drift is
// only visible to somebody filling the form in: the core refuses a part the page never showed,
// or accepts one and stores it nowhere.
//
// The `required` and `default` flags are part of the answer rather than the form's own business,
// because they are what makes the form agree with the check: a part the core will not do without
// is a part the form must mark, and one it fills in by default must be offered filled in.
//
// No secret values, and none asked for: this says which fields hold one, not what any of them is.
func (s *Server) handleResourceKinds(w http.ResponseWriter, r *http.Request) {
	descriptors := make([]map[string]any, 0, len(resource.Kinds()))
	for _, kind := range resource.Kinds() {
		fields := make([]map[string]any, 0, len(kind.Fields))
		for _, field := range kind.Fields {
			fields = append(fields, map[string]any{
				"key":      field.Key,
				"label":    field.Label,
				"hint":     field.Hint,
				"secret":   field.Secret,
				"required": field.Required,
				"port":     field.Port,
				"default":  field.Default,
			})
		}
		// An empty list rather than a nil one, because an object store has no software to
		// choose from and JSON says that as `null` — which a form reading `software[0]` takes
		// as an instruction to read the first letter of nothing.
		software := kind.Software
		if software == nil {
			software = []string{}
		}
		descriptors = append(descriptors, map[string]any{
			"key":      kind.Key,
			"label":    kind.Label,
			"note":     kind.Note,
			"software": software,
			"fields":   fields,
		})
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"kinds": descriptors})
}

// handleCreateResource writes down a resource an administrator has described.
//
// This is the answer a module has been getting "no" to: a database on a host this instance
// does not run, reached with credentials that are not ours to create. Nothing here checks
// that the address answers — that is the administrator's to know — but two things are
// checked. That a kind was named, because a resource with no kind cannot be compared against
// what a module says it needs and so cannot answer a requirement. And that this instance has
// a key, because an address that carries a password must not be written in the clear.
func (s *Server) handleCreateResource(w http.ResponseWriter, r *http.Request) {
	var req createResourceRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	if kind == "" {
		s.writeError(w, r, errBadRequest(
			"say what kind of resource this is — db or s3 — because a requirement is written "+
				"against the kind and a resource without one cannot answer one"))
		return
	}

	descriptor, known := resource.ByKey(kind)
	if !known {
		s.writeError(w, r, errBadRequestf(
			"this instance knows about %s and %s resources, not %s",
			ResourceKindDatabase, ResourceKindObjectStore, kind))
		return
	}

	// Trimmed once, here, so that every later step — the check, the duplicate lookup, the
	// columns — sees the same value. A part that differs from itself by a space is a part
	// that duplicates do not match and a payload a module reads as set.
	parts := resource.Parts{}
	for key, value := range req.Parts {
		parts[key] = strings.TrimSpace(value)
	}
	parts = descriptor.Filled(parts)

	if problems := descriptor.Check(parts); len(problems) > 0 {
		s.writeError(w, r, errBadRequestf(
			"this %s is not complete enough to be written down: %s",
			descriptor.Label, strings.Join(problems, "; ")))
		return
	}

	// Said before it is written rather than after: this is the one check that cannot be
	// undone by deleting the row, because the row holds the only copy of the secret and
	// deleting it deletes that.
	_, secret := descriptor.Split(parts)
	if len(secret) > 0 && !s.sealer.Configured() {
		s.writeError(w, r, errBadRequestf(
			"this instance has no DOGIT_SECRET_KEY, so it cannot store a password without "+
				"writing it in the clear; set it first"))
		return
	}

	origin := models.ResourceOrigin(strings.TrimSpace(req.Origin))
	if origin == "" {
		// Described by a person, never created by this instance. The default is the safe
		// answer: a resource we did not make is one we may not destroy.
		origin = models.OriginManual
	}
	if origin != models.OriginManaged && origin != models.OriginManual {
		s.writeError(w, r, errBadRequestf(
			"a resource is either %s — this instance made it — or %s — somebody described it",
			models.OriginManaged, models.OriginManual))
		return
	}

	if taken, err := s.store.Resources().Taken(r.Context(), descriptor, parts); err != nil {
		s.writeError(w, r, err)
		return
	} else if taken {
		s.writeError(w, r, errBadRequest(
			"this instance already has a resource at that address, for that user and database; "+
				"it may be one you wrote down before under another name, and two records for one "+
				"resource is how a database ends up listed twice and deleted once"))
		return
	}

	wanted := strings.TrimSpace(req.ForModuleKind)
	if wanted != "" {
		// Said as a kind exists, not merely as a string: a module kind nobody has registered
		// is a typo, and a typo here is a resource that waits forever for a module that never
		// comes, looking like a resource that was described for a module that does.
		found, err := s.moduleKindRegistered(r.Context(), wanted)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !found {
			s.writeError(w, r, errBadRequestf(
				"no module of kind %q has registered on this instance; check it, because a "+
					"resource waiting for a kind that never arrives looks exactly like one "+
					"waiting for a module that has not come back yet", wanted))
			return
		}
	}

	one, err := s.store.Resources().Put(r.Context(), models.Resource{
		Kind:                kind,
		Software:            strings.ToLower(strings.TrimSpace(req.Software)),
		Version:             strings.TrimSpace(req.Version),
		Name:                strings.TrimSpace(req.Name),
		Origin:              origin,
		Parts:               parts,
		LastIntegrationKind: wanted,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusCreated, one)
}

// handleReleaseResource takes a resource back from the module holding it.
//
// Not the same as forgetting it: the resource stays, sealed, with nobody holding it and with
// the module it belonged to written beside it. That is what an administrator wants when they
// mean "this module must stop using that" without meaning "delete it".
func (s *Server) handleReleaseResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "resourceID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("that is not a resource"))
		return
	}
	released, err := s.store.Resources().ReleaseByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, errBadRequest(
				"this resource is not in use, so there is nothing to take back"))
			return
		}
		s.writeError(w, r, err)
		return
	}
	s.log.Info("resource given up by hand", "resource", released.Coordinate(), "name", released.Name)
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"released": true, "resource": released.Coordinate(), "name": released.Name,
	})
}

// handleForgetResource deletes the record of a resource.
//
// The record goes; the thing may or may not. A managed database is dropped only when the
// caller says so in as many words, because that is the one irreversible act on this page and
// it should not be something a single button next to the name does by accident.
//
// Naming the two apart: forget is `DELETE /resources/{id}`, and dropping what was created
// along with it is `POST /resources/{id}/destroy` — a separate verb, because it is a
// different act and not a stronger form of the same one.
func (s *Server) handleForgetResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "resourceID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("that is not a resource"))
		return
	}
	one, err := s.store.Resources().Forget(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "still in use") {
			s.writeError(w, r, errBadRequest(err.Error()))
			return
		}
		s.writeError(w, r, errNotFound("no such resource"))
		return
	}
	s.log.Info("resource forgotten", "resource", one.Coordinate(), "name", one.Name,
		"origin", one.Origin)
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"forgotten": true, "resource": one.Coordinate(), "name": one.Name,
		"left_alone": one.Origin != models.OriginManaged,
	})
}

// handleDestroyResource forgets a managed resource and drops what this instance created for it.
//
// The other verb, on purpose. Dropping a database is not undoable and nobody has wanted it
// by accident; it is here because a resource this instance made and nobody now holds is a
// database nobody will ever close, and an administrator should be able to close it without
// going to the database to do it by hand.
func (s *Server) handleDestroyResource(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "resourceID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("that is not a resource"))
		return
	}
	one, err := s.store.Resources().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such resource"))
		return
	}
	if one.Held() {
		s.writeError(w, r, errBadRequest(fmt.Sprintf(
			"%s is still using this resource; give it up first", holderOf(*one))))
		return
	}
	if one.Origin != models.OriginManaged {
		s.writeError(w, r, errBadRequestf(
			"this resource was described by an administrator, not created by this instance, "+
				"so this instance cannot destroy it — drop it where it lives, and forget it here"))
		return
	}

	// Name and role, which the module columns still hold: the address is a connection string
	// and dropping wants the two halves separately.
	// The role is looked up on the module that held it, when it is still here. It is
	// normally not: the rule is that removing a module does not destroy its resource, and
	// the point of this verb is destroying what a removed module left behind. So the role is
	// the database's name, which is what provisioning makes it, and this is only an attempt at
	// something better rather than a requirement.
	name := one.Name
	role := name
	if one.LastIntegrationID != nil {
		if previous, err := s.store.Integrations().ByID(r.Context(), *one.LastIntegrationID); err == nil {
			role = previous.DatabaseRole
		}
	}

	if name != "" {
		if err := s.store.Integrations().DropModuleDatabase(r.Context(), s.cfg.DatabaseURL, name, role); err != nil {
			s.writeError(w, r, errBadRequestf(
				"the resource was forgotten in the book but the database it names could not be "+
					"dropped, so it is still there: %v", err))
			return
		}
		s.log.Info("a database this instance created was dropped with its resource",
			"resource", one.Coordinate(), "database", name)
	}

	if _, err := s.store.Resources().Forget(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"destroyed": true, "database": name})
}

// holderOf names whoever holds a resource, for a sentence about it.
func holderOf(one models.Resource) string {
	switch {
	case one.ModuleName != "":
		return one.ModuleName
	case one.ModuleKind != "":
		return one.ModuleKind
	default:
		return "a module"
	}
}

// moduleKindRegistered says whether any module of this kind has ever registered.
//
// A resource written down for a kind nobody has is the one mistake here that is invisible: it
// waits forever, and a list of resources nobody claims is a list somebody stops reading.
func (s *Server) moduleKindRegistered(ctx context.Context, kind string) (bool, error) {
	integrations, err := s.store.Integrations().List(ctx)
	if err != nil {
		return false, err
	}
	for _, one := range integrations {
		if one.Kind == kind {
			return true, nil
		}
	}
	return false, nil
}
