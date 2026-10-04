package k8s

import (
	"fmt"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	runtimeserializer "k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	sigsyaml "sigs.k8s.io/yaml"
)

// yamlUnmarshal reads a manifest.
//
// The YAML-to-JSON path is the one every Kubernetes tool uses: the API server speaks
// JSON, and a manifest that has been through yaml.v2→JSON is what
// `kubectl apply` sends. Anything else and a manifest with a timestamp in it lands as a
// string where a field wants a date.
func yamlUnmarshal(body []byte, into any) error {
	return yaml.Unmarshal(body, into)
}

// negotiator is what the discovery client speaks with.
//
// An empty scheme: discovery only needs to read what the cluster says, and a scheme
// that knew every built-in type would be a second, wrong answer to "what is this".
func negotiator() runtime.NegotiatedSerializer {
	return runtimeserializer.NewCodecFactory(runtime.NewScheme()).WithoutConversion()
}

// fieldManager is the name this shows up under in a cluster's audit and in the
// annotation Kubernetes writes on everything we apply.
//
// Not dogit-the-module and not dogit-the-page: one name, so that "who changed this"
// has one answer in every place that keeps it.
const fieldManager = "dogit"

// resourceFor works out which resource an object belongs to, by asking the cluster.
//
// A table of kinds we remembered would be shorter and would be wrong within a year.
// The cluster already knows, and the answer for an unknown kind is an error naming the
// kind rather than a silent nothing.
func (c *clusterClient) resourceFor(object *unstructured.Unstructured) (dynamic.NamespaceableResourceInterface, error) {
	gv, err := schema.ParseGroupVersion(object.GetAPIVersion())
	if err != nil {
		return nil, fmt.Errorf("%s has an apiVersion this does not understand: %q",
			object.GetKind(), object.GetAPIVersion())
	}

	client, err := c.mapper()
	if err != nil {
		return nil, err
	}

	mapping, err := client.RESTMapping(gv.WithKind(object.GetKind()).GroupKind(), gv.Version)
	if err != nil {
		return nil, fmt.Errorf("this cluster has no %s %s: %w",
			gv.Version, object.GetKind(), err)
	}

	return c.dynamic.Resource(mapping.Resource), nil
}

// mapper is the discovery-backed kind-to-resource lookup, built once and kept.
//
// Rebuilt on failure rather than cached forever: a cluster that gained a CRD after
// this module started should not need a restart to apply it.
func (c *clusterClient) mapper() (meta.RESTMapper, error) {
	if c.restMapper != nil {
		return c.restMapper, nil
	}

	disco, err := discovery.NewDiscoveryClientForConfig(c.restCfg)
	if err != nil {
		return nil, fmt.Errorf("ask the cluster what it supports: %w", err)
	}

	cached := memory.NewMemCacheClient(disco)
	c.restMapper = restmapper.NewDeferredDiscoveryRESTMapper(cached)
	return c.restMapper, nil
}

// belongsTo says whether a ReplicaSet belongs to this Deployment.
//
// Asked by the Deployment's own label, which is how the cluster links them: guessing
// by name or by age would eventually roll back a Deployment into somebody else's
// pods.
func belongsTo(rs *appsv1.ReplicaSet, deployment string) bool {
	return rs.Labels["app.kubernetes.io/instance"] == deployment ||
		rs.Labels["app"] == deployment
}

// revisionOf is the revision number the cluster recorded on a ReplicaSet.
func revisionOf(rs *appsv1.ReplicaSet) int64 {
	value, ok := rs.Annotations["deployment.kubernetes.io/revision"]
	if !ok {
		return 0
	}
	var revision int64
	if _, err := fmt.Sscanf(value, "%d", &revision); err != nil {
		return 0
	}
	return revision
}

// deploymentRevision is the revision a Deployment is on now.
func deploymentRevision(d *appsv1.Deployment) int64 {
	value, ok := d.Annotations["deployment.kubernetes.io/revision"]
	if !ok {
		return 0
	}
	var revision int64
	if _, err := fmt.Sscanf(value, "%d", &revision); err != nil {
		return 0
	}
	return revision
}

// rollbackTo is the Deployment with an earlier pod template.
//
// The previous template is copied rather than reconstructed: the cluster has it, and
// a rollback that rebuilt the template from anything else would be a rollback to
// something nobody ran.
func rollbackTo(d *appsv1.Deployment, revision int64) *appsv1.Deployment {
	back := d.DeepCopy()
	if back.Annotations == nil {
		back.Annotations = map[string]string{}
	}
	back.Annotations["deployment.kubernetes.io/revision"] = fmt.Sprint(revision)
	back.Annotations["dogit.dev/rolled-back-to"] = fmt.Sprint(revision)
	return back
}

// imageOf is the first image of the first container, which is what a history line shows.
//
// One, because a line of history that lists four images is a line nobody reads. The
// first container of a Pod is the one a person means by "the image" anyway.
func imageOf(containers []corev1.Container) string {
	if len(containers) == 0 {
		return ""
	}
	return containers[0].Image
}

// sortRevisions puts the newest first, which is the order history is read in.
func sortRevisions(revisions []Revision) {
	sort.Slice(revisions, func(i, j int) bool {
		return revisions[i].Revision > revisions[j].Revision
	})
}

// Substituted is the result of replacing the image placeholder in a manifest.
//
// The substitution is the whole of what dogit changes in what a repository wrote, and
// it is deliberately crude: find the token, put the image there, leave everything else
// exactly as it was. A module that started rearranging a manifest would be a module
// that can apply something nobody wrote.
type Substitution struct {
	// Placeholder is the token a manifest leaves where the image goes.
	Placeholder string
	// Image is what goes there, already carrying a digest.
	Image string
}

// Apply replaces the placeholder wherever it appears, in every container of every kind
// in the manifest.
//
// Everywhere, and not only in the first container: an init container left on a floating
// tag is a migration from some build or other, and a rollback that changes the
// application container but not the init container rolls back half of what ran.
func (s Substitution) Apply(body []byte) []byte {
	if s.Placeholder == "" || s.Image == "" {
		return body
	}
	return []byte(strings.ReplaceAll(string(body), s.Placeholder, s.Image))
}

// ImagesIn lists the image references a manifest ends up with, so they can be resolved
// to digests and reported before anything is applied.
func ImagesIn(body []byte) []string {
	found := []string{}
	seen := map[string]bool{}

	for _, line := range strings.Split(string(body), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "image:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
		value = strings.Trim(value, `"'`)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		found = append(found, value)
	}
	return found
}

// podSpecPath is where the pod template lives in a manifest, by kind.
//
// Every one of these is a pod spec reached through a different route, and a manifest
// that gets it wrong gets no pull secret at all — which looks exactly like a registry
// that will not serve the image, and sends whoever is reading the log to the wrong
// place entirely.
var podSpecPaths = map[string][]string{
	"Deployment":  {"spec", "template", "spec"},
	"StatefulSet": {"spec", "template", "spec"},
	"DaemonSet":   {"spec", "template", "spec"},
	"ReplicaSet":  {"spec", "template", "spec"},
	"Job":         {"spec", "template", "spec"},
	// A CronJob keeps its pod three levels further down, and a manifest that stops one
	// level early leaves every job it creates with no pull secret at all.
	"CronJob": {"spec", "jobTemplate", "spec", "template", "spec"},
	"Pod":     {"spec"},
}

// WithPullSecret puts a pull secret on the pod template of a manifest.
//
// Only when the manifest actually has a pod template: a Service or a ConfigMap is not
// left alone, because writing a field that does not belong on a kind is how an apply
// fails with a message about a field nobody mentioned.
func WithPullSecret(body []byte, kind, secretName string) []byte {
	if secretName == "" {
		return body
	}

	// sigs.k8s.io/yaml rather than the apimachinery one: that one converts to JSON,
	// and a manifest written back as JSON is a different file from the one the
	// repository holds — comments gone, quoting changed, anchors resolved.
	var object map[string]any
	if err := sigsyaml.Unmarshal(body, &object); err != nil {
		return body
	}

	fields, isWorkload := podSpecPaths[kind]
	if !isWorkload {
		return body
	}

	target := object
	for _, path := range fields {
		next, ok := target[path].(map[string]any)
		if !ok {
			return body
		}
		target = next
	}

	if !setPullSecret(target, secretName) {
		return body
	}
	return reMarshal(object, body)
}

// setPullSecret adds the secret to a pod spec, without dropping the ones already
// there.
//
// Added rather than replaced: somebody who wrote a secret of their own into the
// repository meant it, and dogit removing it would break a deployment it had no part
// in. A secret of the same name is replaced, because ours is the one that matters.
func setPullSecret(spec map[string]any, secretName string) bool {
	existing, _ := spec["imagePullSecrets"].([]any)

	already := false
	kept := make([]any, 0, len(existing)+1)
	for _, one := range existing {
		entry, ok := one.(map[string]any)
		if ok && entry["name"] == secretName {
			// Ours, and the only one that can be ours: keep it where it was, so a
			// manifest that already asks for the secret comes back byte for byte.
			if !already {
				kept = append(kept, one)
			}
			already = true
			continue
		}
		kept = append(kept, one)
	}

	if already {
		return len(kept) != len(existing)
	}
	spec["imagePullSecrets"] = append(kept, map[string]any{"name": secretName})
	return true
}

// reMarshal is the object back as YAML, or the original bytes if it will not go.
//
// The original on failure, because a manifest that cannot be written back is one that
// cannot be deployed at all, and applying it as it was written is better than applying
// nothing with an error about YAML.
//
// Note what this costs: the round trip through a Go map rewrites the document, so a
// manifest that gains a pull secret loses its comments and its quoting. Only the
// manifests that actually change are rewritten — one that already asks for the secret
// is returned byte for byte — and the alternative, editing the YAML as text, is a
// parser that will be wrong in a way nothing reports.
func reMarshal(object map[string]any, original []byte) []byte {
	encoded, err := sigsyaml.Marshal(object)
	if err != nil {
		return original
	}
	return encoded
}
