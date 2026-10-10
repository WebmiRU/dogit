package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
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

	// Tags are the names that image was published under. Recorded with the deployment
	// rather than asked of the registry when somebody asks, because a tag can be moved
	// and the answer to "what was this called" changes with it.
	Tags   []string `json:"tags,omitempty"`
	Place  string   `json:"place,omitempty"`
	Commit string   `json:"commit,omitempty"`

	Manifests []struct {
		APIVersion string `json:"api_version"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Body       string `json:"body"`
	} `json:"manifests"`

	// Preface is what the image's own run did before this deployment began, as lines
	// under the phases the plan gives them. Recorded with the deployment rather than
	// said again to whoever was watching the build: they were there, and a record read
	// next week was not.
	Preface []deploy.LogLine `json:"preface,omitempty"`

	Pre  []jobRequest `json:"pre"`
	Post []jobRequest `json:"post"`

	// Rollout names the workload to wait for, and WaitForRollout says whether to wait.
	// Registry is the credential the cluster pulls with, when it will not serve the
	// image on its own. Nil means the registry is open to the cluster and there is
	// nothing to write.
	Registry *deployRegistry `json:"registry"`

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

	// Image and Command are the other spelling of a one-shot job: a command to run from an
	// image, rather than a manifest to apply. Both are documented in the pipeline
	// configuration, the core reads both and builds both, and without these two fields the
	// command arrived here with nowhere to go: the body was empty, the kind was empty, and
	// the cluster refused it with "no matches for kind \"\" in version \"\"" — which is the
	// only thing anybody learns about a form that is written down and does not work.
	Image   string   `json:"image"`
	Command []string `json:"command"`
}

// deployRegistry is what the core tells this module about the registry a place pulls from:
// the address, and the credential for it when there is one.
//
// A token goes in the password field with any username, which is the Docker convention
// every client follows; a registry that issues neither is reached with its own login, which
// is what the core sends when the address came from the list of registries rather than from
// this instance.
type deployRegistry struct {
	Address    string `json:"address"`
	Token      string `json:"token"`
	Username   string `json:"username"`
	SecretName string `json:"secret_name"`
	// InsecureTLS is what the registry on the list says about its own certificate. It is
	// written into a pull secret as a kubelet flag rather than into the secret, and it is
	// read here for the same reason: this module asks the registry whether it still serves
	// an image, and it has to ask over the connection the cluster would use rather than
	// over one that verifies where the pull would not.
	InsecureTLS bool `json:"insecure_tls,omitempty"`
}

// pullSecretOf is the Secret to write into the namespace, or nil when there is nothing to
// write: a public mirror needs no credential, and a secret built for an address nobody
// recognised would be a secret for nothing.
func pullSecretOf(registry *deployRegistry) *k8s.PullSecret {
	if registry == nil || registry.Token == "" {
		return nil
	}
	return &k8s.PullSecret{
		Name:     pullSecretName(registry.SecretName),
		Address:  registry.Address,
		Token:    registry.Token,
		Username: registry.Username,
	}
}

// registryAddressOf is the address to fetch an image from, empty when the core named none —
// which means "the registry this image already carries", and so the record is used as it
// stands. The core refuses a place that has chosen no registry rather than sending this, so
// what arrives here is a case that should not happen; it is handled anyway, because a module
// that assumed the core is always right is a module that fails the long way round when it
// is not.
func registryAddressOf(registry *deployRegistry) string {
	if registry == nil {
		return ""
	}
	return registry.Address
}

// placeIsFree answers whether a deployment may start here, and clears what can be
// cleared first.
//
// The order is the whole of it. A record left behind by a module that died holds its
// place until something frees it, and this is the only code positioned to: Begin, which
// also knows how to free one, is reached only after a deployment has been accepted. So a
// refusal here went out without ever consulting the thing that could have prevented it,
// and a place whose deployment ended two hours ago could not be deployed to again — the
// page said a deployment was under way, and there was no deployment.
//
// Reclaiming first and asking second costs one indexed update. Asking first costs a
// place for ever.
func (c *coreClient) placeIsFree(ctx context.Context, project, cluster, namespace string) error {
	if err := c.history.Reclaim(ctx, project, cluster, namespace); err != nil {
		return err
	}

	running, err := c.history.Current(ctx, project, cluster, namespace)
	if err != nil {
		return err
	}
	if running != nil && running.State == deploy.StateRunning {
		return deploy.ErrBusy{
			Namespace: namespace,
			Running:   describeImage(running.Image),
		}
	}
	return nil
}

func (c *coreClient) handleDeploy(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var request deployRequest
	if err := decode(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(request.Project) == "" || strings.TrimSpace(request.Place) == "" {
		writeError(w, http.StatusBadRequest, "a deployment names a project and a place")
		return
	}

	places, err := c.placesNamed(ctx, request.Project, request.Place)
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

	// One deployment per place at a time, asked while a refusal can still be a refusal.
	//
	// The same question the rollback asks before it begins, and for the same reason: the
	// busy case is discovered when the record is written, which is after the answer has
	// already started, so the reason has nowhere to go — the caller is told the module
	// said nothing at all, over a deployment that was refused for a reason everybody
	// could have acted on. Asked first, this is a 409 with a sentence in it and nothing
	// has happened yet.
	for _, place := range places {
		namespace := strings.TrimSpace(request.Namespace)
		if namespace == "" {
			namespace = place.DefaultNamespace
		}
		if err := c.placeIsFree(ctx, request.Project, place.Name, namespace); err != nil {
			var busy deploy.ErrBusy
			if errors.As(err, &busy) {
				writeError(w, http.StatusConflict, fmt.Sprintf(
					"another deployment is under way in %s/%s (%s), so nothing was deployed here",
					place.Name, namespace, busy.Running))
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// The answer is a stream, not a value: a deployment takes minutes, and a caller
	// that hears nothing until the end is watching a spinner rather than a rollout.
	//
	// One stream for every place this deployment goes to, in the order the places were
	// written down: a configuration that names a place it holds twice is asking for two
	// rollouts, and a reader watching one stream should see both.
	stream := newProgressWriter(w)

	var last deploy.Deployment
	for index, place := range places {
		record, failed := c.deployOne(ctx, request, place, stream, index, len(places))
		last = record
		if failed == nil {
			continue
		}
		var busy deploy.ErrBusy
		if errors.As(failed, &busy) {
			// The one place this is not 200 for: a place already being deployed to was
			// never deployed to at all, so there is no record of it to report.
			writeError(w, http.StatusConflict, failed.Error())
			return
		}
		stream.send(deploy.Progress{Phase: string(record.Phase),
			Message: failed.Error(), Failed: true})
	}
	stream.send(deploy.Progress{Done: true, Message: "finished", Deployment: &last})
}

/**
 * Carries out the deployment into one place, and reports what happened.
 *
 * Split out of the handler because a deployment may name a place a project holds more
 * than once: the loop above is the fan-out, and everything in here is what happens to a
 * single place — which is what all of it used to be, in one piece.
 */
func (c *coreClient) deployOne(ctx context.Context, request deployRequest, place Cluster,
	stream *progressWriter, at, of int) (deploy.Deployment, error) {

	namespace := strings.TrimSpace(request.Namespace)
	if namespace == "" {
		namespace = place.DefaultNamespace
	}
	if strings.TrimSpace(namespace) == "" {
		return deploy.Deployment{}, errNoNamespace(place.Name)
	}

	client, err := place.Connect(ctx)
	if err != nil {
		return deploy.Deployment{}, err
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
		// looked at the wrong name would undo somebody's deployment.
		if found := deploy.WorkloadsOf(manifests); len(found) > 0 {
			workload = found[0]
		}
	}

	// The place's own answers first, and the repository's configuration over them: a
	// project that asks for a shorter wait is describing this particular deployment,
	// and asking to keep the Jobs is a claim about code that is reviewed with it.
	timeout := place.timeout()
	if request.TimeoutSeconds > 0 {
		timeout = time.Duration(request.TimeoutSeconds) * time.Second
	}
	keepJobs := place.keepJobs() || request.KeepJobs

	// With more than one place, everything said is said about which place: two rollouts
	// sharing a log with nothing to tell them apart is a log nobody can read.
	say := func(progress deploy.Progress) {
		if of > 1 {
			progress.Message = place.Name + "/" + namespace + ": " + progress.Message
		}
		stream.send(progress)
	}

	deployer := deploy.New(client, c.history, func(format string, args ...any) {
		log.Printf(format, args...)
	})

	// The credential the cluster pulls with, when the core sent one. A cluster that
	// cannot pull is a rollout that never finishes, and the message for that is a
	// timeout with no cause in it.
	//
	// A username comes with it when the registry issues tokens to nobody and wants a login
	// of its own — a registry from the list rather than the one this instance runs. Either
	// way the token goes in the password field, which is the Docker convention, so the two
	// are the same secret as far as the cluster is concerned.
	pullSecret := pullSecretOf(request.Registry)

	return deployer.Run(ctx, deploy.Request{
		Preface:        request.Preface,
		Progress:       deploy.ClosingPhases(say),
		Project:        request.Project,
		PullSecret:     pullSecret,
		Cluster:        place.Name,
		Namespace:      namespace,
		Image:          request.Image,
		Tags:           request.Tags,
		Place:          place.Name,
		Commit:         request.Commit,
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
}

/**
 * Every place a project holds under this name.
 *
 * All of them, because names need not be unique: a configuration that says
 * `place: staging` deploys into every place called staging, so two of them in two
 * namespaces is a way of saying "both of these" rather than a conflict waiting to be
 * reported. Read once and deployed to in turn, so one request cannot watch the list
 * change under it halfway through.
 */
func (c *coreClient) placesNamed(ctx context.Context, project, name string) ([]Cluster, error) {
	settings, err := c.settings(ctx, project)
	if err != nil {
		return nil, err
	}
	clusters, err := clustersOf(settings)
	if err != nil {
		return nil, err
	}

	var found []Cluster
	for _, one := range clusters {
		if one.Name == name {
			found = append(found, one)
		}
	}
	if len(found) == 0 {
		return nil, clusterGone(settings, name)
	}
	return found, nil
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
		return Cluster{}, "", nil, clusterGone(settings, name)
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
// describeImage names an image the way a sentence can carry it: the repository, and as
// much of the digest as is worth reading. A full sha256 in an error is sixty-four
// characters of nothing anybody can compare by eye.
func describeImage(image string) string {
	at := strings.Index(image, "@")
	if at < 0 {
		return image
	}
	digest := image[at+1:]
	if len(digest) > 19 {
		return image[:at] + "@" + digest[:12] + "…"
	}
	return image
}

func (c *coreClient) handleRevert(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var request struct {
		Project   string `json:"project"`
		Cluster   string `json:"cluster"`
		Namespace string `json:"namespace"`
		Workload  string `json:"workload"`
		// ID is the deployment to go back to.
		ID string `json:"deployment_id"`
		// Registry is the address this place pulls its images from, when it is not the one
		// the record names: the same image, the same digest, another name for it. Sent
		// with the credential for it, because a rollback that names an address nobody
		// wrote a secret for is a rollback that fails on a pull. The core refuses a
		// rollback for a place that has named no registry at all, so this is what a
		// rollback is normally given.
		Registry *deployRegistry `json:"registry"`
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

	// A revert is a rollout like any other, so it waits as long as this cluster waits:
	// the slow one is still slow when what is being put back is the version from
	// yesterday.
	cluster, namespace, client, err := c.clusterFor(ctx, request.Project, request.Cluster, request.Namespace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Whether this can be done at all, asked before the first line of the answer.
	//
	// Past that point the response is a story and its status is already spoken for:
	// a refusal written afterwards can only be a line in a success, and the caller
	// reads a revert that ended with nothing in it as a revert that worked.
	target, err := c.history.ByID(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := deploy.Revertable(target, request.Workload); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Whether the place is free, asked while a refusal can still be a refusal.
	//
	// One deployment per place at a time is a rule about the cluster, and a rollback
	// obeys it like anything else: it takes the place first, so a place somebody is
	// already deploying to is refused here — before this module has said anything,
	// rather than after it has rolled pods and found it had nowhere to record that it
	// did.
	running, err := c.history.Current(ctx, request.Project, request.Cluster, namespace)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if running != nil && running.State == deploy.StateRunning {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"another deployment is under way in %s/%s (%s), so nothing has been put back",
			request.Cluster, namespace, describeImage(running.Image)))
		return
	}

	// And whether it is already running the image that was asked for: setting an image
	// a workload already has changes nothing, so a rollback that reports doing it has
	// reported work it did not do.
	workload := request.Workload
	if workload == "" {
		workload = target.Workload
	}
	now, err := client.RunningImage(ctx, namespace, workload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if now == target.Image {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"%s is already running %s in %s/%s, so there is nothing to put back",
			workload, describeImage(target.Image), request.Cluster, namespace))
		return
	}

	// Whether the registry still has the version being put back, asked here for the same
	// reason everything above it is asked here: past this point the answer is a stream, and
	// a refusal inside it can only be a line in a success.
	//
	// Only "the registry says it does not have it" stops it. A registry that did not
	// answer has not said that, and a rollback refused on a timeout would make every
	// rollback on an instance dead for as long as somebody else's registry is asleep.
	if gone, at := imageGone(ctx, request.Registry, target.Image); gone != "" {
		writeError(w, http.StatusBadRequest, fmt.Sprintf(
			"%s is not in %s, so there is nothing to put back — the registry has to have "+
				"it under that digest before a workload can be moved back to it", gone, at))
		return
	}

	// A stream, like a deploy: putting a version back is a rollout too, and the pods
	// coming up one at a time is exactly what somebody watching wants to see.
	stream := newProgressWriter(w)

	deployer := deploy.New(client, c.history, func(format string, args ...any) {
		log.Printf(format, args...)
	})

	record, err := deployer.Revert(ctx, deploy.RevertRequest{
		Progress:   stream.send,
		ID:         id,
		Workload:   request.Workload,
		Namespace:  namespace,
		Timeout:    cluster.timeout(),
		Registry:   registryAddressOf(request.Registry),
		PullSecret: pullSecretOf(request.Registry),
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

// imageGone is what to tell somebody whose registry has answered that it does not have the
// version they asked for, and where it answered that — empty when it did not.
//
// The credential and the address are the ones the core resolved for this place, which is what
// makes the answer about the pull that would happen: the same registry, the same login, the
// same decision about the certificate.
//
// Nothing is asked when the core named no registry or the record's image carries neither a
// digest nor a tag: there is no name to ask about, and a rollback that cannot even say which
// version it wants is refused by the check above rather than here.
func imageGone(ctx context.Context, registry *deployRegistry, image string) (gone, at string) {
	if registry == nil {
		return "", ""
	}
	address := registryAddressOf(registry)
	if address == "" {
		return "", ""
	}
	path, reference := k8s.ImageReferenceAt(image, address)
	if path == "" || reference == "" {
		return "", ""
	}
	probe := k8s.RegistryProbe{
		Address:     address,
		Username:    registry.Username,
		Token:       registry.Token,
		InsecureTLS: registry.InsecureTLS,
	}
	if probe.Has(ctx, path, reference) != k8s.Missing {
		return "", ""
	}
	// The version as the place would name it: the repository without the address, and
	// the digest. The address is said once, as the registry — writing the same host twice
	// in one sentence makes a reader check whether there are two of them.
	return path + "@" + reference, address
}

// handleCurrent answers what is running in one place, right now.
//
// A question about the cluster rather than about this module's bookkeeping, and asked
// of the cluster because the cluster is the thing that is being asked about: a history
// says what this module last did, which is not the same as what is running — somebody
// with kubectl changes the image, a rollout is cut short, a Deployment is recreated
// with something else in it. Every one of those leaves the record confidently wrong and
// the page confidently repeating it.
//
// The tags come from the record and only when the record's own image is the one that
// is running. They are the names this module published that exact digest under, and
// they are a fact about the past: a tag moves, and the name it was published under is
// the one this place was actually given.
//
// A module of another kind answers the same question its own way — an archive over SSH
// cannot be asked, so it answers from what it wrote down and says so in `asked`. What
// it must not do is refuse the question: the page asks it because there is one answer
// it needs, and a module that cannot give it says `known: false` with the reason
// rather than an error, so the page can say "this module cannot tell you" instead of
// "the request failed".
func (c *coreClient) handleCurrent(w http.ResponseWriter, r *http.Request) {
	writeOutcome(w, c.answerCurrent(r.Context(), r.URL.Query()))
}

func (c *coreClient) answerCurrent(ctx context.Context, query url.Values) outcome {

	project := query.Get("project")
	name := strings.TrimSpace(query.Get("cluster"))
	if project == "" || name == "" {
		return outcome{status: http.StatusBadRequest, body: refusalBody("this module is asked what is running by project and cluster")}
	}
	if c.history == nil {
		return outcome{status: http.StatusServiceUnavailable, body: refusalBody(errNoHistory.Error())}
	}

	_, namespace, client, err := c.clusterFor(ctx, project, name, query.Get("namespace"))
	if err != nil {
		return outcome{status: http.StatusBadRequest, body: refusalBody(err.Error())}
	}

	// Which workload: the one asked about, or the one this module last deployed here.
	workload := strings.TrimSpace(query.Get("workload"))
	if workload == "" {
		last, err := c.history.Current(ctx, project, name, namespace)
		if err != nil {
			return outcome{status: http.StatusInternalServerError, body: refusalBody(err.Error())}
		}
		if last == nil || strings.TrimSpace(last.Workload) == "" {
			return outcome{status: http.StatusOK, body: map[string]any{
				"known": false,
				"reason": fmt.Sprintf(
					"nothing has ever been deployed to %s/%s, so there is nothing there to report",
					name, namespace),
				"asked": "this module's own record",
			}}
		}
		workload = last.Workload
	}

	image, err := client.RunningImage(ctx, namespace, workload)
	if err != nil {
		// A cluster that cannot be reached is not "unknown what is running" — it is a
		// failure to find out, and the page says so rather than drawing an empty answer.
		return outcome{status: http.StatusBadGateway, body: refusalBody(err.Error())}
	}

	answer := map[string]any{
		"known":     true,
		"image":     image,
		"workload":  workload,
		"namespace": namespace,
		"cluster":   name,
		"asked":     "the cluster",
	}
	if state, err := client.Rollout(ctx, namespace, workload); err == nil {
		answer["ready"] = state.Ready
		answer["desired"] = state.Desired
		answer["settled"] = state.Done
	}
	// What the pods are on, which during a rollout is more than one thing and is not
	// the same as what the workload is set to.
	//
	// Read from the pods rather than from the Deployment's template because the
	// template names the new image the moment the update is accepted: a page answered
	// from it marks the new image as running while every pod serving traffic is still
	// on the old one, and reports a rollout as finished before it has begun. With both
	// answers the page can say which is coming and which is going, and mark the image
	// that is being replaced as such while it is still up.
	if pods, err := client.RunningImages(ctx, namespace, workload); err == nil && len(pods) > 0 {
		on := make([]map[string]any, 0, len(pods))
		for _, one := range pods {
			entry := map[string]any{"image": one.Image, "pods": one.Pods}
			// The names for this one too: an image being taken off a place is one
			// somebody is watching go, and "retiring [75b0eb9ad0e2]" is a state they can
			// act on where a bare digest is something to look up.
			if tags, err := c.history.TagsOf(ctx, project, name, namespace, one.Image); err == nil && len(tags) > 0 {
				entry["tags"] = tags
			}
			on = append(on, entry)
		}
		answer["pods"] = on
	}
	if tags, err := c.history.TagsOf(ctx, project, name, namespace, image); err == nil && len(tags) > 0 {
		// From the whole history and not from the newest record, which after a rollback
		// is the rollback's own: it says which image went back and nothing about what
		// that image was called. So the one image whose name matters most is the one
		// that comes back nameless — "running [1f2d70a89dd0]" where the place is
		// running v88.10, and the person reading it has to look the digest up.
		answer["tags"] = tags
	}
	if last, err := c.history.Current(ctx, project, name, namespace); err == nil &&
		last != nil && last.Image == image && last.FinishedAt != nil {
		answer["since"] = last.FinishedAt
	}
	return outcome{status: http.StatusOK, body: answer}
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
	writeOutcome(w, c.answerDeployments(r.Context(), r.URL.Query()))
}

func (c *coreClient) answerDeployments(ctx context.Context, query url.Values) outcome {

	project := query.Get("project")
	name := query.Get("cluster")
	namespace := query.Get("namespace")

	// Which page, and how big. Said by the reader rather than guessed here: a control
	// on a page decides what a page is, and the database is what can afford to answer.
	page := queryInt(query, "page", 1)
	perPage := queryInt(query, "per_page", 20)
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 200 {
		perPage = 200
	}

	if project == "" {
		return outcome{status: http.StatusBadRequest, body: refusalBody("the history is asked for by project")}
	}
	if c.history == nil {
		return outcome{status: http.StatusServiceUnavailable, body: refusalBody(errNoHistory.Error())}
	}

	// No cluster named means the whole project: a page asking "what has this project
	// deployed" is not asking about one place, and making it name a cluster first
	// would mean the answer to that question is a form.
	if strings.TrimSpace(name) == "" {
		records, total, err := c.history.List(ctx, project, "", "", perPage, (page-1)*perPage)
		if err != nil {
			return outcome{status: http.StatusInternalServerError, body: refusalBody(err.Error())}
		}
		// The newest row carries its own log, because that is the one a page opened
		// between deployments asks about. The rest are a list of rows: a hundred
		// deployments' logs are a hundred answers to a question nobody has asked yet.
		if page == 1 && len(records) > 0 {
			if lines, err := c.history.LogOf(ctx, records[0].ID); err == nil {
				records[0].Log = lines
			}
		}
		return outcome{status: http.StatusOK, body: pageOf(records, total, page, perPage)}
	}

	if strings.TrimSpace(namespace) == "" {
		settings, err := c.settings(ctx, project)
		if err != nil {
			return outcome{status: http.StatusBadGateway, body: refusalBody(err.Error())}
		}
		if clusters, err := clustersOf(settings); err == nil {
			if cluster, found := find(clusters, name); found {
				namespace = cluster.DefaultNamespace
			}
		}
	}

	records, total, err := c.history.List(ctx, project, name, namespace,
		perPage, (page-1)*perPage)
	if err != nil {
		return outcome{status: http.StatusInternalServerError, body: refusalBody(err.Error())}
	}

	// The newest row carries its log, whether or not the list was narrowed to one
	// place. It used to be only in the unnarrowed case, because that was the only case
	// there was for a long while.
	if page == 1 && len(records) > 0 {
		if lines, err := c.history.LogOf(ctx, records[0].ID); err == nil {
			records[0].Log = lines
		}
	}

	return outcome{status: http.StatusOK, body: pageOf(records, total, page, perPage)}
}

// pageOf is one page of rows, and enough about the rest for a control to be honest
// about what it has not loaded.
func pageOf(records []deploy.Deployment, total, page, perPage int) map[string]any {
	shown := len(records)
	pages := 1
	if perPage > 0 {
		pages = (total + perPage - 1) / perPage
	}
	if pages < 1 {
		pages = 1
	}
	return map[string]any{
		"deployments": viewsOf(records),
		"total":       total,
		"page":        page,
		"per_page":    perPage,
		"pages":       pages,
		// True when asking for the next page would bring rows, which is what a reader
		// of the control needs and what a count alone does not say for an empty tail.
		"has_more": page*perPage < total,
		"shown":    shown,
	}
}

// queryInt is a number the reader asked for, or a default when it asked for nothing
// sensible.
// queryIntOf reads one number, falling back when it is not one.
func queryIntOf(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return value
}

// handleImages is the catalogue: every image this project has put on a place.
//
// Asked for on its own rather than read out of a page of operations. A catalogue built
// from one page is a catalogue that forgets everything older than that page, and the
// older entries are exactly the ones a rollback is chosen from.
func (c *coreClient) handleImages(w http.ResponseWriter, r *http.Request) {
	writeOutcome(w, c.answerImages(r.Context(), r.URL.Query()))
}

func (c *coreClient) answerImages(ctx context.Context, query url.Values) outcome {
	project := query.Get("project")
	if project == "" {
		return outcome{status: http.StatusBadRequest, body: refusalBody("the catalogue is asked for by project")}
	}
	if c.history == nil {
		return outcome{status: http.StatusServiceUnavailable, body: refusalBody(errNoHistory.Error())}
	}

	page := queryInt(query, "page", 1)
	perPage := queryInt(query, "per_page", 20)
	if page < 1 {
		page = 1
	}
	if perPage < 1 {
		perPage = 20
	}
	if perPage > 200 {
		perPage = 200
	}

	images, total, err := c.history.Images(ctx, project,
		query.Get("cluster"), query.Get("namespace"),
		perPage, (page-1)*perPage)
	if err != nil {
		return outcome{status: http.StatusInternalServerError, body: refusalBody(err.Error())}
	}

	pages := 1
	if perPage > 0 {
		pages = (total + perPage - 1) / perPage
	}
	if pages < 1 {
		pages = 1
	}
	return outcome{status: http.StatusOK, body: map[string]any{
		"images": images, "total": total, "page": page,
		"per_page": perPage, "pages": pages, "has_more": page*perPage < total,
	}}
}

// handleImagesAvailability asks the registry this place pulls from whether it still serves
// each of the images on a page of the catalogue.
//
// Asked of the registry rather than of anything this module remembers, because the question
// is about the registry and not about this module's history: the history says what was
// deployed here, and a registry somebody deleted last week still answers for everything this
// module has a record of. The core supplies the address and the credential because it is the
// core that resolved them for the place, and this module would otherwise have to resolve
// them a second time and could come to a different answer.
//
// Fifty is the limit and the core is expected to stay under it — a page of ten. It is here
// anyway, because a request that asks a registry about a thousand manifests at once is a
// request for a rate limit, and the answer that comes back is "unknown" for all of them.
func (c *coreClient) handleImagesAvailability(w http.ResponseWriter, r *http.Request) {
	writeOutcome(w, c.answerImagesAvailability(r.Context(), r.URL.Query(), readBody(r)))
}

func (c *coreClient) answerImagesAvailability(ctx context.Context, query url.Values, body []byte) outcome {
	var request struct {
		Registry deployRegistry `json:"registry"`
		Images   []struct {
			Path      string `json:"path"`
			Reference string `json:"reference"`
		} `json:"images"`
	}
	if err := decodeBody(body, &request); err != nil {
		return outcome{status: http.StatusBadRequest, body: refusalBody(err.Error())}
	}
	if strings.TrimSpace(request.Registry.Address) == "" {
		return outcome{status: http.StatusBadRequest, body: refusalBody("the availability of an image is asked of a registry, and no registry was named")}
	}
	if len(request.Images) > availabilityLimit {
		return outcome{status: http.StatusBadRequest, body: refusalBody(fmt.Sprintf(
			"%d images were asked about at once, and the limit is %d",
			len(request.Images), availabilityLimit))}
	}

	probe := k8s.RegistryProbe{
		Address:     request.Registry.Address,
		Username:    request.Registry.Username,
		Token:       request.Registry.Token,
		InsecureTLS: request.Registry.InsecureTLS,
	}
	images := make([]k8s.ProbedImage, 0, len(request.Images))
	for _, one := range request.Images {
		images = append(images, k8s.ProbedImage{Path: one.Path, Reference: one.Reference})
	}
	states := probe.AvailabilityOf(ctx, images)

	answers := make([]map[string]any, 0, len(images))
	for i, one := range images {
		answers = append(answers, map[string]any{
			"path": one.Path, "reference": one.Reference, "state": string(states[i]),
		})
	}
	return outcome{status: http.StatusOK, body: map[string]any{"images": answers}}
}

// availabilityLimit is how many manifests one request may ask about.
const availabilityLimit = 50

// handleTestCluster says whether a cluster can be reached, before anything is deployed
// to it.
//
// Worth having as its own question: the alternative is finding out at deploy time,
// which is the moment somebody least wants to be told that a cluster is unreachable.
func (c *coreClient) handleTestCluster(w http.ResponseWriter, r *http.Request) {
	writeOutcome(w, c.answerTestCluster(r.Context(), r.URL.Query(), readBody(r)))
}

func (c *coreClient) answerTestCluster(ctx context.Context, query url.Values, body []byte) outcome {

	var request struct {
		Project string `json:"project"`
		Cluster string `json:"cluster"`
	}
	if err := decodeBody(body, &request); err != nil {
		return outcome{status: http.StatusBadRequest, body: refusalBody(err.Error())}
	}

	settings, err := c.settings(ctx, request.Project)
	if err != nil {
		return outcome{status: http.StatusBadGateway, body: refusalBody(err.Error())}
	}
	clusters, err := clustersOf(settings)
	if err != nil {
		return outcome{status: http.StatusBadRequest, body: refusalBody(err.Error())}
	}

	cluster, found := find(clusters, request.Cluster)
	if !found {
		return outcome{status: http.StatusNotFound, body: refusalBody(clusterGone(settings, request.Cluster).Error())}
	}

	client, err := cluster.Connect(ctx)
	if err != nil {
		return outcome{status: http.StatusOK, body: map[string]any{"ok": false, "reason": err.Error()}}
	}

	// Reading the cluster is the test. A module that connected and did nothing has
	// proved the credentials work and nothing else.
	if _, err := client.Rollout(ctx, cluster.DefaultNamespace, "dogit-connection-check"); err != nil {
		// A missing Deployment is the expected answer: the point is that the cluster
		// answered at all.
		if strings.Contains(err.Error(), "no deployment called") {
			return outcome{status: http.StatusOK, body: map[string]any{"ok": true}}
		}
		return outcome{status: http.StatusOK, body: map[string]any{"ok": false, "reason": err.Error()}}
	}

	return outcome{status: http.StatusOK, body: map[string]any{"ok": true}}
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
		if one.Image != "" {
			out = append(out, deploy.Job{
				Name:   one.Name,
				Object: jobFromCommand(one, namespace),
			})
			continue
		}
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

// jobFromCommand is a one-shot job written as a command rather than as a manifest.
//
// The manifest is built here rather than by the core, because the core has no business knowing
// what shape a Kubernetes Job takes: it knows a step may be written either way, and the module is
// the one that knows what to do about it. Sending an empty kind and an empty body instead — which
// is what the command form amounted to — is the cluster being asked for something that is not
// there, and the answer names neither the module nor the configuration.
func jobFromCommand(one jobRequest, namespace string) k8s.Object {
	command := one.Command
	if len(command) == 0 {
		// A container with no command is one that runs the image's own, which is a reasonable
		// thing to ask for and is why this is not an error.
		command = nil
	}
	body := fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: step
          image: %s
          command: %s
`, one.Name, namespace, one.Image, jsonList(command))

	return k8s.Object{
		APIVersion: "batch/v1", Kind: "Job", Namespace: namespace,
		Name: one.Name, Body: []byte(body),
	}
}

// jsonList is a shell argument list as JSON, which is what a container's command is written as.
//
// By hand rather than by marshalling, so that a command containing a quote or a newline is
// escaped by the one library in the process that is trusted to escape things. A manifest built
// by string concatenation is a manifest that a repository can break with a character.
func jsonList(args []string) string {
	encoded, err := json.Marshal(args)
	if err != nil {
		return "[]"
	}
	return string(encoded)
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
		// How many pods this operation dealt with. Sent whenever there were any,
		// so a row with no rollout on it — a failed apply, a revert of something
		// that never ran — says nothing rather than saying zero of zero.
		// The names the image had when this deployment ran. Carried through rather
		// than read from the registry, which would answer for now instead of then.
		"tags":         record.Tags,
		"place":        record.Place,
		"commit":       record.Commit,
		"pods_wanted":  record.PodsWanted,
		"pods_ready":   record.PodsReady,
		"pods_retired": record.PodsRetired,
	}
	if record.FinishedAt != nil {
		out["finished_at"] = *record.FinishedAt
	}
	if len(record.Log) > 0 {
		out["log"] = record.Log
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

// errClusterOff says a cluster exists and is not in use here, which is a different
// thing from not existing and needs a different answer: somebody reading this needs to
// know they can switch it back on, not that they have mistyped a name.
func errClusterOff(name string) error {
	return clusterOffError{name: name}
}

type clusterOffError struct{ name string }

func (e clusterOffError) Error() string {
	return "the cluster " + e.name + " is switched off for this project"
}

// clusterGone says why a cluster is not one this project may deploy to: not written
// down at all, or written down and switched off. It reads the unfiltered list so that
// the two can be told apart — clustersOf has already dropped the switched-off ones.
func clusterGone(settings map[string]any, name string) error {
	if all, err := allClusters(settings); err == nil {
		if _, there := find(all, name); there {
			return errClusterOff(name)
		}
	}
	return errClusterNotFound(name)
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
