package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// WithProjectLock runs fn while holding an advisory lock on a project.
//
// Two editors saving at the same time, or an editor and a push arriving together,
// would otherwise interleave: both read the same revision, both build a commit, and
// one silently disappears. The lock makes the read-decide-write sequence atomic
// with respect to other database sessions, which is exactly what a conditional ref
// update alone cannot do.
//
// The lock is session-scoped rather than transaction-scoped because git operations
// happen outside any transaction; a dedicated connection is held for the duration
// and released afterwards.
func (s *Store) WithProjectLock(ctx context.Context, projectID string, fn func() error) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection for the project lock: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext($1))`, projectID); err != nil {
		return fmt.Errorf("lock project: %w", err)
	}
	defer func() {
		// The unlock runs on a background context because the request context may
		// already be cancelled, and leaving the lock held would block the next
		// editor for the lifetime of the connection.
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, `SELECT pg_advisory_unlock(hashtext($1))`, projectID)
	}()

	return fn()
}

// LockTx takes a transaction-scoped advisory lock. Prefer WithProjectLock unless
// the caller already has a transaction.
func LockTx(ctx context.Context, tx pgx.Tx, projectID string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, projectID)
	return err
}
