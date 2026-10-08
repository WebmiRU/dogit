package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// handleAttachResource puts a free resource into one of a module's slots.
//
// At the module's level rather than on the resource's page, because the question being asked
// is "what should this module use" and answering it from the other side means reading a list
// of resources and working out which of them belongs to which of a module's needs. A module
// with a database and an object store has no way to say that from a resource list.
//
// Filling a slot that is already filled replaces what was there. One action rather than two:
// releasing first would leave the module with nothing for as long as it takes somebody to click
// the second button, and for a required slot that is a module that has stopped working in the
// middle of an afternoon.
func (s *Server) handleAttachResource(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req struct {
		NeedKey    string `json:"need_key"`
		ResourceID string `json:"resource_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	needKey := strings.TrimSpace(req.NeedKey)
	if needKey == "" {
		s.writeError(w, r, errBadRequest(
			"say which of this module's resources this is for: a module may want a database "+
				"and an object store, and only the module knows which is which"))
		return
	}
	resourceID, err := uuid.Parse(strings.TrimSpace(req.ResourceID))
	if err != nil {
		s.writeError(w, r, errBadRequest("that is not a resource"))
		return
	}

	need, ok := needByKey(integration, needKey)
	if !ok {
		s.writeError(w, r, errBadRequestf(
			"this module has no resource called %q, so nothing could be put there; it asks "+
				"for %s", needKey, strings.Join(needKeys(integration), " and ")))
		return
	}

	candidate, err := s.store.Resources().ByID(r.Context(), resourceID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, errNotFound("no such resource"))
			return
		}
		s.writeError(w, r, err)
		return
	}
	if !resourceSatisfies(*candidate, need) {
		s.writeError(w, r, errBadRequestf(
			"this module asks for a %s, and %s is a %s", describeNeed(need), candidate.Coordinate(),
			candidate.Kind))
		return
	}

	// What was in the slot is remembered before it is moved, so that a failure to attach the
	// new one leaves the module with its old resource rather than with nothing.
	replaced, err := s.store.Resources().ReleaseSlot(r.Context(), integration.ID, needKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.Resources().Grant(r.Context(), resourceID, integration.ID, needKey); err != nil {
		if replaced != nil {
			// Put back what was there. A half-done swap is worse than a refused one: the
			// administrator pressed one button, and the result should either be the new
			// resource or the old one, never neither.
			if giveBack := s.store.Resources().Grant(r.Context(),
				replaced.ID, integration.ID, needKey); giveBack != nil {
				s.log.Error("a resource could not be put back after a failed replacement",
					"resource", replaced.Coordinate(), "module", integration.Kind, "error", giveBack)
			}
		}
		s.writeError(w, r, errBadRequest(err.Error()))
		return
	}

	s.log.Info("a resource was put into a module's slot",
		"kind", integration.Kind, "slot", needKey,
		"resource", candidate.Coordinate(), "replaced", replaced != nil)

	answer := map[string]any{"attached": true, "need_key": needKey,
		"resource": candidate.Coordinate(), "name": candidate.Name}
	if replaced != nil {
		answer["replaced"] = replaced.Coordinate()
	}
	s.publishInstanceEvent(r, models.EventModuleUpdated, integration, answer)
	s.writeJSON(w, r, http.StatusOK, answer)
}

// handleDetachResource takes a resource back out of one of a module's slots.
//
// Refused for a required need, and the refusal says why in the words the module's own manifest
// used: without this the module works and forgets. That is the failure this whole rule is about
// — a module that deploys and keeps nothing looks exactly like a module that has never deployed
// anything, and nothing on the page would say otherwise.
func (s *Server) handleDetachResource(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	needKey := strings.TrimSpace(chi.URLParam(r, "needKey"))
	need, ok := needByKey(integration, needKey)
	if !ok {
		s.writeError(w, r, errBadRequestf(
			"this module has no resource called %q", needKey))
		return
	}
	if need.Required {
		s.writeError(w, r, errBadRequestf(
			"this module says it needs this one: it would carry on working and keep nothing, "+
				"which from the outside is a module that has never deployed anything. Put another "+
				"resource in its place instead — that is one action and this module never sits "+
				"with neither"))
		return
	}

	released, err := s.store.Resources().ReleaseSlot(r.Context(), integration.ID, needKey)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, errBadRequest(
				"nothing is in that slot, so there is nothing to take out"))
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.log.Info("a resource was taken out of a module's slot",
		"kind", integration.Kind, "slot", needKey, "resource", released.Coordinate())
	s.publishInstanceEvent(r, models.EventModuleUpdated, integration, map[string]any{
		"detached": true, "need_key": needKey, "resource": released.Coordinate(),
	})
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"detached": true, "need_key": needKey,
		"resource": released.Coordinate(), "name": released.Name,
	})
}

// needByKey finds one of a module's declared needs by the name it gave it.
func needByKey(integration *models.Integration, key string) (models.ResourceNeed, bool) {
	for _, need := range integration.Capabilities.Needs() {
		if need.Key == key {
			return need, true
		}
	}
	return models.ResourceNeed{}, false
}

func needKeys(integration *models.Integration) []string {
	out := []string{}
	for _, need := range integration.Capabilities.Needs() {
		out = append(out, need.Key)
	}
	if len(out) == 0 {
		return []string{"nothing"}
	}
	return out
}

// describeNeed is what a need is called when it has to be said in a sentence.
func describeNeed(need models.ResourceNeed) string {
	switch {
	case need.Kind == "db" && need.Software != "":
		return need.Kind + ":" + need.Software
	case need.Kind != "":
		return need.Kind
	default:
		return "resource"
	}
}
