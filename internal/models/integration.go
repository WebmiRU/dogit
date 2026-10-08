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
	// What a caller may ask a deploy module to do. Reading is what a project's page
	// does when it shows what is deployed; writing is what pressing undo does, and it
	// is separate so that the first can be given to somebody who may only look.
	ScopeDeployRead  = "deploy:read"
	ScopeDeployWrite = "deploy:write"
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
	// Database used to ask the core to provision one. It no longer does anything, and it is
	// kept for a reason that is not nostalgia.
	//
	// Manifests are read with unknown fields refused, so removing this would not quietly stop
	// honouring it — it would refuse the registration of every module built against a core that
	// had it, with a message about a JSON field rather than about the module. A module nobody
	// has rebuilt since a version upgrade is exactly the one that cannot fix itself, and it
	// would be locked out of the instance by a rename.
	//
	// Parsed and ignored. Nothing reads it, nothing writes it, and a module that keeps sending
	// it is no worse off than one that stops.
	//
	// The deprecation lives in a field rather than in a warning nobody reads, because the cost
	// of getting this wrong is a module that will not start.
	Database bool `json:"database,omitempty"`

	// DependsOn lists module kinds this module needs.
	DependsOn []string `json:"depends_on,omitempty"`
	// Description is free text for the module list.
	Description string `json:"description,omitempty"`
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
	// Target is what one of this module's rows is made of, and it is a pointer on
	// purpose: omitempty does nothing for a struct, so a module that has no rows was
	// answered with an empty object and every page decided it had rows to show. A
	// module that says nothing about rows must produce no key at all.
	Target *TargetSpec `json:"target,omitempty"`
}

// TargetSpec describes a recipient, in the module's own terms.
//
// The core stores rows of these settings, inherits them down the instance-group-
// project hierarchy and shows them in a list, and knows nothing about what any of
// them mean. What it needs from the module is which settings name a destination —
// so a list can say "-100…a chat" rather than "row 1" — and a word for what such a
// row is.
type TargetSpec struct {
	// Settings are the keys a row is made of, and it matters that a module says so: a
	// Telegram bot's token is the module's, not a destination's, and a repository must
	// never be asked for it. Left empty, every declared setting is taken to make one,
	// which is what a module with nothing but addresses wants.
	Settings []string `json:"settings,omitempty"`
	// Fields are what those keys look like when a person fills them in — their names,
	// their kinds, and what each is for.
	//
	// Separate from the settings list on purpose, and this is the part that is easy to
	// get wrong: a row's values are not settings of the module, so their descriptions
	// do not belong to the module's settings either. A module whose rows are the
	// clusters it reaches has no "kubeconfig" setting at all — it has a kubeconfig on a
	// row — and describing that row without saying what the fields are leaves a form
	// with one text box and no idea what the others were called.
	Fields []SettingSpec `json:"fields,omitempty"`
	// Identify lists the settings that name a recipient, best first: a chat id, an
	// address. They are shown as the row's name when nobody gave it one.
	Identify []string `json:"identify,omitempty"`
	// Title and Description are what the settings page calls this, in the module's
	// own words — "Which chats", "SMTP servers".
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Flags are the switches each row has besides the one every row has, by the name
	// the core stores them under.
	//
	// A row is already switchable as a whole — enabled says whether this place may be
	// acted on at all, and that is a question every kind of module asks. What is here
	// is for the questions only one kind asks: a deployment module wants to know
	// whether a push may come here by itself, which is a different fact about a
	// different place and means nothing for a chat. The module says which it wants and
	// the core draws a switch per flag, so a module gaining one does not mean teaching
	// the core what autodeploy is.
	Flags []TargetFlag `json:"flags,omitempty"`
}

// TargetFlag is one switch a module's rows have.
type TargetFlag struct {
	// Key is what the core stores the decision under.
	Key string `json:"key"`
	// Label is what the switch is called on the page.
	Label string `json:"label"`
	// Description says what the switch means, in the module's own words, and is worth
	// writing: a switch whose consequence is not obvious is a switch somebody reads
	// the page in order to understand.
	Description string `json:"description,omitempty"`
	// Default is where it stands when nobody has said otherwise, and true unless the
	// module says otherwise: a row that is there and was not switched off should be in
	// use.
	Default bool `json:"default"`
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
	Key   string `json:"key"`
	Label string `json:"label"`
	// Type is string | text | bool | int | enum | url | list.
	//
	// "text" is a string that is shown as a multi-line box. It exists because a
	// kubeconfig is a document: pasting one into a single-line input trims it, and the
	// result is a credential that parses as nothing at all.
	Type        string   `json:"type"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Description string   `json:"description,omitempty"`
	// Secret marks a value that is write-only: it is stored but never returned.
	Secret bool `json:"secret,omitempty"`

	// Required says the module cannot work without this one, and the core refuses to let it
	// register until it has a value.
	//
	// It exists because the alternative is a module that runs and quietly does less. A module
	// with no database still deploys, still answers, and records nothing — which from the
	// outside is exactly a module that has never deployed anything, and there is nothing on any
	// page that says otherwise. Refusing at registration puts the reason where the
	// administrator installing it is looking, and leaves a module that is either working or
	// absent rather than one that is silently broken.
	//
	// Optional by default, and that is the safe direction: a module author who marks
	// everything required has built something that cannot be installed and half-configured,
	// and that shows up on the page rather than at a deploy.
	Required bool `json:"required,omitempty"`

	// Inheritable marks a value that may be sent to the levels below the one that wrote it.
	//
	// False by default, and the default is the safe one: a value stays where it was written
	// unless the module says otherwise. A page below is a place fewer people may look — a
	// project's settings are readable by anybody who can read the project — so what arrives
	// there is what the module has deliberately published downward, and a credential is
	// something a module keeps to itself.
	//
	// The alternative, sending everything above and marking the credentials, is one
	// forgotten mark away from handing a cluster's keys to every project that inherits a
	// row of clusters. A module author should not have to know that a field called
	// kubeconfig is a credential for the rule to hold.
	Inheritable bool `json:"inheritable,omitempty"`

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

	// Identify names the fields that say which entry this is — a cluster's name, a
	// recipient's chat id. Naming them is what makes a list inherit entry by entry: a
	// project that changes one cluster's namespace has not stopped using the others,
	// and without a name to match on there is no telling which entry the smaller list
	// was talking about.
	//
	// A list that names none is replaced whole by any scope below it, because merging
	// it would mean guessing.
	Identify []string `json:"identify,omitempty"`
}

// Integration is a registered module.
type Integration struct {
	ID            uuid.UUID `json:"id"`
	Kind          string    `json:"kind"`
	Name          string    `json:"name"`
	Endpoint      string    `json:"endpoint"`
	ModuleVersion string    `json:"module_version"`
	Capabilities  Manifest  `json:"capabilities"`
	Status        string    `json:"status"`
	Enabled       bool      `json:"enabled"`

	RegisteredAt time.Time      `json:"registered_at"`
	LastSeenAt   *time.Time     `json:"last_seen_at"`
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
