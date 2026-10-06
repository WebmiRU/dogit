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

// catalogTokenTTL is how long an administrator's credential for the registry's
// catalogue lasts.
//
// Short for the same reason every credential handed to a browser is: it exists for
// the requests this page makes, and one that outlived the tab would outlive the
// rights behind it.
const catalogTokenTTL = 10 * time.Minute

// handleRegistryCatalog hands an administrator what is needed to see every image on
// the instance.
//
// The images themselves live in a module, so this cannot be answered from the
// core's database: the core keeps digests it passed on and knows nothing about
// layers, tags or what anything weighs. What the core does is the part that must
// not be delegated — deciding that this caller may see the whole instance rather
// than one project of it — and handing the browser a short credential the module
// will recognise. The page then asks the module directly, at the address it
// published.
//
// It also says which group each project belongs to, because the catalogue is
// grouped by group and the core is the only one that knows. It does not say what
// any of them contain.
func (s *Server) handleRegistryCatalog(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	if !user.IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	answer := map[string]any{
		"reason":  "",
		"groups":  []any{},
		"address": "",
	}

	integration, err := s.store.Integrations().ByKind(r.Context(), registryKind)
	if errors.Is(err, store.ErrNotFound) {
		answer["reason"] = "no_registry_module"
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

	address, err := s.registryAddress(r, integration)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if address == "" {
		answer["reason"] = "registry_not_published"
		s.writeJSON(w, r, http.StatusOK, answer)
		return
	}

	// One token for the whole instance rather than one per project: this page asks
	// the registry once, and a token per project would mean one credential for every
	// project an installation has, all of them live in a tab at the same time.
	// No project: this token covers the whole instance, and it is only handed to an
	// administrator. The registry's own guard still decides per image name — this
	// credential says "an administrator is asking", not "anything goes".
	token, err := s.issuePackageToken(r, user, integration, nil,
		[]string{models.ScopeRegistryPull, models.ScopeRegistryDelete})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// Which projects exist, and which group each is in. Names are resolved here
	// rather than in the page because a group is a thing the core owns, and a page
	// that guessed at one would be guessing at somebody else's structure.
	projects, err := s.store.Projects().ListAll(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	groupNames := map[uuid.UUID]string{}
	listed := make([]map[string]any, 0, len(projects))
	for _, project := range projects {
		if project.GroupID != nil {
			if _, known := groupNames[*project.GroupID]; !known {
				group, err := s.store.Groups().ByID(r.Context(), *project.GroupID)
				if err != nil {
					// A project whose group cannot be read is listed under its own
					// path: missing is not an excuse for leaving it out.
					groupNames[*project.GroupID] = project.Path
				} else {
					groupNames[*project.GroupID] = group.Name
				}
			}
		}
		listed = append(listed, map[string]any{
			"path":  project.Path,
			"group": project.GroupID,
		})
	}

	answer["address"] = address
	answer["kind"] = integration.Kind
	answer["token"] = token
	answer["expires_in"] = int(catalogTokenTTL.Seconds())
	answer["projects"] = listed
	answer["group_names"] = groupNames
	s.writeJSON(w, r, http.StatusOK, answer)
}

// registryAddress is where a browser can reach the registry module.
//
// The operator may name the address themselves, which is the only thing that can
// be right when the module answers somewhere the instance's own name does not
// reach; an unset setting means the module's own declared default, which is what a
// default is.
func (s *Server) registryAddress(r *http.Request, integration *models.Integration) (string, error) {
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
		s.log.Warn("the registry module published no address",
			"module_id", integration.ID, "endpoint", integration.Endpoint)
		return "", nil
	}
	return address, nil
}
