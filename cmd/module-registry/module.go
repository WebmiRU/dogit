package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// config is what this module was told to do, from flags and the environment.
type config struct {
	coreURL           string
	registrationToken string
	endpoint          string
	name              string
	listen            string
	upstream          string
	// publicURL is the address clients use, which is not the address the core uses.
	publicURL string
	interval  time.Duration
}

var errUnauthorized = errors.New("unauthorized")

// heartbeat keeps the core informed that this module is alive, and tells it what
// it can see about itself.
//
// A rejected heartbeat means the core no longer recognises the module token: the
// database was restored, the module was removed and reinstalled, or the token was
// rotated. Retrying with a dead token would never recover, so the module
// re-registers with the instance token it still holds.
func heartbeat(ctx context.Context, core *coreClient, interval time.Duration, register func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			err := core.beat(ctx, stats())
			if err == nil {
				continue
			}
			if !errors.Is(err, errUnauthorized) {
				log.Printf("module-registry: heartbeat failed: %v", err)
				continue
			}

			log.Printf("module-registry: token rejected (%v), re-registering", err)
			if registerErr := register(); registerErr != nil {
				log.Printf("module-registry: re-registration failed: %v", registerErr)
			}
		}
	}
}

var startedAt = time.Now()

// stats is this module's reading of its own surroundings.
//
// The registry's storage is the only thing here it can measure honestly: it knows
// where its storage driver writes, and the filesystem under that is the number an
// administrator wants when deciding whether to add a disk. The node underneath is
// not visible from inside a container, so it is not reported — a zero there would
// read as a machine doing nothing, in exactly the case where nobody knows.
func stats() map[string]any {
	reading := map[string]any{
		"uptime_seconds": int64(time.Since(startedAt).Seconds()),
	}

	if total, used, ok := storageStat(storageDir()); ok {
		reading["storage_total_bytes"] = total
		reading["storage_used_bytes"] = used

		// What a registry knows about itself: how much it holds, in images and in
		// bytes. Only the module can say this, and it is the number people actually
		// come to an image registry to find out.
		reading["extra"] = map[string]any{
			"images":      registryImageCount(),
			"storage_dir": storageDir(),
		}
	}
	if rss, ok := residentBytes(); ok {
		reading["process_memory_bytes"] = rss
	}
	return reading
}

func storageDir() string {
	return envOr("REGISTRY_STORAGE_DIR", "/var/lib/registry")
}

// registryImageCount counts what the registry holds, from its own catalogue.
//
// Walking a real registry's storage would mean understanding every driver's layout.
// Asking the registry is one request and cannot be wrong, and a failure is simply
// reported as no number rather than as zero.
func registryImageCount() string {
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(upstreamURL, "/")+"/v2/_catalog", nil)
	if err != nil {
		return ""
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ""
	}

	var catalogue struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(response.Body).Decode(&catalogue); err != nil {
		return ""
	}
	return strconv.Itoa(len(catalogue.Repositories))
}

// upstreamURL is the registry this module fronts, set once at start-up.
var upstreamURL string

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

// handler is the module's own HTTP surface: the endpoints the core and Kubernetes
// need, kept away from the registry paths so that nothing collides with an image
// name.
func handler(core *coreClient) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/-/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	// The manifest, so an operator can inspect the module without reading the core.
	mux.HandleFunc("/module/manifest", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(manifest())
	})

	// Which project an image name belongs to, and what a caller may do to it.
	//
	// This is the module's whole policy, exposed so that the core — and an operator
	// — can ask the same question the proxy asks, and get the same answer.
	mux.HandleFunc("/access", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Token   string `json:"token"`
			Project string `json:"project"`
			Action  string `json:"action"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}

		answer, err := core.ask(r.Context(), req.Token, req.Project, req.Action)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, answer)
	})

	// Removing the module, as the core asks for it.
	mux.HandleFunc("/uninstall", uninstall(core))

	return mux
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

var _ = fmt.Sprintf
