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
	URL   string `json:"url"`
	Image string `json:"image"`
	Token string `json:"token"`
	// Username and Password are the other shape a registry answers to, and the one a
	// written-down address uses: a registry module mints a token scoped to a project,
	// while an address an administrator wrote down is pushed to with an account that
	// this instance already holds. Token is preferred where both are given, because a
	// scoped token is the narrower of the two and there is no reason to prefer the
	// wider credential when the narrower one is on offer.
	Username    string `json:"login"`
	Password    string `json:"password"`
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
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Stage       string         `json:"stage"`
	ProjectPath string         `json:"project_path"`
	Script      []string       `json:"script"`
	Build       map[string]any `json:"build"`
	// Where the image goes is deliberately NOT here.
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
	Job *Job `json:"job"`
	// Waiting is how many jobs were still queued at the core after this one was taken. A
	// pointer because the core says "not known" by leaving it out, and a runner that cannot
	// count the queue must say so rather than report a confident zero.
	Waiting *int `json:"waiting"`
	// Registry is the push address and the credential for it, and it is here rather than on
	// the job because the core mints it per build — it lives for two hours and is scoped to
	// one project. Naming it on Job instead would have compiled, unmarshalled to nil, and read
	// as "this job builds no image", which is a lie the runner then reports to a person. That
	// is exactly what happened: the field was on Job, decoded to nil every time, and the
	// runner said "no registry to push to" while the core was handing one over.
	Registry *Registry `json:"registry,omitempty"`
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

// JobKey is the credential to clone one job with, and where to clone from.
//
// Asked at the moment the clone is about to happen rather than when the job was claimed. A
// runner may hold a job for minutes before it begins — a slot was busy, or a build ahead of
// it took a while — and a key whose short life was measured from the claim would be dead
// before the machine was ready. It is the one credential in this program that is read-only
// and scoped to one project.
func (c *Client) JobKey(ctx context.Context, jobID int64) (cloneURL, privateKey, fingerprint string, err error) {
	var answer struct {
		CloneURL string `json:"clone_url"`
		Key      struct {
			PrivateKey  string `json:"private_key"`
			Fingerprint string `json:"fingerprint"`
		} `json:"key"`
	}
	if err := c.post(ctx, jobPath(jobID, "key"), map[string]any{}, &answer); err != nil {
		return "", "", "", err
	}
	return answer.CloneURL, answer.Key.PrivateKey, answer.Key.Fingerprint, nil
}

// JobLog appends part of a job's output, tagged with which stream it came from.
//
// Sent as it arrives rather than in one piece at the end, because the log is the only thing
// that says what a build is doing while it is doing it. Sent too often it is its own problem,
// so callers batch.
func (c *Client) JobLog(ctx context.Context, jobID int64, stream, text string) error {
	if text == "" {
		return nil
	}
	return c.post(ctx, jobPath(jobID, "log"), map[string]string{"stream": stream, "text": text}, nil)
}

// JobProgress says which part of the work the runner is on.
//
// Best effort by design. A progress message that fails to arrive costs a page a step it would
// have shown a moment later; failing the job over it would let the core's event feed break a
// build, which is the wrong way round.
func (c *Client) JobProgress(ctx context.Context, jobID int64, phase, message string) error {
	if phase == "" || message == "" {
		return nil
	}
	return c.post(ctx, jobPath(jobID, "progress"),
		map[string]string{"phase": phase, "message": message}, nil)
}

// ReportBuildArtifact records the exact reference and immutable digest of an image whose
// push-enabled BuildKit export completed successfully.
func (c *Client) ReportBuildArtifact(ctx context.Context, jobID int64, image, digest string) error {
	return c.post(ctx, jobPath(jobID, "artifact"), map[string]string{
		"image": image,
		"digest": digest,
	}, nil)
}

// FinishJob says how a job ended, for good.
//
// Status is one of the core's own words — success, failed, canceled — and anything else is
// refused there rather than here, so that the two ends of this cannot drift apart quietly.
func (c *Client) FinishJob(ctx context.Context, jobID int64, status string, took time.Duration, reason string) error {
	return c.post(ctx, jobPath(jobID, "finish"), map[string]any{
		"status":      status,
		"duration_ms": took.Milliseconds(),
		"error":       reason,
	}, nil)
}

// Settings is this module's own configuration, as the core holds it.
//
// Read, and acted on. The runner in dogit declares three settings and reads none of them, so
// an administrator changes a number in a panel and nothing happens — which is worse than not
// offering the number, because a panel is read as a statement about the machine. The schema
// comes back with the values so that a setting this build has never heard of is reported
// rather than silently ignored.
func (c *Client) Settings(ctx context.Context) (effective map[string]any, schema []map[string]any, err error) {
	var answer struct {
		Effective map[string]any   `json:"effective"`
		Schema    []map[string]any `json:"schema"`
	}
	// A GET, and not a POST with an empty body: the core's router answers a GET here and
	// refuses a POST with a 405, which arrived as a settings read failing three times a
	// minute until it was looked at.
	if err := c.get(ctx, "/api/v1/module/settings", &answer); err != nil {
		return nil, nil, err
	}
	return answer.Effective, answer.Schema, nil
}

func jobPath(jobID int64, leaf string) string {
	return fmt.Sprintf("/api/v1/module/runner/jobs/%d/%s", jobID, leaf)
}

func (c *Client) post(ctx context.Context, path string, body any, out any) error {
	return c.postAs(ctx, path, body, c.token, out)
}

// get is a POST-shaped call that is not one. The core's module routes are mostly POSTs
// because they are commands, but a few are questions, and the router answers those with a
// 405 for a POST — so the verb is a property of the endpoint and not of this client.
func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, c.token, out)
}

func (c *Client) postAs(ctx context.Context, path string, body any, token string, out any) error {
	return c.do(ctx, http.MethodPost, path, body, token, out)
}

func (c *Client) do(ctx context.Context, method, path string, body any, token string, out any) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s: %w", path, err)
		}
		reader = bytes.NewReader(payload)
	}

	request, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return fmt.Errorf("build request for %s: %w", path, err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
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
