package api

import (
	"bufio"
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
	"github.com/ewolf/dogit/internal/pipeline"
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

// handleProjectDeployPlaces is where this project says it deploys to, as the repository
// wrote it.
//
// Read-only on purpose. The policy lives in the file, because it is a claim about code
// and is reviewed with it; what the page adds is the ability to read it without opening
// a yml — "which branch reaches production" is a question people ask of the interface,
// not of a file they have to go and find. A second copy of the rules here would be a
// second answer to the same question, and this is not where that belongs.
func (s *Server) handleProjectDeployPlaces(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	config, err := s.pipelineConfig(r.Context(), s.repos.PathFor(project), project.DefaultBranch)
	if err != nil {
		// No configuration is a state the page draws, not a failure of the request.
		s.writeJSON(w, r, http.StatusOK, map[string]any{"places": []any{}})
		return
	}

	places := make([]map[string]any, 0, len(config.Deploys))
	for _, spec := range config.Deploys {
		places = append(places, map[string]any{
			"name":     spec.Name,
			"target":   spec.Target,
			"cluster":  spec.Cluster,
			"namespace": spec.Namespace,
			"tag_only": spec.TagOnly,
			"rollout":  spec.Rollout,
			// Said in the repository's own words rather than evaluated here: a page
			// that interpreted the rules would have to keep up with the interpreter.
			"rules": rulesOf(spec.Rules),
		})
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"places": places,
		"branch": project.DefaultBranch,
	})
}

// handleProjectDeployPlan is the step list a deployment of this project goes through.
//
// Sent on its own so that a page opened between two deployments can draw the list
// without waiting for a run to start. The list used to arrive only as an event, which
// meant the page either showed nothing or replayed the last run's — and a list of steps
// belongs to the repository rather than to any one of its runs.
func (s *Server) handleProjectDeployPlan(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	config, err := s.pipelineConfig(r.Context(), s.repos.PathFor(project), project.DefaultBranch)
	if err != nil || len(config.Deploys) == 0 {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"steps": []any{}})
		return
	}

	manifests, pre, post := 0, 0, 0
	for _, place := range config.Deploys {
		manifests += len(place.Manifests)
		pre += len(place.Pre)
		post += len(place.Post)
	}

	// Whether a run builds an image is the one thing about the list this cannot know
	// without a run, and it is read from the configuration rather than guessed.
	builds := false
	for _, spec := range config.Jobs {
		if len(spec.Build) > 0 {
			builds = true
			break
		}
	}
	built := ""
	if builds {
		built = "the build job"
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{
		"steps": deployStepsFor(built, manifests, pre, post),
	})
}

// rulesOf is what a place listens for, in words.
func rulesOf(rules []pipeline.Rule) []string {
	said := make([]string, 0, len(rules))
	for _, rule := range rules {
		switch {
		case rule.If == "" && len(rule.Changes) > 0:
			said = append(said, "changed files: "+changedPaths(rule.Changes))
		case rule.If != "":
			said = append(said, rule.If)
		case rule.When == "never":
			said = append(said, "never")
		default:
			said = append(said, "always")
		}
	}
	return said
}

// changedPaths are the file patterns a rule is about, in one line.
func changedPaths(changes []pipeline.RuleChange) string {
	paths := []string{}
	for _, change := range changes {
		paths = append(paths, change.Paths...)
	}
	return strings.Join(paths, ", ")
}

// handleProjectDeployments is what a project has running, and what happened to it.
// handleProjectImages is the catalogue of images a project has put on a place, read
// from the deploy module and passed through as it came.
func (s *Server) handleProjectDeployImages(w http.ResponseWriter, r *http.Request) {
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
		s.writeJSON(w, r, http.StatusOK, map[string]any{
			"reason": "no_deploy_module", "images": []any{},
		})
		return
	}

	query := url.Values{}
	query.Set("project", project.Path)
	for _, key := range []string{"cluster", "namespace", "page", "per_page"} {
		if value := r.URL.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}

	body, err := s.callDeployModule(r.Context(), module, http.MethodGet, "/images?"+query.Encode(), nil)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeRaw(w, r, http.StatusOK, body)
}

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
	// Paging is passed through rather than decided here: the module is what knows how
	// many rows there are, and a page size chosen in two places is a page size that
	// will disagree.
	for _, key := range []string{"cluster", "namespace", "page", "per_page"} {
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

	answer, err := s.callDeployModuleStream(r.Context(), module, "/revert", body, func(line []byte) {
		// Each line the module sends is relayed as an event, so a page watching the
		// history sees the pods coming up rather than nothing for two minutes and then
		// a state change.
		var progress struct {
			Phase   string `json:"phase"`
			Message string `json:"message"`
			Ready   int    `json:"ready"`
			Desired int    `json:"desired"`
			Failed  bool   `json:"failed"`
		}
		if json.Unmarshal(line, &progress) != nil || progress.Message == "" {
			return
		}
		s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployOperation, map[string]any{
			"phase": progress.Phase, "message": progress.Message,
			"ready": progress.Ready, "desired": progress.Desired,
		})
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployHistory, map[string]any{
		"project": project.Path,
	})

	s.log.Info("a version was put back", "project", project.Path,
		"cluster", request.Cluster, "deployment", request.DeploymentID, "user", user.Username)

	// A summary, not the module's stream.
	//
	// The module narrates as it goes because a revert is a rollout and somebody is
	// watching pods come up — but that is between the module and this server. What the
	// browser gets is one object it asked for and can parse: handing the ndjson
	// straight through left the page failing to read its own answer.
	var last struct {
		Deployment *struct {
			ID        string `json:"id"`
			Image     string `json:"image"`
			Cluster   string `json:"cluster"`
			Namespace string `json:"namespace"`
			Workload  string `json:"workload"`
			State     string `json:"state"`
			Reason    string `json:"reason"`
		} `json:"deployment"`
	}
	for _, line := range bytes.Split(answer, []byte{byte(10)}) {
		if len(bytes.TrimSpace(line)) > 0 {
			_ = json.Unmarshal(line, &last)
		}
	}

	s.writeJSON(w, r, http.StatusOK, map[string]any{"deployment": last.Deployment})
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

// callDeployModuleStream asks a deploy module something and relays each line as it
// arrives.
//
// The same shape as callDeployModule, with the body of the response read line by line
// as it comes rather than at the end: a revert is a rollout, and the pods coming up
// one at a time is the part somebody watching actually wants.
func (s *Server) callDeployModuleStream(ctx context.Context, module *models.Integration,
	path string, body []byte, onLine func([]byte)) ([]byte, error) {

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(module.Endpoint, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("could not address the %s module: %w", module.Kind, err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/x-ndjson")

	// No timeout at all, as for a deploy: the module decides how long a rollout takes.
	response, err := (&http.Client{Timeout: 0}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("the %s module did not answer: %w", module.Kind, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
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

	var whole []byte
	reader := bufio.NewReader(response.Body)
	for {
		line, readErr := reader.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			whole = append(whole, trimmed...)
			whole = append(whole, byte(10))
			if onLine != nil {
				onLine(trimmed)
			}
		}
		if readErr != nil {
			break
		}
	}
	return whole, nil
}
