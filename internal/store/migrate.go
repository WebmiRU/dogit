package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" database/sql driver

	"github.com/ewolf/dogit/migrations"
)

// Migrate applies pending migrations to url. The pgx5 driver holds an advisory
// lock for the duration, so several instances may start simultaneously.
func Migrate(url string) error {
	return MigrateWithTable(url, migratepgx.DefaultMigrationsTable)
}

func MigrateWithTable(url, table string) error {
	if table == "" {
		table = migratepgx.DefaultMigrationsTable
	}

	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer db.Close()

	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{
		MigrationsTable: table,
	})
	if err != nil {
		return fmt.Errorf("init migration driver: %w", err)
	}

	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		return fmt.Errorf("build migrator: %w", err)
	}

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
