package main

import (
	"errors"

	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/deploy"
	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// What this module remembers, in its own database.
//
// The core does not store this and does not know what it is. A deployment history is
// the module's own business, and the reason it lives here rather than in the core is
// that a second copy of "what is deployed" is how two things start to disagree.

// staleDeploymentAge is how long a deployment may claim a place before it is assumed
// abandoned. Well beyond any rollout, and well beyond the core's own hour-long limit.
const staleDeploymentAge = 2 * time.Hour

// History is this module's record of deployments, which is the contract deploy states.
type History interface {
	deploy.History
	Close(ctx context.Context)
}

// openHistory opens the module's own database and makes it ready to be used.
//
// The schema is created here rather than migrated by hand: a module that has just been
// installed has an empty database and nobody has run anything against it, and a
// deployment tool that will not start until an administrator has run its migrations is
// a tool nobody installs twice.
func openHistory(ctx context.Context, url string) (History, error) {
	if url == "" {
		return nil, fmt.Errorf("no database URL was handed to this module")
	}

	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to the module's database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("reach the module's database: %w", err)
	}

	history := &postgresHistory{pool: pool}
	if err := history.migrate(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return history, nil
}

type postgresHistory struct {
	pool *pgxpool.Pool
}

// migrate creates what this module needs.
//
// Two tables and no migration tool, because there is exactly one version of this
// schema: there is nothing yet to migrate from.
func (h *postgresHistory) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS deployments (
			id          UUID PRIMARY KEY,
			project     TEXT NOT NULL,
			cluster     TEXT NOT NULL,
			namespace   TEXT NOT NULL,
			image       TEXT NOT NULL DEFAULT '',
			workload    TEXT NOT NULL DEFAULT '',
			state       TEXT NOT NULL,
			phase       TEXT NOT NULL DEFAULT '',
			reason      TEXT NOT NULL DEFAULT '',
			started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
			finished_at TIMESTAMPTZ,
			pods_wanted  INT NOT NULL DEFAULT 0,
			pods_ready   INT NOT NULL DEFAULT 0,
			pods_retired INT NOT NULL DEFAULT 0,
			log          JSONB NOT NULL DEFAULT '[]'::jsonb,
			tags         JSONB NOT NULL DEFAULT '[]'::jsonb,
			commit       TEXT NOT NULL DEFAULT '',
			place        TEXT NOT NULL DEFAULT ''
		)`,
		// Added to a table that already exists as well as described in the CREATE
		// above. A module is upgraded by being restarted, and an installation that
		// had records before this version would otherwise start failing to read its
		// own history — which is the one thing it cannot recover from by itself.
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS pods_wanted  INT NOT NULL DEFAULT 0`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS pods_ready   INT NOT NULL DEFAULT 0`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS pods_retired INT NOT NULL DEFAULT 0`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS log JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS tags JSONB NOT NULL DEFAULT '[]'::jsonb`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS commit TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE deployments ADD COLUMN IF NOT EXISTS place TEXT NOT NULL DEFAULT ''`,
		// One deployment at a time per place. The index is the lock: a second one
		// cannot be begun while this exists, so the rule holds even if two tasks reach
		// this module at the same moment and even if this module is running twice.
		`CREATE UNIQUE INDEX IF NOT EXISTS deployments_running_idx
			ON deployments (project, cluster, namespace)
			WHERE state = 'running'`,
		`CREATE INDEX IF NOT EXISTS deployments_history_idx
			ON deployments (project, cluster, namespace, started_at DESC)`,
	}

	for _, statement := range statements {
		if _, err := h.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("prepare the module's database: %w", err)
		}
	}
	return nil
}

// Begin files a deployment, and refuses when one is already under way.
func (h *postgresHistory) Begin(ctx context.Context, d deploy.Deployment) (deploy.Deployment, error) {
	if d.ID == uuid.Nil {
		d.ID = uuid.New()
	}

	// A deployment that was begun and never finished — this module was killed
	// mid-rollout, or the machine it was on went away — would hold this place for
	// ever. One that started long enough ago is not in progress, it is abandoned: its
	// module is not coming back, and every later deployment to the same place is
	// refused by a row nobody will ever close.
	//
	// Long enough is generous. A migration that legitimately takes this long is
	// unheard of, and being wrong in this direction costs one extra concurrent
	// rollout rather than a namespace that can never be deployed to again.
	if _, err := h.pool.Exec(ctx, `
		UPDATE deployments SET state = 'abandoned', finished_at = now(),
			reason = 'the module stopped reporting this deployment'
		WHERE project = $1 AND cluster = $2 AND namespace = $3 AND state = 'running'
		  AND started_at < now() - $4::interval`,
		d.Project, d.Cluster, d.Namespace, staleDeploymentAge); err != nil {
		return d, fmt.Errorf("clear an abandoned deployment: %w", err)
	}

	tags, err := json.Marshal(d.Tags)
	if err != nil {
		return d, err
	}
	if d.Tags == nil {
		tags = []byte("[]")
	}

	if _, err := h.pool.Exec(ctx, `
		INSERT INTO deployments (id, project, cluster, namespace, image, workload, state,
		                         started_at, tags, commit, place)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', $7, $8, $9, $10)`,
		d.ID, d.Project, d.Cluster, d.Namespace, d.Image, d.Workload, d.StartedAt, tags,
		d.Commit, d.Place); err != nil {
		// The partial unique index refused this one, which is the rule rather than a
		// failure: two rollouts in one place means two migrations against one database.
		if running, busy := h.running(ctx, d); busy {
			return d, deploy.ErrBusy{Namespace: d.Namespace, Running: running}
		}
		return d, fmt.Errorf("record the deployment: %w", err)
	}
	return d, nil
}

// running is the deployment holding a place, if any.
func (h *postgresHistory) running(ctx context.Context, d deploy.Deployment) (string, bool) {
	var image string
	err := h.pool.QueryRow(ctx, `
		SELECT image FROM deployments
		WHERE project = $1 AND cluster = $2 AND namespace = $3 AND state = 'running'
		LIMIT 1`, d.Project, d.Cluster, d.Namespace).Scan(&image)
	if err != nil {
		return "", false
	}
	if image == "" {
		return "another deployment", true
	}
	return "another deployment of " + image, true
}

func (h *postgresHistory) Phase(ctx context.Context, id uuid.UUID, state deploy.State,
	phase deploy.Phase, reason string) error {

	_, err := h.pool.Exec(ctx,
		`UPDATE deployments SET phase = $2, reason = $3 WHERE id = $1`, id, phase, reason)
	return err
}

func (h *postgresHistory) Finish(ctx context.Context, id uuid.UUID, state deploy.State,
	reason string) error {

	_, err := h.pool.Exec(ctx, `
		UPDATE deployments
		SET state = $2, reason = $3, finished_at = now()
		WHERE id = $1`, id, state, reason)
	return err
}

// Images reads the catalogue, a page of it.
func (h *postgresHistory) Images(ctx context.Context, project, cluster, namespace string,
	limit, offset int) ([]deploy.KnownImage, int, error) {

	var total int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(DISTINCT image) FROM deployments
		WHERE project = $1 AND image <> ''
		  AND ($2 = '' OR cluster = $2) AND ($3 = '' OR namespace = $3)`,
		project, cluster, namespace).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the images: %w", err)
	}

	// The last deployment of each image comes along with it, chosen by asking the
	// table once per row rather than by a second query per image: a catalogue page of
	// twenty rows should cost twenty rows, not twenty round trips.
	rows, err := h.pool.Query(ctx, `
		SELECT grouped.image, grouped.first_seen, grouped.times, grouped.succeeded,
		       grouped.tags,
		       last.image, last.id, last.cluster, last.namespace, last.workload,
		       last.state, last.started_at
		FROM (
			SELECT image, min(started_at) AS first_seen, count(*) AS times,
			       count(*) FILTER (WHERE state = 'succeeded') AS succeeded,
			       -- Every name this image was ever deployed under, deduplicated in
			       -- the query: a dozen deployments of one image carry a dozen copies
			       -- of the same tag, and the list is shown to a person.
			       COALESCE((
			         SELECT jsonb_agg(DISTINCT tag)
			         FROM deployments d, LATERAL jsonb_array_elements_text(
			           COALESCE(d.tags, '[]'::jsonb)) AS tag
			         WHERE d.image = deployments.image AND d.project = $1
			       ), '[]'::jsonb) AS tags
			FROM deployments
			WHERE project = $1 AND image <> ''
			  AND ($2 = '' OR cluster = $2) AND ($3 = '' OR namespace = $3)
			GROUP BY image
			ORDER BY min(started_at) DESC
			LIMIT $4 OFFSET $5
		) AS grouped
		LEFT JOIN LATERAL (
			-- In the same place as the row it hangs off, which the outer query has
			-- already chosen and this one has to keep to: an image deployed to both
			-- places is one row in each place's catalogue, and each row's "where it is"
			-- has to be this place. Read without it, both places answer with whichever
			-- was deployed most recently, and one place's page offers to put an image
			-- back on the other place's workload — for an image that was never there.
			SELECT image, id, cluster, namespace, workload, state, started_at
			FROM deployments
			WHERE project = $1 AND image = grouped.image AND state = 'succeeded'
			  AND ($2 = '' OR cluster = $2) AND ($3 = '' OR namespace = $3)
			ORDER BY started_at DESC LIMIT 1
		) AS last ON true
		ORDER BY grouped.first_seen DESC`,
		project, cluster, namespace, limit+1, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("read the images: %w", err)
	}
	defer rows.Close()

	out := []deploy.KnownImage{}
	for rows.Next() {
		var one deploy.KnownImage
		var lastImage, lastID, lastCluster, lastNamespace, lastWorkload *string
		var lastState *deploy.State
		var lastAt *time.Time
		var tags []byte
		if err := rows.Scan(&one.Image, &one.FirstSeen, &one.Times, &one.Succeeded, &tags,
			&lastImage, &lastID, &lastCluster, &lastNamespace, &lastWorkload, &lastState,
			&lastAt); err != nil {
			return nil, 0, err
		}
		if len(tags) > 0 {
			_ = json.Unmarshal(tags, &one.Tags)
		}
		if lastID != nil {
			one.Deployed = &deploy.ImageDeployment{
				Image: *lastImage, ID: *lastID, Cluster: *lastCluster,
				Namespace: *lastNamespace, Workload: *lastWorkload,
				State: *lastState, StartedAt: *lastAt,
			}
		}
		out = append(out, one)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

// LogOf reads back what a deployment said.
func (h *postgresHistory) LogOf(ctx context.Context, id uuid.UUID) ([]deploy.LogLine, error) {
	var encoded []byte
	err := h.pool.QueryRow(ctx, `SELECT log FROM deployments WHERE id = $1`, id).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 {
		return nil, nil
	}
	var lines []deploy.LogLine
	if err := json.Unmarshal(encoded, &lines); err != nil {
		return nil, err
	}
	return lines, nil
}

// Log writes down what a deployment said.
func (h *postgresHistory) Log(ctx context.Context, id uuid.UUID, lines []deploy.LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	encoded, err := json.Marshal(lines)
	if err != nil {
		return err
	}
	_, err = h.pool.Exec(ctx, `UPDATE deployments SET log = $2 WHERE id = $1`, id, encoded)
	return err
}

// Counts writes down what a rollout did to the pods.
//
// Its own statement rather than part of Finish: the counts are known while the
// rollout runs, and a deployment that fails halfway has still rolled some of them
// out. Waiting for the end to write them down would lose exactly the records worth
// having.
func (h *postgresHistory) Counts(ctx context.Context, id uuid.UUID, wanted, ready, retired int) error {

	_, err := h.pool.Exec(ctx, `
		UPDATE deployments
		SET pods_wanted = $2, pods_ready = $3, pods_retired = $4
		WHERE id = $1`, id, wanted, ready, retired)
	return err
}

func (h *postgresHistory) Current(ctx context.Context, project, cluster, namespace string) (*deploy.Deployment, error) {
	rows, err := h.pool.Query(ctx, deploymentColumns+`
		WHERE project = $1 AND cluster = $2 AND namespace = $3
		ORDER BY started_at DESC LIMIT 1`, project, cluster, namespace)
	if err != nil {
		return nil, fmt.Errorf("read the last deployment: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return nil, nil
	}
	record, err := scanDeployment(rows)
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// TagsOf is every name an image has been published under in one place.
//
// From the whole history, because the newest record of an image is not the one that
// knows its name: a rollback's record says which image went back and nothing else, and
// it is the newest thing in the table. Read from there, the one image whose name matters
// most — the one that was just put back — is the one that comes back nameless.
func (h *postgresHistory) TagsOf(ctx context.Context, project, cluster, namespace,
	image string) ([]string, error) {

	if strings.TrimSpace(image) == "" {
		return nil, nil
	}
	var encoded []byte
	err := h.pool.QueryRow(ctx, `
		SELECT COALESCE((
			SELECT jsonb_agg(DISTINCT tag)
			FROM deployments d, LATERAL jsonb_array_elements_text(
			  COALESCE(d.tags, '[]'::jsonb)) AS tag
			WHERE d.project = $1 AND d.image = $4
			  AND ($2 = '' OR d.cluster = $2) AND ($3 = '' OR d.namespace = $3)
		), '[]'::jsonb)`, project, cluster, namespace, image).Scan(&encoded)
	if err != nil {
		return nil, fmt.Errorf("read the names of an image: %w", err)
	}
	var tags []string
	if len(encoded) > 0 {
		_ = json.Unmarshal(encoded, &tags)
	}
	return tags, nil
}

// An empty cluster or namespace means "any": the question a project page asks is what
// it has deployed, not what it deployed to one place it already knows the name of.
func (h *postgresHistory) List(ctx context.Context, project, cluster, namespace string,
	limit, offset int) ([]deploy.Deployment, int, error) {

	// One page past what was asked for, to answer "is there more" without a second
	// query, and a count because a control that cannot say how many pages there are
	// leaves somebody guessing.
	var total int
	if err := h.pool.QueryRow(ctx, `
		SELECT count(*) FROM deployments
		WHERE project = $1 AND ($2 = '' OR cluster = $2) AND ($3 = '' OR namespace = $3)`,
		project, cluster, namespace).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count the deployment history: %w", err)
	}

	rows, err := h.pool.Query(ctx, deploymentColumns+`
		WHERE project = $1
		  AND ($2 = '' OR cluster = $2)
		  AND ($3 = '' OR namespace = $3)
		ORDER BY started_at DESC
		LIMIT $4 OFFSET $5`, project, cluster, namespace, limit+1, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("read the deployment history: %w", err)
	}
	defer rows.Close()

	out := []deploy.Deployment{}
	for rows.Next() {
		record, err := scanDeployment(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (h *postgresHistory) Close(_ context.Context) {
	h.pool.Close()
}

// The columns of a deployment, in the order scanDeployment reads them.
//
// Written once and used by every query that reads a whole record, because a list
// written out again is a list that can fall behind: one of these was six columns short
// for as long as there had been two places to deploy to, and the only reader of it was
// the one that puts a version back. Every revert failed, in two milliseconds, and the
// failure was answered as a success — which is what a record nobody can read looks like
// from the outside.
const deploymentColumns = `
	SELECT id, project, cluster, namespace, image, workload, state, phase, reason,
	       started_at, finished_at, pods_wanted, pods_ready, pods_retired, tags, commit, place
	FROM deployments`

type scanner interface {
	Scan(dest ...any) error
}

func scanDeployment(rows scanner) (deploy.Deployment, error) {
	var record deploy.Deployment
	var tags []byte
	if err := rows.Scan(&record.ID, &record.Project, &record.Cluster, &record.Namespace,
		&record.Image, &record.Workload, &record.State, &record.Phase, &record.Reason,
		&record.StartedAt, &record.FinishedAt,
		&record.PodsWanted, &record.PodsReady, &record.PodsRetired, &tags,
		&record.Commit, &record.Place); err != nil {
		return deploy.Deployment{}, fmt.Errorf("read a deployment: %w", err)
	}
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &record.Tags)
	}
	record.FromOurRegistry = strings.HasSuffix(record.Image, "@sha256:") ||
		strings.Contains(record.Image, "@sha256:")
	return record, nil
}

// clustersOf reads the clusters this module may deploy to, from its own settings.
//
// The list arrives as one value, which is the shape the type says it has: a set of
// values that belong together, each of which may or may not be one the rest of the
// system needs to understand.
func clustersOf(settings map[string]any) ([]Cluster, error) {
	all, err := allClusters(settings)
	if err != nil {
		return nil, err
	}

	// A cluster that is switched off is not one this project may deploy to, and is
	// dropped here rather than refused at every call site: the same list is read to
	// list what is available, to find one by name and to validate a job.
	kept := all[:0]
	for _, one := range all {
		if !one.use() {
			continue
		}
		kept = append(kept, one)
	}
	return kept, nil
}

// allClusters is every cluster in the settings, switched off or not.
func allClusters(settings map[string]any) ([]Cluster, error) {
	raw, ok := settings["clusters"]
	if !ok {
		return nil, nil
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("read the cluster list: %w", err)
	}

	var clusters []Cluster
	if err := json.Unmarshal(encoded, &clusters); err != nil {
		return nil, fmt.Errorf("read the cluster list: %w", err)
	}

	// Only a cluster with a name is a cluster. One without is a row somebody started
	// filling in, and using it would mean applying a deployment to a place with no
	// address.
	named := clusters[:0]
	for _, one := range clusters {
		if strings.TrimSpace(one.Name) == "" {
			continue
		}
		named = append(named, one)
	}
	return named, nil
}

// Cluster is one place this module may deploy to.
type Cluster struct {
	Name string `json:"name"`
	// Kubeconfig is the file's contents, not a path to it.
	//
	// The contents, and marked secret, because a kubeconfig is a credential and a path
	// is a promise about the world: the file gets deleted in the next upgrade, the
	// path changes with the container image, and a module deployed somewhere else
	// would need a mount somebody has to remember to add. Pasting the file works the
	// same way in every deployment.
	// A string rather than bytes: JSON has no byte string, so []byte would mean
	// base64 on the wire and every caller would have to encode the document before
	// storing and decode it again on the way out.
	Kubeconfig       string `json:"kubeconfig"`
	Context          string `json:"context"`
	DefaultNamespace string `json:"default_namespace"`

	// RolloutTimeout is how long to wait for this cluster's rollout, in seconds.
	// Zero means the module's own default.
	//
	// Per cluster rather than one number for the whole module, because the clusters
	// differ: one rolls out in twenty seconds and one takes four minutes, and both are
	// normal. A single number is either an endless wait on the fast one or a rollout
	// declared stuck on the slow one.
	RolloutTimeout seconds `json:"rollout_timeout"`

	// KeepJobs leaves this cluster's finished phase Jobs in place.
	//
	// Per cluster for the same reason, and because the two kinds of cluster want
	// different things: a staging cluster somebody is debugging wants the Jobs left
	// where they can be read, a production one does not want yesterday's migrations
	// sitting in the namespace.
	//
	// A pointer so that "nobody said" is not "said no", though for this one the default
	// and the answer happen to be the same.
	KeepJobs *bool `json:"keep_jobs"`

	// InUse says whether this cluster is one this project may deploy to. It is a
	// pointer so that "nobody said" and "said no" are two different things: a cluster
	// written before this field existed is in use, and a cluster somebody below
	// switched off is not.
	//
	// Off is not deleted. The cluster is still configured and still written down, and
	// switching it back on brings it back with its kubeconfig — which is the whole
	// reason this is a switch and not a row's absence.
	InUse *bool `json:"enabled,omitempty"`
}

// seconds is a number of seconds that cannot refuse to be read.
//
// A value that is not a number, or is not a positive one, reads as zero — which means
// the module's own default — rather than failing the whole list. One typo in one row of
// somebody's settings page should not take every other cluster down with it: the core
// refuses such a value on the way in, so this is about a value written before it did.
type seconds int

func (s *seconds) UnmarshalJSON(raw []byte) error {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	number, ok := value.(float64)
	if !ok || number <= 0 {
		return nil
	}
	*s = seconds(number)
	return nil
}

// timeout is how long to wait for this cluster's rollout, or the module's own default
// when this cluster says nothing about it.
func (c Cluster) timeout() time.Duration {
	if c.RolloutTimeout > 0 {
		return time.Duration(c.RolloutTimeout) * time.Second
	}
	return defaultRolloutTimeout
}

// keepJobs reports whether this cluster's finished Jobs are left in place.
func (c Cluster) keepJobs() bool {
	return c.KeepJobs != nil && *c.KeepJobs
}

// defaultRolloutTimeout is what a cluster that says nothing is given: ten minutes,
// which is long enough for a slow cluster and short enough that somebody is still
// watching when it runs out.
const defaultRolloutTimeout = 10 * time.Minute

// use reports whether a cluster is in use, which it is unless somebody said otherwise.
func (c Cluster) use() bool {
	return c.InUse == nil || *c.InUse
}

// Connect builds a client for a cluster, or says why it cannot.
func (c Cluster) Connect(ctx context.Context) (k8s.Client, error) {
	if strings.TrimSpace(c.Kubeconfig) == "" {
		return nil, fmt.Errorf("cluster %q has no kubeconfig, so there is no way in", c.Name)
	}
	return k8s.Connect(ctx, k8s.Access{
		Kubeconfig: []byte(c.Kubeconfig), Context: c.Context})
}

// find is the cluster of that name, or nothing.
func find(clusters []Cluster, name string) (Cluster, bool) {
	for _, one := range clusters {
		if one.Name == name {
			return one, true
		}
	}
	return Cluster{}, false
}

// timeoutFrom reads the module's own default for how long to wait.
//
// A value that is not a number is ignored rather than refused: the module said what it
// will do when nobody says otherwise, and a page with a typo in it should not stop
// deployments from being configured.
func timeoutFrom(settings map[string]any, key string, fallback time.Duration) time.Duration {
	seconds, ok := settings[key].(float64)
	if !ok || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

// setStartedAt moves a deployment's start, for the test that stands in for one whose
// module was killed.
//
// A test helper rather than part of the interface on purpose: nothing in the running
// module has any business rewriting when a deployment began.
func (h *postgresHistory) setStartedAt(ctx context.Context, id uuid.UUID, startedAt time.Time) error {
	_, err := h.pool.Exec(ctx,
		`UPDATE deployments SET started_at = $2 WHERE id = $1`, id, startedAt)
	return err
}

// ByID is one deployment, whichever place it was made to.
//
// Looked up by identity rather than by project and place, because a revert says which
// record it means: the row somebody clicked on a page, and there is no way to guess
// that from a project and a namespace — several deployments to the same place look
// alike from here.
func (h *postgresHistory) ByID(ctx context.Context, id uuid.UUID) (deploy.Deployment, error) {
	rows, err := h.pool.Query(ctx, deploymentColumns+` WHERE id = $1`, id)
	if err != nil {
		return deploy.Deployment{}, fmt.Errorf("read the deployment: %w", err)
	}
	defer rows.Close()

	if !rows.Next() {
		return deploy.Deployment{}, fmt.Errorf(
			"this module has no record of deployment %s, so there is nothing to go back to", id)
	}
	return scanDeployment(rows)
}
