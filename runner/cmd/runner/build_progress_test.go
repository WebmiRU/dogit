package main

import "testing"

func TestNamedOutputProgressPhases(t *testing.T) {
	build := map[string]any{"__dogit_output_name": "api"}
	if got := namedOutputPhase(build, "build"); got != "build:api" {
		t.Fatalf("build phase = %q, want build:api", got)
	}
	if got := namedOutputPhase(build, "push"); got != "push:api" {
		t.Fatalf("push phase = %q, want push:api", got)
	}
	if got := namedOutputPhase(nil, "build"); got != "build" {
		t.Fatalf("unnamed build phase = %q, want build", got)
	}
}

func TestBuildKitExporterStartsPushPhase(t *testing.T) {
	for _, name := range []string{
		"exporting to image",
		"exporting layers",
		"pushing manifest sha256:1234",
	} {
		if !isPushProgressStep(name) {
			t.Errorf("%q was not recognised as image export/push", name)
		}
	}
	for _, name := range []string{"[internal] load build definition", "RUN go build"} {
		if isPushProgressStep(name) {
			t.Errorf("%q was incorrectly recognised as export/push", name)
		}
	}
}
