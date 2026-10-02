// Package dbtest provides the database fixtures that repository and API tests
// share.
//
// It lives outside _test files so that several packages can use it: the permission
// rules and the HTTP layer are tested against the same kind of database, and a
// bug in a query that only one of them can reach is exactly the kind that ships.
package dbtest

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/store"
)

// URL returns the connection string for the test database, skipping the test when
// none is configured:
//
//	DOGIT_TEST_DATABASE_URL=postgres://dogit:dogit@localhost:5432/dogit_test?sslmode=disable
func URL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("DOGIT_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("DOGIT_TEST_DATABASE_URL is not set, skipping the database tests")
	}
	return url
}

// Open connects to the test database, migrating it first so a freshly created
// database is usable.
func Open(t *testing.T) *store.Store {
	t.Helper()

	url := URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := store.Migrate(url); err != nil {
		t.Fatalf("migrate the test database: %v", err)
	}

	st, err := store.Open(ctx, url, store.DefaultOptions())
	if err != nil {
		t.Fatalf("connect to the test database: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// Unique returns a name that will not collide with another run.
func Unique(prefix string) string {
	return prefix + "-" + uuid.NewString()[:8]
}

// NewUser creates a user and removes it when the test ends.
func NewUser(t *testing.T, st *store.Store, prefix string, admin bool) *models.User {
	t.Helper()

	user := &models.User{
		Username:     Unique(prefix),
		Email:        Unique(prefix) + "@example.test",
		Name:         prefix,
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

// NewProject creates a project and removes it when the test ends.
func NewProject(t *testing.T, st *store.Store, prefix string, groupID *uuid.UUID) *models.Project {
	t.Helper()

	project := &models.Project{
		GroupID:       groupID,
		Path:          Unique(prefix),
		Name:          prefix,
		Visibility:    "private",
		DefaultBranch: "main",
		MergeMethod:   "merge",
	}
	if err := st.Projects().Create(context.Background(), project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM projects WHERE id = $1`, project.ID)
	})
	return project
}

// GrantRole gives a user a role on a project.
func GrantRole(t *testing.T, st *store.Store, projectID, userID uuid.UUID, level int, name string) {
	t.Helper()

	if _, err := st.Permissions().AssignProjectRole(context.Background(), projectID,
		name, level, level, &userID, nil); err != nil {
		t.Fatalf("grant %s: %v", name, err)
	}
}

// NewSession creates a session cookie value for a user.
func NewSession(t *testing.T, st *store.Store, userID uuid.UUID) string {
	t.Helper()

	id := uuid.NewString()
	_, err := st.Pool().Exec(context.Background(), `
		INSERT INTO sessions (id, user_id, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
		id, userID)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	return id
}
