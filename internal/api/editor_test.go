package api

import (
	"context"
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

// The editor is the one place where a request both reads and writes git history,
// so it is exercised end to end: a real bare repository on disk, a real database,
// and the real router, so the handler runs exactly as it does in production.
type editorFixture struct {
	server   *Server
	store    *store.Store
	git      *gitx.Git
	project  *models.Project
	user     *models.User
	repoPath string
	session  string
}

// newEditorFixture prepares a project with one commit, owned by a user with a
// live session cookie.
func newEditorFixture(t *testing.T, admin bool) *editorFixture {
	t.Helper()

	ctx := context.Background()
	st := dbtest.Open(t)

	user := dbtest.NewUser(t, st, "editor", admin)

	repoRoot := t.TempDir()
	git := gitx.New(gitx.Options{})

	project := dbtest.NewProject(t, st, "edited", nil)
	repoPath := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repoPath); err != nil {
		t.Fatalf("create repository: %v", err)
	}

	project.RepoPath = repoPath
	dbtest.GrantRole(t, st, project.ID, user.ID, models.AccessLevelOwner, "Owner")

	// Seed one commit so the branch exists.
	seed := func(path, content, message string) string {
		t.Helper()
		_, tree, err := git.WriteBlobAndTree(ctx, repoPath, "", path, []byte(content))
		if err != nil {
			t.Fatalf("seed tree: %v", err)
		}
		commit, err := git.CommitTree(ctx, repoPath, gitx.CommitTreeOptions{
			Tree: tree, Message: message,
			AuthorName: "Seed", AuthorEmail: "seed@example.test",
			CommitterName: "Seed", CommitterEmail: "seed@example.test",
		})
		if err != nil {
			t.Fatalf("seed commit: %v", err)
		}
		return commit
	}
	first := seed("README.md", "hello\n", "Initial commit")
	if err := git.UpdateRef(ctx, repoPath, "refs/heads/main", first, ""); err != nil {
		t.Fatalf("create main: %v", err)
	}

	session := dbtest.NewSession(t, st, user.ID)

	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}

	return &editorFixture{
		server:   srv,
		store:    st,
		git:      git,
		project:  project,
		user:     user,
		repoPath: repoPath,
		session:  session,
	}
}

// post sends a request as the fixture's user.
func (f *editorFixture) post(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()

	router := f.server.Routes()
	url := "/projects/" + f.project.ID.String() + "/repository/files"

	request := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	// A save is a POST carrying the session cookie, so the request must look like
	// the same-origin browser request the API accepts.
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: "dogit_session", Value: f.session})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestEditorCommitsAFile(t *testing.T) {
	ctx := context.Background()
	f := newEditorFixture(t, false)

	before, err := f.git.RevParse(ctx, f.repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}

	recorder := f.post(t, `{"branch":"main","path":"cmd/main.go","content":"package main\n","message":"Add entry point"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	after, err := f.git.RevParse(ctx, f.repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Error("the branch did not move")
	}

	// The content must be readable back, which is the whole point of the save.
	content, _, _, err := f.git.CatFile(ctx, f.repoPath, "main", "cmd/main.go")
	if err != nil {
		t.Fatalf("read the committed file: %v", err)
	}
	if string(content) != "package main\n" {
		t.Errorf("content = %q", content)
	}

	// The commit belongs to the person who saved, not to the server account.
	commits, err := f.git.Log(ctx, f.repoPath, "main", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if commits[0].Subject != "Add entry point" {
		t.Errorf("subject = %q", commits[0].Subject)
	}
	if commits[0].AuthorEmail != f.user.Email {
		t.Errorf("author = %q, want the editor's own address", commits[0].AuthorEmail)
	}
	if len(commits[0].Parents) != 1 {
		t.Errorf("a save must not create a merge commit: %d parents", len(commits[0].Parents))
	}
}

// Editing a file someone else changed must be refused, not merged silently.
func TestEditorRefusesAStaleSave(t *testing.T) {
	ctx := context.Background()
	f := newEditorFixture(t, false)

	// The editor opens the file and remembers its blob.
	blob, err := f.git.RevParseBlob(ctx, f.repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}

	// Meanwhile a push changes the same file.
	_, tree, err := f.git.WriteBlobAndTree(ctx, f.repoPath, mustTree(t, f, "main"), "README.md", []byte("changed elsewhere\n"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := f.git.CommitTree(ctx, f.repoPath, gitx.CommitTreeOptions{
		Tree: tree, Message: "Change the file elsewhere",
		AuthorName: "Someone", AuthorEmail: "someone@example.test",
		CommitterName: "Someone", CommitterEmail: "someone@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	head, err := f.git.RevParse(ctx, f.repoPath, "main")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.git.UpdateRef(ctx, f.repoPath, "refs/heads/main", commit, head); err != nil {
		t.Fatal(err)
	}

	recorder := f.post(t, `{"branch":"main","path":"README.md","content":"my version\n","start_sha":"`+blob+`"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body = %s", recorder.Code, recorder.Body.String())
	}

	// Nothing may have been written.
	content, _, _, err := f.git.CatFile(ctx, f.repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "changed elsewhere\n" {
		t.Errorf("the file was overwritten: %q", content)
	}
}

// The escape hatch has to be explicit, and it has to work.
func TestEditorForceSavesOverAStaleFile(t *testing.T) {
	ctx := context.Background()
	f := newEditorFixture(t, false)

	blob, err := f.git.RevParseBlob(ctx, f.repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}

	recorder := f.post(t, `{"branch":"main","path":"README.md","content":"forced\n","start_sha":"`+blob+`","force":true}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	content, _, _, err := f.git.CatFile(ctx, f.repoPath, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "forced\n" {
		t.Errorf("content = %q", content)
	}
}

func TestEditorCommitsToANewBranch(t *testing.T) {
	ctx := context.Background()
	f := newEditorFixture(t, false)

	recorder := f.post(t, `{"new_branch":"feature/editor","path":"docs/idea.md","content":"notes\n","message":"Start the idea"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	if !strings.Contains(recorder.Body.String(), `"new_branch":true`) {
		t.Errorf("the response does not report the new branch: %s", recorder.Body.String())
	}
	if !f.git.Exists(ctx, f.repoPath, "feature/editor") {
		t.Fatal("the new branch was not created")
	}

	// The default branch must be untouched: the whole point of "commit to a new
	// branch" is that nothing moves on the shared one.
	content, _, _, err := f.git.CatFile(ctx, f.repoPath, "main", "docs/idea.md")
	if err == nil {
		t.Errorf("the default branch gained the file: %q", content)
	}
}

func TestEditorValidatesInput(t *testing.T) {
	f := newEditorFixture(t, false)

	cases := map[string]string{
		"no path":    `{"branch":"main","content":"x","message":"m"}`,
		"traversal":  `{"branch":"main","path":"../../escape.txt","content":"x"}`,
		"dotfile":    `{"branch":"main","path":".git/config","content":"x"}`,
		"bad branch": `{"branch":"bad..name","path":"a.txt","content":"x"}`,
	}

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := f.post(t, body)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

// A viewer may browse but must not write.
func TestEditorRequiresPushRights(t *testing.T) {
	ctx := context.Background()
	f := newEditorFixture(t, false)

	viewer := dbtest.NewUser(t, f.store, "viewer", false)
	dbtest.GrantRole(t, f.store, f.project.ID, viewer.ID, models.AccessLevelGuest, "Guest")
	session := dbtest.NewSession(t, f.store, viewer.ID)

	request := httptest.NewRequest(http.MethodPost,
		"/projects/"+f.project.ID.String()+"/repository/files",
		strings.NewReader(`{"branch":"main","path":"a.txt","content":"x"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: "dogit_session", Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)

	// Reported as missing rather than forbidden, so the response does not confirm
	// the project exists to someone without access.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}

	if f.git.Exists(ctx, f.repoPath, "feature/leak") {
		t.Error("a refused save still changed the repository")
	}
}

func mustTree(t *testing.T, f *editorFixture, rev string) string {
	t.Helper()
	tree, err := f.git.TreeOf(context.Background(), f.repoPath, rev)
	if err != nil {
		t.Fatalf("resolve tree of %s: %v", rev, err)
	}
	return tree
}
