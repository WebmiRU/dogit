package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// handleRegistryAccess answers a module's question: may this caller touch this
// project's images?
//
// This is the only place a registry permission is decided. The module holds no
// member list of its own and cannot grant anything: an account removed here, or a
// member demoted, stops pushing images at the next request, and a project that
// moved to another group carries its access with it.
//
// The module asks with a project path rather than an id because that is what a
// client gave it: an image name is a path, and there is nothing in a push to
// suggest a UUID.
func (s *Server) handleRegistryAccess(w http.ResponseWriter, r *http.Request) {
	var req struct {
		// Token is the caller's credential, presented by the registry client
		// through the module. It is the user's, not the module's: this is the whole
		// mechanism by which the core recognises who is pushing.
		Token string `json:"token"`
		// Project is the path the image lives under.
		Project string `json:"project"`
		// Action is pull, push or delete.
		Action string `json:"action"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	// An unreadable token is "nobody", answered with 200 and an empty answer: the
	// module decides what to do with it, and a 401 here would blur "this token is
	// not real" with "this user may not do that".
	presented, err := s.tokenFromHeaderValue(req.Token)
	if err != nil {
		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{Allowed: false, Reason: "no_token"})
		return
	}

	// The same answer every module gets for the same question, computed once.
	who := s.introspect(r, presented)
	if !who.Active {
		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{Allowed: false, Reason: "unknown_token"})
		return
	}
	if who.UserID == nil {
		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{Allowed: false, Reason: "no_user"})
		return
	}

	action, err := registryAction(req.Action)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// A module cannot ask about somebody else's module: the token is checked
	// against this module, so a token minted for a different one resolves to
	// nothing here.
	path := strings.Trim(req.Project, "/")
	if path == "" {
		s.writeError(w, r, errBadRequest("a project path is required"))
		return
	}

	project, err := s.store.Projects().ByPath(r.Context(), path)
	if err != nil {
		// A project that does not exist is not told apart from one the caller
		// cannot see. Otherwise the answer to "can I push here?" would be a way to
		// enumerate every project on the instance.
		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{Allowed: false, Reason: "no_such_project"})
		return
	}

	level, err := s.store.Permissions().AccessLevel(r.Context(), *who.UserID, project.ID)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	minimum, known := store.MinLevel[action]
	if !known {
		s.writeError(w, r, errBadRequest("unknown action"))
		return
	}

	// A credential the core itself minted for this project stands on its own.
	//
	// This is how a machine pushes: the core issues the builder a token scoped to
	// one project, and the builder is deliberately not a member of it — it is a
	// system account that must not hold rights on anything but the one thing it was
	// created to do. Judging that token by the membership table would mean granting
	// the builder real access to every project it builds, which is the opposite of
	// what the scope is for.
	//
	// The scope has to match this project as well as this action. A token minted for
	// another project carries no rights here at all, and falls through to the
	// membership check below like any other credential.
	if who.ProjectID != nil && *who.ProjectID == project.ID && who.HasScope(scopeOf(action)) {
		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{
			Allowed:  true,
			Action:   req.Action,
			Level:    level,
			Minimum:  minimum,
			Username: who.Username,
			UserID:   who.UserID,
			Project: map[string]any{
				"id":   project.ID,
				"path": project.Path,
			},
		})
		return
	}

	if level < minimum {
		// Somebody with no access at all is told the project does not exist. "You
		// may not" would confirm it does, and the registry is the one place a
		// stranger could otherwise enumerate every project on the instance by asking
		// whether they may push to it.
		if level == store.NoAccess {
			s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{
				Allowed: false,
				Reason:  "no_such_project",
			})
			return
		}

		s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{
			Allowed: false,
			Reason:  "not_permitted",
			Level:   level,
			Minimum: minimum,
		})
		return
	}

	s.writeJSON(w, r, http.StatusOK, models.RegistryAccess{
		Allowed:  true,
		Action:   req.Action,
		Level:    level,
		Minimum:  minimum,
		Username: who.Username,
		UserID:   who.UserID,
		Project: map[string]any{
			"id":   project.ID,
			"path": project.Path,
		},
	})
}

// handleRegistryAuthenticate signs a registry client in.
//
// A registry client is a program with its own idea of how to log in: it reads a
// challenge, fetches a token from the realm the challenge names, and presents that
// token afterwards. So the instance needs a realm it can be pointed at, and this
// is it.
//
// The password arrives here rather than at the module, which is the only place it
// could honestly be checked: the core owns identity, and a module that verified
// passwords would have to keep a copy of every hash to do it.
func (s *Server) handleRegistryAuthenticate(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	var req struct {
		Login string `json:"login"`
		// Credential is what the client presented as its password. It is usually a
		// password, and it is sometimes something the core itself issued: a runner
		// has no password to type, only the token it was handed for this project's
		// images. Both are credentials and both are checked here, where identity
		// lives — the module forwards what it was given and never looks inside.
		Credential string `json:"credential"`
		Password   string `json:"password"`
		// Project and Scopes come from the scope the client asked for, which is how
		// a docker login becomes access to one repository rather than to everything.
		Project string   `json:"project"`
		Scopes  []string `json:"scopes"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	credential := req.Credential
	if credential == "" {
		credential = req.Password
	}

	// A credential the core issued is honoured for what it is: a token, not a
	// password. It carries its own rights, so the membership check below is not
	// applied to it — a machine account is deliberately a member of nothing.
	user, fromToken, err := s.authenticateCredential(r, req.Login, credential)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// The same wording and roughly the same work for an account that does not
			// exist, so the answer does not say which accounts are real.
			auth.VerifyPassword(req.Password, dummyHash)
		}
		s.writeJSON(w, r, http.StatusUnauthorized, map[string]any{
			"errors": []map[string]string{{
				"code": "UNAUTHORIZED", "message": "invalid login or password",
			}},
		})
		return
	}

	path := strings.Trim(req.Project, "/")
	if path == "" {
		s.writeError(w, r, errBadRequest("a project is required"))
		return
	}

	project, err := s.store.Projects().ByPath(r.Context(), path)
	if err != nil {
		s.writeJSON(w, r, http.StatusUnauthorized, map[string]any{
			"errors": []map[string]string{{
				"code": "UNAUTHORIZED", "message": "invalid login or password",
			}},
		})
		return
	}

	wanted := registryScopes(req.Scopes)

	var scopes []string
	if fromToken != nil {
		// The token's own scope is the whole of what may be minted from it. A token
		// for one repository cannot be traded at the token endpoint for one that
		// reaches further than it does.
		if fromToken.ProjectID == nil || *fromToken.ProjectID != project.ID {
			s.writeJSON(w, r, http.StatusUnauthorized, map[string]any{
				"errors": []map[string]string{{
					"code": "UNAUTHORIZED", "message": "invalid login or password",
				}},
			})
			return
		}
		for _, scope := range wanted {
			if !fromToken.HasScope(scope) {
				s.writeJSON(w, r, http.StatusUnauthorized, map[string]any{
					"errors": []map[string]string{{
						"code": "UNAUTHORIZED", "message": "invalid login or password",
					}},
				})
				return
			}
		}
		scopes = wanted
	} else {
		// No rights at all means no credential either. Minting one would leave a
		// token lying around for a caller who can do nothing with it, and every later
		// request with it would be a request the core has to refuse again.
		level, err := s.store.Permissions().AccessLevel(r.Context(), user.ID, project.ID)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		if level < store.MinLevel[store.ActionRegistryPull] {
			s.writeJSON(w, r, http.StatusUnauthorized, map[string]any{
				"errors": []map[string]string{{
					"code": "UNAUTHORIZED", "message": "invalid login or password",
				}},
			})
			return
		}

		scopes, err = filterScopes(integration, wanted)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
	}

	// The token is short: it only has to survive the pull or push it was minted
	// for, and a credential that outlives it would keep working after the rights
	// behind it were taken away.
	token, _, err := s.mintModuleToken(r, user, integration, &project.ID, scopes, moduleTokenTTL)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"token":        token,
		"access_token": token,
		"expires_in":   int(moduleTokenTTL.Seconds()),
		"username":     user.Username,
	})
}

// authenticateLogin checks a login and password the one way that is meaningful.
func (s *Server) authenticateLogin(r *http.Request, login, password string) (*models.User, error) {
	user, _, err := s.authenticateCredential(r, login, password)
	return user, err
}

// authenticateCredential accepts either kind of credential a registry client can
// present, and says which kind it was.
//
// A password is checked against the account's hash. A token is looked up as one,
// and the answer about it is returned so the caller can honour its scope rather
// than the account's rights: those are different things, and a machine's token
// being narrow is the point of it.
//
// Which one was presented is decided by whether the core recognises the string as
// a token, not by anything the caller says: a client that calls itself an account
// gets nothing it has not proved.
func (s *Server) authenticateCredential(r *http.Request, login, credential string) (*models.User, *models.Introspection, error) {
	if login == "" || credential == "" {
		return nil, nil, store.ErrNotFound
	}

	if presented, err := s.tokenFromHeaderValue(credential); err == nil {
		who := s.introspect(r, presented)
		if who.Active && who.UserID != nil {
			user, err := s.store.Users().ByID(r.Context(), *who.UserID)
			if err == nil {
				return user, &who, nil
			}
		}
	}

	user, err := s.findUserByLogin(r, login)
	if err != nil {
		return nil, nil, err
	}
	if !auth.VerifyPassword(credential, user.PasswordHash) {
		return nil, nil, store.ErrNotFound
	}
	return user, nil, nil
}

// registryScopes turns what a client asked for into scopes this module knows.
//
// A client sends "repository:name:pull,push"; anything it does not understand is
// dropped rather than honoured, and a request for a scope the module never
// declared cannot widen what it may do.
func registryScopes(requested []string) []string {
	if len(requested) == 0 {
		return []string{models.ScopeRegistryPull, models.ScopeRegistryPush}
	}

	wanted := map[string]bool{}
	for _, scope := range requested {
		switch scope {
		case "pull":
			wanted[models.ScopeRegistryPull] = true
		case "push":
			wanted[models.ScopeRegistryPush] = true
		case "delete":
			wanted[models.ScopeRegistryDelete] = true
		}
	}

	out := []string{}
	for _, scope := range []string{
		models.ScopeRegistryPull, models.ScopeRegistryPush, models.ScopeRegistryDelete,
	} {
		if wanted[scope] {
			out = append(out, scope)
		}
	}
	if len(out) == 0 {
		return []string{models.ScopeRegistryPull}
	}
	return out
}

// handleRegistryResolve answers the other half of a registry's question: which
// project does this image name belong to?
//
// The module fronts a storage full of names and knows nothing about projects, so
// it cannot work this out for itself — and it must not be able to: the project
// list is an administrator's view, not something a module is entitled to page
// through. The core holds both the projects and the naming rule, so the rule is
// applied here, once.
func (s *Server) handleRegistryResolve(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	var req struct {
		Image string `json:"image"`
	}
	if err := decodeJSON(r, &req); err != nil {
		s.writeError(w, r, err)
		return
	}

	name := strings.Trim(strings.TrimSpace(req.Image), "/")
	if name == "" {
		s.writeError(w, r, errBadRequest("an image name is required"))
		return
	}

	// The naming rule is this module's own setting, read through the same
	// inheritance every module's settings go through.
	settings, err := s.store.Integrations().SettingsFor(r.Context(), integration.ID, nil, nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// An unset setting means the module's own declared default, which is what a
	// default is: the module said what it will do when nobody says otherwise.
	template := ""
	if raw, ok := settings["image_name_template"]; ok {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			template = strings.TrimSpace(value)
		}
	}
	if template == "" {
		if spec, found := settingSpecOf(integration, "image_name_template"); found {
			if value, ok := spec.Default.(string); ok {
				template = strings.TrimSpace(value)
			}
		}
	}
	if template == "" {
		s.writeError(w, r, errBadRequest(
			"this module has no image name template set, so no image name can be resolved"))
		return
	}

	projects, err := s.store.Projects().ListAll(r.Context())
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	for _, project := range projects {
		if imageNameFor(template, project.Path, "") == name {
			s.writeJSON(w, r, http.StatusOK, map[string]any{
				"image":    name,
				"project":  project.Path,
				"id":       project.ID,
				"template": template,
			})
			return
		}
	}

	// No answer rather than an empty one: a name this rule cannot produce belongs
	// to something else entirely, and the registry holds those too.
	s.writeJSON(w, r, http.StatusOK, map[string]any{"image": name, "project": nil})
}

// imageNameFor renders an image name from a project path.
//
// This is the boundary a permission check rests on, so it is written once and used
// both here and by the module. A name that does not contain the project cannot be
// traced back to one, and two projects could then write to the same name.
func imageNameFor(template, projectPath, branch string) string {
	group := ""
	name := projectPath
	if index := strings.Index(projectPath, "/"); index != -1 {
		group, name = projectPath[:index], projectPath[index+1:]
	}

	return strings.Trim(strings.NewReplacer(
		"{{group}}", group,
		"{{project}}", name,
		"{{branch}}", branch,
		"{{path}}", projectPath,
	).Replace(template), "/")
}

// scopeOf is the permission a token must carry to do an action here.
//
// The same naming as everywhere else scopes are written: registry:pull and so on.
// A token with no scopes carries none, which is what an old token issued before
// scopes existed should be taken to mean.
func scopeOf(action store.Action) string {
	switch action {
	case store.ActionRegistryPush:
		return models.ScopeRegistryPush
	case store.ActionRegistryDelete:
		return models.ScopeRegistryDelete
	default:
		return models.ScopeRegistryPull
	}
}

func registryAction(action string) (store.Action, error) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "pull", "":
		return store.ActionRegistryPull, nil
	case "push":
		return store.ActionRegistryPush, nil
	case "delete":
		return store.ActionRegistryDelete, nil
	default:
		return "", errBadRequestf("unknown registry action %q", action)
	}
}
