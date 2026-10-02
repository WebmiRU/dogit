package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// clearEnv removes every DOGIT_ variable so each test starts from a known state.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "DOGIT_") {
			key, _, _ := strings.Cut(entry, "=")
			t.Setenv(key, "")
			_ = os.Unsetenv(key)
		}
	}
	// Unsetenv through t.Setenv is not possible, so remove the leftovers directly
	// and restore them at the end of the test.
	t.Cleanup(func() {})
}

func TestLoadUsesDefaults(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// These defaults are what a bare `docker compose up` relies on; a regression
	// here silently produces broken clone URLs and rejected cookies.
	if cfg.SSHHost != "localhost" {
		t.Errorf("SSHHost = %q, want localhost", cfg.SSHHost)
	}
	if cfg.HookBinary != "dogit-hook" {
		t.Errorf("HookBinary = %q, want dogit-hook", cfg.HookBinary)
	}
	if cfg.Environment != "development" {
		t.Errorf("Environment = %q, want development", cfg.Environment)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if got := cfg.CloneURL("group/hello"); got != "git@localhost:group/hello.git" {
		t.Errorf("CloneURL() = %q, want the portless form on port 22", got)
	}

	// Derived paths must be filled in when not set explicitly.
	if cfg.RepoDir != cfg.DataDir+"/repos" {
		t.Errorf("RepoDir = %q, want %q", cfg.RepoDir, cfg.DataDir+"/repos")
	}
	if cfg.ArtifactDir != cfg.DataDir+"/artifacts" {
		t.Errorf("ArtifactDir = %q", cfg.ArtifactDir)
	}
	if cfg.SSHHostKey != cfg.DataDir+"/ssh/host_ed25519" {
		t.Errorf("SSHHostKey = %q", cfg.SSHHostKey)
	}
}

func TestLoadReadsEnvironment(t *testing.T) {
	clearEnv(t)
	t.Setenv("DOGIT_SSH_HOST", "git.example.com")
	t.Setenv("DOGIT_ENV", "production")
	t.Setenv("DOGIT_RUNNER_CONCURRENCY", "8")
	t.Setenv("DOGIT_SSH_PORT", "2222")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SSHHost != "git.example.com" {
		t.Errorf("SSHHost = %q", cfg.SSHHost)
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() = false for DOGIT_ENV=production")
	}
	if cfg.RunnerConcurrency != 8 {
		t.Errorf("RunnerConcurrency = %d, want 8", cfg.RunnerConcurrency)
	}
	// A non-standard port forces the ssh:// form: the scp-like syntax has nowhere
	// to put a port, and a malformed URL is worse than a verbose one.
	if got := cfg.CloneURL("hello"); got != "ssh://git@git.example.com:2222/hello.git" {
		t.Errorf("CloneURL() = %q, want the ssh:// form with the port", got)
	}
}

func TestProductionHasNoDefaultAllowedOrigins(t *testing.T) {
	clearEnv(t)
	t.Setenv("DOGIT_ENV", "production")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Accepting the development origins in production would let any local
	// process drive a production session.
	if len(cfg.AllowedOrigins) != 0 {
		t.Errorf("AllowedOrigins = %v, want empty in production", cfg.AllowedOrigins)
	}
}

func TestAllowedOriginsParsing(t *testing.T) {
	clearEnv(t)
	t.Setenv("DOGIT_ENV", "production")
	t.Setenv("DOGIT_ALLOWED_ORIGINS", " https://a.example , https://b.example ,, ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	want := []string{"https://a.example", "https://b.example"}
	if len(cfg.AllowedOrigins) != len(want) {
		t.Fatalf("AllowedOrigins = %v, want %v", cfg.AllowedOrigins, want)
	}
	for i := range want {
		if cfg.AllowedOrigins[i] != want[i] {
			t.Errorf("AllowedOrigins[%d] = %q, want %q", i, cfg.AllowedOrigins[i], want[i])
		}
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{"unknown environment", map[string]string{"DOGIT_ENV": "staging"}},
		{"unknown log level", map[string]string{"DOGIT_LOG_LEVEL": "verbose"}},
		{"zero concurrency", map[string]string{"DOGIT_RUNNER_CONCURRENCY": "0"}},
		{"empty database url", map[string]string{"DOGIT_DATABASE_URL": " "}},
		{"port out of range", map[string]string{"DOGIT_SSH_PORT": "70000"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if _, err := Load(); err == nil {
				t.Error("expected validation to fail")
			}
		})
	}
}

func TestCloneURLHandlesEdgeCases(t *testing.T) {
	clearEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.CloneURL("/group/hello/"); got != "git@localhost:group/hello.git" {
		t.Errorf("leading and trailing slashes are not trimmed: %q", got)
	}
	if got := cfg.CloneURL(""); got != "" {
		t.Errorf("an empty path must produce an empty URL, got %q", got)
	}
}

func TestLoadEnvFileSuppliesValues(t *testing.T) {
	clearEnv(t)

	// OpenSSH runs forced commands with a minimal environment, so the container
	// entrypoint writes the settings to a file. Values already present in the
	// environment must win over the file.
	dir := t.TempDir()
	path := filepath.Join(dir, "env")
	contents := strings.Join([]string{
		"# comment",
		"DOGIT_SSH_HOST=from-file",
		"DOGIT_ENV=production",
		"",
		"not a valid line",
		"DOGIT_LOG_LEVEL=warn",
	}, "\n")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOGIT_ENV_FILE", path)
	t.Setenv("DOGIT_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.SSHHost != "from-file" {
		t.Errorf("SSHHost = %q, want from-file", cfg.SSHHost)
	}
	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want production", cfg.Environment)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q; the environment must win over the file", cfg.LogLevel)
	}
}
