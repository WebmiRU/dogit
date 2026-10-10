package api

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// deployLock keeps one database session and all physical target locks acquired through it.
// A single deployment may fan out to several Kubernetes clusters/namespaces under one target
// name, so all locks must be held through the complete module call.
type deployLock struct {
	conn       *pgxpool.Conn
	identities []string
}

func uniqueLockIdentities(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// tryDeployLock is the single-target form used by a manual rollback.
func (s *Server) tryDeployLock(ctx context.Context, identity string) (*deployLock, bool, error) {
	return s.tryDeployLocks(ctx, []string{identity})
}

// tryDeployLocks uses PostgreSQL session advisory locks, held on one acquired connection for the
// entire rollout. Unlike the in-memory queue this coordinates every dogit replica; unlike a
// transaction-level lock it survives the individual queries made while deploying. All callers
// acquire in sorted order, and release every partial acquisition when a target is already held.
func (s *Server) tryDeployLocks(ctx context.Context, identities []string) (*deployLock, bool, error) {
	identities = uniqueLockIdentities(identities)
	if len(identities) == 0 {
		return nil, false, fmt.Errorf("deployment has no physical target identity")
	}
	conn, err := s.store.Pool().Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire a connection for the deployment lock: %w", err)
	}

	acquired := make([]string, 0, len(identities))
	unlockPartial := func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		broken := false
		for index := len(acquired) - 1; index >= 0; index-- {
			var released bool
			if err := conn.QueryRow(cleanupCtx,
				`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, acquired[index]).Scan(&released); err != nil {
				broken = true
				s.log.Error("release a partially acquired deployment lock",
					"target", acquired[index], "error", err)
			}
		}
		if broken {
			// Never return a pooled session that may still hold advisory locks. Destroying
			// the physical connection makes PostgreSQL release every lock owned by it.
			s.discardDeployLockConnection(conn)
			return
		}
		conn.Release()
	}

	for _, identity := range identities {
		var ok bool
		if err := conn.QueryRow(ctx,
			`SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, identity).Scan(&ok); err != nil {
			unlockPartial()
			return nil, false, fmt.Errorf("ask for the deployment lock for %q: %w", identity, err)
		}
		if !ok {
			unlockPartial()
			return nil, false, nil
		}
		acquired = append(acquired, identity)
	}
	return &deployLock{conn: conn, identities: acquired}, true, nil
}

// releaseDeployLock returns all session locks before the in-process slot is passed to the next
// waiter. If the connection has died, PostgreSQL releases its session locks with it.
func (s *Server) releaseDeployLock(ctx context.Context, lock *deployLock) {
	if lock == nil || lock.conn == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	broken := false
	for index := len(lock.identities) - 1; index >= 0; index-- {
		identity := lock.identities[index]
		var released bool
		if err := lock.conn.QueryRow(cleanupCtx,
			`SELECT pg_advisory_unlock(hashtextextended($1, 0))`, identity).Scan(&released); err != nil {
			broken = true
			s.log.Error("release the shared deployment lock", "target", identity, "error", err)
		} else if !released {
			s.log.Warn("the shared deployment lock was no longer held", "target", identity)
		}
	}
	if broken {
		s.discardDeployLockConnection(lock.conn)
	} else {
		lock.conn.Release()
	}
	lock.conn = nil
}

// discardDeployLockConnection removes a physical session from the pool when its advisory locks
// could not all be released. PostgreSQL releases session advisory locks only when that session
// disconnects, so returning it to the pool would let an unrelated request inherit a hidden lock.
func (s *Server) discardDeployLockConnection(conn *pgxpool.Conn) {
	if conn == nil {
		return
	}
	pgConn := conn.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pgConn.Close(ctx); err != nil {
		s.log.Warn("close a database session with a possibly held deployment lock", "error", err)
	}
}
