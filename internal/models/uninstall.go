package models

import (
	"time"

	"github.com/google/uuid"
)

// UninstallJob states.
//
// There is no "cancelling": removal is not a thing you can half-regret, so the
// only way out is through the end. "stalled" and "interrupted" are not failures
// either — they say the job stopped talking and nobody has yet decided whether
// that means it is dead.
const (
	UninstallQueued      = "queued"
	UninstallRunning     = "running"
	UninstallDone        = "done"
	UninstallFailed      = "failed"
	UninstallStalled     = "stalled"
	UninstallInterrupted = "interrupted"
)

// UninstallJob is one module's removal, from the click to the last line of the
// log.
type UninstallJob struct {
	ID            uuid.UUID      `json:"id"`
	IntegrationID uuid.UUID      `json:"integration_id"`
	Options       []string       `json:"options"`
	Status        string         `json:"status"`
	ProgressDone  *int64         `json:"progress_done,omitempty"`
	ProgressTotal *int64         `json:"progress_total,omitempty"`
	Summary       map[string]any `json:"summary,omitempty"`
	Error         string         `json:"error,omitempty"`
	StartedAt     *time.Time     `json:"started_at,omitempty"`
	FinishedAt    *time.Time     `json:"finished_at,omitempty"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	LastLineAt    *time.Time     `json:"last_line_at,omitempty"`
}

// Finished says whether the job will not move again by itself.
func (j *UninstallJob) Finished() bool {
	if j == nil {
		return true
	}
	switch j.Status {
	case UninstallDone, UninstallFailed, UninstallInterrupted:
		return true
	}
	return false
}

// Percent is the module's progress as a whole number, or false when it reports no
// progress at all. A module that deletes one file at a time and a module that
// deletes a hundred thousand are both fine; neither should show a bar invented
// from what it happened to say last.
func (j *UninstallJob) Percent() (int, bool) {
	if j == nil {
		return 0, false
	}
	if j.ProgressDone == nil || j.ProgressTotal == nil || *j.ProgressTotal <= 0 {
		return 0, false
	}
	if *j.ProgressDone > *j.ProgressTotal {
		return 100, true
	}
	return int(*j.ProgressDone * 100 / *j.ProgressTotal), true
}

// UninstallLogLine is one line of the log, as the module wrote it.
type UninstallLogLine struct {
	ID        int64          `json:"id"`
	JobID     uuid.UUID      `json:"job_id"`
	CreatedAt time.Time      `json:"created_at"`
	Level     string         `json:"level"`
	Message   string         `json:"message"`
	Progress  map[string]any `json:"progress,omitempty"`
}
