package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	// setImageTo is the image the last SetImage was asked for, and setImageFails what
	// the cluster was still running instead.
	setImageTo    string
	setImageFails string
	// pullSecrets is what was written into the namespace, in order.
	pullSecrets []k8s.PullSecret
}

func newFake() *fakeClient {
	return &fakeClient{
		failOn:   map[string]error{},
		rollouts: map[string]k8s.Rollout{},
	}
}

// A cancelled context fails here, the way it does against a real cluster: the caller
// has gone, and anything that would take time to talk to the API server no longer
// does. A fake that ignored it would say a deployment worked when nobody was there to
// receive it.
func (f *fakeClient) Apply(ctx context.Context, object k8s.Object) (k8s.State, error) {
	if err := ctx.Err(); err != nil {
		return k8s.State{}, err
	}
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

// SetImage is what a revert uses now, and it records what it was asked for: a revert
// that reports success without changing the image is the failure these tests exist for.
func (f *fakeClient) SetImage(_ context.Context, _, _, image string, _ time.Duration) (k8s.Rollout, error) {
	if f.rollbackErr != nil {
		return k8s.Rollout{}, f.rollbackErr
	}
	if f.setImageFails != "" {
		return k8s.Rollout{}, fmt.Errorf("the image was set to %s but app is running %s",
			image, f.setImageFails)
	}
	f.setImageTo = image
	return k8s.Rollout{Desired: 1, Updated: 1, Ready: 1, Done: true}, nil
}

func (f *fakeClient) RunningImage(_ context.Context, _, _ string) (string, error) {
	return f.setImageFails, nil
}

func (f *fakeClient) Revisions(_ context.Context, _, _ string) ([]k8s.Revision, error) {
	return nil, nil
}

// Counts answers what the watching code asks, so a test sees the same numbers a page
// would: no pods yet, none of the old ones left.
func (f *fakeClient) Counts(_ context.Context, _, _, _ string) (k8s.RolloutCounts, error) {
	return k8s.RolloutCounts{Ready: 1, Desired: 1}, nil
}

// WatchCounts stands in for the cluster's event stream: it says the same numbers once
// and then stays quiet, which is what a test wants — a rollout that never moves is one
// whose first frame can be asserted on.
//
// A fake that emitted a stream of changing numbers would be testing its own fiction.
func (f *fakeClient) WatchCounts(ctx context.Context, _, _, _ string, onChange func(k8s.RolloutCounts)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	onChange(k8s.RolloutCounts{Ready: 1, Desired: 1})
	<-ctx.Done()
	return nil
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
	// logs is what each deployment said, kept per deployment so a test can ask what a
	// run reported rather than only that it finished.
	logs map[string][]LogLine
}

// TagsOf is every name an image was deployed under in one place, from the whole
// history: what the real store does, and what makes the difference between a rollback's
// record (which names no tag) and the version the page has to show.
func (h *memoryHistory) TagsOf(_ context.Context, project, cluster, namespace,
	image string) ([]string, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	seen := map[string]bool{}
	for _, record := range h.records[lockKey(project, cluster, namespace)] {
		if record.Image != image {
			continue
		}
		for _, tag := range record.Tags {
			seen[tag] = true
		}
	}
	tags := make([]string, 0, len(seen))
	for tag := range seen {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	return tags, nil
}

func newHistory() *memoryHistory {
	return &memoryHistory{
		records: map[string][]Deployment{},
		busy:    map[string]string{},
		held:    make(chan string, 8),
		logs:    map[string][]LogLine{},
	}
}

// stuckHistory is a history where a deployment never finishes, which is what a
// deployment that is still running looks like from the outside.
type stuckHistory struct {
	*memoryHistory
}

// Counts does nothing: the deployment it belongs to never got as far as counting.
// The tests here are about a rollout that cannot finish, and the counts are not
// what they are checking.
func (s *stuckHistory) Counts(_ context.Context, _ uuid.UUID, _, _, _ int) error {
	return nil
}

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

// Log keeps what it was told, in order, which is what the tests check it for.
func (h *memoryHistory) Log(_ context.Context, id uuid.UUID, lines []LogLine) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	h.logs[id.String()] = append(h.logs[id.String()], lines...)
	return nil
}

// Images answers with nothing: these tests are about deployments, and a catalogue
// question asked of a test double should say "not what this is for" rather than make up
// an answer every test would then depend on.
func (h *memoryHistory) Images(_ context.Context, _, _, _ string, _, _ int) ([]KnownImage, int, error) {
	return nil, 0, nil
}

func (h *memoryHistory) LogOf(_ context.Context, id uuid.UUID) ([]LogLine, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()
	return h.logs[id.String()], nil
}

func (h *memoryHistory) Phase(_ context.Context, _ uuid.UUID, _ State, _ Phase, _ string) error {
	return nil
}

// Counts writes the rollout's numbers onto the record, the way the real history does.
func (h *memoryHistory) Counts(_ context.Context, id uuid.UUID, wanted, ready, retired int) error {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	for _, records := range h.records {
		for index := range records {
			if records[index].ID != id {
				continue
			}
			records[index].PodsWanted = wanted
			records[index].PodsReady = ready
			records[index].PodsRetired = retired
			h.records[lockKey(records[index].Project, records[index].Cluster,
				records[index].Namespace)] = records
		}
	}
	return nil
}

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

// ByID is a record from the map, by identity.
func (h *memoryHistory) ByID(_ context.Context, id uuid.UUID) (Deployment, error) {
	h.mutex.Lock()
	defer h.mutex.Unlock()

	for _, records := range h.records {
		for _, record := range records {
			if record.ID == id {
				return record, nil
			}
		}
	}
	return Deployment{}, fmt.Errorf("there is no deployment %s here", id)
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

// List pages the way the database does, so a test that paginates is testing the same
// arithmetic the real one does.
func (h *memoryHistory) List(_ context.Context, project, cluster, namespace string,
	limit, offset int) ([]Deployment, int, error) {

	h.mutex.Lock()
	defer h.mutex.Unlock()
	all := h.records[lockKey(project, cluster, namespace)]
	if offset > len(all) {
		offset = len(all)
	}
	page := all[offset:]
	if len(page) > limit {
		page = page[:limit]
	}
	return page, len(all), nil
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

// A revert says what it did and what it did not. It puts one particular image back; a
// database is not touched, and a message claiming otherwise would be the worst kind of
// wrong.
func TestARevertSaysWhatItDidNotUndo(t *testing.T) {
	client := newFake()
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	first, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:aaa", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err != nil {
		t.Fatalf("the first deployment: %v", err)
	}
	second, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err != nil {
		t.Fatalf("the second deployment: %v", err)
	}

	record, err := deployer.Revert(context.Background(), RevertRequest{ID: first.ID})
	if err != nil {
		t.Fatalf("revert: %v", err)
	}

	// The image that went back is the one that deployment ran, not "the previous
	// revision" of whatever the cluster still remembers.
	if client.setImageTo != "reg/app@sha256:aaa" {
		t.Errorf("the image put back was %q, want the one deployment %s ran", client.setImageTo, first.ID)
	}
	if record.State != StateReverted {
		t.Errorf("the history says %q after a revert", record.State)
	}
	if !strings.Contains(record.Reason, "left as they are") {
		t.Errorf("a revert does not say what it left alone: %q", record.Reason)
	}

	// The record of the second deployment is untouched: it is history, and rewriting
	// it would leave no memory of that version ever having been live.
	kept, err := history.ByID(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("read the second deployment: %v", err)
	}
	if kept.State != StateSucceeded {
		t.Errorf("the deployment that was reverted away from now says %q", kept.State)
	}
}

// A revert that changed nothing must be refused, not reported as done.
//
// This is the case that made the button untrustworthy: an undo with no revision behind
// it returns happily while the workload keeps the image it had.
func TestARevertThatChangesNothingIsRefused(t *testing.T) {
	client := newFake()
	client.setImageFails = "reg/app@sha256:bbb"
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	deployed, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:bbb", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if _, err := deployer.Revert(context.Background(), RevertRequest{ID: deployed.ID}); err == nil {
		t.Fatal("a revert that left the same image running was reported as done")
	}
}

// A deployment that never finished is not a version to go back to.
func TestADeploymentThatFailedIsNotAVersionToGoBackTo(t *testing.T) {
	client := newFake()
	client.failOn[k8s.Object{Kind: "Deployment", Namespace: "web", Name: "app"}.Ref()] =
		fmt.Errorf("refused")
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	_, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:aaa", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err == nil {
		t.Fatal("the deployment was expected to fail")
	}

	records, _, err := history.List(context.Background(), "home-store/www", "production", "web", 20, 0)
	if err != nil || len(records) == 0 {
		t.Fatalf("read the history: %v", err)
	}

	if _, err := deployer.Revert(context.Background(), RevertRequest{ID: records[0].ID}); err == nil {
		t.Fatal("a failed deployment was offered as something to go back to")
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

// A deployment the caller walked away from still has to be recorded as finished.
//
// This is the case the whole rule depends on. The record that says "running" is what
// holds a place; a deployment cut short by the core going away is written on the
// caller's context, which is already cancelled, so the outcome is lost — and the place
// is then held by a record nobody will ever close, for ever.
func TestACancelledCallerStillLeavesTheRecordFinished(t *testing.T) {
	client := newFake()
	history := newHistory()
	deployer := New(client, history, nil)

	// A context the caller has already walked away from, which is what the core's own
	// timeout looks like from in here.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := deployer.Run(ctx, Request{
		Project:   "app",
		Cluster:   "c",
		Namespace: "n",
		Image:     "reg/app@sha256:abc",
		Manifests: []k8s.Object{{
			APIVersion: "apps/v1", Kind: "Deployment", Namespace: "n", Name: "app",
			Body: []byte("apiVersion: apps/v1\nkind: Deployment\n"),
		}},
	})
	if err == nil {
		t.Fatal("a deployment on a cancelled context was reported as having worked")
	}

	current, err := history.Current(context.Background(), "app", "c", "n")
	if err != nil || current == nil {
		t.Fatalf("read the deployment back: %v", err)
	}
	if current.State == StateRunning {
		t.Error("the record is still running: the place is now held for ever")
	}
}

// Putting back the image that is already running is refused.
//
// It changes nothing — no template differs, so no new pods — and then reports success,
// which is how a button stops being believed. Said plainly instead.
func TestARevertToTheImageAlreadyRunningIsRefused(t *testing.T) {
	client := newFake()
	client.setImageFails = "reg/app@sha256:aaa"
	history := newHistory()
	deployer := &Deployer{client: client, history: history, Now: time.Now}

	deployed, err := deployer.Run(context.Background(), Request{
		Project: "home-store/www", Cluster: "production", Namespace: "web",
		Image: "reg/app@sha256:aaa", Placeholder: "IMAGE",
		Manifests: []k8s.Object{manifestObject("app", "IMAGE")}, Workload: "app",
	})
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}

	if _, err := deployer.Revert(context.Background(), RevertRequest{ID: deployed.ID}); err == nil {
		t.Fatal("putting back the image already running was reported as done")
	}
}
