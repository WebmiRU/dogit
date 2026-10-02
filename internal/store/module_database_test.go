package store

import (
	"strings"
	"testing"
)

// Identifiers cannot be passed as query parameters, so every value that ends up in
// a statement is reduced to a conservative alphabet first. These tests are the
// guard for that: a module kind arrives from an HTTP request.
func TestSanitiseIdentifier(t *testing.T) {
	cases := map[string]string{
		"registry:docker":       "registry_docker",
		"cache:npm":             "cache_npm",
		"builder:docker":        "builder_docker",
		"":                      "",
		"UPPER":                 "upper",
		"a-b.c":                 "a_b_c",
		`weird"'; drop`:         "weird____drop",
		strings.Repeat("x", 60): strings.Repeat("x", 24),
	}

	for input, want := range cases {
		got := sanitiseIdentifier(input)
		if got != want {
			t.Errorf("sanitiseIdentifier(%q) = %q, want %q", input, got, want)
		}

		// Whatever the input, the result must be safe to interpolate.
		for _, r := range got {
			isSafe := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_'
			if !isSafe {
				t.Errorf("sanitiseIdentifier(%q) produced an unsafe character %q", input, r)
			}
		}
	}
}

func TestQuoteIdentifierAndLiteral(t *testing.T) {
	if got := quoteIdentifier(`bad"name`); got != `"bad""name"` {
		t.Errorf("quoteIdentifier = %q", got)
	}
	if got := quoteLiteral("it's"); got != `'it''s'` {
		t.Errorf("quoteLiteral = %q", got)
	}
}

func TestBuildModuleDSNPointsAtTheModuleDatabase(t *testing.T) {
	dsn := buildModuleDSN(
		"postgres://dogit:app-password-must-not-leak@postgres:5432/dogit?sslmode=disable",
		"dogit_registry_docker_abc123",
		"dogit_registry_docker_abc123",
		"module-secret",
	)

	if !strings.Contains(dsn, "dogit_registry_docker_abc123") {
		t.Errorf("the module database is missing from %q", dsn)
	}
	// The application database and its credentials must not leak into the string
	// handed to a module: it would then be able to reach the core's tables.
	if strings.Contains(dsn, "/dogit?") {
		t.Errorf("the application database survived in %q", dsn)
	}
	if strings.Contains(dsn, "app-password-must-not-leak") {
		t.Errorf("the application password survived in %q", dsn)
	}
	if strings.Contains(dsn, "dogit@") || strings.Contains(dsn, ":dogit:") {
		t.Errorf("the application user survived in %q", dsn)
	}
	if !strings.Contains(dsn, "module-secret") {
		t.Errorf("the module password is missing from %q", dsn)
	}
}

// The keyword/value form has to be rewritten too: a deployment configured that way
// would otherwise hand out the application's connection string unchanged.
func TestBuildModuleDSNHandlesKeywordForm(t *testing.T) {
	dsn := buildModuleDSN(
		"host=postgres port=5432 dbname=dogit user=dogit password=secret sslmode=disable",
		"dogit_cache_demo_abc123",
		"dogit_cache_demo_abc123",
		"module-secret",
	)

	if !strings.Contains(dsn, "dbname=dogit_cache_demo_abc123") {
		t.Errorf("the module database is missing from %q", dsn)
	}
	if strings.Contains(dsn, "dbname=dogit ") || strings.Contains(dsn, "user=dogit ") {
		t.Errorf("the application credentials survived in %q", dsn)
	}
	if !strings.Contains(dsn, "password=module-secret") {
		t.Errorf("the module password is missing from %q", dsn)
	}
}

func TestRandomPasswordIsLongAndAlphanumeric(t *testing.T) {
	first, err := randomPassword()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Errorf("password length = %d, want 32", len(first))
	}

	for _, r := range first {
		isSafe := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !isSafe {
			t.Errorf("password contains %q, which needs escaping", r)
		}
	}

	second, err := randomPassword()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("two generated passwords are identical")
	}
}
