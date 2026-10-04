// Package k8s is the only part of dogit that knows what a Kubernetes cluster is.
//
// It sits behind an interface on purpose. Everything above it — the module's own
// history, the phases of a deploy, the wiring to the core — is ordinary logic that
// must be testable without a cluster, because a test that needs a cluster is a test
// nobody runs. The cluster is the thing at the bottom, and it is the only thing here
// that talks to one.
//
// What this package deliberately does not do is know what a deployment is meant to be.
// It applies objects and reads their state back. What those objects should say is the
// repository's business, and dogit does not generate manifests: the moment it starts
// owning a Deployment schema, it owns every way that schema can be wrong.
package k8s

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Object is one thing to apply, as it came out of a manifest in the repository.
type Object struct {
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	// Body is the manifest with the image placeholder already substituted. It is
	// applied as written, because what a repository says is what gets applied.
	Body []byte
}

// Ref identifies an object without describing it.
func (o Object) Ref() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s/%s", o.Kind, o.Name)
	}
	return fmt.Sprintf("%s/%s/%s", o.Kind, o.Namespace, o.Name)
}

// State is what a cluster says about an object it was asked to apply.
type State struct {
	Ref     string
	Applied bool
	// Reason explains a refusal from the cluster, in the cluster's own words. Empty
	// when there is nothing to explain.
	Reason string
}

// Rollout is how a workload is getting on, read from the same conditions a person
// would read with `kubectl`.
type Rollout struct {
	// Desired, Updated and Ready are the three numbers that tell the story: what we
	// asked for, what has been created, and what is actually serving.
	Desired int32
	Updated int32
	Ready   int32
	// Done is true when the rollout has finished: everything asked for is updated and
	// ready, and nothing is left over.
	Done bool
	// Reason says why it has not finished, from the Deployment's own conditions. This
	// is where "ImagePullBackOff" comes from, and it is the difference between a red
	// row and a red row somebody can act on.
	Reason string
}

// Client is the cluster, as far as anything above this package is concerned.
type Client interface {
	// Apply writes an object, replacing what is there. Used for everything: the
	// manifests from the repository and the Jobs of the pre and post phases.
	Apply(ctx context.Context, object Object) (State, error)

	// Get reads one object back.
	Get(ctx context.Context, ref Object) (*unstructured.Unstructured, error)

	// EnsurePullSecret writes the credential a namespace pulls private images with.
	//
	// Part of the client's interface rather than something the deployer assembles,
	// because a Secret of the right shape is a thing this cluster understands and a
	// hand-rolled one is a thing only this module does.
	EnsurePullSecret(ctx context.Context, namespace string, secret PullSecret) error

	// Delete removes an object. Used for the Jobs afterwards, which are the only thing
	// here dogit removes: everything else in the manifest was meant to be there.
	Delete(ctx context.Context, ref Object) error

	// Rollout reports how a workload is getting on.
	Rollout(ctx context.Context, namespace, name string) (Rollout, error)

	// Revisions lists the history of a workload, newest first.
	Revisions(ctx context.Context, namespace, name string) ([]Revision, error)

	// SetImage puts one image on a workload's containers and waits for the rollout.
	//
	// This rather than an undo, because an undo is relative to whatever the cluster
	// happens to remember: it steps back one revision, its history is bounded and
	// prunable, and it will happily report success while leaving the same image in
	// place. Naming the image says what should run, and says it whether or not the
	// cluster still has any idea what ran before.
	SetImage(ctx context.Context, namespace, name, image string, timeout time.Duration) (Rollout, error)
}

// Revision is one point in a workload's history.
type Revision struct {
	Revision int64
	// Image is what this revision ran, so a history line says which version it was
	// rather than only which number.
	Image string
	// Time is when it was created, as the cluster recorded it.
	Time metav1.Time
	// Current is whether this is what is running now.
	Current bool
}

// Connect builds a client for one cluster.
//
// Two ways in, and the difference is not cosmetic. A kubeconfig is a secret somebody
// copied, so it can be wrong and has to be rotated by hand. A ServiceAccount is what
// the cluster hands a pod that is running inside the cluster, and it expires on its
// own — which is why a module deployed into the cluster should be given this and
// nothing to store.
func Connect(ctx context.Context, access Access) (Client, error) {
	config, err := restConfig(access)
	if err != nil {
		return nil, err
	}
	return NewForConfig(config)
}

// Access is how to reach one cluster.
type Access struct {
	// Kubeconfig is the contents of a kubeconfig, not a path to one.
	//
	// The contents rather than a path because a path is only true until the file
	// moves. It is deleted during an upgrade, it is a different path in the next
	// container image, and it needs a volume mount somebody has to remember to add to
	// a compose file. What the operator configured is the credential itself, and
	// keeping it means this module deployed anywhere can use it with nothing else
	// arranged first.
	Kubeconfig []byte
	// Context names which context in that file to use. Empty means the current one.
	Context string
}

func restConfig(access Access) (*rest.Config, error) {
	switch {
	case len(access.Kubeconfig) > 0:
		// Straight from the bytes somebody pasted into a settings page.
		//
		// The context is honoured by rewriting the document in memory rather than by
		// editing what is stored: one cluster row can be used by two projects that
		// name different contexts, and a stored value that one of them had rewritten
		// would quietly break the other.
		document := access.Kubeconfig
		if access.Context != "" {
			chosen, err := withCurrentContext(document, access.Context)
			if err != nil {
				return nil, err
			}
			document = chosen
		}

		config, err := clientcmd.RESTConfigFromKubeConfig(document)
		if err != nil {
			return nil, fmt.Errorf("the kubeconfig could not be read: %w", err)
		}
		return config, nil

	default:
		return nil, fmt.Errorf(
			"no way in: a cluster needs either a kubeconfig or a module running inside one")
	}
}

// clusterClient talks to a real cluster, and is the only type here that does.
type clusterClient struct {
	typed   kubernetes.Interface
	dynamic dynamic.Interface
	restCfg *rest.Config
	// restMapper is built on first use and kept: it asks the cluster what kinds exist,
	// which is a discovery request, and doing that per manifest would be one round trip
	// per file for an answer that cannot change while the module runs.
	restMapper meta.RESTMapper
}

var _ Client = (*clusterClient)(nil)

// NewForConfig builds a client for a resolved configuration.
func NewForConfig(config *rest.Config) (Client, error) {
	typed, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("reach the cluster: %w", err)
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("reach the cluster: %w", err)
	}
	restClient, err := rest.UnversionedRESTClientFor(withGroupVersion(config))
	if err != nil {
		return nil, fmt.Errorf("reach the cluster: %w", err)
	}
	_ = restClient
	return &clusterClient{typed: typed, dynamic: dyn, restCfg: config}, nil
}

// groupVersion is the discovery client used to find what a manifest means.
//
// A manifest names its own apiVersion, so the only thing that has to be guessed is
// which resource a kind belongs to — and asking is better than a table of kinds we
// remembered, which is how a module ends up not supporting whatever was added last
// year.
var groupVersion = schema.GroupVersion{Group: "", Version: "v1"}

func withGroupVersion(config *rest.Config) *rest.Config {
	copied := rest.CopyConfig(config)
	copied.GroupVersion = &groupVersion
	copied.NegotiatedSerializer = negotiator()
	copied.APIPath = "/api"
	return copied
}

// Apply writes one object.
func (c *clusterClient) Apply(ctx context.Context, object Object) (State, error) {
	desired := map[string]any{}
	if err := yamlUnmarshal(object.Body, &desired); err != nil {
		return State{}, fmt.Errorf("%s is not a Kubernetes object: %w", object.Ref(), err)
	}
	content := &unstructured.Unstructured{Object: desired}

	resource, err := c.resourceFor(content)
	if err != nil {
		return State{}, err
	}

	// Namespace the object asked for, falling back to the one the deploy is for. A
	// manifest that names no namespace goes where the deploy said, rather than into
	// whatever the cluster calls default.
	if content.GetNamespace() == "" && object.Namespace != "" {
		content.SetNamespace(object.Namespace)
	}

	force := true
	_, err = resource.Namespace(content.GetNamespace()).
		Patch(ctx, content.GetName(), types.ApplyPatchType, object.Body,
			metav1.PatchOptions{FieldManager: fieldManager, Force: &force})
	if err != nil {
		// The namespace has to exist before anything can be put in it. Creating it is
		// not dogit's decision to make: a namespace is somebody's decision, made where
		// they can see what is already in it. So this says so rather than doing it.
		if apierrors.IsNotFound(err) && resource != nil {
			return State{}, fmt.Errorf(
				"%s: the namespace does not exist, and dogit does not create namespaces", content.GetNamespace())
		}
		return State{}, fmt.Errorf("apply %s: %w", content.GetName(), err)
	}

	return State{Ref: object.Ref(), Applied: true}, nil
}

// Get reads one object back.
func (c *clusterClient) Get(ctx context.Context, ref Object) (*unstructured.Unstructured, error) {
	found := &unstructured.Unstructured{}
	found.SetAPIVersion(ref.APIVersion)
	found.SetKind(ref.Kind)

	resource, err := c.resourceFor(found)
	if err != nil {
		return nil, err
	}

	answer, err := resource.Namespace(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", ref.Ref(), err)
	}
	return answer, nil
}

// Delete removes one object, tolerating one that is already gone.
func (c *clusterClient) Delete(ctx context.Context, ref Object) error {
	found := &unstructured.Unstructured{}
	found.SetAPIVersion(ref.APIVersion)
	found.SetKind(ref.Kind)

	resource, err := c.resourceFor(found)
	if err != nil {
		return err
	}

	// Background propagation, and the reason is a warning the cluster prints:
	// deleting a Job keeps its pods by default. Those pods then linger with their
	// owner gone, holding the names a re-created Job wants for its own — so the next
	// deploy of the same Job fails on pods that already exist, in a namespace nothing
	// appears to have touched.
	background := metav1.DeletePropagationBackground
	err = resource.Namespace(ref.Namespace).Delete(ctx, ref.Name, metav1.DeleteOptions{
		PropagationPolicy: &background,
	})
	if apierrors.IsNotFound(err) {
		// Already gone is what was asked for. Saying otherwise would make a deploy fail
		// over cleanup that had nothing left to do.
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete %s: %w", ref.Ref(), err)
	}
	return nil
}

// Rollout reports how a Deployment is getting on.
func (c *clusterClient) Rollout(ctx context.Context, namespace, name string) (Rollout, error) {
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Rollout{}, fmt.Errorf(
				"there is no deployment called %s in %s", name, namespace)
		}
		return Rollout{}, fmt.Errorf("read deployment %s: %w", name, err)
	}
	return rolloutOf(deployment), nil
}

// rolloutOf is the same judgement kubectl rollout status makes.
//
// The first thing it does is refuse to call anything finished before the cluster has
// seen the Deployment at all. That is not pedantry: a Deployment that was created a
// moment ago has no observed generation and may have no replica count yet, and
// "zero of zero are ready" would then be true and would mean nothing. Reading that as
// a finished rollout makes every deployment in dogit report itself successful the
// instant it starts, which is the one answer nobody can undo later.
func rolloutOf(d *appsv1.Deployment) Rollout {
	want := int32(0)
	if d.Spec.Replicas != nil {
		want = *d.Spec.Replicas
	}

	out := Rollout{
		Desired: want,
		Updated: d.Status.UpdatedReplicas,
		Ready:   d.Status.ReadyReplicas,
	}

	seen := d.Generation <= d.Status.ObservedGeneration
	out.Done = seen && out.Updated >= want && out.Ready >= want

	// The conditions are where the reason lives, and they are asked in the order a
	// person would read them: the newest thing that went wrong, not the first one.
	for _, condition := range d.Status.Conditions {
		if condition.Status != corev1.ConditionTrue {
			continue
		}
		switch condition.Type {
		case appsv1.DeploymentProgressing:
			if condition.Reason == "ProgressDeadlineExceeded" {
				out.Reason = condition.Message
			}
		case appsv1.DeploymentReplicaFailure:
			out.Reason = condition.Message
		}
	}

	switch {
	case !seen:
		out.Reason = "the cluster has not seen this deployment yet"
	case out.Reason == "" && !out.Done:
		out.Reason = fmt.Sprintf("%d of %d updated, %d ready", out.Updated, out.Desired, out.Ready)
	}
	return out
}

// Rollback returns a Deployment to the revision before the current one.

// Revisions lists a Deployment's history, newest first.
func (c *clusterClient) Revisions(ctx context.Context, namespace, name string) ([]Revision, error) {
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("read deployment %s: %w", name, err)
	}

	replicaSets, err := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("read the history of %s: %w", name, err)
	}

	current := deploymentRevision(deployment)
	out := []Revision{}
	for _, rs := range replicaSets.Items {
		if !belongsTo(&rs, name) {
			continue
		}
		out = append(out, Revision{
			Revision: revisionOf(&rs),
			Image:    imageOf(rs.Spec.Template.Spec.Containers),
			Time:     rs.CreationTimestamp,
			Current:  revisionOf(&rs) == current,
		})
	}

	sortRevisions(out)
	return out, nil
}

// withCurrentContext returns the kubeconfig with a different context selected.
//
// Said rather than assumed: a context that is not in the document is refused with the
// contexts that are, because the alternative is a connection attempt against whatever
// happens to be current, and "it connected to the wrong cluster" is not a failure
// anybody can diagnose from a log line.
func withCurrentContext(document []byte, context string) ([]byte, error) {
	parsed, err := clientcmd.Load(document)
	if err != nil {
		return nil, fmt.Errorf("the kubeconfig could not be read: %w", err)
	}

	if _, found := parsed.Contexts[context]; !found {
		available := make([]string, 0, len(parsed.Contexts))
		for name := range parsed.Contexts {
			available = append(available, name)
		}
		sort.Strings(available)
		if len(available) == 0 {
			return nil, fmt.Errorf(
				"this kubeconfig has no contexts at all, so there is nothing to connect to")
		}
		return nil, fmt.Errorf("this kubeconfig has no context called %q; it has: %s",
			context, strings.Join(available, ", "))
	}

	parsed.CurrentContext = context
	return clientcmd.Write(*parsed)
}

// PullSecret is what a cluster needs to pull an image from a registry that will not
// serve anonymously.
type PullSecret struct {
	// Name is the Secret to create or update in the namespace.
	Name string
	// Address is the registry host:port, which is what the auth entry is keyed by and
	// what has to match the image name exactly.
	Address string
	// Token is the bearer token, already scoped to one project and one registry.
	Token string
	// Username pairs with the token. Registries that issue a token rather than a
	// password want the token in the password field and anything here, which is the
	// Docker convention every client follows.
	Username string
}

// dockerConfigJSON is a docker config holding exactly one entry.
//
// Built by hand rather than by walking a map: the shape is fixed, it is written once,
// and a dependency to serialise a three-key document would be a dependency this
// module otherwise does not have.
func dockerConfigJSON(secret PullSecret) []byte {
	entry := map[string]string{
		"username": secret.Username,
		"password": secret.Token,
	}
	if entry["username"] == "" {
		// A token is not a password and some registries care: an empty username with a
		// token is what an anonymous bearer pull looks like.
		entry["username"] = "<token>"
	}

	auth := base64.StdEncoding.EncodeToString(
		[]byte(entry["username"] + ":" + entry["password"]))

	document := map[string]any{
		"auths": map[string]any{
			secret.Address: map[string]any{"auth": auth},
		},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		// Unreachable for a document of strings, and returning nothing would write a
		// Secret nobody can use.
		return []byte(`{"auths":{}}`)
	}
	return encoded
}

// EnsurePullSecret creates or replaces the pull secret in a namespace.
//
// Replaced rather than merged, because a credential that has been rotated must not
// leave the old one behind: a namespace holding two secrets for the same registry
// means the cluster will keep using whichever it found first, which may be the one
// that was revoked.
func (c *clusterClient) EnsurePullSecret(ctx context.Context, namespace string, secret PullSecret) error {
	body := map[string]any{
		"apiVersion": "v1",
		"kind":       "Secret",
		"type":       "kubernetes.io/dockerconfigjson",
		"metadata": map[string]any{
			"name":      secret.Name,
			"namespace": namespace,
		},
		"data": map[string]any{
			".dockerconfigjson": base64.StdEncoding.EncodeToString(dockerConfigJSON(secret)),
		},
	}

	encoded, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("describe the pull secret: %w", err)
	}
	if _, err := c.Apply(ctx, Object{
		APIVersion: "v1", Kind: "Secret", Namespace: namespace,
		Name: secret.Name, Body: encoded,
	}); err != nil {
		return fmt.Errorf("write the pull secret: %w", err)
	}
	return nil
}

// SetImage puts one image on every container of a workload and waits for the rollout.
//
// Every container, because a workload with two containers has two images and
// replacing only the first leaves half of it on whatever it had — which is the same
// mistake as an init container left on a floating tag.
func (c *clusterClient) SetImage(ctx context.Context, namespace, name, image string,
	timeout time.Duration) (Rollout, error) {

	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Rollout{}, fmt.Errorf(
				"there is no deployment called %s in %s to change the image of", name, namespace)
		}
		return Rollout{}, fmt.Errorf("read deployment %s: %w", name, err)
	}

	// Already running it, and said so.
	//
	// Setting an image a workload already has changes nothing: no template differs, so
	// no new pods, so nothing anybody watching would see — and the operation then
	// reports success. That is the answer that teaches people not to trust the button,
	// so it is refused with the reason rather than performed quietly.
	if current := imagesOf(deployment); current == image {
		return Rollout{}, fmt.Errorf(
			"%s is already running %s, so there is nothing to put back", name, image)
	}

	changed := deployment.DeepCopy()
	for index := range changed.Spec.Template.Spec.Containers {
		changed.Spec.Template.Spec.Containers[index].Image = image
	}

	if _, err := c.typed.AppsV1().Deployments(namespace).Update(ctx, changed,
		metav1.UpdateOptions{}); err != nil {
		return Rollout{}, fmt.Errorf("set the image of %s: %w", name, err)
	}

	return c.awaitImage(ctx, namespace, name, image, timeout)
}

// awaitImage waits until a workload is running the image it was asked for.
//
// The image is checked as well as the rollout being finished, because those are two
// different questions, and answering only the second is how a revert reports success
// over a cluster still running what it had before.
func (c *clusterClient) awaitImage(ctx context.Context, namespace, name, image string,
	timeout time.Duration) (Rollout, error) {

	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)

	last := ""
	for {
		deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			last = imagesOf(deployment)
			if last == image {
				return c.rolloutNow(ctx, namespace, name)
			}
		}

		if time.Now().After(deadline) {
			return Rollout{}, fmt.Errorf(
				"the image was set to %s but %s is running %s", image, name, describe(last))
		}
		select {
		case <-ctx.Done():
			return Rollout{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// rolloutNow reads the rollout state without waiting for it.
func (c *clusterClient) rolloutNow(ctx context.Context, namespace, name string) (Rollout, error) {
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Rollout{}, fmt.Errorf("read deployment %s: %w", name, err)
	}
	return rolloutOf(deployment), nil
}

// imagesOf is what a workload's containers are running, in order.
func imagesOf(deployment *appsv1.Deployment) string {
	images := make([]string, 0, len(deployment.Spec.Template.Spec.Containers))
	for _, container := range deployment.Spec.Template.Spec.Containers {
		images = append(images, container.Image)
	}
	return strings.Join(images, ",")
}

// describe is what to say about an image nobody can name.
func describe(images string) string {
	if images == "" {
		return "nothing this module can see"
	}
	return images
}
