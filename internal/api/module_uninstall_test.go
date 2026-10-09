package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// fakeModule is a module that removes itself on request.
//
// It stands in for the registry and everything else that will be written later:
// what these tests care about is the core's side of the contract — that it asks,
// reads a stream, keeps a log and reports an outcome — and none of that is
// particular to any one module.
type fakeModule struct {
	*httptest.Server

	// lines is what the module writes while removing itself.
	lines []map[string]any
	// status is what it answers with before streaming anything.
	status int
	// requests records the option keys each call was given.
	requests chan []string
}

func newFakeModule(t *testing.T, lines ...map[string]any) *fakeModule {
	t.Helper()

	module := &fakeModule{lines: lines, status: http.StatusOK, requests: make(chan []string, 8)}

	module.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/uninstall" {
			http.NotFound(w, r)
			return
		}

		var body struct {
			Options []string `json:"options"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		module.requests <- body.Options

		if module.status != http.StatusOK {
			http.Error(w, "I would rather not", module.status)
			return
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)
		for _, line := range module.lines {
			encoded, _ := json.Marshal(line)
			fmt.Fprintf(w, "%s\n", encoded)
			flusher.Flush()
		}
	}))
	t.Cleanup(module.Close)

	return module
}

// registerUninstallingModule records a module that declares how it is removed.
func registerUninstallingModule(t *testing.T, f *moduleFixture, endpoint string, options []models.UninstallOption) {
	t.Helper()

	// The module comes back as a module that declares how it is removed, which is
	// how a real one does it: by registering with a new manifest, not by being
	// asked to fill in a form.
	_, err := f.store.Integrations().Register(t.Context(), f.module.Kind, f.module.Name,
		endpoint, models.Manifest{
			Version: "0.1.0",
			Scopes:  []string{models.ScopeRegistryPush},
			Uninstall: models.UninstallSpec{
				Options: options,
			},
		})
	if err != nil {
		t.Fatalf("declare the module's removal options: %v", err)
	}
}

func registryOptions() []models.UninstallOption {
	return []models.UninstallOption{
		{
			Key:         "purge_data",
			Label:       "Delete all images",
			Description: "Every repository and tag created through dogit.",
			Default:     true,
			Dangerous:   true,
		},
		{
			Key:         "drop_database",
			Label:       "Delete the module database",
			Description: "The command that drops it; nothing can be recovered.",
			Default:     false,
			Dangerous:   true,
		},
	}
}

// startUninstall asks for a removal and returns the job the core answered with.
func (f *moduleFixture) startUninstall(t *testing.T, options ...string) map[string]any {
	t.Helper()

	body, _ := json.Marshal(map[string]any{"options": options})
	recorder := f.asAdmin(t, http.MethodPost,
		"/modules/"+f.module.ID.String()+"/uninstall", string(body))
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("start the removal: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	var answer struct {
		Job map[string]any `json:"job"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return answer.Job
}

// waitForJob waits until a job reaches one of the given states.
func (f *moduleFixture) waitForJob(t *testing.T, id string, states ...string) models.UninstallJob {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		job, err := f.store.ModuleUninstall().ByID(t.Context(), mustUUID(t, id))
		if err == nil {
			for _, state := range states {
				if job.Status == state {
					return *job
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	job, err := f.store.ModuleUninstall().ByID(t.Context(), mustUUID(t, id))
	if err != nil {
		t.Fatalf("read the job at the end: %v", err)
	}
	t.Fatalf("the job is %q, want one of %v", job.Status, states)
	return *job
}

// The whole path: a request, a stream, a log that survives, and a summary.
func TestUninstallingAModuleKeepsItsLog(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t,
		map[string]any{"message": "removing repository grp1/prj1",
			"progress": map[string]any{"done": 1, "total": 3}},
		map[string]any{"message": "removing repository grp2/api",
			"progress": map[string]any{"done": 2, "total": 3}},
		map[string]any{"message": "dropping database registry_ab12", "level": "warn"},
		map[string]any{"summary": map[string]any{
			"removed_repositories": 2, "removed_tags": 410, "freed_bytes": 536870912000,
		}},
	)
	registerUninstallingModule(t, f, module.URL, registryOptions())

	started := f.startUninstall(t, "purge_data")

	// The chosen keys are handed to the module verbatim: the core does not
	// translate them into anything of its own.
	select {
	case got := <-module.requests:
		if len(got) != 1 || got[0] != "purge_data" {
			t.Errorf("the module was asked for %v, want [purge_data]", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the module was never asked to remove itself")
	}

	job := f.waitForJob(t, started["id"].(string), models.UninstallDone)
	if summary, _ := job.Summary["removed_repositories"].(float64); summary != 2 {
		t.Errorf("the summary lost the module's own counts: %+v", job.Summary)
	}

	// The log is in the database, not in a pipe: this is the whole reason it is
	// stored, so that it can be read long after the request finished.
	lines, err := f.store.ModuleUninstall().LogAfter(t.Context(), job.ID, 0, 0)
	if err != nil {
		t.Fatalf("read the log: %v", err)
	}

	messages := []string{}
	for _, line := range lines {
		messages = append(messages, line.Message)
	}
	joined := strings.Join(messages, "\n")
	for _, want := range []string{
		"asking", "removing repository grp1/prj1", "removing repository grp2/api",
		"dropping database registry_ab12", "done",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("the log has nothing about %q:\n%s", want, joined)
		}
	}

	// The warning level the module chose is kept rather than flattened.
	var warned bool
	for _, line := range lines {
		if strings.Contains(line.Message, "dropping database") && line.Level == "warn" {
			warned = true
		}
	}
	if !warned {
		t.Error("the module's warning level was lost")
	}

	// Progress is the module's own, and a finished job has no bar to show.
	if job.ProgressDone == nil || *job.ProgressDone != 2 {
		t.Errorf("progress is %v, want 2", job.ProgressDone)
	}
	if _, ok := (&models.UninstallJob{}).Percent(); ok {
		t.Error("a job with no progress invented a percentage")
	}
}

// The core refuses to remove a module on terms the module never agreed to.
//
// This is the guard that matters most in the whole feature: if the core and a
// module disagree about what "remove me" means, guessing which of them is right
// is how data that somebody meant to keep gets deleted.
func TestUninstallOptionsAreCheckedAgainstTheModule(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t, map[string]any{"message": "done"})
	registerUninstallingModule(t, f, module.URL, registryOptions())

	path := "/modules/" + f.module.ID.String() + "/uninstall"

	if recorder := f.asAdmin(t, http.MethodPost, path, `{"options":["nonsense"]}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("an option the module never offered: status %d, want 400", recorder.Code)
	}

	// And the module was not asked to do anything on that basis.
	select {
	case got := <-module.requests:
		t.Errorf("the module was asked to remove itself with %v after a refused request", got)
	case <-time.After(200 * time.Millisecond):
	}

	// A required option is the module saying "removing me without this loses
	// data". The core obeys that rather than tidying up after it.
	options := append(registryOptions(), models.UninstallOption{
		Key: "drop_everything", Label: "Drop everything", Required: true,
	})
	registerUninstallingModule(t, f, module.URL, options)

	if recorder := f.asAdmin(t, http.MethodPost, path, `{"options":["purge_data"]}`); recorder.Code != http.StatusBadRequest {
		t.Errorf("a removal missing a required option: status %d, want 400", recorder.Code)
	}
}

// A module that removed itself stops being listed. If the process is somehow
// still running, its next heartbeat is refused and it re-registers — a module
// that removed itself should not keep serving by being merely alive.
func TestARemovedModuleIsForgotten(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t, map[string]any{
		"summary": map[string]any{"removed_entries": 2},
	})
	registerUninstallingModule(t, f, module.URL, registryOptions())

	started := f.startUninstall(t, "purge_data")
	f.waitForJob(t, started["id"].(string), models.UninstallDone)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := f.store.Integrations().ByID(t.Context(), f.module.ID); err != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the module is still registered after it removed itself")
}

// A module that says no leaves a record that says so.
func TestUninstallFailureIsRecorded(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t)
	module.status = http.StatusInternalServerError
	registerUninstallingModule(t, f, module.URL, registryOptions())

	started := f.startUninstall(t, "purge_data")
	job := f.waitForJob(t, started["id"].(string), models.UninstallFailed)

	if job.Error == "" {
		t.Error("a failed removal recorded no reason")
	}

	lines, _ := f.store.ModuleUninstall().LogAfter(t.Context(), job.ID, 0, 0)
	var sawError bool
	for _, line := range lines {
		if line.Level == "error" {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("the log has no error line: %+v", lines)
	}

	// A module that refused stays registered: it is still there, still holding
	// whatever it holds, and forgetting it would hide a problem rather than fix it.
	if _, err := f.store.Integrations().ByID(t.Context(), f.module.ID); err != nil {
		t.Errorf("a module that refused to be removed was forgotten anyway: %v", err)
	}
}

// A module that accepts the work and then says nothing is neither a failure nor
// a success, and the core must not pretend to know which.
//
// The janitor is what decides, after its deadline; until then the job keeps
// running, because a module deleting a great many small things is working, not
// stuck, and calling that a failure would be a lie.
func TestAModuleThatGoesQuietIsNotYetAFailure(t *testing.T) {
	f := newModuleFixture(t)

	release := make(chan struct{})

	module := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"message":"starting"}`+"\n")
		w.(http.Flusher).Flush()

		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))

	// The handler is still holding the connection open, so it is let go of before
	// the server is asked to shut down. The other order waits forever: closing a
	// server waits for its requests, and the request waits for this channel.
	t.Cleanup(func() {
		close(release)
		module.Close()
	})
	registerUninstallingModule(t, f, module.URL, registryOptions())

	started := f.startUninstall(t, "purge_data")

	// The one line it managed to write is kept: it is usually the line that
	// explains where a module stopped.
	deadline := time.Now().Add(5 * time.Second)
	var lines []models.UninstallLogLine
	for time.Now().Before(deadline) {
		lines, _ = f.store.ModuleUninstall().LogAfter(t.Context(), mustUUID(t, started["id"].(string)), 0, 0)
		if len(lines) > 0 && strings.Contains(lines[len(lines)-1].Message, "starting") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	var found bool
	for _, line := range lines {
		if strings.Contains(line.Message, "starting") {
			found = true
		}
	}
	if !found {
		t.Errorf("the line the module managed to write was not kept: %+v", lines)
	}

	// Still running: nothing has declared it finished, and nothing has declared
	// it broken either.
	job, err := f.store.ModuleUninstall().ByID(t.Context(), mustUUID(t, started["id"].(string)))
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if job.Finished() {
		t.Errorf("the job is %q; a silent module is not a finished one", job.Status)
	}
}

// One removal at a time: a second click while the first is running is a mistake,
// not a queue.
func TestOnlyOneRemovalPerModuleAtATime(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t)
	registerUninstallingModule(t, f, module.URL, registryOptions())

	// Held open so the first removal is still running when the second arrives.
	release := make(chan struct{})
	module.Close()
	module.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintf(w, `{"message":"done"}`+"\n")
		w.(http.Flusher).Flush()
	}))
	t.Cleanup(module.Close)
	t.Cleanup(func() { close(release) })
	registerUninstallingModule(t, f, module.URL, registryOptions())

	f.startUninstall(t, "purge_data")

	body, _ := json.Marshal(map[string]any{"options": []string{"purge_data"}})
	path := "/modules/" + f.module.ID.String() + "/uninstall"

	deadline := time.Now().Add(3 * time.Second)
	var code int
	for time.Now().Before(deadline) {
		code = f.asAdmin(t, http.MethodPost, path, string(body)).Code
		if code == http.StatusConflict {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if code != http.StatusConflict {
		t.Errorf("a second removal: status %d, want 409", code)
	}
}

// A restart does not know what happened, and must not pretend otherwise.
func TestRestartMarksOpenRemovalsUnknown(t *testing.T) {
	f := newModuleFixture(t)

	if _, err := f.store.ModuleUninstall().Start(t.Context(), f.module.ID, "registry:docker", "demo", []string{"purge_data"}); err != nil {
		t.Fatalf("start a removal: %v", err)
	}

	// Every unfinished job is interrupted, not just this module's: a restart stops
	// all of them, and pretending otherwise would leave one running that nobody is
	// watching. The count is deliberately not asserted — jobs outlive the modules
	// they describe, so a shared database holds others.
	if _, err := f.store.ModuleUninstall().InterruptOpen(t.Context()); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	job, err := f.store.ModuleUninstall().Latest(t.Context(), f.module.ID)
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	if job.Status != models.UninstallInterrupted {
		t.Errorf("the job is %q, want %q", job.Status, models.UninstallInterrupted)
	}
	if job.Error == "" {
		t.Error("an interrupted removal gives no explanation")
	}
	if !job.Finished() {
		t.Error("an interrupted removal still counts as running")
	}

	// The database no longer holds a job against this module, so a new removal
	// may begin — which is exactly the point of marking it unknown rather than
	// leaving it open forever.
	if _, err := f.store.ModuleUninstall().Start(t.Context(), f.module.ID, "registry:docker", "demo", nil); err != nil {
		t.Errorf("a new removal after an interruption: %v", err)
	}
}

// A stalled removal is not a failed one, and keeps whatever the module managed to
// write before going quiet.
func TestStalledRemovalKeepsItsLog(t *testing.T) {
	f := newModuleFixture(t)

	job, err := f.store.ModuleUninstall().Start(t.Context(), f.module.ID, "registry:docker", "demo", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := f.store.ModuleUninstall().MarkRunning(t.Context(), job.ID); err != nil {
		t.Fatalf("mark running: %v", err)
	}
	if _, err := f.store.ModuleUninstall().AppendLog(t.Context(), job.ID, "info",
		"removing repository grp1/prj1", nil); err != nil {
		t.Fatalf("append: %v", err)
	}

	// The line was written a while ago, which is the only evidence a job gives of
	// being stuck: what it last said, and when.
	if _, err := f.store.Pool().Exec(t.Context(),
		`UPDATE module_uninstall_jobs SET last_line_at = now() - interval '10 minutes'
		 WHERE id = $1`, job.ID); err != nil {
		t.Fatalf("age the job: %v", err)
	}

	stalled, err := f.store.ModuleUninstall().Stale(t.Context(), store.StalledAfter)
	if err != nil {
		t.Fatalf("mark stalled: %v", err)
	}
	if stalled != 1 {
		t.Fatalf("stalled %d jobs, want 1", stalled)
	}

	// A line written after the deadline proves the job is alive, and silence is
	// the only evidence there is.
	if err := f.store.ModuleUninstall().Touch(t.Context(), job.ID); err != nil {
		t.Fatalf("touch: %v", err)
	}
	if again, _ := f.store.ModuleUninstall().Stale(t.Context(), store.StalledAfter); again != 0 {
		t.Errorf("a job that had just spoken was stalled again (%d)", again)
	}

	current, err := f.store.ModuleUninstall().Latest(t.Context(), f.module.ID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if current.Status != models.UninstallRunning {
		t.Errorf("the job is %q", current.Status)
	}

	lines, _ := f.store.ModuleUninstall().LogAfter(t.Context(), job.ID, 0, 0)
	if len(lines) != 1 || !strings.Contains(lines[0].Message, "grp1/prj1") {
		t.Errorf("a stalled job lost its log: %+v", lines)
	}
}

// The log is read forwards from a remembered point, so a dropped connection
// cannot lose a line.
func TestLogIsReadFromWhereItWasLeft(t *testing.T) {
	f := newModuleFixture(t)

	job, err := f.store.ModuleUninstall().Start(t.Context(), f.module.ID, "registry:docker", "demo", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	ids := []int64{}
	for _, message := range []string{"first", "second", "third"} {
		line, err := f.store.ModuleUninstall().AppendLog(t.Context(), job.ID, "info", message, nil)
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		ids = append(ids, line.ID)
	}

	// A reader that saw the first line asks for what came after it.
	rest, err := f.store.ModuleUninstall().LogAfter(t.Context(), job.ID, ids[0], 0)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if len(rest) != 2 || rest[0].Message != "second" || rest[1].Message != "third" {
		t.Errorf("resumed reading gave %+v", rest)
	}

	// Nothing new yet: an empty answer, not a stall.
	none, err := f.store.ModuleUninstall().LogAfter(t.Context(), job.ID, ids[2], 0)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("there is nothing after the last line, got %+v", none)
	}
}

// The summary is turned into a sentence for the log, because the last thing an
// administrator reads should be the point of the exercise.
func TestSummaryIsWrittenAsAReadableLine(t *testing.T) {
	line := describeSummary(map[string]any{
		"removed_repositories": float64(12),
		"removed_tags":         float64(410),
		"freed_bytes":          float64(536870912000),
		"dropped_database":     "registry_ab12",
	})

	for _, want := range []string{"12", "410", "500.0 GiB", "registry_ab12"} {
		if !strings.Contains(line, want) {
			t.Errorf("the line %q says nothing about %q", line, want)
		}
	}

	if plain := describeSummary(map[string]any{}); plain != "the module finished removing itself" {
		t.Errorf("an empty summary read as %q", plain)
	}
}

// Removing a module is an administrator's decision, like every other thing done
// to a module.
func TestUninstallNeedsAnAdministrator(t *testing.T) {
	f := newModuleFixture(t)

	module := newFakeModule(t, map[string]any{"message": "done"})
	registerUninstallingModule(t, f, module.URL, registryOptions())

	session := dbtestSession(t, f)
	path := "/modules/" + f.module.ID.String() + "/uninstall"

	if recorder := f.as(t, session, http.MethodPost, path, `{"options":["purge_data"]}`); recorder.Code == http.StatusAccepted {
		t.Error("an ordinary user started a removal")
	}
	select {
	case got := <-module.requests:
		t.Errorf("the module was asked to remove itself with %v by a non-administrator", got)
	case <-time.After(200 * time.Millisecond):
	}

	if recorder := f.as(t, session, http.MethodGet, path+"/log", ""); recorder.Code == http.StatusOK {
		t.Error("an ordinary user streamed the removal log")
	}
}

func TestUninstallJobStoreRejectsAMissingModule(t *testing.T) {
	f := newModuleFixture(t)

	if _, err := f.store.ModuleUninstall().Current(t.Context(), f.module.ID); err == nil {
		t.Error("a module with no removals answered with one")
	}

	var none *models.UninstallJob
	if _, ok := none.Percent(); ok {
		t.Error("no job at all invented a percentage")
	}
}

func mustUUID(t *testing.T, raw string) uuid.UUID {
	t.Helper()
	id, err := uuid.Parse(raw)
	if err != nil {
		t.Fatalf("the core answered with an unusable job id %q", raw)
	}
	return id
}

func dbtestSession(t *testing.T, f *moduleFixture) string {
	t.Helper()
	return dbtest.NewSession(t, f.store, f.plain.ID)
}
