// Command module-cache-demo is a reference implementation of the dogit module
// protocol.
//
// It is not a useful product feature: it exists so the contract has a working
// second implementation before real modules are written. Anything a real module
// needs beyond this, it may implement — this program simply must not need it.
//
// What it demonstrates:
//
//   - registration with an instance token, receiving a module token in return;
//   - heartbeats, so the core can tell a running module from a dead one;
//   - the manifest: version, scopes and the settings schema the admin UI renders;
//   - introspection: it asks the core who a caller is, and never decides access
//     on its own.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultCoreURL  = "http://localhost:8080"
	moduleKind      = "cache:demo"
	defaultEndpoint = "http://module-cache-demo:8090"
)

type config struct {
	coreURL           string
	registrationToken string
	endpoint          string
	name              string
	listen            string
	interval          time.Duration
}

func main() {
	cfg := config{}

	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL),
		"base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token",
		os.Getenv("DOGIT_MODULE_TOKEN"),
		"instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", defaultEndpoint),
		"address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", "demo"),
		"module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", ":8090"),
		"address to listen on")
	flag.DurationVar(&cfg.interval, "heartbeat", 30*time.Second, "heartbeat interval")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module: a registration token is required (DOGIT_MODULE_TOKEN)")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	client := &coreClient{baseURL: strings.TrimRight(cfg.coreURL, "/")}

	// Register first: the module token comes back from the registration call, and
	// everything after this point authenticates with it.
	moduleToken, err := client.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest())
	if err != nil {
		log.Fatalf("module: registration failed: %v", err)
	}
	client.token = moduleToken
	log.Printf("module: registered, heartbeat every %s", cfg.interval)

	// The same registration call is reused on recovery, so it is kept as a closure.
	register := func() error {
		token, err := client.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest())
		if err != nil {
			return err
		}
		client.token = token
		log.Printf("module: re-registered")
		return nil
	}

	go heartbeat(ctx, client, cfg.interval, register)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           routes(client),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	// The module's own directory, created before it reports on it. A module that
	// cannot measure its storage should say so rather than report a number it made
	// up, and the simplest way for it to be measurable is to have somewhere to work.
	if err := os.MkdirAll(envOr("MODULE_CACHE_DIR", "/data/cache"), 0o750); err != nil {
		log.Fatalf("module: cannot create the cache directory: %v", err)
	}

	log.Printf("module: listening on %s (advertised as %s)", cfg.listen, cfg.endpoint)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("module: %v", err)
	}
}

func manifest() map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Reference implementation of the dogit module protocol",
		"scopes":      []string{"cache:read", "cache:write"},
		"settings": []map[string]any{
			{
				"key":         "upstream",
				"label":       "Upstream registry",
				"type":        "url",
				"default":     "https://registry.npmjs.org",
				"description": "Requests for uncached packages are fetched from here.",
			},
			{
				"key":         "max_entry_bytes",
				"label":       "Maximum cached entry",
				"type":        "int",
				"default":     52428800,
				"description": "Entries larger than this are passed through uncached.",
			},
			{
				"key":         "upstream_token",
				"label":       "Upstream token",
				"type":        "string",
				"secret":      true,
				"description": "Never returned by the API once written.",
			},
		},
		// A module says what removing it means; the core renders these and passes
		// the chosen keys back. The core has no idea what a cache entry is.
		"uninstall": map[string]any{
			"options": []map[string]any{
				{
					"key":         "purge_cache",
					"label":       "Delete the cache",
					"description": "Every entry this module has stored, fetched or built.",
					"default":     true,
					"dangerous":   true,
				},
			},
		},
	}
}

func routes(client *coreClient) http.Handler {
	mux := http.NewServeMux()

	// The two endpoints Kubernetes needs in order to manage this pod.
	mux.HandleFunc("/-/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	})

	// The manifest, so an operator can inspect a module without reading the core.
	mux.HandleFunc("/module/manifest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, manifest())
	})

	// Who am I? Demonstrates the introspection call a module makes instead of
	// keeping its own user directory.
	mux.HandleFunc("/whoami", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			writeJSON(w, map[string]any{"error": "missing bearer token"})
			return
		}
		result, err := client.introspect(r.Context(), token)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, result)
	})

	// The effective settings a job would run with, which is how a module learns
	// where a given project expects it to point.
	mux.HandleFunc("/settings", func(w http.ResponseWriter, r *http.Request) {
		projectPath := r.URL.Query().Get("project")
		settings, err := client.settings(r.Context(), projectPath)
		if err != nil {
			writeJSON(w, map[string]any{"error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"effective": settings})
	})

	// Removing this module, as the core asks for it.
	//
	// The answer is a stream of lines rather than a single response, because
	// deleting what a module holds does not fit in one request, and an
	// administrator who asked for it will not be watching when it finishes. Every
	// line is kept by the core, so they may close the tab and come back.
	mux.HandleFunc("/uninstall", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Options []string `json:"options"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		// The core checks the keys against the manifest before asking, and an
		// unknown one means the two disagree about what removal means. Refusing is
		// the safe answer: guessing which side is right is how data somebody meant
		// to keep gets deleted.
		if !slices.Contains(body.Options, "purge_cache") && len(body.Options) > 0 {
			http.Error(w, "unknown option", http.StatusBadRequest)
			return
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

		// Slow on purpose: this is a reference implementation, and a real module's
		// log arrives over minutes or hours. A demonstration that finished
		// instantly would never show whether the core keeps up with a real one.
		for i := 1; i <= 3; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(700 * time.Millisecond):
			}
			say(map[string]any{
				"message":  fmt.Sprintf("removing cache entry %d", i),
				"progress": map[string]any{"done": i, "total": 3},
			})
		}

		say(map[string]any{
			"message": "the upstream is not ours to delete",
			"level":   "warn",
		})
		say(map[string]any{
			"summary": map[string]any{
				"removed_entries": 3,
				"freed_bytes":     1048576,
			},
		})
	})

	return mux
}

// heartbeat keeps the core informed that this module is alive.
//
// A rejected heartbeat means the core no longer recognises the module token: the
// database was restored, the module was removed and reinstalled, or the token was
// rotated. Retrying with a dead token would never recover, so the module
// re-registers with the instance token it still holds. That is what a real module
// has to do too, and it is why the instance token is kept in its secret.
func heartbeat(ctx context.Context, client *coreClient, interval time.Duration, register func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := client.beatWithStats(ctx, stats())
			if err == nil {
				continue
			}
			if !errors.Is(err, errUnauthorized) {
				log.Printf("module: heartbeat failed: %v", err)
				continue
			}

			log.Printf("module: token rejected (%v), re-registering", err)
			if registerErr := register(); registerErr != nil {
				log.Printf("module: re-registration failed: %v", registerErr)
			}
		}
	}
}

// errUnauthorized marks a 401 from the core.
var errUnauthorized = errors.New("module authentication is required")

// coreClient talks to the dogit core. Every call except registration carries the
// module token the core issued.
type coreClient struct {
	baseURL string
	token   string
}

func (c *coreClient) post(ctx context.Context, path string, body any, token string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("%s: %w", path, errUnauthorized)
		}
		return fmt.Errorf("%s: %s", path, strings.TrimSpace(string(raw)))
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (c *coreClient) get(ctx context.Context, path, token string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", path, strings.TrimSpace(string(raw)))
	}
	return json.Unmarshal(raw, out)
}

func (c *coreClient) register(ctx context.Context, registrationToken, name, endpoint string, man map[string]any) (string, error) {
	var response struct {
		Token string `json:"token"`
	}
	err := c.post(ctx, "/api/v1/modules/register", map[string]any{
		"kind":     moduleKind,
		"name":     name,
		"endpoint": endpoint,
		"manifest": man,
	}, registrationToken, &response)
	if err != nil {
		return "", err
	}
	if response.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return response.Token, nil
}

func (c *coreClient) beat(ctx context.Context) error {
	return c.post(ctx, "/api/v1/module/heartbeat", map[string]any{}, c.token, nil)
}

// beatWithStats reports what this module can see about itself.
//
// Only what is actually knowable. A value that cannot be read is left out rather
// than sent as zero: the core shows an absent value as "not reported", which is
// true, while a zero would claim an empty disk that nobody checked.
func (c *coreClient) beatWithStats(ctx context.Context, reading map[string]any) error {
	return c.post(ctx, "/api/v1/module/heartbeat", map[string]any{"stats": reading}, c.token, nil)
}

var startedAt = time.Now()

// stats is this module's reading of its own surroundings.
//
// The container is the world this process knows: it can see its own memory
// through /proc and the filesystem it writes to, and usually nothing of the node
// underneath. Sending what it cannot see would be inventing numbers.
func stats() map[string]any {
	reading := map[string]any{
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
	}

	if total, used, ok := storageStat(envOr("MODULE_CACHE_DIR", "/data/cache")); ok {
		reading["storage_total_bytes"] = total
		reading["storage_used_bytes"] = used
	}
	if rss, ok := residentBytes(); ok {
		reading["process_memory_bytes"] = rss
	}
	return reading
}

// storageStat reports the filesystem behind a directory.
func storageStat(dir string) (total, used int64, ok bool) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0, false
	}
	total = int64(stat.Blocks) * int64(stat.Bsize)
	free := int64(stat.Bavail) * int64(stat.Bsize)
	return total, total - free, true
}

// residentBytes reads this process's own resident set from /proc.
func residentBytes() (int64, bool) {
	content, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	fields := strings.Fields(string(content))
	if len(fields) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return pages * int64(os.Getpagesize()), true
}

func (c *coreClient) introspect(ctx context.Context, userToken string) (map[string]any, error) {
	var result map[string]any
	// Introspection answers 200 with active=false for an unknown token, so an
	// empty result is the signal to deny rather than to retry.
	if err := c.post(ctx, "/api/v1/auth/introspect", map[string]any{}, userToken, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *coreClient) settings(ctx context.Context, projectPath string) (map[string]any, error) {

	// The module reads its own effective settings from the module-authenticated
	// endpoint, so no administrator session is involved.
	path := "/api/v1/module/settings"
	if projectPath != "" {
		path += "?project=" + url.QueryEscape(projectPath)
	}

	var response struct {
		Effective map[string]any `json:"effective"`
	}
	if err := c.get(ctx, path, c.token, &response); err != nil {
		return nil, err
	}
	return response.Effective, nil
}

// moduleSelf asks the core which module this process is.
func (c *coreClient) moduleSelf(ctx context.Context) (map[string]any, error) {
	var response struct {
		Module map[string]any `json:"module"`
	}
	if err := c.get(ctx, "/api/v1/module/me", c.token, &response); err != nil {
		return nil, err
	}
	return response.Module, nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("module: write response: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
