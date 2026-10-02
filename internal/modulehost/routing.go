// Package modulehost works out where a module is reachable.
//
// A module that speaks a protocol the browser does not cannot live under a path
// on the main site: `registry.example.com/v2/` is read by the client as a host
// `registry.example.com` and nothing else. So such a module gets its own name on
// the same ports. Others — a page of images, a webhook receiver behind a prefix
// — work perfectly well under a path, and a self-hosted installation often wants
// a module on the main name to avoid another certificate.
//
// Both are supported, and neither is special-cased: the module says what shape it
// wants, one function says what that means here, and the proxy is generated from
// the same answer. The place where this is decided is deliberately one function,
// because two would eventually disagree.
package modulehost

import (
	"fmt"
	"strings"

	"github.com/ewolf/dogit/internal/models"
)

// BaseURL is the address a module is reachable at, and whether it is reachable
// at all.
//
// A module with no routing declaration is reachable only inside the cluster, on
// its own address. That is a normal thing for a module to be — a helper nobody
// visits — and it is why "no address" is not an error here.
func BaseURL(publicHost string, routing models.RoutingSpec) (string, bool) {
	domains := routing.PublicDomains(publicHost)

	// A module that asked only for a prefix is served under the instance's own
	// name: that is the address it is reachable at, and no name of its own is
	// being asked for.
	if len(domains) == 0 {
		if strings.Trim(routing.Path, "/") == "" || publicHost == "" {
			return "", false
		}
		domains = []string{publicHost}
	}

	// Several names may point at one module — a registry reachable at both
	// registry.example.com and images.example.com. The first is what the interface
	// shows and what push instructions tell people to use.
	domain := domains[0]

	// HTTPS unless the module insists otherwise. A module that says "http://" is
	// usually one being developed; a module that says nothing is a service on the
	// same certificate as the instance.
	scheme := "https"
	if explicit, rest, found := strings.Cut(domain, "://"); found {
		scheme, domain = explicit, rest
	}

	path := strings.Trim(routing.Path, "/")
	switch {
	case path == "":
		return scheme + "://" + domain, true
	case strings.Contains(path, "{"):
		// A template inside the path is resolved against the same host, which is
		// what makes "docker" under the instance's own name expressible without
		// repeating it.
		path = strings.ReplaceAll(path, "{host}", publicHost)
		path = strings.ReplaceAll(path, "{domain}", domain)
		if !strings.Contains(path, ".") && !strings.Contains(path, ":") {
			path = domain + "/" + path
		}
		return scheme + "://" + domain + "/" + path, true
	default:
		return scheme + "://" + domain + "/" + path, true
	}
}

// IsDedicatedHost says whether a module needs a name of its own.
//
// This is the decision that matters to an operator: a dedicated name means a DNS
// record and a certificate, and no firewall may be involved at all. A prefix means
// neither.
func IsDedicatedHost(publicHost string, routing models.RoutingSpec) bool {
	domains := routing.PublicDomains(publicHost)
	if len(domains) == 0 {
		return false
	}

	if len(domains) == 0 {
		return false
	}

	for _, domain := range domains {
		// A name that resolves to the instance's own host is the instance's name,
		// not a new one, and needs no certificate of its own. Whether the module
		// sits under a prefix on it does not change that.
		if publicHost != "" && domain == publicHost {
			continue
		}
		return true
	}
	return false
}

// Route is one module as the proxy needs to hear about it.
type Route struct {
	ModuleID  string
	Kind      string
	Name      string
	Upstream  string
	Domains   []string
	Path      string
	Websocket bool
}

// URL is the address this route is reachable at, using the same rules as
// BaseURL so that a route shown in an interface and an address a client was told
// to use cannot differ.
func (r Route) URL() string {
	address, _ := BaseURL("", models.RoutingSpec{Domains: r.Domains, Path: r.Path})
	return address
}

// Describe builds the routes of every module that asked to be reachable.
//
// Modules that declared nothing are left out rather than given the instance's own
// address: a module that did not ask must not end up under the main name just
// because it happened to be running.
func Describe(publicHost string, modules []*models.Integration) []Route {
	routes := []Route{}

	for _, module := range modules {
		routing := module.Capabilities.Routing
		domains := routing.PublicDomains(publicHost)
		if len(domains) == 0 {
			// A module that asked only for a prefix lives on the instance's own
			// name, and is just as published as one with a name of its own.
			if strings.Trim(routing.Path, "/") == "" || publicHost == "" {
				continue
			}
			domains = []string{publicHost}
		}
		routes = append(routes, Route{
			ModuleID:  module.ID.String(),
			Kind:      module.Kind,
			Name:      module.Name,
			Upstream:  strings.TrimRight(module.Endpoint, "/"),
			Domains:   domains,
			Path:      module.Capabilities.Routing.Path,
			Websocket: module.Capabilities.Routing.Websocket,
		})
	}
	return routes
}

// NginxServerBlocks renders routes as nginx server blocks.
//
// The same text serves both deployments: in Docker it is written to a file the
// web service includes, and in Kubernetes it is a ConfigMap behind an Ingress
// controller. Nothing here is specific to either, which is the point — the module
// arrangement must not change when the way it is hosted does.
func NginxServerBlocks(routes []Route, tlsServerName string) string {
	var out strings.Builder

	for _, route := range routes {
		for _, domain := range route.Domains {
			out.WriteString("\n# dogit module: " + route.Kind + " (" + route.Name + ")\n")
			out.WriteString("server {\n")
			out.WriteString("    listen 80;\n")
			if domain != "_" && domain != "" {
				out.WriteString("    server_name " + domain + ";\n")
			}

			// A name of its own is answered with a redirect rather than the module,
			// so that a client which ignores TLS — many do, by default — is told
			// once where to go instead of talking plaintext to a registry.
			if tlsServerName != "" {
				out.WriteString("    location /.well-known/acme-challenge/ { root /var/www/certbot; }\n")
				out.WriteString("    location / { return 301 https://" + domain + `$request_uri; }` + "\n")
				out.WriteString("}\n\n")
				out.WriteString("server {\n")
				out.WriteString("    listen 443 ssl;\n")
				out.WriteString("    http2 on;\n")
				out.WriteString("    server_name " + domain + ";\n")
				out.WriteString("    ssl_certificate     /etc/letsencrypt/live/" + tlsServerName + "/fullchain.pem;\n")
				out.WriteString("    ssl_certificate_key /etc/letsencrypt/live/" + tlsServerName + "/privkey.pem;\n")
			}

			prefix := "/" + strings.Trim(route.Path, "/")
			location := "/"
			if prefix != "/" {
				location = prefix + "/"
				out.WriteString(fmt.Sprintf("    # %s is served under %s on this name.\n", route.Name, prefix))
				out.WriteString("    location = " + prefix + " { return 308 " + prefix + "/; }\n")
			}

			out.WriteString("    location " + location + " {\n")
			out.WriteString("        # A variable rather than an upstream block: nginx resolves upstream\n")
			out.WriteString("        # names once at start-up, and a recreated module container would then\n")
			out.WriteString("        # be talked to at an address it no longer has.\n")
			out.WriteString(fmt.Sprintf("        set $module_upstream %s;\n", route.Upstream))

			// A prefix module receives the address it was mounted at, so a module
			// that builds absolute links does not have to know where it was mounted.
			if prefix != "/" {
				out.WriteString(fmt.Sprintf("        proxy_pass $module_upstream%s/;\n", prefix))
			} else {
				out.WriteString("        proxy_pass $module_upstream$request_uri;\n")
			}

			out.WriteString("        proxy_http_version 1.1;\n")
			out.WriteString("        proxy_set_header Host $host;\n")
			out.WriteString("        proxy_set_header X-Real-IP $remote_addr;\n")
			out.WriteString("        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n")
			out.WriteString("        proxy_set_header X-Forwarded-Proto $scheme;\n")
			if route.Websocket {
				out.WriteString("        proxy_set_header Upgrade $http_upgrade;\n")
				out.WriteString("        proxy_set_header Connection \"upgrade\";\n")
			}

			out.WriteString("        # Registry pushes are a single large layer arriving at once and\n")
			out.WriteString("        # manifests are read as a stream: neither is a small request, and\n")
			out.WriteString("        # buffering them apart would break clients that stream a response.\n")
			out.WriteString("        proxy_request_buffering off;\n")
			out.WriteString("        proxy_buffering off;\n")
			out.WriteString("        client_max_body_size 0;\n")
			out.WriteString("        proxy_read_timeout 3600s;\n")
			out.WriteString("        proxy_send_timeout 3600s;\n")
			out.WriteString("    }\n")
			out.WriteString("}\n")
		}
	}

	return out.String()
}
