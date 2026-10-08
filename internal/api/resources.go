package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
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

	// Without the addresses. This list is a page of twenty things and their holders; the
	// address of each is a password, and a page that answers "what do you have" with every
	// password on the instance thrown in is a page that will be pasted into a ticket.
	for i := range all {
		all[i].Address = ""
	}
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
	Address  string `json:"address"`
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
	switch kind {
	case ResourceKindDatabase, ResourceKindObjectStore:
	default:
		s.writeError(w, r, errBadRequestf("this instance knows about %s and %s resources, not %s",
			ResourceKindDatabase, ResourceKindObjectStore, kind))
		return
	}

	address := strings.TrimSpace(req.Address)
	if address == "" {
		s.writeError(w, r, errBadRequest(
			"say how this resource is reached; an address with no password in it is still an "+
				"answer to nothing, and one with a password is the only thing this table is for"))
		return
	}

	// Said before it is written rather than after: this is the one check that cannot be
	// undone by deleting the row, because the row holds the only copy of the address and
	// deleting it deletes that.
	if !s.sealer.Configured() {
		s.writeError(w, r, errBadRequestf(
			"this instance has no DOGIT_SECRET_KEY, so it cannot store an address without "+
				"writing the password in the clear; set it first"))
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

	if taken, err := s.store.Resources().Taken(r.Context(), address); err != nil {
		s.writeError(w, r, err)
		return
	} else if taken {
		s.writeError(w, r, errBadRequest(
			"this instance already has a resource with that address; it may be one you wrote "+
				"down before under another name, and two records for one resource is how a "+
				"database ends up listed twice and deleted once"))
		return
	}

	one, err := s.store.Resources().Put(r.Context(), models.Resource{
		Kind:     kind,
		Software: strings.ToLower(strings.TrimSpace(req.Software)),
		Version:  strings.TrimSpace(req.Version),
		Name:     strings.TrimSpace(req.Name),
		Origin:   origin,
		Address:  address,
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
