package main

import "testing"

// Where a runner clones from: the host it already reaches the core at, and the port it is
// told to use — never the port the outside world is given, which is a different port inside
// the network.
func TestWhereARunnerClonesFrom(t *testing.T) {
	for _, c := range []struct {
		name      string
		coreURL   string
		sshHost   string
		clonePort int
		want      string
	}{
		{"a runner on the core's own network", "http://app:8080", "localhost", 22, "ssh://git@app"},
		{"with the port said", "http://app:8080", "localhost", 2222, "ssh://git@app:2222"},
		{"a runner across the world", "https://forge.example.com", "forge.example.com", 22,
			"ssh://git@forge.example.com"},
		{"a path and a port in the address", "https://forge.example.com:8443/dogit", "x", 22,
			"ssh://git@forge.example.com"},
		{"nothing to go on but what it was told", "", "localhost", 22, "ssh://git@localhost"},
	} {
		cfg := config{coreURL: c.coreURL, sshHost: c.sshHost, clonePort: c.clonePort}
		if got := cloneAddress(cfg); got != c.want {
			t.Errorf("%s: cloneAddress = %q, want %q", c.name, got, c.want)
		}
	}
}
