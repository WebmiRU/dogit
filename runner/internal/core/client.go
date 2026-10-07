// Package core is how this runner talks to a dogit instance.
//
// Three calls and a heartbeat, which is the whole of a module's side of the protocol. The
// split into a package of its own is not tidiness for its own sake: the alternative is one
// file where the HTTP plumbing, the reconnection logic and the job loop are all in the same
// few hundred lines, and the bug that costs a real build is always in one of the first two.
package core

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrUnauthorized is the core refusing the credential. It is separate from every other
// failure because the answer to it is different: a 500 is worth retrying, an unauthorized is
// worth registering again, and a program that treats them alike either hammers a core that
// is already unwell or gives up on a credential that is merely stale.
var ErrUnauthorized = errors.New("unauthorized")

// Client is a connection to one dogit instance.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New makes a client. The timeout is generous rather than tight on purpose: a build can hold
// a connection open for minutes while it pushes, and a client that gives up first turns a
// slow build into a lost one.
func New(base, token string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 5 * time.Minute},
	}
}

// Token is the credential this client presents, which is the module's own after registration.
func (c *Client) Token() string { return c.token }

func (c *Client) WithToken(token string) *Client {
	other := *c
	other.token = token
	return &other
}

// Registry is what a build needs to push: where to, and a credential scoped to this project
// for a couple of hours. The core mints it per job rather than per runner, which is the point
// — a runner that held one long-lived credential would hold access to every project it ever
// built, for ever, and the audit trail would say "runner" instead of "this build".
type Registry struct {
	URL         string `json:"url"`
	Image       string `json:"image"`
	Token       string `json:"token"`
	ExpiresIn   int    `json:"expires_in"`
	InternalURL string `json:"internal_url"`
}

// Job is one unit of work as the core describes it.
//
// Only the fields this runner has to act on are here. A Go struct silently ignores what it
// does not name, which means a field added to the answer later is not a compile error but a
// zero value — so anything unlisted below is something to add deliberately, with the field's
// real type in front of you.
type Job struct {
	ID            int64             `json:"id"`
	Name          string            `json:"name"`
	Stage         string            `json:"stage"`
	ProjectPath   string            `json:"project_path"`
	Script        []string          `json:"script"`
	Build         map[string]any    `json:"build"`
	Registry      *Registry         `json:"registry"`
	CloneURL      string            `json:"clone_url"`
	Variables     map[string]string `json:"variables"`
	CommitSHA     string            `json:"commit_sha"`
	ShortSHA      string            `json:"commit_short_sha"`
	RefName       string            `json:"ref_name"`
	Branch        string            `json:"branch"`
	Tag           string            `json:"tag"`
	WorkspaceKey  string            `json:"workspace_key"`
	ContainerName string            `json:"container_name"`
}

// CloneKey is the deploy key the core mints for one job: read-only, and taken away when the
// job is over. A runner cannot already hold one, because a runner is a machine the deployment
// knows nothing about.
type CloneKey struct {
	PrivateKey  string `json:"private_key"`
	Fingerprint string `json:"fingerprint"`
	ExpiresAt   string `json:"expires_at"`
}

// Claim is the core's answer to "is there anything for me".
type Claim struct {
	Job      *Job      `json:"job"`
	Waiting  *int      `json:"waiting"`
	CloneURL string    `json:"clone_url"`
	Key      *CloneKey `json:"key"`
	LogKey   string    `json:"log_key"`
}

// Register introduces this runner to an instance and returns the credential it will use from
// then on.
//
// The registration token is the instance's, not the module's, and it is not the same token:
// this is the one door that hands out module credentials, and it is why a pod that can
// register is a pod someone trusted with that door. A runner that has already registered
// does not need it again, which is why it is not kept.
func (c *Client) Register(ctx context.Context, registrationToken, kind, name, endpoint string, manifest map[string]any) (string, error) {
	var answer struct {
		Token string `json:"token"`
	}
	err := c.postAs(ctx, "/api/v1/modules/register", map[string]any{
		"kind": kind, "name": name, "endpoint": endpoint, "manifest": manifest,
	}, registrationToken, &answer)
	if err != nil {
		return "", err
	}
	if answer.Token == "" {
		return "", errors.New("the core returned no module token")
	}
	return answer.Token, nil
}

// Claim asks for work.
//
// An empty queue is an answer, not a failure, and comes back as a nil Job and a nil error.
// The alternative — reporting "nothing to do" as an error — would make every idle runner log
// a problem several times a second, which is how a real problem stops being noticed.
func (c *Client) Claim(ctx context.Context, tags []string) (Claim, error) {
	var answer Claim
	if err := c.post(ctx, "/api/v1/module/runner/claim", map[string]any{"tags": tags}, &answer); err != nil {
		return answer, err
	}
	return answer, nil
}

// Stats is what a module says about itself on its heartbeat.
//
// The typed fields are the ones the core has a chart for. Extra is the rest — a queue depth,
// a capacity, the time since work last arrived — which the core draws as name and value pairs
// without knowing what they mean. A pointer and not a zero, everywhere: a module that did not
// measure something must be able to say so, because a zero there is read as good news in
// exactly the place where it is not.
type Stats struct {
	At time.Time `json:"at"`

	StorageTotalBytes *int64 `json:"storage_total_bytes,omitempty"`
	StorageUsedBytes  *int64 `json:"storage_used_bytes,omitempty"`

	ProcessCPUPercent  *float64 `json:"process_cpu_percent,omitempty"`
	ProcessMemoryBytes *int64   `json:"process_memory_bytes,omitempty"`

	HostCPUPercent       *float64 `json:"host_cpu_percent,omitempty"`
	HostMemoryTotalBytes *int64   `json:"host_memory_total_bytes,omitempty"`
	HostMemoryUsedBytes  *int64   `json:"host_memory_used_bytes,omitempty"`
	HostLoad1            *float64 `json:"host_load1,omitempty"`
	UptimeSeconds        *int64   `json:"uptime_seconds,omitempty"`

	Extra map[string]string `json:"extra,omitempty"`
}

// Heartbeat keeps the module marked online and carries the statistics with it.
//
// They ride along rather than having an endpoint of their own: the module is already awake
// and already talking to us, and a second thing to remember to call is a second thing that
// quietly stops being called.
func (c *Client) Heartbeat(ctx context.Context, stats Stats) (time.Duration, error) {
	stats.At = time.Now().UTC()
	var answer struct {
		HeartbeatInterval string `json:"heartbeat_interval"`
		NextDeadline      string `json:"next_deadline"`
	}
	if err := c.post(ctx, "/api/v1/module/heartbeat", map[string]any{"stats": stats}, &answer); err != nil {
		return 0, err
	}
	if answer.HeartbeatInterval == "" {
		// The core says how often to come back, and it is the core that knows how many
		// missed heartbeats it tolerates. Guessing here is how a module and a core end up
		// disagreeing about what "online" means.
		return 30 * time.Second, nil
	}
	interval, err := time.ParseDuration(answer.HeartbeatInterval)
	if err != nil {
		return 30 * time.Second, nil
	}
	return interval, nil
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.postAs(ctx, path, body, c.token, out)
}

func (c *Client) postAs(ctx context.Context, path string, body any, token string, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	defer response.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read answer from %s: %w", path, err)
	}

	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return ErrUnauthorized
	}
	if response.StatusCode >= 300 {
		// The core's own wording, kept. "an unexpected error occurred" is what a 500 says
		// when nothing better was available, and a runner that logs that has learned nothing
		// and will go on logging it.
		return fmt.Errorf("%s: %s: %s", path, response.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode answer from %s: %w", path, err)
	}
	return nil
}
