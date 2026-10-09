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
	"github.com/ewolf/dogit/internal/repos"
)

// A user with no display name must still be able to create a project.
//
// git refuses a commit with an empty identity, so the seed silently produced an
// empty repository: the project existed, the API answered, and every read of it
// came back blank with no error anywhere the user could see.
func TestCreatingAProjectAlwaysSeedsACommit(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	creator := dbtest.NewUser(t, st, "nameless", false)
	// Not everyone fills in a display name, and git refuses a commit whose
	// identity is empty.
	if _, err := st.Pool().Exec(ctx, `UPDATE users SET name = '' WHERE id = $1`, creator.ID); err != nil {
		t.Fatalf("clear the display name: %v", err)
	}
	creator.Name = ""
	if creator.Name != "" {
		t.Fatalf("the fixture needs a user with no display name, got %q", creator.Name)
	}

	repoRoot := t.TempDir()
	session := dbtest.NewSession(t, st, creator.ID)

	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}

	request := httptest.NewRequest(http.MethodPost, "/projects", strings.NewReader(`{
		"path": "`+dbtest.Unique("seeded")+`",
		"name": "Seeded",
		"visibility": "private",
		"initialize_with_readme": true
	}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	srv.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	project, _, err := st.Projects().ListVisible(ctx, creator.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(project) != 1 {
		t.Fatalf("expected exactly one project, found %d", len(project))
	}
	created := project[0]

	repoDir := filepath.Join(repoRoot, created.Path+".git")
	commits, err := git.Log(ctx, repoDir, "main", 5, 0)
	if err != nil {
		t.Fatalf("read the seeded history: %v", err)
	}
	if len(commits) != 1 {
		t.Fatalf("the repository has %d commits, want the seeded one", len(commits))
	}
	if commits[0].AuthorName != creator.Username {
		t.Errorf("author = %q, want the login %q", commits[0].AuthorName, creator.Username)
	}
}
