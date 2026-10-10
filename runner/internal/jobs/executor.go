// Running a job's work.
//
// In this runner's own container, and that is a decision with a cost, so the cost is written
// here rather than left to be discovered.
//
// The job's script is somebody else's code. It runs beside the runner, as the same user, with
// the same memory ceiling and the same CPU ceiling. Three consequences, stated plainly:
//
//   - a credential on a filesystem in this container is a credential the script can read,
//     which is why the builder's client key and the registration token are gone from disk and
//     from the environment before any job starts;
//   - a job that eats the container's memory gets it OOMKilled, and the runner goes with it;
//     with a single replica that is a stalled queue, not a failed build;
//   - there is no per-job limit, because a limit is a thing you can give to a container and
//     this is not one.
//
// What it buys, which is the reason it was chosen: no Kubernetes API access, no second
// namespace, no image to pull before a job can start. The destination is a pod per job, and
// this is the way to have working jobs before that exists.
package jobs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Reporter is what the executor tells the core about a job. Satisfied by *core.Client, and an
// interface rather than that type so that this package has no idea where the core is.
//
// Finishing a job is NOT one of these. The executor runs the script; the image is a second
// half of the same job, owned by the caller, and the core must not hear that the job is done
// until the image is in the registry. So a Reporter here could not finish a job even if it
// wanted to, which is the point: the mistake is not available.
type Reporter interface {
	JobProgress(ctx context.Context, jobID int64, phase, message string) error
	JobLog(ctx context.Context, jobID int64, stream, text string) error
	JobKey(ctx context.Context, jobID int64) (cloneURL, privateKey, fingerprint string, err error)
}

// Job is one unit of work, as the core described it.
type Job struct {
	ID          int64
	Stage       string
	Name        string
	ProjectPath string
	Script      []string
	Variables   map[string]string
	Registry    *Registry
}

// Registry is the credential the core minted for this build.
type Registry struct {
	URL   string
	Image string
	Token string
	// Username and Password are the other shape a registry answers to. A registry
	// module mints a token scoped to one project; a registry the instance was merely
	// given the address of is pushed to with an account it already holds. Both arrive
	// here from the core, and which one is filled in is the core's decision rather than
	// this runner's.
	Username string
	Password string
}

// Result is how a job ended.
type Result struct {
	Status string
	Reason string
	Steps  int
	Took   time.Duration
}

// Statuses, which are the core's own words rather than ours.
const (
	StatusSuccess = "success"
	StatusFailed  = "failed"
)

// Executor runs jobs.
type Executor struct {
	// workspace is where checkouts go. One directory per job, because two jobs of the same
	// project at different commits must not share a working tree, and because a directory
	// left over from a job that died mid-way is evidence rather than litter.
	workspace string
	// shell is what runs a step. sh and not bash because a build script that needs bash
	// can say so in its own first line, and a runner that decides which shell a project's
	// scripts are written for is a runner one project away from a surprise.
	shell string
	// timeout ends a job that will not end. A build still running after an hour is not
	// going to finish, and a job holding a slot for ever is a queue that stops draining.
	timeout time.Duration
	// report is where the log and the progress go.
	report Reporter
	// keepCheckout leaves a finished job's working copy in place for the next job of the same
	// project.
	//
	// A real choice rather than a detail, and both sides of it are real. Leaving it means the
	// next job of the same project fetches one commit instead of the whole history, which on
	// a large repository is the difference between a build that starts and one that waits.
	// Removing it means the disk does not grow with the number of projects and nobody has to
	// decide that a directory is stale. So it is the administrator's to make, and the default
	// is to keep, because the cost of keeping is a disk that fills and the cost of removing is
	// a wait that happens on every single build.
	keepCheckout bool
}

// KeepCheckouts says whether finished working copies are left behind.
func (e *Executor) KeepCheckouts() *bool { return &e.keepCheckout }

// New makes an executor.
func New(workspace, shell string, timeout time.Duration, report Reporter, keepCheckout bool) *Executor {
	if shell == "" {
		shell = "/bin/sh"
	}
	if timeout <= 0 {
		timeout = time.Hour
	}
	return &Executor{
		workspace: workspace, shell: shell, timeout: timeout,
		report: report, keepCheckout: keepCheckout,
	}
}

// Run performs a job's script steps, reporting as it goes.
//
// It does NOT finish the job. The caller does, and it does so after the image is built and
// pushed — which is the whole of why it is the caller. An earlier version finished here, in
// a defer, and that was wrong in the way that matters: the core saw the build job succeed
// and started the deploy immediately, while the image was still being pushed. The deploy
// then asked the registry for a tag that had not been pushed yet, got a 404, and fell back
// to deploying by tag — a rollback would then bring back whatever else was pushed under that
// name, which is precisely the thing pinning by digest exists to prevent.
//
// So this returns what happened, and the runner closes the job once the whole of it is done.
func (e *Executor) Run(ctx context.Context, job *Job) Result {
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	started := time.Now()
	result := Result{Status: StatusFailed}

	// Under "build", like every other phase this package says, and for the same reason:
	// "checkout" is a word of its own on a page that shows a plan, and it becomes a row
	// below it.
	_ = e.report.JobProgress(ctx, job.ID, "build", "preparing "+job.ProjectPath)

	// One directory per project, shared between that project's jobs, rather than one per job.
	// That is what makes keeping a checkout worth anything: the next job of the same project
	// fetches one commit instead of the whole history. A directory per job would be re-cloned
	// every time whatever the setting said, which is the version where the setting is a knob
	// that does nothing.
	directory := filepath.Join(e.workspace, projectSlug(job.ProjectPath))
	if err := os.MkdirAll(directory, 0o750); err != nil {
		result.Reason = "cannot make a workspace at " + directory + ": " + err.Error()
		return result
	}

	// Left behind on purpose, on every path out, because a checkout of a job that failed is
	// evidence: the next person to look at it can see what the script was working on.
	defer func() {
		if e.keepCheckout {
			return
		}
		if err := os.RemoveAll(directory); err != nil {
			fmt.Fprintf(os.Stderr, "runner: could not remove %s: %v\n", directory, err)
		}
	}()

	if err := e.clone(ctx, job, directory); err != nil {
		result.Reason = err.Error()
		return result
	}

	for index, step := range job.Script {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		// A build prints layer after layer, and a page watching it live cannot tell from
		// those lines whether the image is being built or pushed. This names the step.
		//
		// The phase is "build" and not a word of this package's own choosing: the core's
		// deployment plan already has a step under that name, and a page draws a phase it
		// has never heard of as an extra row below the plan — carrying an arrow of its own,
		// because nothing closes a phase the runner never finishes. So a runner that calls
		// its work "checkout" and "script" hands a person four arrows and a list whose
		// steps do not match the plan they were just shown. Same step, the name the plan
		// already uses.
		_ = e.report.JobProgress(ctx, job.ID, "build", step)

		if err := e.step(ctx, job, directory, step); err != nil {
			result.Reason = err.Error()
			result.Steps = index
			return result
		}
		result.Steps = index + 1
	}

	// No phase of its own for "the script is over". A page learns a step is finished from
	// the step after it, and this package has no opinion about what comes next; announcing
	// it under a name of its own would put a row on the page that no plan asked for.
	result.Status = StatusSuccess
	result.Took = time.Since(started)
	return result
}

// clone fetches the job's commit into a directory of its own.
//
// The commit and not the default branch. A CI run is a claim about one commit, and building
// the head of the main branch when the run was for something else is a build that passes and
// says nothing about the code it was started for.
func (e *Executor) clone(ctx context.Context, job *Job, directory string) error {
	cloneURL, privateKey, _, err := e.report.JobKey(ctx, job.ID)
	if err != nil {
		return fmt.Errorf("ask for a clone credential: %w", err)
	}

	// Said here rather than left to git. An empty address reaches `git remote add origin ""`,
	// which succeeds, and the failure arrives two steps later as "no path specified; see 'git
	// help pull'" — a message about a pull in a program that never pulls anything, pointing
	// at a directory that already looked right.
	if strings.TrimSpace(cloneURL) == "" {
		return fmt.Errorf("the core offered no address to clone %s from; "+
			"this runner has no way to work out one itself. "+
			"DOGIT_SSH_HOST and DOGIT_SSH_PORT on the core are what it builds that address from",
			job.ProjectPath)
	}
	if strings.TrimSpace(privateKey) == "" {
		return fmt.Errorf("the core offered no key to clone %s with", job.ProjectPath)
	}

	key, err := os.CreateTemp("", "dogit-job-*.key")
	if err != nil {
		return fmt.Errorf("make a file for the clone key: %w", err)
	}
	defer os.Remove(key.Name())
	if _, err := key.WriteString(privateKey + "\n"); err != nil {
		key.Close()
		return fmt.Errorf("write the clone key: %w", err)
	}
	if err := key.Close(); err != nil {
		return fmt.Errorf("close the clone key: %w", err)
	}
	if err := os.Chmod(key.Name(), 0o600); err != nil {
		return fmt.Errorf("make the clone key private: %w", err)
	}

	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + directory,
		// accept-new rather than a known_hosts file nobody maintains, and /dev/null for the
		// same reason: a runner that refuses to clone because it has never seen the host is a
		// runner that needs an ssh-keyscan run first, at three in the morning, by whoever is
		// on call. The trade is that a changed host key goes unnoticed, which is a real cost
		// and a smaller one than a build that cannot start.
		"GIT_SSH_COMMAND=ssh -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -i " + key.Name(),
		// No prompting. A job that stops for input is a job that holds a slot until the
		// timeout, and the message it would print is about authentication, not about the
		// build somebody started.
		"GIT_TERMINAL_PROMPT=0",
	}

	// A specific commit, which is what the run is for. Fetching a bare SHA is what a server
	// allows when it allows it, and the branch is the fallback for one that does not — the
	// other way round would quietly check out a branch head and call it the commit.
	commit := job.Variables["CI_COMMIT_SHA"]
	ref := job.Variables["CI_COMMIT_BRANCH"]
	if job.Variables["CI_COMMIT_TAG"] != "" {
		ref = job.Variables["CI_COMMIT_TAG"]
	}

	// The core's address is the base and not the repository: a host and a port, the one a
	// person clones from. That is on purpose, so that where the core lives and where a project
	// lives can be configured apart. So the project is added here — escaped, because a path
	// with a slash in it is two segments to git rather than one.
	repository := strings.TrimRight(cloneURL, "/") + "/" +
		url.PathEscape(job.ProjectPath) + ".git"

	// Reuse the repository if it is already there. `git init` is harmless on a directory that
	// is a repository, and `git remote` is only added when there is not one yet — a second
	// `remote add` would fail with "remote origin already exists", which is a fine error for
	// a person and a silly one for a cache.
	steps := [][]string{{"init", "--quiet"}}
	if _, err := os.Stat(filepath.Join(directory, ".git")); err != nil {
		steps = append(steps, []string{"remote", "add", "origin", repository})
	} else {
		// The origin can have moved — a project's address is a thing an administrator can
		// change — so it is pointed at what the core said this time rather than at whatever
		// was cached.
		steps = append(steps, []string{"remote", "set-url", "origin", repository})
	}
	if commit != "" {
		steps = append(steps, []string{"fetch", "--depth", "1", "origin", commit})
	}
	if ref != "" {
		steps = append(steps, []string{"fetch", "--depth", "1", "origin", ref})
	}
	steps = append(steps,
		[]string{"checkout", "--quiet", "--detach", "FETCH_HEAD"},
		[]string{"submodule", "update", "--init", "--recursive", "--depth", "1"},
	)

	for _, step := range steps {
		command := exec.CommandContext(ctx, "git", step...)
		command.Dir = directory
		command.Env = environment
		if output, err := command.CombinedOutput(); err != nil {
			// The whole thing, not the last line. A clone that failed has usually failed for
			// one reason, and it is somewhere in the output rather than in the exit status.
			return fmt.Errorf("git %s: %w: %s", strings.Join(step, " "), err,
				strings.TrimSpace(string(output)))
		}
	}
	return nil
}

// step runs one line of a job's script and streams what it prints.
//
// Combined into the same pipe for stdout and stderr, and the reason is in the type: two
// separate writers produce a log that reads as a build which printed nothing, followed by an
// error out of nowhere.
func (e *Executor) step(ctx context.Context, job *Job, directory, script string) error {
	command := exec.CommandContext(ctx, e.shell, "-c", script)
	command.Dir = directory
	command.Env = e.environment(job, directory)

	pipe, err := command.StdoutPipe()
	if err != nil {
		return fmt.Errorf("open a pipe for the step: %w", err)
	}
	command.Stderr = command.Stdout

	started := time.Now()
	if err := command.Start(); err != nil {
		return fmt.Errorf("run %q: %w", script, err)
	}

	// Batched, because a build that prints a line per second would otherwise be a thousand
	// requests, and a core that answers a thousand requests a second is a core that is not
	// answering them about the build.
	var (
		mu      sync.Mutex
		pending strings.Builder
	)
	flush := func(force bool) {
		mu.Lock()
		defer mu.Unlock()
		if pending.Len() == 0 {
			return
		}
		if !force && pending.Len() < 2048 {
			return
		}
		text := pending.String()
		pending.Reset()
		_ = e.report.JobLog(context.WithoutCancel(ctx), job.ID, "stdout", text)
	}
	defer func() { flush(true) }()

	scanner := bufio.NewScanner(pipe)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		mu.Lock()
		pending.WriteString(scanner.Text())
		pending.WriteByte('\n')
		mu.Unlock()
		flush(false)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
		_ = e.report.JobLog(context.WithoutCancel(ctx), job.ID, "stderr",
			"reading the step's output: "+err.Error())
	}

	waited := command.Wait()
	took := time.Since(started).Round(time.Millisecond)

	switch {
	case waited == nil:
		_ = e.report.JobLog(ctx, job.ID, "stdout", fmt.Sprintf("[the step finished in %s]\n", took))
		return nil
	case ctx.Err() != nil:
		// A step that was still going when the timeout arrived, which is a different sentence
		// from a step that failed, and a shorter job name in the log would hide which.
		return fmt.Errorf("the step was stopped after %s (the job's own limit is %s)", took, e.timeout)
	default:
		return fmt.Errorf("the step failed after %s: %w", took, waited)
	}
}

// environment is what a step sees.
//
// Built from nothing rather than from this process's own environment. A step inheriting the
// runner's environment would be handed whatever the runner was given — and the reason the
// registration token and the builder key are not in it is that they should not be handed to
// anything, least of all a script written by somebody whose build this is.
func (e *Executor) environment(job *Job, directory string) []string {
	environment := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + directory,
		// A project with a shell script that reads a tty is not a build that works, and
		// saying so here is kinder than letting it block.
		"CI=true",
		"TERM=dumb",
		"CI_PROJECT_PATH=" + job.ProjectPath,
		"CI_JOB_ID=" + strconv.FormatInt(job.ID, 10),
		"CI_JOB_NAME=" + job.Name,
		"CI_JOB_STAGE=" + job.Stage,
	}
	// The core recorded these; the runner does not work out whether the run is on a tag,
	// because a build script that had to decide for itself would be one git call away from
	// tagging the wrong image.
	for name, value := range job.Variables {
		if isCIeVariable(name) {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func isCIeVariable(name string) bool {
	return strings.HasPrefix(name, "CI_")
}

// projectSlug is a project's working directory: readable, one level, and a leftover says
// which project it belongs to.
func projectSlug(projectPath string) string {
	slug := strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(projectPath)
	return strings.Trim(slug, "-")
}

// Directory is where this job's checkout is, so that a caller can measure it or find it.
func Directory(workspace, projectPath string) string {
	return filepath.Join(workspace, projectSlug(projectPath))
}
