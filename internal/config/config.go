// Package config loads runtime configuration from environment variables.
//
// Every setting has a sensible default so that a fresh checkout runs with
// `docker compose up` and nothing else.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// HTTP
	HTTPAddr string

	// SSH
	SSHAddr     string
	SSHHostKey  string
	SSHRepoRoot string

	// Database
	DatabaseURL     string
	DBMaxConns      int32
	DBConnectRetry  time.Duration
	DBConnectMax    time.Duration
	RunMigrationsAt bool

	// Storage
	DataDir     string
	RepoDir     string
	ArtifactDir string

	// CI
	PipelineConfigFile string
	RunnerConcurrency  int
	JobTimeout         time.Duration
	DockerBinary       string
	DockerNetwork      string

	// Misc
	GitBinary string
	// HookBinary is the dogit-hook executable that OpenSSH force-executes.
	HookBinary string
	// SSHHost is the hostname shown in clone URLs.
	SSHHost       string
	GitAuthorName string
	GitAuthorMail string
	AuthTokenTTL  time.Duration
	LogLevel      string
	Environment   string
}

func Load() (*Config, error) {
	loadEnvFile()

	c := &Config{
		HTTPAddr:           env("DOGIT_HTTP_ADDR", ":8080"),
		SSHAddr:            env("DOGIT_SSH_ADDR", ":2222"),
		SSHHostKey:         env("DOGIT_SSH_HOST_KEY", ""),
		SSHRepoRoot:        env("DOGIT_SSH_REPO_ROOT", ""),
		DatabaseURL:        env("DOGIT_DATABASE_URL", "postgres://dogit:dogit@localhost:5432/dogit?sslmode=disable"),
		DBMaxConns:         int32(envInt("DOGIT_DB_MAX_CONNS", 20)),
		DBConnectRetry:     envDuration("DOGIT_DB_CONNECT_RETRY", 1*time.Second),
		DBConnectMax:       envDuration("DOGIT_DB_CONNECT_MAX", 60*time.Second),
		RunMigrationsAt:    envBool("DOGIT_RUN_MIGRATIONS", true),
		DataDir:            env("DOGIT_DATA_DIR", "./data"),
		RepoDir:            env("DOGIT_REPO_DIR", ""),
		ArtifactDir:        env("DOGIT_ARTIFACT_DIR", ""),
		PipelineConfigFile: env("DOGIT_PIPELINE_CONFIG", ".gitlab-ci-lite.yml"),
		RunnerConcurrency:  envInt("DOGIT_RUNNER_CONCURRENCY", 2),
		JobTimeout:         envDuration("DOGIT_JOB_TIMEOUT", 30*time.Minute),
		DockerBinary:       env("DOGIT_DOCKER_BINARY", "docker"),
		DockerNetwork:      env("DOGIT_DOCKER_NETWORK", "bridge"),
		GitBinary:          env("DOGIT_GIT_BINARY", "git"),
		HookBinary:         env("DOGIT_HOOK_BINARY", "dogit-hook"),
		GitAuthorName:      env("DOGIT_GIT_AUTHOR_NAME", "dogit"),
		GitAuthorMail:      env("DOGIT_GIT_AUTHOR_MAIL", "dogit@localhost"),
		AuthTokenTTL:       envDuration("DOGIT_AUTH_TOKEN_TTL", 720*time.Hour),
		LogLevel:           env("DOGIT_LOG_LEVEL", "info"),
		Environment:        env("DOGIT_ENV", "development"),
	}

	// Derive storage paths that were not set explicitly.
	if c.RepoDir == "" {
		c.RepoDir = c.DataDir + "/repos"
	}
	if c.ArtifactDir == "" {
		c.ArtifactDir = c.DataDir + "/artifacts"
	}
	if c.SSHRepoRoot == "" {
		c.SSHRepoRoot = c.RepoDir
	}
	if c.SSHHostKey == "" {
		c.SSHHostKey = c.DataDir + "/ssh/host_ed25519"
	}

	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) Validate() error {
	switch c.Environment {
	case "development", "test", "production":
	default:
		return fmt.Errorf("DOGIT_ENV: unknown environment %q (want development, test or production)", c.Environment)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("DOGIT_LOG_LEVEL: unknown level %q (want debug, info, warn or error)", c.LogLevel)
	}
	if c.DatabaseURL == "" {
		return fmt.Errorf("DOGIT_DATABASE_URL must not be empty")
	}
	if c.RunnerConcurrency < 1 {
		return fmt.Errorf("DOGIT_RUNNER_CONCURRENCY must be >= 1")
	}
	return nil
}

// IsProduction reports whether production-only safeguards should apply.
func (c *Config) IsProduction() bool { return c.Environment == "production" }

// loadEnvFile reads a KEY=VALUE file into the environment for variables that
// are not already set.
//
// Why this exists: OpenSSH runs AuthorizedKeysCommand and forced commands with a
// deliberately minimal environment, so a container's DOGIT_* variables never
// reach dogit on a git connection. The container entrypoint therefore writes
// those variables to a file, and Load picks them up from there.
func loadEnvFile() {
	path := os.Getenv("DOGIT_ENV_FILE")
	if path == "" {
		path = "/etc/dogit/env"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" || !validEnvKey(key) {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, strings.Trim(value, `"'`))
	}
}

func validEnvKey(key string) bool {
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '_':
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			return d
		}
	}
	return def
}
