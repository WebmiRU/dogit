// The tests live in an external test package so they can use dbtest, which
// imports store itself.
package store_test

import (
	"context"
	"testing"

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
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

func TestProjectAccessLevelsFromRoles(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	owner := dbtest.NewUser(t, st, "owner", false)
	member := dbtest.NewUser(t, st, "member", false)
	stranger := dbtest.NewUser(t, st, "stranger", false)

	project := dbtest.NewProject(t, st, "project", nil)

	// Direct user role.
	if _, err := st.Permissions().AssignProjectRole(ctx, project.ID, "Owner",
		models.AccessLevelOwner, models.AccessLevelOwner, &owner.ID, nil); err != nil {
		t.Fatalf("assign owner role: %v", err)
	}
	// A role granted to a group the member belongs to.
	group, err := st.Groups().Create(ctx, dbtest.Unique("group"), "group")
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
		{"stranger has no access", stranger, store.NoAccess},
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
	st := dbtest.Open(t)

	user := dbtest.NewUser(t, st, "developer", false)

	project := dbtest.NewProject(t, st, "project", nil)
	project.AllowMerge = true
	project.AllowPipelineTrigger = true
	if err := st.Projects().Update(ctx, project); err != nil {
		t.Fatalf("update project: %v", err)
	}

	if _, err := st.Permissions().AssignProjectRole(ctx, project.ID, "Developer",
		models.AccessLevelDeveloper, models.AccessLevelDeveloper, &user.ID, nil); err != nil {
		t.Fatalf("assign role: %v", err)
	}

	allowed := []struct {
		action store.Action
		want   bool
	}{
		{store.ActionReadProject, true},
		{store.ActionPush, true},
		{store.ActionTriggerCI, true},
		{store.ActionMergeMR, false}, // maintainer territory
		{store.ActionManageProject, false},
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

	canMerge, err := st.Permissions().Can(ctx, user, project, store.ActionMergeMR)
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
	st := dbtest.Open(t)

	owner := dbtest.NewUser(t, st, "groupowner", false)
	stranger := dbtest.NewUser(t, st, "groupstranger", false)

	group, err := st.Groups().Create(ctx, dbtest.Unique("group"), "group")
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
	if level != store.NoAccess {
		t.Errorf("a stranger has level %d, want none", level)
	}
}

// Group roles must also reach projects inside the group.
func TestGroupRolesReachGroupProjects(t *testing.T) {
	ctx := context.Background()
	st := dbtest.Open(t)

	member := dbtest.NewUser(t, st, "groupmember", false)

	group, err := st.Groups().Create(ctx, dbtest.Unique("group"), "group")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	t.Cleanup(func() { _, _ = st.Pool().Exec(ctx, `DELETE FROM groups WHERE id = $1`, group.ID) })

	project := dbtest.NewProject(t, st, "grouped", &group.ID)

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
