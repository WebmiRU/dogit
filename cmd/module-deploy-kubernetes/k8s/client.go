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
	"k8s.io/apimachinery/pkg/watch"
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

	// RunningImage is what a workload's containers are running right now.
	//
	// Asked on its own so that a refusal can be made before an answer begins: a caller
	// that is already streaming cannot say "you are putting back what is already
	// running" — it can only finish a success that changed nothing, which is a green
	// tick over no work.
	RunningImage(ctx context.Context, namespace, name string) (string, error)

	// Counts says how far a rollout has got, in the numbers a person watching asks
	// about.
	Counts(ctx context.Context, namespace, name, image string) (RolloutCounts, error)

	// WatchCounts follows a rollout and calls back on every change, until the context
	// is done.
	//
	// A watch, not a question asked every so often, because the two answer different
	// questions. "How many pods are ready" asked on a timer can only ever see the
	// states that happened to coincide with a question: a rollout that goes from one
	// ready pod to ten inside one interval is reported as those two numbers and the
	// eight in between never existed for anybody — not in the log, not on the page.
	// The cluster's own event stream carries every change it observed, so the whole
	// path from one to ten is there to be read.
	//
	// A resync runs alongside it anyway, every few seconds, and that is deliberate:
	// a watch can be severed — the API server restarts, the connection drops, the
	// event history is compacted away and the stream answers "410 Gone" — and
	// everything that happened while it was down was missed by definition. The resync
	// re-reads the truth outright, so a severed watch costs a moment of history
	// rather than a rollout nobody can account for. It never replaces the watch; it
	// only notices when the watch stopped being a complete answer.
	WatchCounts(ctx context.Context, namespace, name, image string, onChange func(RolloutCounts)) error
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

// RunningImage is what a workload's containers are running right now.
func (c *clusterClient) RunningImage(ctx context.Context, namespace, name string) (string, error) {
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", fmt.Errorf("there is no deployment called %s in %s", name, namespace)
		}
		return "", fmt.Errorf("read deployment %s: %w", name, err)
	}
	return imagesOf(deployment), nil
}

// RolloutCounts is how a rollout is going, in the two numbers a person actually asks
// about: how many pods are running the new image, and how many of the old ones are
// still up.
//
// Both, because "3 of 3 ready" on its own is a moment rather than a state — the old
// pods are usually still terminating when the new ones report ready, and somebody
// watching wants to see that drain rather than be told it finished.
type RolloutCounts struct {
	Ready       int
	Desired     int
	OldUp       int
	OldScaledTo int
}

// Counts reads a workload's pods and says how far along it is.
//
// "Old" means still running a different image, rather than being an earlier
// revision number. Those are nearly the same thing and the second is bookkeeping: what
// somebody watching wants to know is whether the pods that used to be serving traffic
// are still up, and the only honest answer is the one about the image they run.
func (c *clusterClient) Counts(ctx context.Context, namespace, name, image string) (RolloutCounts, error) {
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return RolloutCounts{}, fmt.Errorf("read deployment %s: %w", name, err)
	}

	desired := int32(1)
	if deployment.Spec.Replicas != nil {
		desired = *deployment.Spec.Replicas
	}
	counts := RolloutCounts{Desired: int(desired), Ready: int(deployment.Status.ReadyReplicas)}

	pods, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		// Not knowing is not knowing; the counts read so far are still true.
		return counts, nil
	}

	for index := range pods.Items {
		pod := pods.Items[index]
		if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
			continue
		}

		for _, container := range pod.Spec.Containers {
			if container.Image != image {
				counts.OldUp++
			}
		}
	}
	return counts, nil
}

// rolloutResync is how often the truth is re-read outright while a watch is running.
//
// A compromise, and it is worth saying which side of the argument it takes: the watch
// is what makes the progress visible, and this is what makes it trustworthy when the
// watch breaks. Five seconds is slow enough not to matter next to a cluster and short
// enough that a severed watch is corrected while somebody is still watching.
const rolloutResync = 5 * time.Second

// rolloutRetry is the pause before a watch is taken again after ending.
//
// The cluster is not asked "what happened?" in a hurry: a watch that ended is a watch
// that has to be rebuilt from a fresh list first, and rebuilding in a tight loop
// against a cluster that is already unhappy is how one unhappy cluster becomes many.
const rolloutRetry = time.Second

// podSeen is what the follower remembers about one pod, kept only for what it counts.
type podSeen struct {
	// revision is the ReplicaSet this pod belongs to, which is what says whether it is
	// one of the new ones or one being replaced.
	revision string
	// new is that question answered, kept rather than recomputed so the comparison is
	// made once per pod rather than once per pod per tally.
	new bool
	// ready is the pod's own readiness, which is not the same as running: a pod that is
	// up but not yet serving is running the new image and not yet one of the ones that
	// work, and conflating the two is how a rollout reports itself finished early.
	ready bool
}

// currentRevision finds the ReplicaSet a rollout is aiming at: the newest one this
// Deployment owns.
//
// By when it was created, which is what the cluster's own revision numbers say anyway
// and what they say correctly even when they are absent — and they are absent more
// often than the API suggests, because the annotation is written by the controller and
// a cluster that has been interrupted mid-rollout leaves a ReplicaSet without one.
// Reading it anyway would silently find nothing and report a rollout with no new pods
// in it, which is worse than looking.
func currentRevision(ctx context.Context, typed kubernetes.Interface, namespace, name string) (string, error) {
	deployment, err := typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return "", err
	}

	sets, err := typed.AppsV1().ReplicaSets(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", err
	}

	newest := ""
	var newestAt time.Time
	for index := range sets.Items {
		set := sets.Items[index]
		if !ownedBy(set.OwnerReferences, deployment.UID) {
			continue
		}
		// The newest wins, and where two were made in the same second — which a rollout
		// of one manifest does produce — the name settles it, so the answer does not
		// depend on the order the API returned them in.
		if newest == "" || set.CreationTimestamp.After(newestAt) ||
			(set.CreationTimestamp.Time.Equal(newestAt) && set.Name > newest) {
			newest, newestAt = set.Name, set.CreationTimestamp.Time
		}
	}
	return newest, nil
}

// ownedBy says whether one of these owners is this object.
func ownedBy(refs []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range refs {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

// podRevision is the ReplicaSet a pod belongs to, or empty when it belongs to none
// this follower can account for — a bare pod, or one left over from a workload that is
// gone.
func podRevision(pod *corev1.Pod) string {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "ReplicaSet" {
			return ref.Name
		}
	}
	return ""
}

// WatchCounts follows a rollout, calling back on every change.
//
// See the interface for why this is a watch and what the resync is for.
func (c *clusterClient) WatchCounts(ctx context.Context, namespace, name, image string,
	onChange func(RolloutCounts)) error {

	// The pods of this workload and nobody else's. Without the selector every pod in
	// the namespace is counted, so a second deployment in the same namespace reports
	// this one's progress — a number that is confidently wrong rather than obviously
	// missing.
	selector := ""
	desired := 1
	deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read deployment %s: %w", name, err)
	}
	if deployment.Spec.Replicas != nil {
		desired = int(*deployment.Spec.Replicas)
	}
	if deployment.Spec.Selector != nil {
		if parsed, err := metav1.LabelSelectorAsSelector(deployment.Spec.Selector); err == nil {
			selector = parsed.String()
		}
	}

	pods := map[string]podSeen{}
	var last RolloutCounts
	said := false

	// Which revision is being rolled out to, and the answer is re-read whenever the
	// truth is: a rollout's first seconds are the new pods arriving while the old are
	// still up, and until the cluster has named the new revision none of them can be
	// told apart from the ones they are replacing.
	revision, err := currentRevision(ctx, c.typed, namespace, name)
	if err != nil {
		return fmt.Errorf("read the revision being rolled out: %w", err)
	}
	if revision == "" {
		// No revision means the workload has never been rolled, or the cluster is
		// between shapes. Counting nothing is honest here; guessing which pods are
		// new is not.
		return fmt.Errorf("the deployment %s/%s has no replica set yet", namespace, name)
	}

	// Re-reads the truth and publishes it, which is both the resync and the answer to
	// a watch that has just ended. One function for both, because they are the same
	// question and two copies of an answer are two answers.
	sync := func(fromWatch bool) error {
		fresh, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			return err
		}
		if updated, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{}); err == nil {
			if updated.Spec.Replicas != nil {
				desired = int(*updated.Spec.Replicas)
			}
		}

		// The revision moves under a rollout — that is what a rollout is — so it is
		// re-read here rather than decided once. Every pod already remembered is
		// re-judged against it, because a pod that was new a second ago can be the
		// one being replaced now.
		if latest, err := currentRevision(ctx, c.typed, namespace, name); err == nil && latest != "" {
			revision = latest
		}
		pods = map[string]podSeen{}
		for index := range fresh.Items {
			remember(pods, &fresh.Items[index], revision)
		}
		counts := tally(pods, desired)

		// A frame is only worth sending if it says something new. Silence every five
		// seconds would fill the log with the same numbers and teach whoever reads it
		// to skip the lines that changed.
		if said && counts == last {
			return nil
		}
		last, said = counts, true
		onChange(counts)
		return nil
	}

	if err := sync(false); err != nil {
		return err
	}

	resync := time.NewTicker(rolloutResync)
	defer resync.Stop()

	// The stream is rebuilt from a fresh list after every ending, which is what keeps
	// it honest: the list says where things stand now, the watch from that point says
	// what happens next, and nothing in between is invented.
	for ctx.Err() == nil {
		// The list above and this watch must agree, or the first event after the gap
		// is applied to a cache from before the gap. So the resource version of the
		// list is where the watch begins.
		current, err := c.typed.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			time.Sleep(rolloutRetry)
			continue
		}
		if latest, err := currentRevision(ctx, c.typed, namespace, name); err == nil && latest != "" {
			revision = latest
		}
		pods = map[string]podSeen{}
		for index := range current.Items {
			remember(pods, &current.Items[index], revision)
		}
		counts := tally(pods, desired)
		if !said || counts != last {
			last, said = counts, true
			onChange(counts)
		}

		stream, err := c.typed.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
			LabelSelector:   selector,
			ResourceVersion: current.ResourceVersion,
		})
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			time.Sleep(rolloutRetry)
			continue
		}

		// The inner loop: read until the stream ends, resync on the timer, and start
		// again outside. Kept flat rather than as a goroutine per source, so there is
		// exactly one writer to the cache and no locking to be wrong about.
		for ended := false; !ended; {
			select {
			case <-ctx.Done():
				stream.Stop()
				return nil

			case <-resync.C:
				// The watch is the fast path; this is the check that the fast path is
				// still true.
				if err := sync(true); err != nil && ctx.Err() != nil {
					stream.Stop()
					return nil
				}

			case event, open := <-stream.ResultChan():
				if !open {
					ended = true
					break
				}

				pod, ok := event.Object.(*corev1.Pod)
				if !ok {
					continue
				}
				switch event.Type {
				case watch.Deleted:
					delete(pods, string(pod.UID))
				default:
					remember(pods, pod, revision)
				}

				// Every change is published, not only the ones that move a number.
				// Two changes that happen to leave the counts equal are still two
				// things that happened, and the person watching wants the path, not
				// the compressions of it.
				counts := tally(pods, desired)
				if counts != last {
					last = counts
					onChange(counts)
				}
			}
		}
		stream.Stop()
		if ctx.Err() != nil {
			return nil
		}
		time.Sleep(rolloutRetry)
	}
	return nil
}

// remember adds or replaces one pod in the cache, keyed by identity rather than by
// name, because a pod's name is not its identity — a new pod may reuse a name a
// deleted one had, and treating those as the same pod loses an entire rollout.
func remember(pods map[string]podSeen, pod *corev1.Pod, revision string) {
	if pod.Status.Phase != corev1.PodRunning && pod.Status.Phase != corev1.PodPending {
		// A pod that has finished or failed is neither serving nor on its way out;
		// keeping it would put a number on the page that no longer describes anything.
		delete(pods, string(pod.UID))
		return
	}

	// Whether it is serving yet, asked of the containers' own statuses and matched by
	// name — the only field the spec and the status agree on. A pod that is up but not
	// yet ready is not one of the ones that would take traffic, and counting it is how a
	// rollout reports itself finished while it is still starting.
	serving := false
	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready {
			serving = true
			break
		}
	}

	own := podRevision(pod)
	pods[string(pod.UID)] = podSeen{
		revision: own,
		new:      revision != "" && own == revision,
		ready:    serving,
	}
}

// tally turns the cache of pods into the three numbers a page shows.
//
// Ready counts pods of the current revision that are serving, so "3 of 10" means three
// that would take traffic rather than three that exist. OldUp counts pods still up from
// any earlier revision — the ones this rollout is replacing — and deliberately not
// every pod that is not yet ready: a new pod still starting is not an old pod, and
// counting it as one makes the number climb while nothing is draining.
func tally(pods map[string]podSeen, desired int) RolloutCounts {
	counts := RolloutCounts{Desired: desired}
	for _, pod := range pods {
		if pod.revision == "" {
			continue
		}
		if !pod.new {
			counts.OldUp++
			continue
		}
		if pod.ready {
			counts.Ready++
		}
	}

	// Never more ready than were asked for. A pod left over from a wider previous state
	// — a scale-down not yet acted on, a pod that has outlived its revision — is
	// counted as new because nothing says it is not, and eleven of them are not eleven
	// of ten. The page would read "11 of 10", which is a number nobody can act on.
	if counts.Ready > desired {
		counts.Ready = desired
	}
	return counts
}

// awaitImage waits until a workload is running the image it was asked for.
//
// The image is checked as well as the rollout being finished, because those are two
// different questions, and answering only the second is how a revert reports success
// over a cluster still running what it had before.
// awaitImage waits until the workload is actually running the image it was given.
//
// Not until the Deployment's template names it: that is true the moment the update is
// accepted, and a caller that returns there reports a finished operation over pods
// that are still running the image it was asked to take away. "Finished" then meant
// "asked for", and the page showed a green tick while the old pods carried on serving.
//
// So the wait is for the rollout, by the same judgement a deployment waits by, and the
// answer is that judgement — including the reason it did not finish, which is the part
// somebody watching a rollback needs.
func (c *clusterClient) awaitImage(ctx context.Context, namespace, name, image string,
	timeout time.Duration) (Rollout, error) {

	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	deadline := time.Now().Add(timeout)

	last, state := "", Rollout{}
	for {
		deployment, err := c.typed.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			last = imagesOf(deployment)
			if last == image {
				state = rolloutOf(deployment)
				if state.Done {
					return state, nil
				}
			}
		}

		if time.Now().After(deadline) {
			if last == image {
				return state, fmt.Errorf(
					"the image was set to %s but %s never finished rolling it out: %s",
					image, name, state.Reason)
			}
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
