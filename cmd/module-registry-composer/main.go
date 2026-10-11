// Command module-registry-composer is a public, read-only Composer 2 proxy.
// Clients do not authenticate; the module token is used only with Dogit's core.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

const (
	moduleKind           = "registry:composer"
	defaultCoreURL       = "http://app:8080"
	defaultEndpoint      = "http://module-registry-composer:8095"
	defaultListen        = ":8095"
	defaultUpstream      = "https://repo.packagist.org"
	defaultStorageDir    = "/data/cache"
	defaultStoragePrefix = "dogit/composer"
)

type config struct {
	coreURL           string
	registrationToken string
	endpoint           string
	name               string
	listen             string
	publicURL          string
	publicAddress      string
	upstream           string
	additionalUpstream string
	databaseURL        string
	storageDir         string
	allowedOrigins     string
	interval           time.Duration
}

var startedAt = time.Now()

func main() {
	cfg := config{}
	flag.StringVar(&cfg.coreURL, "core", envOr("DOGIT_CORE_URL", defaultCoreURL), "base URL of the dogit core")
	flag.StringVar(&cfg.registrationToken, "registration-token", os.Getenv("DOGIT_MODULE_TOKEN"), "instance token created with: dogit module token create")
	flag.StringVar(&cfg.endpoint, "endpoint", envOr("DOGIT_MODULE_ENDPOINT", defaultEndpoint), "address the core should use to reach this module")
	flag.StringVar(&cfg.name, "name", envOr("DOGIT_MODULE_NAME", "composer"), "module name, unique per kind")
	flag.StringVar(&cfg.listen, "listen", envOr("DOGIT_MODULE_LISTEN", defaultListen), "address to listen on")
	flag.StringVar(&cfg.publicURL, "public-url", envOr("DOGIT_MODULE_PUBLIC_URL", ""), "public address clients use to reach this module")
	flag.StringVar(&cfg.publicAddress, "public-address", envOr("DOGIT_COMPOSER_PUBLIC_ADDRESS", ""), "default address written into distribution metadata")
	flag.StringVar(&cfg.upstream, "upstream", envOr("COMPOSER_UPSTREAM_URL", defaultUpstream), "primary Composer 2 upstream")
	flag.StringVar(&cfg.additionalUpstream, "additional-upstream", envOr("COMPOSER_ADDITIONAL_UPSTREAM_URL", ""), "optional additional Composer 2 upstream")
	flag.StringVar(&cfg.databaseURL, "database", os.Getenv("DOGIT_DATABASE_URL"), "PostgreSQL connection string")
	flag.StringVar(&cfg.storageDir, "storage-dir", envOr("COMPOSER_STORAGE_DIR", defaultStorageDir), "filesystem cache directory")
	flag.StringVar(&cfg.allowedOrigins, "allowed-origins", envOr("DOGIT_MODULE_ALLOWED_ORIGINS", ""), "comma-separated browser origins allowed to read the catalog")
	flag.DurationVar(&cfg.interval, "heartbeat", 30*time.Second, "heartbeat interval")
	flag.Parse()

	if cfg.registrationToken == "" {
		log.Fatal("module-registry-composer: a registration token is required (DOGIT_MODULE_TOKEN)")
	}
	if strings.TrimSpace(cfg.databaseURL) == "" {
		log.Fatal("module-registry-composer: PostgreSQL is required (DOGIT_DATABASE_URL)")
	}
	if !validHTTPURL(cfg.upstream) {
		log.Fatalf("module-registry-composer: primary upstream %q is not an HTTP(S) URL", cfg.upstream)
	}
	if cfg.additionalUpstream != "" && !validHTTPURL(cfg.additionalUpstream) {
		log.Fatalf("module-registry-composer: additional upstream %q is not an HTTP(S) URL", cfg.additionalUpstream)
	}
	if cfg.interval <= 0 {
		cfg.interval = 30 * time.Second
	}
	if cfg.publicAddress == "" {
		cfg.publicAddress = cfg.publicURL
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		log.Fatalf("module-registry-composer: open PostgreSQL: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(2 * time.Minute)

	pingCtx, cancelPing := context.WithTimeout(ctx, 15*time.Second)
	err = db.PingContext(pingCtx)
	cancelPing()
	if err != nil {
		log.Fatalf("module-registry-composer: PostgreSQL is unavailable: %v", err)
	}

	catalog := &catalogStore{db: db}
	if err := catalog.migrate(ctx); err != nil {
		log.Fatalf("module-registry-composer: prepare PostgreSQL tables: %v", err)
	}

	core := &coreClient{
		baseURL: strings.TrimRight(cfg.coreURL, "/"),
		client:  &http.Client{Timeout: 10 * time.Second},
	}
	token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg))
	if err != nil {
		log.Fatalf("module-registry-composer: registration failed: %v", err)
	}
	core.token = token
	log.Printf("module-registry-composer: registered; primary upstream %s", cfg.upstream)

	register := func() error {
		token, err := core.register(ctx, cfg.registrationToken, cfg.name, cfg.endpoint, manifest(cfg))
		if err != nil {
			return err
		}
		core.token = token
		log.Printf("module-registry-composer: re-registered")
		return nil
	}

	svc := newService(cfg, core, catalog)
	go heartbeat(ctx, core, cfg.interval, svc, register)

	server := &http.Server{
		Addr:              cfg.listen,
		Handler:           svc.handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// ZIP files are streamed and may be large, so the server has no write deadline.
		WriteTimeout: 0,
		IdleTimeout:  5 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	log.Printf("module-registry-composer: listening on %s (endpoint %s)", cfg.listen, cfg.endpoint)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("module-registry-composer: %v", err)
	}
}

func manifest(cfg config) map[string]any {
	return map[string]any{
		"version":     "0.1.0",
		"description": "Public, read-only Composer 2 proxy with cached ZIP distribution archives",
		"routing": map[string]any{
			"domains": []string{"composer.{host}"},
		},
		"settings": []map[string]any{
			{
				"key": "upstream_url", "label": "Primary Composer repository", "type": "url",
				"default": cfg.upstream,
				"description": "Composer 2 repository URL. Packagist is used by default.",
			},
			{
				"key": "additional_upstream_url", "label": "Additional Composer repository", "type": "url",
				"default": cfg.additionalUpstream,
				"description": "Optional second Composer 2 repository. Versions are merged; primary repository versions win on version conflicts.",
			},
			{
				"key": "public_address", "label": "Public address", "type": "url",
				"default": cfg.publicAddress,
				"description": "Base URL clients can reach. Cached ZIP URLs are rewritten to this address.",
			},
			{
				"key": "request_timeout", "label": "Metadata request timeout", "type": "int",
				"default": 45,
				"description": "Seconds to wait for upstream metadata.",
			},
			{
				"key": "storage_backend", "label": "Storage", "type": "enum",
				"options": []string{"filesystem", "s3"}, "default": "filesystem",
				"description": "Where cached distribution ZIP archives are kept. Changing backend does not move existing data.",
			},
			{
				"key": "storage_path", "label": "Filesystem cache directory", "type": "string",
				"default": cfg.storageDir,
				"description": "Persistent directory used by the filesystem backend.",
			},
			{
				"key": "s3_endpoint", "label": "S3 endpoint", "type": "url",
				"description": "S3-compatible endpoint; use http:// for a plain-HTTP endpoint.",
			},
			{
				"key": "s3_bucket", "label": "S3 bucket", "type": "string",
				"description": "An existing bucket for ZIP archives.",
			},
			{
				"key": "s3_region", "label": "S3 region", "type": "string",
				"default": "us-east-1",
				"description": "Region used for request signing.",
			},
			{
				"key": "s3_access_key", "label": "S3 access key", "type": "string",
				"description": "Credentials for the object store.",
			},
			{
				"key": "s3_secret_key", "label": "S3 secret key", "type": "string",
				"secret": true,
				"description": "Never returned by the core once written.",
			},
			{
				"key": "s3_prefix", "label": "S3 prefix", "type": "string",
				"default": defaultStoragePrefix,
				"description": "Namespace for this module's objects inside the bucket.",
			},
		},
	}
}

var errUnauthorized = errors.New("the core rejected the module token")

type coreClient struct {
	baseURL string
	token   string
	client  *http.Client
}

func (c *coreClient) request(ctx context.Context, method, requestPath string, body any, token string, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("%s: %w", requestPath, errUnauthorized)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("%s: core answered %s: %s", requestPath, response.Status, strings.TrimSpace(string(raw)))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s: decode core response: %w", requestPath, err)
		}
	}
	return nil
}

func (c *coreClient) register(ctx context.Context, registrationToken, name, endpoint string, moduleManifest map[string]any) (string, error) {
	var answer struct {
		Token string "json:\"token\""
	}
	err := c.request(ctx, http.MethodPost, "/api/v1/modules/register", map[string]any{
		"kind": moduleKind, "name": name, "endpoint": endpoint, "manifest": moduleManifest,
	}, registrationToken, &answer)
	if err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return answer.Token, nil
}

func (c *coreClient) settings(ctx context.Context) (map[string]any, error) {
	var answer struct {
		Effective map[string]any "json:\"effective\""
	}
	if err := c.request(ctx, http.MethodGet, "/api/v1/module/settings", nil, c.token, &answer); err != nil {
		return nil, err
	}
	if answer.Effective == nil {
		return map[string]any{}, nil
	}
	return answer.Effective, nil
}

func (c *coreClient) heartbeat(ctx context.Context, stats map[string]any) error {
	return c.request(ctx, http.MethodPost, "/api/v1/module/heartbeat", map[string]any{"stats": stats}, c.token, nil)
}

func heartbeat(ctx context.Context, core *coreClient, interval time.Duration, svc *service, register func() error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := core.heartbeat(ctx, svc.stats(ctx)); err == nil {
				continue
			} else if !errors.Is(err, errUnauthorized) {
				log.Printf("module-registry-composer: heartbeat failed: %v", err)
				continue
			}
			log.Printf("module-registry-composer: token rejected; registering again")
			if err := register(); err != nil {
				log.Printf("module-registry-composer: re-registration failed: %v", err)
			}
		}
	}
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
