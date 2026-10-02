package models

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// IntegrationStatus is the lifecycle state of a registered module.
const (
	IntegrationPending  = "pending"  // registered, heartbeat not seen yet
	IntegrationOnline   = "online"   // heartbeat within the expected window
	IntegrationOffline  = "offline"  // heartbeat missed
	IntegrationDisabled = "disabled" // switched off by an administrator
)

// Capability names the right a module grants to a user token. Modules declare the
// set they understand in their manifest; the core never invents its own.
const (
	ScopeRegistryPull   = "registry:pull"
	ScopeRegistryPush   = "registry:push"
	ScopeRegistryDelete = "registry:delete"
	ScopeCacheRead      = "cache:read"
	ScopeCacheWrite     = "cache:write"
	ScopeBuilderBuild   = "builder:build"
)

// Manifest is what a module reports about itself at registration time.
type Manifest struct {
	// Version is the module's own version string.
	Version string `json:"version"`
	// Scopes lists the permission strings the module understands.
	Scopes []string `json:"scopes"`
	// Settings describes the settings the module accepts, so the admin UI can
	// render a form without hard-coding knowledge of any particular module.
	Settings []SettingSpec `json:"settings"`
	// DependsOn lists module kinds this module needs.
	DependsOn []string `json:"depends_on,omitempty"`
	// Description is free text for the module list.
	Description string `json:"description,omitempty"`
}

// SettingSpec is one configurable setting of a module.
type SettingSpec struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // string | bool | int | enum | url
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Description string   `json:"description,omitempty"`
	// Secret marks a value that is write-only: it is stored but never returned.
	Secret bool `json:"secret,omitempty"`
}

// Integration is a registered module.
type Integration struct {
	ID            uuid.UUID      `json:"id"`
	Kind          string         `json:"kind"`
	Name          string         `json:"name"`
	Endpoint      string         `json:"endpoint"`
	ModuleVersion string         `json:"module_version"`
	Capabilities  Manifest       `json:"capabilities"`
	Status        string         `json:"status"`
	Enabled       bool           `json:"enabled"`
	LastSeenAt    *time.Time     `json:"last_seen_at,omitempty"`
	RegisteredAt  time.Time      `json:"registered_at"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Settings      map[string]any `json:"settings,omitempty"`
}

// ScopeType identifies how widely a setting applies.
const (
	ScopeInstance = "instance"
	ScopeGroup    = "group"
	ScopeProject  = "project"
)

// IntegrationSetting is one setting at one scope.
type IntegrationSetting struct {
	ID            int64           `json:"id"`
	IntegrationID uuid.UUID       `json:"integration_id"`
	ScopeType     string          `json:"scope_type"`
	ScopeID       *uuid.UUID      `json:"scope_id,omitempty"`
	Key           string          `json:"key"`
	Value         json.RawMessage `json:"value"`
	UpdatedAt     time.Time       `json:"updated_at"`
}

// ModuleToken is an instance token used by a module to register itself.
type ModuleToken struct {
	ID          uuid.UUID  `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

// IntegrationToken is a short-lived user token scoped to a module.
type IntegrationToken struct {
	ID            int64      `json:"id"`
	IntegrationID uuid.UUID  `json:"integration_id"`
	UserID        *uuid.UUID `json:"user_id,omitempty"`
	ProjectID     *uuid.UUID `json:"project_id,omitempty"`
	Scopes        []string   `json:"scopes"`
	ExpiresAt     time.Time  `json:"expires_at"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
}

// Introspection is the answer the core gives a module that asks "who is this?".
type Introspection struct {
	// Active is false for an expired or revoked token; the module must then deny
	// the request rather than treat the caller as anonymous-but-allowed.
	Active      bool       `json:"active"`
	UserID      *uuid.UUID `json:"user_id,omitempty"`
	Username    string     `json:"username,omitempty"`
	ProjectID   *uuid.UUID `json:"project_id,omitempty"`
	Scopes      []string   `json:"scopes"`
	AccessLevel int        `json:"access_level,omitempty"`
	AccessName  string     `json:"access_name,omitempty"`
}
