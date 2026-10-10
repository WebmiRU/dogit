package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tryDeployLock uses a PostgreSQL session advisory lock, held on the acquired connection for
// the entire rollout. Unlike the in-memory queue this coordinates every dogit replica, and
// unlike a transaction-level lock it survives the individual queries made while deploying.
func (s *Server) tryDeployLock(ctx context.Context, identity string) (*pgxpool.Conn, bool, error) {
	if identity == "" {
		return nil, false, fmt.Errorf("deployment has no physical target identity")
	}
	conn, err := s.store.Pool().Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire a connection for the deployment lock: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, identity).Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("ask for the deployment lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, false, nil
	}
	return conn, true, nil
}

// releaseDeployLock returns the session lock before the in-process slot is passed to another
// waiter. If the connection has died, PostgreSQL releases its session locks with it.
func (s *Server) releaseDeployLock(ctx context.Context, conn *pgxpool.Conn, identity string) {
	if conn == nil {
		return
	}
	var released bool
	if err := conn.QueryRow(ctx,
		`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, identity).Scan(&released); err != nil {
		s.log.Error("release the shared deployment lock", "target", identity, "error", err)
	} else if !released {
		s.log.Warn("the shared deployment lock was no longer held", "target", identity)
	}
	conn.Release()
}
