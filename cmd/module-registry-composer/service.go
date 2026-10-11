package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/ewolf/dogit/internal/objects"
)

const (
	maxMetadataBytes = 32 << 20
	archiveTimeout   = 15 * time.Minute
	settingsRefresh  = 10 * time.Second
)

var packageSegmentPattern = regexp.MustCompile("^[A-Za-z0-9_.-]+$")
var distKeyPattern = regexp.MustCompile("^[a-f0-9]{64}$")

type service struct {
	cfg          config
	core         *coreClient
	repo         *catalogStore
	stores       *storeManager
	upstreamHTTP *http.Client

	settingsMu sync.Mutex
	settings   map[string]any
	settingsAt time.Time
}

func newService(cfg config, core *coreClient, repo *catalogStore) *service {
	return &service{
		cfg: cfg, core: core, repo: repo, stores: newStoreManager(),
		upstreamHTTP: &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many upstream redirects")
			}
			return requirePublicDestination(request.Context(), request.URL.String())
		}},
	}
}

func (s *service) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/packages.json", http.StatusFound)
	})
	mux.HandleFunc("GET /packages.json", s.packagesJSON)
	mux.HandleFunc("GET /p2/{package...}", s.packageMetadata)
	mux.HandleFunc("GET /dist/{file}", s.distribution)
	mux.HandleFunc("/catalog", s.catalog)
	mux.HandleFunc("GET /-/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok")
	})
	mux.HandleFunc("GET /-/ready", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ready")
	})
	mux.HandleFunc("GET /module/manifest", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, manifest(s.cfg))
	})
	mux.HandleFunc("POST /uninstall", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{\"summary\":{\"message\":\"module removed; the cache was left untouched\"}}\n")
	})
	return mux
}

func (s *service) packagesJSON(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"packages":     map[string]any{},
		"metadata-url": "/p2/%package%.json",
	})
}

func (s *service) packageMetadata(w http.ResponseWriter, r *http.Request) {
	name, ok := packageNameFromPath(r.PathValue("package"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	settings := s.effectiveSettings(r.Context())
	primary := settingString(settings, "upstream_url", s.cfg.upstream)
	if !validHTTPURL(primary) {
		http.Error(w, "the primary Composer upstream is not configured correctly", http.StatusBadGateway)
		return
	}
	upstreams := []string{primary}
	additional := settingStringValue(settings, "additional_upstream_url", s.cfg.additionalUpstream)
	if additional != "" && validHTTPURL(additional) && strings.TrimRight(additional, "/") != strings.TrimRight(primary, "/") {
		upstreams = append(upstreams, additional)
	}

	timeoutSeconds := settingInt(settings, "request_timeout", 45)
	if timeoutSeconds < 5 {
		timeoutSeconds = 5
	}
	if timeoutSeconds > 600 {
		timeoutSeconds = 600
	}
	timeout := time.Duration(timeoutSeconds) * time.Second

	documents := make([][]byte, 0, len(upstreams))
	notFoundCount := 0
	for _, upstream := range upstreams {
		body, status, err := s.fetchP2(r.Context(), upstream, name, timeout)
		if err != nil {
			log.Printf("module-registry-composer: metadata request for %s from %s failed: %v", name, upstream, err)
			continue
		}
		switch status {
		case http.StatusOK:
			documents = append(documents, body)
		case http.StatusNotFound:
			notFoundCount++
		default:
			log.Printf("module-registry-composer: metadata request for %s from %s answered %d", name, upstream, status)
		}
	}

	var raw []byte
	if len(documents) > 0 {
		var err error
		raw, err = mergeP2Documents(documents)
		if err != nil {
			http.Error(w, "upstream returned invalid Composer metadata", http.StatusBadGateway)
			return
		}
		if err := s.repo.putPackage(r.Context(), name, raw, p2VersionCount(raw)); err != nil {
			log.Printf("module-registry-composer: %v", err)
			http.Error(w, "could not store Composer metadata", http.StatusBadGateway)
			return
		}
	} else {
		if notFoundCount == len(upstreams) {
			http.NotFound(w, r)
			return
		}
		cached, found, err := s.repo.packageByName(r.Context(), name)
		if err != nil {
			log.Printf("module-registry-composer: %v", err)
			http.Error(w, "could not read Composer metadata cache", http.StatusBadGateway)
			return
		}
		if !found {
			http.Error(w, "Composer upstream is unavailable", http.StatusBadGateway)
			return
		}
		raw = cached.Metadata
	}

	rewritten, err := s.rewriteMetadata(r.Context(), raw, s.publicAddress(settings))
	if err != nil {
		log.Printf("module-registry-composer: rewrite metadata for %s: %v", name, err)
		http.Error(w, "could not prepare Composer metadata", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(rewritten)
}

func (s *service) fetchP2(parent context.Context, upstream, packageName string, timeout time.Duration) ([]byte, int, error) {
	target, err := p2URL(upstream, packageName)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "dogit-composer-registry/0.1")
	response, err := s.upstreamHTTP.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, response.StatusCode, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxMetadataBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(body) > maxMetadataBytes {
		return nil, 0, fmt.Errorf("metadata response exceeded %d bytes", maxMetadataBytes)
	}
	return body, response.StatusCode, nil
}

func p2URL(upstream, packageName string) (string, error) {
	if !validHTTPURL(upstream) {
		return "", fmt.Errorf("invalid upstream URL %q", upstream)
	}
	parts := strings.Split(packageName, "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid package name %q", packageName)
	}
	return strings.TrimRight(upstream, "/") + "/p2/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + ".json", nil
}

func packageNameFromPath(value string) (string, bool) {
	value = strings.TrimPrefix(value, "/")
	if !strings.HasSuffix(value, ".json") {
		return "", false
	}
	value = strings.TrimSuffix(value, ".json")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || !validPackageSegment(parts[0]) || !validPackageSegment(parts[1]) {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func validPackageSegment(value string) bool {
	return value != "" && value != "." && value != ".." && packageSegmentPattern.MatchString(value)
}

func validPackageName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && validPackageSegment(parts[0]) && validPackageSegment(parts[1])
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func mergeP2Documents(documents [][]byte) ([]byte, error) {
	if len(documents) == 0 {
		return nil, errors.New("no Composer metadata documents")
	}
	var result map[string]json.RawMessage
	merged := map[string][]json.RawMessage{}
	seen := map[string]map[string]bool{}

	for _, raw := range documents {
		var document map[string]json.RawMessage
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, fmt.Errorf("decode metadata document: %w", err)
		}
		if result == nil {
			result = document
		} else {
			for key, value := range document {
				if key == "packages" {
					continue
				}
				if _, exists := result[key]; !exists {
					result[key] = value
				}
			}
		}

		var packages map[string][]json.RawMessage
		if err := json.Unmarshal(document["packages"], &packages); err != nil {
			return nil, fmt.Errorf("decode metadata packages: %w", err)
		}
		for packageName, versions := range packages {
			if seen[packageName] == nil {
				seen[packageName] = map[string]bool{}
			}
			for _, version := range versions {
				key := metadataVersionKey(version)
				if seen[packageName][key] {
					continue
				}
				seen[packageName][key] = true
				merged[packageName] = append(merged[packageName], version)
			}
		}
	}
	packagesJSON, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	result["packages"] = packagesJSON
	return json.Marshal(result)
}

func metadataVersionKey(raw json.RawMessage) string {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) == nil {
		for _, key := range []string{"version_normalized", "version", "name"} {
			if value, ok := values[key]; ok {
				var text string
				if json.Unmarshal(value, &text) == nil && text != "" {
					return text
				}
			}
		}
	}
	return string(raw)
}

func metadataVersionName(values map[string]json.RawMessage, index int) string {
	for _, key := range []string{"version", "version_normalized", "name"} {
		if raw, ok := values[key]; ok {
			var value string
			if json.Unmarshal(raw, &value) == nil && value != "" {
				return value
			}
		}
	}
	return fmt.Sprintf("entry-%d", index)
}

func p2VersionCount(raw []byte) int {
	var document struct {
		Packages map[string][]json.RawMessage "json:\"packages\""
	}
	if json.Unmarshal(raw, &document) != nil {
		return 0
	}
	count := 0
	for _, versions := range document.Packages {
		count += len(versions)
	}
	return count
}

func distCacheKey(packageName, version, upstreamURL string) string {
	sum := sha256.Sum256([]byte(packageName + "\x00" + version + "\x00" + upstreamURL))
	return hex.EncodeToString(sum[:])
}

func (s *service) rewriteMetadata(ctx context.Context, raw []byte, publicAddress string) ([]byte, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode Composer metadata: %w", err)
	}
	var packages map[string][]json.RawMessage
	if err := json.Unmarshal(document["packages"], &packages); err != nil {
		return nil, fmt.Errorf("decode Composer package versions: %w", err)
	}

	base := strings.TrimRight(publicAddress, "/")
	if base == "" {
		base = strings.TrimRight(s.cfg.publicURL, "/")
	}
	if base == "" {
		base = strings.TrimRight(s.cfg.endpoint, "/")
	}
	for packageName, versions := range packages {
		for index, versionRaw := range versions {
			var version map[string]json.RawMessage
			if err := json.Unmarshal(versionRaw, &version); err != nil {
				continue
			}
			distRaw, ok := version["dist"]
			if !ok {
				continue
			}
			var distribution map[string]json.RawMessage
			if err := json.Unmarshal(distRaw, &distribution); err != nil {
				continue
			}
			var archiveType, sourceURL string
			_ = json.Unmarshal(distribution["type"], &archiveType)
			_ = json.Unmarshal(distribution["url"], &sourceURL)
			if archiveType != "zip" || !validHTTPURL(sourceURL) {
				continue
			}

			versionName := metadataVersionName(version, index)
			key := distCacheKey(packageName, versionName, sourceURL)
			objectKey := "dist/" + key + ".zip"
			if err := s.repo.putDist(ctx, distRecord{
				CacheKey: key, PackageName: packageName, Version: versionName,
				UpstreamURL: sourceURL, ObjectKey: objectKey,
			}); err != nil {
				return nil, err
			}
			rewriteURL, err := json.Marshal(base + "/dist/" + key + ".zip")
			if err != nil {
				return nil, err
			}
			distribution["url"] = rewriteURL
			distributionJSON, err := json.Marshal(distribution)
			if err != nil {
				return nil, err
			}
			version["dist"] = distributionJSON
			versionJSON, err := json.Marshal(version)
			if err != nil {
				return nil, err
			}
			versions[index] = versionJSON
		}
		packages[packageName] = versions
	}
	packagesJSON, err := json.Marshal(packages)
	if err != nil {
		return nil, err
	}
	document["packages"] = packagesJSON
	return json.Marshal(document)
}

func (s *service) publicAddress(settings map[string]any) string {
	address := settingString(settings, "public_address", s.cfg.publicAddress)
	if address == "" {
		address = s.cfg.publicURL
	}
	if address == "" {
		address = s.cfg.endpoint
	}
	return strings.TrimRight(address, "/")
}

func (s *service) distribution(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("file")
	key := strings.TrimSuffix(filename, ".zip")
	if filename != key+".zip" || !distKeyPattern.MatchString(key) {
		http.NotFound(w, r)
		return
	}
	record, found, err := s.repo.distByKey(r.Context(), key)
	if err != nil {
		log.Printf("module-registry-composer: %v", err)
		http.Error(w, "could not read distribution cache entry", http.StatusBadGateway)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}

	settings := s.effectiveSettings(r.Context())
	store, err := s.stores.get(r.Context(), s.cfg, settings)
	if err != nil {
		log.Printf("module-registry-composer: %v", err)
		http.Error(w, "distribution storage is not configured", http.StatusBadGateway)
		return
	}
	reader, info, err := store.Get(r.Context(), record.ObjectKey)
	if err == nil {
		if saveErr := s.repo.markDistCached(r.Context(), key, info.Size, info.LastModified); saveErr != nil {
			log.Printf("module-registry-composer: %v", saveErr)
		}
		s.writeArchive(w, reader, info, record)
		return
	}
	if !errors.Is(err, objects.ErrNotFound) {
		log.Printf("module-registry-composer: read cached distribution %s: %v", key, err)
		http.Error(w, "could not read cached distribution", http.StatusBadGateway)
		return
	}

	downloadCtx, cancel := context.WithTimeout(r.Context(), archiveTimeout)
	defer cancel()
	if !validHTTPURL(record.UpstreamURL) {
		http.Error(w, "invalid upstream distribution URL", http.StatusBadGateway)
		return
	}
	if err := requirePublicDestination(downloadCtx, record.UpstreamURL); err != nil {
		log.Printf("module-registry-composer: refused distribution URL for %s: %v", key, err)
		http.Error(w, "upstream distribution URL is not public", http.StatusBadGateway)
		return
	}
	request, err := http.NewRequestWithContext(downloadCtx, http.MethodGet, record.UpstreamURL, nil)
	if err != nil {
		http.Error(w, "invalid upstream distribution URL", http.StatusBadGateway)
		return
	}
	request.Header.Set("User-Agent", "dogit-composer-registry/0.1")
	response, err := s.upstreamHTTP.Do(request)
	if err != nil {
		log.Printf("module-registry-composer: download distribution %s: %v", key, err)
		http.Error(w, "Composer upstream is unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		log.Printf("module-registry-composer: distribution %s upstream answered %d", key, response.StatusCode)
		http.Error(w, "Composer distribution is unavailable upstream", http.StatusBadGateway)
		return
	}

	info, err = store.Put(downloadCtx, record.ObjectKey, response.Body, response.ContentLength, "application/zip")
	if err != nil {
		log.Printf("module-registry-composer: cache distribution %s: %v", key, err)
		http.Error(w, "could not cache Composer distribution", http.StatusBadGateway)
		return
	}
	if err := s.repo.markDistCached(downloadCtx, key, info.Size, info.LastModified); err != nil {
		log.Printf("module-registry-composer: %v", err)
	}
	reader, info, err = store.Get(downloadCtx, record.ObjectKey)
	if err != nil {
		log.Printf("module-registry-composer: read downloaded distribution %s: %v", key, err)
		http.Error(w, "could not read cached Composer distribution", http.StatusBadGateway)
		return
	}
	s.writeArchive(w, reader, info, record)
}

func (s *service) writeArchive(w http.ResponseWriter, reader io.ReadCloser, info objects.ObjectInfo, record distRecord) {
	defer reader.Close()
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+safeFilename(record.PackageName+"-"+record.Version+".zip")+"\"")
	if info.Size >= 0 {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size))
	}
	if _, err := io.Copy(w, reader); err != nil {
		log.Printf("module-registry-composer: stream distribution %s: %v", record.CacheKey, err)
	}
}

func safeFilename(value string) string {
	var out strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r) {
			out.WriteRune(r)
		} else {
			out.WriteByte('_')
		}
	}
	return out.String()
}

type catalogResponse struct {
	Packages           []catalogPackage `json:"packages"`
	TotalPackages      int              `json:"total_packages"`
	TotalVersions      int              `json:"total_versions"`
	CachedArchives     int              `json:"cached_archives"`
	CachedArchiveBytes int64            `json:"cached_archive_bytes"`
}

type catalogPackage struct {
	Name          string           `json:"name"`
	VersionCount  int              `json:"version_count"`
	LastFetchedAt time.Time        `json:"last_fetched_at"`
	Versions      []catalogVersion `json:"versions"`
}

type catalogVersion struct {
	Version          string     `json:"version"`
	ArchiveAvailable bool       `json:"archive_available"`
	ArchiveCached    bool       `json:"archive_cached"`
	ArchiveSizeBytes int64      `json:"archive_size_bytes"`
	ArchiveCachedAt  *time.Time `json:"archive_cached_at,omitempty"`
}

func (s *service) catalog(w http.ResponseWriter, r *http.Request) {
	s.applyCatalogCORS(w, r)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	settings := s.effectiveSettings(r.Context())
	store, err := s.stores.get(r.Context(), s.cfg, settings)
	if err != nil {
		log.Printf("module-registry-composer: %v", err)
		http.Error(w, "distribution storage is not configured", http.StatusBadGateway)
		return
	}
	objectsInStore, err := store.List(r.Context(), "dist/", 0)
	if err != nil {
		log.Printf("module-registry-composer: list cache objects: %v", err)
		http.Error(w, "could not read distribution cache", http.StatusBadGateway)
		return
	}
	cachedObjects := make(map[string]objects.ObjectInfo, len(objectsInStore))
	var cachedArchiveBytes int64
	for _, object := range objectsInStore {
		cachedObjects[object.Key] = object
		cachedArchiveBytes += object.Size
	}
	packages, err := s.repo.listPackages(r.Context())
	if err != nil {
		log.Printf("module-registry-composer: %v", err)
		http.Error(w, "could not list cached Composer packages", http.StatusBadGateway)
		return
	}

	answer := catalogResponse{
		Packages: make([]catalogPackage, 0, len(packages)),
		TotalPackages: len(packages), CachedArchives: len(objectsInStore),
		CachedArchiveBytes: cachedArchiveBytes,
	}
	for _, packageInfo := range packages {
		var document struct {
			Packages map[string][]json.RawMessage "json:\"packages\""
		}
		if err := json.Unmarshal(packageInfo.Metadata, &document); err != nil {
			continue
		}
		actualName := packageInfo.Name
		versions, ok := document.Packages[actualName]
		if !ok && len(document.Packages) == 1 {
			for candidate, entries := range document.Packages {
				actualName, versions = candidate, entries
			}
		}
		item := catalogPackage{
			Name: packageInfo.Name, VersionCount: len(versions),
			LastFetchedAt: packageInfo.LastFetchedAt,
			Versions: make([]catalogVersion, 0, len(versions)),
		}
		for index, versionRaw := range versions {
			var versionObject map[string]json.RawMessage
			if err := json.Unmarshal(versionRaw, &versionObject); err != nil {
				continue
			}
			version := catalogVersion{Version: metadataVersionName(versionObject, index)}
			if distRaw, ok := versionObject["dist"]; ok {
				var distribution map[string]json.RawMessage
				if json.Unmarshal(distRaw, &distribution) == nil {
					var archiveType, sourceURL string
					_ = json.Unmarshal(distribution["type"], &archiveType)
					_ = json.Unmarshal(distribution["url"], &sourceURL)
					if archiveType == "zip" && validHTTPURL(sourceURL) {
						version.ArchiveAvailable = true
						key := distCacheKey(actualName, version.Version, sourceURL)
						objectKey := "dist/" + key + ".zip"
						if object, exists := cachedObjects[objectKey]; exists {
							version.ArchiveCached = true
							version.ArchiveSizeBytes = object.Size
							cachedAt := object.LastModified
							version.ArchiveCachedAt = &cachedAt
						}
					}
				}
			}
			item.Versions = append(item.Versions, version)
		}
		answer.TotalVersions += item.VersionCount
		answer.Packages = append(answer.Packages, item)
	}
	writeJSON(w, answer)
}

func (s *service) applyCatalogCORS(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return
	}
	for _, candidate := range strings.Split(s.cfg.allowedOrigins, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == origin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.Header().Add("Vary", "Origin")
			return
		}
	}
}

func (s *service) stats(ctx context.Context) map[string]any {
	reading := map[string]any{"uptime_seconds": int64(time.Since(startedAt).Seconds())}
	summary, err := s.repo.summary(ctx)
	if err == nil {
		reading["extra"] = map[string]any{
			"cached_packages": summary.Packages,
			"cached_versions": summary.Versions,
			"cached_archives": summary.Archives,
			"cached_archive_bytes": summary.ArchiveBytes,
			"storage_backend": settingString(s.effectiveSettings(ctx), "storage_backend", "filesystem"),
		}
	}
	return reading
}

func (s *service) effectiveSettings(ctx context.Context) map[string]any {
	s.settingsMu.Lock()
	if s.settings != nil && time.Since(s.settingsAt) < settingsRefresh {
		values := s.settings
		s.settingsMu.Unlock()
		return values
	}
	s.settingsMu.Unlock()

	values, err := s.core.settings(ctx)
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if err != nil {
		s.settingsAt = time.Now()
		if s.settings != nil {
			log.Printf("module-registry-composer: using last known settings: %v", err)
			return s.settings
		}
		log.Printf("module-registry-composer: read settings: %v", err)
		s.settings = map[string]any{}
		return s.settings
	}
	s.settings = values
	s.settingsAt = time.Now()
	return s.settings
}

func settingString(settings map[string]any, key, fallback string) string {
	value := settingStringValue(settings, key, fallback)
	if value == "" {
		return fallback
	}
	return value
}

func settingStringValue(settings map[string]any, key, fallback string) string {
	if value, ok := settings[key]; ok {
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return fallback
}

func settingInt(settings map[string]any, key string, fallback int) int {
	value, ok := settings[key]
	if !ok {
		return fallback
	}
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case string:
		var parsed int
		if _, err := fmt.Sscanf(typed, "%d", &parsed); err == nil {
			return parsed
		}
	}
	return fallback
}

func requirePublicDestination(ctx context.Context, target string) error {
	parsed, err := url.Parse(target)
	if err != nil {
		return err
	}
	host := parsed.Hostname()
	if host == "" || strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return fmt.Errorf("local destination %q is not allowed", host)
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("resolve destination: %w", err)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("destination has no IP addresses")
	}
	for _, address := range addresses {
		ip := address.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
			return fmt.Errorf("destination resolves to non-public IP %s", ip)
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("module-registry-composer: write JSON response: %v", err)
	}
}
