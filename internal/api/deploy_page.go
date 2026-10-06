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
	due := s.placesDueNow(r.Context(), project)
	for _, spec := range config.Deploys {
		places = append(places, map[string]any{
			"name":   spec.Name,
			"module": spec.Module,
			"target": spec.Target,
			// Whether the run under way has not reached this place yet.
			//
			// Asked of the run rather than worked out by the page: a place this run is
			// still working its way towards has nothing of its own to show, and a page
			// that fills the gap with the last thing that happened to it draws a green
			// deployment of an older commit — which reads as "done" for as long as the
			// run takes, right beside another place that is plainly still going.
			//
			// The names come from the default branch while the jobs come from the commit
			// being run, so a run deployed from a branch whose file disagrees with the
			// default one says nothing here rather than guessing: an entry the run has
			// no job for is simply not marked.
			"queued":   due[spec.Target],
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

// placesDueNow names the places the run under way has not reached yet.
//
// The newest run that has not ended, and the deployments in it that have neither begun
// nor finished: those are the places whose turn is coming. A run with nothing left to do
// says nothing about any place — a place it never deployed to was not waiting for
// anything, and saying "queued" there would be a place that can never stop waiting.
func (s *Server) placesDueNow(ctx context.Context, project *models.Project) map[string]bool {
	due := map[string]bool{}

	runs, _, err := s.store.Pipelines().ListPipelinesPage(ctx, project.ID,
		store.PipelineQuery{Page: 1, PerPage: 1})
	if err != nil || len(runs) == 0 {
		return due
	}
	switch runs[0].Status {
	case store.PipelinePending, store.PipelineRunning:
	default:
		return due
	}

	jobs, err := s.store.Pipelines().JobsOfPipeline(ctx, runs[0].ID)
	if err != nil {
		return due
	}
	for _, job := range jobs {
		if job.Stage != "deploy" || job.Status != store.JobPending {
			continue
		}
		// "Target" and not "target": the specification is written into the job as the
		// Go value marshalled, and its fields carry no names of their own, so it is
		// stored under the field's own. Read as the file spells it, this answers
		// nothing and says every place is waiting for ever.
		target, _ := job.Deploy["Target"].(string)
		if target != "" {
			due[target] = true
		}
	}
	return due
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

	// Putting a version back is not a deployment, and a page asking what a deployment
	// of this project goes through is not asking what a rollback goes through. Asked
	// for by name, and answered from the list the core keeps for it, so that the page
	// draws the same three rows whether it learned about this from a run or opened on a
	// finished one.
	if r.URL.Query().Get("kind") == "revert" {
		s.writeJSON(w, r, http.StatusOK, map[string]any{"steps": deployStepsForRevert()})
		return
	}

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

// handleProjectDeployCurrent asks the module what is running in one place, right now.
//
// Asked of the module rather than answered from what this core knows, because the
// question is about the module's own kind of destination: a cluster can be asked which
// image a workload runs, an archive over SSH can only be asked what was last written to
// it, and neither answer is the core's to work out. It is also the only one of the two
// that is true — the core's history says what was done, and what was done is not always
// what is there.
//
// A module that cannot answer the question says so in the answer rather than by
// failing: "this module cannot tell you what is running" and "the request failed" are
// different things, and the page draws them differently.
func (s *Server) handleProjectDeployCurrent(w http.ResponseWriter, r *http.Request) {
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
			"known": false, "asked": "nothing",
			"reason": "no deploy module is installed on this instance",
		})
		return
	}

	query := url.Values{}
	query.Set("project", project.Path)
	for _, key := range []string{"cluster", "namespace", "workload"} {
		if value := r.URL.Query().Get(key); value != "" {
			query.Set(key, value)
		}
	}

	body, err := s.callDeployModule(r.Context(), module, http.MethodGet, "/current?"+query.Encode(), nil)
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
// handleTestDeployCluster asks the module whether one place can be reached.
//
// Its own endpoint rather than a row of settings, because the answer is about the world
// rather than about the configuration: a kubeconfig can be perfectly valid and the
// cluster unreachable, and the moment to be told that is before a deployment is
// attempted rather than after a timeout. The module is asked, rather than the core
// working it out, because what "reachable" means is the module's business — it is the
// thing that has to get in.
//
// The place is named by the name a repository writes in `cluster:`, which is also the
// name the rows are listed under, so what is tested is what is on the page.
func (s *Server) handleTestDeployCluster(w http.ResponseWriter, r *http.Request) {
	project, _, err := s.projectWithAccess(r, store.ActionReadCI)
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	var request struct {
		Cluster string `json:"cluster"`
	}
	if err := decodeJSON(r, &request); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(request.Cluster) == "" {
		s.writeError(w, r, errBadRequest("say which place to test"))
		return
	}

	module, err := s.deployModuleFor(r, project)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	if module == nil {
		s.writeError(w, r, errBadRequest(
			"no deploy module is installed, so there is nowhere to test"))
		return
	}

	body, err := json.Marshal(map[string]any{
		"project": project.Path,
		"cluster": request.Cluster,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	answer, err := s.callDeployModule(r.Context(), module, http.MethodPost, "/clusters/test", body)
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	s.writeRaw(w, r, http.StatusOK, answer)
}

// revertRequest is what a rollback asks for.
//
// DeploymentID names the record to go back to, sent as an id rather than as an image,
// because the module is the one that knows what a record ran, and a client that could
// name an arbitrary image could put anything on a cluster.
type revertRequest struct {
	Cluster      string `json:"cluster"`
	Namespace    string `json:"namespace"`
	Workload     string `json:"workload"`
	DeploymentID string `json:"deployment_id"`
}

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

	var request revertRequest
	if err := decodeJSON(r, &request); err != nil {
		s.writeError(w, r, err)
		return
	}
	if strings.TrimSpace(request.DeploymentID) == "" {
		s.writeError(w, r, errBadRequest(
			"say which version to go back to: a revert names the deployment, not \"the last one\""))
		return
	}

	// Which registry this place pulls from, when it names one. A rollback has no image of
	// its own — the module reads that out of the record — so what travels with it is the
	// address and the credential for it, and nothing at all when the place names nothing.
	// That last case is the one that has always worked: the record carries the address, and
	// the secret the last deployment wrote is still in the namespace.
	registry, err := s.placeRegistryCredential(r.Context(), project, module, request.Cluster, nil)
	if err != nil {
		s.log.Warn("a rollback could not be told which registry its place uses",
			"project", project.Path, "cluster", request.Cluster, "error", err)
	}

	body, err := json.Marshal(map[string]any{
		"project":       project.Path,
		"cluster":       request.Cluster,
		"namespace":     request.Namespace,
		"workload":      request.Workload,
		"deployment_id": request.DeploymentID,
		"registry":      registry,
	})
	if err != nil {
		s.writeError(w, r, err)
		return
	}

	// The plan, before the first line of it.
	//
	// Published here rather than with a run because there is no run: somebody in a
	// browser pressed a button, and the page that pressed it has to be told what that
	// button's work goes through before the first step happens. Without this the page
	// keeps whatever list it had — a deployment's, with its build and its push — beside
	// a log about pods, and every row above the real ones is a step that is never going
	// to turn.
	s.publishPipeline(r.Context(), project.ID, nil, models.EventDeployPlan, map[string]any{
		"steps": deployStepsForRevert(),
	})

	// Started, and carried out here rather than in the browser's request.
	//
	// A revert is a rollout: it takes as long as the pods take, which on a real cluster
	// is longer than anybody's patience and, on a laptop, longer than the tab that
	// asked for it. Carried out inside the request, closing the tab — or reloading it,
	// or losing the network for a moment — cancelled the work halfway: the image was on
	// the workload and the module's record said "context canceled", so the place showed
	// a failure over a rollback that had worked. A deployment has never had this
	// problem, because it is a job of the run's rather than a request somebody's.
	//
	// The context outlives the request, and the module's own timeout is the deadline:
	// nobody waiting on this answer is waiting for the rollout, they are waiting to be
	// told it has begun.
	go s.carryOutRevert(context.WithoutCancel(r.Context()), module, project, body, request, user)

	s.log.Info("a version is being put back", "project", project.Path,
		"cluster", request.Cluster, "deployment", request.DeploymentID, "user", user.Username)

	s.writeJSON(w, r, http.StatusAccepted, map[string]any{
		"started":  true,
		"cluster":  request.Cluster,
		"place":    request.Cluster,
		"workload": request.Workload,
	})
}

// carryOutRevert is the rollback itself, told about rather than waited for.
//
// The module narrates as it goes and every line is published, so a page watching the
// place sees the pods come up rather than nothing for a minute and then a state change.
// The history is published at the end, which is what makes the row for it appear.
func (s *Server) carryOutRevert(ctx context.Context, module *models.Integration,
	project *models.Project, body []byte, request revertRequest, user *models.User) {

	answer, err := s.callDeployModuleStream(ctx, module, "/revert", body, func(line []byte) {
		// The module's line, whole, with the one thing it cannot know added: which
		// place this is about. A revert is somebody choosing a place and asking for an
		// image there, and it is that place's card that has to show it happening —
		// without this the lines belong to no place, and a card about one place shows
		// nothing while its own workload is being changed, which is a log that stops
		// mid-run with no failure anywhere.
		place := map[string]any{
			"cluster": request.Cluster, "namespace": request.Namespace,
			"workload": request.Workload,
		}
		if len(bytes.TrimSpace(line)) == 0 {
			return
		}
		s.publishPipeline(ctx, project.ID, nil, models.EventDeployOperation,
			relayOf(line, 0, place))
	})
	if err != nil {
		s.log.Warn("a rollback did not finish", "project", project.Path,
			"cluster", request.Cluster, "deployment", request.DeploymentID, "error", err)
		s.publishPipeline(ctx, project.ID, nil, models.EventDeployOperation, map[string]any{
			"phase": "apply", "message": err.Error(), "failed": true,
			"deployment": map[string]any{
				"cluster": request.Cluster, "namespace": request.Namespace,
				"workload": request.Workload,
			},
		})
	}

	s.publishPipeline(ctx, project.ID, nil, models.EventDeployHistory, map[string]any{
		"project": project.Path,
	})

	if err != nil {
		s.log.Info("a rollback was refused", "project", project.Path,
			"cluster", request.Cluster, "user", user.Username, "error", err)
		return
	}

	// What it left behind, in the log rather than in the answer: nobody is waiting for
	// this any more, and the record the module wrote is where the truth is.
	var last struct {
		Deployment *struct {
			ID       string `json:"id"`
			Image    string `json:"image"`
			State    string `json:"state"`
			Workload string `json:"workload"`
		} `json:"deployment"`
	}
	for _, line := range bytes.Split(answer, []byte{byte(10)}) {
		if len(bytes.TrimSpace(line)) > 0 {
			_ = json.Unmarshal(line, &last)
		}
	}
	if last.Deployment != nil {
		s.log.Info("a version was put back", "project", project.Path,
			"cluster", request.Cluster, "deployment", last.Deployment.ID,
			"image", last.Deployment.Image, "state", last.Deployment.State,
			"user", user.Username)
	}
}

// deployModuleFor is the deploy module this project's deployments are read from.
//
// By the target the project named and by what is installed, because both can differ:
// a project may name a target whose module is not here any more, and that is worth
// saying rather than answering with some other module's history.
// deployModuleFor is the module a request names, or the one this project deploys with.
func (s *Server) deployModuleFor(r *http.Request, project *models.Project) (*models.Integration, error) {
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	if target == "" {
		target = strings.TrimSpace(r.PathValue("target"))
	}
	if target == "" {
		// Said as a request that is missing something, not as a failure inside the
		// server. With no name there is no kind to look up, and asking for the module
		// called "" fails in a way that reads as the instance being broken rather than
		// as the caller having left out the one word that says which module to ask.
		return nil, errBadRequest(
			"say which deploy module this is about: the request names none")
	}

	found, err := s.deployModule(r.Context(), target)
	if err != nil {
		return nil, err
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
