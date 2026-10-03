package models

import (
	"strings"

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

	// Uninstall describes what this module needs to be told before it removes
	// itself. The core renders these and passes the chosen keys back, and knows
	// nothing about what any of them mean: a registry offers to delete its
	// images, a build cache offers to delete its layers, and the core is not in
	// the business of knowing either.
	Uninstall UninstallSpec `json:"uninstall,omitempty"`

	// Routing says what address the module needs to be reachable at.
	Routing RoutingSpec `json:"routing,omitempty"`

	// Capacity is how much this machine will take at once. A runner declares it so
	// that a page can say "one of four slots is free" without the core guessing at
	// it, and zero means the module did not say — which is not the same as none.
	Capacity int `json:"capacity,omitempty"`

	// Target says this module can be pointed at more than one place, and which of
	// its settings make one. A notification module is asked to send to two chats or
	// to a chat and a mailbox, and one flat set of settings cannot say that; a set
	// per destination can.
	Target TargetSpec `json:"target,omitempty"`
}

// TargetSpec describes a recipient, in the module's own terms.
//
// The core stores rows of these settings, inherits them down the instance-group-
// project hierarchy and shows them in a list, and knows nothing about what any of
// them mean. What it needs from the module is which settings name a destination —
// so a list can say "-100…a chat" rather than "row 1" — and a word for what such a
// row is.
type TargetSpec struct {
	// Settings are the keys a recipient is made of, and it matters that a module
	// says so: a Telegram bot's token is the module's, not a destination's, and a
	// repository must never be asked for it. Left empty, every declared setting is
	// taken to make one, which is what a module with nothing but addresses wants.
	Settings []string `json:"settings,omitempty"`
	// Identify lists the settings that name a recipient, best first: a chat id, an
	// address. They are shown as the row's name when nobody gave it one.
	Identify []string `json:"identify,omitempty"`
	// Title and Description are what the settings page calls this, in the module's
	// own words — "Which chats", "SMTP servers".
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
}

// RoutingSpec is a module's request to be served on an address of its own.
//
// A module that speaks a protocol the browser does not — a registry, a package
// feed — cannot live under a path on the main site: the client treats everything
// before the first slash as a hostname and nothing after it as a path. So it gets
// its own name on the same ports, and the module says what that name should look
// like rather than being handed a fixed one.
type RoutingSpec struct {
	// Domains are the names to serve this module on. The literal {host} is replaced
	// with the instance's public host, which is what makes "registry.{host}" mean
	// registry on this instance and not on some other one.
	Domains []string `json:"domains,omitempty"`

	// Path is a prefix under those domains, for a module that wants one. Empty means
	// the whole name belongs to the module.
	Path string `json:"path,omitempty"`

	// Websocket asks the proxy to pass connection upgrades through, which a module
	// serving a live view needs and one serving a registry must not.
	Websocket bool `json:"websocket,omitempty"`
}

// PublicDomains expands a routing spec into the names it actually means on this
// instance.
//
// A template naming nothing known is passed through untouched rather than turned
// into a broken name: an administrator who wrote a literal host meant it.
func (r RoutingSpec) PublicDomains(publicHost string) []string {
	if len(r.Domains) == 0 {
		return nil
	}

	domains := make([]string, 0, len(r.Domains))
	for _, domain := range r.Domains {
		if publicHost != "" && strings.Contains(domain, "{host}") {
			domains = append(domains, strings.ReplaceAll(domain, "{host}", publicHost))
			continue
		}
		domains = append(domains, domain)
	}
	return domains
}

// UninstallSpec is a module's own description of its removal.
type UninstallSpec struct {
	// Options are the switches the administrator is offered.
	Options []UninstallOption `json:"options,omitempty"`
}

// UninstallOption is one switch in the removal dialog.
type UninstallOption struct {
	Key string `json:"key"`
	// Label and Description are the module's own words, so an operator reading
	// them learns what this module will delete rather than what the core guesses.
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
	// Dangerous marks an option whose consequence cannot be undone. The interface
	// says so, and asks for a second confirmation.
	Dangerous bool `json:"dangerous,omitempty"`
	// Required makes removal impossible without this option: a module that has
	// stored something an administrator would lose data by leaving behind says
	// so here rather than letting a careless deletion look tidy.
	Required bool `json:"required,omitempty"`
}

// RegistryAccess is the core's answer to a module asking about a caller.
//
// It is deliberately blunt: allowed, and if not, why. A module has to be able to
// log the reason without interpreting prose.
type RegistryAccess struct {
	Allowed bool   `json:"allowed"`
	Action  string `json:"action,omitempty"`
	// Reason explains a refusal in the module's terms, so it can be logged and
	// shown without the core knowing which module asked.
	Reason string `json:"reason,omitempty"`

	// Level and Minimum make a refusal explainable: "you are a guest, this needs a
	// developer" is something an administrator can act on.
	Level    int        `json:"level,omitempty"`
	Minimum  int        `json:"minimum,omitempty"`
	Username string     `json:"username,omitempty"`
	UserID   *uuid.UUID `json:"user_id,omitempty"`

	// Project is echoed back when the answer is yes, so the module does not have
	// to ask a second time to learn which project the path meant.
	Project map[string]any `json:"project,omitempty"`
}

// SettingSpec is one configurable setting of a module.
type SettingSpec struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Type        string   `json:"type"` // string | bool | int | enum | url | list
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Description string   `json:"description,omitempty"`
	// Secret marks a value that is write-only: it is stored but never returned.
	Secret bool `json:"secret,omitempty"`

	// Items describes the fields of one entry when the type is "list".
	//
	// It exists because a list is the only setting whose value has fields of its own,
	// and those fields have to be describable for two reasons: the form renders from
	// the description rather than guessing, and a secret field inside an entry has to
	// be markable so it can be masked on the way out. A password inside a list stored
	// as one piece of JSON cannot be masked at all — the blob is either returned whole
	// or not at all — which is why this is a type and not a convention.
	Items *SettingItems `json:"items,omitempty"`

	// MustContain lists substrings every value of this setting must include, and
	// WhichAreThen explains why in the module's own words.
	//
	// It exists so the core can enforce a rule without knowing what the rule is
	// about: a registry's image name must contain its project's name, and the core
	// has no business knowing that a registry exists. The module states the
	// requirement and the reason; the core checks it on write, where refusing is
	// cheap, rather than at push time, where refusing is an outage.
	MustContain []string `json:"must_contain,omitempty"`
	// WhyContains is shown when a value is refused.
	WhyContains string `json:"why_contains,omitempty"`
}

// SettingItems is what one entry of a list setting is made of.
type SettingItems struct {
	// Fields describe each entry. Their own keys are the names used in the stored
	// value, and a field this build does not know is refused rather than dropped.
	Fields []SettingSpec `json:"fields"`
	// AddLabel is what the button to add another entry says. In the module's words,
	// because "add a source" and "add a mirror" are not the same button.
	AddLabel string `json:"add_label,omitempty"`
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

// HasScope says whether a credential carries a permission.
//
// The empty scope is not "everything": a credential that carries nothing carries
// nothing, and reading it as a wildcard would make every old token as powerful as
// a new one, which is the wrong way round.
func (i Introspection) HasScope(scope string) bool {
	for _, have := range i.Scopes {
		if have == scope {
			return true
		}
	}
	return false
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
