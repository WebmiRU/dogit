package main

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// Which flags a build passes is decided by asking the builder, because the two of them
// belong to BuildKit and a host with only the legacy builder refuses the whole command over
// the first one — no build at all, and a message naming a flag rather than the missing
// component.
//
// These are two fake builders rather than the real docker: the question is what the runner
// does with the answer, and the answer is decided by what the help text says.
func TestOnlyTheFlagsTheBuilderKnowsArePassed(t *testing.T) {
	const help = `Usage:  docker build [OPTIONS] PATH | URL | -

Options:
      --progress string   Set type of progress output (auto, plain, tty, rawjson)
      --provenance string  Add a provenance attestation
`
	for _, c := range []struct {
		name       string
		help       string
		progress   bool
		provenance bool
	}{
		{"a builder that knows both", help, true, true},
		{"a builder that knows neither", "Usage:  docker build [OPTIONS] PATH\n", false, false},
		{"a builder that knows one of them",
			"      --provenance string  Add a provenance attestation\n", false, true},
	} {
		dir := t.TempDir()
		printed := filepath.Join(dir, "asked")
		binary := filepath.Join(dir, "docker")
		script := "#!/bin/sh\nif [ \"$1\" = build ] && [ \"$2\" = --help ]; then\n" +
			"  cat <<'HELP'\n" + c.help + "HELP\n  exit 0\nfi\n" +
			"  echo \"$@\" > " + printed + "\nexit 0\n"
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatalf("write the fake docker: %v", err)
		}

		cfg := config{dockerBinary: binary}
		// A fresh question for every case: the answer is remembered for the life of the
		// process, which is what makes it one call rather than one per build.
		builderFlags.once = sync.Once{}
		builderFlags.known = nil
		builderFlags.checked = false

		if got := buildFlagKnown(cfg, "--progress"); got != c.progress {
			t.Errorf("%s: --progress passed: %v, want %v", c.name, got, c.progress)
		}
		if got := buildFlagKnown(cfg, "--provenance"); got != c.provenance {
			t.Errorf("%s: --provenance passed: %v, want %v", c.name, got, c.provenance)
		}
	}
}

// A docker that cannot be asked at all is a docker that knows nothing, and the build then
// carries no BuildKit flag rather than one guessed at.
func TestABuilderThatCannotBeAskedIsTreatedAsKnowingNothing(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "docker")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatalf("write the fake docker: %v", err)
	}
	builderFlags.once = sync.Once{}
	builderFlags.known = nil
	builderFlags.checked = false

	cfg := config{dockerBinary: binary}
	if buildFlagKnown(cfg, "--progress") || buildFlagKnown(cfg, "--provenance") {
		t.Error("a builder that could not be asked was taken to know the flags")
	}
}
