// Package store owns the PostgreSQL connection pool and exposes repositories.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned by repository methods when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("conflict")

// Store is the database handle shared by all repositories.
type Store struct {
	pool *pgxpool.Pool
}

// Options tunes how Open establishes the connection pool.
type Options struct {
	MaxConns       int32
	ConnectRetry   time.Duration
	ConnectTimeout time.Duration
}

// DefaultOptions returns options suitable for a single application instance.
func DefaultOptions() Options {
	return Options{
		MaxConns:       20,
		ConnectRetry:   time.Second,
		ConnectTimeout: 60 * time.Second,
	}
}

// Open connects to Postgres, retrying until the server responds or the
// deadline expires. Database availability at start-up is a hard requirement:
// an instance that cannot reach Postgres must not serve traffic.
func Open(ctx context.Context, url string, opts Options) (*Store, error) {
	if opts.MaxConns <= 0 {
		opts.MaxConns = 20
	}
	if opts.ConnectRetry <= 0 {
		opts.ConnectRetry = time.Second
	}
	if opts.ConnectTimeout <= 0 {
		opts.ConnectTimeout = 60 * time.Second
	}

	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	cfg.MaxConns = opts.MaxConns
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	deadline := time.Now().Add(opts.ConnectTimeout)
	for {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err = pool.Ping(pingCtx)
		cancel()
		if err == nil {
			return &Store{pool: pool}, nil
		}
		if time.Now().After(deadline) {
			pool.Close()
			return nil, fmt.Errorf("connect to postgres: %w", err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(opts.ConnectRetry):
		}
	}
}

func (s *Store) Pool() *pgxpool.Pool { return s.pool }

func (s *Store) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Health verifies the database is reachable, for readiness probes.
func (s *Store) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return s.pool.Ping(ctx)
}

// Tx runs fn inside a transaction, rolling back on error or panic.
func (s *Store) Tx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error { return fn(tx) })
}

// pgxNoRows is aliased so repository files compare against one symbol.
var pgxNoRows = pgx.ErrNoRows

// Querier is satisfied by both *pgxpool.Pool and pgx.Tx, so repository helpers
// can run either standalone or inside a transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (s *Store) Q() Querier { return s.pool }

// IsUniqueViolation reports whether err is a Postgres unique-constraint error.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
