package runner

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"strings"
	"sync"
	"time"
)

// Docker runs jobs as containers through the docker CLI.
//
// The CLI is used rather than the API client on purpose: it is what an operator
// already knows how to debug, it avoids pinning the project to a Docker API
// version, and it behaves identically on a workstation and on a server. The cost
// is one extra process per job, which is irrelevant next to the container itself.
type Docker struct {
	binary string
	log    Logger

	mu      sync.Mutex
	handles map[string]Handle
	cancel  map[string]context.CancelFunc
	// keep is a retention window for containers; a job container left behind would
	// keep its writable layer forever.
	keep time.Duration
}

// Logger narrows the dependency to what this file needs, so the runtime can be
// tested without a real logger.
type Logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
	Debug(msg string, args ...any)
}

type DockerOptions struct {
	Binary string
	Logger Logger
	// KeepSeconds is how long a finished job container is retained for debugging.
	KeepSeconds int
}

func NewDocker(opts DockerOptions) *Docker {
	binary := opts.Binary
	if binary == "" {
		binary = "docker"
	}
	if opts.Logger == nil {
		opts.Logger = nopLogger{}
	}
	keep := time.Duration(opts.KeepSeconds) * time.Second
	if keep <= 0 {
		keep = 10 * time.Minute
	}

	return &Docker{
		binary:  binary,
		log:     opts.Logger,
		handles: map[string]Handle{},
		cancel:  map[string]context.CancelFunc{},
		keep:    keep,
	}
}

// KeepWindow reports how long a finished job container is retained.
func (d *Docker) KeepWindow() time.Duration { return d.keep }

func (d *Docker) Name() string { return "docker" }

// Prepare creates the job's container without starting it.
//
// The container is created stopped, and the job's script is baked in as its
// command, so nothing can change between the moment the environment is defined and
// the moment it runs.
func (d *Docker) Prepare(ctx context.Context, spec JobSpec) (Handle, error) {
	if err := d.available(ctx); err != nil {
		return Handle{}, err
	}

	name := containerName(spec)
	args := []string{
		"create",
		"--name", name,
		"--label", "dogit.job=" + fmt.Sprint(spec.ID),
		"--label", "dogit.job_name=" + spec.Name,
	}

	if spec.WorkingDir != "" {
		args = append(args, "-w", spec.WorkingDir)
	}
	if spec.Resources.CPUs > 0 {
		args = append(args, "--cpus", trimFloat(spec.Resources.CPUs))
	}
	if spec.Resources.Memory > 0 {
		args = append(args, "--memory", fmt.Sprint(spec.Resources.Memory))
	}
	// A job is untrusted code from the project's point of view, so it gets as little
	// of the host as it can run without: no privileges, no added capabilities, its
	// own filesystem layer.
	args = append(args,
		"--cap-drop=ALL",
		"--security-opt", "no-new-privileges",
		"--network", jobNetwork(spec),
	)

	for _, volume := range spec.Volumes {
		target := volume.Target
		if target == "" {
			continue
		}
		if volume.ReadOnly {
			args = append(args, "-v", volume.Source+":"+target+":ro")
		} else {
			args = append(args, "-v", volume.Source+":"+target)
		}
	}

	for _, key := range sortedKeys(spec.Environment) {
		args = append(args, "-e", key+"="+spec.Environment[key])
	}

	// Network access for package downloads, unless the job says otherwise.
	if spec.Environment["DOGIT_OFFLINE"] != "1" {
		args = append(args, "-e", "HTTP_PROXY="+envOr(spec.Environment, "HTTP_PROXY", ""))
		args = append(args, "-e", "HTTPS_PROXY="+envOr(spec.Environment, "HTTPS_PROXY", ""))
	}

	args = append(args, ImageTag(spec.Image), "/bin/sh", "-c", scriptBody(spec))

	_, stderr, err := d.run(ctx, args...)
	if err != nil {
		// A container left behind by a crashed runner would block this job forever
		// under the same name. Removing it and retrying once is what makes a job
		// re-runnable after the runner was killed mid-job.
		if isNameConflict(stderr) {
			d.log.Debug("removing stale job container", "container", name)
			_, _, _ = d.run(ctx, "rm", "-f", name)
			_, stderr, err = d.run(ctx, args...)
		}
		if err != nil {
			return Handle{}, fmt.Errorf("docker create: %w: %s", err, strings.TrimSpace(stderr))
		}
	}

	handle := Handle{runtimeID: name, ContainerID: name}

	d.mu.Lock()
	d.handles[name] = handle
	d.mu.Unlock()

	d.log.Debug("job environment prepared", "job", spec.Name, "container", name)
	return handle, nil
}

// Stream starts the container and copies its output until it exits.
//
// Logs are read incrementally rather than collected and printed at the end: a
// build that takes ten minutes has to show progress while it runs, and a failed
// step has to show the lines that came before it.
func (d *Docker) Stream(ctx context.Context, handle Handle, spec JobSpec, logs io.Writer) Result {
	started := time.Now()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	d.mu.Lock()
	d.cancel[handle.runtimeID] = cancel
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		delete(d.cancel, handle.runtimeID)
		d.mu.Unlock()
	}()

	if spec.Timeout > 0 {
		timeoutCtx, timeoutCancel := context.WithTimeout(runCtx, spec.Timeout)
		defer timeoutCancel()
		runCtx = timeoutCtx
	}

	run := exec.CommandContext(runCtx, d.binary, "start", "-a", handle.ContainerID)
	run.Stdout = logs
	run.Stderr = logs

	result := Result{Status: StatusSuccess}

	err := run.Run()
	result.Duration = time.Since(started)

	switch {
	case err == nil:
		result.ExitCode = 0

	case runCtx.Err() != nil && ctx.Err() == nil:
		// The job exceeded its own deadline, which is a failure of the job rather
		// than of the runtime.
		result.Status = StatusFailed
		result.ExitCode = 124
		result.Error = fmt.Errorf("job %q exceeded its %s timeout", spec.Name, spec.Timeout)

	case ctx.Err() != nil:
		result.Status = StatusCanceled
		result.ExitCode = 130
		result.Error = ctx.Err()

	default:
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			result.Status = StatusFailed
			result.ExitCode = exitErr.ExitCode()
			result.Error = ScriptError(spec.Name, exitErr.ExitCode())
		} else {
			result.Status = StatusFailed
			result.ExitCode = 1
			result.Error = fmt.Errorf("docker start: %w", err)
		}
	}

	d.log.Debug("job finished",
		"job", spec.Name, "status", result.Status, "exit", result.ExitCode,
		"duration", result.Duration)

	return result
}

// Collect copies artifact paths out of a finished container.
//
// "docker cp" is used rather than "docker exec" because the container is no longer
// running by the time artifacts are collected, and exec requires a live process.
// The paths come back as a tar stream on stdout, so nothing from the job has to be
// mounted on the host: a build cannot influence any host path by naming one.
func (d *Docker) Collect(ctx context.Context, handle Handle, spec JobSpec, paths []string) (map[string][]byte, error) {
	artifacts := map[string][]byte{}

	for _, path := range paths {
		path = strings.TrimPrefix(strings.TrimSpace(path), "/")
		if path == "" {
			continue
		}

		cmd := exec.CommandContext(ctx, d.binary,
			"cp", handle.ContainerID+":/"+path, "-")

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			// An artifact that the job did not produce is not an error: the declared
			// paths come from configuration, and a conditional step may skip them.
			d.log.Debug("artifact not produced",
				"job", spec.Name, "path", path, "error", strings.TrimSpace(stderr.String()))
			continue
		}

		extracted, err := unpackTar(&stdout)
		if err != nil {
			return nil, fmt.Errorf("collect %q: %w", path, err)
		}
		for name, content := range rebaseKeys(extracted, path) {
			artifacts[path+"/"+name] = content
		}
	}

	return artifacts, nil
}

// Destroy removes the container, tolerating one that is already gone.
func (d *Docker) Destroy(ctx context.Context, handle Handle) error {
	d.mu.Lock()
	if cancel, ok := d.cancel[handle.runtimeID]; ok {
		cancel()
		delete(d.cancel, handle.runtimeID)
	}
	d.mu.Unlock()

	// The container is removed once the job's artifacts and logs have been collected.
	// It is not removed with --rm at creation, so a failure stays inspectable for as
	// long as the retention window.
	if _, out, err := d.run(ctx, "rm", "-f", handle.ContainerID); err != nil {
		d.log.Debug("remove job container", "container", handle.ContainerID,
			"error", err, "output", strings.TrimSpace(out))
	}

	d.mu.Lock()
	delete(d.handles, handle.runtimeID)
	d.mu.Unlock()

	return nil
}

// available checks that the docker CLI is present and the daemon answers.
//
// This is checked once per Prepare rather than cached: a daemon that is restarted
// mid-day must not leave the runner permanently convinced it works.
// run executes a docker command and returns its output streams.
func (d *Docker) run(ctx context.Context, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer

	cmd := exec.CommandContext(ctx, d.binary, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errOut

	err = cmd.Run()
	return out.String(), errOut.String(), err
}

// isNameConflict reports whether docker refused because the container name is
// taken.
func isNameConflict(stderr string) bool {
	return strings.Contains(stderr, "Conflict") ||
		strings.Contains(stderr, "is already in use")
}

// Available reports whether this machine can run a job at all.
//
// A runner exposes it on its readiness endpoint, because "ready" is a claim: a
// container that says it is ready while its Docker is down will be handed work it
// cannot start.
func (d *Docker) Available(ctx context.Context) error { return d.available(ctx) }

func (d *Docker) available(ctx context.Context) error {
	if _, err := exec.LookPath(d.binary); err != nil {
		return fmt.Errorf("runner: docker binary %q is not available: %w", d.binary, err)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if _, stderr, err := d.run(ctx, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("runner: docker daemon is not reachable: %w: %s",
			err, strings.TrimSpace(stderr))
	}
	return nil
}

// scriptBody turns the job's script into a single shell program.
//
// The steps are echoed as they run so the log shows which command failed, and the
// shell stops at the first failure, which is what every CI user expects.
func scriptBody(spec JobSpec) string {
	var body strings.Builder
	body.WriteString("set -e\n")
	for _, step := range spec.Script {
		body.WriteString("\necho \"$ ")
		body.WriteString(step)
		body.WriteString("\"\n")
		body.WriteString(step)
		body.WriteString("\n")
	}
	return body.String()
}

// jobNetwork picks the network the job runs on.
//
// Jobs talk to module caches and registries by service name, so they share a
// network with the application. A job that says it is offline still needs the
// network for its own services, but no outbound access is configured for it.
func jobNetwork(spec JobSpec) string {
	if network := spec.Environment["DOGIT_JOB_NETWORK"]; network != "" {
		return network
	}
	return "dogit_default"
}

func containerName(spec JobSpec) string {
	return fmt.Sprintf("dogit-job-%d-%s", spec.ID, sanitise(spec.Name))
}

// sanitise reduces a job name to something docker accepts as a container name.
func sanitise(name string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		return "job"
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	// A stable order keeps the container's configuration reproducible, which matters
	// when comparing two runs of the same job.
	sortStrings(keys)
	return keys
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func envOr(env map[string]string, key, fallback string) string {
	if value, ok := env[key]; ok && value != "" {
		return value
	}
	return fallback
}

func trimFloat(value float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

// rebaseKeys normalises the keys of a copied directory.
//
// "docker cp <container>:/build/out -" produces an archive whose entries are
// "out/result.txt", that is, prefixed with the directory's own name. Callers ask
// for paths, so the artifact is expected to be "build/out/result.txt"; the
// redundant component is dropped when every entry carries it.
func rebaseKeys(objects map[string][]byte, base string) map[string][]byte {
	prefix := path.Base(base) + "/"

	carriesPrefix := len(objects) > 0
	for name := range objects {
		if !strings.HasPrefix(name, prefix) {
			carriesPrefix = false
			break
		}
	}
	if !carriesPrefix {
		return objects
	}

	out := make(map[string][]byte, len(objects))
	for name, content := range objects {
		out[strings.TrimPrefix(name, prefix)] = content
	}
	return out
}

// unpackTar reads a tar stream into memory, keyed by path relative to the archive
// root and with the leading path component removed.
func unpackTar(stream *bytes.Buffer) (map[string][]byte, error) {
	out := map[string][]byte{}
	reader := tar.NewReader(bufio.NewReader(stream))

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return out, fmt.Errorf("read artifact archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}

		content, err := io.ReadAll(io.LimitReader(reader, 512<<20))
		if err != nil {
			return out, fmt.Errorf("read artifact %q: %w", header.Name, err)
		}
		out[strings.TrimPrefix(header.Name, "./")] = content
	}
}

type nopLogger struct{}

func (nopLogger) Warn(string, ...any)  {}
func (nopLogger) Info(string, ...any)  {}
func (nopLogger) Debug(string, ...any) {}

// LogWriter serialises writes from several goroutines and stamps every line with a
// timestamp.
//
// The runtime writes the container's stdout and stderr through one writer, and they
// arrive interleaved; without the mutex a single line can be torn in half.
type LogWriter struct {
	mu      sync.Mutex
	out     io.Writer
	prefix  string
	started time.Time
}

func NewLogWriter(out io.Writer, prefix string) *LogWriter {
	return &LogWriter{out: out, prefix: prefix, started: time.Now()}
}

// Line writes one already-terminated line.
func (w *LogWriter) Line(text string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	fmt.Fprintf(w.out, "[%6.1fs] %s %s\n",
		time.Since(w.started).Seconds(), w.prefix, text)
}

// Copy forwards a stream line by line.
func (w *LogWriter) Copy(src io.Reader) error {
	scanner := bufio.NewScanner(src)
	// A single line of build output can be long; a scanner's default 64 KiB limit
	// would silently truncate it.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		w.Line(scanner.Text())
	}
	return scanner.Err()
}
