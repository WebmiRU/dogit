package deploy

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/ewolf/dogit/cmd/module-deploy-kubernetes/k8s"
)

// The order things happen in, and what stops when something fails.
//
// The phases exist because of one uncomfortable fact: a rolling update means old pods
// and new ones run at the same time, against the same database. A migration that runs
// halfway through that leaves the cluster straddling two schemas. So the migration
// runs first or not at all, and the tests here are about that being true.

// fakeClient is a cluster that is only a list of what it was asked to do.
type fakeClient struct {
	applied     []k8s.Object
	deleted     []string
	failOn      map[string]error
	rollouts    map[string]k8s.Rollout
	rollbackErr error
	// pullSecrets is what was written into the namespace, in order.
	pullSecrets []k8s.PullSecret
}

func newFake() *fakeClient {
	return &fakeClient{
		failOn:   map[string]error{},
		rollouts: map[string]k8s.Rollout{},
	}
}

func (f *fakeClient) Apply(_ context.Context, object k8s.Object) (k8s.State, error) {
	if err := f.failOn[object.Ref()]; err != nil {
		return k8s.State{}, err
	}
	f.applied = append(f.applied, object)
	return k8s.State{Ref: object.Ref(), Applied: true}, nil
}

// EnsurePullSecret records what was written, because whether a namespace has the
// credential is the difference between pods that start and pods that do not.
func (f *fakeClient) EnsurePullSecret(_ context.Context, namespace string, secret k8s.PullSecret) error {
	f.pullSecrets = append(f.pullSecrets, secret)
	return nil
}

func (f *fakeClient) Get(_ context.Context, ref k8s.Object) (*unstructured.Unstructured, error) {
	if err := f.failOn[ref.Ref()]; err != nil {
		return nil, err
	}
	// A Job answers as finished and successful unless it was told otherwise.
	status := "True"
	kind := "Complete"
	if err := f.failOn["job:"+ref.Name]; err != nil {
		status = "True"
		kind = "Failed"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"status": map[string]any{
			"conditions": []any{map[string]any{
				"type": kind, "status": status, "reason": "BackoffLimitExceeded",
				"message": "the migration failed",
			}},
		},
	}}, nil
}

func (f *fakeClient) Delete(_ context.Context, ref k8s.Object) error {
	f.deleted = append(f.deleted, ref.Ref())
	return nil
}

func (f *fakeClient) Rollout(_ context.Context, _, name string) (k8s.Rollout, error) {
	if r, ok := f.rollouts[name]; ok {
		return r, nil
	}
	return k8s.Rollout{Desired: 1, Updated: 1, Ready: 1, Done: true}, nil
}

func (f *fakeClient) Rollback(_ context.Context, _, _ string) (k8s.Rollout, error) {
	if f.rollbackErr != nil {
		return k8s.Rollout{}, f.rollbackErr
	}
	return k8s.Rollout{Desired: 1, Updated: 1, Ready: 1, Done: true}, nil
}

func (f *fakeClient) Revisions(_ context.Context, _, _ string) ([]k8s.Revision, error) {
	return nil, nil
}

// appliedRefs is what reached the cluster, in order.
func (f *fakeClient) appliedRefs() []string {
	out := make([]string, 0, len(f.applied))
	for _, one := range f.applied {
		out = append(out, one.Ref())
	}
	return out
}

// memoryHistory is a history in a map, with the same refusal about one at a time.
type memoryHistory struct {
	mutex   sync.Mutex
	records map[string][]Deployment
	busy    map[string]string
	// held is closed when something takes a lock, so a test can wait for the other
	// deployment to be under way rather than sleeping and hoping.
	held chan string
}

func newHistory() *memoryHistory {
	return &memoryHistory{
		records: map[string][]Deployment{},
		busy:    map[string]string{},
		held:    make(chan string, 8),
	}
}

// stuckHistory is a history where a deployment never finishes, which is what a
// deployment that is still running looks like from the outside.
type stuckHistory struct {
	*memoryHistory
}

// Finish does nothing: the deployment it belongs to is still running.
func (s *stuckHistory) Finish(_ context.Context, _ uuid.UUID, _ State, _ string) error {
	return nil
}

func (h *memoryHistory) Begin(_ context.Context, d Deployment) (Deployment, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	key := lockKey(d.Project, d.Cluster, d.Namespace)
	if running, taken := h.busy[key]; taken {
		return d, ErrBusy{Namespace: d.Namespace, Running: running}
	}
	h.busy[key] = d.ID.String()
	h.records[key] = append(h.records[key], d)

	select {
	case h.held <- key:
	default:
	}
	return d, nil
}

// waitForLock blocks until some deployment has taken a lock.
func waitForLock(t *testing.T, h *memoryHistory) {
	t.Helper()
	select {
	case <-h.held:
	case <-time.After(2 * time.Second):
		t.Fatal("no deployment ever started")
	}
}

func lockKey(project, cluster, namespace string) string {
	return strings.Join([]string{project, cluster, namespace}, "|")
}

func (h *memoryHistory) Phase(_ context.Context, _ uuid.UUID, _ State, _ Phase, _ string) error {
	return nil
}

// Finish closes the deployment and frees the lock.
func (h *memoryHistory) Finish(_ context.Context, id uuid.UUID, state State, reason string) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	for key, list := range h.records {
		for index := range list {
			if list[index].ID != id {
				continue
			}
			list[index].State = state
			list[index].Reason = reason
			h.records[key] = list
		}
		if h.busy[key] == id.String() {
			delete(h.busy, key)
		}
	}
	return nil
}

func (h *memoryHistory) Current(_ context.Context, project, cluster, namespace string) (*Deployment, error) {
	key := lockKey(project, cluster, namespace)
	list := h.records[key]
	if len(list) == 0 {
		return nil, nil
	}
	last := list[len(list)-1]
	return &last, nil
}

func (h *memoryHistory) List(_ context.Context, project, cluster, namespace string) ([]Deployment, error) {
	return h.records[lockKey(project, cluster, namespace)], nil
}

func manifestObject(name, image string) k8s.Object {
	return k8s.Object{
		APIVersion: "apps/v1", Kind: "Deployment", Namespace: "web", Name: name,
		Body: []byte("spec:\n  template:\n    spec:\n      containers:\n        - image: " + image + "\n"),
	}
}

func jobObject(name string) k8s.Object {
	return k8s.Object{
		APIVersion: "batch/v1", Kind: "Job", Namespace: "web", Name: name,
		Body: []byte("spec:\n  template:\n    spec:\n      containers:\n        - image: IMAGE\n"),
	}
}

// A deployment that works: the objects go out with the image in them, and the history
// says what was deployed.
func TestADeploymentAppliesTheImageAndRemembersIt(t *testing.T) {
	client := newFake()
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	record, err := deployer.Run(context.Background(), Request{
		Project:     "home-store/www",
		Cluster:     "production",
		Namespace:   "web",
		Image:       "reg/app@sha256:bbb",
		Placeholder: "IMAGE",
		Manifests:   []k8s.Object{manifestObject("app", "IMAGE")},
		Workload:    "app",
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if record.State != StateSucceeded {
		t.Errorf("state is %q, want succeeded", record.State)
	}
	if record.Image != "reg/app@sha256:bbb" {
		t.Errorf("the history does not say what was deployed: %q", record.Image)
	}

	applied := string(client.applied[0].Body)
	if !strings.Contains(applied, "reg/app@sha256:bbb") || strings.Contains(applied, "image: IMAGE") {
		t.Errorf("what was applied still has the placeholder: %s", applied)
	}
}

// A migration that fails stops everything. This is the whole reason the phases are in
// this order: applying the new pods after a failed migration would leave the cluster
// straddling two schemas, with the old code talking to a schema it does not know.
func TestAFailedMigrationStopsTheDeploymentBeforeAnythingIsApplied(t *testing.T) {
	client := newFake()
	client.failOn["job:migrate"] = errors.New("the migration failed")
	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	record, err := deployer.Run(context.Background(), Request{
		Project:     "home-store/www",
		Cluster:     "production",
		Namespace:   "web",
		Image:       "reg/app@sha256:bbb",
		Placeholder: "IMAGE",
		Manifests:   []k8s.Object{manifestObject("app", "IMAGE")},
		Pre:         []Job{{Name: "migrate", Object: jobObject("migrate")}},
		Workload:    "app",
	})

	if err == nil {
		t.Fatalf("a failed migration was reported as a successful deployment; applied=%v state=%q phase=%q",
			client.appliedRefs(), record.State, record.Phase)
	}
	if record.State != StateFailed || record.Phase != PhasePre {
		t.Errorf("recorded as %q in %q, want failed in pre", record.State, record.Phase)
	}
	for _, ref := range client.appliedRefs() {
		if strings.HasPrefix(ref, "Deployment") {
			t.Errorf("the Deployment was applied although the migration failed: %v", client.appliedRefs())
		}
	}
}

// A post step that fails is not a failed deployment: the rollout happened, and the
// person reading it needs to be told which of the two went wrong.
func TestAFailedPostStepSaysTheRolloutHappened(t *testing.T) {
	client := newFake()
	client.failOn["job:smoke"] = errors.New("the check failed")
	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	record, err := deployer.Run(context.Background(), Request{
		Project:     "home-store/www",
		Cluster:     "production",
		Namespace:   "web",
		Image:       "reg/app@sha256:bbb",
		Placeholder: "IMAGE",
		Manifests:   []k8s.Object{manifestObject("app", "IMAGE")},
		Post:        []Job{{Name: "smoke", Object: jobObject("smoke")}},
		Workload:    "app",
	})

	if err == nil {
		t.Fatal("a failed post step was reported as success")
	}
	if record.Phase != PhasePost {
		t.Errorf("recorded as failing in %q, want post", record.Phase)
	}
	if !strings.Contains(err.Error(), "post step") {
		t.Errorf("the message does not say which half failed: %v", err)
	}
	// The Deployment did go out; the failure was after it.
	if len(client.appliedRefs()) == 0 {
		t.Error("nothing was applied at all, so this was not a post-step failure")
	}
}

// One deployment per cluster and namespace at a time. Two at once is two migrations
// against one database, and it is refused rather than queued silently.
func TestTwoDeploymentsAtOnceAreRefused(t *testing.T) {
	client := newFake()
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	// A history whose first deployment never finishes, which is what "running" is.
	blocked := &stuckHistory{memoryHistory: history}

	first := &Deployer{client: client, history: blocked, Now: time.Now}
	go func() {
		_, _ = first.Run(context.Background(), Request{
			Project: "home-store/www", Cluster: "production", Namespace: "web",
			Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
			Manifests: []k8s.Object{manifestObject("app", "IMAGE")},
		})
	}()
	waitForLock(t, blocked.memoryHistory)

	_, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:ccc", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")},
	})

	if err == nil {
		t.Fatal("a second deployment to the same place was accepted")
	}
	var busy ErrBusy
	if !errors.As(err, &busy) {
		t.Fatalf("the refusal is %v, want the busy one", err)
	}
	if !strings.Contains(busy.Error(), "migration") {
		t.Errorf("the refusal does not explain the danger: %v", busy)
	}
}

// A deployment to another namespace of the same cluster is not blocked: the lock is on
// the pair, because that is what two rollouts would collide over.
func TestADeploymentToAnotherNamespaceIsNotBlocked(t *testing.T) {
	client := newFake()
	history := newHistory()
	blocked := &stuckHistory{memoryHistory: history}
	deployer := &Deployer{client: client, history: blocked, Now: time.Now}

	go func() {
		_, _ = (&Deployer{client: client, history: blocked, Now: time.Now}).Run(
			context.Background(), Request{
				Project: "home-store/www", Cluster: "production", Namespace: "web",
				Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
				Manifests: []k8s.Object{manifestObject("app", "IMAGE")},
			})
	}()
	waitForLock(t, blocked.memoryHistory)

	if _, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "api",
		Image: "reg/app@sha256:ccc", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")},
	}); err != nil {
		t.Errorf("a deployment to a different namespace was refused: %v", err)
	}
}

// A rollback says what it did and what it did not. It puts the image back; a database
// is not touched, and a message claiming otherwise would be the worst kind of wrong.
func TestARollbackSaysWhatItDidNotUndo(t *testing.T) {
	client := newFake()
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	_, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	record, err := deployer.Rollback(context.Background(), "home-store/www", "production", "web")
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if record.State != StateRolledBack {
		t.Errorf("the history says %q after a rollback", record.State)
	}
	if !strings.Contains(record.Reason, "left alone") {
		t.Errorf("a rollback does not say what it left alone: %q", record.Reason)
	}
}

// A Job that has run is removed, and one that failed is removed too: it has already
// said what it had to say, and a namespace that accumulates finished Jobs is a
// namespace nobody can read.
func TestFinishedJobsAreRemoved(t *testing.T) {
	client := newFake()
	deployer := &Deployer{client: client, history: newHistory(), Now: time.Now}

	if _, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")},
		Pre:       []Job{{Name: "migrate", Object: jobObject("migrate")}},
		Workload:  "app",
	}); err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if len(client.deleted) != 1 || !strings.Contains(client.deleted[0], "migrate") {
		t.Errorf("the finished job was not removed: %v", client.deleted)
	}
}
