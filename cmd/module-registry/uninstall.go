package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// uninstall is the module removing itself, as the core asks.
//
// The answer is a stream of lines rather than a single response, because deleting
// images does not fit in one request and the administrator who asked will not be
// watching when it finishes. The core keeps every line, so they may close the tab.
//
// Removal is idempotent: the core may ask twice after a restart it did not hear the
// end of, and the second attempt must finish the job rather than refuse.
func uninstall(core *coreClient) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// The core may stop listening at any moment; every call below is made with
		// this context so a removal stops rather than writing into nothing.
		ctx := r.Context()

		var body struct {
			Options []string `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		// An option the core passed and this build does not know is a version
		// mismatch. Refusing is the safe answer: guessing which side is right is how
		// images somebody meant to keep get deleted.
		known := map[string]bool{"purge_images": true, "drop_database": true}
		for _, option := range body.Options {
			if !known[option] {
				http.Error(w, "unknown option: "+option, http.StatusBadRequest)
				return
			}
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, _ := w.(http.Flusher)

		say := func(line map[string]any) {
			encoded, err := json.Marshal(line)
			if err != nil {
				return
			}
			_, _ = fmt.Fprintf(w, "%s\n", encoded)
			if flusher != nil {
				flusher.Flush()
			}
		}

		purge := containsString(body.Options, "purge_images")
		dropDatabase := containsString(body.Options, "drop_database")

		var freed int64

		if purge {
			repositories, bytesFreed, err := removeImages(ctx, say)
			if err != nil {
				say(map[string]any{"level": "error", "message": err.Error()})
				return
			}
			freed = bytesFreed

			if repositories > 0 {
				say(map[string]any{
					"summary": map[string]any{
						"removed_repositories": repositories,
						"freed_bytes":          freed,
					},
				})
			}
		} else {
			say(map[string]any{"message": "leaving every image in place, as asked"})
			say(map[string]any{
				"message": "the registry container will keep running until it is removed",
				"level":   "warn",
			})
		}

		if dropDatabase {
			// The database is the core's to drop: it provisioned it, and it is the
			// only thing that knows how. Images are not in there, so this is not a
			// way to delete any.
			say(map[string]any{"message": "asking the core to drop the module database"})
			if err := core.dropDatabase(ctx); err != nil {
				say(map[string]any{"level": "error", "message": err.Error()})
				return
			}
			say(map[string]any{"summary": map[string]any{"dropped_database": true}})
		}

		if !purge && !dropDatabase {
			say(map[string]any{"summary": map[string]any{}})
		}
	}
}

// removeImages deletes what the registry holds, one repository at a time.
//
// It goes through the registry's own API rather than touching files: the storage
// driver's layout is not this module's to know, and deleting a blob behind a
// running registry's back leaves it answering from a catalogue that no longer
// matches anything on disk.
func removeImages(ctx context.Context, say func(map[string]any)) (repositories int, freed int64, err error) {
	client := &http.Client{Timeout: 2 * time.Minute}

	catalogue, err := fetchCatalogue(ctx, client)
	if err != nil {
		return 0, 0, err
	}
	if len(catalogue) == 0 {
		say(map[string]any{"message": "the registry holds no images"})
		return 0, 0, nil
	}

	for index, repository := range catalogue {
		select {
		case <-ctx.Done():
			// The core stopped listening. What was deleted stays deleted, and running
			// on would mean writing into a connection nobody is reading.
			return index, freed, ctx.Err()
		default:
		}

		say(map[string]any{
			"message":  "removing " + repository,
			"progress": map[string]any{"done": index, "total": len(catalogue)},
		})

		bytesFreed, err := deleteRepository(ctx, client, repository)
		if err != nil {
			// One repository failing is reported and the rest are left alone: a
			// half-finished removal that is visible is recoverable, and one that
			// claims success is not.
			say(map[string]any{
				"message": fmt.Sprintf("could not remove %s: %v", repository, err),
				"level":   "error",
			})
			return index, freed, nil
		}
		freed += bytesFreed
		repositories++
	}

	say(map[string]any{
		"progress": map[string]any{"done": len(catalogue), "total": len(catalogue)},
	})
	return repositories, freed, nil
}

func fetchCatalogue(ctx context.Context, client *http.Client) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(upstreamURL, "/")+"/v2/_catalog?n=10000", nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the registry did not answer: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the registry answered %d to a catalogue request", response.StatusCode)
	}

	var catalogue struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalogue); err != nil {
		return nil, fmt.Errorf("could not read the catalogue: %w", err)
	}
	return catalogue.Repositories, nil
}

// deleteRepository removes every tag of one repository.
//
// The registry specification has no "delete a repository": tags are deleted one at
// a time, and the repository stops being listed once its last tag is gone.
func deleteRepository(ctx context.Context, client *http.Client, repository string) (int64, error) {
	var freed int64

	tags, err := fetchTags(ctx, client, repository)
	if err != nil {
		return 0, err
	}

	for _, tag := range tags {
		digest, err := manifestDigest(ctx, client, repository, tag)
		if err != nil {
			return freed, err
		}

		bytesFreed := deleteManifest(ctx, client, repository, digest)
		freed += bytesFreed
	}

	// The digest itself, now that no tag points at it.
	if err := deleteByDigest(ctx, client, repository, "manifests", ""); err != nil {
		return freed, err
	}
	return freed, nil
}

func fetchTags(ctx context.Context, client *http.Client, repository string) ([]string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		fmt.Sprintf("%s/v2/%s/tags/list?n=10000", strings.TrimRight(upstreamURL, "/"), repository), nil)
	if err != nil {
		return nil, err
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the registry answered %d when listing tags of %s", response.StatusCode, repository)
	}

	var listing struct {
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(response.Body).Decode(&listing); err != nil {
		return nil, err
	}
	return listing.Tags, nil
}

// manifestDigest finds the digest behind a tag, which is what deleting a tag
// means: the specification deletes by digest, and the tag is removed by deleting
// what it pointed at.
func manifestDigest(ctx context.Context, client *http.Client, repository, tag string) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead,
		fmt.Sprintf("%s/v2/%s/manifests/%s", strings.TrimRight(upstreamURL, "/"), repository, tag), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", acceptManifests)

	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the registry answered %d for the manifest of %s:%s",
			response.StatusCode, repository, tag)
	}
	return response.Header.Get("Docker-Content-Digest"), nil
}

func deleteManifest(ctx context.Context, client *http.Client, repository, digest string) int64 {
	if digest == "" {
		return 0
	}
	if err := deleteByDigest(ctx, client, repository, "manifests", digest); err != nil {
		return 0
	}
	return 1
}

func deleteByDigest(ctx context.Context, client *http.Client, repository, kind, digest string) error {
	url := fmt.Sprintf("%s/v2/%s/%s/%s", strings.TrimRight(upstreamURL, "/"), repository, kind, digest)
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}

	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	// Already gone is the outcome that was wanted.
	if response.StatusCode == http.StatusOK ||
		response.StatusCode == http.StatusAccepted ||
		response.StatusCode == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("the registry answered %d when deleting %s", response.StatusCode, url)
}

// dropDatabase asks the core to remove this module's database.
//
// The core provisioned it and knows the role it owns, so the module does not try
// to reach into the cluster's other databases.
func (c *coreClient) dropDatabase(ctx context.Context) error {
	return c.post(ctx, "/api/v1/module/database/drop", map[string]any{}, c.token, nil)
}

// containsString is written out rather than imported so the option handling reads
// the same at every call site.
func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
