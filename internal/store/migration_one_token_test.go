package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/ewolf/dogit/internal/dbtest"
	"github.com/ewolf/dogit/internal/store"
	"github.com/ewolf/dogit/migrations"
)

// migrateTo applies migrations up to a version and stops there.
//
// Needed because the question in this file cannot be asked any other way: whether a module that
// was already running keeps working. Such a module only exists at version 32 — the column its
// credential lives in has since been dropped, so there is nothing left to insert one into.
func migrateTo(t *testing.T, url string, version uint) {
	t.Helper()

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database for migrations: %v", err)
	}
	defer db.Close()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{
		MigrationsTable: migratepgx.DefaultMigrationsTable,
	})
	if err != nil {
		t.Fatalf("init migration driver: %v", err)
	}
	source, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("open migration source: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", source, "pgx5", driver)
	if err != nil {
		t.Fatalf("build migrator: %v", err)
	}

	if err := m.Migrate(version); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		t.Fatalf("migrate to %d: %v", version, err)
	}
}

// TestAModuleRunningBeforeTheOneTokenChangeKeepsWorking decides whether this migration can be
// shipped at all.
//
// A module on an instance being upgraded is holding a credential that exists nowhere else: the
// core issued it at registration, stored it on the module's own row, and the administrator who
// created the token the module registered with never saw it and cannot reissue it. So if the
// migration moves that credential anywhere without carrying it, the module is locked out of an
// instance it was working on a minute ago, and the only way back is for somebody to rebuild and
// redeploy it by hand.
//
// Which is what nearly happened. The migration was first written assuming the secret on the
// module's row was the administrator's token, because that is what "one token per module" would
// have made it. It was never so: the core handed out a second secret at registration, so on this
// instance the assumption matched nothing at all. What caught it was counting rows on the stand
// rather than reading the code — one token, four modules, zero matches.
func TestAModuleRunningBeforeTheOneTokenChangeKeepsWorking(t *testing.T) {
	ctx := context.Background()
	url := dbtest.URL(t)

	// The database is walked backwards and forwards here, so it is put back at the newest
	// version whatever happens. A test that fails halfway through would otherwise leave every
	// test after it failing about columns that are not there, which is a way of hiding one
	// failure behind a dozen.
	t.Cleanup(func() {
		if err := store.Migrate(url); err != nil {
			t.Errorf("put the database back at the newest version: %v", err)
		}
	})

	migrateTo(t, url, 32)

	// What version 32 held: a module, the secret it authenticates with, and the token an
	// administrator created for it — two different secrets, which is the arrangement being
	// removed.
	moduleSecret := sha256.Sum256([]byte("the-secret-the-module-already-has"))
	registrationSecret := sha256.Sum256([]byte("the-secret-an-administrator-chose"))

	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, `
		INSERT INTO integrations (kind, name, endpoint, token_hash, module_version)
		VALUES ('registry:docker', 'running-before', 'http://registry:8091', $1, '0.1.0')`,
		moduleSecret[:]); err != nil {
		t.Fatalf("insert a module as it existed before: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO module_tokens (name, description, token_hash)
		VALUES ('old registration token', '', $1)`, registrationSecret[:]); err != nil {
		t.Fatalf("insert an administrator's token: %v", err)
	}

	migrateTo(t, url, 33)

	st := dbtest.Open(t)

	// The secret does not change, so the module that was working keeps working: no reinstall,
	// no restart, no reissue.
	token, err := st.ModuleTokens().ByHash(ctx, moduleSecret[:])
	if err != nil {
		t.Fatalf("the module's existing credential is not recognised after the migration, so it "+
			"has been locked out of an instance it was working on: %v", err)
	}
	if !token.Bound() {
		t.Fatal("the module's credential exists but is bound to nothing, so the core cannot tell " +
			"which module presented it")
	}

	module, err := st.Integrations().ByID(ctx, *token.IntegrationID)
	if err != nil {
		t.Fatalf("the module is not there after the migration: %v", err)
	}
	if module.Name != "running-before" {
		t.Fatalf("the credential is bound to the wrong module: %q", module.Name)
	}

	// The administrator's spent registration token is untouched, and is still an invitation.
	// It was never the module's credential, so there is nothing to carry over — and quietly
	// deleting an administrator's rows is not something a schema change should be doing.
	if _, err := st.ModuleTokens().ByHash(ctx, registrationSecret[:]); err != nil {
		t.Fatalf("an administrator's token was disturbed by a migration about module credentials: %v", err)
	}

	// One place left that holds the secret, so the two copies cannot drift apart.
	var copies int
	if err := st.Pool().QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		 WHERE table_name = 'integrations' AND column_name = 'token_hash'`).Scan(&copies); err != nil {
		t.Fatalf("ask whether the old column is gone: %v", err)
	}
	if copies != 0 {
		t.Fatal("the module's row still carries the secret, so there are two copies of it and " +
			"nothing that keeps them in step")
	}

	// Rolled back and brought forward again, with the module still working at the end of it.
	//
	// A migration that cannot be re-applied after a rollback is not a migration anybody can undo
	// their way out of: it leaves an operator who has rolled back to fix something, unable to
	// come forward again without editing the database by hand. This was nearly shipped that way
	// — the down migration kept the columns the up migration adds, on the grounds that a token
	// with an end date is worth having — and the reason it is written this way is that running
	// the test twice against the same database is not something anybody would think to do.
	migrateTo(t, url, 32)
	migrateTo(t, url, 33)

	after, err := dbtest.Open(t).ModuleTokens().ByHash(ctx, moduleSecret[:])
	if err != nil {
		t.Fatalf("the module's credential stopped working after a rollback and a re-apply: %v", err)
	}
	if !after.Bound() || *after.IntegrationID != module.ID {
		t.Fatal("the credential survived the cycle but no longer points at its module")
	}
}
