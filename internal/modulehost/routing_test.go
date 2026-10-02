package modulehost

import (
	"strings"
	"testing"

	"github.com/ewolf/dogit/internal/models"
)

func TestBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		routing models.RoutingSpec
		want    string
		wantOK  bool
	}{
		{
			name:    "a module that asked for nothing is not published",
			host:    "git.example.com",
			routing: models.RoutingSpec{},
		},
		{
			// The case the registry needs: a client reads everything before the
			// first slash as a host, so this has to be a whole name.
			name:    "a subdomain of the instance",
			host:    "git.example.com",
			routing: models.RoutingSpec{Domains: []string{"registry.{host}"}},
			want:    "https://registry.git.example.com",
			wantOK:  true,
		},
		{
			// The case that needs no DNS and no certificate of its own, which is the
			// one a self-hosted installation on a single name usually wants.
			name:    "a prefix on the instance's own name",
			host:    "git.example.com",
			routing: models.RoutingSpec{Path: "packages"},
			want:    "https://git.example.com/packages",
			wantOK:  true,
		},
		{
			name: "a prefix under a name of its own",
			host: "git.example.com",
			routing: models.RoutingSpec{
				Domains: []string{"images.{host}"},
				Path:    "docker",
			},
			want:   "https://images.git.example.com/docker",
			wantOK: true,
		},
		{
			name: "slashes around the prefix do not double up",
			host: "git.example.com",
			routing: models.RoutingSpec{
				Domains: []string{"images.{host}"},
				Path:    "/docker/",
			},
			want:   "https://images.git.example.com/docker",
			wantOK: true,
		},
		{
			// A literal name is an administrator's decision and is left alone.
			name:    "a literal name",
			host:    "git.example.com",
			routing: models.RoutingSpec{Domains: []string{"registry.internal.example.net"}},
			want:    "https://registry.internal.example.net",
			wantOK:  true,
		},
		{
			// Without a public host there is nothing to expand, and pretending
			// otherwise would hand out an address with a brace in it.
			name:    "no instance host leaves the template alone",
			host:    "",
			routing: models.RoutingSpec{Domains: []string{"registry.{host}"}},
			want:    "https://registry.{host}",
			wantOK:  true,
		},
		{
			name:    "an explicit scheme is taken from the module",
			host:    "git.example.com",
			routing: models.RoutingSpec{Domains: []string{"http://registry.{host}"}},
			want:    "http://registry.git.example.com",
			wantOK:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := BaseURL(tc.host, tc.routing)
			if ok != tc.wantOK {
				t.Fatalf("published = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.want {
				t.Errorf("address = %q, want %q", got, tc.want)
			}
		})
	}
}

// Several names may serve one module. The first is the one to show, and it must
// be the same one everywhere it appears, or people will be told to push to one
// address and shown another.
func TestSeveralNamesForOneModule(t *testing.T) {
	routing := models.RoutingSpec{Domains: []string{"registry.{host}", "images.{host}"}}

	base, ok := BaseURL("git.example.com", routing)
	if !ok || base != "https://registry.git.example.com" {
		t.Fatalf("address = %q (published=%v)", base, ok)
	}

	names := routing.PublicDomains("git.example.com")
	if len(names) != 2 || names[1] != "images.git.example.com" {
		t.Errorf("names = %v", names)
	}
}

// Whether a name of its own is needed is a decision an operator makes with DNS and
// certificates, so the core says which it is rather than leaving them to guess
// from a config file.
func TestWhetherAModuleNeedsANameOfItsOwn(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		routing models.RoutingSpec
		want    bool
	}{
		{"a subdomain", "git.example.com",
			models.RoutingSpec{Domains: []string{"registry.{host}"}}, true},
		{"a prefix on the instance name", "git.example.com",
			models.RoutingSpec{Path: "packages"}, false},
		{"a prefix on a name of its own still needs the name", "git.example.com",
			models.RoutingSpec{Domains: []string{"images.{host}"}, Path: "docker"}, true},
		{"the instance's own name", "git.example.com",
			models.RoutingSpec{Domains: []string{"{host}"}}, false},
		{"nothing asked for", "git.example.com", models.RoutingSpec{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDedicatedHost(tc.host, tc.routing); got != tc.want {
				t.Errorf("needs a name of its own = %v, want %v", got, tc.want)
			}
		})
	}
}

// A module that said nothing is left out of the proxy configuration entirely.
// Publishing it under the main name because it happened to be running would put
// somebody else's service in front of our users.
func TestOnlyModulesThatAskedAreDescribed(t *testing.T) {
	quiet := &models.Integration{Name: "quiet", Endpoint: "http://quiet:9000"}
	loud := &models.Integration{
		Name:     "registry",
		Kind:     "registry:docker",
		Endpoint: "http://registry:5000",
	}
	loud.Capabilities.Routing = models.RoutingSpec{Domains: []string{"registry.{host}"}}

	routes := Describe("git.example.com", []*models.Integration{quiet, loud})
	if len(routes) != 1 {
		t.Fatalf("described %d routes, want 1", len(routes))
	}
	if routes[0].Upstream != "http://registry:5000" {
		t.Errorf("upstream = %q", routes[0].Upstream)
	}
}

// The generated proxy configuration is what both Docker and Kubernetes consume,
// so it has to stand on its own without anything a developer would add by hand.
func TestNginxServerBlocks(t *testing.T) {
	blocks := NginxServerBlocks([]Route{{
		ModuleID: "abc",
		Kind:     "registry:docker",
		Name:     "registry",
		Upstream: "http://registry:5000",
		Domains:  []string{"registry.git.example.com"},
	}}, "")

	for _, want := range []string{
		"listen 80;",
		"server_name registry.git.example.com;",
		"set $module_upstream http://registry:5000;",
		"proxy_set_header X-Forwarded-Proto $scheme;",
		// A layer is uploaded as one large request; a manifest may be read as a
		// stream. Buffering either apart is what makes docker clients hang.
		"client_max_body_size 0;",
		"proxy_request_buffering off;",
		// The upstream is a variable so a recreated module container is found
		// again, rather than nginx talking to an address it no longer has.
		"set $module_upstream",
	} {
		if !strings.Contains(blocks, want) {
			t.Errorf("the configuration says nothing about %q:\n%s", want, blocks)
		}
	}

	// A module that did not ask for connection upgrades must not get them, or it
	// can answer a plain request with a stream it cannot close.
	if strings.Contains(blocks, "Upgrade $http_upgrade") {
		t.Error("upgrades were passed to a module that did not ask for them")
	}
}

// A module mounted under a prefix has to be told which prefix it was mounted at,
// or it cannot build an absolute link to itself.
func TestPrefixedModuleIsToldItsMountPoint(t *testing.T) {
	blocks := NginxServerBlocks([]Route{{
		Kind:     "cache:npm",
		Name:     "cache",
		Upstream: "http://cache:8080",
		Domains:  []string{"git.example.com"},
		Path:     "npm",
	}}, "")

	if !strings.Contains(blocks, "proxy_pass $module_upstream/npm/;") {
		t.Errorf("the prefix is not passed on:\n%s", blocks)
	}
	// Without this, a request for /npm with no slash would be served by the
	// single-page app instead of reaching the module.
	if !strings.Contains(blocks, "location = /npm { return 308") {
		t.Errorf("a request for the bare prefix is not redirected:\n%s", blocks)
	}
}

func TestWebsocketModuleIsPassedUpgrades(t *testing.T) {
	blocks := NginxServerBlocks([]Route{{
		Kind:      "build:log",
		Name:      "build log",
		Upstream:  "http://build:8080",
		Domains:   []string{"build.git.example.com"},
		Websocket: true,
	}}, "")

	if !strings.Contains(blocks, "Upgrade $http_upgrade") {
		t.Errorf("a module that asked for a live view is not given one:\n%s", blocks)
	}
}

// With a certificate named, plain HTTP answers with a redirect instead of the
// module: many clients try plain HTTP first, and a registry that answers there
// teaches them to stop trying TLS.
func TestPlainHTTPRedirectsWhenACertificateIsConfigured(t *testing.T) {
	blocks := NginxServerBlocks([]Route{{
		Kind:     "registry:docker",
		Upstream: "http://registry:5000",
		Domains:  []string{"registry.git.example.com"},
	}}, "git.example.com")

	if !strings.Contains(blocks, "listen 443 ssl;") {
		t.Errorf("no TLS server was produced:\n%s", blocks)
	}
	if !strings.Contains(blocks, "return 301 https://registry.git.example.com") {
		t.Errorf("plain HTTP is served rather than redirected:\n%s", blocks)
	}
	if !strings.Contains(blocks, "/.well-known/acme-challenge/") {
		t.Errorf("certificate renewal is not reachable:\n%s", blocks)
	}
}
