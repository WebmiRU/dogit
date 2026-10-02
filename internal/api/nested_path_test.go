package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ewolf/dogit/internal/config"
	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/gitx"
	"github.com/ewolf/dogit/internal/logger"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/repos"
)

// nestedFixture is a project that lives inside a group, so its path carries a
// slash: "platform/api".
func TestNestedProjectPathResolves(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	owner := dbtest.NewUser(t, st, "nested-owner", false)

	group, err := st.Groups().Create(ctx, dbtest.Unique("platform"), "Platform")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID)
	})

	project := dbtest.NewProject(t, st, "api", &group.ID)

	// The repository path is derived from the project path rather than stored, so
	// the bare repository has to sit exactly where the service looks for it.
	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	project.RepoPath = repo
	dbtest.GrantRole(t, st, project.ID, owner.ID, models.AccessLevelOwner, "Owner")

	// A commit, so the repository routes have something to serve.
	_, tree, err := git.WriteBlobAndTree(ctx, repo, "", "README.md", []byte("hi\n"))
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

	session := dbtest.NewSession(t, st, owner.ID)

	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}
	// The routes are registered the way the web server registers them: inside a
	// group mounted at /api/v1. Mounting changes how chi resolves the patterns, and
	// the shadowing that hid the grouped project only showed up under that mount —
	// a test against the bare router passed while every real request failed.
	router := chi.NewRouter()
	router.Route("/api/v1", func(v1 chi.Router) { srv.Register(v1) })

	// The project itself, the branch list and a file: the three shapes of request
	// the frontend makes when a project sits under a group.
	cases := []struct {
		name string
		path string
		want string
	}{
		{
			name: "project",
			path: "/api/v1/projects/" + project.Path,
			want: `"path":"` + project.Path + `"`,
		},
		{
			name: "refs",
			path: "/api/v1/projects/" + project.Path + "/repository/refs",
			want: `"branches"`,
		},
		{
			name: "file",
			path: "/api/v1/projects/" + project.Path + "/repository/file?ref=main&path=README.md",
			want: `"content":"hi\n"`,
		},
		// A static tail under the parameter. The grouped-project dispatcher has to
		// sit in NotFound rather than in a wildcard, because a wildcard competes
		// with the parameter routes and silently takes "/compare" away from them.
		{
			name: "compare",
			path: "/api/v1/projects/" + project.Path + "/repository/compare?from=main&to=main",
			want: `"files"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.path, nil)
			request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), tc.want) {
				t.Errorf("body does not contain %s: %s", tc.want, recorder.Body.String())
			}
		})
	}

	// A path that resolves to nothing must not fall through to the project
	// handlers, and must not say anything about projects that do exist.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+group.Slug+"/missing", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404; body = %s", recorder.Code, recorder.Body.String())
	}
}
