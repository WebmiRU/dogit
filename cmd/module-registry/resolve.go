package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Turning a tag into a digest.
//
// A deployment rolls out a digest rather than a tag, and there is exactly one way to
// know which digest a tag means: ask the registry, now, and use what it says. The
// answer is not stored anywhere, because a stored answer goes stale the moment
// somebody re-pushes the tag — which is the event a rollback has to survive.

const digestPrefix = "sha256:"

// resolve is the core asking what a tag currently points at.
func resolve(registry *registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Project string `json:"project"`
			Image   string `json:"image"`
			Token   string `json:"token"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		name := imageNameOf(req.Image)
		if name == "" {
			http.Error(w, "no image was named", http.StatusBadRequest)
			return
		}

		// The permission is this module's whole policy, asked of the core, and the
		// same question the proxy asks before it serves a blob. Resolving is a pull:
		// somebody asking what a tag is has no more right to the answer than to the
		// image behind it, and a deploy module asking about a project's image is
		// acting for that project.
		access, err := registry.core.ask(r.Context(), req.Token, projectOf(name), "pull")
		if err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if !access.Allowed {
			http.Error(w, "not allowed to pull "+name+": "+access.Reason, http.StatusForbidden)
			return
		}

		reference, err := registry.resolveDigest(r.Context(), name, tagOf(name))
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		writeJSON(w, map[string]any{
			"image":  strings.TrimSuffix(req.Image, ":"+tagOf(name)),
			"tag":    tagOf(name),
			"digest": reference,
			"pinned": reference,
		})
	}
}

// resolveDigest asks the registry what a reference currently is.
//
// The digest comes out of the Docker-Content-Digest header rather than out of the
// body, because that header is the registry's own statement about what it just
// served. Reading a digest out of a manifest body would mean parsing content and
// deciding that our hash of it agrees — which is the registry's job, not this
// module's, and a re-serialised manifest would hash differently for no reason at
// all.
func (registry *registry) resolveDigest(ctx context.Context, name, tag string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("no image was named")
	}

	reference := tag
	if reference == "" {
		reference = "latest"
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodHead,
		fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimRight(upstreamURL, "/"), name, reference), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", acceptManifests)

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("the registry did not answer: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry answered %d for %s:%s, so there is nothing to deploy",
			response.StatusCode, name, reference)
	}

	digest := strings.TrimSpace(response.Header.Get("Docker-Content-Digest"))
	if !strings.HasPrefix(digest, digestPrefix) {
		return "", fmt.Errorf(
			"the registry did not say what digest %s:%s is, so it cannot be pinned to one",
			name, reference)
	}
	return digest, nil
}

// imageNameOf is the repository part of an image reference, without the registry host.
//
// The caller hands over whatever the pipeline recorded, which includes the host this
// registry is reached at — a name that is right for a client pushing from this
// network and wrong for the storage API, which addresses repositories only.
func imageNameOf(image string) string {
	name := strings.TrimSpace(image)
	if name == "" {
		return ""
	}
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	return name
}

// tagOf is the tag in a reference, or empty when there is none.
func tagOf(image string) string {
	name := strings.TrimSpace(image)
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	index := strings.LastIndex(name, ":")
	if index <= 0 {
		return ""
	}
	return name[index+1:]
}

// projectOf is which project an image belongs to: the first path element.
//
// The registry's naming rule is its own, and the core does not know it. What the
// module can say without being told is the part before the first slash, which is the
// convention every image name in the registry already follows.
func projectOf(name string) string {
	if index := strings.Index(name, "/"); index > 0 {
		return name[:index]
	}
	return name
}
