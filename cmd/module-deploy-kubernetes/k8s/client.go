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
	"fmt"

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

	// Delete removes an object. Used for the Jobs afterwards, which are the only thing
	// here dogit removes: everything else in the manifest was meant to be there.
	Delete(ctx context.Context, ref Object) error

	// Rollout reports how a workload is getting on.
	Rollout(ctx context.Context, namespace, name string) (Rollout, error)

	// Rollback returns a workload to the revision before the current one.
	//
	// The cluster keeps a Deployment's previous pod templates as ReplicaSets, so this
	// is "apply the previous template again" rather than an undo of anything: it does
	// not touch a database, a ConfigMap or anything else that was applied alongside.
	Rollback(ctx context.Context, namespace, name string) (Rollout, error)

	// Revisions lists the history of a workload, newest first.
	Revisions(ctx context.Context, namespace, name string) ([]Revision, error)
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
	// Kubeconfig is a path to a kubeconfig file. Used when the module runs outside the
	// cluster.
	Kubeconfig string
	// Context names which context in that file to use. Empty means the current one.
	Context string
	// InCluster asks for the ServiceAccount of the pod this is running in.
	InCluster bool
}

func restConfig(access Access) (*rest.Config, error) {
	switch {
	case access.InCluster:
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf(
				"this module is set to use the cluster it runs in, and it is not running in one: %w", err)
		}
		return config, nil

	case access.Kubeconfig != "":
		rules := &clientcmd.ClientConfigLoadingRules{ExplicitPath: access.Kubeconfig}
		overrides := &clientcmd.ConfigOverrides{}
		if access.Context != "" {
			overrides.CurrentContext = access.Context
		}
		config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", access.Kubeconfig, err)
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
func (c *clusterClient) Rollback(ctx context.Context, namespace, name string) (Rollout, error) {
	deployments := c.typed.AppsV1().Deployments(namespace)
	deployment, err := deployments.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return Rollout{}, fmt.Errorf("read deployment %s: %w", name, err)
	}

	// Asked of the cluster rather than worked out here: the cluster is what keeps the
	// history, and a list of previous pod templates assembled by hand is a second
	// source of truth about a cluster, which is the thing this whole design avoids.
	previous := int64(0)
	replicaSets, err := c.typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return Rollout{}, fmt.Errorf("read the history of %s: %w", name, err)
	}

	for _, rs := range replicaSets.Items {
		if !belongsTo(&rs, name) {
			continue
		}
		revision := revisionOf(&rs)
		if revision >= deploymentRevision(deployment) {
			continue
		}
		if previous == 0 || revision > previous {
			previous = revision
		}
	}
	if previous == 0 {
		return Rollout{}, fmt.Errorf(
			"there is no earlier revision of %s to go back to", name)
	}

	if _, err := c.typed.AppsV1().Deployments(namespace).Update(ctx,
		rollbackTo(deployment, previous), metav1.UpdateOptions{}); err != nil {
		return Rollout{}, fmt.Errorf("roll %s back to revision %d: %w", name, previous, err)
	}

	return c.Rollout(ctx, namespace, name)
}

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
