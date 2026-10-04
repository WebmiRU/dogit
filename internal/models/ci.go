package models

import (
	"time"

	"github.com/google/uuid"
)

type MergeRequestState string

const (
	MRStateOpened MergeRequestState = "opened"
	MRStateMerged MergeRequestState = "merged"
	MRStateClosed MergeRequestState = "closed"
)

type MergeRequest struct {
	ID             int64             `json:"id"`
	IID            int               `json:"iid"`
	ProjectID      uuid.UUID         `json:"project_id"`
	AuthorID       uuid.UUID         `json:"author_id"`
	SourceBranch   string            `json:"source_branch"`
	TargetBranch   string            `json:"target_branch"`
	Title          string            `json:"title"`
	Description    string            `json:"description"`
	State          MergeRequestState `json:"state"`
	MergeCommitSHA *string           `json:"merge_commit_sha,omitempty"`
	SHA            string            `json:"sha"`
	MergedAt       *time.Time        `json:"merged_at,omitempty"`
	MergedByID     *uuid.UUID        `json:"merged_by_id,omitempty"`
	ClosedAt       *time.Time        `json:"closed_at,omitempty"`
	Squash         bool              `json:"squash"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`

	// Joined in for display, so a list of requests does not need a query per row
	// to know who opened one or where it lives.
	AuthorName       string `json:"author_name"`
	AuthorUsername   string `json:"author_username"`
	MergedByName     string `json:"merged_by_name,omitempty"`
	MergedByUsername string `json:"merged_by_username,omitempty"`
	// URL is where the request lives in the web interface.
	URL     string   `json:"url"`
	Project *Project `json:"project,omitempty"`

	// Computed fields, not persisted.
	HasConflicts bool      `json:"has_conflicts"`
	MergeStatus  string    `json:"merge_status"`
	DiffStats    *DiffStat `json:"diff_stats,omitempty"`
}

// IsOpen reports whether the merge request is still waiting for a decision.
func (m *MergeRequest) IsOpen() bool { return m.State == MRStateOpened }

type MergeRequestNote struct {
	ID             int64     `json:"id"`
	MergeRequestID int64     `json:"merge_request_id"`
	AuthorID       uuid.UUID `json:"author_id"`
	AuthorName     string    `json:"author_name"`
	AuthorUsername string    `json:"author_username"`
	Body           string    `json:"body"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type DiffStat struct {
	FilesChanged int `json:"files_changed"`
	Additions    int `json:"additions"`
	Deletions    int `json:"deletions"`
}

type PipelineSource string

const (
	PipelineSourcePush         PipelineSource = "push"
	// PipelineSourceTag is a run started by a tag being created. Its own source
	// because a release and a build somebody kicked off by hand are different events:
	// one was chosen, and the page and the notifications should be able to say which.
	PipelineSourceTag          PipelineSource = "tag"
	PipelineSourceWeb          PipelineSource = "web"
	PipelineSourceSchedule     PipelineSource = "schedule"
	PipelineSourceMergeRequest PipelineSource = "merge_request_event"
)

type PipelineStatus string

const (
	PipelinePending  PipelineStatus = "pending"
	PipelineRunning  PipelineStatus = "running"
	PipelineSuccess  PipelineStatus = "success"
	PipelineFailed   PipelineStatus = "failed"
	PipelineCanceled PipelineStatus = "canceled"
	PipelineSkipped  PipelineStatus = "skipped"
	PipelineManual   PipelineStatus = "manual"
)

type Pipeline struct {
	ID              int64             `json:"id"`
	ProjectID       uuid.UUID         `json:"project_id"`
	IID             int               `json:"iid"`
	Ref             string            `json:"ref"`
	SHA             string            `json:"sha"`
	Source          PipelineSource    `json:"source"`
	Status          PipelineStatus    `json:"status"`
	MergeRequestIID *int              `json:"merge_request_iid,omitempty"`
	Variables       map[string]string `json:"variables,omitempty"`
	CreatedByID     *uuid.UUID        `json:"created_by_id,omitempty"`
	CreatedAt       time.Time         `json:"created_at"`
	StartedAt       *time.Time        `json:"started_at,omitempty"`
	FinishedAt      *time.Time        `json:"finished_at,omitempty"`
}

type JobStage string

type JobStatus string

const (
	JobPending  JobStatus = "pending"
	JobRunning  JobStatus = "running"
	JobSuccess  JobStatus = "success"
	JobFailed   JobStatus = "failed"
	JobCanceled JobStatus = "canceled"
	JobSkipped  JobStatus = "skipped"
	JobManual   JobStatus = "manual"
)

type Job struct {
	ID           int64      `json:"id"`
	PipelineID   int64      `json:"pipeline_id"`
	IID          int        `json:"iid"`
	Name         string     `json:"name"`
	Stage        string     `json:"stage"`
	Status       JobStatus  `json:"status"`
	RunnerID     *uuid.UUID `json:"runner_id,omitempty"`
	Image        string     `json:"image"`
	Script       []string   `json:"script"`
	AllowFailure bool       `json:"allow_failure"`
	Needs        []string   `json:"needs,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	DurationMS   int64      `json:"duration_ms"`
	CreatedAt    time.Time  `json:"created_at"`
}

type Artifact struct {
	ID        int64     `json:"id"`
	JobID     int64     `json:"job_id"`
	ProjectID uuid.UUID `json:"project_id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"` // path on disk, relative to artifact root
	Size      int64     `json:"size"`
	Type      string    `json:"type"`
	Protected bool      `json:"protected"`
	CreatedAt time.Time `json:"created_at"`
}

type Environment struct {
	ID          int64     `json:"id"`
	ProjectID   uuid.UUID `json:"project_id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	ExternalURL string    `json:"external_url"`
	CreatedAt   time.Time `json:"created_at"`
}

type Deployment struct {
	ID          int64            `json:"id"`
	ProjectID   uuid.UUID        `json:"project_id"`
	Environment string           `json:"environment"`
	JobID       int64            `json:"job_id"`
	SHA         string           `json:"sha"`
	Ref         string           `json:"ref"`
	Status      DeploymentStatus `json:"status"`
	CreatedAt   time.Time        `json:"created_at"`
}

type DeploymentStatus string

const (
	DeploymentRunning  DeploymentStatus = "running"
	DeploymentSuccess  DeploymentStatus = "success"
	DeploymentFailed   DeploymentStatus = "failed"
	DeploymentCanceled DeploymentStatus = "canceled"
)

type IssueState string

const (
	IssueOpened IssueState = "opened"
	IssueClosed IssueState = "closed"
)

type Issue struct {
	ID          int64      `json:"id"`
	IID         int        `json:"iid"`
	ProjectID   uuid.UUID  `json:"project_id"`
	AuthorID    uuid.UUID  `json:"author_id"`
	AssigneeID  *uuid.UUID `json:"assignee_id,omitempty"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	State       IssueState `json:"state"`
	Labels      []string   `json:"labels"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
}

type IssueNote struct {
	ID        int64     `json:"id"`
	IssueID   int64     `json:"issue_id"`
	AuthorID  uuid.UUID `json:"author_id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Runner struct {
	ID            uuid.UUID  `json:"id"`
	Description   string     `json:"description"`
	RunnerType    string     `json:"runner_type"` // project | instance
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	TokenHash     []byte     `json:"-"`
	Online        bool       `json:"online"`
	Paused        bool       `json:"paused"`
	LastContactAt *time.Time `json:"last_contact_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}
