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

	"github.com/ewolf/dogit/internal/auth"
	"github.com/ewolf/dogit/internal/models"
	"github.com/ewolf/dogit/internal/secrets"
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
//
// With a sealer, from DOGIT_SECRET_KEY, defaulting to a throwaway key. Without one a test that
// writes anything sealed is refused — which is the correct behaviour and makes such a test
// useless: it fails about the missing key rather than about the thing it was written to check.
// The default is not used when the variable is set, so a test run against a real instance's key
// seals with the real key and is refused rather than writing something the real key cannot open.
func Open(t *testing.T) *store.Store {
	t.Helper()

	url := URL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := store.Migrate(url); err != nil {
		t.Fatalf("migrate the test database: %v", err)
	}

	key := os.Getenv("DOGIT_SECRET_KEY")
	if key == "" {
		// 32 bytes, base64. Written out rather than generated, so that a test which needs the value
		// for itself — to check that a sealed column is sealed with it, say — has it to hand.
		key = "ZG9naXQtdGVzdC1vbmx5LWtleS0zMi1ieXRlcyEhISE="
	}
	sealer, err := secrets.New(key)
	if err != nil {
		t.Fatalf("a throwaway key was refused: %v", err)
	}

	options := store.DefaultOptions()
	options.Sealer = sealer

	st, err := store.Open(ctx, url, options)
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

// NewUserWithPassword creates a user whose password is actually set.
//
// Some tests need to log in rather than to hold a session: anything that goes
// through the API's own credential check, such as a registry client signing in.
func NewUserWithPassword(t *testing.T, st *store.Store, prefix, password string, admin bool) *models.User {
	t.Helper()

	user := NewUser(t, st, prefix, admin)

	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash the password: %v", err)
	}
	if _, err := st.Pool().Exec(context.Background(),
		`UPDATE users SET password_hash = $2 WHERE id = $1`, user.ID, hash); err != nil {
		t.Fatalf("set the password: %v", err)
	}
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

// NewGroup creates a group and removes it when the test ends.
//
// Groups hold projects of their own, so anything about how a group passes settings
// down to them needs one.
func NewGroup(t *testing.T, st *store.Store, prefix string) *models.Group {
	t.Helper()

	group, err := st.Groups().Create(context.Background(), Unique(prefix), prefix)
	if err != nil {
		t.Fatalf("create group: %v", err)
	}

	t.Cleanup(func() {
		_, _ = st.Pool().Exec(context.Background(), `DELETE FROM groups WHERE id = $1`, group.ID)
	})
	return group
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
