// Package runner executes CI jobs.
//
// The package deliberately knows nothing about Docker or Kubernetes. A Runtime
// prepares an isolated environment for a job, streams its output, collects what it
// produced and tears it down. Two implementations exist — a local Docker runtime
// and a Kubernetes one — and adding the second is a new file rather than a
// rewrite, which is what makes moving to a cluster a mechanical change.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// JobSpec describes what has to run, in terms the runtime understands.
type JobSpec struct {
	// ID identifies the job within the system and is used for log and artifact
	// naming.
	ID int64
	// Name is the job's name in the pipeline configuration.
	Name string
	// Image is the container image the job runs in.
	Image string
	// Script is the list of commands to execute, one per line.
	Script []string
	// Environment is the job's environment, already resolved: secrets are included,
	// because the runtime runs it and nothing else sees them.
	Environment map[string]string
	// WorkingDir is where the script runs inside the container.
	WorkingDir string
	// Timeout bounds the whole job, log capture included.
	Timeout time.Duration
	// Resources are soft limits; a runtime without quotas ignores them.
	Resources ResourceLimits
	// Volumes are additional mounts the job needs, such as the module caches.
	Volumes []Volume
	// Services are extra containers the job talks to, started and stopped with it.
	Services []ServiceSpec
}

// ResourceLimits are the soft limits applied to a job's environment.
type ResourceLimits struct {
	CPUs   float64
	Memory int64 // bytes
}

// Volume is an additional mount inside the job's environment.
type Volume struct {
	// Name identifies the volume for a runtime that needs one.
	Name string
	// Source is a host path or a driver-specific identifier.
	Source string
	// Target is the mount point inside the environment.
	Target string
	// ReadOnly mounts the source without write access.
	ReadOnly bool
}

// ServiceSpec is a companion container running alongside the job.
type ServiceSpec struct {
	Name        string
	Image       string
	Environment map[string]string
	Ports       []string
}

// Handle refers to a prepared environment. It is opaque to the runner: only the
// runtime that produced it can interpret it.
type Handle struct {
	runtimeID string
	// ContainerID names the environment for diagnostics.
	ContainerID string
}

// ID returns the runtime's identifier for this environment.
func (h Handle) ID() string { return h.runtimeID }

// Discards is a writer that throws everything away, for a caller that wants the
// exit status and not the output.
var Discard io.Writer = io.Discard

// Status is the outcome of a job.
type Status string

const (
	StatusSuccess  Status = "success"
	StatusFailed   Status = "failed"
	StatusCanceled Status = "canceled"
)

// Result is what a finished job reports.
type Result struct {
	Status Status
	// ExitCode is the job's exit code. A nonzero code is a failure unless the job
	// was allowed to fail.
	ExitCode int
	// Duration is the wall-clock time the job ran for.
	Duration time.Duration
	// Error carries a diagnostic for failures that are not the script's own, such as
	// a runtime that could not start the environment.
	Error error
}

// Runtime prepares and runs job environments.
//
// Implementations must be safe for concurrent use: the runner may hold several
// jobs at once.
type Runtime interface {
	// Prepare creates an isolated environment for the job.
	Prepare(ctx context.Context, spec JobSpec) (Handle, error)
	// Stream runs the job, writing its output to the given writers until the script
	// finishes or the deadline passes.
	//
	// The two streams are written separately rather than merged. A build that fails
	// says why on stderr, and a merged log makes that line look like any other —
	// which is precisely the line the reader is looking for.
	Stream(ctx context.Context, handle Handle, spec JobSpec, stdout, stderr io.Writer) Result
	// Collect retrieves the declared artifact paths from a finished environment.
	//
	// It runs after Stream because artifacts are whatever the job left behind: an
	// artifact that is produced but not uploaded by the script itself is the normal
	// case, not a special one.
	Collect(ctx context.Context, handle Handle, spec JobSpec, paths []string) (map[string][]byte, error)
	// Destroy removes the environment and everything it produced.
	Destroy(ctx context.Context, handle Handle) error
	// Name identifies the runtime in logs and diagnostics.
	Name() string
}

// ErrNoRuntime is returned when a job is submitted with no runtime configured.
var ErrNoRuntime = errors.New("runner: no runtime configured")

// ScriptError turns a nonzero exit code into an error a log line can carry.
func ScriptError(name string, exitCode int) error {
	if exitCode == 0 {
		return nil
	}
	return fmt.Errorf("job %q exited with status %d", name, exitCode)
}

// ImageTag appends a tag when one is missing, so a configuration that names only a
// repository still resolves to something pullable.
func ImageTag(image string) string {
	if strings.TrimSpace(image) == "" {
		return "alpine:3.21"
	}
	if strings.ContainsAny(image, ":@") && !strings.Contains(image, "/") {
		return image
	}
	// A colon after the last slash is a tag; before it, it is a registry port.
	lastSlash := strings.LastIndex(image, "/")
	if colon := strings.Index(image[lastSlash+1:], ":"); colon >= 0 {
		return image
	}
	return image + ":latest"
}
