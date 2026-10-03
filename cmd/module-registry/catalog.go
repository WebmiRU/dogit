package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// catalogEntry is one image across the whole instance, as the module knows it.
//
// The module is the only thing that can answer this: the core stores digests of
// images in passing and knows nothing about layers, tags or when anything was
// pushed. The core's job is to decide who may ask, and then to get out of the way.
type catalogEntry struct {
	// Repository is the image name, and Project the path it belongs to — the two
	// are not the same thing once a template puts a group or a branch in the name.
	Repository string `json:"repository"`
	Project    string `json:"project"`

	Tags    []catalogTag `json:"tags"`
	Size    int64        `json:"size_bytes"`
	TagsLen int          `json:"tag_count"`
}

type catalogTag struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Size   int64  `json:"size_bytes"`
	// Created is when the manifest itself says the image was built, which is not
	// when it was pushed: an image built a month ago and pushed yesterday says so,
	// and the two dates are answers to different questions.
	Created time.Time `json:"created_at,omitempty"`
}

// catalog is everything the registry holds, described.
//
// Listing means asking the registry for its repositories and then describing each
// one, which is a request per repository: there is no bulk call in the
// specification, and pretending otherwise would mean holding the whole catalogue in
// the module's memory to answer a question about one project.
func (registry *registry) catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		registryError(w, http.StatusMethodNotAllowed, "method_not_allowed", "not a method this endpoint serves")
		return
	}

	var body struct {
		// Projects narrows the listing. An administrator may leave it empty and see
		// everything, which is the point of an administrator's view.
		Projects []string `json:"projects"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && r.Method == http.MethodPost {
		registryError(w, http.StatusBadRequest, "invalid_body", "invalid body")
		return
	}

	names, err := registry.repositoryNames(r.Context(), body.Projects)
	if err != nil {
		registryError(w, http.StatusBadGateway, "registry_unavailable", err.Error())
		return
	}

	entries := make([]catalogEntry, 0, len(names))
	var total int64
	for _, name := range names {
		entry := registry.describeOne(r.Context(), name)
		if len(entry.Tags) == 0 {
			// A repository with no tags is a name somebody reserved, not an image.
			continue
		}
		total += entry.Size
		entries = append(entries, entry)
	}

	// Biggest first: an operator opening this page is usually looking for what is
	// taking the space, and the answer should not require sorting.
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Size != entries[j].Size {
			return entries[i].Size > entries[j].Size
		}
		return entries[i].Repository < entries[j].Repository
	})

	writeJSON(w, map[string]any{
		"repos":       entries,
		"total_bytes": total,
		"count":       len(entries),
		"tags":        totalTags(entries),
	})
}

// repositoryNames is what the registry holds, resolved to projects.
//
// The registry's catalogue lists names without saying who owns them; that is asked
// of the core, once per name, because only the core knows which project a name
// belongs to. A name the core cannot place is left out: it came from outside this
// installation, and attributing it to whichever project fits would hand somebody
// else the ability to delete it.
//
// Paged by following the registry's own links rather than by asking for a large
// page. This registry refuses a page size it does not like with a bare 400, and an
// installation with thousands of images would be truncated by a limit instead —
// which is worse, because the list would look complete.
func (registry *registry) repositoryNames(ctx context.Context, only []string) ([]string, error) {
	wanted := map[string]bool{}
	for _, path := range only {
		wanted[strings.Trim(path, "/")] = true
	}

	client := defaultClient()
	base := strings.TrimRight(upstreamURL, "/") + "/v2/_catalog"

	var all []string
	// Bounded, so a registry whose links never end cannot hold this forever. A
	// hundred pages is far past any real installation, and stopping short says the
	// list is incomplete rather than pretending it is not.
	for page := 0; page < 100; page++ {
		batch, next, err := fetchCatalogPage(ctx, client, base)
		if err != nil {
			return nil, err
		}

		for _, name := range batch {
			project, err := registry.core.resolveImage(ctx, name)
			if err != nil || project == "" {
				continue
			}
			if len(wanted) > 0 && !wanted[project] {
				continue
			}
			all = append(all, name)
		}

		if next == "" {
			return all, nil
		}
		base = next
	}
	return all, nil
}

// fetchCatalogPage reads one page of the catalogue and says where the next one is.
func fetchCatalogPage(ctx context.Context, client *http.Client, url string) (names []string, next string, err error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("the registry answered %d when listing what it holds", response.StatusCode)
	}

	var listing struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		return nil, "", err
	}

	next = nextLink(response.Header.Get("Link"))
	if next != "" {
		// A relative link is relative to the registry, not to whatever address was
		// asked for, and the two differ here: this module talks to the registry by
		// its internal name.
		if strings.HasPrefix(next, "/") {
			next = strings.TrimRight(upstreamURL, "/") + next
		}
	}
	return listing.Repositories, next, nil
}

// nextLink is where the registry says the rest of the catalogue is.
//
// The header names a full URL in angle brackets, optionally followed by its
// relations. Anything else is not a link this module should follow: it is a
// registry that does not do paging, and it has just said so by omitting it.
func nextLink(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 {
			continue
		}
		relates := false
		for _, segment := range segments[1:] {
			if strings.Contains(strings.ReplaceAll(segment, " ", ""), `rel="next"`) {
				relates = true
			}
		}
		if !relates {
			continue
		}
		target := strings.TrimSpace(segments[0])
		return strings.Trim(target, "<>")
	}
	return ""
}

// describeOne reads one repository's tags and what they weigh.
//
// The same rules as a project's own page: layers shared between tags are counted
// once, and a tag that cannot be described does not hide the others.
func (registry *registry) describeOne(ctx context.Context, name string) catalogEntry {
	client := defaultClient()
	entry := catalogEntry{Repository: name, Tags: []catalogTag{}}

	project, err := registry.core.resolveImage(ctx, name)
	if err == nil {
		entry.Project = project
	}

	tags, err := fetchTags(ctx, client, name)
	if err != nil {
		return entry
	}

	seen := map[string]bool{}
	for _, tag := range tags {
		digest, err := manifestDigest(ctx, client, name, tag)
		if err != nil {
			continue
		}

		described := catalogTag{Name: tag, Digest: digest}
		if !seen[digest] {
			seen[digest] = true
			described.Size, _ = manifestSize(ctx, client, name, digest)
			entry.Size += described.Size
			described.Created = manifestCreated(ctx, client, name, digest)
		}
		entry.Tags = append(entry.Tags, described)
	}

	sort.Slice(entry.Tags, func(i, j int) bool {
		return entry.Tags[i].Name < entry.Tags[j].Name
	})
	entry.TagsLen = len(entry.Tags)
	return entry
}

func totalTags(entries []catalogEntry) int {
	count := 0
	for _, entry := range entries {
		count += entry.TagsLen
	}
	return count
}

// manifest is one image manifest, read once.
type manifestBody struct {
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Size int64 `json:"size"`
	} `json:"layers"`
	Manifests []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"manifests"`
}

// fetchManifest reads a manifest, which is the one request that says what an image
// is made of.
func fetchManifest(ctx context.Context, client *http.Client, name, digest string) (*manifestBody, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimRight(upstreamURL, "/"), name, digest), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", acceptManifests)

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the registry answered %d for a manifest", response.StatusCode)
	}

	var body manifestBody
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		return nil, err
	}
	return &body, nil
}

// fetchBlob reads one blob by digest, which is where a build's own record lives.
func fetchBlob(ctx context.Context, client *http.Client, name, digest string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/blobs/%s", strings.TrimRight(upstreamURL, "/"), name, digest), nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the registry answered %d for a blob", response.StatusCode)
	}
	return io.ReadAll(response.Body)
}

// manifestCreated is when the image was built, as its own config says.
//
// Read from the config blob rather than the manifest: the manifest names the
// config by digest and says nothing about time, while the config carries the
// timestamp the builder wrote. A missing or unreadable one is absent rather than
// zero — a date of 1970 is a claim, and this is not it.
func manifestCreated(ctx context.Context, client *http.Client, name, digest string) time.Time {
	manifest, err := fetchManifest(ctx, client, name, digest)
	if err != nil {
		return time.Time{}
	}

	// An index points at one manifest per platform and names no config of its own,
	// so there is nothing to read a date from until it is opened. A multi-platform
	// image — which is what every modern build produces — would otherwise report no
	// date at all, and "not reported" would be a statement about this code rather
	// than about the image.
	config := manifest.Config.Digest
	if config == "" && len(manifest.Manifests) > 0 {
		child, err := fetchManifest(ctx, client, name, manifest.Manifests[0].Digest)
		if err != nil {
			return time.Time{}
		}
		config = child.Config.Digest
	}
	if config == "" {
		return time.Time{}
	}

	blob, err := fetchBlob(ctx, client, name, config)
	if err != nil {
		return time.Time{}
	}

	var body struct {
		Created time.Time `json:"created"`
	}
	if err := json.Unmarshal(blob, &body); err != nil || body.Created.IsZero() {
		return time.Time{}
	}
	return body.Created.UTC()
}
