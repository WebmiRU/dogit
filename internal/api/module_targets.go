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

// handleListModuleTargets is the merged list for one place.
//
// Own rows and inherited rows come back together, in the order they are used, each
// saying which level decided it. A row nobody has touched is not marked, because
// marking everything that happened to be inherited would make the interesting part
// invisible.
func (s *Server) handleListModuleTargets(w http.ResponseWriter, r *http.Request) {
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

	// Settings that no longer mean anything are removed when a project changes group,
	// and the ones removed are reported here rather than left only in the log: a setting
	// that vanishes without a word is a setting somebody will look for.
	stale := s.dropStaleTargets(r)
	if stale > 0 {
		s.log.Info("notification settings that no longer apply were removed",
			"project", scopeType, "removed", stale)
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"targets":       targets,
		"modules":       s.notificationModules(r),
		"stale_removed": stale,
	})
}

// resolvedTargets is the merged list, with the wording the interface shows.
func (s *Server) resolvedTargets(r *http.Request, groupID, projectID *uuid.UUID) ([]map[string]any, error) {
	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return nil, err
	}

	prefix := targetKindPrefix(r)

	out := []map[string]any{}
	for _, integration := range integrations {
		if !strings.HasPrefix(integration.Kind, prefix) || !integration.Enabled {
			continue
		}

		// Only the rows that used to be settings are adopted into targets, and only
		// for notifications: that is the one list that had a legacy form, and moving
		// a deploy module's clusters into rows is this migration's job rather than
		// something a read may do behind somebody's back.
		if prefix == notifyKindPrefix {
			s.adoptLegacySettings(r.Context(), integration)
		}

		resolved, err := s.store.ModuleTargets().Effective(r.Context(), integration.ID, groupID, projectID)
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

	flags := map[string]bool{}
	for key, value := range row.Flags {
		flags[key] = value
	}
	flagsHere := map[string]bool{}
	for key, value := range row.Own.Flags {
		flagsHere[key] = value
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
		"flags":       flags,
		"flags_here":  flagsHere,
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

// dropStaleTargets removes the rows at this scope that override something no longer in
// it, and says how many that was.
//
// Called from the list rather than from the move itself, because the move may be undone:
// a project that goes back to the group it came from should find its settings intact,
// and the moment to decide they are gone is when somebody looks.
func (s *Server) dropStaleTargets(r *http.Request) int {
	scopeType, scopeID, err := s.settingScope(r)
	if err != nil || scopeType != store.ScopeProject || scopeID == nil {
		return 0
	}

	removed, err := s.store.ModuleTargets().PruneStale(r.Context(), *scopeID)
	if err != nil {
		s.log.Warn("could not remove notification settings that stopped applying", "error", err)
		return 0
	}
	return len(removed)
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
		if !strings.HasPrefix(integration.Kind, targetKindPrefix(r)) {
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

// targetKindPrefix is which modules the rows asked for are wanted for, as a prefix.
//
// A prefix rather than an exact kind, because the interesting line is between two
// families: every kind that starts `notify:` is a place to send things, and every kind
// that starts `deploy:` is somewhere to put a release. Defaulting to notifications
// keeps a caller that says nothing from being handed every row there is.
func targetKindPrefix(r *http.Request) string {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		return notifyKindPrefix
	}
	return kind
}

type moduleTargetRequest struct {
	IntegrationID string            `json:"module_id"`
	Label         string            `json:"label"`
	Enabled       *bool             `json:"enabled"`
	// Flags are the switches this level is deciding. Only the keys the module declared
	// are accepted, and only the keys actually present change: a switch sends the key it
	// means and nothing else, so a form that shows one flag must not blank the rest.
	Flags map[string]bool `json:"flags"`
	Values        map[string]string `json:"values"`
	// Overrides names the inherited row this one changes. Empty means a row of its own.
	Overrides string `json:"overrides"`
	Position  *int   `json:"position"`
}

// handleCreateModuleTarget adds a row at a level.
func (s *Server) handleCreateModuleTarget(w http.ResponseWriter, r *http.Request) {
	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	var req moduleTargetRequest
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
		s.writeError(w, r, errBadRequest("the row being overridden is not an id"))
		return
	}
	if overrides != nil {
		parent, err := s.store.ModuleTargets().ByID(r.Context(), *overrides)
		if err != nil {
			s.writeError(w, r, errNotFound("the row being overridden no longer exists"))
			return
		}
		if parent.ScopeType == scopeType && parent.ScopeID != nil && scopeID != nil &&
			*parent.ScopeID == *scopeID {
			s.writeError(w, r, errBadRequest("a row cannot override itself"))
			return
		}
	}

	values, err := s.checkTargetValues(r, integration, req.Values)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	position := 0
	if existing, err := s.store.ModuleTargets().At(r.Context(), integration.ID,
		scopeType, scopeID); err == nil {
		position = len(existing)
	}
	if req.Position != nil {
		position = *req.Position
	}

	created, err := s.store.ModuleTargets().Create(r.Context(), &store.ModuleTarget{
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

	s.log.Info("module target added",
		"module", integration.Kind, "scope", scopeType, "recipient", created.Label)

	s.writeJSON(w, r, http.StatusCreated, map[string]any{"target": targetView(*created)})
}

// sameScopeID says whether two ids name the same place, both of which may be nil.
//
// Written out rather than compared with == because a nil interface and a nil pointer
// are not equal however they are boxed, and "this level has no id" is the normal case
// for the instance rather than an edge worth remembering.
func sameScopeID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// overrideTarget writes a level's own change of a row, and returns the row to write it
// on: the level's own override if it already has one, and a new row if it does not.
//
// The values sent are what this level wants to be different about, not what it wants
// the row to become. That is the difference between an override and a second row, and
// getting it backwards is how a project that changed one namespace ends up pinned to
// a copy of everything the instance had, frozen at the moment it was copied.
func (s *Server) overrideTarget(r *http.Request, parent store.ModuleTarget,
	scopeType string, scopeID *uuid.UUID) (*store.ModuleTarget, error) {

	rows, err := s.store.ModuleTargets().At(r.Context(), parent.IntegrationID, scopeType, scopeID)
	if err != nil {
		return nil, err
	}
	for _, one := range rows {
		if one.Overrides != nil && *one.Overrides == parent.ID {
			return &one, nil
		}
	}

	created, err := s.store.ModuleTargets().Create(r.Context(), &store.ModuleTarget{
		IntegrationID: parent.IntegrationID,
		ScopeType:     scopeType,
		ScopeID:       scopeID,
		Label:         parent.Label,
		Position:      parent.Position,
		Overrides:     &parent.ID,
		Values:        map[string]json.RawMessage{},
	})
	if err != nil {
		return nil, err
	}
	s.log.Info("a row was overridden here", "scope", scopeType, "row", parent.Label)
	return created, nil
}

// handleUpdateModuleTarget changes one row: its name, its switch, its values.
func (s *Server) handleUpdateModuleTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a row id is required"))
		return
	}

	existing, err := s.store.ModuleTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such row"))
		return
	}

	// A change made at a level other than the row's own becomes an override rather
	// than an edit above it.
	//
	// This is the whole point of the hierarchy and it cannot be left to the caller.
	// The row in the list is the row everybody sees, at every level below the one that
	// wrote it, so a project switching it off means "off here" — and writing that on
	// the row itself would switch it off for every project that inherits it. The form
	// asks for an override explicitly; a switch does not, because a switch has nowhere
	// to put the word. So a switch is answered here, where the level is known, rather
	// than refused or obeyed.
	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	if scopeType != existing.ScopeType || !sameScopeID(existing.ScopeID, scopeID) {
		written, err := s.overrideTarget(r, *existing, scopeType, scopeID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		existing = written
	} else if err := s.allowSettingScope(r, existing.ScopeType, existing.ScopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	var req moduleTargetRequest
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
	// The switches, checked against what the module said its rows have.
	//
	// Checked rather than stored, because the column is a map and a map accepts
	// anything: a flag nobody declared would be written down, shown as nothing, and
	// quietly take a name that a later version of the module means something else by.
	if len(req.Flags) > 0 {
		declared := map[string]bool{}
		if target := integration.Capabilities.Target; target != nil {
			for _, one := range target.Flags {
				declared[one.Key] = true
			}
		}
		for key := range req.Flags {
			if !declared[key] {
				s.writeError(w, r, errBadRequestf("a row of this module has no switch called %q", key))
				return
			}
		}
		if existing.Flags == nil {
			existing.Flags = map[string]bool{}
		}
		for key, value := range req.Flags {
			existing.Flags[key] = value
		}
	}
	if req.Enabled != nil {
		existing.Enabled = req.Enabled
	}
	if req.Position != nil {
		existing.Position = *req.Position
	}

	updated, err := s.store.ModuleTargets().Update(r.Context(), existing)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"target": targetView(*updated)})
}

// handleDeleteModuleTarget removes a row at the level that defined it.
func (s *Server) handleDeleteModuleTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a row id is required"))
		return
	}

	existing, err := s.store.ModuleTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such row"))
		return
	}
	if err := s.allowSettingScope(r, existing.ScopeType, existing.ScopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	if err := s.store.ModuleTargets().Delete(r.Context(), id); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.log.Info("module target removed", "module", existing.IntegrationID, "scope", existing.ScopeType)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"deleted": true})
}

// handleTestModuleTarget sends one message to one recipient, to see what a row is
// addressed by before anything is relied on.
//
// The row is addressed by what the interface is holding: the settings a project sees
// on an inherited row are not the row's own, and testing those is what somebody
// clicking the button is asking for.
func (s *Server) handleTestModuleTarget(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(pathParam(r, "targetID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a row id is required"))
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

	row, err := s.store.ModuleTargets().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, errNotFound("no such row"))
		return
	}

	// Sending something is what a notification module does. A row of a deployment
	// module names a place to put a release, and there is no message to send it: the
	// button that appears on every row would then offer to message a cluster.
	integration, err := s.store.Integrations().ByID(r.Context(), row.IntegrationID)
	if err != nil || !strings.HasPrefix(integration.Kind, notifyKindPrefix) {
		s.writeError(w, r, errBadRequest("only a notification row can be sent a test message"))
		return
	}

	// The values are what this place would actually send to, and they may be more
	// than the row says on its own.
	groupID, projectID := s.scopeParents(r.Context(), scopeType, scopeID)
	resolved, err := s.store.ModuleTargets().Effective(r.Context(), row.IntegrationID,
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

// checkTargetValues enforces what the module said about its own settings, and refuses
// anything that is not part of a destination.
//
// The second part is the one that keeps a repository from being shown the module's
// own configuration: a bot token belongs to the bot, and asking a project for it
// because the form happened to render every setting the module declared is how a
// secret ends up copied into twenty places.
func (s *Server) checkTargetValues(r *http.Request, integration *models.Integration,
	values map[string]string) (map[string]json.RawMessage, error) {

	declared := map[string]models.SettingSpec{}
	for _, spec := range integration.Capabilities.Settings {
		declared[spec.Key] = spec
	}

	// A module that says nothing is taken to mean that all of its settings make one
	// recipient, which is the case for a module with nothing but an address.
	recipientKeys := map[string]bool{}
	for _, spec := range integration.Capabilities.Settings {
		recipientKeys[spec.Key] = true
	}
	// A module that says nothing about rows is taken to mean that all of its settings
	// make one.
	if target := integration.Capabilities.Target; target != nil && len(target.Settings) > 0 {
		declaredTarget := target.Settings
		recipientKeys = map[string]bool{}
		for _, key := range declaredTarget {
			recipientKeys[key] = true
		}
	}

	out := map[string]json.RawMessage{}
	for key, value := range values {
		spec, ok := declared[key]
		if !ok {
			// An unknown setting is a refusal, not a shrug: a form that sends a field
			// nobody declared is a form and a core that have drifted apart.
			return nil, errBadRequest("this module does not have a setting called " + key)
		}
		if !recipientKeys[key] {
			// Declared, but not part of a destination. Said plainly rather than
			// quietly dropped, so a form that is one field out of date says which.
			return nil, errBadRequest(spec.Label + " belongs to the module, not to a recipient")
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
