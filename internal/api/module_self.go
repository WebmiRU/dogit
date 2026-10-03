package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/store"
)

// handleModuleSelf answers "which module am I?", so a module can confirm it
// registered as intended and see what the core holds for it.
func (s *Server) handleModuleSelf(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, nil, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"module": map[string]any{
			"id":             integration.ID,
			"kind":           integration.Kind,
			"name":           integration.Name,
			"endpoint":       integration.Endpoint,
			"module_version": integration.ModuleVersion,
			"status":         integration.Status,
			"enabled":        integration.Enabled,
		},
		// Not masked, and the reason is that this is the module's own configuration.
		//
		// A module that cannot read its own token cannot work: the telegram module
		// spent a while taking its bot token from the environment instead and treating
		// the stored value as unusable, which is a workaround for a rule that was
		// protecting nobody. Masking belongs on the path to a browser, where an
		// operator's session is what could leak it — and there is none here.
		"settings": unmaskedSettings(settings),
	})
}

// handleModuleSelfSettingsWrite lets a module record its own configuration.
//
// A module knows things the core does not: the token an operator gave it, the
// address it is really reachable at. Requiring a person to copy those into the
// settings page is how they end up wrong, and a module that cannot record what it
// was told keeps asking every time it starts.
//
// Only settings the module itself declared are accepted, and only at the instance
// scope: a module configuring a group or a project would be writing policy on
// somebody's behalf.
func (s *Server) handleModuleSelfSettingsWrite(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	var req struct {
		Values map[string]json.RawMessage `json:"values"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}
	if len(req.Values) == 0 {
		s.writeError(w, r, errBadRequest("no settings were given"))
		return
	}

	for key, value := range req.Values {
		if !moduleDeclaresSetting(integration, key) {
			s.writeError(w, r, errBadRequestf("module %s does not declare a setting named %q",
				integration.Kind, key))
			return
		}
		if problem := s.checkSettingValue(integration, key, value); problem != nil {
			s.writeError(w, r, problem)
			return
		}
	}

	for key, value := range req.Values {
		if err := s.store.Integrations().SetSetting(r.Context(), integration.ID,
			store.ScopeInstance, nil, key, value); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.log.Info("module recorded its own setting", "kind", integration.Kind, "key", key)
	}

	settings, err := s.store.Integrations().SettingsAt(r.Context(), integration.ID, store.ScopeInstance, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"settings": settings})
}

// handleModuleSelfSettings returns the effective settings for a project or group.
//
// This is what a job's environment is built from: the module asks the core what it
// is supposed to be for this project and inherits instance defaults, group
// overrides and project overrides without knowing how those layers are stored.
func (s *Server) handleModuleSelfSettings(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	var groupID, projectID *uuid.UUID

	if raw := strings.TrimSpace(r.URL.Query().Get("group")); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			s.writeError(w, r, errBadRequest("group must be an id"))
			return
		}
		groupID = &id
	}

	if raw := strings.TrimSpace(r.URL.Query().Get("project")); raw != "" {
		// A project may be given as an id or as a path, because a job script knows
		// the path and nothing else.
		id, err := s.resolveProjectRef(r.Context(), raw)
		if err != nil {
			s.writeError(w, r, errNotFoundf("project %q does not exist", raw))
			return
		}
		projectID = id
	}

	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, groupID, projectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"kind":   integration.Kind,
		"scopes": integration.Capabilities.Scopes,
		"schema": integration.Capabilities.Settings,
		// The module's own configuration, unmasked, for the same reason as above.
		"effective": unmaskedSettings(settings),
	})
}

// unmaskedSettings decodes a module's settings as plain values.
//
// Deliberately not the redaction path: a module asking what it was configured with is
// asking for the values it needs to do its job, and it authenticated as itself to ask.
func unmaskedSettings(settings map[string]json.RawMessage) map[string]any {
	out := map[string]any{}
	for key, raw := range settings {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			out[key] = value
			continue
		}
		out[key] = string(raw)
	}
	return out
}

// resolveProjectRef accepts a project id or a project path.
func (s *Server) resolveProjectRef(ctx context.Context, ref string) (*uuid.UUID, error) {
	if id, err := uuid.Parse(ref); err == nil {
		return &id, nil
	}
	project, err := s.store.Projects().ByPath(ctx, strings.Trim(ref, "/"))
	if err != nil {
		return nil, err
	}
	return &project.ID, nil
}
