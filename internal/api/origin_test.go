package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ewolf/dogit/internal/config"
)

// newOriginTestServer builds a server with only the origin checks configured;
// the database is never reached by the tests below.
func newOriginTestServer(allowedOrigins []string) *Server {
	cfg := &config.Config{AllowedOrigins: allowedOrigins}
	return &Server{cfg: cfg, log: slog.Default()}
}

func requestWith(method string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(method, "/api/v1/projects", nil)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestCookieAllowedForReads(t *testing.T) {
	// Reads cannot change state, so they are always permitted: a cross-site
	// link to a page must still be able to fetch data.
	s := newOriginTestServer(nil)
	if !s.cookieAllowedFor(requestWith(http.MethodGet, nil)) {
		t.Error("a GET without origin headers was rejected")
	}
}

func TestCookieAllowedForSameOriginWrites(t *testing.T) {
	s := newOriginTestServer([]string{"http://localhost:3000"})

	tests := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{
			name:    "browser same-origin",
			headers: map[string]string{"Sec-Fetch-Site": "same-origin"},
			want:    true,
		},
		{
			name:    "explicit matching origin",
			headers: map[string]string{"Origin": "http://localhost:3000"},
			want:    true,
		},
		{
			name:    "cross-site is refused",
			headers: map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "http://localhost:3000"},
			want:    false,
		},
		{
			name:    "foreign origin is refused",
			headers: map[string]string{"Origin": "http://evil.example"},
			want:    false,
		},
		{
			name:    "same-site subdomain is refused",
			headers: map[string]string{"Sec-Fetch-Site": "same-site", "Origin": "http://attacker.localhost:3000"},
			want:    false,
		},
		{
			name:    "referer fallback is accepted",
			headers: map[string]string{"Referer": "http://localhost:3000/projects"},
			want:    true,
		},
		{
			name:    "no signals at all is refused",
			headers: nil,
			want:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.cookieAllowedFor(requestWith(http.MethodPost, tc.headers)); got != tc.want {
				t.Errorf("cookieAllowedFor() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOriginAllowedRejectsPrefixMatches(t *testing.T) {
	// "http://localhost:3000.evil.example" must not pass as a prefix of an
	// allowed origin; the comparison is exact for that reason.
	s := newOriginTestServer([]string{"http://localhost:3000"})

	spoofed := requestWith(http.MethodPost, map[string]string{
		"Origin": "http://localhost:3000.evil.example",
	})
	if s.originAllowed(spoofed) {
		t.Error("a lookalike origin was accepted")
	}

	withPortPrefix := requestWith(http.MethodPost, map[string]string{
		"Origin": "http://localhost:3000123",
	})
	if s.originAllowed(withPortPrefix) {
		t.Error("an origin with an extra suffix was accepted")
	}
}

func TestSafeMethod(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		if !safeMethod(method) {
			t.Errorf("expected %s to be safe", method)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if safeMethod(method) {
			t.Errorf("expected %s to be unsafe", method)
		}
	}
}
