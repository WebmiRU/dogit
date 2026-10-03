package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The recipients list: where notifications go, at the instance, for a group, or for
// a project.
//
// A recipient is one row of a module's settings, defined at a level and inherited
// downwards. Somebody may add to what is inherited, change it, or switch it off, and
// the answer the interface needs in order to show all three honestly is "where did
// this value come from" — so that is what every one of these returns.
//
// The permissions are the ones the settings pages already use: the instance belongs
// to an administrator, a group to whoever manages it, a project to whoever manages
// that. Recipients are settings, and should not be the one thing that is easier to
// change than the rest.

// handleListNotificationTargets is the merged list for one place.
//
// Own rows and inherited rows come back together, in the order they are used, each
// saying which level decided it. A row nobody has touched is not marked, because
// marking everything that happened to be inherited would make the interesting part
// invisible.
func (s *Server) handleListNotificationTargets(w http.ResponseWriter, r *http.Request) {
	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	groupID, projectID := s.scopeParents(r.Context(), scopeType, scopeID)

	targets, err := s.resolvedTargets(r, groupID, projectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"targets": targets,
		"modules": s.notificationModules(r),
	})
}

// resolvedTargets is the merged list, with the wording the interface shows.
func (s *Server) resolvedTargets(r *http.Request, groupID, projectID *uuid.UUID) ([]map[string]any, error) {
	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return nil, err
	}

	out := []map[string]any{}
	for _, integration := range integrations {
		if !strings.HasPrefix(integration.Kind, notifyKindPrefix) || !integration.Enabled {
			continue
		}

		s.adoptLegacySettings(r.Context(), integration)

		resolved, err := s.store.NotificationTargets().Effective(r.Context(), integration.ID, groupID, projectID)
		if err != nil {
			return nil, err
		}
		for _, row := range resolved.Targets {
			out = append(out, s.targetRowView(r, integration, row))
		}
	}
	return out, nil
}

// targetRowView is one row of the list.
//
// DefinedAt says where a change belongs; InheritedFrom says where the row itself was
// created. Both are answers to a question people really ask — "why is this here, and
// whose is it" — and neither can be worked out from the values alone.
func (s *Server) targetRowView(r *http.Request, integration *models.Integration,
	row store.EffectiveTarget) map[string]any {

	values := map[string]any{}
	for key, raw := range row.Values {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		values[key] = value
	}

	overridden := row.DefinedAt != row.Root.ScopeType
	own := map[string]any{}
	for key, raw := range row.Own.Values {
		var value any
		if err := json.Unmarshal(raw, &value); err != nil {
			continue
		}
		own[key] = value
	}

	return map[string]any{
		"id":          row.Own.ID,
		"module_id":   integration.ID,
		"module_kind": integration.Kind,
		"module_name": integration.Name,
		"label":       s.targetLabel(integration, row),
		"values":      values,
		"own_values":  own,
		"set_here":    row.SetHere,
		"enabled":     row.Enabled,
		// EnabledHere says this level is the one that switched it, as against every
		// level above it saying the same thing because nobody decided.
		"enabled_here":   row.Own.Enabled != nil && *row.Own.Enabled == row.Enabled,
		"scope_type":     row.DefinedAt,
		"inherited_from": row.Root.ScopeType,
		"overridden":     overridden,
		"position":       row.Root.Position,
		"can_edit":       true,
	}
}

// notificationModules is what may be added to the list: the modules that announce
// things, with what a recipient of theirs is made of.
func (s *Server) notificationModules(r *http.Request) []map[string]any {
	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return []map[string]any{}
	}

	out := []map[string]any{}
	for _, integration := range integrations {
		if !strings.HasPrefix(integration.Kind, notifyKindPrefix) {
			continue
		}
		out = append(out, map[string]any{
			"id": integration.ID, "kind": integration.Kind, "name": integration.Name,
			"enabled":  integration.Enabled,
			"target":   integration.Capabilities.Target,
			"settings": integration.Capabilities.Settings,
		})
	}
	return out
}

type notificationTargetRequest struct {
	IntegrationID string            `json:"module_id"`
	Label         string            `json:"label"`
	Enabled       *bool             `json:"enabled"`
	Values        map[string]string `json:"values"`
	// Overrides names the inherited row this one changes. Empty means a new recipient.
	Overrides string `json:"overrides"`
	Position  *int   `json:"position"`
}

// handleCreateNotificationTarget adds a recipient at a level.
func (s *Server) handleCreateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	var req notificationTargetRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	integrationID, err := uuid.Parse(strings.TrimSpace(req.IntegrationID))
	if err != nil {
		s.writeError(w, r, errBadRequest("which module should deliver to this is required"))
		return
	}
	integration, err := s.store.Integrations().ByID(r.Context(), integrationID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such module"))
		return
	}

	overrides, err := optionalUUID(req.Overrides)
	if err != nil {
		s.writeError(w, r, errBadRequest("the recipient being overridden is not an id"))
		return
	}
	if overrides != nil {
		parent, err := s.store.NotificationTargets().ByID(r.Context(), *overrides)
		if err != nil {
			s.writeError(w, r, errNotFound("the recipient being overridden no longer exists"))
			return
		}
		if parent.ScopeType == scopeType && parent.ScopeID != nil && scopeID != nil &&
			*parent.ScopeID == *scopeID {
			s.writeError(w, r, errBadRequest("a recipient cannot override itself"))
			return
		}
	}

	values, err := s.checkTargetValues(r, integration, req.Values)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	position := 0
	if existing, err := s.store.NotificationTargets().At(r.Context(), integration.ID,
		scopeType, scopeID); err == nil {
		position = len(existing)
	}
	if req.Position != nil {
		position = *req.Position
	}

	created, err := s.store.NotificationTargets().Create(r.Context(), &store.NotificationTarget{
		IntegrationID: integration.ID,
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Label:         strings.TrimSpace(req.Label),
		Enabled:       req.Enabled,
		Position:      position,
		Overrides:     overrides,
		Values:        values,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("notification recipient added",
		"module", integration.Kind, "scope", scopeType, "recipient", created.Label)

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"target": targetView(*created)})
}

// handleUpdateNotificationTarget changes one row: its name, its switch, its values.
func (s *Server) handleUpdateNotificationTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a recipient id is required"))
		return
	}

	existing, err := s.store.NotificationTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such recipient"))
		return
	}
	// The row is changed where it is defined, not where it is seen: a project editing
	// an inherited recipient must first say what it wants of it, not edit above it.
	if err := s.allowSettingScope(r, existing.ScopeType, existing.ScopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	var req notificationTargetRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	integration, err := s.store.Integrations().ByID(r.Context(), existing.IntegrationID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such module"))
		return
	}

	// Only the keys that were sent change, and sending none changes no value at all.
	// A form that shows one field of a recipient would otherwise blank the rest — and
	// so would a switch, which sends no values and only means to be flipped.
	if len(req.Values) > 0 {
		values, err := s.checkTargetValues(r, integration, req.Values)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		existing.Values = values
	}
	if req.Label != "" {
		existing.Label = strings.TrimSpace(req.Label)
	}
	if req.Enabled != nil {
		existing.Enabled = req.Enabled
	}
	if req.Position != nil {
		existing.Position = *req.Position
	}

	updated, err := s.store.NotificationTargets().Update(r.Context(), existing)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"target": targetView(*updated)})
}

// handleDeleteNotificationTarget removes a row at the level that defined it.
func (s *Server) handleDeleteNotificationTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a recipient id is required"))
		return
	}

	existing, err := s.store.NotificationTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such recipient"))
		return
	}
	if err := s.allowSettingScope(r, existing.ScopeType, existing.ScopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.NotificationTargets().Delete(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.log.Info("notification recipient removed", "module", existing.IntegrationID, "scope", existing.ScopeType)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// handleTestNotificationTarget sends one message to one recipient.
//
// The row is addressed by what the interface is holding: the settings a project sees
// on an inherited row are not the row's own, and testing those is what somebody
// clicking the button is asking for.
func (s *Server) handleTestNotificationTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a recipient id is required"))
		return
	}

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	row, err := s.store.NotificationTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such recipient"))
		return
	}

	// The values are what this place would actually send to, and they may be more
	// than the row says on its own.
	groupID, projectID := s.scopeParents(r.Context(), scopeType, scopeID)
	resolved, err := s.store.NotificationTargets().Effective(r.Context(), row.IntegrationID,
		groupID, projectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var address store.NotificationAddress
	found := false
	for _, one := range resolved.Targets {
		if one.Root.ID == row.ID {
			address = one.Address()
			found = true
			break
		}
	}
	if !found {
		// An override whose base has gone: the values on the row itself are all that
		// is left to test with, which is better than refusing.
		address = store.NotificationAddress{ID: row.ID, Values: map[string]any{}}
		for key, raw := range row.Values {
			var value any
			if err := json.Unmarshal(raw, &value); err == nil {
				address.Values[key] = value
			}
		}
	}

	integration, err := s.store.Integrations().ByID(r.Context(), row.IntegrationID)
	if err != nil {
		s.writeError(w, r, errNotFound("no such module"))
		return
	}

	if _, err := s.store.Notifications().Record(r.Context(), "test", integration.Kind,
		"dogit: this is what notifications from this recipient will look like.",
		"test", nil, &address); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("a test notification was queued", "module", integration.Kind,
		"recipient", row.Label, "by", userFrom(r.Context()).Username)

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{"queued": true})
}

// scopeParents is which group and project a scope sits in, for the inheritance the
// recipients are resolved through.
//
// A project knows its group; a group has no group above it; the instance has
// neither.
func (s *Server) scopeParents(ctx context.Context, scopeType string,
	scopeID *uuid.UUID) (*uuid.UUID, *uuid.UUID) {

	switch scopeType {
	case store.ScopeGroup:
		return scopeID, nil
	case store.ScopeProject:
		if scopeID == nil {
			return nil, nil
		}
		project, err := s.store.Projects().ByID(ctx, *scopeID)
		if err != nil {
			// A project that cannot be read has no group to inherit from. Saying so
			// quietly would show an empty list as though nothing were configured.
			s.log.Warn("could not read the project for its recipients", "project", scopeID, "error", err)
			return nil, scopeID
		}
		return project.GroupID, scopeID
	default:
		return nil, nil
	}
}

// checkTargetValues enforces what the module said about its own settings.
func (s *Server) checkTargetValues(r *http.Request, integration *models.Integration,
	values map[string]string) (map[string]json.RawMessage, error) {

	declared := map[string]models.SettingSpec{}
	for _, spec := range integration.Capabilities.Settings {
		declared[spec.Key] = spec
	}

	out := map[string]json.RawMessage{}
	for key, value := range values {
		spec, ok := declared[key]
		if !ok {
			// An unknown setting is a refusal, not a shrug: a form that sends a field
			// nobody declared is a form and a core that have drifted apart.
			return nil, errBadRequest("this module does not have a setting called " + key)
		}
		for _, want := range spec.MustContain {
			if !strings.Contains(value, want) {
				return nil, errBadRequest(spec.WhyContains)
			}
		}
		if spec.Type == "int" {
			if _, err := strconv.Atoi(value); err != nil {
				return nil, errBadRequest(spec.Label + " must be a whole number")
			}
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		out[key] = json.RawMessage(strconv.Quote(value))
	}
	return out, nil
}

// optionalUUID reads an id that may be absent, which is how a form says "no parent".
func optionalUUID(raw string) (*uuid.UUID, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}
