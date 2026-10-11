package main

import "testing"

func TestNamedOutputPhase(t *testing.T) {
	build := map[string]any{"__dogit_output_name": "web"}
	if got := namedOutputPhase(build, "build"); got != "build:web" {
		t.Fatalf("build phase = %q, want build:web", got)
	}
	if got := namedOutputPhase(build, "push"); got != "push:web" {
		t.Fatalf("push phase = %q, want push:web", got)
	}
	if got := namedOutputPhase(nil, "build"); got != "build" {
		t.Fatalf("unnamed phase = %q, want build", got)
	}
}

func TestNamedOutputMessages(t *testing.T) {
	registry := map[string]any{"url": "https://registry.f220.ru"}
	if got := namedOutputMessage("web", "push", "pushing to registry.f220.ru under latest", registry); got != "pushing image web to registry.f220.ru" {
		t.Fatalf("push message = %q", got)
	}
	if got := namedOutputMessage("api", "build", "building Dockerfile from .", registry); got != "building image api — Dockerfile from ." {
		t.Fatalf("build message = %q", got)
	}
	if got := namedOutputMessage("", "build", "building Dockerfile from .", registry); got != "building Dockerfile from ." {
		t.Fatalf("unnamed message = %q", got)
	}
}
