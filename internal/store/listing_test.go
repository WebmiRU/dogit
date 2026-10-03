package store_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// The one list of where to work.
//
// Two things matter here and they pull against each other. The list has to be
// complete and paged, because it is the same page whatever the instance has grown
// to — and it must never show a person something they may not see, because a search
// that finds a private repository is a leak dressed as a convenience.

func setupListing(t *testing.T) (*store.Store, *models.User) {
	t.Helper()
	st := dbtest.Open(t)
	return st, dbtest.NewUser(t, st, "lister", false)
}

func paths(rows []store.Listing) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Path)
	}
	return out
}

func TestProjectsAndGroupsAreRowsOfTheSameList(t *testing.T) {
	st, user := setupListing(t)

	group := dbtest.NewGroup(t, st, "team")
	project := dbtest.NewProject(t, st, "shared-thing", nil)
	inside := dbtest.NewProject(t, st, "inside-group", &group.ID)

	// Rights are what make a place visible, and a project inside a group is visible to
	// the people in the group — which here means being given rights on it as well,
	// because the user is not a member of the group table.
	mustGrantGroup(t, st, group.ID, user.ID)
	mustGrantProject(t, st, project.ID, user.ID)
	mustGrantProject(t, st, inside.ID, user.ID)

	rows, total, err := st.ListVisible(context.Background(), user.ID, store.ListingQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 {
		t.Errorf("the list holds %d rows (%v), want 3", total, paths(rows))
	}

	kinds := map[string]string{}
	for _, row := range rows {
		kinds[row.Path] = row.Kind
	}
	if kinds[project.Path] != store.KindProject || kinds[inside.Path] != store.KindProject {
		t.Errorf("a project was not listed as one: %v", kinds)
	}
	if kinds[group.FullPath] != store.KindGroup {
		t.Errorf("a group was not listed as one: %v", kinds)
	}

	// A group says how many projects it holds; a project has no such number, because
	// the column would be meaningless rather than zero.
	for _, row := range rows {
		switch row.Kind {
		case store.KindGroup:
			if row.ProjectCount == nil {
				t.Error("a group does not say how many projects it holds")
			}
		case store.KindProject:
			if row.ProjectCount != nil {
				t.Errorf("a project reports %d projects", *row.ProjectCount)
			}
		}
	}
}

// A search narrows the list and never widens it.
func TestSearchNarrowsWithoutWidening(t *testing.T) {
	st, user := setupListing(t)

	mine := dbtest.NewProject(t, st, "billing-service", nil)
	theirs := dbtest.NewProject(t, st, "billing-service-legacy", nil)
	// The second one is private and the user is not in it.
	makePrivate(t, st, theirs.ID)
	mustGrantProject(t, st, mine.ID, user.ID)

	rows, total, err := st.ListVisible(context.Background(), user.ID,
		store.ListingQuery{Search: "billing"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("a search returned %d of %d rows (%v), want only the visible one",
			len(rows), total, paths(rows))
	}
	if rows[0].Path != mine.Path {
		t.Errorf("the search found %q", rows[0].Path)
	}
}

// The wildcards in a search are words, not patterns: somebody looking for "50%" is
// looking for a project called "50% done".
func TestWildcardsInASearchAreText(t *testing.T) {
	st, user := setupListing(t)

	half := dbtest.NewProject(t, st, "fifty-percent", nil)
	mustGrantProject(t, st, half.ID, user.ID)
	mustGrantProject(t, st, dbtest.NewProject(t, st, "anything", nil).ID, user.ID)

	rows, _, err := st.ListVisible(context.Background(), user.ID, store.ListingQuery{Search: "%"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a percent sign matched %d rows: %v", len(rows), paths(rows))
	}
}

// A page is a window, and the count beside it is the whole list rather than the window.
func TestPagingSaysHowMuchThereIs(t *testing.T) {
	st, user := setupListing(t)

	for _, name := range []string{"one-alpha", "two-beta", "three-gamma", "four-delta", "five-eps"} {
		mustGrantProject(t, st, dbtest.NewProject(t, st, name, nil).ID, user.ID)
	}

	first, total, err := st.ListVisible(context.Background(), user.ID,
		store.ListingQuery{PerPage: 2, Page: 1})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("the first page holds %d rows, want 2: %v", len(first), paths(first))
	}
	if total != 5 {
		t.Errorf("the count says %d, want the whole list (5)", total)
	}

	third, _, err := st.ListVisible(context.Background(), user.ID,
		store.ListingQuery{PerPage: 2, Page: 3})
	if err != nil {
		t.Fatalf("third page: %v", err)
	}
	if len(third) != 1 {
		t.Errorf("the last page holds %d rows, want 1", len(third))
	}

	// Pages do not overlap: the same row twice in one list reads as a bug in the list.
	if first[0].Path == third[0].Path {
		t.Errorf("row %q appears on both pages", first[0].Path)
	}
}

// Past the end is not the end: the count has to come back anyway, or the page control
// cannot say where the end is.
func TestAPagePastTheEndStillSaysHowMuchThereIs(t *testing.T) {
	st, user := setupListing(t)
	mustGrantProject(t, st, dbtest.NewProject(t, st, "only-one", nil).ID, user.ID)

	rows, total, err := st.ListVisible(context.Background(), user.ID,
		store.ListingQuery{PerPage: 20, Page: 9})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("a page past the end returned %v", paths(rows))
	}
	if total != 1 {
		t.Errorf("the count says %d on an empty page, want 1", total)
	}
}

// A page that is not a number is the first page rather than an error.
func TestAnAbsurdPageFallsBackToTheFirst(t *testing.T) {
	st, user := setupListing(t)
	mustGrantProject(t, st, dbtest.NewProject(t, st, "one", nil).ID, user.ID)

	rows, _, err := st.ListVisible(context.Background(), user.ID,
		store.ListingQuery{Page: -5, PerPage: 100000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("the list came back %v", paths(rows))
	}
}

// The access level is the same one the rest of the instance decides from, so a row
// never claims rights somebody does not have.
func TestEachRowSaysWhatTheReaderMayDoThere(t *testing.T) {
	st, user := setupListing(t)

	group := dbtest.NewGroup(t, st, "owners")
	mustGrantGroup(t, st, group.ID, user.ID, models.AccessLevelOwner)
	inside := dbtest.NewProject(t, st, "inside", &group.ID)
	mustGrantProject(t, st, inside.ID, user.ID)

	rows, _, err := st.ListVisible(context.Background(), user.ID, store.ListingQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	found := 0
	for _, row := range rows {
		if row.AccessLevel < 1 {
			t.Errorf("row %q says level %d, which is no rights at all", row.Path, row.AccessLevel)
		}
		found++
	}
	if found != 2 {
		t.Errorf("the list holds %d rows, want the group and the project", found)
	}
}

// mustGrantProject gives somebody rights on a project.
func mustGrantProject(t *testing.T, st *store.Store, projectID, userID uuid.UUID) {
	t.Helper()
	dbtest.GrantRole(t, st, projectID, userID, models.AccessLevelDeveloper, "developer")
}

// mustGrantGroup gives somebody rights in a group.
func mustGrantGroup(t *testing.T, st *store.Store, groupID, userID uuid.UUID, level ...int) {
	t.Helper()
	access := models.AccessLevelDeveloper
	if len(level) > 0 {
		access = level[0]
	}
	if _, err := st.Permissions().AssignGroupRole(context.Background(), groupID, "member",
		access, models.AccessLevelOwner, &userID, nil); err != nil {
		t.Fatalf("grant group rights: %v", err)
	}
}

// makePrivate hides a project from everybody who is not already in it.
func makePrivate(t *testing.T, st *store.Store, projectID uuid.UUID) {
	t.Helper()
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE projects SET visibility = 'private' WHERE id = $1`, projectID); err != nil {
		t.Fatalf("make it private: %v", err)
	}
}
