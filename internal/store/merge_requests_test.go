package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// TestMergeRequestNumbersArePerProject pins the behaviour a person relies on
// without noticing: the first merge request in a project is !1, and a second
// project starts counting from one again rather than continuing the first
// project's sequence.
func TestMergeRequestNumbersArePerProject(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	author := dbtest.NewUser(t, st, "mr-author", false)
	first := dbtest.NewProject(t, st, "mr-one", nil)
	second := dbtest.NewProject(t, st, "mr-two", nil)

	create := func(project *models.Project, title string) *models.MergeRequest {
		t.Helper()

		mr, err := st.MergeRequests().Create(ctx, store.CreateParams{
			ProjectID: project.ID, AuthorID: author.ID,
			SourceBranch: "feature/" + title, TargetBranch: "main",
			Title: title, SHA: "abc123",
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return mr
	}

	one := create(first, "First")
	two := create(first, "Second")
	other := create(second, "Elsewhere")

	if one.IID != 1 {
		t.Errorf("the first request in a project has iid %d, want 1", one.IID)
	}
	if two.IID != 2 {
		t.Errorf("the second request has iid %d, want 2", two.IID)
	}
	if other.IID != 1 {
		t.Errorf("a request in another project has iid %d, want 1", other.IID)
	}
	if one.State != models.MRStateOpened {
		t.Errorf("state = %q, want opened", one.State)
	}
}

// A merged request must not be mergeable again: two people pressing the button
// at once should produce one merge, not two.
func TestMergeRequestMergesExactlyOnce(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	author := dbtest.NewUser(t, st, "mr-merger", false)
	merger := dbtest.NewUser(t, st, "mr-merger2", false)
	project := dbtest.NewProject(t, st, "mr-merge", nil)

	mr, err := st.MergeRequests().Create(ctx, store.CreateParams{
		ProjectID: project.ID, AuthorID: author.ID,
		SourceBranch: "feature/x", TargetBranch: "main", Title: "Work", SHA: "deadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}

	merged, err := st.MergeRequests().MarkMerged(ctx, mr.ID, "c0ffee", merger.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if merged.State != models.MRStateMerged {
		t.Errorf("state = %q, want merged", merged.State)
	}
	if merged.MergeCommitSHA == nil || *merged.MergeCommitSHA != "c0ffee" {
		t.Errorf("merge commit = %v, want c0ffee", merged.MergeCommitSHA)
	}
	if merged.MergedAt == nil {
		t.Error("merged_at was not recorded")
	}

	// The second attempt has to be refused, not quietly overwrite the first.
	if _, err := st.MergeRequests().MarkMerged(ctx, mr.ID, "other", merger.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("merging twice returned %v, want not found", err)
	}

	// And a merged request cannot be reopened, because its commits are already on
	// the target branch.
	if _, err := st.MergeRequests().Reopen(ctx, mr.ID); !errors.Is(err, store.ErrConflict) {
		t.Errorf("reopening a merged request returned %v, want conflict", err)
	}
}

func TestMergeRequestClosesAndReopens(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	author := dbtest.NewUser(t, st, "mr-closer", false)
	project := dbtest.NewProject(t, st, "mr-close", nil)

	mr, err := st.MergeRequests().Create(ctx, store.CreateParams{
		ProjectID: project.ID, AuthorID: author.ID,
		SourceBranch: "feature/y", TargetBranch: "main", Title: "Work", SHA: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}

	closed, err := st.MergeRequests().MarkClosed(ctx, mr.ID)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.State != models.MRStateClosed {
		t.Errorf("state = %q, want closed", closed.State)
	}
	if closed.ClosedAt == nil {
		t.Error("closed_at was not recorded")
	}

	reopened, err := st.MergeRequests().Reopen(ctx, mr.ID)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.IsOpen() {
		t.Errorf("state = %q, want opened", reopened.State)
	}
	if reopened.ClosedAt != nil {
		t.Error("closed_at survived the reopen")
	}
}

// Editing one field must not blank out the others: a person who changes the
// title should keep the description they wrote earlier.
func TestMergeRequestEditLeavesOtherFieldsAlone(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	author := dbtest.NewUser(t, st, "mr-editor", false)
	project := dbtest.NewProject(t, st, "mr-edit", nil)

	mr, err := st.MergeRequests().Create(ctx, store.CreateParams{
		ProjectID: project.ID, AuthorID: author.ID,
		SourceBranch: "feature/z", TargetBranch: "main",
		Title: "Original title", Description: "A description worth keeping", SHA: "abc",
	})
	if err != nil {
		t.Fatal(err)
	}

	newTitle := "Better title"
	updated, err := st.MergeRequests().Update(ctx, mr.ID, store.UpdateFields{Title: &newTitle})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	if updated.Title != newTitle {
		t.Errorf("title = %q, want %q", updated.Title, newTitle)
	}
	if updated.Description != "A description worth keeping" {
		t.Errorf("description = %q, the edit wiped it", updated.Description)
	}
}

// The listing is what the sidebar and the merge request page both read, so it
// has to carry the author and the project without extra round trips.
func TestMergeRequestListCarriesAuthorAndProject(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	author := dbtest.NewUser(t, st, "mr-lister", false)
	project := dbtest.NewProject(t, st, "mr-list", nil)
	other := dbtest.NewProject(t, st, "mr-list-other", nil)

	for _, branch := range []string{"a", "b"} {
		if _, err := st.MergeRequests().Create(ctx, store.CreateParams{
			ProjectID: project.ID, AuthorID: author.ID,
			SourceBranch: "feature/" + branch, TargetBranch: "main",
			Title: "Request " + branch, SHA: "abc",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.MergeRequests().Create(ctx, store.CreateParams{
		ProjectID: other.ID, AuthorID: author.ID,
		SourceBranch: "feature/c", TargetBranch: "main", Title: "Elsewhere", SHA: "abc",
	}); err != nil {
		t.Fatal(err)
	}

	list, err := st.MergeRequests().List(ctx, store.ListFilter{ProjectID: &project.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d requests, want only the project's own 2", len(list))
	}

	for _, mr := range list {
		if mr.AuthorUsername != author.Username {
			t.Errorf("author = %q, want %q", mr.AuthorUsername, author.Username)
		}
		if mr.Project == nil || mr.Project.Path != project.Path {
			t.Errorf("the project was not joined in: %+v", mr.Project)
		}
	}

	count, err := st.MergeRequests().CountOpen(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("open count = %d, want 2", count)
	}
}

// Branch names are what a person sees first, so the generated title has to be
// readable without editing it.
func TestTitleFromBranch(t *testing.T) {
	cases := map[string]string{
		"feature/add-the-widget":   "Add The Widget",
		"fix_login":                "Fix Login",
		"main":                     "Main",
		"feature/deep/nested/name": "Name",
	}

	for branch, want := range cases {
		if got := store.TitleFromBranch(branch); got != want {
			t.Errorf("TitleFromBranch(%q) = %q, want %q", branch, got, want)
		}
	}
}

var _ = uuid.Nil
