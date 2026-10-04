package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// What has been deployed, and how to undo it.
//
// The core proxies to the deploy module rather than handing the browser its address,
// for the same reason the registry page is the exception rather than the rule: a
// deployment record lives in the module's own database, the module is what can read
// it, and a second path to that database is a second thing to keep in step.
//
// What the core decides is only who may ask. Everything it sends on is the module's
// answer, unchanged and in the module's words — including when the module is not
// installed, which is a sentence to show rather than an error page.

// handleProjectDeployments is what a project has running, and what happened to it.
func (s *Server) handleProjectDeployments(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	module, err := s.deployModuleFor(r, project)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if module == nil {
		// Nothing installed is a state the page draws, not a failure of the request:
		// "no deploy module" and "this project's deployments could not be read" are
		// different answers and the first is an ordinary one.
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"reason": "no_deploy_module", "deployments": []any{},
		})
		return
	}

	query := url.Values{}
	query.Set("project", project.Path)
	for _, key := range []string{"cluster", "namespace"} {
		if value := r.URL.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}

	body, err := s.callDeployModule(r.Context(), module, http.MethodGet, "/deployments?"+query.Encode(), nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeRaw(w, r, http.StatusOK, body)
}

// handleRevertDeployment puts one chosen version back on a place.
//
// Not an undo of "the previous revision": the client sends the deployment it wants
// back, the module looks up the digest that deployment ran, and that digest is what
// goes onto the workload. Where it goes is decided by this module's history rather
// than by whatever the cluster still remembers, which is bounded, prunable and lost
// entirely if the Deployment is ever recreated.
//
// Refused without the write scope, and refused for anybody who may only look: this
// changes a running system, and a page that shows what is deployed has no business
// being able to change it.
func (s *Server) handleRevertDeployment(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	user := userFrom(r.Context())
	if !user.IsAdmin {
		s.writeError(w, r, errForbidden("putting a version back is an administrator's action"))
		return
	}

	module, err := s.deployModuleFor(r, project)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if module == nil {
		s.writeError(w, r, errBadRequest(
			"no deploy module is installed, so there is nothing to roll back"))
		return
	}

	var request struct {
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  string `json:"workload"`
		// DeploymentID is the record to go back to. Sent as an id rather than as an
		// image, because the module is the one that knows what a record ran, and a
		// client that could name an arbitrary image could put anything on a cluster.
		DeploymentID string `json:"deployment_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(request.DeploymentID) == "" {
		s.writeError(w, r, errBadRequest(
			"say which version to go back to: a revert names the deployment, not \"the last one\""))
		return
	}

	body, err := json.Marshal(map[string]string{
		"project":       project.Path,
		"cluster":       request.Cluster,
		"namespace":     request.Namespace,
		"workload":      request.Workload,
		"deployment_id": request.DeploymentID,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	answer, err := s.callDeployModule(r.Context(), module, http.MethodPost, "/revert", body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	s.log.Info("a version was put back", "project", project.Path,
		"cluster", request.Cluster, "deployment", request.DeploymentID, "user", user.Username)
	s.writeRaw(w, r, http.StatusOK, answer)
}

// deployModuleFor is the deploy module this project's deployments are read from.
//
// By the target the project named and by what is installed, because both can differ:
// a project may name a target whose module is not here any more, and that is worth
// saying rather than answering with some other module's history.
func (s *Server) deployModuleFor(r *http.Request, project *models.Project) (*models.Integration, error) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		target = strings.TrimSpace(r.PathValue("target"))
	}

	modules, err := s.store.Integrations().List(r.Context())
	if err != nil {
		return nil, err
	}

	var found *models.Integration
	for _, module := range modules {
		if !strings.HasPrefix(module.Kind, "deploy:") || !module.Enabled {
			continue
		}
		if target != "" && module.Kind != fmt.Sprintf(deployTargetKind, target) {
			continue
		}
		found = module
		break
	}

	if found == nil && target != "" {
		return nil, errNotFoundf(
			"no deploy module for the target %q is installed on this instance", target)
	}
	return found, nil
}

// callDeployModule asks a deploy module something, as that module.
//
// A short-lived credential scoped to this project, so the module can tell which
// project's deployments it is being asked about without being told twice. It is not
// held anywhere: it exists for this request and is over when the answer is.
func (s *Server) callDeployModule(ctx context.Context, module *models.Integration,
	method, path string, body []byte) ([]byte, error) {

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	request, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(module.Endpoint, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("could not address the %s module: %w", module.Kind, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	// A deployment can take as long as a rollout, and this is a page request rather
	// than a job: the answer is about the past, so it is a read and it should be
	// quick. A module that cannot answer in half a minute is not answering at all.
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the %s module did not answer: %w", module.Kind, err)
	}
	defer response.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode != http.StatusOK {
		var refused struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &refused) == nil && refused.Error.Message != "" {
			return nil, errBadRequestf("the %s module said: %s", module.Kind, refused.Error.Message)
		}
		return nil, fmt.Errorf("the %s module said %s", module.Kind, response.Status)
	}
	return raw, nil
}

// writeRaw passes a module's own JSON on unchanged.
func (s *Server) writeRaw(w http.ResponseWriter, r *http.Request, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
