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

	// AllowedOrigins lists browser origins that may use a cookie session for
	// state-changing requests. An empty list means cross-origin browser requests
	// are rejected, which is the correct default for a self-hosted instance
	// served from a single hostname.
	AllowedOrigins []string

	// Misc
	GitBinary string
	// HookBinary is the dogit-hook executable that OpenSSH force-executes.
	HookBinary string
	// SSHHost is the hostname shown in clone URLs.
	SSHHost string
	// SSHPort is the port clients connect to. It only appears in clone URLs when
	// it is not the standard 22, which is the case during development.
	SSHPort       int
	GitAuthorName string
	GitAuthorMail string
	AuthTokenTTL  time.Duration
	LogLevel      string
	// HookLogLevel applies to the git entry points. Their stderr is the client's
	// "remote:" stream, so they default to warnings even when the server logs at
	// debug level.
	HookLogLevel string
	Environment  string
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
		SSHHost:            env("DOGIT_SSH_HOST", "localhost"),
		SSHPort:            envInt("DOGIT_SSH_PORT", 22),
		GitAuthorName:      env("DOGIT_GIT_AUTHOR_NAME", "dogit"),
		GitAuthorMail:      env("DOGIT_GIT_AUTHOR_MAIL", "dogit@localhost"),
		AuthTokenTTL:       envDuration("DOGIT_AUTH_TOKEN_TTL", 720*time.Hour),
		AllowedOrigins:     envList("DOGIT_ALLOWED_ORIGINS", defaultOrigins(env("DOGIT_ENV", "development"))),
		LogLevel:           env("DOGIT_LOG_LEVEL", "info"),
		HookLogLevel:       env("DOGIT_HOOK_LOG_LEVEL", "warn"),
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
	for name, level := range map[string]string{
		"DOGIT_LOG_LEVEL":      c.LogLevel,
		"DOGIT_HOOK_LOG_LEVEL": c.HookLogLevel,
	} {
		switch level {
		case "debug", "info", "warn", "error":
		default:
			return fmt.Errorf("%s: unknown level %q (want debug, info, warn or error)", name, level)
		}
	}
	if strings.TrimSpace(c.DatabaseURL) == "" {
		return fmt.Errorf("DOGIT_DATABASE_URL must not be empty")
	}
	if strings.TrimSpace(c.SSHHost) == "" {
		return fmt.Errorf("DOGIT_SSH_HOST must not be empty")
	}
	if c.SSHPort < 1 || c.SSHPort > 65535 {
		return fmt.Errorf("DOGIT_SSH_PORT must be between 1 and 65535")
	}
	if strings.TrimSpace(c.HookBinary) == "" {
		return fmt.Errorf("DOGIT_HOOK_BINARY must not be empty")
	}
	if c.RunnerConcurrency < 1 {
		return fmt.Errorf("DOGIT_RUNNER_CONCURRENCY must be >= 1")
	}
	return nil
}

// CloneURL returns a copy-pasteable git clone URL for a project path.
//
// The scp-like form has no place for a port, so a non-standard port forces the
// ssh:// form. Both are accepted by every git client; only one of them can be
// correct for a given port.
func (c *Config) CloneURL(projectPath string) string {
	path := strings.Trim(projectPath, "/")
	if path == "" {
		return ""
	}
	if c.SSHPort == 22 {
		return fmt.Sprintf("git@%s:%s.git", c.SSHHost, path)
	}
	return fmt.Sprintf("ssh://git@%s:%d/%s.git", c.SSHHost, c.SSHPort, path)
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

// defaultOrigins lists the origins the development stack serves the UI from.
// In production the value must be set explicitly to the real hostname.
func defaultOrigins(environment string) []string {
	if environment != "development" {
		return nil
	}
	return []string{
		"http://localhost:3000", // Nuxt dev server
		"http://localhost:8080", // API served without the UI
	}
}

// envList parses a comma-separated environment variable.
func envList(key string, def []string) []string {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
