package api

import (
	"net/http"

	"github.com/jackc/pgx/v5"
)

// handleModuleDropDatabase removes the database the core provisioned for this
// module.
//
// The module asks; the core does. It provisioned the database and owns the role
// beside it, so it is the only thing that can drop both without guessing — and a
// module reaching into the shared cluster's other databases is exactly what the
// per-module role exists to prevent.
//
// The images a registry holds are not in here. This removes bookkeeping, not
// anything anybody would miss.
func (s *Server) handleModuleDropDatabase(w http.ResponseWriter, r *http.Request) {
	integration := integrationFrom(r.Context())

	if integration.DatabaseName == "" {
		// Nothing was provisioned. Already being in the state asked for is not a
		// failure: the core may be asked twice after a restart it did not hear the
		// end of.
		s.writeJSON(w, r, http.StatusOK, map[string]any{"dropped": false, "reason": "none_provisioned"})
		return
	}

	name := integration.DatabaseName
	role := integration.DatabaseRole

	if err := s.dropModuleDatabase(r, name, role); err != nil {
		s.log.Error("drop the module database", "module", integration.Kind, "error", err)
		s.writeError(w, r, errBadRequestf("the module database %q could not be dropped: %v", name, err))
		return
	}

	if err := s.store.Integrations().ClearModuleDatabase(r.Context(), integration.ID); err != nil {
		// The database is gone but the row still says otherwise. The row is the
		// smaller problem and is worth logging loudly rather than failing over.
		s.log.Error("forget the dropped module database", "module_id", integration.ID, "error", err)
	}

	s.log.Info("module database dropped", "kind", integration.Kind, "database", name)
	s.writeJSON(w, r, http.StatusOK, map[string]any{"dropped": true, "database": name})
}

// dropModuleDatabase removes a database and the role that owns it.
//
// The role goes too: a database dropped out from under its owner leaves a role
// that owns nothing, and the next module provisioned with the same name would
// silently inherit it.
func (s *Server) dropModuleDatabase(r *http.Request, name, role string) error {
	ctx := r.Context()

	// Nothing running should still be connected; terminate rather than fail on a
	// connection an interrupted request left behind.
	_, _ = s.store.Pool().Exec(ctx,
		`SELECT pg_terminate_backend(pid) FROM pg_stat_activity
		 WHERE datname = $1 AND pid <> pg_backend_pid()`, name)

	if _, err := s.store.Pool().Exec(ctx, `DROP DATABASE IF EXISTS `+quoteIdentifier(name)); err != nil {
		return err
	}
	if role != "" {
		_, _ = s.store.Pool().Exec(ctx, `DROP ROLE IF EXISTS `+quoteIdentifier(role))
	}
	return nil
}

// quoteIdentifier makes a name safe to interpolate.
//
// The names come from the core itself, but a DROP statement cannot take a
// parameter, and an identifier containing a quote is the kind of mistake that is
// only ever discovered afterwards.
func quoteIdentifier(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
