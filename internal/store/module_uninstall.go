package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ewolf/dogit/internal/models"
)

// isUniqueViolation reports whether an error is the database refusing a
// duplicate. It is the one database error worth branching on: the alternative is
// a check and an insert racing each other.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// decodeJSONB reads a JSON column into dst, leaving dst alone if the column holds
// something we cannot read. A corrupt row should show up as a thinner answer,
// not as a page that will not load.
func decodeJSONB(raw []byte, dst any) {
	if len(raw) == 0 {
		return
	}
	_ = json.Unmarshal(raw, dst)
}

// ModuleUninstallRepo keeps module removals and their logs.
//
// The log lives here rather than in the browser because the browser is where
// the administrator was when they started, and they will not be there at the end.
type ModuleUninstallRepo struct{ s *Store }

func (s *Store) ModuleUninstall() *ModuleUninstallRepo { return &ModuleUninstallRepo{s: s} }

// logTailLimit is how many lines a single job keeps.
//
// A module deleting half a terabyte can produce a line per image; nobody reads
// them all. Keeping a tail bounds the table without touching the part that
// matters, and the summary at the end carries the totals anyway.
const logTailLimit = 5000

// StalledAfter is how long a removal may say nothing before it is considered
// stalled.
//
// Generous, because a module deleting a hundred thousand small blobs is working,
// not stuck, and telling the administrator it has stalled would be a lie.
const StalledAfter = 5 * time.Minute

// ErrJobRunning is returned when a module already has a removal in flight.
var ErrJobRunning = errors.New("module removal is already running")

// Start records a new job for a module.
//
// The uniqueness that matters is enforced by the database, not here: two
// administrators clicking Delete at the same moment is an ordinary thing that
// should not depend on both of them losing a race in Go.
func (r *ModuleUninstallRepo) Start(ctx context.Context, integrationID uuid.UUID, options []string) (*models.UninstallJob, error) {
	if options == nil {
		options = []string{}
	}

	job := &models.UninstallJob{
		ID:            uuid.New(),
		IntegrationID: integrationID,
		Options:       options,
		Status:        models.UninstallQueued,
	}

	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO module_uninstall_jobs (id, integration_id, options, status)
		VALUES ($1, $2, $3, $4)`,
		job.ID, integrationID, options, job.Status)
	if err != nil {
		// The partial unique index refuses a second open job for the same module.
		if isUniqueViolation(err) {
			return nil, ErrJobRunning
		}
		return nil, fmt.Errorf("start module uninstall: %w", err)
	}
	return job, nil
}

// Current returns a module's unfinished job, if it has one.
func (r *ModuleUninstallRepo) Current(ctx context.Context, integrationID uuid.UUID) (*models.UninstallJob, error) {
	return r.one(ctx, `
		WHERE integration_id = $1 AND finished_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, integrationID)
}

// Latest returns a module's most recent job, finished or not.
func (r *ModuleUninstallRepo) Latest(ctx context.Context, integrationID uuid.UUID) (*models.UninstallJob, error) {
	return r.one(ctx, `WHERE integration_id = $1 ORDER BY created_at DESC LIMIT 1`, integrationID)
}

// ByID returns one job.
func (r *ModuleUninstallRepo) ByID(ctx context.Context, id uuid.UUID) (*models.UninstallJob, error) {
	return r.one(ctx, `WHERE id = $1`, id)
}

func (r *ModuleUninstallRepo) one(ctx context.Context, where string, args ...any) (*models.UninstallJob, error) {
	var job models.UninstallJob
	var options []byte
	var failure sql.NullString

	err := r.s.pool.QueryRow(ctx, `
		SELECT id, integration_id, options, status, progress_done, progress_total,
		       summary, error, started_at, finished_at, created_at, updated_at, last_line_at
		FROM module_uninstall_jobs `+where, args...,
	).Scan(&job.ID, &job.IntegrationID, &options, &job.Status,
		&job.ProgressDone, &job.ProgressTotal, &job.Summary, &failure,
		&job.StartedAt, &job.FinishedAt, &job.CreatedAt, &job.UpdatedAt, &job.LastLineAt)
	if errors.Is(err, pgxNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read module uninstall job: %w", err)
	}
	job.Options = []string{}
	decodeJSONB(options, &job.Options)
	job.Error = failure.String
	return &job, nil
}

// MarkRunning stamps the moment work actually started.
func (r *ModuleUninstallRepo) MarkRunning(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs
		SET status = $2, started_at = COALESCE(started_at, now()), updated_at = now()
		WHERE id = $1`, id, models.UninstallRunning)
	if err != nil {
		return fmt.Errorf("mark module uninstall running: %w", err)
	}
	return nil
}

// Progress records what the module said it had done so far.
func (r *ModuleUninstallRepo) Progress(ctx context.Context, id uuid.UUID, done, total *int64) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs
		SET progress_done = $2, progress_total = $3, updated_at = now()
		WHERE id = $1`, id, done, total)
	if err != nil {
		return fmt.Errorf("record module uninstall progress: %w", err)
	}
	return nil
}

// Finish closes a job with its summary, or with the reason it could not be
// finished.
func (r *ModuleUninstallRepo) Finish(ctx context.Context, id uuid.UUID, status string, summary map[string]any, failure string) error {
	// A nil summary leaves whatever is already there. The module's own totals are
	// written when its stream ends, and the caller then records the same outcome
	// without them; the second write must not erase the first.
	_, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs
		SET status = $2, summary = COALESCE($3, summary), error = $4,
		    finished_at = now(), updated_at = now()
		WHERE id = $1`, id, status, summary, failure)
	if err != nil {
		return fmt.Errorf("finish module uninstall: %w", err)
	}
	return nil
}

// Touch notes that the module said something, which is what stops a job looking
// stalled while it is perfectly alive.
//
// A job that was declared stalled and then hears from the module again is put
// back to work: the module did not stop, the core gave up waiting, and the only
// evidence that settles the question is the module speaking.
func (r *ModuleUninstallRepo) Touch(ctx context.Context, id uuid.UUID) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs
		SET last_line_at = now(),
		    status = CASE WHEN status = $2 THEN $3 ELSE status END,
		    updated_at = now()
		WHERE id = $1`, id, models.UninstallStalled, models.UninstallRunning)
	if err != nil {
		return fmt.Errorf("touch module uninstall: %w", err)
	}
	return nil
}

// Stale marks jobs whose module has stopped talking.
//
// A stalled job is not deleted: the administrator needs to see the last line it
// managed to write, which is usually the line explaining where it stopped.
func (r *ModuleUninstallRepo) Stale(ctx context.Context, silence time.Duration) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs SET status = $1, updated_at = now()
		WHERE finished_at IS NULL AND status = $2
		  AND COALESCE(last_line_at, started_at, created_at) < now() - $3::interval`,
		models.UninstallStalled, models.UninstallRunning, silence.String())
	if err != nil {
		return 0, fmt.Errorf("stall module uninstall jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// InterruptOpen marks every unfinished job as interrupted.
//
// This runs at startup, because the only jobs running when the core was stopped
// are the ones it stopped running. Their outcome is unknown rather than bad:
// the module may well have finished deleting, and nobody heard.
func (r *ModuleUninstallRepo) InterruptOpen(ctx context.Context) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE module_uninstall_jobs
		SET status = $1, error = $2, finished_at = now(), updated_at = now()
		WHERE finished_at IS NULL`, models.UninstallInterrupted,
		"the core was restarted while this removal was running; its outcome is unknown")
	if err != nil {
		return 0, fmt.Errorf("interrupt module uninstall jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AppendLog writes one line and trims the job's log back to its tail.
//
// The trim is by count rather than by age because what matters is how much there
// is to read, not when it was written.
func (r *ModuleUninstallRepo) AppendLog(ctx context.Context, jobID uuid.UUID, level, message string, progress map[string]any) (*models.UninstallLogLine, error) {
	line := &models.UninstallLogLine{
		JobID:    jobID,
		Level:    level,
		Message:  message,
		Progress: progress,
	}

	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO module_uninstall_log (job_id, level, message, progress)
		VALUES ($1, $2, $3, $4)
		RETURNING id, created_at`,
		jobID, level, message, progress,
	).Scan(&line.ID, &line.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("append module uninstall log: %w", err)
	}

	if _, err := r.s.pool.Exec(ctx, `
		DELETE FROM module_uninstall_log
		WHERE job_id = $1 AND id <= (
			SELECT id FROM module_uninstall_log WHERE job_id = $1
			ORDER BY id DESC OFFSET $2 LIMIT 1
		)`, jobID, logTailLimit); err != nil {
		return nil, fmt.Errorf("trim module uninstall log: %w", err)
	}
	return line, nil
}

// LogAfter returns log lines with an id greater than after, oldest first.
//
// This is what makes a dropped connection harmless: the browser remembers the
// last id it saw and asks for everything after it, so a line can never fall
// into the gap between two connections.
func (r *ModuleUninstallRepo) LogAfter(ctx context.Context, jobID uuid.UUID, after int64, limit int) ([]models.UninstallLogLine, error) {
	if limit <= 0 || limit > logTailLimit {
		limit = 200
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT id, job_id, created_at, level, message, progress
		FROM module_uninstall_log
		WHERE job_id = $1 AND id > $2
		ORDER BY id ASC LIMIT $3`, jobID, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read module uninstall log: %w", err)
	}
	defer rows.Close()

	lines := []models.UninstallLogLine{}
	for rows.Next() {
		var line models.UninstallLogLine
		var progress []byte
		if err := rows.Scan(&line.ID, &line.JobID, &line.CreatedAt,
			&line.Level, &line.Message, &progress); err != nil {
			return nil, fmt.Errorf("scan module uninstall log: %w", err)
		}
		line.Progress = map[string]any{}
		decodeJSONB(progress, &line.Progress)
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read module uninstall log: %w", err)
	}
	return lines, nil
}
