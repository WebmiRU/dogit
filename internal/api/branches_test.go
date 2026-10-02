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
)

// A slash is legal inside a branch name, so every client percent-encodes it. The
// server has to decode it back: otherwise "feature/thing" arrives as
// "feature%2Fthing", which is not a valid reference name, and deleting such a
// branch fails with an error about the name rather than about the branch.
func TestBranchNamesWithSlashesSurviveTheRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	user := dbtest.NewUser(t, st, "slashy", false)
	project := dbtest.NewProject(t, st, "slashy", nil)

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
	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour, SSHHost: "localhost"},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}
	router := srv.Routes()

	send := func(method, path string) int {
		request := httptest.NewRequest(method, path, strings.NewReader(""))
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Code
	}

	base := "/projects/" + project.ID.String() + "/repository"

	// Created exactly as the browser does it, with the slash encoded.
	body := strings.NewReader(`{"name":"feature/api-pagination","start_point":"main"}`)
	request := httptest.NewRequest(http.MethodPost, base+"/branches", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create: status %d, body %s", recorder.Code, recorder.Body.String())
	}
	if !git.Exists(ctx, repo, "feature/api-pagination") {
		t.Fatal("the branch was not created")
	}

	// And now it is deleted the same way it was created.
	if status := send(http.MethodDelete, base+"/branches/feature%2Fapi-pagination"); status != http.StatusNoContent {
		t.Fatalf("delete: status %d, want 204", status)
	}
	if git.Exists(ctx, repo, "feature/api-pagination") {
		t.Error("the branch is still there after a successful delete")
	}
}
