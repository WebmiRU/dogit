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
	// Database asks the core to provision a database in the shared cluster when the
	// module registers. The credentials arrive once, in the registration response.
	Database bool `json:"database,omitempty"`
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
	ID            uuid.UUID  `json:"id"`
	Kind          string     `json:"kind"`
	Name          string     `json:"name"`
	Endpoint      string     `json:"endpoint"`
	ModuleVersion string     `json:"module_version"`
	Capabilities  Manifest   `json:"capabilities"`
	Status        string     `json:"status"`
	Enabled       bool       `json:"enabled"`
	LastSeenAt    *time.Time `json:"last_seen_at,omitempty"`
	// DatabaseName and DatabaseRole are the non-secret half of a provisioned
	// database. The password is not stored anywhere in the core.
	DatabaseName string `json:"database_name,omitempty"`
	DatabaseRole string `json:"database_role,omitempty"`

	RegisteredAt time.Time      `json:"registered_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Settings     map[string]any `json:"settings,omitempty"`
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

	// ModuleEnabled says whether the module the token was minted for is still
	// allowed to act. A token outlives an administrator's decision to forbid the
	// module, and the token itself is not something we can tell the module to
	// forget — so the answer is given here instead.
	ModuleEnabled bool `json:"-"`
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

// ModuleStats is what a module reports about itself with its heartbeat.
//
// Every field is optional and absent means "not reported". The distinction
// matters: a module inside a container is often not allowed to read the node's
// memory, and a zero there would be a claim — this disk is empty — that is not
// only untrue but reassuring in exactly the wrong direction.
type ModuleStats struct {
	At time.Time `json:"at"`

	// The module's own storage.
	StorageTotalBytes *int64 `json:"storage_total_bytes,omitempty"`
	StorageUsedBytes  *int64 `json:"storage_used_bytes,omitempty"`

	// The module process.
	ProcessCPUPercent  *float64 `json:"process_cpu_percent,omitempty"`
	ProcessMemoryBytes *int64   `json:"process_memory_bytes,omitempty"`

	// The node the module runs on.
	HostCPUPercent       *float64 `json:"host_cpu_percent,omitempty"`
	HostMemoryTotalBytes *int64   `json:"host_memory_total_bytes,omitempty"`
	HostMemoryUsedBytes  *int64   `json:"host_memory_used_bytes,omitempty"`
	HostLoad1            *float64 `json:"host_load1,omitempty"`
	UptimeSeconds        *int64   `json:"uptime_seconds,omitempty"`

	// Extra carries the module's own facts, which the core renders as name/value
	// pairs without knowing what they mean: image counts, cache hit rates, the
	// size of a queue.
	Extra map[string]string `json:"extra,omitempty"`
}

// StorageUsedFraction is how full the module's storage is, or false when it did
// not say. A module that reports a total of zero is ignored rather than divided
// by.
func (s *ModuleStats) StorageUsedFraction() (float64, bool) {
	if s == nil || s.StorageTotalBytes == nil || s.StorageUsedBytes == nil {
		return 0, false
	}
	if *s.StorageTotalBytes <= 0 {
		return 0, false
	}
	return float64(*s.StorageUsedBytes) / float64(*s.StorageTotalBytes), true
}
