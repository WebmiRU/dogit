package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// buildTar renders files as a tar stream, the shape the Docker runtime collects
// artifacts in.
func buildTar(t *testing.T, files map[string]string) *bytes.Buffer {
	t.Helper()

	var out bytes.Buffer
	writer := tar.NewWriter(&out)

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sortStrings(names)

	for _, name := range names {
		content := files[name]
		header := &tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &out
}

// dockerAvailable reports whether a docker daemon answers, so the integration test
// can be skipped on a machine without one.
func dockerAvailable(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}").Run(); err != nil {
		t.Skip("the docker daemon is not reachable")
	}
}

func TestScriptBodyEchoesAndStopsAtTheFirstFailure(t *testing.T) {
	body := scriptBody(JobSpec{Script: []string{"echo one", "false", "echo two"}})

	// The steps are echoed so the log shows which command failed, and the shell
	// stops at the first failure: a CI job that keeps going after an error would
	// report success on a broken build.
	if !strings.HasPrefix(body, "set -e\n") {
		t.Errorf("the script does not stop at the first failure: %q", body)
	}
	if !strings.Contains(body, `echo "$ echo one"`) {
		t.Error("a step is not echoed")
	}
	steps := strings.Count(body, "\n")
	if steps < 6 {
		t.Errorf("expected every step to be emitted, got %q", body)
	}
}

func TestImageTag(t *testing.T) {
	cases := map[string]string{
		"":                     "alpine:3.21",
		"alpine":               "alpine:latest",
		"alpine:3.20":          "alpine:3.20",
		"golang":               "golang:latest",
		"registry:5000/app":    "registry:5000/app:latest",
		"registry:5000/app:v1": "registry:5000/app:v1",
		"ghcr.io/owner/img":    "ghcr.io/owner/img:latest",
		"ghcr.io/owner/img:v2": "ghcr.io/owner/img:v2",
		"image@sha256:abc":     "image@sha256:abc",
	}

	for image, want := range cases {
		if got := ImageTag(image); got != want {
			t.Errorf("ImageTag(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestSanitiseProducesValidContainerNames(t *testing.T) {
	cases := map[string]string{
		"build":            "build",
		"Build And Deploy": "build-and-deploy",
		"test/unit":        "test-unit",
		"":                 "job",
		"///":              "job",
	}

	for input, want := range cases {
		if got := sanitise(input); got != want {
			t.Errorf("sanitise(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestContainerNameIsStableAndUnique(t *testing.T) {
	first := containerName(JobSpec{ID: 7, Name: "Unit Tests"})
	second := containerName(JobSpec{ID: 8, Name: "Unit Tests"})

	// The job id has to be part of the name, or two runs of the same pipeline would
	// collide on one container.
	if first == second {
		t.Error("two jobs produced the same container name")
	}
	if first != containerName(JobSpec{ID: 7, Name: "Unit Tests"}) {
		t.Error("container names must be stable for a given job")
	}
}

func TestUnpackTarStripsTheLeadingComponent(t *testing.T) {
	archive := buildTar(t, map[string]string{
		"./out/artifact.txt": "hello",
		"./out/nested/other": "world",
	})

	objects, err := unpackTar(archive)
	if err != nil {
		t.Fatalf("unpack: %v", err)
	}

	if got := string(objects["out/artifact.txt"]); got != "hello" {
		t.Errorf("artifact = %q, want hello", got)
	}
	if got := string(objects["out/nested/other"]); got != "world" {
		t.Errorf("nested artifact = %q, want world", got)
	}
	// Directories and metadata entries are not artifacts.
	for key := range objects {
		if strings.HasSuffix(key, "/") {
			t.Errorf("a directory was returned as an artifact: %q", key)
		}
	}
}

func TestLogWriterPrefixesAndSerialises(t *testing.T) {
	var out bytes.Buffer
	writer := NewLogWriter(&out, "[job 1]")

	writer.Line("starting")
	if !strings.Contains(out.String(), "job 1") {
		t.Errorf("the prefix is missing: %q", out.String())
	}
	if !strings.Contains(out.String(), "starting") {
		t.Errorf("the message is missing: %q", out.String())
	}
}

func TestLogWriterCopiesLongLines(t *testing.T) {
	var out bytes.Buffer
	writer := NewLogWriter(&out, "")

	// Build output can emit a single line far longer than a scanner's default
	// buffer; truncating it would silently lose the end of the error message.
	long := strings.Repeat("x", 200_000)

	var source bytes.Buffer
	source.WriteString(long)
	source.WriteString("\n")
	source.WriteString("after\n")

	if err := writer.Copy(&source); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if !strings.Contains(out.String(), long) {
		t.Error("a long line was truncated")
	}
	if !strings.Contains(out.String(), "after") {
		t.Error("content after the long line was lost")
	}
}

// The Docker runtime end to end: a real container runs a real script, its output is
// captured, and artifacts are collected out of it.
func TestDockerRuntimeRunsAJob(t *testing.T) {
	dockerAvailable(t)

	runtime := NewDocker(DockerOptions{Binary: "docker"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spec := JobSpec{
		ID:   900001,
		Name: "smoke test",
		// The script creates an artifact so Collect has something to retrieve.
		Script: []string{
			"mkdir -p /build/out",
			"echo 'built by dogit' > /build/out/result.txt",
			"echo hello from the job",
		},
		Environment: map[string]string{"CI": "true"},
		Image:       "alpine:3.21",
		Timeout:     time.Minute,
	}

	handle, err := runtime.Prepare(ctx, spec)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer runtime.Destroy(ctx, handle)

	var logs, errors bytes.Buffer
	result := runtime.Stream(ctx, handle, spec, &logs, &errors)

	if result.Status != StatusSuccess {
		t.Fatalf("status = %s, error = %v\nlogs:\n%s", result.Status, result.Error, logs.String())
	}
	if result.Duration <= 0 {
		t.Error("duration was not measured")
	}
	if !strings.Contains(logs.String(), "hello from the job") {
		t.Errorf("the job's output is missing from the log:\n%s", logs.String())
	}

	artifacts, err := runtime.Collect(ctx, handle, spec, []string{"build/out"})
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if got := string(artifacts["build/out/result.txt"]); !strings.Contains(got, "built by dogit") {
		t.Errorf("artifact = %q", got)
	}
}

func TestDockerRuntimeReportsAFailingScript(t *testing.T) {
	dockerAvailable(t)

	runtime := NewDocker(DockerOptions{Binary: "docker"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spec := JobSpec{
		ID:      900002,
		Name:    "failing job",
		Script:  []string{"echo before", "exit 3", "echo after"},
		Image:   "alpine:3.21",
		Timeout: time.Minute,
	}

	handle, err := runtime.Prepare(ctx, spec)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer runtime.Destroy(ctx, handle)

	var logs, errors bytes.Buffer
	result := runtime.Stream(ctx, handle, spec, &logs, &errors)

	if result.Status != StatusFailed {
		t.Errorf("status = %s, want failed", result.Status)
	}
	if result.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", result.ExitCode)
	}
	if strings.Contains(logs.String(), "after") {
		t.Error("the script continued past the failing step")
	}
	if !strings.Contains(logs.String(), "before") {
		t.Errorf("output before the failure is missing:\n%s", logs.String())
	}
}

func TestDockerRuntimeEnforcesTheTimeout(t *testing.T) {
	dockerAvailable(t)

	runtime := NewDocker(DockerOptions{Binary: "docker"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	spec := JobSpec{
		ID:      900003,
		Name:    "slow job",
		Script:  []string{"sleep 60"},
		Image:   "alpine:3.21",
		Timeout: 3 * time.Second,
	}

	handle, err := runtime.Prepare(ctx, spec)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer runtime.Destroy(ctx, handle)

	var logs, errors bytes.Buffer
	result := runtime.Stream(ctx, handle, spec, &logs, &errors)

	// A job that runs past its deadline must fail, not linger: a pipeline that hangs
	// is worse than one that reports a timeout.
	if result.Status != StatusFailed {
		t.Errorf("status = %s, want failed", result.Status)
	}
	if result.ExitCode != 124 {
		t.Errorf("exit code = %d, want 124 for a timeout", result.ExitCode)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "timeout") {
		t.Errorf("error = %v, want a timeout diagnostic", result.Error)
	}
}

// "docker cp" prefixes a copied directory's entries with the directory's own name;
// artifacts are addressed by path, so the component has to go.
func TestRebaseKeysDropsTheDirectoryPrefix(t *testing.T) {
	withPrefix := map[string][]byte{
		"out/result.txt": []byte("a"),
		"out/nested/one": []byte("b"),
	}
	stripped := rebaseKeys(withPrefix, "build/out")

	if _, ok := stripped["result.txt"]; !ok {
		t.Errorf("the prefix was not dropped: %v", keysOf(stripped))
	}
	if _, ok := stripped["nested/one"]; !ok {
		t.Errorf("a nested entry was lost: %v", keysOf(stripped))
	}

	// Entries that do not all carry the prefix are left alone: dropping a
	// component from only some of them would corrupt the rest.
	mixed := map[string][]byte{
		"out/result.txt": []byte("a"),
		"loose.txt":      []byte("b"),
	}
	if got := rebaseKeys(mixed, "build/out"); len(got) != 2 {
		t.Errorf("mixed entries were modified: %v", keysOf(got))
	}

	// A single file copied by name needs no rebasing.
	single := map[string][]byte{"result.txt": []byte("a")}
	if got := rebaseKeys(single, "build/out/result.txt"); len(got) != 1 {
		t.Errorf("a single file was modified: %v", keysOf(got))
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	sortStrings(out)
	return out
}
