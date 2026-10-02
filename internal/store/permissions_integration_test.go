package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
)

// These tests exercise the permission queries against a real PostgreSQL.
//
// The queries are the part of the system that unit tests cannot reach: a wrong
// alias or a wrong join only shows up when the database parses it, and getting
// that wrong has broken access three times in a row. The suite therefore runs
// against a throwaway database when one is configured:
//
//	DOGIT_TEST_DATABASE_URL=postgres://dogit:dogit@localhost:5432/dogit_test?sslmode=disable
//
// It is skipped otherwise, so the ordinary suite stays runnable offline.

// openTestStore connects to the test database and gives each test its own isolated
// rows rather than truncating, so tests can run in any order.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	url := os.Getenv("DOGIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOGIT_TEST_DATABASE_URL is not set, skipping the database tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// The test database is created empty, so it has to be migrated before the suite
	// can run against it.
	if err := Migrate(url); err != nil {
		t.Fatalf("migrate the test database: %v", err)
	}

	st, err := Open(ctx, url, DefaultOptions())
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	t.Cleanup(st.Close)

	if _, err := st.Pool().Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("the test database is not migrated: %v", err)
	}
	return st
}

// newTestUser creates a user with a unique name so repeated runs do not collide.
func newTestUser(t *testing.T, st *Store, username string, admin bool) *models.User {
	t.Helper()

	user := &models.User{
		Username:     uniqueName(username),
		Email:        uniqueName(username) + "@example.test",
		Name:         username,
		PasswordHash: []byte("not-a-real-hash"),
		IsAdmin:      admin,
	}
	if err := st.Users().Create(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM users WHERE id = $1`, user.ID)
	})
	return user
}

func uniqueName(prefix string) string {
	return prefix + "-" + uuid.NewString()[:8]
}

func TestProjectAccessLevelsFromRoles(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	owner := newTestUser(t, st, "owner", false)
	member := newTestUser(t, st, "member", false)
	stranger := newTestUser(t, st, "stranger", false)

	project := &models.Project{
		Path: uniqueName("project"), Name: "project",
		Visibility: "private", DefaultBranch: "main", MergeMethod: "merge",
	}
	if err := st.Projects().Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM projects WHERE id = $1`, project.ID) })

	// Direct user role.
	if _, err := st.Permissions().AssignProjectRole(ctx, project.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &owner.ID, nil); err != nil {
		t.Fatalf("assign owner role: %v", err)
	}
	// A role granted to a group the member belongs to.
	group, err := st.Groups().Create(ctx, uniqueName("group"), "group")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID) })

	if err := st.Groups().AddMember(ctx, group.ID, member.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	if _, err := st.Permissions().AssignProjectRole(ctx, project.ID, "Developer",
		models.AccessLevelDeveloper, models.AccessLevelDeveloper, nil, &group.ID); err != nil {
		t.Fatalf("assign group role: %v", err)
	}

	cases := []struct {
		name string
		user *models.User
		want int
	}{
		{"owner holds owner", owner, models.AccessLevelOwner},
		{"member inherits developer", member, models.AccessLevelDeveloper},
		{"stranger has no access", stranger, NoAccess},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			level, err := st.Permissions().AccessLevel(ctx, tc.user.ID, project.ID)
			if err != nil {
				t.Fatalf("access level: %v", err)
			}
			if level != tc.want {
				t.Errorf("level = %d, want %d", level, tc.want)
			}
		})
	}
}

func TestCanEnforcesActionLevels(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	user := newTestUser(t, st, "developer", false)

	project := &models.Project{
		Path: uniqueName("project"), Name: "project",
		Visibility: "private", DefaultBranch: "main", MergeMethod: "merge",
		AllowMerge: true, AllowPipelineTrigger: true,
	}
	if err := st.Projects().Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM projects WHERE id = $1`, project.ID) })

	if _, err := st.Permissions().AssignProjectRole(ctx, project.ID, "Developer",
		models.AccessLevelDeveloper, models.AccessLevelDeveloper, &user.ID, nil); err != nil {
		t.Fatalf("assign role: %v", err)
	}

	allowed := []struct {
		action Action
		want   bool
	}{
		{ActionReadProject, true},
		{ActionPush, true},
		{ActionTriggerCI, true},
		{ActionMergeMR, false}, // maintainer territory
		{ActionManageProject, false},
	}

	for _, tc := range allowed {
		t.Run(string(tc.action), func(t *testing.T) {
			got, err := st.Permissions().Can(ctx, user, project, tc.action)
			if err != nil {
				t.Fatalf("can: %v", err)
			}
			if got != tc.want {
				t.Errorf("can %s = %v, want %v", tc.action, got, tc.want)
			}
		})
	}

	// A project-level flag must be able to withdraw a permission the level grants.
	project.AllowMerge = false
	if err := st.Projects().Update(ctx, project); err != nil {
		t.Fatalf("update project: %v", err)
	}

	_, _ = st.Permissions().AssignProjectRole(ctx, project.ID, "Maintainer",
		models.AccessLevelMaintainer, models.AccessLevelMaintainer, &user.ID, nil)

	canMerge, err := st.Permissions().Can(ctx, user, project, ActionMergeMR)
	if err != nil {
		t.Fatalf("can merge: %v", err)
	}
	if canMerge {
		t.Error("merging must be refused when the project disallows it, even for a maintainer")
	}
}

// The group level query is separate from the project one, and a broken alias in
// either of them only shows up here.
func TestGroupAccessLevel(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	owner := newTestUser(t, st, "groupowner", false)
	stranger := newTestUser(t, st, "groupstranger", false)

	group, err := st.Groups().Create(ctx, uniqueName("group"), "group")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID) })

	if _, err := st.Permissions().AssignGroupRole(ctx, group.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &owner.ID, nil); err != nil {
		t.Fatalf("assign group role: %v", err)
	}

	level, err := st.Permissions().GroupAccessLevel(ctx, owner.ID, group.ID)
	if err != nil {
		t.Fatalf("group access level: %v", err)
	}
	if level != models.AccessLevelOwner {
		t.Errorf("owner level = %d, want %d", level, models.AccessLevelOwner)
	}

	level, err = st.Permissions().GroupAccessLevel(ctx, stranger.ID, group.ID)
	if err != nil {
		t.Fatalf("group access level for a stranger: %v", err)
	}
	if level != NoAccess {
		t.Errorf("a stranger has level %d, want none", level)
	}
}

// Group roles must also reach projects inside the group.
func TestGroupRolesReachGroupProjects(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)

	member := newTestUser(t, st, "groupmember", false)

	group, err := st.Groups().Create(ctx, uniqueName("group"), "group")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID) })

	project := &models.Project{
		GroupID:       &group.ID,
		Path:          uniqueName("grouped"),
		Name:          "grouped",
		Visibility:    "private",
		DefaultBranch: "main",
		MergeMethod:   "merge",
	}
	if err := st.Projects().Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM projects WHERE id = $1`, project.ID) })

	if err := st.Groups().AddMember(ctx, group.ID, member.ID); err != nil {
		t.Fatalf("add member: %v", err)
	}
	if _, err := st.Permissions().AssignGroupRole(ctx, group.ID, "Developer",
		models.AccessLevelDeveloper, models.AccessLevelDeveloper, &member.ID, nil); err != nil {
		t.Fatalf("assign group role: %v", err)
	}

	level, err := st.Permissions().AccessLevel(ctx, member.ID, project.ID)
	if err != nil {
		t.Fatalf("access level: %v", err)
	}
	if level != models.AccessLevelDeveloper {
		t.Errorf("level = %d, want %d", level, models.AccessLevelDeveloper)
	}

	// The project must also be visible in the list, or the group role would grant
	// access to something nobody can find.
	visible, levels, err := st.Projects().ListVisible(ctx, member.ID, "")
	if err != nil {
		t.Fatalf("list visible: %v", err)
	}
	var found bool
	for _, p := range visible {
		if p.ID == project.ID {
			found = true
		}
	}
	if !found {
		t.Error("a project reachable through a group role is missing from the project list")
	}
	if got := levels[project.ID]; got != models.AccessLevelDeveloper {
		t.Errorf("listed level = %d, want %d", got, models.AccessLevelDeveloper)
	}
}
