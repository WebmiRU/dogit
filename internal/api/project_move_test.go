package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
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

// moveFixture is a project with a repository on disk, ready to be moved.
type moveFixture struct {
	server   *Server
	store    *store.Store
	git      *gitx.Git
	repoRoot string
	project  *models.Project
	owner    *models.User
}

func newMoveFixture(t *testing.T, level int) *moveFixture {
	t.Helper()

	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	owner := dbtest.NewUser(t, st, "mover", false)
	project := dbtest.NewProject(t, st, "movable", nil)

	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	project.RepoPath = repo

	// A project role at a given level, for the cases that are about permissions.
	if level != 0 {
		dbtest.GrantRole(t, st, project.ID, owner.ID, level, "Role")
	} else {
		dbtest.GrantRole(t, st, project.ID, owner.ID, models.AccessLevelOwner, "Owner")
	}

	_, tree, err := git.WriteBlobAndTree(ctx, repo, "", "README.md", []byte("hello\n"))
	if err != nil {
		t.Fatal(err)
	}
	commit, err := git.CommitTree(ctx, repo, gitx.CommitTreeOptions{
		Tree: tree, Message: "Initial commit",
		AuthorName: "Seed", AuthorEmail: "seed@example.test",
		CommitterName: "Seed", CommitterEmail: "seed@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := git.UpdateRef(ctx, repo, "refs/heads/main", commit, ""); err != nil {
		t.Fatal(err)
	}

	return &moveFixture{
		server: &Server{
			cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
			log:   logger.Discard(),
			store: st,
			git:   git,
			repos: repos.New(st, git, repoRoot),
		},
		store: st, git: git, repoRoot: repoRoot, project: project, owner: owner,
	}
}

// asUser sends a request as a user with the given project role.
func (f *moveFixture) asUser(t *testing.T, user *models.User, level int, body string) *httptest.ResponseRecorder {
	t.Helper()

	if level != 0 {
		dbtest.GrantRole(t, f.store, f.project.ID, user.ID, level, "Role")
	}
	session := dbtest.NewSession(t, f.store, user.ID)

	request := httptest.NewRequest(http.MethodPost,
		"/projects/"+f.project.ID.String()+"/move", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	f.server.Routes().ServeHTTP(recorder, request)
	return recorder
}

func (f *moveFixture) asOwner(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return f.asUser(t, f.owner, 0, body)
}

// Moving into a group moves the repository too: the path is where it lives.
func TestMoveProjectIntoAGroup(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, models.AccessLevelOwner)
	name := lastSegmentOf(f.project.Path)

	group, err := f.store.Groups().Create(ctx, dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})

	// The mover has to own the group as well as the project.
	if _, err := f.store.Permissions().AssignGroupRole(ctx, group.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &f.owner.ID, nil); err != nil {
		t.Fatalf("grant the group role: %v", err)
	}

	recorder := f.asOwner(t, `{"group_path":"`+group.FullPath+`"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("move: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), group.FullPath+"/"+name) {
		t.Errorf("the response does not carry the new path: %s", recorder.Body.String())
	}

	// The row follows.
	stored, err := f.store.Projects().ByID(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Path != group.FullPath+"/"+name {
		t.Errorf("path = %q", stored.Path)
	}
	if stored.GroupID == nil || *stored.GroupID != group.ID {
		t.Errorf("the project is not in the group")
	}

	// And so does the directory: a row that moved while the history did not is a
	// project nobody can open.
	movedDir := filepath.Join(f.repoRoot, group.FullPath, name+".git")
	if _, err := f.git.Log(ctx, movedDir, "main", 1, 0); err != nil {
		t.Fatalf("the repository did not move: %v", err)
	}
	oldDir := filepath.Join(f.repoRoot, f.project.Path+".git")
	if _, err := f.git.Log(ctx, oldDir, "main", 1, 0); err == nil {
		t.Error("the repository is still at the old address")
	}

	// The push hook reads the path out of the repository's own configuration, so
	// a move without updating it would report every push under the old name.
	configured, err := f.git.GetConfig(ctx, movedDir, "dogit.projectpath")
	if err != nil {
		t.Fatalf("read dogit.projectpath: %v", err)
	}
	if strings.TrimSpace(configured) != group.FullPath+"/"+name {
		t.Errorf("dogit.projectpath = %q", strings.TrimSpace(configured))
	}
}

// The other direction: a project leaves its group and becomes a top-level one.
func TestMoveProjectOutOfAGroup(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, models.AccessLevelOwner)
	name := lastSegmentOf(f.project.Path)

	group, err := f.store.Groups().Create(ctx, dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})
	if _, err := f.store.Permissions().AssignGroupRole(ctx, group.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &f.owner.ID, nil); err != nil {
		t.Fatal(err)
	}

	if recorder := f.asOwner(t, `{"group_path":"`+group.FullPath+`"}`); recorder.Code != http.StatusOK {
		t.Fatalf("into the group: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if recorder := f.asOwner(t, `{"group_path":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("out of the group: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	stored, err := f.store.Projects().ByID(ctx, f.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Path != name {
		t.Errorf("path = %q, want the bare name %q", stored.Path, name)
	}
	if stored.GroupID != nil {
		t.Error("the project is still in the group")
	}
	if _, err := f.git.Log(ctx, filepath.Join(f.repoRoot, name+".git"), "main", 1, 0); err != nil {
		t.Errorf("the repository is not at the top level: %v", err)
	}
}

// A move is an owner's decision, on the project and on the group.
func TestMoveProjectNeedsOwnerOnBothSides(t *testing.T) {
	ctx := context.Background()

	group, err := dbtest.Open(t).Groups().Create(ctx, dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	t.Run("a maintainer cannot move a project", func(t *testing.T) {
		f := newMoveFixture(t, models.AccessLevelMaintainer)

		maintainer := dbtest.NewUser(t, f.store, "maint", false)
		dbtest.GrantRole(t, f.store, f.project.ID, maintainer.ID, models.AccessLevelMaintainer, "Maintainer")

		recorder := f.asUser(t, maintainer, 0, `{"group_path":"`+group.FullPath+`"}`)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403; body %s", recorder.Code, recorder.Body.String())
		}
		// A refused move must leave the repository where it was.
		if !f.git.Exists(ctx, filepath.Join(f.repoRoot, f.project.Path+".git"), "main") {
			t.Error("a refused move moved the repository anyway")
		}
	})

	t.Run("a project owner cannot move it into a group they do not own", func(t *testing.T) {
		f := newMoveFixture(t, models.AccessLevelOwner)

		stranger := dbtest.NewUser(t, f.store, "outsider", false)
		dbtest.GrantRole(t, f.store, f.project.ID, stranger.ID, models.AccessLevelOwner, "Owner")

		recorder := f.asUser(t, stranger, 0, `{"group_path":"`+group.FullPath+`"}`)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("status %d, want 403; body %s", recorder.Code, recorder.Body.String())
		}
	})
}

// Two projects cannot end up at one address.
func TestMoveProjectRefusesAnOccupiedAddress(t *testing.T) {
	ctx := context.Background()
	f := newMoveFixture(t, models.AccessLevelOwner)

	group, err := f.store.Groups().Create(ctx, dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.store.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})
	if _, err := f.store.Permissions().AssignGroupRole(ctx, group.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &f.owner.ID, nil); err != nil {
		t.Fatal(err)
	}

	// Someone else's project already lives at that address in that group.
	name := lastSegmentOf(f.project.Path)
	squatter := dbtest.NewProject(t, st(t, f), name, &group.ID)
	squatter.Path = group.FullPath + "/" + name
	occupied := filepath.Join(f.repoRoot, group.FullPath, name+".git")
	if err := f.git.InitBare(ctx, occupied); err != nil {
		t.Fatal(err)
	}
	_ = squatter

	recorder := f.asOwner(t, `{"group_path":"`+group.FullPath+`"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409; body %s", recorder.Code, recorder.Body.String())
	}
	if !f.git.Exists(ctx, filepath.Join(f.repoRoot, name+".git"), "main") {
		t.Error("a refused move still moved the repository")
	}
	if !f.git.Exists(ctx, occupied, "HEAD") && !fileExists(occupied) {
		t.Error("the occupied address was taken over")
	}
}

// st returns the fixture's store, for the helpers that create their own rows.
func st(t *testing.T, f *moveFixture) *store.Store { return f.store }

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// A group that does not exist is reported as such, and nothing moves.
func TestMoveProjectIntoAMissingGroup(t *testing.T) {
	f := newMoveFixture(t, models.AccessLevelOwner)

	recorder := f.asOwner(t, `{"group_path":"no-such-group"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404; body %s", recorder.Code, recorder.Body.String())
	}
}
