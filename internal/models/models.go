// Package models contains the domain types shared between the store and the
// HTTP/SSH layers.
package models

import (
	"time"

	"github.com/google/uuid"
)

// Access levels, borrowed from GitLab's numeric access levels. Higher is more
// powerful. Guest can only read, Maintainer can merge and administer.
const (
	AccessLevelGuest      = 10
	AccessLevelReporter   = 20
	AccessLevelDeveloper  = 30
	AccessLevelMaintainer = 40
	AccessLevelOwner      = 50
)

// AccessLevelName returns the human readable role name for a level.
func AccessLevelName(level int) string {
	switch {
	case level >= AccessLevelOwner:
		return "Owner"
	case level >= AccessLevelMaintainer:
		return "Maintainer"
	case level >= AccessLevelDeveloper:
		return "Developer"
	case level >= AccessLevelReporter:
		return "Reporter"
	default:
		return "Guest"
	}
}

// ValidAccessLevel reports whether level is one of the known levels.
func ValidAccessLevel(level int) bool {
	switch level {
	case AccessLevelGuest, AccessLevelReporter, AccessLevelDeveloper, AccessLevelMaintainer, AccessLevelOwner:
		return true
	}
	return false
}

type User struct {
	ID           uuid.UUID  `json:"id"`
	Username     string     `json:"username"`
	Email        string     `json:"email"`
	Name         string     `json:"name"`
	PasswordHash []byte     `json:"-"`
	IsAdmin      bool       `json:"is_admin"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	LastSignIn   *time.Time `json:"last_sign_in,omitempty"`
}

type SSHKey struct {
	ID          uuid.UUID  `json:"id"`
	UserID      uuid.UUID  `json:"user_id"`
	Title       string     `json:"title"`
	Fingerprint string     `json:"fingerprint"`
	PublicKey   string     `json:"public_key"`
	CreatedAt   time.Time  `json:"created_at"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

type Group struct {
	ID   uuid.UUID `json:"id"`
	Slug string    `json:"slug"`
	Name string    `json:"name"`
	// Description is a line about what the group is for, in the group's own words.
	// Empty means nobody has written one, which the page says rather than inventing.
	Description string    `json:"description"`
	FullPath    string    `json:"full_path"`
	CreatedAt   time.Time `json:"created_at"`
}

type GroupMember struct {
	ID        uuid.UUID `json:"id"`
	GroupID   uuid.UUID `json:"group_id"`
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

// GroupRole is a named bundle of access levels assigned to a source
// (a user, or every member of a group) on a group or project.
type GroupRole struct {
	ID             uuid.UUID  `json:"id"`
	GroupID        uuid.UUID  `json:"group_id"`
	Name           string     `json:"name"`
	MinAccessLevel int        `json:"min_access_level"`
	MaxAccessLevel int        `json:"max_access_level"`
	SourceUserID   *uuid.UUID `json:"source_user_id,omitempty"`
	SourceGroupID  *uuid.UUID `json:"source_group_id,omitempty"`
}

type Project struct {
	ID            uuid.UUID  `json:"id"`
	GroupID       *uuid.UUID `json:"group_id,omitempty"`
	Path          string     `json:"path"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Visibility    string     `json:"visibility"` // private | internal | public
	DefaultBranch string     `json:"default_branch"`

	AllowPipelineTrigger bool   `json:"allow_pipeline_trigger"`
	// AutoDeployPaused stops a push or a tag from starting a run by itself. Manual
	// runs are unaffected: this is a brake, not a policy.
	AutoDeployPaused bool `json:"auto_deploy_paused"`
	AllowMerge           bool   `json:"allow_merge"`
	MergeMethod          string `json:"merge_method"` // merge | ff | squash
	RemoveSourceBranch   bool   `json:"remove_source_branch"`
	PublicEmails         bool   `json:"public_emails"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ArchivedAt *time.Time `json:"archived_at,omitempty"`

	// RepoPath is the absolute path to the bare repository on disk.
	RepoPath string `json:"-"`
}

// FullPath is "group/path" for grouped projects, "path" otherwise.
func (p Project) FullPath() string {
	if p.GroupID == nil {
		return p.Path
	}
	return p.Path
}

// CloneSSHPath is the path portion used in clone URLs: "group/path" or "path".
func (p Project) ClonePath() string { return p.Path }

type ProjectMember struct {
	ID        uuid.UUID `json:"id"`
	ProjectID uuid.UUID `json:"project_id"`
	UserID    uuid.UUID `json:"user_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ProjectRole struct {
	ID             uuid.UUID  `json:"id"`
	ProjectID      uuid.UUID  `json:"project_id"`
	Name           string     `json:"name"`
	MinAccessLevel int        `json:"min_access_level"`
	MaxAccessLevel int        `json:"max_access_level"`
	SourceUserID   *uuid.UUID `json:"source_user_id,omitempty"`
	SourceGroupID  *uuid.UUID `json:"source_group_id,omitempty"`
}

type Session struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	UserAgent  string     `json:"-"`
	IP         string     `json:"-"`
}

// PATScopes enumerates the permissions a personal access token can carry.
type PATScopes []string

const (
	ScopeReadUser     = "read_user"
	ScopeReadRepo     = "read_repository"
	ScopeWriteRepo    = "write_repository"
	ScopeReadAPI      = "read_api"
	ScopeFullAPI      = "api"
	ScopeCI           = "ci"
	ScopeManageTokens = "manage_tokens"
)

type PersonalAccessToken struct {
	ID         uuid.UUID  `json:"id"`
	UserID     uuid.UUID  `json:"user_id"`
	Name       string     `json:"name"`
	TokenHash  []byte     `json:"-"`
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// Commit is a lightweight snapshot of a git commit, written by the
// post-receive hook so that commit feeds do not have to walk git history.
type Commit struct {
	SHA            string    `json:"sha"`
	ProjectID      uuid.UUID `json:"project_id"`
	Ref            string    `json:"ref"`
	Branch         string    `json:"branch"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	Message        string    `json:"message"`
	Timestamp      time.Time `json:"timestamp"`
	Added          int       `json:"added"`
	Removed        int       `json:"removed"`
}
