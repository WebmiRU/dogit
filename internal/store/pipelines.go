package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PipelineRepo keeps pipelines and the jobs in them.
//
// A job is claimed rather than scheduled: the core does not know which runners
// exist or where they are, and a runner on a machine nobody told the core about
// still does the work. Claiming is atomic and is the only place two runners can
// collide.
type PipelineRepo struct{ s *Store }

func (s *Store) Pipelines() *PipelineRepo { return &PipelineRepo{s: s} }

// Pipeline statuses. A pipeline is whatever its worst job is, which is computed on
// read rather than stored, so a job's state can change without anything having to
// remember to fix its parent.
const (
	PipelinePending = "pending"
	PipelineRunning = "running"
	// PipelineInterrupted is a pipeline whose runner stopped reporting while its
	// jobs were running. It is neither a failure nor a success: the machine went
	// away mid-build, and either verdict would be a claim nobody can support.
	PipelineInterrupted = "interrupted"
	PipelineSuccess     = "success"
	PipelineFailed      = "failed"
	PipelineCanceled    = "canceled"
)

// Job statuses.
const (
	JobPending  = "pending"
	JobRunning  = "running"
	JobSuccess  = "success"
	JobFailed   = "failed"
	JobCanceled = "canceled"
	JobSkipped  = "skipped"
	// JobInterrupted is a job whose runner stopped answering while it was running.
	// It is neither a failure nor a success, because neither is known: the machine
	// went away mid-build, and saying the build failed would be a claim nobody can
	// support.
	JobInterrupted = "interrupted"
)

// Pipeline is one run of a pipeline configuration.
type Pipeline struct {
	ID        int64     `json:"id"`
	IID       int       `json:"iid"`
	ProjectID uuid.UUID `json:"project_id"`
	Ref       string    `json:"ref"`
	SHA       string    `json:"sha"`
	Source    string    `json:"source"`
	Status    string    `json:"status"`
	// CommitTitle and CommitAuthor describe the commit this run was made against,
	// copied at creation rather than read from the repository later: the branch
	// moves on, and a list of old runs that re-reads it would show whatever is at
	// the tip now for every one of them.
	CommitTitle       string            `json:"commit_title"`
	CommitAuthorName  string            `json:"commit_author_name"`
	CommitAuthorEmail string            `json:"commit_author_email"`
	Variables         map[string]string `json:"variables,omitempty"`
	CreatedBy         *uuid.UUID        `json:"created_by_id,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
}

// Commit is what a pipeline was run against: enough to say which change it was
// for, without going back to git to find out.
type Commit struct {
	Title       string `json:"title"`
	AuthorName  string `json:"author_name"`
	AuthorEmail string `json:"author_email"`
}

// DurationMs is how long the pipeline took, or nil while it has not finished.
//
// Measured from when the first job started rather than when the run was created:
// the seconds between asking for a build and a runner picking it up belong to
// nobody, and adding them would make every queued pipeline look slow.
func (p *Pipeline) DurationMs() *int64 {
	if p.StartedAt == nil {
		return nil
	}
	end := time.Now()
	if p.FinishedAt != nil {
		end = *p.FinishedAt
	}
	ms := end.Sub(*p.StartedAt).Milliseconds()
	if ms < 0 {
		return nil
	}
	return &ms
}

// Job is one unit of work inside a pipeline.
type Job struct {
	ID           int64      `json:"id"`
	PipelineID   int64      `json:"pipeline_id"`
	IID          int        `json:"iid"`
	Name         string     `json:"name"`
	Stage        string     `json:"stage"`
	Status       string     `json:"status"`
	RunnerID     *uuid.UUID `json:"runner_id,omitempty"`
	Image        string     `json:"image"`
	Script       []string   `json:"script"`
	AllowFailure bool       `json:"allow_failure"`
	Needs        []string   `json:"needs"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	CreatedAt    time.Time  `json:"created_at"`

	// Build describes an image this job produces, when it produces one. The
	// definition is the module's own: the core stores it and hands it back, and does
	// not decide what a Dockerfile is.
	Build map[string]any `json:"build,omitempty"`
	// ProjectPath and ProjectID are carried on the job rather than looked up, so a
	// runner on another machine needs one round trip rather than two.
	ProjectPath string    `json:"project_path"`
	ProjectID   uuid.UUID `json:"project_id"`
}

// CreatePipeline records a pipeline and its jobs, taking the next number for the
// project.
func (r *PipelineRepo) CreatePipeline(ctx context.Context, projectID uuid.UUID, ref, sha, source string,
	variables map[string]string, createdBy *uuid.UUID, commit Commit, jobs []Job) (*Pipeline, error) {

	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}
	defer tx.Rollback(ctx)

	// The number is taken under a lock on the project rather than computed from a
	// max(): two pipelines created at the same instant must not end up with the same
	// number, and a pipeline number is something people read aloud. FOR UPDATE does
	// not work here — it cannot be combined with an aggregate — so the lock is the
	// project's, which is the thing being numbered anyway.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"pipeline:"+projectID.String()); err != nil {
		return nil, fmt.Errorf("lock the project: %w", err)
	}

	var iid int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(iid), 0) + 1 FROM pipelines WHERE project_id = $1`,
		projectID).Scan(&iid); err != nil {
		return nil, fmt.Errorf("allocate pipeline number: %w", err)
	}

	// An empty object rather than NULL: the column is not nullable, and "no
	// variables" is an empty object, not an absence.
	if variables == nil {
		variables = map[string]string{}
	}

	pipeline := &Pipeline{
		IID:               iid,
		ProjectID:         projectID,
		Ref:               ref,
		SHA:               sha,
		Source:            source,
		Status:            PipelinePending,
		Variables:         variables,
		CreatedBy:         createdBy,
		CommitTitle:       commit.Title,
		CommitAuthorName:  commit.AuthorName,
		CommitAuthorEmail: commit.AuthorEmail,
	}

	err = tx.QueryRow(ctx, `
		INSERT INTO pipelines (iid, project_id, ref, sha, source, status, variables, created_by_id,
		                       commit_title, commit_author_name, commit_author_email)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id, created_at`,
		pipeline.IID, projectID, ref, sha, source, pipeline.Status, variables, createdBy,
		pipeline.CommitTitle, pipeline.CommitAuthorName, pipeline.CommitAuthorEmail,
	).Scan(&pipeline.ID, &pipeline.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}

	for index, job := range jobs {
		job.IID = index + 1
		if job.Status == "" {
			job.Status = JobPending
		}
		job.PipelineID = pipeline.ID

		// A job with no script is a job with an empty one. The columns are not
		// nullable, and "nothing to run" is a fact about the job rather than an
		// absence of a fact.
		if job.Script == nil {
			job.Script = []string{}
		}
		if job.Needs == nil {
			job.Needs = []string{}
		}
		if job.Stage == "" {
			job.Stage = "test"
		}

		build, err := jsonbOf(job.Build)
		if err != nil {
			return nil, err
		}

		// RETURNING rather than a plain insert: the job's id is needed by whatever
		// asked for this pipeline, and reading it back separately would be a second
		// round trip for a value the database already had.
		err = tx.QueryRow(ctx, `
			INSERT INTO jobs (pipeline_id, iid, name, stage, status, image, script,
			                  allow_failure, needs, build)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			RETURNING id`,
			job.PipelineID, job.IID, job.Name, job.Stage, job.Status, job.Image,
			job.Script, job.AllowFailure, job.Needs, build).Scan(&job.ID)
		if err != nil {
			return nil, fmt.Errorf("create job: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("create pipeline: %w", err)
	}
	return pipeline, nil
}

// PipelineByIID returns one pipeline of a project.
func (r *PipelineRepo) PipelineByIID(ctx context.Context, projectID uuid.UUID, iid int) (*Pipeline, error) {
	var pipeline Pipeline
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, iid, project_id, ref, sha, source, status, variables, created_by_id,
		       commit_title, commit_author_name, commit_author_email,
		       created_at, started_at, finished_at
		FROM pipelines WHERE project_id = $1 AND iid = $2`, projectID, iid,
	).Scan(&pipeline.ID, &pipeline.IID, &pipeline.ProjectID, &pipeline.Ref, &pipeline.SHA,
		&pipeline.Source, &pipeline.Status, &pipeline.Variables, &pipeline.CreatedBy,
		&pipeline.CommitTitle, &pipeline.CommitAuthorName, &pipeline.CommitAuthorEmail,
		&pipeline.CreatedAt, &pipeline.StartedAt, &pipeline.FinishedAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read pipeline: %w", err)
	}
	return &pipeline, nil
}

// ListPipelines returns a project's pipelines, newest first.
func (r *PipelineRepo) ListPipelines(ctx context.Context, projectID uuid.UUID, limit int) ([]Pipeline, error) {
	if limit <= 0 {
		limit = 20
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT id, iid, project_id, ref, sha, source, status, variables, created_by_id,
		       commit_title, commit_author_name, commit_author_email,
		       created_at, started_at, finished_at
		FROM pipelines WHERE project_id = $1 ORDER BY iid DESC LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list pipelines: %w", err)
	}
	defer rows.Close()

	pipelines := []Pipeline{}
	for rows.Next() {
		var pipeline Pipeline
		if err := rows.Scan(&pipeline.ID, &pipeline.IID, &pipeline.ProjectID, &pipeline.Ref,
			&pipeline.SHA, &pipeline.Source, &pipeline.Status, &pipeline.Variables,
			&pipeline.CreatedBy, &pipeline.CommitTitle, &pipeline.CommitAuthorName,
			&pipeline.CommitAuthorEmail, &pipeline.CreatedAt, &pipeline.StartedAt,
			&pipeline.FinishedAt); err != nil {
			return nil, fmt.Errorf("scan pipeline: %w", err)
		}
		pipelines = append(pipelines, pipeline)
	}
	return pipelines, rows.Err()
}

// JobByID returns one job with everything a runner needs to run it.
func (r *PipelineRepo) JobByID(ctx context.Context, id int64) (*Job, error) {
	var job Job
	var build []byte

	err := r.s.pool.QueryRow(ctx, jobColumns+`
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		JOIN projects pr ON pr.id = p.project_id
		WHERE j.id = $1`, id).Scan(&job.ID, &job.PipelineID, &job.IID, &job.Name, &job.Stage,
		&job.Status, &job.RunnerID, &job.Image, &job.Script, &job.AllowFailure, &job.Needs,
		&build, &job.StartedAt, &job.FinishedAt, &job.DurationMS, &job.CreatedAt,
		&job.ProjectID, &job.ProjectPath)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read job: %w", err)
	}

	job.Build = map[string]any{}
	decodeJSONB(build, &job.Build)
	if len(job.Build) == 0 {
		job.Build = nil
	}
	return &job, nil
}

// JobsOfPipeline returns a pipeline's jobs in the order they were declared.
func (r *PipelineRepo) JobsOfPipeline(ctx context.Context, pipelineID int64) ([]Job, error) {
	rows, err := r.s.pool.Query(ctx, jobColumns+`
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		JOIN projects pr ON pr.id = p.project_id
		WHERE j.pipeline_id = $1 ORDER BY j.iid`, pipelineID)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()

	jobs := []Job{}
	for rows.Next() {
		job, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *job)
	}
	return jobs, rows.Err()
}

const jobColumns = `
	SELECT j.id, j.pipeline_id, j.iid, j.name, j.stage, j.status, j.runner_id, j.image,
	       j.script, j.allow_failure, j.needs, j.build, j.started_at, j.finished_at,
	       j.duration_ms, j.created_at, p.project_id, pr.path`

func scanJob(rows pgx.Rows) (*Job, error) {
	var job Job
	var build []byte

	if err := rows.Scan(&job.ID, &job.PipelineID, &job.IID, &job.Name, &job.Stage, &job.Status,
		&job.RunnerID, &job.Image, &job.Script, &job.AllowFailure, &job.Needs, &build,
		&job.StartedAt, &job.FinishedAt, &job.DurationMS, &job.CreatedAt,
		&job.ProjectID, &job.ProjectPath); err != nil {
		return nil, fmt.Errorf("scan job: %w", err)
	}

	job.Build = map[string]any{}
	decodeJSONB(build, &job.Build)
	if len(job.Build) == 0 {
		job.Build = nil
	}
	return &job, nil
}

// ClaimJob hands one pending job to a runner, atomically.
//
// This is the only place two runners can collide, and it is settled by the
// database: the row is updated only if it is still pending, so whichever runner's
// statement lands first gets the job and the other is told there is nothing to do.
// A queue that decides by reading then writing would give the same job to two
// machines the moment two of them looked at once.
func (r *PipelineRepo) ClaimJob(ctx context.Context, runnerID uuid.UUID, tags []string) (*Job, error) {
	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	defer tx.Rollback(ctx)

	// FOR UPDATE SKIP LOCKED: a runner takes the row and moves on, rather than
	// waiting behind one that is about to find the job is not available.
	var id int64
	err = tx.QueryRow(ctx, `
		UPDATE jobs SET status = $1, runner_id = $2, started_at = now()
		WHERE id = (
			SELECT j.id FROM jobs j
			WHERE j.status = $3
			ORDER BY j.pipeline_id, j.iid
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1
		)
		RETURNING id`, JobRunning, runnerID, JobPending).Scan(&id)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}

	// The pipeline is running now. Its overall state is derived on read, so nothing
	// has to be kept in step here.
	if _, err := tx.Exec(ctx, `
		UPDATE pipelines SET started_at = COALESCE(started_at, now())
		WHERE id = (SELECT pipeline_id FROM jobs WHERE id = $1) AND status = $2`,
		id, PipelinePending); err != nil {
		return nil, fmt.Errorf("mark the pipeline running: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return r.JobByID(ctx, id)
}

// FinishJob records what happened to a job.
//
// The status is taken from the runner rather than deduced: a job that let the
// script fail but was allowed to fail is a success, and only the runner knows
// which jobs those are.
func (r *PipelineRepo) FinishJob(ctx context.Context, id int64, status string, duration time.Duration) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE jobs SET status = $2, finished_at = now(), duration_ms = $3
		WHERE id = $1`, id, status, duration.Milliseconds())
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}

	// The pipeline is finished when nothing of its is still waiting. Both the
	// pending and the running states count as waiting: a pipeline whose second job
	// has not started is not over, however the first went.
	if _, err := r.s.pool.Exec(ctx, `
		UPDATE pipelines
		SET finished_at = now(), status = $2
		WHERE id = (SELECT pipeline_id FROM jobs WHERE id = $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs
		      WHERE pipeline_id = pipelines.id AND status IN ($3, $4))`,
		id, pipelineStatusFor(status), JobPending, JobRunning); err != nil {
		return fmt.Errorf("finish pipeline: %w", err)
	}
	return nil
}

// JobStaleAfter is how long a running job may go without finishing before the core
// stops believing the machine is still working on it.
//
// Long enough for a slow build on a loaded runner: a job that takes ten minutes is
// ordinary, and cutting it off early would leave a container running with nobody
// watching it. Short enough that a pipeline does not hang for a day over a runner
// that was switched off.
const JobStaleAfter = 30 * time.Minute

// ReapStaleJobs puts back the jobs whose runner stopped reporting.
//
// A job marked running is a claim that some machine is working on it. When that
// machine is gone the claim is still there, and the pipeline waits on it for ever,
// which is the one state a pipeline must never be in: an operator who restarts a
// runner should not have to go and repair the queue by hand.
//
// The status is "interrupted" rather than a failure, because nothing is known about
// how far it got. Failing it would say the build broke; it did not, its machine
// stopped. Either way it is no longer running, and that is the part anybody needs
// to know.
func (r *PipelineRepo) ReapStaleJobs(ctx context.Context, olderThan time.Duration) ([]string, error) {
	// One statement, so two janitors cannot finish the same job twice: the second
	// UPDATE matches nothing, because the first has already changed the status.
	rows, err := r.s.pool.Query(ctx, `
		UPDATE jobs
		SET status = $1, finished_at = now(),
		    duration_ms = GREATEST(0, (EXTRACT(EPOCH FROM (now() - started_at)) * 1000)::bigint)
		WHERE status = $2 AND started_at IS NOT NULL AND started_at < now() - $3::interval
		RETURNING name`, JobInterrupted, JobRunning, fmt.Sprintf("%d seconds", int(olderThan.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("reclaim stale jobs: %w", err)
	}
	defer rows.Close()

	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan a reclaimed job: %w", err)
		}
		stale = append(stale, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(stale) == 0 {
		return nil, nil
	}

	// The pipelines those jobs belonged to are finished for the same reason, by the
	// same rule that finishes one when a job completes: a pipeline whose last job
	// was reclaimed has to end, or it waits for ever.
	if _, err := r.s.pool.Exec(ctx, `
		UPDATE pipelines p
		SET finished_at = now(), status = $1
		WHERE p.status = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs j
		      WHERE j.pipeline_id = p.id AND j.status IN ($3, $4))`,
		pipelineStatusFor(JobInterrupted), PipelineRunning, JobPending, JobRunning); err != nil {
		return stale, fmt.Errorf("finish the pipelines of reclaimed jobs: %w", err)
	}
	return stale, nil
}

// ReleaseJob puts a claimed job back without having run it.
//
// For the moment between "a runner took this" and "the core finished answering".
// A job in that state is owned by nobody as far as the rest of the system is
// concerned: the runner was told nothing, so it will never report on it, and a
// pipeline waiting for it waits for ever.
func (r *PipelineRepo) ReleaseJob(ctx context.Context, id int64) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, runner_id = NULL, started_at = NULL
		WHERE id = $1 AND status = $3`, id, JobPending, JobRunning)
	if err != nil {
		return fmt.Errorf("release job: %w", err)
	}
	return nil
}

// RetryJob puts one job back in the queue.
//
// The same row, not a copy: a job that is being run again is the same job, and a
// copy would leave two rows with one name, of which the pipeline counts both. The
// times and the runner are cleared because what they meant was the last attempt.
func (r *PipelineRepo) RetryJob(ctx context.Context, id int64) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, runner_id = NULL, started_at = NULL, finished_at = NULL, duration_ms = 0
		WHERE id = $1`, id, JobPending)
	if err != nil {
		return fmt.Errorf("retry job: %w", err)
	}

	// The pipeline is unfinished again: it has work waiting, and until it does the
	// status it showed was a conclusion about a run that is no longer the last one.
	if _, err := r.s.pool.Exec(ctx, `
		UPDATE pipelines
		SET status = $2, started_at = coalesce(started_at, now()), finished_at = NULL
		WHERE id = (SELECT pipeline_id FROM jobs WHERE id = $1)`, id, PipelineRunning); err != nil {
		return fmt.Errorf("retry pipeline: %w", err)
	}
	return nil
}

// JobByIID returns one job of a pipeline by the number the interface shows.
func (r *PipelineRepo) JobByIID(ctx context.Context, pipelineID int64, iid int) (*Job, error) {
	var job Job

	err := r.s.pool.QueryRow(ctx, `
		SELECT j.id, j.pipeline_id, j.iid, j.name, j.stage, j.status, j.image, j.script,
		       j.allow_failure, j.started_at, j.finished_at, j.duration_ms, j.created_at
		FROM jobs j WHERE j.pipeline_id = $1 AND j.iid = $2`, pipelineID, iid,
	).Scan(&job.ID, &job.PipelineID, &job.IID, &job.Name, &job.Stage, &job.Status,
		&job.Image, &job.Script, &job.AllowFailure, &job.StartedAt, &job.FinishedAt,
		&job.DurationMS, &job.CreatedAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read job: %w", err)
	}
	return &job, nil
}

// pipelineStatusFor is how one finished job leaves the pipeline it is in.
//
// A failure is the pipeline's state whatever else is true, and a job that was
// allowed to fail does not drag it down — which is the whole point of allowing it.
func pipelineStatusFor(jobStatus string) string {
	switch jobStatus {
	case JobInterrupted:
		// Nothing is known about how far it got, so the pipeline is not called
		// failed: what is known is that it is no longer running, and that is the
		// part anybody waiting needs.
		return PipelineInterrupted
	case JobSuccess, JobSkipped:
		return PipelineSuccess
	case JobCanceled:
		return PipelineCanceled
	default:
		return PipelineFailed
	}
}

// pipelineStatusOf derives a pipeline's state from its jobs.
//
// It is computed on read rather than stored, so a job whose state changes cannot
// leave its parent saying something that is no longer true.
func pipelineStatusOf(status string, jobs []Job) string {
	if status == PipelineCanceled {
		return PipelineCanceled
	}

	pending, running, failed, interrupted := 0, 0, 0, 0
	for _, job := range jobs {
		switch job.Status {
		case JobInterrupted:
			// Counted, and not counted as a failure: the job did not break, the
			// machine running it did. It still decides the pipeline, because a
			// pipeline with a job that will never finish is not a successful one.
			interrupted++
		// A skipped job is one that will never run, and a pipeline waiting for
		// something that will never happen is a pipeline that never finishes.
		case JobSkipped:
		case JobPending:
			pending++
		case JobRunning:
			running++
		case JobFailed:
			if !job.AllowFailure {
				failed++
			}
		}
	}

	switch {
	case failed > 0:
		return PipelineFailed
	case running > 0:
		return PipelineRunning
	case pending > 0:
		return PipelinePending
	case interrupted > 0:
		return PipelineInterrupted
	default:
		return PipelineSuccess
	}
}

// StageStatus is what one stage's jobs add up to.
//
// A stage is finished only when all of its jobs are, and a stage with a failed
// job that was allowed to fail is a stage that passed: that is the whole point of
// allowing a failure.
func StageStatus(jobs []Job) string {
	if len(jobs) == 0 {
		return JobPending
	}

	failed, pending, running := 0, 0, 0
	for _, job := range jobs {
		switch job.Status {
		case JobFailed:
			if !job.AllowFailure {
				failed++
			}
		case JobSkipped:
		case JobRunning:
			running++
		case JobPending:
			pending++
		}
	}

	switch {
	case failed > 0:
		return JobFailed
	case running > 0:
		return JobRunning
	case pending > 0:
		return JobPending
	default:
		return JobSuccess
	}
}

// PipelineStatus derives a pipeline's current state from its jobs.
func PipelineStatus(pipeline *Pipeline, jobs []Job) string {
	if pipeline == nil {
		return ""
	}
	return pipelineStatusOf(pipeline.Status, jobs)
}

func jsonbOf(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	return value, nil
}
