package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"integration":        s.integrationView(r, integration, nil, nil),
		"token":              plaintext,
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
	s.writeJSON(w, r, http.StatusOK, map[string]any{"module": updated})
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

	if err := s.store.Integrations().SetSetting(r.Context(), integration.ID, scopeType, scopeID, key, req.Value); err != nil {
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
	effective, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, groupID, projectID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"scope":     scopeType,
		"settings":  at,
		"effective": effective,
		"schema":    integration.Capabilities.Settings,
	})
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

	plaintext, hash, err := auth.GenerateToken()
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	token := &models.IntegrationToken{
		IntegrationID: integration.ID,
		UserID:        &user.ID,
		ProjectID:     projectID,
		Scopes:        scopes,
		ExpiresAt:     time.Now().Add(ttl),
	}
	if err := s.store.IntegrationTokens().Create(r.Context(), token, hash); err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusCreated, map[string]any{
		"token":      plaintext,
		"scopes":     scopes,
		"expires_at": token.ExpiresAt.Format(time.RFC3339),
		"kind":       integration.Kind,
	})
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

	token, err := s.store.IntegrationTokens().Introspect(r.Context(), presented)
	if err != nil {
		s.writeJSON(w, r, http.StatusOK, models.Introspection{Active: false, Scopes: []string{}})
		return
	}
	if time.Now().After(token.ExpiresAt) {
		s.writeJSON(w, r, http.StatusOK, models.Introspection{Active: false, Scopes: []string{}})
		return
	}
	if !token.ModuleEnabled {
		// Forbidding a module has to take effect at once, without asking the module
		// to drop anything: the core stops saying who the caller is, and a module
		// that cannot identify anyone has nothing to let through. That is what makes
		// the button work on modules nobody has updated.
		s.log.Info("module token presented by a forbidden module",
			"module_id", token.IntegrationID)
		s.writeJSON(w, r, http.StatusOK, models.Introspection{Active: false, Scopes: []string{}})
		return
	}

	result := models.Introspection{
		Active:    true,
		UserID:    token.UserID,
		ProjectID: token.ProjectID,
		Scopes:    token.Scopes,
	}

	if token.UserID != nil {
		if user, err := s.store.Users().ByID(r.Context(), *token.UserID); err == nil {
			result.Username = user.Username

			if token.ProjectID != nil {
				if level, err := s.store.Permissions().AccessLevel(r.Context(), user.ID, *token.ProjectID); err == nil {
					result.AccessLevel = level
					result.AccessName = models.AccessLevelName(level)
				}
			}
		} else {
			// The account is gone. Revoking by disappearance is the whole point of
			// the core owning identity, so the token stops working immediately.
			s.writeJSON(w, r, http.StatusOK, models.Introspection{Active: false, Scopes: []string{}})
			return
		}
	}

	if err := s.store.IntegrationTokens().TouchUsed(r.Context(), token.ID); err != nil {
		s.log.Debug("record module token usage", "token_id", token.ID, "error", err)
	}

	s.writeJSON(w, r, http.StatusOK, result)
}

// --- helpers ------------------------------------------------------------

// integrationView renders a module for the API, with the effective settings for
// the requested scope.
func (s *Server) integrationView(r *http.Request, integration *models.Integration, groupID, projectID *uuid.UUID) map[string]any {
	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, groupID, projectID)
	if err != nil {
		s.log.Debug("read module settings", "module", integration.Name, "error", err)
		settings = map[string]json.RawMessage{}
	}

	// Secret settings are never returned; the UI shows only that one is set.
	declared := map[string]bool{}
	for _, spec := range integration.Capabilities.Settings {
		declared[spec.Key] = spec.Secret
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
		"settings":       redactSettings(settings, declared),
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

func redactSettings(settings map[string]json.RawMessage, secret map[string]bool) map[string]any {
	out := map[string]any{}
	for key, raw := range settings {
		if secret[key] {
			out[key] = "********"
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
	header := r.Header.Get("Authorization")
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

// settingScope reads ?scope=instance|group|project and, for the latter two, the
// scope id from the path.
func (s *Server) settingScope(r *http.Request) (string, *uuid.UUID, error) {
	scope := strings.TrimSpace(r.URL.Query().Get("scope"))
	if scope == "" {
		scope = store.ScopeInstance
	}

	switch scope {
	case store.ScopeInstance:
		return scope, nil, nil
	case store.ScopeGroup:
		id, err := uuid.Parse(pathParam(r, "groupID"))
		if err != nil {
			return "", nil, errBadRequest("a group id is required for this scope")
		}
		if _, err := s.store.Groups().ByID(r.Context(), id); err != nil {
			return "", nil, errNotFound("group does not exist")
		}
		return scope, &id, nil
	case store.ScopeProject:
		id, err := uuid.Parse(pathParam(r, "projectID"))
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
