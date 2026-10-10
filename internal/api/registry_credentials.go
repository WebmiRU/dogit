package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// writtenRegistryCredentials is what a build needs to push to a registry an
// administrator wrote down.
//
// The shape a runner receives is the same one it receives for a module — a url, an image
// and a credential — because the runner has no reason to know which it is talking to and
// a difference here would show up as a runner that works with this instance's own registry
// and not with this one. What differs is where the credential comes from and who chooses
// it, not what arrives.
//
// The naming is the registry's own convention: the host, then the group and the project.
// A module asks its naming rule of the module, because the module is the thing that
// stores the image; an address nobody here stores images in gets the name every registry
// client already understands.
func (s *Server) writtenRegistryCredentials(ctx context.Context, project *models.Project,
	job *store.Job, reg *store.DockerRegistry) (map[string]any, error) {

	credential, err := s.store.RegistryCredentials().Resolve(ctx, reg, project.GroupID, &project.ID)
	if err != nil {
		return nil, err
	}

	login, password, err := s.credentialAccount(ctx, credential)
	if err != nil {
		return nil, err
	}
	if login == "" {
		return nil, errBadRequestf(
			"the registry %q has no account to push with: it names the source %q and nothing "+
				"has supplied a login. Set one on the registry, or on the group or project that "+
				"pushes to it", reg.URL, credential.Source)
	}

	address := strings.TrimRight(reg.URL, "/")
	return map[string]any{
		"url":      address,
		"image":    registryHost(address) + "/" + imageNameFor("{{group}}/{{project}}", project.Path, jobRef(job)),
		"login":    login,
		"password": password,
		// A written-down account is not scoped to a project and is not minted here, so
		// there is no expiry to report. The runner is told a long one rather than none:
		// a build that outlives it is a build that has stopped working, and being told
		// so by the answer would be a smaller surprise than a push refused on a socket.
		"expires_in": int((30 * 24 * time.Hour).Seconds()),
	}, nil
}

// credentialAccount is the login and password the resolved source names.
//
// "static" is the pair written down, and nothing else is asked of it.
//
// "user" names a dogit account, and the pair is still the one written down beside it. That
// sounds like a distinction without a difference, and today it nearly is one — but it is
// not the same claim. A static row is a pair of strings that happen to work at a registry.
// A user row is a named account of this instance, checked to exist before anything is
// sent to the registry, and the name of the account is what an audit reads afterwards.
//
// It is also where a shared directory fits. Where dogit and the registry are two doors
// onto the same accounts — one LDAP, one service — the password is in the directory and
// in neither of them, and the answer is to ask the directory rather than to store it
// twice. That is not built yet, and what it needs is a way to ask: this instance holds
// password hashes and cannot hand one to a registry. Until that exists, the written-down
// password is used and the account is only named, checked and attributed — which is the
// part of the promise that can be kept without storing anything reversible.
func (s *Server) credentialAccount(ctx context.Context, resolved store.ResolvedCredential) (string, string, error) {
	if resolved.Source != store.CredentialSourceUser {
		return resolved.Login, resolved.Password, nil
	}
	if resolved.Login == "" {
		return "", "", errBadRequest(
			"this registry pushes with a dogit account, but no account was named. Set a login, " +
				"or change the source to a static one")
	}
	// Checked rather than assumed: a registry refusing a login it has never heard of
	// answers with a 401 that names neither the typo nor the registry, and the runner
	// reports a push failure for what is a spelling mistake written here.
	if _, err := s.store.Users().ByUsername(ctx, resolved.Login); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", "", errBadRequestf(
				"this registry pushes with the dogit account %q, and there is no such account here",
				resolved.Login)
		}
		return "", "", err
	}
	if resolved.Password == "" {
		return "", "", errBadRequestf(
			"this registry pushes with the dogit account %q, but no password has been written "+
				"for it. Set one here: this instance keeps password hashes and cannot hand one "+
				"to a registry", resolved.Login)
	}
	return resolved.Login, resolved.Password, nil
}

// writtenRegistryNames names the registries in an error that is about choosing between
// them. Names, not addresses: an administrator recognises a name they wrote down, and an
// address is one more thing to match up while reading a message about a build.
func (s *Server) writtenRegistryNames(registries []*store.DockerRegistry) string {
	names := make([]string, 0, len(registries))
	for _, reg := range registries {
		if reg.Name != "" {
			names = append(names, reg.Name)
			continue
		}
		names = append(names, reg.URL)
	}
	return strings.Join(names, ", ")
}

// registryCredentialInput is the credential part of a request to write one down.
type registryCredentialInput struct {
	CredentialSource *string `json:"credential_source"`
	Login            *string `json:"login"`
	Password         *string `json:"password"`
}

// handleRegistryCredentials writes what one scope says about who pushes to a registry.
//
// Scoped the same way module settings are, and for the same reason: an installation is
// rarely one arrangement, and the arrangement a group needs is not the arrangement the
// whole instance has. Instance, group, project, in that order, and each scope changes
// only what it names — a project that sets a password inherits the login, and one that
// sets a login inherits the password.
func (s *Server) handleRegistryCredentials(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	id, err := uuid.Parse(pathParam(r, "registryID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a registry id is required"))
		return
	}
	reg, err := s.store.DockerRegistries().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var body registryCredentialInput
	if err := decodeJSON(r, &body); err != nil {
		s.writeError(w, r, err)
		return
	}

	// Passed on as pointers because every one of these is optional and the difference
	// between the two empties is the whole of the form: not mentioned means this scope
	// says nothing about it, and an empty string means this scope inherits it. Handing
	// the store three plain strings would lose that, and the store would then have to
	// choose for everybody — it chose to replace the row, and setting a login quietly
	// threw away the password beside it.
	change := store.RegistryCredentialChange{
		RegistryID: reg.ID,
		ScopeType:  scopeType,
		ScopeID:    scopeID,
	}
	if body.CredentialSource != nil {
		source := strings.TrimSpace(*body.CredentialSource)
		if source != "" && source != store.CredentialSourceStatic && source != store.CredentialSourceUser {
			s.writeError(w, r, errBadRequestf(
				"a credential source is either %q or %q",
				store.CredentialSourceStatic, store.CredentialSourceUser))
			return
		}
		change.CredentialSource = &source
	}
	if body.Login != nil {
		change.Login = body.Login
	}
	if body.Password != nil {
		change.Password = body.Password
	}

	if err := s.store.RegistryCredentials().Set(r.Context(), change); err != nil {
		s.writeError(w, r, err)
		return
	}

	// The answer is what a build would now use, rather than what was written: a scope
	// that set a password alone still pushes with the login it inherited, and saying so
	// here is the difference between a form that saved and a form that was understood.
	resolved, err := s.store.RegistryCredentials().Resolve(r.Context(), reg, groupOf(scopeType, scopeID), projectOf(scopeType, scopeID))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"scope_type": scopeType,
		"scope_id":   scopeID,
		"resolved": map[string]any{
			"credential_source": resolved.Source,
			"login":             resolved.Login,
			"has_password":      resolved.Password != "",
		},
	})
}

// registryCredentialView is what one scope says, and what it comes to.
//
// Two answers because a form needs both. `written` is what this scope has set down, and it
// is empty where this scope said nothing — an empty login here means "inherited", not
// "cleared". `resolved` is what a build for this scope would actually push with, which is
// the whole chain applied. A form shown only the second would offer to save an inherited
// login as though the group had chosen it, and saving would turn an inheritance into a
// copy that no longer follows the thing it was inheriting from.
type registryCredentialView struct {
	ScopeType        string     `json:"scope_type"`
	ScopeID          *uuid.UUID `json:"scope_id,omitempty"`
	Written          bool       `json:"written"`
	CredentialSource string     `json:"credential_source"`
	Login            string     `json:"login"`
	HasPassword      bool       `json:"has_password"`
	Resolved         struct {
		CredentialSource string `json:"credential_source"`
		Login            string `json:"login"`
		HasPassword      bool   `json:"has_password"`
	} `json:"resolved"`
	// Set when this registry cannot be pushed to at all — marked read-only — so the page
	// can say so where it is being edited rather than only at the moment a build fails.
	ResolvedError string `json:"resolved_error,omitempty"`
}

// handleRegistryCredentialsRead is what one scope has written down about one registry, and
// what a build for that scope would use.
//
// The read side of the same route the write side uses, and answering about one scope per
// request rather than all three at once: a page asking for every group on the instance
// would be a page asking for every credential on it, which is not a page anybody opened
// a registry to see.
func (s *Server) handleRegistryCredentialsRead(w http.ResponseWriter, r *http.Request) {
	if !userFrom(r.Context()).IsAdmin {
		s.writeError(w, r, errForbidden("administrator rights are required"))
		return
	}

	id, err := uuid.Parse(pathParam(r, "registryID"))
	if err != nil {
		s.writeError(w, r, errBadRequest("a registry id is required"))
		return
	}
	reg, err := s.store.DockerRegistries().ByID(r.Context(), id)
	if err != nil {
		s.writeError(w, r, dockerRegistryError(err))
		return
	}

	scopeType, scopeID, err := s.settingScope(r)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	view := registryCredentialView{ScopeType: scopeType, ScopeID: scopeID}

	row, written, err := s.store.RegistryCredentials().Scoped(r.Context(), reg.ID, scopeType, scopeID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if written {
		view.Written = true
		view.CredentialSource = strings.TrimSpace(row.CredentialSource)
		view.Login = row.Login
		view.HasPassword = row.Password != ""
	}

	resolved, err := s.store.RegistryCredentials().Resolve(r.Context(), reg,
		groupOf(scopeType, scopeID), projectOf(scopeType, scopeID))
	if err != nil {
		// Reported and not refused. The question asked here is what this scope has
		// written, and that is answerable whatever this registry will or will not do
		// with a build; refusing the whole answer over it would hide the form behind a
		// fact about the registry that the page can already show.
		view.ResolvedError = err.Error()
	} else {
		view.Resolved.CredentialSource = resolved.Source
		view.Resolved.Login = resolved.Login
		view.Resolved.HasPassword = resolved.Password != ""
	}

	s.writeJSON(w, r, http.StatusOK, view)
}

// groupOf and projectOf pick the one scope id that applies, for a resolution that is about
// a single registry row rather than a project's whole arrangement. A registry credential
// for a group is resolved with the group's own scope and no project, which is what
// "what does this group push with" means.
func groupOf(scopeType string, scopeID *uuid.UUID) *uuid.UUID {
	if scopeType != store.ScopeGroup {
		return nil
	}
	return scopeID
}

func projectOf(scopeType string, scopeID *uuid.UUID) *uuid.UUID {
	if scopeType != store.ScopeProject {
		return nil
	}
	return scopeID
}
