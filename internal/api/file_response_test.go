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
)

// TestFileResponseIdentifiesTheFileAndItsLastChange pins down the two object ids
// the file view returns, because the editor writes one of them back on save.
//
// The bug this guards against is quiet: a file view that reports the branch tip
// instead of the blob makes every save look like a conflict, and the editor is
// then unusable in exactly the repositories people work in.
func TestFileResponseIdentifiesTheFileAndItsLastChange(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)
	git := gitx.New(gitx.Options{})

	user := dbtest.NewUser(t, st, "fileview", false)
	project := dbtest.NewProject(t, st, "fileview", nil)

	repoRoot := t.TempDir()
	repo := filepath.Join(repoRoot, project.Path+".git")
	if err := git.InitBare(ctx, repo); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	project.RepoPath = repo
	dbtest.GrantRole(t, st, project.ID, user.ID, models.AccessLevelOwner, "Owner")

	commitFile := func(message, path, content, parent string) string {
		t.Helper()

		var err error
		tree := ""
		if parent != "" {
			var resolved string
			resolved, err = git.TreeOf(ctx, repo, parent)
			if err != nil {
				t.Fatalf("tree of %q: %v", parent, err)
			}
			tree = resolved
		}
		_, tree, err = git.WriteBlobAndTree(ctx, repo, tree, path, []byte(content))
		if err != nil {
			t.Fatalf("write %s: %v", path, err)
		}

		parents := []string{}
		if parent != "" {
			parents = []string{parent}
		}
		commit, err := git.CommitTree(ctx, repo, gitx.CommitTreeOptions{
			Tree: tree, Parents: parents, Message: message,
			AuthorName: "Seed", AuthorEmail: "seed@example.test",
			CommitterName: "Seed", CommitterEmail: "seed@example.test",
		})
		if err != nil {
			t.Fatalf("commit %s: %v", message, err)
		}
		if err := git.UpdateRef(ctx, repo, "refs/heads/main", commit, parent); err != nil {
			t.Fatalf("update ref: %v", err)
		}
		return commit
	}

	// Two files, so the second commit moves the branch without touching the first.
	first := commitFile("Add both files", "README.md", "readme\n", "")
	commitFile("Add a second file", "other.txt", "other\n", first)
	// A third commit that changes only README.md, which is what the view reports.
	touched := commitFile("Edit the readme", "README.md", "readme, edited\n", mustHead(t, git, repo, "main"))
	// A commit after it that touches another file: now the branch tip and the last
	// change to README.md are different commits, which is the case worth testing.
	commitFile("Edit the other file", "other.txt", "other, edited\n", touched)

	session := dbtest.NewSession(t, st, user.ID)
	srv := &Server{
		cfg:   &config.Config{AuthTokenTTL: time.Hour},
		log:   logger.Discard(),
		store: st,
		git:   git,
		repos: repos.New(st, git, repoRoot),
	}

	get := func(path string) fileResponse {
		t.Helper()

		request := httptest.NewRequest(http.MethodGet,
			"/projects/"+project.ID.String()+"/repository/file?ref=main&path="+path, nil)
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

		recorder := httptest.NewRecorder()
		srv.Routes().ServeHTTP(recorder, request)

		if recorder.Code != http.StatusOK {
			t.Fatalf("reading %s: status %d, body %s", path, recorder.Code, recorder.Body.String())
		}
		var response fileResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return response
	}

	readme := get("README.md")

	wantBlob, err := git.RevParseBlob(ctx, repo, "main", "README.md")
	if err != nil {
		t.Fatal(err)
	}
	if readme.SHA != wantBlob {
		t.Errorf("sha = %s, want the blob id %s", readme.SHA, wantBlob)
	}

	// The last commit for this path, not the branch tip: the tip is a later commit
	// that changed a different file.
	branchTip := mustHead(t, git, repo, "main")
	if readme.CommitSHA == branchTip {
		t.Errorf("last_commit_sha = %s, which is the branch tip rather than the commit that changed the file", branchTip)
	}
	if readme.CommitSHA != touched {
		t.Errorf("last_commit_sha = %s, want %s", readme.CommitSHA, touched)
	}

	// The save path uses the blob id, so the round trip has to work end to end:
	// reading a file and saving it unchanged is not a conflict.
	request := httptest.NewRequest(http.MethodPost,
		"/projects/"+project.ID.String()+"/repository/files",
		strings.NewReader(`{"branch":"main","path":"README.md","content":"readme, edited\nnext\n","message":"Continue","start_sha":"`+readme.SHA+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder := httptest.NewRecorder()
	srv.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("saving a file read from the API: status %d, body %s", recorder.Code, recorder.Body.String())
	}

	// And the same save from a stale copy is refused.
	request = httptest.NewRequest(http.MethodPost,
		"/projects/"+project.ID.String()+"/repository/files",
		strings.NewReader(`{"branch":"main","path":"README.md","content":"mine\n","message":"Stale","start_sha":"`+readme.SHA+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})

	recorder = httptest.NewRecorder()
	srv.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("saving from a stale copy: status %d, want 409; body %s", recorder.Code, recorder.Body.String())
	}
}

func mustHead(t *testing.T, git *gitx.Git, repo, ref string) string {
	t.Helper()

	sha, err := git.RevParse(context.Background(), repo, ref)
	if err != nil {
		t.Fatalf("resolve %s: %v", ref, err)
	}
	return sha
}
