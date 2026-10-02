package store

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ModuleDatabase is the database provisioned for one module.
//
// Modules share the application's PostgreSQL cluster but never its database, its
// role or its schema. That is the whole point: one physical cluster to operate,
// one backup to take, and no way for a module to drop the application's tables by
// accident.
type ModuleDatabase struct {
	// Name is the database name inside the cluster.
	Name string `json:"name"`
	// Role is the database role that owns it.
	Role string `json:"role"`
	// URL is returned exactly once, when the database is provisioned. Only the
	// plaintext password lives here; the core never stores it.
	URL string `json:"url,omitempty"`
}

// ProvisionModuleDatabase creates a database and an owning role for a module.
//
// The credentials are returned to the caller and never written to the database
// table: a module persists them in its own secret, where they belong. Losing them
// means asking for a new database, which is a deliberate administrative action
// rather than a recovery from a leaked table.
func (r *IntegrationRepo) ProvisionModuleDatabase(ctx context.Context, adminDSN string, kind string) (*ModuleDatabase, error) {
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	safeKind := sanitiseIdentifier(kind)
	role := fmt.Sprintf("dogit_%s_%s", safeKind, suffix)
	database := fmt.Sprintf("dogit_%s_%s", safeKind, suffix)
	password, err := randomPassword()
	if err != nil {
		return nil, err
	}

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return nil, fmt.Errorf("connect as the database administrator: %w", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// The role is created with the password inline rather than through ALTER, so a
	// module database is usable the moment this returns.
	createRole := fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD %s`, quoteIdentifier(role), quoteLiteral(password))
	if _, err := admin.Exec(ctx, createRole); err != nil {
		return nil, fmt.Errorf("create role for module %s: %w", kind, err)
	}

	createDB := fmt.Sprintf(`CREATE DATABASE %s OWNER %s ENCODING 'UTF8'`, quoteIdentifier(database), quoteIdentifier(role))
	if _, err := admin.Exec(ctx, createDB); err != nil {
		// Leaving a role behind after a failed database creation would block a
		// retry with the same name, so it is removed again.
		_, _ = admin.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, quoteIdentifier(role)))
		return nil, fmt.Errorf("create database for module %s: %w", kind, err)
	}

	// Revoking the public schema's default privileges stops the module from creating
	// objects in shared namespaces such as "public" extensions.
	if _, err := admin.Exec(ctx, fmt.Sprintf(`REVOKE ALL ON DATABASE %s FROM PUBLIC`, quoteIdentifier(database))); err != nil {
		return nil, fmt.Errorf("lock down database %s: %w", database, err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`GRANT ALL ON DATABASE %s TO %s`, quoteIdentifier(database), quoteIdentifier(role))); err != nil {
		return nil, fmt.Errorf("grant database %s: %w", database, err)
	}

	return &ModuleDatabase{
		Name: database,
		Role: role,
		URL:  buildModuleDSN(adminDSN, database, role, password),
	}, nil
}

// DropModuleDatabase removes a module's database and role.
func (r *IntegrationRepo) DropModuleDatabase(ctx context.Context, adminDSN string, database, role string) error {
	if database == "" {
		return nil
	}

	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return fmt.Errorf("connect as the database administrator: %w", err)
	}
	defer func() { _ = admin.Close(ctx) }()

	// Terminating sessions first: a module that is still running keeps a connection
	// open, and a database with connections cannot be dropped.
	// The query takes the database name as a parameter: this one is not an
	// identifier and does not need quoting.
	if _, err := admin.Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE datname = $1 AND pid <> pg_backend_pid()`, database); err != nil {
		return fmt.Errorf("disconnect module database %s: %w", database, err)
	}

	if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s`, quoteIdentifier(database))); err != nil {
		return fmt.Errorf("drop module database %s: %w", database, err)
	}
	if role != "" {
		if _, err := admin.Exec(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, quoteIdentifier(role))); err != nil {
			return fmt.Errorf("drop module role %s: %w", role, err)
		}
	}
	return nil
}

// ModuleDatabases lists the module databases in the cluster, which is what an
// operator checks when a module misbehaves.
func (r *IntegrationRepo) ModuleDatabases(ctx context.Context, adminDSN string) ([]ModuleDatabase, error) {
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close(ctx) }()

	rows, err := admin.Query(ctx, `
		SELECT datname, pg_catalog.pg_get_userbyid(datdba)
		FROM pg_database
		WHERE datname LIKE 'dogit\\_%' AND datname <> current_database()
		ORDER BY datname`)
	if err != nil {
		return nil, fmt.Errorf("list module databases: %w", err)
	}
	defer rows.Close()

	out := []ModuleDatabase{}
	for rows.Next() {
		var database ModuleDatabase
		if err := rows.Scan(&database.Name, &database.Role); err != nil {
			return nil, err
		}
		out = append(out, database)
	}
	return out, rows.Err()
}

// buildModuleDSN rewrites a connection string for the module's own database and
// role.
//
// The string is rebuilt rather than patched through pgx.Config: a config parsed
// from a URL keeps the original text and ConnString returns it unchanged, which
// would hand the module the application's database and credentials.
func buildModuleDSN(adminDSN, database, role, password string) string {
	if parsed, err := url.Parse(adminDSN); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		parsed.User = url.UserPassword(role, password)
		parsed.Path = "/" + database
		return parsed.String()
	}

	// Keyword/value form: rebuild it from the parsed parts, keeping only the
	// settings that affect reachability.
	config, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		return ""
	}
	sslMode := "disable"
	if config.TLSConfig != nil {
		sslMode = "require"
	}
	return fmt.Sprintf("host=%s port=%d dbname=%s user=%s password=%s sslmode=%s",
		config.Host, config.Port, database, role, password, sslMode)
}

// sanitiseIdentifier reduces a module kind to characters valid in a SQL
// identifier. Identifiers cannot be passed as parameters, so the value has to be
// made safe rather than quoted, and this reduces it to a conservative alphabet.
func sanitiseIdentifier(kind string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(kind) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if len(out) > 24 {
		out = out[:24]
	}
	return strings.Trim(out, "_")
}

// quoteIdentifier wraps an identifier in double quotes. Together with
// sanitiseIdentifier this is belt and braces: the value can never terminate the
// quoted identifier and continue into another statement.
func quoteIdentifier(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteLiteral wraps a value as a SQL string literal.
func quoteLiteral(value string) string {
	return `'` + strings.ReplaceAll(value, `'`, `''`) + `'`
}

func randomPassword() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate a database password: %w", err)
	}
	for i, b := range buf {
		buf[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(buf), nil
}
