package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
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

// History is this module's record of deployments.
type History interface {
	Begin(ctx context.Context, d deploy.Deployment) (deploy.Deployment, error)
	Phase(ctx context.Context, id uuid.UUID, state deploy.State, phase deploy.Phase, reason string) error
	Finish(ctx context.Context, id uuid.UUID, state deploy.State, reason string) error
	Current(ctx context.Context, project, cluster, namespace string) (*deploy.Deployment, error)
	// ByID is one deployment by identity, which is how a revert names the row
	// somebody clicked on.
	ByID(ctx context.Context, id uuid.UUID) (deploy.Deployment, error)
	List(ctx context.Context, project, cluster, namespace string) ([]deploy.Deployment, error)
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
			finished_at TIMESTAMPTZ
		)`,
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

	_, err := h.pool.Exec(ctx, `
		INSERT INTO deployments (id, project, cluster, namespace, image, workload, state, started_at)
		VALUES ($1, $2, $3, $4, $5, $6, 'running', $7)`,
		d.ID, d.Project, d.Cluster, d.Namespace, d.Image, d.Workload, d.StartedAt)
	if err != nil {
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

func (h *postgresHistory) Current(ctx context.Context, project, cluster, namespace string) (*deploy.Deployment, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT id, project, cluster, namespace, image, workload, state, phase, reason, started_at, finished_at
		FROM deployments
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

// An empty cluster or namespace means "any": the question a project page asks is what
// it has deployed, not what it deployed to one place it already knows the name of.
func (h *postgresHistory) List(ctx context.Context, project, cluster, namespace string) ([]deploy.Deployment, error) {
	rows, err := h.pool.Query(ctx, `
		SELECT id, project, cluster, namespace, image, workload, state, phase, reason, started_at, finished_at
		FROM deployments
		WHERE project = $1
		  AND ($2 = '' OR cluster = $2)
		  AND ($3 = '' OR namespace = $3)
		ORDER BY started_at DESC LIMIT 100`, project, cluster, namespace)
	if err != nil {
		return nil, fmt.Errorf("read the deployment history: %w", err)
	}
	defer rows.Close()

	out := []deploy.Deployment{}
	for rows.Next() {
		record, err := scanDeployment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (h *postgresHistory) Close(_ context.Context) {
	h.pool.Close()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanDeployment(rows scanner) (deploy.Deployment, error) {
	var record deploy.Deployment
	if err := rows.Scan(&record.ID, &record.Project, &record.Cluster, &record.Namespace,
		&record.Image, &record.Workload, &record.State, &record.Phase, &record.Reason,
		&record.StartedAt, &record.FinishedAt); err != nil {
		return deploy.Deployment{}, fmt.Errorf("read a deployment: %w", err)
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
	kept := clusters[:0]
	for _, one := range clusters {
		if strings.TrimSpace(one.Name) == "" {
			continue
		}
		kept = append(kept, one)
	}
	return kept, nil
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
	rows, err := h.pool.Query(ctx, `
		SELECT id, project, cluster, namespace, image, workload, state, phase, reason, started_at, finished_at
		FROM deployments WHERE id = $1`, id)
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
