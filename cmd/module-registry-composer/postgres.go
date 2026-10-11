package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type catalogStore struct {
	db *sql.DB
}

type packageRecord struct {
	Name          string
	Metadata      []byte
	VersionCount  int
	LastFetchedAt time.Time
}

type distRecord struct {
	CacheKey    string
	PackageName string
	Version     string
	UpstreamURL string
	ObjectKey   string
	SizeBytes   int64
	CachedAt    sql.NullTime
}

type cacheSummary struct {
	Packages     int64
	Versions     int64
	Archives     int64
	ArchiveBytes int64
}

func (s *catalogStore) migrate(ctx context.Context) error {
	statements := []string{
		"CREATE TABLE IF NOT EXISTS composer_package_cache (" +
			"package_name text PRIMARY KEY, metadata jsonb NOT NULL, " +
			"version_count integer NOT NULL DEFAULT 0, last_fetched_at timestamptz NOT NULL DEFAULT now())",
		"CREATE TABLE IF NOT EXISTS composer_dist_cache (" +
			"cache_key text PRIMARY KEY, package_name text NOT NULL, version text NOT NULL, " +
			"upstream_url text NOT NULL, object_key text NOT NULL, size_bytes bigint NOT NULL DEFAULT 0, " +
			"cached_at timestamptz, last_seen_at timestamptz NOT NULL DEFAULT now())",
		"CREATE INDEX IF NOT EXISTS composer_dist_cache_package_idx ON composer_dist_cache (package_name)",
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create Composer cache table: %w", err)
		}
	}
	return nil
}

func (s *catalogStore) putPackage(ctx context.Context, name string, metadata []byte, versionCount int) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO composer_package_cache "+
		"(package_name, metadata, version_count, last_fetched_at) VALUES ($1, $2::jsonb, $3, now()) "+
		"ON CONFLICT (package_name) DO UPDATE SET metadata = EXCLUDED.metadata, "+
		"version_count = EXCLUDED.version_count, last_fetched_at = now()",
		name, string(metadata), versionCount)
	if err != nil {
		return fmt.Errorf("save Composer metadata for %s: %w", name, err)
	}
	return nil
}

func (s *catalogStore) packageByName(ctx context.Context, name string) (packageRecord, bool, error) {
	var record packageRecord
	err := s.db.QueryRowContext(ctx, "SELECT package_name, metadata, version_count, last_fetched_at "+
		"FROM composer_package_cache WHERE package_name = $1", name).
		Scan(&record.Name, &record.Metadata, &record.VersionCount, &record.LastFetchedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return packageRecord{}, false, nil
	}
	if err != nil {
		return packageRecord{}, false, fmt.Errorf("read Composer metadata for %s: %w", name, err)
	}
	return record, true, nil
}

func (s *catalogStore) listPackages(ctx context.Context) ([]packageRecord, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT package_name, metadata, version_count, last_fetched_at "+
		"FROM composer_package_cache ORDER BY package_name")
	if err != nil {
		return nil, fmt.Errorf("list cached Composer packages: %w", err)
	}
	defer rows.Close()

	out := []packageRecord{}
	for rows.Next() {
		var record packageRecord
		if err := rows.Scan(&record.Name, &record.Metadata, &record.VersionCount, &record.LastFetchedAt); err != nil {
			return nil, fmt.Errorf("read cached Composer package: %w", err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate cached Composer packages: %w", err)
	}
	return out, nil
}

func (s *catalogStore) putDist(ctx context.Context, record distRecord) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO composer_dist_cache "+
		"(cache_key, package_name, version, upstream_url, object_key, last_seen_at) "+
		"VALUES ($1, $2, $3, $4, $5, now()) ON CONFLICT (cache_key) DO UPDATE "+
		"SET package_name = EXCLUDED.package_name, version = EXCLUDED.version, "+
		"upstream_url = EXCLUDED.upstream_url, object_key = EXCLUDED.object_key, last_seen_at = now()",
		record.CacheKey, record.PackageName, record.Version, record.UpstreamURL, record.ObjectKey)
	if err != nil {
		return fmt.Errorf("save Composer distribution URL for %s %s: %w", record.PackageName, record.Version, err)
	}
	return nil
}

func (s *catalogStore) distByKey(ctx context.Context, key string) (distRecord, bool, error) {
	var record distRecord
	err := s.db.QueryRowContext(ctx, "SELECT cache_key, package_name, version, upstream_url, object_key, size_bytes, cached_at "+
		"FROM composer_dist_cache WHERE cache_key = $1", key).
		Scan(&record.CacheKey, &record.PackageName, &record.Version, &record.UpstreamURL,
			&record.ObjectKey, &record.SizeBytes, &record.CachedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return distRecord{}, false, nil
	}
	if err != nil {
		return distRecord{}, false, fmt.Errorf("read Composer distribution %s: %w", key, err)
	}
	return record, true, nil
}

func (s *catalogStore) markDistCached(ctx context.Context, key string, size int64, cachedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, "UPDATE composer_dist_cache "+
		"SET size_bytes = $2, cached_at = COALESCE(cached_at, $3), last_seen_at = now() WHERE cache_key = $1",
		key, size, cachedAt)
	if err != nil {
		return fmt.Errorf("record cached Composer distribution %s: %w", key, err)
	}
	return nil
}

func (s *catalogStore) summary(ctx context.Context) (cacheSummary, error) {
	var summary cacheSummary
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(version_count), 0) "+
		"FROM composer_package_cache").Scan(&summary.Packages, &summary.Versions); err != nil {
		return cacheSummary{}, fmt.Errorf("count Composer metadata cache: %w", err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FILTER (WHERE cached_at IS NOT NULL), "+
		"COALESCE(SUM(size_bytes) FILTER (WHERE cached_at IS NOT NULL), 0) FROM composer_dist_cache").
		Scan(&summary.Archives, &summary.ArchiveBytes); err != nil {
		return cacheSummary{}, fmt.Errorf("count Composer archive cache: %w", err)
	}
	return summary, nil
}
