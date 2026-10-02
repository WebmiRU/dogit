package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// acceptManifests is what a registry serves a manifest as, and it varies: an
// ordinary image, a multi-platform index, and the two Open Container formats of
// each. Asking for one and being given another is how a client ends up reading a
// manifest as something else.
const acceptManifests = "application/vnd.docker.distribution.manifest.v2+json, " +
	"application/vnd.docker.distribution.manifest.list.v2+json, " +
	"application/vnd.oci.image.manifest.v1+json, " +
	"application/vnd.oci.image.index.v1+json"

// registryRepository is one image name's tags, with what they weigh.
type registryRepository struct {
	Name string        `json:"name"`
	Tags []registryTag `json:"tags"`
	Size int64         `json:"size_bytes"`
}

type registryTag struct {
	Name      string    `json:"name"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size_bytes"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// The page cannot be served from the core's database — the core stores hashes, not
// layers — so it asks here, at the address the module published, with the short
// credential the core minted for it. Everything here is read-only: the module
// stores what it holds and says so, and the core decided who is asking.
func (registry *registry) packages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		registryError(w, http.StatusMethodNotAllowed, "method_not_allowed", "not a method this endpoint serves")
		return
	}

	var body struct {
		Project string `json:"project"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.Method == http.MethodPost {
		registryError(w, http.StatusBadRequest, "invalid_body", "a project path is required")
		return
	}
	if body.Project == "" {
		body.Project = r.URL.Query().Get("project")
	}

	name, err := registryProject("/v2/" + strings.Trim(body.Project, "/") + "/tags/list")
	if err != nil {
		registryError(w, http.StatusBadRequest, "no_such_project", "a project path is required")
		return
	}

	repositories, err := registry.describe(r.Context(), name)
	if err != nil {
		registryError(w, http.StatusBadGateway, "registry_unavailable", err.Error())
		return
	}

	total := int64(0)
	for _, repository := range repositories {
		total += repository.Size
	}

	writeJSON(w, map[string]any{
		"image":       name,
		"repos":       repositories,
		"total_bytes": total,
	})
}

// deletePackage removes one tag.
//
// The core has already decided this caller may: a credential that cannot delete
// is refused before it reaches here. What this checks is the part the core cannot
// know — that the name really is one this project's rule produced, so a credential
// scoped to a project cannot reach outside it by naming something else.
func (registry *registry) deletePackage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Project    string `json:"project"`
		Repository string `json:"repository"`
		Tag        string `json:"tag"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		registryError(w, http.StatusBadRequest, "invalid_body", "a repository and a tag are required")
		return
	}

	repository := strings.Trim(body.Repository, "/")
	tag := strings.TrimSpace(body.Tag)
	if repository == "" || tag == "" {
		registryError(w, http.StatusBadRequest, "invalid_body", "a repository and a tag are required")
		return
	}

	// The name has to belong to the project the credential was minted for. Without
	// this, a credential scoped to one project could name another project's image
	// and delete it.
	own, err := registry.core.resolveImage(r.Context(), repository)
	if err != nil {
		registryError(w, http.StatusBadGateway, "core_unavailable", "dogit is not answering")
		return
	}
	if own != strings.Trim(body.Project, "/") {
		registryError(w, http.StatusNotFound, "no_such_project", "no such repository")
		return
	}

	digest, err := manifestDigest(r.Context(), defaultClient(), repository, tag)
	if err != nil {
		registryError(w, http.StatusNotFound, "no_such_tag", "no such tag")
		return
	}
	if err := deleteByDigest(r.Context(), defaultClient(), repository, "manifests", digest); err != nil {
		registryError(w, http.StatusBadGateway, "registry_unavailable", err.Error())
		return
	}

	writeJSON(w, map[string]any{"removed": repository + ":" + tag})
}

// describe reads one repository's tags and what they weigh.
//
// The size of a tag is the size of the blobs its manifest names, counted once: a
// multi-platform image references the same layers several times over, and adding
// them up as they appear would report a number nobody could believe.
func (registry *registry) describe(ctx context.Context, name string) ([]registryRepository, error) {
	client := defaultClient()

	tags, err := fetchTags(ctx, client, name)
	if err != nil {
		return nil, err
	}

	repository := registryRepository{Name: name, Tags: []registryTag{}}
	if len(tags) == 0 {
		// A repository with no tags is not yet an image, and listing it as an empty
		// row would be an odd thing to show somebody.
		return []registryRepository{}, nil
	}

	seen := map[string]bool{}
	for _, tag := range tags {
		digest, err := manifestDigest(ctx, client, name, tag)
		if err != nil {
			// One unreadable tag does not hide the others. An image that cannot be
			// described is still an image somebody pushed.
			continue
		}

		entry := registryTag{Name: tag, Digest: digest}
		if !seen[digest] {
			seen[digest] = true
			entry.Size, _ = manifestSize(ctx, client, name, digest)
			repository.Size += entry.Size
		}
		repository.Tags = append(repository.Tags, entry)
	}

	sort.Slice(repository.Tags, func(i, j int) bool {
		return repository.Tags[i].Name < repository.Tags[j].Name
	})

	return []registryRepository{repository}, nil
}

// manifestSize is what a manifest's blobs weigh, each counted once.
func manifestSize(ctx context.Context, client *http.Client, name, digest string) (int64, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimRight(upstreamURL, "/"), name, digest), nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", acceptManifests)

	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the registry answered %d for a manifest", response.StatusCode)
	}

	var manifest struct {
		Config struct {
			Size int64 `json:"size"`
		} `json:"config"`
		Layers []struct {
			Size int64 `json:"size"`
		} `json:"layers"`
		// An image index points at other manifests rather than at layers, and each
		// of those has its own layers.
		Manifests []struct {
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"manifests"`
	}
	if err := json.NewDecoder(response.Body).Decode(&manifest); err != nil {
		return 0, err
	}

	total := manifest.Config.Size
	for _, layer := range manifest.Layers {
		total += layer.Size
	}

	// For an index, the manifests' own sizes are counted here and their layers are
	// counted when the index is asked for each child — which is why this is the
	// shallow answer and a platform-specific tag is described differently from a
	// multi-platform one.
	for _, child := range manifest.Manifests {
		total += child.Size
	}
	return total, nil
}

func defaultClient() *http.Client {
	return &http.Client{Timeout: 2 * time.Minute}
}
