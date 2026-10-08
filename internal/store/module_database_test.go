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

// A module is told where the cluster is, and it is told it from the configuration rather than
// from the connection the core happens to have open. The two are usually the same and are not
// always: a core configured with a unix socket hands out no host at all, and a module reading
// the core's own socket path would be reading a path inside the core's container.
func TestDsnEndpointFromURL(t *testing.T) {
	cases := []struct {
		dsn  string
		host string
		port uint16
	}{
		{"postgres://dogit:secret@postgres:5432/dogit?sslmode=disable", "postgres", 5432},
		{"postgresql://dogit:secret@db.internal/dogit", "db.internal", 5432},
		{"mysql://u:p@mysql:3306/app", "mysql", 3306},
		{"postgres://u:p@[::1]:5432/app", "::1", 5432},
	}
	for _, c := range cases {
		host, port, err := dsnEndpoint(c.dsn)
		if err != nil {
			t.Errorf("dsnEndpoint(%q): %v", c.dsn, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("dsnEndpoint(%q) = %s:%d, want %s:%d", c.dsn, host, port, c.host, c.port)
		}
	}
}

// The keyword/value form is what a deployment configured without a URL ends up with, and the
// port is often left out of it entirely.
func TestDsnEndpointFromKeywords(t *testing.T) {
	host, port, err := dsnEndpoint(
		"host=postgres port=5432 dbname=dogit user=dogit password=secret sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if host != "postgres" || port != 5432 {
		t.Fatalf("dsnEndpoint = %s:%d, want postgres:5432", host, port)
	}

	host, port, err = dsnEndpoint("host=db.internal user=dogit dbname=dogit")
	if err != nil {
		t.Fatal(err)
	}
	if host != "db.internal" || port != 5432 {
		t.Fatalf("a DSN with no port should fall back to the one the driver assumes, got %s:%d",
			host, port)
	}
}

// A port that is not a number is refused rather than passed on as zero: a module handed port 0
// gets a connection refused and blames the cluster, and the cluster is fine.
func TestDsnEndpointRefusesAPortThatIsNotOne(t *testing.T) {
	if _, _, err := dsnEndpoint("postgres://u:p@db:not-a-port/app"); err == nil {
		t.Fatal("a port that is not a number was accepted")
	}
	if _, _, err := dsnEndpoint("this is not a DSN at all"); err == nil {
		t.Fatal("a DSN that names no host was accepted")
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
