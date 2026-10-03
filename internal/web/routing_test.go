package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ewolf/dogit/internal/api"
	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/objects"
	"github.com/ewolf/dogit/internal/repos"
	"github.com/ewolf/dogit/internal/web"
)

// TestGroupedProjectIsRoutableThroughTheServer drives the whole HTTP stack, the
// way a request from nginx arrives.
//
// Mounting the API under /api/v1 changes how chi resolves the route patterns
// beneath it, and that is where a grouped project used to disappear: it answered
// 404 while a test against the API's own router passed. Every request here goes
// through the real server, so the path a user types is the path under test.
func TestGroupedProjectIsRoutableThroughTheServer(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	user := dbtest.NewUser(t, st, "routed", false)
	group, err := st.Groups().Create(ctx, dbtest.Unique("team"), "Team")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})

	project := dbtest.NewProject(t, st, "service", &group.ID)
	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	project.RepoPath = repo
	dbtest.GrantRole(t, st, project.ID, user.ID, models.AccessLevelOwner, "Owner")

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

	session := dbtest.NewSession(t, st, user.ID)

	objects, err := objects.NewLocal(t.TempDir(), "test")
	if err != nil {
		t.Fatalf("object store: %v", err)
	}

	apiServer := api.New(&config.Config{AuthTokenTTL: time.Hour}, logger.Discard(), st, git,
		repos.New(st, git, repoRoot), nil, objects)
	server := web.NewServer(&config.Config{}, logger.Discard(), st, apiServer)

	cases := []struct {
		name string
		path string
	}{
		{name: "project", path: "/api/v1/projects/" + project.Path},
		{name: "refs", path: "/api/v1/projects/" + project.Path + "/repository/refs"},
		{name: "file", path: "/api/v1/projects/" + project.Path + "/repository/file?ref=main&path=README.md"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.AddCookie(&http.Cookie{Name: "dogit_session", Value: session})

			recorder := httptest.NewRecorder()
			server.Routes().ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}
