// Package app wires the shared dependencies that every dogit process needs.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/events"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/store"
)

// App holds the dependencies shared by all subcommands.
type App struct {
	Cfg    *config.Config
	Log    *slog.Logger
	Store  *store.Store
	Git    *gitx.Git
	Events *events.Bus
}

// New loads configuration, initialises the logger, applies migrations and
// prepares the data directories.
func New(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	log := logger.New(cfg.LogLevel, cfg.Environment)

	if err := ensureDirs(cfg); err != nil {
		return nil, err
	}

	if err := store.Migrate(cfg.DatabaseURL); err != nil {
		return nil, err
	}

	st, err := store.Open(ctx, cfg.DatabaseURL, store.Options{
		MaxConns:       cfg.DBMaxConns,
		ConnectRetry:   cfg.DBConnectRetry,
		ConnectTimeout: cfg.DBConnectMax,
	})
	if err != nil {
		return nil, err
	}

	return &App{
		Cfg:    cfg,
		Log:    log,
		Store:  st,
		Git:    gitx.New(gitx.Options{Binary: cfg.GitBinary}),
		Events: events.New(st, log),
	}, nil
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
