package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/modulehost"
	"github.com/ewolf/dogit/internal/store"
)

// registryKind is the module kind that holds images.
//
// The core names it in one place so that "is there a registry on this instance" is a
// question with one answer rather than a string repeated wherever a project page
// wants to show images.
const registryKind = "registry:docker"

// packageTokenTTL is how long a credential for reading a project's images lasts.
//
// Short, because it is handed to a browser: it exists for the few requests a page
// makes, and a credential that outlived the tab would outlive the rights behind
// it.
const packageTokenTTL = 10 * time.Minute

// handleProjectImages hands the browser what it needs to see a project's images.
//
// The images themselves live in a module, so the page cannot be served from the
// core's database — the core stores hashes, not layers. What the core does is the
// part that must not be delegated: it decides whether this caller may look, and it
// mints a short credential the module will recognise. The page then asks the
// module at the address it published, which is the same address anybody pushing an
// image uses.
func (s *Server) handleProjectImages(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	project, _, err := s.projectWithAccess(r, store.ActionReadProject)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	answer := map[string]any{
		"project":    project.Path,
		"registry":   nil,
		"reason":     "no_registry_module",
		"can_push":   false,
		"can_delete": false,
	}

	integration, err := s.store.Integrations().ByKind(r.Context(), registryKind)
	if errors.Is(err, store.ErrNotFound) {
		s.writeJSON(w, r, http.StatusOK, answer)
		return
	}
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !integration.Enabled {
		answer["reason"] = "registry_forbidden"
		s.writeJSON(w, r, http.StatusOK, answer)
		return
	}

	pull, err := s.store.Permissions().Can(r.Context(), user, project, store.ActionRegistryPull)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if !pull {
		// Not told the project is missing: it may well exist, this caller just has
		// nothing on it, and the project page has already decided what to show.
		s.writeError(w, r, errForbidden("you do not have rights on this project's images"))
		return
	}

	// The operator may name the address themselves, which is the only thing that can
	// be right when the module answers somewhere the instance's name does not reach.
	// An unset setting means the module's own declared default, which is what a
	// default is: the module said where it publishes itself when it started.
	override := ""
	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, nil, nil, integration.Capabilities.Settings)
	if err == nil {
		if raw, ok := settings["public_address"]; ok {
			_ = json.Unmarshal(raw, &override)
		}
	}
	if strings.TrimSpace(override) == "" {
		if spec, found := settingSpecOf(integration, "public_address"); found {
			if value, ok := spec.Default.(string); ok {
				override = value
			}
		}
	}

	address, published := modulehost.BaseURLAs(s.cfg.PublicHost, integration.Capabilities.Routing, override)
	if !published {
		// A registry nobody can reach is a misconfiguration, not a state to hide: the
		// module registered without saying where it is.
		s.log.Warn("the registry module published no address",
			"module_id", integration.ID, "endpoint", integration.Endpoint)
		answer["reason"] = "registry_not_published"
		s.writeJSON(w, r, http.StatusOK, answer)
		return
	}

	scopes := []string{models.ScopeRegistryPull}
	canDelete := false
	if allowed, err := s.store.Permissions().Can(r.Context(), user, project, store.ActionRegistryDelete); err == nil && allowed {
		scopes = append(scopes, models.ScopeRegistryDelete)
		canDelete = true
	}
	canPush := false
	if allowed, err := s.store.Permissions().Can(r.Context(), user, project, store.ActionRegistryPush); err == nil && allowed {
		canPush = true
	}

	token, err := s.issuePackageToken(r, user, integration, &project.ID, scopes)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	answer["registry"] = map[string]any{
		"url":            address,
		"dedicated_host": modulehost.IsDedicatedHost(s.cfg.PublicHost, integration.Capabilities.Routing),
		"kind":           integration.Kind,
	}
	answer["token"] = token
	answer["expires_in"] = int(packageTokenTTL.Seconds())
	answer["reason"] = ""
	answer["can_push"] = canPush
	answer["can_delete"] = canDelete

	s.writeJSON(w, r, http.StatusOK, answer)
}

// issuePackageToken mints the credential the page presents to the module.
//
// The core holds only the hash of every other credential it has, so it cannot call
// a module and be believed; handing the browser a token inverts that honestly. The
// module verifies it by asking the core who is presenting it, which is the same
// question every module asks about every request.
// A nil projectID mints a token for the whole instance, which only an
// administrator is given: there is no project to narrow it to, and narrowing is
// what makes the per-project token safe to hand a browser at all.
func (s *Server) issuePackageToken(r *http.Request, user *models.User, integration *models.Integration,
	projectID *uuid.UUID, scopes []string) (string, error) {

	scopes, err := filterScopes(integration, scopes)
	if err != nil {
		return "", err
	}

	token, _, err := s.mintModuleToken(r.Context(), user, integration, projectID, scopes, packageTokenTTL)
	if err != nil {
		return "", err
	}
	return token, nil
}
