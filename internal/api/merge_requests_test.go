package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/store"
)

// mergeFixture is a project with a main branch, a feature branch and a session.
//
// A merge request only means something once there is something to merge, so the
// fixture builds real commits in a real repository rather than describing them.
type mergeFixture struct {
	server  *Server
	git     *gitx.Git
	store   *store.Store
	project *models.Project
	owner   *models.User
	repo    string
	session string
}

func newMergeFixture(t *testing.T, configure ...func(*models.Project)) *mergeFixture {
	t.Helper()

	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	owner := dbtest.NewUser(t, st, "mr-owner", false)
	project := dbtest.NewProject(t, st, "mr", nil)
	// Merging is a project setting and defaults to off; without it the fixture
	// could not merge anything at all.
	project.AllowMerge = true
	for _, apply := range configure {
		apply(project)
	}
	if err := st.Projects().Update(ctx, project); err != nil {
		t.Fatalf("configure the project: %v", err)
	}

	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	project.RepoPath = repo
	dbtest.GrantRole(t, st, project.ID, owner.ID, models.AccessLevelOwner, "Owner")

	f := &mergeFixture{
		git: git, store: st, project: project, owner: owner,
		repo:    repo,
		session: dbtest.NewSession(t, st, owner.ID),
	}

	head := f.write(t, "README.md", "first\n", "")
	if err := git.UpdateRef(ctx, repo, "refs/heads/main", head, ""); err != nil {
		t.Fatalf("create main: %v", err)
	}
	// A feature branch with real work in it, which is the ordinary case.
	feature := f.write(t, "feature.txt", "a feature\n", head)
	if err := git.UpdateRef(ctx, repo, "refs/heads/feature", feature, ""); err != nil {
		t.Fatalf("create the feature branch: %v", err)
	}

	f.server = &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}
	return f
}

// write commits one file on top of parent and returns the commit.
func (f *mergeFixture) write(t *testing.T, path, content, parent string) string {
	t.Helper()

	ctx := context.Background()

	base := ""
	if parent != "" {
		tree, err := f.git.TreeOf(ctx, f.repo, parent)
		if err != nil {
			t.Fatalf("tree of %s: %v", parent, err)
		}
		base = tree
	}

	_, tree, err := f.git.WriteBlobAndTree(ctx, f.repo, base, path, []byte(content))
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	parents := []string{}
	if parent != "" {
		parents = []string{parent}
	}
	commit, err := f.git.CommitTree(ctx, f.repo, gitx.CommitTreeOptions{
		Tree: tree, Parents: parents, Message: "Change " + path,
		AuthorName: "Seed", AuthorEmail: "seed@example.test",
		CommitterName: "Seed", CommitterEmail: "seed@example.test",
	})
	if err != nil {
		t.Fatalf("commit %s: %v", path, err)
	}
	return commit
}

// do sends a request as the fixture's owner and decodes the JSON body.
func (f *mergeFixture) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}

	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: f.session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)

	payload := map[string]any{}
	if recorder.Body.Len() > 0 {
		_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	}
	return recorder.Code, payload
}

func (f *mergeFixture) mrURL(path string) string {
	return "/projects/" + f.project.ID.String() + "/merge_requests" + path
}

// The ordinary path: open a request, see it, merge it, find the work on main.
func TestMergeRequestLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)

	status, body := f.do(t, http.MethodPost, f.mrURL(""), `{
		"source_branch": "feature",
		"target_branch": "main",
		"title": "Add the feature",
		"description": "What this changes and why."
	}`)
	if status != http.StatusCreated {
		t.Fatalf("open: status %d, body %v", status, body)
	}

	mr, _ := body["merge_request"].(map[string]any)
	if mr == nil {
		t.Fatalf("no merge request in the response: %v", body)
	}
	if mr["iid"].(float64) != 1 {
		t.Errorf("iid = %v, want 1", mr["iid"])
	}
	if body["mergeable"] != "can_fast_forward" {
		t.Errorf("mergeable = %v, want can_fast_forward", body["mergeable"])
	}

	status, body = f.do(t, http.MethodGet, f.mrURL("/1"), "")
	if status != http.StatusOK {
		t.Fatalf("read: status %d, body %v", status, body)
	}
	read, _ := body["merge_request"].(map[string]any)
	if read["merge_status"] != "can_fast_forward" {
		t.Errorf("merge_status = %v", read["merge_status"])
	}
	if stats, _ := read["diff_stats"].(map[string]any); stats != nil {
		if stats["files_changed"].(float64) != 1 {
			t.Errorf("files_changed = %v, want 1", stats["files_changed"])
		}
	}

	status, body = f.do(t, http.MethodPost, f.mrURL("/1/merge"), `{"method":"merge"}`)
	if status != http.StatusOK {
		t.Fatalf("merge: status %d, body %v", status, body)
	}
	merged, _ := body["merge_request"].(map[string]any)
	if merged["state"] != "merged" {
		t.Errorf("state = %v, want merged", merged["state"])
	}

	// The work has to be on the target branch, and the merge commit has to have
	// two parents: one parent would mean the feature branch's history was dropped.
	if !f.git.Exists(ctx, f.repo, "main") {
		t.Fatal("the target branch is gone")
	}
	content, _, _, err := f.git.CatFile(ctx, f.repo, "main", "feature.txt")
	if err != nil {
		t.Fatalf("the feature file is not on main: %v", err)
	}
	if string(content) != "a feature\n" {
		t.Errorf("main has %q", content)
	}

	commits, err := f.git.Log(ctx, f.repo, "main", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits[0].Parents) != 2 {
		t.Errorf("the merge commit has %d parents, want 2", len(commits[0].Parents))
	}
	if !strings.Contains(commits[0].Subject, "feature") {
		t.Errorf("the merge commit subject is %q, it should name the branch", commits[0].Subject)
	}
}

// A conflicting merge must say so and change nothing.
func TestMergeRequestRefusesAConflictingMerge(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)

	// Both branches change the same line.
	mainHead, err := f.git.RevParse(ctx, f.repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	ours := f.write(t, "README.md", "ours\n", mainHead)
	if err := f.git.UpdateRef(ctx, f.repo, "refs/heads/main", ours, mainHead); err != nil {
		t.Fatal(err)
	}

	featureHead, err := f.git.RevParse(ctx, f.repo, "feature")
	if err != nil {
		t.Fatal(err)
	}
	theirs := f.write(t, "README.md", "theirs\n", featureHead)
	if err := f.git.UpdateRef(ctx, f.repo, "refs/heads/feature", theirs, featureHead); err != nil {
		t.Fatal(err)
	}

	if _, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main"}`); body["conflicts"] == nil {
		t.Logf("open reported: %v", body)
	}

	_, body := f.do(t, http.MethodGet, f.mrURL("/1"), "")
	mr, _ := body["merge_request"].(map[string]any)
	if mr["has_conflicts"] != true {
		t.Errorf("has_conflicts = %v, want true", mr["has_conflicts"])
	}
	if mr["merge_status"] != "conflicts" {
		t.Errorf("merge_status = %v, want conflicts", mr["merge_status"])
	}

	status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")
	if status != http.StatusConflict {
		t.Fatalf("merge: status %d, want 409; body %v", status, body)
	}

	// The target branch must not have moved.
	after, err := f.git.RevParse(ctx, f.repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if after != ours {
		t.Error("a refused merge still moved the target branch")
	}

	// And the request is still open, so the conflict can be fixed and retried.
	mr, _ = body["merge_request"].(map[string]any)
	if mr != nil && mr["state"] != "opened" {
		t.Errorf("state = %v, want opened after a refused merge", mr["state"])
	}
}

// Merging twice, or merging what is already merged, must not happen twice.
func TestMergeRequestCannotBeMergedTwice(t *testing.T) {
	f := newMergeFixture(t)

	if status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main"}`); status != http.StatusCreated {
		t.Fatalf("open: status %d, body %v", status, body)
	}
	if status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), ""); status != http.StatusOK {
		t.Fatalf("first merge: status %d, body %v", status, body)
	}

	status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")
	if status != http.StatusBadRequest {
		t.Fatalf("second merge: status %d, want 400; body %v", status, body)
	}
	if message, _ := body["error"].(map[string]any); message != nil {
		if !strings.Contains(message["message"].(string), "already merged") {
			t.Errorf("message = %v", message["message"])
		}
	}
}

// A project with merging switched off must refuse even for an owner: the setting
// is the point of the setting.
func TestMergeRequestHonoursTheProjectSetting(t *testing.T) {
	f := newMergeFixture(t, func(p *models.Project) { p.AllowMerge = false })

	if status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main"}`); status != http.StatusCreated {
		t.Fatalf("open: status %d, body %v", status, body)
	}

	status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")
	if status != http.StatusForbidden {
		t.Fatalf("merge: status %d, want 403; body %v", status, body)
	}
}

// The source branch may be removed as part of the merge, which is what keeps a
// repository from filling with branches whose work is already on main.
func TestMergeRequestCanRemoveTheSourceBranch(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t, func(p *models.Project) { p.RemoveSourceBranch = true })

	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")
	if status != http.StatusOK {
		t.Fatalf("merge: status %d, body %v", status, body)
	}
	if f.git.Exists(ctx, f.repo, "feature") {
		t.Error("the merged source branch is still there")
	}
	if !f.git.Exists(ctx, f.repo, "main") {
		t.Error("the target branch is gone")
	}
}

// Opening a second request for work already under review returns the first one
// instead: two live requests for one branch pair make the merge button ambiguous.
func TestMergeRequestDoesNotDuplicateABranchPair(t *testing.T) {
	f := newMergeFixture(t)

	status, _ := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main"}`)
	if status != http.StatusCreated {
		t.Fatalf("first open: status %d", status)
	}

	status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main","title":"Again"}`)
	if status != http.StatusOK {
		t.Fatalf("second open: status %d, want 200; body %v", status, body)
	}
	if body["existing"] != true {
		t.Errorf("the second open did not report an existing request: %v", body)
	}

	_, list := f.do(t, http.MethodGet, f.mrURL("?state=opened"), "")
	items, _ := list["merge_requests"].([]any)
	if len(items) != 1 {
		t.Errorf("there are %d open requests for one branch pair", len(items))
	}
}

// A branch that is already merged into the target has nothing to review.
// A merged request whose source branch was removed is still readable.
//
// Merging can delete the source branch, which is what keeps a repository from
// filling with branches that are already merged. The request outlives that
// branch, so reading it must not fail: the answer says the branches are gone
// instead of asking for a diff that cannot exist.
func TestMergedRequestSurvivesItsBranchBeingRemoved(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t, func(p *models.Project) { p.RemoveSourceBranch = true })

	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)
	if status, body := f.do(t, http.MethodPost, f.mrURL("/1/merge"), ""); status != http.StatusOK {
		t.Fatalf("merge: status %d, body %v", status, body)
	}
	if f.git.Exists(ctx, f.repo, "feature") {
		t.Fatal("the source branch is still there, so the rest of this test proves nothing")
	}

	status, body := f.do(t, http.MethodGet, f.mrURL("/1"), "")
	if status != http.StatusOK {
		t.Fatalf("reading a merged request whose branch is gone: status %d, body %v", status, body)
	}
	if body["branches_gone"] != true {
		t.Errorf("branches_gone = %v, want true", body["branches_gone"])
	}
	if url, _ := body["diff_url"].(string); url != "" {
		t.Errorf("diff_url = %q, want it left out when there is nothing to compare", url)
	}

	mr, _ := body["merge_request"].(map[string]any)
	if mr["state"] != "merged" {
		t.Errorf("state = %v, want merged", mr["state"])
	}
	if mr["merge_status"] != "branches_gone" {
		t.Errorf("merge_status = %v, want branches_gone", mr["merge_status"])
	}
	if title, _ := mr["title"].(string); title == "" {
		t.Error("the request itself did not come back")
	}
}

func TestMergeRequestRefusesABranchWithNothingNew(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)

	// Merge the feature into main by hand, so the feature is an ancestor.
	mainHead, _ := f.git.RevParse(ctx, f.repo, "main")
	featureHead, _ := f.git.RevParse(ctx, f.repo, "feature")
	if err := f.git.UpdateRef(ctx, f.repo, "refs/heads/main", featureHead, mainHead); err != nil {
		t.Fatal(err)
	}

	status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %v", status, body)
	}
	if message, _ := body["error"].(map[string]any); message != nil {
		if !strings.Contains(message["message"].(string), "no commits") {
			t.Errorf("message = %v", message["message"])
		}
	}
}

// A branch with the same name on both sides is not a merge request.
func TestMergeRequestRefusesIdenticalBranches(t *testing.T) {
	f := newMergeFixture(t)

	status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"main","target_branch":"main"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("status %d, want 400; body %v", status, body)
	}
}

// Comments belong to the request, in order.
func TestMergeRequestNotes(t *testing.T) {
	f := newMergeFixture(t)

	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	status, body := f.do(t, http.MethodPost, f.mrURL("/1/notes"), `{"body":"Looks good to me"}`)
	if status != http.StatusCreated {
		t.Fatalf("add a note: status %d, body %v", status, body)
	}
	if status, body := f.do(t, http.MethodPost, f.mrURL("/1/notes"), `{"body":"   "}`); status != http.StatusBadRequest {
		t.Errorf("an empty note was accepted: status %d, body %v", status, body)
	}

	_, body = f.do(t, http.MethodGet, f.mrURL("/1"), "")
	notes, _ := body["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("got %d notes, want 1", len(notes))
	}
	note, _ := notes[0].(map[string]any)
	if note["body"] != "Looks good to me" {
		t.Errorf("body = %v", note["body"])
	}
	if note["author_username"] != f.owner.Username {
		t.Errorf("author = %v, want %s", note["author_username"], f.owner.Username)
	}
}

// Closing and reopening, and refusing to reopen what was merged.
func TestMergeRequestStateChanges(t *testing.T) {
	f := newMergeFixture(t)

	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	status, body := f.do(t, http.MethodPut, f.mrURL("/1/state"), `{"state":"closed"}`)
	if status != http.StatusOK {
		t.Fatalf("close: status %d, body %v", status, body)
	}
	if mr, _ := body["merge_request"].(map[string]any); mr["state"] != "closed" {
		t.Errorf("state = %v, want closed", mr["state"])
	}

	status, body = f.do(t, http.MethodPut, f.mrURL("/1/state"), `{"state":"opened"}`)
	if status != http.StatusOK {
		t.Fatalf("reopen: status %d, body %v", status, body)
	}

	// Merged requests cannot come back.
	f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")
	status, body = f.do(t, http.MethodPut, f.mrURL("/1/state"), `{"state":"opened"}`)
	if status != http.StatusConflict {
		t.Errorf("reopening a merged request: status %d, want 409; body %v", status, body)
	}
}

// Somebody with read access may look, but not merge.
func TestMergeRequestMergeNeedsMergeRights(t *testing.T) {
	f := newMergeFixture(t)

	reader := dbtest.NewUser(t, f.store, "mr-reader", false)
	dbtest.GrantRole(t, f.store, f.project.ID, reader.ID, models.AccessLevelReporter, "Reporter")
	session := dbtest.NewSession(t, f.store, reader.ID)

	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	request := httptest.NewRequest(http.MethodPost, f.mrURL("/1/merge"), strings.NewReader(""))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatalf("a reporter merged a merge request: %s", recorder.Body.String())
	}
	if !f.git.Exists(context.Background(), f.repo, "feature") {
		t.Error("the source branch was removed by a refused merge")
	}
}

// A request about a project the caller cannot read must be invisible, not merely
// forbidden.
func TestMergeRequestOfAnUnreadableProjectIsInvisible(t *testing.T) {
	f := newMergeFixture(t)
	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	stranger := dbtest.NewUser(t, f.store, "mr-stranger", false)
	session := dbtest.NewSession(t, f.store, stranger.ID)

	request := httptest.NewRequest(http.MethodGet,
		"/projects/"+f.project.ID.String()+"/merge_requests/1", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", recorder.Code, recorder.Body.String())
	}
}

// The global listing spans projects and never shows what the caller cannot read.
func TestGlobalMergeRequestListRespectsVisibility(t *testing.T) {
	f := newMergeFixture(t)
	f.do(t, http.MethodPost, f.mrURL(""), `{"source_branch":"feature","target_branch":"main"}`)

	stranger := dbtest.NewUser(t, f.store, "mr-nobody", false)
	session := dbtest.NewSession(t, f.store, stranger.ID)

	request := httptest.NewRequest(http.MethodGet, "/merge_requests?state=opened", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}

	var payload struct {
		MergeRequests []map[string]any `json:"merge_requests"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.MergeRequests) != 0 {
		t.Errorf("a private project's merge requests leaked to a stranger: %v", payload.MergeRequests)
	}

	// The owner sees it, with the project joined in so a link can be built.
	_, body := f.do(t, http.MethodGet, "/merge_requests?state=opened", "")
	items, _ := body["merge_requests"].([]any)
	if len(items) != 1 {
		t.Fatalf("the owner sees %d requests", len(items))
	}
	entry, _ := items[0].(map[string]any)
	project, _ := entry["project"].(map[string]any)
	if project == nil || project["path"] != f.project.Path {
		t.Errorf("the project was not joined in: %v", entry["project"])
	}
	if entry["url"] == "" {
		t.Error("the request has no web address")
	}
}

// A title is generated from the branch name when none is given, because a request
// called "feature" is worse than one called "Feature Login Page".
func TestMergeRequestGeneratesATitle(t *testing.T) {
	ctx := context.Background()
	f := newMergeFixture(t)

	commit := f.write(t, "more.txt", "more\n", mustRev(t, f, "feature"))
	if err := f.git.UpdateRef(ctx, f.repo, "refs/heads/login-page", commit, ""); err != nil {
		t.Fatal(err)
	}

	status, body := f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"login-page","target_branch":"main"}`)
	if status != http.StatusCreated {
		t.Fatalf("status %d: %v", status, body)
	}

	mr, _ := body["merge_request"].(map[string]any)
	if mr["title"] != "Login Page" {
		t.Errorf("title = %v, want \"Login Page\"", mr["title"])
	}
}

// Editing is only possible while the request is open.
func TestMergeRequestEditIsRefusedAfterMerging(t *testing.T) {
	f := newMergeFixture(t)

	f.do(t, http.MethodPost, f.mrURL(""),
		`{"source_branch":"feature","target_branch":"main","title":"Original"}`)

	status, body := f.do(t, http.MethodPut, f.mrURL("/1"), `{"title":"Better title"}`)
	if status != http.StatusOK {
		t.Fatalf("edit: status %d, body %v", status, body)
	}
	mr, _ := body["merge_request"].(map[string]any)
	if mr["title"] != "Better title" {
		t.Errorf("title = %v", mr["title"])
	}

	f.do(t, http.MethodPost, f.mrURL("/1/merge"), "")

	status, body = f.do(t, http.MethodPut, f.mrURL("/1"), `{"title":"Too late"}`)
	if status != http.StatusBadRequest {
		t.Errorf("editing a merged request: status %d, want 400; body %v", status, body)
	}
}

func mustRev(t *testing.T, f *mergeFixture, ref string) string {
	t.Helper()

	sha, err := f.git.RevParse(context.Background(), f.repo, ref)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	return sha
}
