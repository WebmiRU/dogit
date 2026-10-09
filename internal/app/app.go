// Package app wires the shared dependencies that every dogit process needs.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/events"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/objects"
	"github.com/ewolf/dogit/internal/secrets"
	"github.com/ewolf/dogit/internal/store"
)

// App holds the dependencies shared by all subcommands.
type App struct {
	Cfg     *config.Config
	Log     *slog.Logger
	Store   *store.Store
	Git     *gitx.Git
	Events  *events.Bus
	Objects objects.Store
}

// New loads configuration, initialises the logger, applies migrations and
// prepares the data directories.
func New(ctx context.Context) (*App, error) { return NewWithLogLevel(ctx, "") }

// NewWithLogLevel is New with the log level forced.
//
// The git entry points need it: their stderr is the pusher's terminal, so the
// level has to be right before anything is constructed, not patched afterwards.
// Anything logged while starting up would otherwise reach the user's terminal.
func NewWithLogLevel(ctx context.Context, level string) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if level != "" {
		cfg.LogLevel = level
	}
	log := logger.New(cfg.LogLevel, cfg.Environment)

	if err := ensureDirs(cfg); err != nil {
		return nil, err
	}

	if err := store.Migrate(cfg.DatabaseURL); err != nil {
		return nil, err
	}

	// The sealer is made here rather than in the API, because the store needs it to read a
	// credential back: a sealed value on an instance whose store has no key is an error, and
	// an error in the middle of reading a page of settings is a page that does not load.
	sealer, err := secrets.New(cfg.SecretKey)
	if err != nil && !errors.Is(err, secrets.ErrNoKey) {
		return nil, fmt.Errorf("the secret key cannot be used, so this instance cannot read "+
			"what it has already sealed: %w", err)
	}

	st, err := store.Open(ctx, cfg.DatabaseURL, store.Options{
		MaxConns:       cfg.DBMaxConns,
		ConnectRetry:   cfg.DBConnectRetry,
		ConnectTimeout: cfg.DBConnectMax,
		Sealer:         sealer,
	})
	if err != nil {
		return nil, err
	}

	objectsStore, err := newObjectStore(ctx, cfg, log)
	if err != nil {
		st.Close()
		return nil, err
	}

	return &App{
		Cfg:     cfg,
		Log:     log,
		Store:   st,
		Git:     gitx.New(gitx.Options{Binary: cfg.GitBinary}),
		Events:  events.New(st, log),
		Objects: objectsStore,
	}, nil
}

// newObjectStore builds the blob store.
//
// The remote store is verified at start-up on purpose: a wrong endpoint should
// fail here, loudly, rather than halfway through the first job that uploads an
// artifact.
func newObjectStore(ctx context.Context, cfg *config.Config, log *slog.Logger) (objects.Store, error) {
	if cfg.ObjectsBackend != "s3" {
		local, err := objects.NewLocal(cfg.ObjectsLocalDir, cfg.ObjectsPrefix)
		if err != nil {
			return nil, err
		}
		log.Info("object storage ready", "backend", "local", "dir", cfg.ObjectsLocalDir)
		return local, nil
	}

	remote, err := objects.NewS3(objects.S3Options{
		Endpoint:   cfg.ObjectsEndpoint,
		AccessKey:  cfg.ObjectsAccessKey,
		SecretKey:  cfg.ObjectsSecretKey,
		Bucket:     cfg.ObjectsBucket,
		Region:     cfg.ObjectsRegion,
		Prefix:     cfg.ObjectsPrefix,
		UseSSL:     boolPtr(cfg.ObjectsUseSSL),
		PartSizeMB: 16,
	})
	if err != nil {
		return nil, err
	}
	if err := remote.EnsureBucket(ctx); err != nil {
		return nil, err
	}

	log.Info("object storage ready", "backend", "s3", "bucket", cfg.ObjectsBucket)
	return remote, nil
}

func boolPtr(v bool) *bool { return &v }

// PublishInstanceEvent records something that happened to a module.
//
// The janitor lives below the API and still has to be heard: a module going quiet
// is the one change an administrator most needs to see promptly, and it happens
// with nobody in front of the browser to cause it.
func (a *App) PublishInstanceEvent(ctx context.Context, kind models.EventKind, payload map[string]any) {
	if a.Events == nil {
		return
	}
	if err := a.Events.Publish(ctx, kind, nil, nil, payload); err != nil {
		a.Log.Debug("publish an instance event", "kind", kind, "error", err)
	}
}

// Close releases the database pool.
func (a *App) Close() {
	if a.Store != nil {
		a.Store.Close()
	}
}

func ensureDirs(cfg *config.Config) error {
	dirs := []string{
		cfg.DataDir,
		cfg.RepoDir,
		cfg.ArtifactDir,
		filepath.Join(cfg.DataDir, "logs"),
		filepath.Join(cfg.DataDir, "ssh"),
		filepath.Join(cfg.ArtifactDir, "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}
	return nil
}

// EnsureHostKey generates the SSH host key on first start. A missing host key
// would make every client report a changed fingerprint.
func EnsureHostKey(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create ssh directory: %w", err)
	}
	if err := generateEd25519Key(path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
