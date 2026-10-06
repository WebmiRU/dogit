package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulehost"
	"github.com/ewolf/dogit/internal/store"
)

// moduleHeartbeatInterval tells a module how often to call back.
const moduleHeartbeatInterval = 30 * time.Second

// moduleTokenTTL is how long a token minted for a module stays valid. Registry
// clients present it on every layer request, so it must be short: revoking a
// user's access should take effect within minutes, not days.
const moduleTokenTTL = 15 * time.Minute

// maxHeartbeatWindow is how long the core tolerates silence before marking a
// module offline. Several missed heartbeats are allowed so a rolling restart does
// not flip the status in the admin UI.
const maxHeartbeatWindow = 90 * time.Second

type registerModuleRequest struct {
	Kind     string          `json:"kind"`
	Name     string          `json:"name"`
	Endpoint string          `json:"endpoint"`
	Manifest models.Manifest `json:"manifest"`
}

// handleModuleRegister is how a module announces itself.
//
// Authentication uses an instance token created by an administrator: the module
// proves it is allowed to exist, and the core hands back a token the module uses
// on every subsequent call. Registration is idempotent on (kind, name), because a
// module that restarts in Kubernetes comes back with a new address and must not
// appear as a second module.
func (s *Server) handleModuleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerModuleRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	req.Kind = strings.TrimSpace(strings.ToLower(req.Kind))
	req.Name = strings.TrimSpace(req.Name)
	req.Endpoint = strings.TrimSpace(req.Endpoint)

	if err := validateModuleKind(req.Kind); err != nil {
		s.writeError(w, r, err)
		return
	}
	if req.Name == "" || len(req.Name) > 64 {
		s.writeError(w, r, errBadRequest("a module name is required"))
		return
	}
	if err := validateModuleEndpoint(req.Endpoint); err != nil {
		s.writeError(w, r, err)
		return
	}

	registrationToken, err := s.tokenFromHeader(r)
	if err != nil {
		s.writeError(w, r, errUnauthorized("a valid module registration token is required"))
		return
	}
	if _, err := s.store.ModuleTokens().ByHash(r.Context(), registrationToken); err != nil {
		s.writeError(w, r, errUnauthorized("a valid module registration token is required"))
		return
	}

	// The module gets its own token: from here on it authenticates as itself, not
	// with the administrator secret that let it in.
	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	integration, err := s.store.Integrations().Register(r.Context(),
		req.Kind, req.Name, req.Endpoint, hash, req.Manifest)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("module registered",
		"kind", integration.Kind, "name", integration.Name,
		"version", integration.ModuleVersion, "endpoint", integration.Endpoint)

	// A module that asks for a database gets one, here, and the credentials come back
	// exactly once.
	//
	// Provisioning happens at registration rather than on first use because a module
	// cannot work out for itself whether it has one: it has nothing to connect to and
	// nothing to compare against, and "no database" and "not asked for" are the same
	// silence from the other side. A module that declares it wants one and then
	// crashes without keeping the credentials is a module with an empty database
	// nobody will point it at again, which is why the names are also returned — a
	// module that lost them can show an administrator what to restore.
	var database any
	if integration.Capabilities.Database {
		// Only ever once per module.
		//
		// Re-registering is normal — a module that restarts comes back with a new
		// address and says so — and provisioning again would leave the module holding
		// the password to a second, empty database while its first, which has the
		// history in it, is orphaned. So a module that already has one is told nothing
		// and keeps what it kept, and a module that has lost its password is a module
		// whose administrator has to hand it a new one: that is a deliberate act rather
		// than something a restart does to somebody.
		switch {
		case integration.DatabaseName != "":
			s.log.Info("the module already has a database; keeping it",
				"kind", integration.Kind, "database", integration.DatabaseName)
		default:
			provisioned, err := s.store.Integrations().ProvisionModuleDatabase(
				r.Context(), s.cfg.DatabaseURL, integration.Kind)
			if err != nil {
				s.log.Error("could not provision a database for the module",
					"kind", integration.Kind, "error", err)
				s.writeError(w, r, err)
				return
			}

			if err := s.store.Integrations().SetModuleDatabase(r.Context(), integration.ID,
				provisioned.Name, provisioned.Role); err != nil {
				s.log.Warn("the database was created but its name could not be recorded",
					"kind", integration.Kind, "error", err)
			}

			database = map[string]any{
				"url":  provisioned.URL,
				"name": provisioned.Name,
				"role": provisioned.Role,
			}
			s.log.Info("provisioned a database for the module",
				"kind", integration.Kind, "database", provisioned.Name)
		}
	}

	s.publishInstanceEvent(r, models.EventModuleRegistered, integration, map[string]any{
		"enabled": integration.Enabled,
	})

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"integration":        s.integrationView(r, integration, nil, nil),
		"token":              plaintext,
		"database":           database,
		"heartbeat_interval": moduleHeartbeatInterval.String(),
		"expires_at":         time.Now().Add(maxHeartbeatWindow).Format(time.RFC3339),
	})
}

// handleModuleHeartbeat keeps a module marked online.
func (s *Server) handleModuleHeartbeat(w http.ResponseWriter, r *http.Request) {
	integration, err := s.integrationFromRequest(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.store.Integrations().Heartbeat(r.Context(), integration.ID); err != nil {
		s.writeError(w, r, err)
		return
	}

	// Statistics ride along with the heartbeat rather than having their own
	// endpoint: the module is already awake and already talking to us, and a
	// separate endpoint would be a second thing to forget to call.
	var body struct {
		Stats *models.ModuleStats `json:"stats"`
	}
	// A module built before statistics existed sends an empty body, and that is
	// not a malformed request: it is a module saying nothing. Refusing the
	// heartbeat would take a working module offline for the sake of a feature it
	// has never heard of.
	if err := decodeOptionalJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.Stats != nil {
		if err := s.store.ModuleStats().Record(r.Context(), integration.ID, body.Stats); err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"heartbeat_interval": moduleHeartbeatInterval.String(),
		"next_deadline":      time.Now().Add(maxHeartbeatWindow).Format(time.RFC3339),
	})
}

// handleListModules is an administrator's overview of what is installed.
//
// It is admin-only rather than open to every signed-in user: it exposes every
// module's endpoint and token age, which is not everyone's business.
func (s *Server) handleListModules(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integrations, err := s.store.Integrations().List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	views := make([]map[string]any, 0, len(integrations))
	for _, integration := range integrations {
		views = append(views, s.integrationView(r, integration, nil, nil))
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"modules": views})
}

func (s *Server) handleGetModule(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	settings, err := s.store.Integrations().SettingsAt(r.Context(), integration.ID, store.ScopeInstance, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"module":   s.integrationView(r, integration, nil, nil),
		"settings": settings,
	})
}

// handleSetModuleState enables or forbids a module.
//
// Forbidding is the switch an administrator reaches for when a module misbehaves:
// the core stops introducing it to callers, and the module — which has nothing but
// what it is told — closes itself. Nothing is deleted, and allowing it again
// restores the previous state exactly.
// handleGetModuleStats returns what a module last reported, and the readings
// behind it when a range is asked for.
//
// "Last reported" is worded that way on purpose: a reading is only as fresh as
// the module's last heartbeat, and the core is not going to keep reporting on a
// module's behalf after the module stops talking.
func (s *Server) handleGetModuleStats(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ctx := r.Context()

	// The range is validated before anything else, so a request that cannot be
	// served is refused whether or not the module has ever reported.
	query := r.URL.Query()
	var from, to time.Time
	wantsSeries := query.Get("series") != ""
	if wantsSeries {
		to = time.Now()
		if raw := query.Get("to"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				s.writeError(w, r, errBadRequest("to is not a valid timestamp"))
				return
			}
			to = parsed
		}

		from = to.Add(-24 * time.Hour)
		if raw := query.Get("from"); raw != "" {
			parsed, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				s.writeError(w, r, errBadRequest("from is not a valid timestamp"))
				return
			}
			from = parsed
		}
		if to.Before(from) {
			s.writeError(w, r, errBadRequest("from must not be after to"))
			return
		}
	}

	latest, err := s.store.ModuleStats().Latest(ctx, integration.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// A module that has never reported is ordinary on the day it is installed.
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"module_id": integration.ID,
			"latest":    nil,
			"reason":    "never_reported",
			"series":    []models.ModuleStats{},
		})
		return
	case err != nil:
		s.writeError(w, r, err)
		return
	}

	if !wantsSeries {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"module_id": integration.ID, "latest": latest})
		return
	}

	readings, err := s.store.ModuleStats().Series(ctx, integration.ID, from, to)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"module_id": integration.ID,
		"latest":    latest,
		"from":      from.UTC().Format(time.RFC3339),
		"to":        to.UTC().Format(time.RFC3339),
		"series":    readings,
	})
}

func (s *Server) handleSetModuleState(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}
	if body.Enabled == nil {
		s.writeError(w, r, errBadRequest("enabled is required"))
		return
	}

	if err := s.store.Integrations().SetEnabled(r.Context(), integration.ID, *body.Enabled); err != nil {
		s.writeError(w, r, err)
		return
	}

	action := "forbidden"
	if *body.Enabled {
		action = "allowed"
	}
	s.log.Info("module state changed",
		"kind", integration.Kind, "state", action, "user", userFrom(r.Context()).Username)

	updated, err := s.store.Integrations().ByID(r.Context(), integration.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.publishInstanceEvent(r, models.EventModuleUpdated, updated, map[string]any{
		"enabled": updated.Enabled,
	})

	s.writeJSON(w, r, http.StatusOK, map[string]any{"module": updated})
}

// publishInstanceEvent records something that happened to a module.
//
// Module events carry no project: a module belongs to the instance rather than to
// anything inside it, and the only page that watches them is the administrator's.
func (s *Server) publishInstanceEvent(r *http.Request, kind models.EventKind,
	integration *models.Integration, payload map[string]any) {

	if s.events == nil {
		return
	}
	body := payload
	if body == nil {
		body = map[string]any{}
	}
	body["kind"] = integration.Kind
	body["name"] = integration.Name

	// Nil when nobody is behind the call: a module registering itself has no
	// session, and a page being read has nobody to attribute its events to.
	var actorID *uuid.UUID
	if user := userFrom(r.Context()); user != nil && user.ID != uuid.Nil {
		id := user.ID
		actorID = &id
	}
	if err := s.events.Publish(r.Context(), kind, nil, actorID, body); err != nil {
		s.log.Debug("publish a module event", "kind", kind, "error", err)
	}
}

// handleModuleReport records a change a module made to something the core does not
// hold.
//
// The core knows what it has: pipelines, jobs, projects, permissions. It does not
// know what is inside a module — which images a registry holds, what a cache
// evicted. Yet a page has to be able to say "this changed", and the honest way for
// it to be told is by the module that did the changing.
//
// A module can be believed about itself because it authenticates with the token it
// was given at registration, and the core never asks anybody else to vouch for it.
// It is believed about nothing else: the payload is recorded as the module's own
// account of what it did, and nothing reads it as anything more.
func (s *Server) handleModuleReport(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	var req struct {
		// What happened, in the module's own words: "deleted", "pushed", whatever
		// that module calls it. Nobody here interprets it.
		Change string `json:"change"`
		// Subject is what it happened to — a project path, a repository name. Also
		// not interpreted, only carried, so that a page can decide what to re-read.
		Subject string `json:"subject"`
		Detail  any    `json:"detail,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	change := strings.TrimSpace(req.Change)
	if change == "" {
		s.writeError(w, r, errBadRequest("a module must say what it changed"))
		return
	}

	s.publishInstanceEvent(r, models.EventModuleReported, integration, map[string]any{
		"change":  change,
		"subject": strings.TrimSpace(req.Subject),
		"detail":  req.Detail,
	})

	s.writeJSON(w, r, http.StatusOK, map[string]any{"recorded": true})
}

func (s *Server) handleDeleteModule(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.store.Integrations().Delete(r.Context(), integration.ID); err != nil {
		s.writeError(w, r, err)
		return
	}
	s.log.Info("module removed", "kind", integration.Kind, "name", integration.Name)
	s.publishInstanceEvent(r, models.EventModuleRemoved, integration, nil)
	s.writeJSON(w, r, http.StatusNoContent, nil)
}

type setSettingRequest struct {
	Value json.RawMessage `json:"value"`
}

// handleSetModuleSettings writes settings at one scope: instance, group or
// project. Scoping happens here rather than in the module so that inheritance and
// overrides are decided in one place for every module.
func (s *Server) handleSetModuleSettings(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var req setSettingRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		s.writeError(w, r, errBadRequest("a setting key is required"))
		return
	}
	if !moduleDeclaresSetting(integration, key) {
		// Writing a setting the module never announced would leave it silently
		// ignored, which is worse than a clear rejection.
		s.writeError(w, r, errBadRequestf("module %s does not declare a setting named %q", integration.Kind, key))
		return
	}

	// A module may require something of a setting's value — that an image name
	// template contain its project, for instance. It says so in its manifest, in
	// its own words, and the core checks it here where refusing is free. Catching it
	// at push time instead would mean a registry whose images cannot be traced back
	// to a project, discovered by somebody trying to use them.
	if problem := s.checkSettingValue(integration, key, req.Value); problem != nil {
		s.writeError(w, r, problem)
		return
	}

	// A value that is nothing means this scope has stopped saying it, rather than
	// saying it is nothing — and a value that says exactly what the level above says is
	// not an override either. See withoutEmpties and withoutRedundant.
	value, err := withoutEmpties(integration, key, req.Value)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	value = s.withoutRedundant(r, integration, key, value, scopeType, scopeID)

	// A list with nothing in it says nothing at this level, and a level that says
	// nothing is a level with no row: stored, it would be an empty answer to a question
	// that has an inherited one waiting above it.
	if isEmptyList(value) {
		// Nothing to delete is nothing decided here, which is what an empty list means.
		if err := s.store.Integrations().DeleteSettingAt(r.Context(), integration.ID,
			scopeType, scopeID, key); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.writeError(w, r, err)
			return
		}
	} else if err := s.store.Integrations().SetSetting(r.Context(), integration.ID, scopeType, scopeID, key, value); err != nil {
		s.writeError(w, r, err)
		return
	}

	settings, err := s.store.Integrations().SettingsAt(r.Context(), integration.ID, scopeType, scopeID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"settings": settings})
}

// handleResetModuleSetting puts one setting back to what the module declared.
//
// An operator who set a value they did not mean — and was allowed to, because the
// module allowed it — needs a way back that is not "remember what it was". There
// is no DELETE on the settings endpoint itself, because deleting the row and
// deleting the setting are different things and only the first is meaningful here.
func (s *Server) handleResetModuleSetting(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	key := strings.TrimSpace(r.URL.Query().Get("key"))
	if key == "" {
		s.writeError(w, r, errBadRequest("a setting key is required"))
		return
	}
	if !moduleDeclaresSetting(integration, key) {
		s.writeError(w, r, errBadRequestf("module %s does not declare a setting named %q",
			integration.Kind, key))
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

	if err := s.store.Integrations().DeleteSettingAt(r.Context(), integration.ID, scopeType, scopeID, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Already at its default, which is the state that was asked for. Saying so
			// rather than refusing keeps the button from becoming an error for
			// something the operator has already done.
			s.writeJSON(w, r, http.StatusOK, map[string]any{"reset": key, "already": true})
			return
		}
		s.writeError(w, r, err)
		return
	}

	s.log.Info("module setting reset to its default", "kind", integration.Kind, "key", key,
		"scope", scopeType, "user", userFrom(r.Context()).Username)

	s.writeJSON(w, r, http.StatusOK, map[string]any{"reset": key})
}

// handleSetModuleSettingsBulk writes several settings at once.
//
// One request rather than one per row, because that is what the interface does:
// a form is submitted, not a series of unrelated actions. It also makes the write
// one decision — every value is checked before any is stored, so a form with one
// wrong value changes nothing rather than half of it.
func (s *Server) handleSetModuleSettingsBulk(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

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

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if err := s.allowSettingScope(r, scopeType, scopeID); err != nil {
		s.writeError(w, r, err)
		return
	}

	// Everything is checked first. A form saved in one go should either land or
	// leave nothing half-written, or the operator is left guessing which half took.
	for key, value := range req.Values {
		if !moduleDeclaresSetting(integration, key) {
			// Writing a setting the module never announced would leave it silently
			// ignored, which is worse than a clear rejection.
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
		// A secret inside a list comes back as a mask, and the form sends the mask
		// back unchanged — the browser never had the value to send. Writing that
		// through would replace somebody's kubeconfig with a row of asterisks, so
		// the masked fields are put back to what is already stored.
		value, err := s.restoreMaskedSecrets(r.Context(), integration.ID, scopeType, scopeID,
			key, value)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		value, err = withoutEmpties(integration, key, value)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		value = s.withoutRedundant(r, integration, key, value, scopeType, scopeID)

		if isEmptyList(value) {
			// Nothing to delete is nothing decided here, which is what an empty list means.
			if err := s.store.Integrations().DeleteSettingAt(r.Context(), integration.ID,
				scopeType, scopeID, key); err != nil && !errors.Is(err, store.ErrNotFound) {
				s.writeError(w, r, err)
				return
			}
		} else if err := s.store.Integrations().SetSetting(r.Context(), integration.ID,
			scopeType, scopeID, key, value); err != nil {
			s.writeError(w, r, err)
			return
		}
		s.log.Info("module setting changed", "kind", integration.Kind, "key", key,
			"scope", scopeType, "user", userFrom(r.Context()).Username)
	}

	settings, err := s.store.Integrations().SettingsAt(r.Context(), integration.ID, scopeType, scopeID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{"settings": settings})
}

// secretMask is what a secret comes back as, because it is stored but never returned.
const secretMask = "********"

// settingSpecAt is what the registered module declared about one setting.
func (s *Server) settingSpecAt(ctx context.Context, integrationID uuid.UUID,
	key string) (models.SettingSpec, bool) {

	integration, err := s.store.Integrations().ByID(ctx, integrationID)
	if err != nil {
		return models.SettingSpec{}, false
	}
	return settingSpecOf(integration, key)
}

// restoreMaskedSecrets puts the stored values back into a list whose secrets arrived
// masked.
//
// Only ever done for a masked field that is present and still holding the mask: an
// operator who cleared the field means it, and one who typed a new value means that.
// A single secret setting is not touched here, because it never comes back at all and
// so is simply not part of the payload.
func (s *Server) restoreMaskedSecrets(ctx context.Context, integrationID uuid.UUID,
	scopeType string, scopeID *uuid.UUID, key string, value json.RawMessage) (json.RawMessage, error) {

	spec, found := s.settingSpecAt(ctx, integrationID, key)
	if !found || spec.Type != "list" || spec.Items == nil {
		return value, nil
	}

	secret := map[string]bool{}
	for _, field := range spec.Items.Fields {
		if field.Secret {
			secret[field.Key] = true
		}
	}
	if len(secret) == 0 {
		return value, nil
	}

	var rows []map[string]any
	if err := json.Unmarshal(value, &rows); err != nil {
		return value, nil
	}

	stored, err := s.store.Integrations().SettingAt(ctx, integrationID, scopeType, scopeID, key)
	if err != nil || len(stored) == 0 {
		return value, nil
	}
	var previous []map[string]any
	if err := json.Unmarshal(stored, &previous); err != nil {
		return value, nil
	}

	changed := false
	for index, row := range rows {
		for field := range secret {
			if row[field] != secretMask {
				continue
			}
			if index < len(previous) {
				if kept, ok := previous[index][field]; ok {
					row[field] = kept
					changed = true
				}
			}
		}
	}
	if !changed {
		return value, nil
	}

	merged, err := json.Marshal(rows)
	if err != nil {
		return value, nil
	}
	return merged, nil
}

// checkSettingValue enforces whatever the module said about one value: its type,
// and any requirement it stated about its contents.
//
// The type is checked here because the module declared it in order to have it
// checked. "600" written where an integer was declared is refused at the door
// rather than stored and found later by whatever read it.
func (s *Server) checkSettingValue(integration *models.Integration, key string, value json.RawMessage) error {
	spec, found := settingSpecOf(integration, key)
	if !found {
		return nil
	}

	if err := checkSettingType(spec, value); err != nil {
		return err
	}

	if len(spec.MustContain) == 0 {
		return nil
	}

	var text string
	if err := json.Unmarshal(value, &text); err != nil {
		// A value of another type cannot be checked against substrings, and a
		// requirement that only sometimes applies would be worse than none.
		return errBadRequestf("%q must be text", spec.Label)
	}

	var missing []string
	for _, required := range spec.MustContain {
		if !strings.Contains(text, required) {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		reason := spec.WhyContains
		if reason == "" {
			reason = "the module requires it, and does not say why"
		}
		return errBadRequestf("%q is missing %s: %s", text, strings.Join(missing, ", "), reason)
	}
	return nil
}

// checkSettingType refuses a value that is not the kind the module declared.
//
// A number written as a quoted string is refused as well as nonsense: the module
// said what it expects, and a value it will have to parse again later is worth
// catching while the operator is still looking at the field.
func checkSettingType(spec models.SettingSpec, value json.RawMessage) error {
	var (
		text    string
		number  float64
		flag    bool
		decoded any
	)

	if err := json.Unmarshal(value, &decoded); err != nil {
		return errBadRequestf("%s is not a value this module accepts", spec.Label)
	}

	switch spec.Type {
	case "int":
		if err := json.Unmarshal(value, &number); err == nil {
			return nil
		}
		return errBadRequestf("%s must be a whole number", spec.Label)

	case "bool":
		if err := json.Unmarshal(value, &flag); err == nil {
			return nil
		}
		return errBadRequestf("%s must be true or false", spec.Label)

	case "string", "text", "url":
		if err := json.Unmarshal(value, &text); err == nil {
			return nil
		}
		return errBadRequestf("%s must be text", spec.Label)

	case "enum":
		if err := json.Unmarshal(value, &text); err == nil {
			for _, option := range spec.Options {
				if option == text {
					return nil
				}
			}
			return errBadRequestf("%s must be one of: %s", spec.Label, strings.Join(spec.Options, ", "))
		}
		return errBadRequestf("%s must be one of: %s", spec.Label, strings.Join(spec.Options, ", "))

	case "list":
		return checkListSetting(spec, value)

	default:
		return nil
	}
}

// switchField is the entry field that says whether a row is in use. See where it is
// allowed in checkListSetting: it is the core's word, not the module's, and it is the
// only way a scope below the writer's says "not here" without rewriting the row.
const (
	switchField     = "enabled"
	autoDeployField = "auto_deploy"
	// rowIDField is how a row is recognised as the same row when its name changes.
	//
	// The name says which cluster a row is, and a lower level may decide to call it
	// something else — that is an override of one field, not a second row of the same
	// cluster. Without an identity of its own a row could only be matched to the one it
	// overrides by its name, so a renamed row could only become a new row.
	//
	// The core's own field, written and never shown: a page does not decide which row
	// is which, and a row's identity is not something anybody reads.
	rowIDField = "dogit_row_id"
	// rowOwnField says a row was written at the scope asking for it, rather than
	// inherited. The core is the only thing that knows: the row the page receives is
	// stripped down to what this scope decided, and a row that has decided nothing here
	// looks exactly like a row somebody just added and left alone.
	//
	// It decides two things, and both are things a page must not get wrong: whether the
	// row may be deleted here (an inherited row may only be switched off), and whether
	// saving a shorter list is a deletion or a no-op. Answering it by guessing from the
	// fields a row happens to carry is how an inherited row ended up with a Remove on it
	// and an administrator's own cluster without one.
	rowOwnField = "dogit_row_own"
	// rowNameField is what a row is called, for a page to call it by.
	//
	// It is the core's word and not a field: a name this scope has not decided is not
	// sent as one, because an empty field that means "inherited" and comes back holding
	// the inherited value teaches that clearing a field does nothing. The page still has
	// to be able to say which place a row is — a row nobody can name is a row nobody can
	// find — so the name comes as itself rather than as a field to save.
	rowNameField = "dogit_row_name"
)

// checkListSetting refuses a list that is not a list of the fields the module
// declared.
//
// Checked at the door rather than left to the module: the module described these
// fields precisely so that a row with a field nobody declared would be caught before
// it was stored, and because a secret field inside an entry is only maskable while
// the core knows which fields those are. A value of a different shape would defeat
// both.
func checkListSetting(spec models.SettingSpec, value json.RawMessage) error {
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(value, &rows); err != nil {
		return errBadRequestf("%s must be a list", spec.Label)
	}

	if spec.Items == nil {
		// A list that declares no fields is a module that has not described itself.
		// Anything is refused rather than stored, because nothing could be rendered
		// from it and no secret inside it could ever be masked.
		return errBadRequestf("%s is a list but describes no fields", spec.Label)
	}

	byKey := map[string]models.SettingSpec{}
	for _, field := range spec.Items.Fields {
		byKey[field.Key] = field
	}

	// "enabled" and "auto_deploy" are the core's, not the module's: they say whether a row
	// is in use here and whether a push may act on it by itself, which are the two things
	// a scope below the one that wrote the row may say about it without rewriting it.
	// A row that is switched off is not removed — it is still there, still configured,
	// and can be switched back on — which is why these are fields and not deletions.
	//
	// Only for a list whose entries have names: a row of a list nobody can identify
	// cannot be switched off by name, and a switch nobody could find would be worse
	// than none.
	if len(spec.Items.Identify) > 0 {
		byKey[switchField] = models.SettingSpec{Key: switchField, Label: "In use", Type: "bool"}
		byKey[autoDeployField] = models.SettingSpec{
			Key: autoDeployField, Label: "Autodeploy", Type: "bool"}
		byKey[rowIDField] = models.SettingSpec{Key: rowIDField, Label: "Row", Type: "string"}
		byKey[rowOwnField] = models.SettingSpec{Key: rowOwnField, Label: "Own row", Type: "bool"}
		byKey[rowNameField] = models.SettingSpec{Key: rowNameField, Label: "Row name", Type: "string"}
	}

	for index, row := range rows {
		for key, entry := range row {
			field, known := byKey[key]
			if !known {
				return errBadRequestf(
					"%s has no field called %q in entry %d", spec.Label, key, index+1)
			}
			if problem := checkSettingType(field, entry); problem != nil {
				return problem
			}
		}
	}
	return nil
}

func (s *Server) handleGetModuleSettings(w http.ResponseWriter, r *http.Request) {
	integration, err := s.moduleFromPath(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	at, err := s.store.Integrations().SettingsAt(r.Context(), integration.ID, scopeType, scopeID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The effective view is what a job actually runs with, so it is returned
	// alongside the raw rows.
	var groupID, projectID *uuid.UUID
	switch scopeType {
	case store.ScopeGroup:
		groupID = scopeID
	case store.ScopeProject:
		projectID = scopeID
	}

	// The levels above this one, and only those. For the instance there are none, so
	// an instance form has nothing inherited and every row in it is its own.
	inheritedGroup, inheritedProject := groupID, projectID
	noLevelsAbove := false
	switch scopeType {
	case store.ScopeGroup:
		inheritedGroup = nil
	case store.ScopeProject:
		inheritedProject = nil
	}
	if scopeType == store.ScopeInstance {
		// Nothing is above the instance. Resolving "the levels above" here would answer
		// with the instance's own rows, and every row on that page would then read as
		// somebody else's row that this scope may not delete.
		inheritedGroup, inheritedProject = nil, nil
		noLevelsAbove = true
	}
	effective, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, groupID, projectID, integration.Capabilities.Settings)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// What this scope did not decide: the same resolution stopped one level above.
	// It is what lets the form say which of the rows it shows are this scope's own,
	// and what stops a save from writing down every inherited row as if it had been
	// decided here.
	var inherited map[string]json.RawMessage
	if noLevelsAbove {
		inherited = map[string]json.RawMessage{}
	} else {
		inherited, err = s.store.Integrations().SettingsFor(r.Context(), integration.ID,
			inheritedGroup, inheritedProject, integration.Capabilities.Settings)
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Redacted here exactly as the module view redacts it, because this is the other
	// way the same values leave the core and a secret masked on one path and not on
	// the other is a secret that was never masked.
	declared := map[string]bool{}
	specs := map[string]models.SettingSpec{}
	for _, spec := range integration.Capabilities.Settings {
		declared[spec.Key] = spec.Secret
		specs[spec.Key] = spec
	}

	// What this scope decided, and nothing that it inherited.
	//
	// The values a level above holds are not sent to a page at all. Not masked, not
	// blanked afterwards: not sent. A browser that has a group's settings in it has
	// them whatever the page does with them, and there is no level at which somebody
	// who may read a project should be holding a credential the instance wrote for
	// somebody else.
	//
	// A row's name still comes, because a row has to be nameable to be shown as
	// inherited — that is what says "this exists, and it is not yours to change". Every
	// other field of an inherited row is left out, and the page shows it empty.
	own := s.ownSettings(effective, inherited, integration.Capabilities.Settings)

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"scope":    scopeType,
		"settings": at,
		"own": redactSettings(own, declared, func(key string) (models.SettingSpec, bool) {
			spec, ok := specs[key]
			return spec, ok
		}),
		"schema": integration.Capabilities.Settings,
	})
}

// ownSettings is what one scope decided: the effective values minus the ones that are
// exactly what the level above says.
//
// A field is left out rather than blanked when the two agree, because a page cannot tell
// a field it was given from a field it was sent, and one that looks written but is not
// is a value somebody will save back unchanged.
func (s *Server) ownSettings(effective, inherited map[string]json.RawMessage,
	specs []models.SettingSpec) map[string]json.RawMessage {

	out := map[string]json.RawMessage{}
	for key, value := range effective {
		above, hasAbove := inherited[key]

		var spec *models.SettingSpec
		for i := range specs {
			if specs[i].Key == key {
				spec = &specs[i]
				break
			}
		}
		named := spec != nil && spec.Items != nil && len(spec.Items.Identify) > 0

		if !hasAbove {
			// Nothing above holds this key at all. A named list still goes row by row:
			// every row of it was written here, and saying so is what lets the page offer
			// Remove on an administrator's own cluster and on nothing else.
			if !named {
				out[key] = value
				continue
			}
			if own := ownEntries(value, nil, spec.Items.Identify); len(own) > 0 {
				out[key] = own
			}
			continue
		}

		if !named {
			// A plain value: this scope's answer or nobody's.
			if !sameJSON(value, above) {
				out[key] = value
			}
			continue
		}

		own := ownEntries(value, above, spec.Items.Identify)
		if len(own) > 0 {
			out[key] = own
		}
	}
	return out
}

// ownEntries is a list of rows reduced to what this scope decided about each of them.
//
// A row with no counterpart above is this scope's own and comes whole. A row that is
// somebody else's keeps its name — that is how the page says "this row exists and is
// inherited" — and nothing else.
func ownEntries(effective, inherited json.RawMessage, identify []string) json.RawMessage {
	var applied, above []map[string]json.RawMessage
	if err := json.Unmarshal(effective, &applied); err != nil {
		return effective
	}
	_ = json.Unmarshal(inherited, &above)

	nameOf := func(row map[string]json.RawMessage) string {
		parts := make([]string, 0, len(identify))
		for _, field := range identify {
			raw, ok := row[field]
			if !ok {
				return ""
			}
			parts = append(parts, strings.Trim(string(raw), `"`))
		}
		return strings.Join(parts, " ")
	}

	// The row a lower scope is talking about is the one it carries the identity of; a row
	// with no identity is matched by its name, which is all there is to match on.
	sameRow := func(one, other map[string]json.RawMessage) bool {
		if id, ok := one[rowIDField]; ok {
			if theirs, had := other[rowIDField]; had && string(id) == string(theirs) {
				return true
			}
		}
		// Different identities still fall back to the name, because one side may have
		// been written before rows had identities and the other since: the same row, found
		// by the only thing both of them are called.
		name := nameOf(one)
		return name != "" && nameOf(other) == name
	}

	rows := make([]map[string]json.RawMessage, 0, len(applied))
	for _, row := range applied {
		var was map[string]json.RawMessage
		for _, one := range above {
			if sameRow(row, one) {
				was = one
				break
			}
		}
		if was == nil {
			// No counterpart above: this scope is where the row was written. Said in the
			// row, because a row that carries nothing else cannot be told apart from one
			// that is inherited and untouched.
			own := make(map[string]json.RawMessage, len(row)+1)
			for field, value := range row {
				own[field] = value
			}
			own[rowOwnField] = json.RawMessage("true")
			rows = append(rows, own)
			continue
		}
		kept := map[string]json.RawMessage{}
		// The identity always: it is what the level above is matched by, and a row that
		// lost it could never be found again.
		if id, ok := row[rowIDField]; ok {
			kept[rowIDField] = id
		}
		// The row's own name, as a name: it is how the page says "this place exists, and
		// it is not yours to change". It is not written into the fields, because a field
		// is an answer this scope gave, and the answer "the place above is called this"
		// is not one — it is what happens when nobody has answered. The loop below still
		// keeps the name as a field when this scope has given one of its own, which is a
		// rename.
		//
		// The first identifying field and not the whole identity joined: a row of a list
		// that identifies itself by a name and a namespace is called its name, and a page
		// handed "production web" would look for a cluster with a space in its name and
		// find nothing.
		if len(identify) > 0 {
			if first, ok := row[identify[0]]; ok {
				kept[rowNameField] = first
			}
		} else if name := nameOf(row); name != "" {
			kept[rowNameField] = json.RawMessage(strconv.Quote(name))
		}
		// The whole identity, always — name and namespace, or whatever else a row is
		// found by.
		//
		// Not as answers and not by this scope's decision: these are how the row is
		// addressed, and a page that cannot address a row cannot ask about it. With the
		// namespace missing, a card asked for one place's history was sent the other
		// one's as well — two places in one cluster, and one page showing both under
		// one name. Which is what "not inherited values are sent" has to yield to: the
		// rule is about answers, and a place's own coordinates are not an answer, they
		// are where it is.
		for _, field := range identify {
			if value, ok := row[field]; ok {
				kept[field] = value
			}
		}
		for field, value := range row {
			if previous, had := was[field]; !had || !sameJSON(value, previous) {
				kept[field] = value
			}
		}
		rows = append(rows, kept)
	}
	if len(rows) == 0 {
		return nil
	}
	encoded, err := json.Marshal(rows)
	if err != nil {
		return effective
	}
	return encoded
}

/**
 * A value with everything empty taken out of it.
 *
 * An emptied field is a decision to stop overriding, not a decision to override with
 * nothing: what it goes back to is the level above's value, which only the core holds.
 * Writing "" instead would keep the override alive with nothing in it, and a deployment
 * would be told the namespace is the empty string.
 *
 * Only strings and numbers are emptied this way. A boolean is not: off is an answer,
 * not an absence, and a row whose Autodeploy is off must stay written down rather than
 * quietly becoming "whatever the level above says".
 */
func withoutEmpties(integration *models.Integration, key string, value json.RawMessage) (json.RawMessage, error) {
	var spec *models.SettingSpec
	for i := range integration.Capabilities.Settings {
		if integration.Capabilities.Settings[i].Key == key {
			spec = &integration.Capabilities.Settings[i]
			break
		}
	}
	if spec == nil || spec.Type != "list" || spec.Items == nil {
		return value, nil
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(value, &rows); err != nil {
		return value, nil
	}

	// A row of a list whose entries have names gets an identity of its own, so that
	// changing the name later is a change of one field rather than a second row.
	named := len(spec.Items.Identify) > 0
	identified := false
	if named {
		for _, row := range rows {
			if _, has := row[rowIDField]; !has {
				row[rowIDField] = json.RawMessage(strconv.Quote(uuid.NewString()))
				identified = true
			}
		}
	}

	emptied, stripped := false, false
	kept := make([]map[string]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		trimmed := make(map[string]json.RawMessage, len(row))
		for field, raw := range row {
			// Whose row a row is, and what it is called, are the core's to say. A page
			// may send both back after reading them, and storing them would let the next
			// read believe the page.
			if field == rowOwnField || field == rowNameField {
				stripped = true
				continue
			}
			var text string
			if err := json.Unmarshal(raw, &text); err == nil && text == "" {
				emptied = true
				continue
			}
			trimmed[field] = raw
		}
		if len(trimmed) > 0 {
			kept = append(kept, trimmed)
		}
	}
	if !emptied && !identified && !stripped {
		return value, nil
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return value, nil
	}
	return encoded, nil
}

// isEmptyList says whether a value is a list with nothing in it.
func isEmptyList(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return false
	}
	return string(trimmed) == "[]" || string(trimmed) == "[ ]" || string(trimmed) == "[]\n"
}

// withoutRedundant takes the overrides out of a list that say what the level above
// already says.
//
// An override is a difference. A field written with the value the level above holds is
// not a decision, it is a copy — and a copy stops following the level above without
// anybody deciding that, quietly, the next time somebody changes it there. A switch that
// was flicked and flicked back ends here as no switch at all.
//
// The instance has nothing above it, so nothing is ever redundant there.
func (s *Server) withoutRedundant(r *http.Request, integration *models.Integration,
	key string, value json.RawMessage, scopeType string, scopeID *uuid.UUID) json.RawMessage {

	if scopeType == store.ScopeInstance {
		return value
	}

	var spec *models.SettingSpec
	for i := range integration.Capabilities.Settings {
		if integration.Capabilities.Settings[i].Key == key {
			spec = &integration.Capabilities.Settings[i]
			break
		}
	}
	if spec == nil || spec.Type != "list" || spec.Items == nil || len(spec.Items.Identify) == 0 {
		return value
	}

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(value, &rows); err != nil {
		return value
	}

	// The levels above this one, and only those — the same resolution the reading side
	// stops one level up, so that "what the level above says" means the same thing on
	// both paths.
	//
	// Comparing a row with what this level said last time instead is not "redundant",
	// it is an erasure: a project that has Autodeploy off writes the whole row back
	// when somebody turns In use off, the Autodeploy field matches the project's own
	// previous answer, and the override is dropped as though it were a copy of the
	// instance's — so the switch it was protecting comes back on by itself.
	groupID, projectID := s.scopeParents(r.Context(), scopeType, scopeID)
	switch scopeType {
	case store.ScopeGroup:
		groupID, projectID = nil, nil
	case store.ScopeProject:
		projectID = nil
	}
	above, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID,
		groupID, projectID, integration.Capabilities.Settings)
	if err != nil {
		return value
	}

	var inherited []map[string]json.RawMessage
	if err := json.Unmarshal(above[key], &inherited); err != nil {
		return value
	}

	nameOf := func(row map[string]json.RawMessage) string {
		parts := make([]string, 0, len(spec.Items.Identify))
		for _, field := range spec.Items.Identify {
			parts = append(parts, strings.Trim(string(row[field]), `"`))
		}
		return strings.Join(parts, " ")
	}

	// Which row of the level above a row is talking about: by the identity the core gave
	// it first, because a name is a field a lower level may change and a row that has none
	// of its own here — a place it has named nothing for — is matched by nothing else.
	idOf := func(row map[string]json.RawMessage) string {
		return strings.Trim(string(row[rowIDField]), `"`)
	}

	trimmed := false
	kept := make([]map[string]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		var was map[string]json.RawMessage
		for _, one := range inherited {
			if id := idOf(row); id != "" && idOf(one) == id {
				was = one
				break
			}
			if nameOf(one) == nameOf(row) && nameOf(row) != "" {
				was = one
				break
			}
		}
		if was == nil {
			kept = append(kept, row)
			continue
		}
		out := map[string]json.RawMessage{}
		for field, raw := range row {
			// The field that says which row this is is never redundant: it is how the row
			// is matched to the one it overrides, and a row that lost it is a row nobody
			// can find again.
			for _, identity := range spec.Items.Identify {
				if field == identity {
					out[field] = raw
				}
			}
			if _, already := out[field]; already {
				continue
			}
			// A row the level above does not carry says something anyway: a switch the
			// module never declared a default for is on, because that is what nobody
			// saying means, and every other field is empty.
			previous, had := was[field]
			if !had {
				if defaultValue(field) != nil && sameJSON(raw, *defaultValue(field)) {
					trimmed = true
					continue
				}
				out[field] = raw
				continue
			}
			if sameJSON(raw, previous) {
				trimmed = true
				continue
			}
			out[field] = raw
		}
		// A row that kept something of its own keeps its identity as well, even though
		// the level above has one that is the same: the identity is how this row is found
		// again — by the next save, and by the merge that puts this row back on top of the
		// one it overrides. A row that overrides a name and arrives without its identity
		// is a second row of the same cluster.
		if len(out) > 0 {
			if id, ok := row[rowIDField]; ok {
				out[rowIDField] = id
			}
			kept = append(kept, out)
		}
	}
	if !trimmed {
		return value
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return value
	}
	return encoded
}

// defaultValue is what a row field means when nobody above has said anything about it.
//
// Only the core's own row fields have one, because only they are written by the core: a
// switch nobody mentions is on, which is what makes "I did not say no" mean "yes" all
// the way up the chain.
func defaultValue(field string) *json.RawMessage {
	switch field {
	case switchField, autoDeployField:
		on := json.RawMessage("true")
		return &on
	default:
		return nil
	}
}

func sameJSON(one, other json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(one), bytes.TrimSpace(other))
}

// mintTokenRequest asks for a token a user may present to a module.
type mintTokenRequest struct {
	// ProjectID scopes the token to one project, which is what a registry needs:
	// access is per project, not per instance.
	ProjectID  *uuid.UUID `json:"project_id"`
	Scopes     []string   `json:"scopes"`
	TTLSeconds int        `json:"ttl_seconds"`
}

// handleMintModuleToken issues a short-lived user token for a module.
//
// The core decides which scopes a caller gets; the module only checks that the
// presented token carries a scope it understands. That keeps the authorisation
// rules in one place instead of duplicated per module.
func (s *Server) handleMintModuleToken(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	kind := strings.TrimSpace(pathParam(r, "kind"))
	if kind == "" {
		s.writeError(w, r, errBadRequest("a module kind is required"))
		return
	}

	integration, err := s.store.Integrations().ByKind(r.Context(), kind)
	if errors.Is(err, store.ErrNotFound) {
		s.writeError(w, r, errNotFoundf("module %q is not installed", kind))
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !integration.Enabled {
		s.writeError(w, r, errForbiddenf("module %q is disabled", kind))
		return
	}

	var req mintTokenRequest
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	// A request without a body is valid: it means "whatever I am allowed".
	if req.Scopes == nil {
		req.Scopes = scopesForAccessLevel(user, integration)
	}

	var projectID *uuid.UUID
	if req.ProjectID != nil {
		project, err := s.store.Projects().ByID(r.Context(), *req.ProjectID)
		if err != nil {
			s.writeError(w, r, errNotFound("project does not exist"))
			return
		}
		allowed, err := s.store.Permissions().Can(r.Context(), user, project, store.ActionReadProject)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if !allowed {
			s.writeError(w, r, errNotFound("project does not exist"))
			return
		}
		projectID = req.ProjectID
	}

	scopes, err := filterScopes(integration, req.Scopes)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 || ttl > moduleTokenTTL {
		ttl = moduleTokenTTL
	}

	plaintext, _, err := s.mintModuleToken(r.Context(), user, integration, projectID, scopes, ttl)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"token":      plaintext,
		"scopes":     scopes,
		"expires_at": time.Now().Add(ttl).Format(time.RFC3339),
		"kind":       integration.Kind,
	})
}

// mintModuleToken creates a short-lived credential a module will accept.
//
// One place mints them, for every route that issues one, so a registry login and
// an explicit token request cannot drift apart in how long a credential lasts or
// what it carries.
// It takes a context rather than a request because not everything that needs a
// credential has one: a deployment runs in the background, after the request that
// asked for the pipeline has been answered, and it still needs to pull.
func (s *Server) mintModuleToken(ctx context.Context, user *models.User, integration *models.Integration,
	projectID *uuid.UUID, scopes []string, ttl time.Duration) (string, *models.IntegrationToken, error) {

	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		return "", nil, err
	}

	token := &models.IntegrationToken{
		IntegrationID: integration.ID,
		UserID:        &user.ID,
		ProjectID:     projectID,
		Scopes:        scopes,
		ExpiresAt:     time.Now().Add(ttl),
	}
	if err := s.store.IntegrationTokens().Create(ctx, token, hash); err != nil {
		return "", nil, err
	}
	return plaintext, token, nil
}

// handleIntrospect answers a module's question "who is presenting this token?".
//
// This is the single point where the core's user and permission model is applied
// on behalf of every module. Modules hold no user database of their own: an
// account deleted here stops working in every module at the next request.
func (s *Server) handleIntrospect(w http.ResponseWriter, r *http.Request) {
	presented, err := s.tokenFromHeader(r)
	if err != nil {
		// An unknown token is an inactive token, not an error: the module decides
		// what to do with it, and 401 would blur the two cases.
		s.writeJSON(w, r, http.StatusOK, models.Introspection{Active: false, Scopes: []string{}})
		return
	}

	result := s.introspect(r, presented)
	s.writeJSON(w, r, http.StatusOK, result)
}

// introspect resolves a credential a module presented.
//
// One function, because two callers asking the same question differently is how a
// registry ends up allowing something the rest of the system refuses. An answer
// is always 200 with a body: "who is this" has several correct answers, and the
// module has to be able to tell them apart.
func (s *Server) introspect(r *http.Request, presented []byte) models.Introspection {
	inactive := models.Introspection{Active: false, Scopes: []string{}}

	token, err := s.store.IntegrationTokens().Introspect(r.Context(), presented)
	if err != nil {
		return inactive
	}
	if time.Now().After(token.ExpiresAt) {
		return inactive
	}
	if !token.ModuleEnabled {
		// Forbidding a module has to take effect at once, without asking the module
		// to drop anything: the core stops saying who the caller is, and a module
		// that cannot identify anyone has nothing to let through. That is what makes
		// the button work on modules nobody has updated.
		s.log.Info("module token presented by a forbidden module",
			"module_id", token.IntegrationID)
		return inactive
	}

	result := models.Introspection{
		Active:    true,
		UserID:    token.UserID,
		ProjectID: token.ProjectID,
		Scopes:    token.Scopes,
	}

	if token.UserID != nil {
		user, err := s.store.Users().ByID(r.Context(), *token.UserID)
		if err != nil {
			// The account is gone. Revoking by disappearance is the whole point of
			// the core owning identity, so the token stops working immediately.
			return inactive
		}
		result.Username = user.Username

		if token.ProjectID != nil {
			if level, err := s.store.Permissions().AccessLevel(r.Context(), user.ID, *token.ProjectID); err == nil {
				result.AccessLevel = level
				result.AccessName = models.AccessLevelName(level)
			}
		}
	}

	if err := s.store.IntegrationTokens().TouchUsed(r.Context(), token.ID); err != nil {
		s.log.Debug("record module token usage", "token_id", token.ID, "error", err)
	}
	return result
}

// --- helpers ------------------------------------------------------------

// integrationView renders a module for the API, with the effective settings for
// the requested scope.
func (s *Server) integrationView(r *http.Request, integration *models.Integration, groupID, projectID *uuid.UUID) map[string]any {
	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, groupID, projectID, integration.Capabilities.Settings)
	if err != nil {
		s.log.Debug("read module settings", "module", integration.Name, "error", err)
		settings = map[string]json.RawMessage{}
	}

	// Secret settings are never returned; the UI shows only that one is set. A list
	// setting is described as well as flagged, because a secret field inside one of
	// its entries has to be masked entry by entry.
	declared := map[string]bool{}
	specs := map[string]models.SettingSpec{}
	for _, spec := range integration.Capabilities.Settings {
		declared[spec.Key] = spec.Secret
		specs[spec.Key] = spec
	}

	view := map[string]any{
		"id":             integration.ID,
		"kind":           integration.Kind,
		"name":           integration.Name,
		"endpoint":       integration.Endpoint,
		"module_version": integration.ModuleVersion,
		"manifest":       integration.Capabilities,
		"status":         integration.Status,
		"enabled":        integration.Enabled,
		"last_seen_at":   integration.LastSeenAt,
		"registered_at":  integration.RegisteredAt,
		"settings": redactSettings(settings, declared, func(key string) (models.SettingSpec, bool) {
			spec, ok := specs[key]
			return spec, ok
		}),
	}

	// Scopes declared but unused are worth showing: a module offering
	// "registry:delete" that nothing mints is a hint that the core is missing a
	// rule.
	view["scopes"] = integration.Capabilities.Scopes

	// Where the module is reachable from the outside, worked out here rather than
	// in the interface: the answer is needed by the push instructions, by the
	// proxy configuration and by an operator reading this page, and three places
	// answering it separately is how they end up disagreeing.
	// What the module last said about itself, when it has said anything. A module
	// that reports nothing is shown as reporting nothing rather than as having
	// reported zeroes.
	if stats, err := s.store.ModuleStats().Latest(r.Context(), integration.ID); err == nil {
		view["stats"] = stats
	} else if !errors.Is(err, store.ErrNotFound) {
		s.log.Debug("read module stats", "module", integration.Name, "error", err)
	}

	if address, published := modulehost.BaseURL(s.cfg.PublicHost, integration.Capabilities.Routing); published {
		view["public_url"] = address
		view["dedicated_host"] = modulehost.IsDedicatedHost(s.cfg.PublicHost, integration.Capabilities.Routing)
	} else {
		view["public_url"] = nil
	}
	return view
}

// handleModuleRoutes reports where every module is published.
//
// The answer is JSON as well as nginx configuration because the question has two
// audiences: an operator who wants to know which names to point DNS at, and a
// deployment that wants the proxy configuration itself.
func (s *Server) handleModuleRoutes(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	modules, err := s.store.Integrations().List(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	routes := modulehost.Describe(s.cfg.PublicHost, modules)

	if r.URL.Query().Get("nginx") != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, modulehost.NginxServerBlocks(routes, s.cfg.TLSServerName))
		return
	}

	views := make([]map[string]any, 0, len(routes))
	for _, route := range routes {
		views = append(views, map[string]any{
			"module_id": route.ModuleID,
			"kind":      route.Kind,
			"name":      route.Name,
			"upstream":  route.Upstream,
			"domains":   route.Domains,
			"path":      route.Path,
			"websocket": route.Websocket,
			"url":       route.URL(),
		})
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"public_host": s.cfg.PublicHost,
		"routes":      views,
	})
}

// redactSettings is the stored settings as they may be read out.
//
// A secret field is replaced rather than hidden, so the page can say "this is set"
// without saying what. For a list setting that means descending into the entries: a
// password inside one entry is a password, and a whole blob that is either returned or
// not has no way to return the address beside it without the password.
func redactSettings(settings map[string]json.RawMessage, secret map[string]bool,
	describe func(string) (models.SettingSpec, bool)) map[string]any {

	out := map[string]any{}
	for key, raw := range settings {
		if secret[key] {
			out[key] = "********"
			continue
		}
		if spec, ok := describe(key); ok && spec.Items != nil {
			out[key] = redactList(spec, raw)
			continue
		}
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			out[key] = value
			continue
		}
		out[key] = string(raw)
	}
	return out
}

// redactList masks the secret fields of every entry of a list setting.
//
// Called on the way out only, and only for a setting whose description says which
// fields are secret: the core is reading a module's own description of its own value,
// which is the same thing it does for a plain secret setting.
func redactList(spec models.SettingSpec, raw json.RawMessage) any {
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		var value any
		if err := json.Unmarshal(raw, &value); err == nil {
			return value
		}
		return string(raw)
	}

	if spec.Items == nil {
		return entries
	}
	secret := map[string]bool{}
	for _, field := range spec.Items.Fields {
		if field.Secret {
			secret[field.Key] = true
		}
	}

	for _, entry := range entries {
		for key := range secret {
			if _, set := entry[key]; set {
				entry[key] = "********"
			}
		}
	}
	return entries
}

// settingSpecOf finds what a module said about one of its settings.
func settingSpecOf(integration *models.Integration, key string) (models.SettingSpec, bool) {
	for _, spec := range integration.Capabilities.Settings {
		if spec.Key == key {
			return spec, true
		}
	}
	return models.SettingSpec{}, false
}

func moduleDeclaresSetting(integration *models.Integration, key string) bool {
	for _, spec := range integration.Capabilities.Settings {
		if spec.Key == key {
			return true
		}
	}
	return false
}

// scopesForAccessLevel derives the scopes a user is entitled to from the level
// they hold on the module's project.
//
// The table is deliberately coarse: a module that needs finer rules declares
// nothing extra, and the core learns to ask the module before minting. Guessing
// finer permissions here would be guesswork dressed as policy.
func scopesForAccessLevel(user *models.User, integration *models.Integration) []string {
	read := []string{models.ScopeRegistryPull, models.ScopeCacheRead}
	write := []string{models.ScopeRegistryPush, models.ScopeCacheWrite}
	admin := []string{models.ScopeRegistryDelete, models.ScopeBuilderBuild}

	if user.IsAdmin {
		return unionScopes(read, write, admin)
	}
	// Without a project the caller is at least a developer of the instance; the
	// project-scoped check happens when a project is supplied.
	return unionScopes(read, write)
}

func unionScopes(groups ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, group := range groups {
		for _, scope := range group {
			if seen[scope] {
				continue
			}
			seen[scope] = true
			out = append(out, scope)
		}
	}
	return out
}

// filterScopes drops scopes the module did not declare, so a typo or a stale
// client cannot hand out a permission the module would not understand.
func filterScopes(integration *models.Integration, requested []string) ([]string, error) {
	known := map[string]bool{}
	for _, scope := range integration.Capabilities.Scopes {
		known[scope] = true
	}

	out := []string{}
	seen := map[string]bool{}
	for _, scope := range requested {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !known[scope] {
			continue
		}
		if seen[scope] {
			continue
		}
		seen[scope] = true
		out = append(out, scope)
	}

	if len(out) == 0 {
		return nil, errBadRequestf(
			"module %s does not accept any of the requested scopes", integration.Kind)
	}
	return out, nil
}

func validateModuleKind(kind string) error {
	if kind == "" {
		return errBadRequest("a module kind is required, for example registry:docker")
	}
	namespace, name, ok := strings.Cut(kind, ":")
	if !ok || namespace == "" || name == "" {
		return errBadRequest("a module kind must look like <namespace>:<name>")
	}
	for _, part := range []string{namespace, name} {
		for _, r := range part {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			default:
				return errBadRequest("a module kind may contain lowercase letters, digits, '-' and '_'")
			}
		}
	}
	return nil
}

// validateModuleEndpoint keeps a module from registering an address that would
// make the core call something unexpected, such as a file path or a unix socket.
func validateModuleEndpoint(endpoint string) error {
	if endpoint == "" {
		return errBadRequest("a module endpoint is required")
	}
	if len(endpoint) > 512 {
		return errBadRequest("the module endpoint is too long")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return errBadRequest("the module endpoint must be an http:// or https:// URL")
	}
	if strings.ContainsAny(endpoint, " \t\n") {
		return errBadRequest("the module endpoint contains whitespace")
	}
	return nil
}

// tokenFromHeader reads a bearer token presented by a module.
func (s *Server) tokenFromHeader(r *http.Request) ([]byte, error) {
	return hashBearer(r.Header.Get("Authorization"))
}

// tokenFromHeaderValue hashes a credential a module found somewhere other than
// its own request — a registry client presenting a token through the module, for
// instance. The module passes the token as it received it; deciding what it means
// happens here.
func (s *Server) tokenFromHeaderValue(token string) ([]byte, error) {
	return hashBearer("Bearer " + strings.TrimSpace(token))
}

func hashBearer(header string) ([]byte, error) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return nil, errors.New("no bearer token")
	}
	return auth.HashToken(strings.TrimSpace(header[len(prefix):])), nil
}

// integrationFromRequest authenticates the calling module by its own token.
func (s *Server) integrationFromRequest(r *http.Request) (*models.Integration, error) {
	hash, err := s.tokenFromHeader(r)
	if err != nil {
		return nil, errUnauthorized("module authentication is required")
	}
	return s.store.Integrations().ByTokenHash(r.Context(), hash)
}

func (s *Server) moduleFromPath(r *http.Request) (*models.Integration, error) {
	id, err := uuid.Parse(pathParam(r, "integrationID"))
	if err != nil {
		return nil, errBadRequest("invalid module id")
	}
	return s.store.Integrations().ByID(r.Context(), id)
}

// settingScope is whose settings are being written.
//
// The ids come from the query rather than only from the path, because the same
// operation is done from three places — the instance, a group and a project — and
// giving each its own route to one handler would be three spellings of one thing.
// A path id still wins when there is one, so a caller that already has it is not
// surprised by an id in the query.
// allowSettingScope decides who may write settings at this level.
//
// The instance is the administrator's alone. Below it, somebody who can manage the
// thing the settings belong to may write them: a project maintainer choosing which
// channel their builds are announced in is not a decision that needs an
// administrator, and making it one would leave projects on the wrong channel until
// somebody remembered to ask.
func (s *Server) allowSettingScope(r *http.Request, scopeType string, scopeID *uuid.UUID) error {
	user := userFrom(r.Context())
	if user.IsAdmin {
		return nil
	}

	switch scopeType {
	case store.ScopeInstance:
		return errForbidden("administrator rights are required")

	case store.ScopeGroup:
		if scopeID == nil {
			return errForbidden("a group id is required for this scope")
		}
		allowed, err := s.store.Permissions().CanGroup(r.Context(), user, *scopeID, store.ActionManageGroup)
		if err != nil {
			return err
		}
		if !allowed {
			return errForbidden("managing this group is required")
		}
		return nil

	case store.ScopeProject:
		if scopeID == nil {
			return errForbidden("a project id is required for this scope")
		}
		project, err := s.store.Projects().ByID(r.Context(), *scopeID)
		if err != nil {
			return errNotFound("project does not exist")
		}
		allowed, err := s.store.Permissions().Can(r.Context(), user, project, store.ActionManageProject)
		if err != nil {
			return err
		}
		if !allowed {
			return errForbidden("managing this project is required")
		}
		return nil

	default:
		return errBadRequest("scope must be instance, group or project")
	}
}

func (s *Server) settingScope(r *http.Request) (string, *uuid.UUID, error) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = store.ScopeInstance
	}

	ref := func(name string) string {
		if raw := strings.TrimSpace(pathParam(r, name)); raw != "" {
			return raw
		}
		return strings.TrimSpace(r.URL.Query().Get(name))
	}

	switch scope {
	case store.ScopeInstance:
		return scope, nil, nil
	case store.ScopeGroup:
		id, err := uuid.Parse(ref("groupID"))
		if err != nil {
			return "", nil, errBadRequest("a group id is required for this scope")
		}
		if _, err := s.store.Groups().ByID(r.Context(), id); err != nil {
			return "", nil, errNotFound("group does not exist")
		}
		return scope, &id, nil
	case store.ScopeProject:
		id, err := uuid.Parse(ref("projectID"))
		if err != nil {
			return "", nil, errBadRequest("a project id is required for this scope")
		}
		if _, err := s.store.Projects().ByID(r.Context(), id); err != nil {
			return "", nil, errNotFound("project does not exist")
		}
		return scope, &id, nil
	default:
		return "", nil, errBadRequest("scope must be instance, group or project")
	}
}
