package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	PipelineSuperseded  = "superseded"
	PipelineSuccess     = "success"
	PipelineFailed      = "failed"
	PipelineCanceled    = "canceled"
)

// Queue priorities are scheduling hints, not lifecycle statuses.
const (
	QueuePriorityLow    = 10
	QueuePriorityNormal = 100
)

// Job statuses.
const (
	JobPending  = "pending"
	JobRunning  = "running"
	JobSuccess  = "success"
	JobFailed   = "failed"
	JobCanceled = "canceled"
	JobSkipped  = "skipped"
	// JobAbandoned is a step the pipeline stopped waiting for. Distinct from failed, and from
	// skipped, in the same way refused is: nothing was attempted and nothing broke.
	JobAbandoned = "abandoned"
	// JobRefused is a job that was carried out and declined: the module was asked and
	// said no, in words, having done nothing to the cluster.
	//
	// Its own status and not a flavour of failed, because everything downstream of this word
	// means something different for the two. A failed job is a red card, a red run, and a
	// notification to somebody whose afternoon has just been interrupted. A refusal is a grey
	// card and a run that carries on to the next place: nothing broke, nothing is red in the
	// cluster, and the next attempt may well work. Recording it as a failure is the same
	// mistake as painting it red — a decision, reported as a fault.
	JobRefused = "refused"
	// JobSuperseded is a deployment that was waiting its turn and was overtaken by a newer
	// one before that turn came. Never started, so nothing in the cluster was touched, and
	// nothing broke.
	//
	// Its own status because "refused" would be a lie and "failed" would be a worse one. A
	// refusal says the module was asked and declined; nothing was asked here. A failure is a
	// red card and a notification to somebody whose afternoon has just been interrupted, over
	// a deployment that was never attempted and whose code is not the code anybody is waiting
	// for. This is grey: the run carries on, the log says what overtook it, and the newest
	// deployment is the one that reached the cluster.
	JobSuperseded = "superseded"
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
	Name          string     `json:"name"`
	Stage         string     `json:"stage"`
	StageOrder    int        `json:"stage_order"`
	Status        string     `json:"status"`
	QueuePriority int        `json:"queue_priority"`
	RunnerID      *uuid.UUID `json:"runner_id,omitempty"`
	Image        string     `json:"image"`
	Script       []string   `json:"script"`
	AllowFailure bool       `json:"allow_failure"`
	Needs        []string   `json:"needs"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	CreatedAt    time.Time  `json:"created_at"`

	// Error is why this job failed, in the words of whatever ran it.
	//
	// Kept rather than left in the log because a job that dies before printing
	// anything — a checkout that could not authenticate, a machine that would not
	// start — has a log with nothing in it, and a red badge on its own says that
	// something went wrong without saying what.
	Error string `json:"error,omitempty"`

	// Build describes an image this job produces, when it produces one. The
	// definition is the module's own: the core stores it and hands it back, and does
	// not decide what a Dockerfile is.
	Build map[string]any `json:"build,omitempty"`
	// Deploy is set on a job that deploys rather than runs a script. No runner may
	// claim such a job — there is nothing on a machine to run — so the core carries
	// it out itself, by handing the task to the module that was named. It lives on
	// the job so a deploy is part of the run that caused it: same log, same retry,
	// same place on the pipeline page.
	Deploy map[string]any `json:"deploy,omitempty"`
	// Variables are the run's own answers: what ref it is on, what tag, which commit.
	// Carried on the job because the runner is handed the job and nothing else, and
	// asking the core back would be a round trip per job to be told what the core wrote
	// down when it filed the run.
	Variables map[string]string `json:"variables,omitempty"`
	// ProjectPath and ProjectID are carried on the job rather than looked up, so a
	// runner on another machine needs one round trip rather than two.
	ProjectPath string    `json:"project_path"`
	ProjectID   uuid.UUID `json:"project_id"`
}

// CreatePipeline records a pipeline and its jobs, taking the next number for the
// project.
func (r *PipelineRepo) CreatePipeline(ctx context.Context, projectID uuid.UUID, ref, sha, source string,
	variables map[string]string, createdBy *uuid.UUID, commit Commit, jobs []Job) (*Pipeline, error) {
	pipeline, _, err := r.CreatePipelineWithCandidates(ctx, projectID, ref, sha, source,
		variables, createdBy, commit, jobs, nil)
	return pipeline, err
}

// CreatePipelineWithCandidates writes a pipeline and records its deploy candidates in the
// same transaction. This is important: announcing a push before the candidate is recorded
// would leave a window where an older build can finish and deploy just as the newer push
// is being created.
func (r *PipelineRepo) CreatePipelineWithCandidates(ctx context.Context, projectID uuid.UUID,
	ref, sha, source string, variables map[string]string, createdBy *uuid.UUID,
	commit Commit, jobs []Job, candidateKeys map[int][]string) (*Pipeline, []DeployCandidateChange, error) {

	tx, err := r.s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create pipeline: %w", err)
	}
	defer tx.Rollback(ctx)

	// The number is taken under a lock on the project rather than computed from a
	// max(): two pipelines created at the same instant must not end up with the same
	// number, and a pipeline number is something people read aloud. FOR UPDATE does
	// not work here — it cannot be combined with an aggregate — so the lock is the
	// project's, which is the thing being numbered anyway.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`,
		"pipeline:"+projectID.String()); err != nil {
		return nil, nil, fmt.Errorf("lock the project: %w", err)
	}

	var iid int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce(max(iid), 0) + 1 FROM pipelines WHERE project_id = $1`,
		projectID).Scan(&iid); err != nil {
		return nil, nil, fmt.Errorf("allocate pipeline number: %w", err)
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
		return nil, nil, fmt.Errorf("create pipeline: %w", err)
	}

	candidateJobs := map[string]int64{}
	candidateMembers := map[string][]int64{}
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
		if job.QueuePriority <= 0 {
			job.QueuePriority = QueuePriorityNormal
		}

		build, err := jsonbOf(job.Build)
		if err != nil {
			return nil, nil, err
		}
		deploy, err := jsonbOf(job.Deploy)
		if err != nil {
			return nil, nil, err
		}

		// RETURNING rather than a plain insert: the job's id is needed by whatever
		// asked for this pipeline, and reading it back separately would be a second
		// round trip for a value the database already had.
		err = tx.QueryRow(ctx, `
			INSERT INTO jobs (pipeline_id, iid, name, stage, status, image, script,
			                  allow_failure, needs, build, deploy, stage_order, queue_priority)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING id`,
			job.PipelineID, job.IID, job.Name, job.Stage, job.Status, job.Image,
			job.Script, job.AllowFailure, job.Needs, build, deploy, job.StageOrder,
			job.QueuePriority).Scan(&job.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("create job: %w", err)
		}

		for _, key := range uniqueDeployCandidateKeys(candidateKeys[index]) {
			if _, exists := candidateJobs[key]; !exists {
				candidateJobs[key] = job.ID
			}
			candidateMembers[key] = append(candidateMembers[key], job.ID)
		}
	}

	// Candidate rows are locked in one globally sorted order, rather than job order.
	// Two multi-target pipelines can otherwise deadlock if their manifests list the
	// same targets in opposite orders.
	changes := []DeployCandidateChange{}
	keys := make([]string, 0, len(candidateJobs))
	for key := range candidateJobs {
		keys = append(keys, key)
	}
	for _, key := range uniqueDeployCandidateKeys(keys) {
		change, err := registerDeployCandidateTx(ctx, tx, key, candidateJobs[key], pipeline.ID, candidateMembers[key])
		if err != nil {
			return nil, nil, err
		}
		if change != nil {
			changes = append(changes, *change)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("create pipeline: %w", err)
	}
	return pipeline, changes, nil
}

// PipelineByID returns one pipeline by its internal id.
//
// For the places that hold that id rather than a project's own numbering of it: a
// runner reporting on a job, for instance, knows which job it was given and not
// what number the job is to the people watching.
func (r *PipelineRepo) PipelineByID(ctx context.Context, id int64) (*Pipeline, error) {
	var pipeline Pipeline
	err := r.s.pool.QueryRow(ctx, `
		SELECT id, iid, project_id, ref, sha, source, status, variables, created_by_id,
		       commit_title, commit_author_name, commit_author_email,
		       created_at, started_at, finished_at
		FROM pipelines WHERE id = $1`, id,
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

// SetJobBuild writes down what a job produces, when the core learns it.
//
// The definition starts out as what the configuration said and becomes what was
// actually built: a resolved name, once the registry's own rule has been applied.
func (r *PipelineRepo) SetJobBuild(ctx context.Context, id int64, build map[string]any) error {
	raw, err := jsonbOf(build)
	if err != nil {
		return err
	}
	if _, err := r.s.pool.Exec(ctx, `UPDATE jobs SET build = $2 WHERE id = $1`, id, raw); err != nil {
		return fmt.Errorf("record a job's build: %w", err)
	}
	return nil
}

// UnfinishedJobs is how many jobs of a pipeline are still to run.
//
// What a pipeline "finished" means: not that its last job did, but that nothing of
// it is left waiting. A notification that says a pipeline passed while a second
// job is still queued is worse than none, and this is the question that decides it.
func (r *PipelineRepo) UnfinishedJobs(ctx context.Context, pipelineID int64) (int, error) {
	var count int
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs
		WHERE pipeline_id = $1 AND status IN ($2, $3)`,
		pipelineID, JobPending, JobRunning).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count unfinished jobs: %w", err)
	}
	return count, nil
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
// PipelineQuery is which runs to show and how many of them.
//
// A project with ten thousand runs is ordinary — a busy repository makes one per push —
// so the list is a window with a count beside it rather than the last twenty and no
// way to reach the rest.
type PipelineQuery struct {
	// Search matches the run number, the branch, the commit, its author and the
	// state, case-insensitively.
	//
	// It is here rather than in the interface because the list is now a page of a long
	// history: a filter that only looked at the twenty runs on screen would report
	// "nothing matches" about a run that happened yesterday.
	Search string
	// Ref narrows to one branch. Empty is every branch.
	Ref string
	// Status narrows to one state. Empty is every state.
	Status string
	// Source narrows to what started the run. Empty is both a push and a person.
	Source string
	Page   int
	// PerPage is how many runs to return.
	PerPage int
}

// PipelinePageSizeDefault is how many runs a page holds when nobody says.
const PipelinePageSizeDefault = 20

// PipelinePageSizeMax is the most one request may ask for.
const PipelinePageSizeMax = 100

// searchPattern is a search term as a pattern, or "" when there is no search.
//
// The wildcards in it are escaped: somebody looking for "50%" is looking for a run
// about the "50%" stage, not for every run there is.
func (q PipelineQuery) searchPattern() string {
	if q.Search == "" {
		return ""
	}
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q.Search))
	return "%" + escaped + "%"
}

func (q PipelineQuery) normalise() PipelineQuery {
	if q.Page < 1 {
		q.Page = 1
	}
	if q.PerPage < 1 {
		q.PerPage = PipelinePageSizeDefault
	}
	if q.PerPage > PipelinePageSizeMax {
		q.PerPage = PipelinePageSizeMax
	}
	q.Search = strings.TrimSpace(q.Search)
	q.Ref = strings.TrimSpace(q.Ref)
	q.Status = strings.TrimSpace(q.Status)
	q.Source = strings.TrimSpace(q.Source)
	return q
}

// ListPipelinesPage returns one page of runs, and how many there are in total.
//
// Newest first, because the run somebody came to look at is nearly always the last
// one, and a list ordered the other way makes them scroll to the bottom of a page of
// history to find out whether anything happened.
// AutomaticRunExists says whether this commit already started a run for this reason.
//
// Asked before a push starts anything, because the durable event log is read from the
// beginning every time the process starts: without this, every restart replays every
// push ever made and builds all of them again. Three runs of one commit is not a bug
// anybody can live with, and "has this commit been run for this reason" is a question
// with a cheap answer.
func (r *PipelineRepo) AutomaticRunExists(ctx context.Context, projectID uuid.UUID,
	sha, source string) (bool, error) {

	var exists bool
	err := r.s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pipelines
			WHERE project_id = $1 AND sha = $2 AND source = $3
		)`, projectID, sha, source).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("ask whether a run already happened: %w", err)
	}
	return exists, nil
}

func (r *PipelineRepo) ListPipelinesPage(ctx context.Context, projectID uuid.UUID,
	q PipelineQuery) ([]Pipeline, int, error) {

	q = q.normalise()

	const statement = `
		WITH matching AS (
			SELECT * FROM pipelines
			WHERE project_id = $1
			  AND ($2 = '' OR ref = $2)
			  AND ($3 = '' OR status = $3)
			  AND ($4 = '' OR source = $4)
			  AND ($5 = '' OR iid::text ILIKE $5 ESCAPE '\'
			                  OR ref ILIKE $5 ESCAPE '\'
			                  OR sha ILIKE $5 ESCAPE '\'
			                  OR COALESCE(commit_title, '') ILIKE $5 ESCAPE '\'
			                  OR COALESCE(commit_author_name, '') ILIKE $5 ESCAPE '\'
			                  OR status ILIKE $5 ESCAPE '\')
		), totals AS (
			SELECT count(*) AS total FROM matching
		), page AS (
			SELECT * FROM matching ORDER BY iid DESC LIMIT $6 OFFSET $7
		)
		SELECT page.id, page.iid, page.project_id, page.ref, page.sha, page.source,
		       page.status, page.variables, page.created_by_id, page.commit_title,
		       page.commit_author_name, page.commit_author_email, page.created_at,
		       page.started_at, page.finished_at, totals.total
		FROM page CROSS JOIN totals`

	rows, err := r.s.pool.Query(ctx, statement, projectID, q.Ref, q.Status, q.Source,
		q.searchPattern(), q.PerPage, (q.Page-1)*q.PerPage)
	if err != nil {
		return nil, 0, fmt.Errorf("list pipelines: %w", err)
	}
	defer rows.Close()

	pipelines := []Pipeline{}
	total := 0
	for rows.Next() {
		var pipeline Pipeline
		if err := rows.Scan(&pipeline.ID, &pipeline.IID, &pipeline.ProjectID, &pipeline.Ref,
			&pipeline.SHA, &pipeline.Source, &pipeline.Status, &pipeline.Variables,
			&pipeline.CreatedBy, &pipeline.CommitTitle, &pipeline.CommitAuthorName,
			&pipeline.CommitAuthorEmail, &pipeline.CreatedAt, &pipeline.StartedAt,
			&pipeline.FinishedAt, &total); err != nil {
			return nil, 0, fmt.Errorf("scan pipeline: %w", err)
		}
		pipelines = append(pipelines, pipeline)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}

	// Past the end is not the end: the count has to come back anyway, or the page
	// control cannot say where the end is.
	if total == 0 && q.Page > 1 {
		if err := r.s.pool.QueryRow(ctx,
			`SELECT count(*) FROM pipelines WHERE project_id = $1
			   AND ($2 = '' OR ref = $2) AND ($3 = '' OR status = $3) AND ($4 = '' OR source = $4)
			   AND ($5 = '' OR iid::text ILIKE $5 ESCAPE '\'
			                  OR ref ILIKE $5 ESCAPE '\'
			                  OR sha ILIKE $5 ESCAPE '\'
			                  OR COALESCE(commit_title, '') ILIKE $5 ESCAPE '\'
			                  OR COALESCE(commit_author_name, '') ILIKE $5 ESCAPE '\'
			                  OR status ILIKE $5 ESCAPE '\')`,
			projectID, q.Ref, q.Status, q.Source, q.searchPattern()).Scan(&total); err != nil {
			return nil, 0, fmt.Errorf("count pipelines: %w", err)
		}
	}

	return pipelines, total, nil
}

// JobByID returns one job with everything a runner needs to run it.
func (r *PipelineRepo) JobByID(ctx context.Context, id int64) (*Job, error) {
	// Read through the same scan as every other list of jobs. This one used to have its
	// own, written out again beside it, and a column added to the row is then a query
	// that returns one field too many for the place it is being put — an error that
	// surfaces as a runner unable to claim work, a long way from the column.
	rows, err := r.s.pool.Query(ctx, jobColumns+`
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		JOIN projects pr ON pr.id = p.project_id
		WHERE j.id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("read job: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("read job: %w", err)
		}
		return nil, ErrNotFound
	}
	return scanJob(rows)
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
	       j.script, j.allow_failure, j.needs, j.build, j.deploy, j.started_at, j.finished_at,
	       j.duration_ms, j.created_at, j.error, p.project_id, pr.path, p.variables,
	       j.stage_order, j.queue_priority`

// scanJob reads the row the cursor is standing on. It does not step the cursor: the
// caller is walking a list, and a scan that advanced it would swallow every other row.
func scanJob(rows pgx.Rows) (*Job, error) {
	return scanJobRow(rows)
}

// scanJobRow reads one job out of whatever the pool handed back — a list of rows or a
// single row, which is the same row as far as anybody reading a job is concerned.
func scanJobRow(row interface{ Scan(...any) error }) (*Job, error) {
	var job Job
	var build []byte
	var deploy []byte
	var variables []byte

	if err := row.Scan(&job.ID, &job.PipelineID, &job.IID, &job.Name, &job.Stage, &job.Status,
		&job.RunnerID, &job.Image, &job.Script, &job.AllowFailure, &job.Needs, &build, &deploy,
		&job.StartedAt, &job.FinishedAt, &job.DurationMS, &job.CreatedAt, &job.Error,
		&job.ProjectID, &job.ProjectPath, &variables, &job.StageOrder, &job.QueuePriority); err != nil {
		return nil, fmt.Errorf("scan job: %w", err)
	}

	job.Build = map[string]any{}
	decodeJSONB(build, &job.Build)
	if len(job.Build) == 0 {
		job.Build = nil
	}

	job.Variables = map[string]string{}
	decodeJSONB(variables, &job.Variables)
	if len(job.Variables) == 0 {
		job.Variables = nil
	}

	job.Deploy = map[string]any{}
	decodeJSONB(deploy, &job.Deploy)
	if len(job.Deploy) == 0 {
		job.Deploy = nil
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
	//
	// deploy IS NULL is not an optimisation. A job with a deploy has no script, so a
	// runner that claimed it would find nothing to run, exit successfully, and report
	// a deployment that never happened — the worst possible outcome, because it is a
	// green tick over something that was not done.
	var id int64
	err = tx.QueryRow(ctx, `
		UPDATE jobs SET status = $1, runner_id = $2, started_at = now()
		WHERE id = (
			SELECT j.id FROM jobs j
			WHERE j.status = $3 AND j.deploy IS NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM jobs prior
			      WHERE prior.pipeline_id = j.pipeline_id
			        AND prior.stage_order < j.stage_order
			        AND (
			          prior.status IN ($4, $5, $6, $7)
			          OR (prior.status = $8 AND NOT prior.allow_failure)
			        )
			  )
			ORDER BY j.queue_priority DESC, j.created_at, j.id
			FOR UPDATE OF j SKIP LOCKED
			LIMIT 1
		)
		-- Recheck eligibility on the UPDATE target too. If concurrent statements
		-- selected the same pending row before one acquired its lock, the loser must
		-- not update the winner's now-running row after it wakes.
		AND status = $3
		RETURNING id`, JobRunning, runnerID, JobPending,
		JobPending, JobRunning, JobCanceled, JobInterrupted, JobFailed).Scan(&id)
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

	// The job is read inside the transaction that took it, and the answer goes back only
	// once both have happened.
	//
	// Committing first and reading afterwards leaves a job marked as being worked on by
	// a machine that was never told about it: the claim is recorded, the response fails,
	// and the job sits at "running" for ever with nothing running it — a queue that waits
	// on work nobody is doing, and a page that says a build is in progress when the
	// build has not started.
	job, err := r.jobByIDTx(ctx, tx, id)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("claim job: %w", err)
	}
	return job, nil
}

// CountPendingJobs is how many jobs are sitting in the queue waiting for a runner.
//
// The same question ClaimJob asks, over the same rows, so that the number a runner is told
// is the number it would have got: counting a job that is about to be taken and reporting it
// as still waiting is the kind of off-by-one that makes a panel disagree with itself for a
// few seconds at a time and teaches people to stop reading it.
//
// Deploy jobs are excluded for the same reason ClaimJob excludes them — a job with a deploy
// has no script, so it is not work a runner can be given, and counting it would report a
// queue that cannot be worked off.
func (r *PipelineRepo) CountPendingJobs(ctx context.Context) (int, error) {
	var count int
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*) FROM jobs WHERE status = $1 AND deploy IS NULL`,
		JobPending).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending jobs: %w", err)
	}
	return count, nil
}

// jobByIDTx reads one job through a transaction, so that a job taken inside it can be
// read before it is given away.
func (r *PipelineRepo) jobByIDTx(ctx context.Context, tx pgx.Tx, id int64) (*Job, error) {
	row := tx.QueryRow(ctx, jobColumns+`
		FROM jobs j
		JOIN pipelines p ON p.id = j.pipeline_id
		JOIN projects pr ON pr.id = p.project_id
		WHERE j.id = $1`, id)
	return scanJobRow(row)
}

// ClaimDeployJob marks a deployment as this process's to carry out.
//
// Separate from FinishJob because that one stamps finished_at: a claim is not an end.
// Doing it through the wrong method would leave a job with a start and a finish at
// the same instant and a duration of zero, which is a lie about how long a rollout
// took.
//
// It only succeeds if the job is still pending, which is the lock. Two calls arriving
// together — a retried finish, a second runner reporting the last test — both see a
// pending row, and only the first update finds one to update.
func (r *PipelineRepo) ClaimDeployJob(ctx context.Context, id int64) (bool, error) {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE jobs
		SET status = $2, started_at = now()
		WHERE id = $1 AND status = $3`, id, JobRunning, JobPending)
	if err != nil {
		return false, fmt.Errorf("claim deployment job: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}

	if _, err := r.s.pool.Exec(ctx, `
		UPDATE pipelines SET started_at = COALESCE(started_at, now())
		WHERE id = (SELECT pipeline_id FROM jobs WHERE id = $1) AND status = $2`,
		id, PipelinePending); err != nil {
		return true, fmt.Errorf("mark the pipeline running: %w", err)
	}
	return true, nil
}

// FinishJob records what happened to a job.
//
// The status is taken from the runner rather than deduced: a job that let the
// script fail but was allowed to fail is a success, and only the runner knows
// which jobs those are.
func (r *PipelineRepo) FinishJob(ctx context.Context, id int64, status string, duration time.Duration, reason string) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE jobs SET status = $2, finished_at = now(), duration_ms = $3, error = $4
		WHERE id = $1`, id, status, duration.Milliseconds(), strings.TrimSpace(reason))
	if err != nil {
		return fmt.Errorf("finish job: %w", err)
	}

	// A job that failed for good takes the rest of the run with it: the jobs after
	// it will never run, and leaving them pending is a run that waits for ever on
	// something nobody is going to do. It is also what a person reading the queue
	// is misled by — a deploy job sitting at "waiting" beside a build that failed
	// says the run is still going somewhere, and the run is not.
	//
	// Skipped rather than canceled: nothing chose this, and the difference is worth
	// keeping. A failure the job was allowed to fail does not stop the run, and a
	// retry puts the job back to pending where it belongs.
	//
	// A job that is already running is left alone: it belongs to a machine that is
	// working on it, and cancelling it here would only make two things believe
	// otherwise — the machine, and whoever reads the log afterwards.
	if status == JobFailed {
		// A failure stops later stages, not sibling jobs in the same stage. An explicitly
		// allowed failure does not block anything downstream.
		if _, err := r.s.pool.Exec(ctx, `
			UPDATE jobs SET status = $2, finished_at = now()
			WHERE pipeline_id = (SELECT pipeline_id FROM jobs WHERE id = $1 AND NOT allow_failure)
			  AND stage_order > (SELECT stage_order FROM jobs WHERE id = $1)
			  AND status = $3`,
			id, JobSkipped, JobPending); err != nil {
			return fmt.Errorf("skip the stages after a failed one: %w", err)
		}
	}

	// The pipeline is finished when nothing of its is still waiting. Both the
	// pending and the running states count as waiting: a pipeline whose second job
	// has not started is not over, however the first went.
	if _, err := r.s.pool.Exec(ctx, `
		UPDATE pipelines
		SET finished_at = now(),
		    status = CASE
		      WHEN pipelines.status = $2 OR EXISTS (
		          SELECT 1 FROM jobs j WHERE j.id = $1 AND j.status = $3
		      ) THEN $2
		      WHEN EXISTS (
		          SELECT 1 FROM jobs j
		          WHERE j.pipeline_id = pipelines.id
		            AND j.status = $4 AND NOT j.allow_failure
		      ) THEN $5
		      WHEN EXISTS (
		          SELECT 1 FROM jobs j
		          WHERE j.pipeline_id = pipelines.id AND j.status = $6
		      ) THEN $7
		      WHEN EXISTS (
		          SELECT 1 FROM jobs j
		          WHERE j.pipeline_id = pipelines.id AND j.status = $8
		      ) THEN $9
		      ELSE $10
		    END
		WHERE id = (SELECT pipeline_id FROM jobs WHERE id = $1)
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs
		      WHERE pipeline_id = pipelines.id AND status IN ($11, $12))`,
		id, PipelineCanceled, JobCanceled, JobFailed, PipelineFailed,
		JobInterrupted, PipelineInterrupted, JobSuperseded, PipelineSuperseded,
		PipelineSuccess, JobPending, JobRunning); err != nil {
		return fmt.Errorf("finish pipeline: %w", err)
	}
	return nil
}


// DeprioritizeObsoleteBuilds moves pending artifact-producing jobs to the low-priority queue
// when all deployments in their pipeline have been superseded. A running build is never touched.
func (r *PipelineRepo) DeprioritizeObsoleteBuilds(ctx context.Context, pipelineID int64) ([]int64, error) {
	rows, err := r.s.pool.Query(ctx, `
		UPDATE jobs
		SET queue_priority = $2
		WHERE pipeline_id = $1 AND build IS NOT NULL AND status = $3
		  AND queue_priority > $2
		  AND EXISTS (
		      SELECT 1 FROM jobs d
		      WHERE d.pipeline_id = jobs.pipeline_id
		        AND d.deploy IS NOT NULL AND d.status = $4
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM jobs d
		      WHERE d.pipeline_id = jobs.pipeline_id
		        AND d.deploy IS NOT NULL AND d.status IN ($3, $5)
		  )
		RETURNING id`, pipelineID, QueuePriorityLow, JobPending, JobSuperseded, JobRunning)
	if err != nil {
		return nil, fmt.Errorf("deprioritize obsolete build jobs: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read deprioritized build job: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read deprioritized build jobs: %w", err)
	}
	return ids, nil
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
		SET status = $2, runner_id = NULL, started_at = NULL, finished_at = NULL, duration_ms = 0,
		    error = '' 
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
		       j.allow_failure, j.started_at, j.finished_at, j.duration_ms, j.created_at, j.error,
		       j.stage_order, j.queue_priority
		FROM jobs j WHERE j.pipeline_id = $1 AND j.iid = $2`, pipelineID, iid,
	).Scan(&job.ID, &job.PipelineID, &job.IID, &job.Name, &job.Stage, &job.Status,
		&job.Image, &job.Script, &job.AllowFailure, &job.StartedAt, &job.FinishedAt,
		&job.DurationMS, &job.CreatedAt, &job.Error, &job.StageOrder, &job.QueuePriority)
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
	case JobSuperseded:
		return PipelineSuperseded
	case JobSuccess, JobSkipped, JobRefused:
		// A refused deployment does not fail the run. Nothing broke, and the places after
		// it in the same run are waiting on it — a run failed over a busy cluster is a run
		// that took every other place down with it for no reason at all.
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

	pending, running, failed, interrupted, superseded := 0, 0, 0, 0, 0
	for _, job := range jobs {
		switch job.Status {
		case JobInterrupted:
			// Counted, and not counted as a failure: the job did not break, the
			// machine running it did. It still decides the pipeline, because a
			// pipeline with a job that will never finish is not a successful one.
			interrupted++
		case JobSuperseded:
			superseded++
		// A skipped job is one that will never run, and a pipeline waiting for
		// something that will never happen is a pipeline that never finishes.
		case JobSkipped, JobRefused:
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
	case superseded > 0:
		return PipelineSuperseded
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

	failed, pending, running, passed, skipped := 0, 0, 0, 0, 0
	for _, job := range jobs {
		switch job.Status {
		case JobFailed:
			if !job.AllowFailure {
				failed++
			}
		case JobSkipped, JobRefused, JobSuperseded:
			// Counted with the skipped, and for the same reason: work that did not
			// happen is work that did not happen, whether a rule passed it over or a
			// module declined it. Counting it as passed would put a green tick on a
			// deployment nobody made.
			skipped++
		case JobRunning:
			running++
		case JobPending:
			pending++
		case JobSuccess:
			passed++
		}
	}

	switch {
	case failed > 0:
		return JobFailed
	case running > 0:
		return JobRunning
	case pending > 0:
		return JobPending
	case passed == 0 && skipped > 0:
		// Nothing in this stage ran: an earlier one failed, and these were never
		// reached. Drawing it as passed puts a green tick on work that did not
		// happen, which is the one thing a colour on a page must never do.
		return JobSkipped
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
