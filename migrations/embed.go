// Package migrations embeds the SQL migration files into the binary.
package migrations

import "embed"

// FS holds every .sql file in this directory, named for golang-migrate:
// NNNNNN_name.up.sql and NNNNNN_name.down.sql.
//
//go:embed *.sql
var FS embed.FS
