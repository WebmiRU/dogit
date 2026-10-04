package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/deploy"
	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// What this module answers to, and what it refuses.
//
// Every request here names a project and a cluster. The project is how the core
// resolves which settings apply, because the levels are the core's business; the
// cluster is one of this module's own rows. This module does not decide whether a
// project may deploy anywhere — it was told where, and it does that.

// deployRequest is one deployment, as the core describes it.
//
// The manifests arrive as bytes because they are the repository's, read at the commit
// being deployed. This module substitutes the image and applies the rest as written;
// anything it did not understand would be applying something nobody wrote.
type deployRequest struct {
	Project   string `json:"project"`
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`

	// Image is what to substitute, digest included. Empty means the manifests are
	// applied exactly as they are, which is only right for a deployment that changes
	// nothing about what runs.
	Image       string `json:"image"`
	Placeholder string `json:"placeholder"`

	Manifests []struct {
		APIVersion string `json:"api_version"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Body       string `json:"body"`
	} `json:"manifests"`

	Pre  []jobRequest `json:"pre"`
	Post []jobRequest `json:"post"`

	// Rollout names the workload to wait for, and WaitForRollout says whether to wait.
	// Registry is the credential the cluster pulls with, when it will not serve the
	// image on its own. Nil means the registry is open to the cluster and there is
	// nothing to write.
	Registry *struct {
		Address    string `json:"address"`
		Token      string `json:"token"`
		SecretName string `json:"secret_name"`
	} `json:"registry"`

	WaitForRollout bool   `json:"wait_for_rollout"`
	Rollout        string `json:"rollout"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	KeepJobs       bool   `json:"keep_jobs"`
}

type jobRequest struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Body       string `json:"body"`
}

func (c *coreClient) handleDeploy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var request deployRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Project) == "" || strings.TrimSpace(request.Cluster) == "" {
		writeError(w, http.StatusBadRequest, "a deployment names a project and a cluster")
		return
	}

	cluster, namespace, client, err := c.clusterFor(ctx, request.Project, request.Cluster, request.Namespace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if c.history == nil {
		// Refused rather than deployed-and-forgotten: a deployment nobody can undo is
		// a deployment somebody will wish they had not done.
		writeError(w, http.StatusServiceUnavailable,
			"this module cannot remember deployments, so it is not deploying anything: "+errNoHistory.Error())
		return
	}

	settings, err := c.settings(ctx, request.Project)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	substitution := k8s.Substitution{Placeholder: request.Placeholder, Image: request.Image}

	manifests := make([]k8s.Object, 0, len(request.Manifests))
	for _, one := range request.Manifests {
		manifests = append(manifests, k8s.Object{
			APIVersion: one.APIVersion, Kind: one.Kind, Namespace: namespace,
			Name: one.Name, Body: substitution.Apply([]byte(one.Body)),
		})
	}

	workload := request.Rollout
	if workload == "" {
		// Asked of what was applied rather than configured separately: a second place
		// to name the workload is a second place to get it wrong, and a rollback that
		// looked at the wrong name would undo somebody else's deployment.
		if found := deploy.WorkloadsOf(manifests); len(found) > 0 {
			workload = found[0]
		}
	}

	timeout := timeoutFrom(settings, "default_rollout_timeout", 10*time.Minute)
	if request.TimeoutSeconds > 0 {
		timeout = time.Duration(request.TimeoutSeconds) * time.Second
	}
	keepJobs := request.KeepJobs || boolSetting(settings, "keep_jobs")

	// The answer is a stream, not a value: a deployment takes minutes, and a caller
	// that hears nothing until the end is watching a spinner rather than a rollout.
	stream := newProgressWriter(w)

	deployer := deploy.New(client, c.history, func(format string, args ...any) {
		log.Printf(format, args...)
	})

	// The credential the cluster pulls with, when the core sent one. A cluster that
	// cannot pull is a rollout that never finishes, and the message for that is a
	// timeout with no cause in it.
	var pullSecret *k8s.PullSecret
	if request.Registry != nil && request.Registry.Token != "" {
		pullSecret = &k8s.PullSecret{
			Name:    pullSecretName(request.Registry.SecretName),
			Address: request.Registry.Address,
			Token:   request.Registry.Token,
		}
	}

	record, err := deployer.Run(ctx, deploy.Request{
		Progress:       stream.send,
		Project:        request.Project,
		PullSecret:     pullSecret,
		Cluster:        cluster.Name,
		Namespace:      namespace,
		Image:          request.Image,
		Placeholder:    request.Placeholder,
		Manifests:      manifests,
		Pre:            jobs(substitution, request.Pre, namespace),
		Post:           jobs(substitution, request.Post, namespace),
		WaitForRollout: request.WaitForRollout,
		Rollout:        workload,
		Workload:       workload,
		Timeout:        timeout,
		KeepJobs:       keepJobs,
	})

	// However it went, the last line says so and carries the record. The status is 200
	// either way: the request was answered, and the answer is what happened rather than
	// whether it was what was hoped for — so a reader takes the last line and is never
	// left guessing from a status code.
	//
	// One place it is not 200: a place that is already being deployed to was never
	// deployed at all, so there is no record and no stream to say anything into.
	var busy deploy.ErrBusy
	if errors.As(err, &busy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		stream.send(deploy.Progress{Phase: string(record.Phase), Message: err.Error(), Failed: true})
	}
	stream.send(deploy.Progress{Done: true, Message: "finished", Deployment: &record})
}

// clusterFor resolves which cluster, which namespace, and a client for it.
func (c *coreClient) clusterFor(ctx context.Context, project, name, namespace string) (
	Cluster, string, k8s.Client, error) {

	settings, err := c.settings(ctx, project)
	if err != nil {
		return Cluster{}, "", nil, err
	}
	clusters, err := clustersOf(settings)
	if err != nil {
		return Cluster{}, "", nil, err
	}

	cluster, found := find(clusters, name)
	if !found {
		return Cluster{}, "", nil, errClusterNotFound(name)
	}

	if strings.TrimSpace(namespace) == "" {
		namespace = cluster.DefaultNamespace
	}
	if strings.TrimSpace(namespace) == "" {
		return Cluster{}, "", nil, errNoNamespace(cluster.Name)
	}

	client, err := cluster.Connect(ctx)
	if err != nil {
		return Cluster{}, "", nil, err
	}
	return cluster, namespace, client, nil
}

// handleRevert puts a chosen image back on a workload.
//
// The client sends the id of the deployment it wants back rather than an image: this
// module is the one that knows what that deployment ran, and a client that could name
// an arbitrary image could put anything at all on a cluster.
func (c *coreClient) handleRevert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var request struct {
		Project   string `json:"project"`
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  string `json:"workload"`
		// ID is the deployment to go back to.
		ID string `json:"deployment_id"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Project) == "" || strings.TrimSpace(request.ID) == "" {
		writeError(w, http.StatusBadRequest, "a revert names a project and the deployment to go back to")
		return
	}

	id, err := uuid.Parse(request.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "that is not a deployment this module recorded")
		return
	}
	if c.history == nil {
		writeError(w, http.StatusServiceUnavailable, errNoHistory.Error())
		return
	}

	_, namespace, client, err := c.clusterFor(ctx, request.Project, request.Cluster, request.Namespace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// A stream, like a deploy: putting a version back is a rollout too, and the pods
	// coming up one at a time is exactly what somebody watching wants to see.
	stream := newProgressWriter(w)

	deployer := deploy.New(client, c.history, func(format string, args ...any) {
		log.Printf(format, args...)
	})

	record, err := deployer.Revert(ctx, deploy.RevertRequest{
		Progress:  stream.send,
		ID:        id,
		Workload:  request.Workload,
		Namespace: namespace,
		Timeout:   timeoutFrom(mustSettings(ctx, c, request.Project), "default_rollout_timeout", 10*time.Minute),
	})
	// A refusal before anything was written is a refusal; anything after is part of the
	// stream, so that a revert that started and then failed is told rather than
	// silently turned into a page that stops updating.
	var busy deploy.ErrBusy
	if errors.As(err, &busy) || (err != nil && record.ID == uuid.Nil) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		stream.send(deploy.Progress{Message: err.Error(), Failed: true})
	}
	stream.send(deploy.Progress{Done: true, Message: "finished", Deployment: &record})
}

// mustSettings is the module's settings for a project, or empty.
func mustSettings(ctx context.Context, c *coreClient, project string) map[string]any {
	settings, err := c.settings(ctx, project)
	if err != nil {
		return map[string]any{}
	}
	return settings
}

func (c *coreClient) handleDeployments(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	project := r.URL.Query().Get("project")
	name := r.URL.Query().Get("cluster")
	namespace := r.URL.Query().Get("namespace")

	if project == "" {
		writeError(w, http.StatusBadRequest, "the history is asked for by project")
		return
	}
	if c.history == nil {
		writeError(w, http.StatusServiceUnavailable, errNoHistory.Error())
		return
	}

	// No cluster named means the whole project: a page asking "what has this project
	// deployed" is not asking about one place, and making it name a cluster first
	// would mean the answer to that question is a form.
	if strings.TrimSpace(name) == "" {
		records, err := c.history.List(ctx, project, "", "")
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deployments": viewsOf(records)})
		return
	}

	if strings.TrimSpace(namespace) == "" {
		settings, err := c.settings(ctx, project)
		if err != nil {
			writeError(w, http.StatusBadGateway, err.Error())
			return
		}
		if clusters, err := clustersOf(settings); err == nil {
			if cluster, found := find(clusters, name); found {
				namespace = cluster.DefaultNamespace
			}
		}
	}

	records, err := c.history.List(ctx, project, name, namespace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"deployments": viewsOf(records)})
}

// handleTestCluster says whether a cluster can be reached, before anything is deployed
// to it.
//
// Worth having as its own question: the alternative is finding out at deploy time,
// which is the moment somebody least wants to be told that a cluster is unreachable.
func (c *coreClient) handleTestCluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var request struct {
		Project string `json:"project"`
		Cluster string `json:"cluster"`
	}
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	settings, err := c.settings(ctx, request.Project)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	clusters, err := clustersOf(settings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cluster, found := find(clusters, request.Cluster)
	if !found {
		writeError(w, http.StatusNotFound, errClusterNotFound(request.Cluster).Error())
		return
	}

	client, err := cluster.Connect(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": err.Error()})
		return
	}

	// Reading the cluster is the test. A module that connected and did nothing has
	// proved the credentials work and nothing else.
	if _, err := client.Rollout(ctx, cluster.DefaultNamespace, "dogit-connection-check"); err != nil {
		// A missing Deployment is the expected answer: the point is that the cluster
		// answered at all.
		if strings.Contains(err.Error(), "no deployment called") {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pullSecretName is the Secret this module writes, whatever the core asked for.
//
// Fixed rather than taken from the request: a name is written into every pod template
// this deployment applies, and one that came from a request could be a name of
// somebody else's existing secret.
func pullSecretName(requested string) string {
	if strings.TrimSpace(requested) == "" {
		return "dogit-registry"
	}
	return requested
}

func jobs(substitution k8s.Substitution, requests []jobRequest, namespace string) []deploy.Job {
	out := make([]deploy.Job, 0, len(requests))
	for _, one := range requests {
		out = append(out, deploy.Job{
			Name: one.Name,
			Object: k8s.Object{
				APIVersion: one.APIVersion, Kind: one.Kind, Namespace: namespace,
				Name: one.Name, Body: substitution.Apply([]byte(one.Body)),
			},
		})
	}
	return out
}

// viewOf is a deployment as the interface sees it.
func viewOf(record deploy.Deployment) map[string]any {
	out := map[string]any{
		"id":         record.ID,
		"project":    record.Project,
		"cluster":    record.Cluster,
		"namespace":  record.Namespace,
		"image":      record.Image,
		"workload":   record.Workload,
		"state":      record.State,
		"phase":      record.Phase,
		"reason":     record.Reason,
		"started_at": record.StartedAt,
	}
	if record.FinishedAt != nil {
		out["finished_at"] = *record.FinishedAt
	}
	return out
}

// viewsOf is a list of deployments as the interface sees them.
func viewsOf(records []deploy.Deployment) []map[string]any {
	views := make([]map[string]any, 0, len(records))
	for _, one := range records {
		views = append(views, viewOf(one))
	}
	return views
}

func boolSetting(settings map[string]any, key string) bool {
	value, _ := settings[key].(bool)
	return value
}

// errClusterNotFound is asked for by name, and the answer lists what there is.
func errClusterNotFound(name string) error {
	return clusterNotFoundError{name: name}
}

type clusterNotFoundError struct{ name string }

func (e clusterNotFoundError) Error() string {
	return "no cluster is called " + e.name + " in this project's settings"
}

// errNoNamespace says what is missing without inventing a place to deploy to.
func errNoNamespace(cluster string) error {
	return &namespaceMissingError{cluster: cluster}
}

type namespaceMissingError struct{ cluster string }

func (e *namespaceMissingError) Error() string {
	return "cluster " + e.cluster + " has no namespace and this deployment did not name one; " +
		"dogit does not guess a namespace and does not create one"
}

// progressWriter streams a deployment's progress as ndjson.
//
// One JSON object per line, and every line complete on its own, because the reader on
// the other side is a page that shows whatever has arrived so far: it has to be able
// to draw after the first line rather than after the last. It also means a caller that
// goes away mid-deployment has still been told everything that happened up to that
// point — which is the case that lost a rollout once already.
type progressWriter struct {
	writer  http.ResponseWriter
	encoder *json.Encoder
}

func newProgressWriter(w http.ResponseWriter) *progressWriter {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	// Said before anything is written: a proxy that waits for a complete body would
	// buffer the whole deployment and undo the entire point.
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return &progressWriter{writer: w, encoder: json.NewEncoder(w)}
}

// send writes one line, and flushes it.
//
// Flushed every time: a rollout of two minutes is useless to somebody if the lines
// arrive all at once at the end of it.
func (p *progressWriter) send(progress deploy.Progress) {
	_ = p.encoder.Encode(progress)
	if flusher, ok := p.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}
