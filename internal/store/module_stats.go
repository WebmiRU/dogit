package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/ewolf/dogit/internal/models"
)

// ModuleStatsRepo keeps what modules report about themselves.
//
// The readings are a time series because the question an administrator asks is
// almost never "how full is it right now" but "is it filling up" — and only a
// series can answer that.
type ModuleStatsRepo struct{ s *Store }

func (s *Store) ModuleStats() *ModuleStatsRepo { return &ModuleStatsRepo{s: s} }

// statsRetention is how long readings are kept.
//
// A heartbeat every fifteen seconds would be nearly six thousand rows a day per
// module, and an administrator looks at a module's page for weeks, not days.
// A week of readings still shows a slow leak; anything longer would be a
// warehouse nobody queries.
const statsRetention = 7 * 24 * time.Hour

// Record stores one reading and drops what has aged out.
func (r *ModuleStatsRepo) Record(ctx context.Context, integrationID uuid.UUID, stats *models.ModuleStats) error {
	if stats == nil {
		return nil
	}

	// A module's clock is not the core's clock, and a clock running ahead would
	// let a reading land in the future and sort ahead of later readings forever.
	// The moment of arrival is the moment we know about.
	at := time.Now().UTC()

	// A module with nothing of its own to say sends no facts at all, and an empty
	// object reads as "it had nothing" rather than as a missing reading.
	if stats.Extra == nil {
		stats.Extra = map[string]string{}
	}

	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO module_stats (
			integration_id, at,
			storage_total_bytes, storage_used_bytes,
			process_cpu_percent, process_memory_bytes,
			host_cpu_percent, host_memory_total_bytes, host_memory_used_bytes, host_load1,
			uptime_seconds, extra
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
		)
		ON CONFLICT (integration_id, at) DO NOTHING`,
		integrationID, at,
		stats.StorageTotalBytes, stats.StorageUsedBytes,
		stats.ProcessCPUPercent, stats.ProcessMemoryBytes,
		stats.HostCPUPercent, stats.HostMemoryTotalBytes, stats.HostMemoryUsedBytes, stats.HostLoad1,
		stats.UptimeSeconds, stats.Extra,
	)
	if err != nil {
		return fmt.Errorf("record module stats: %w", err)
	}

	if _, err := r.s.pool.Exec(ctx,
		`DELETE FROM module_stats WHERE integration_id = $1 AND at < $2`,
		integrationID, at.Add(-statsRetention)); err != nil {
		return fmt.Errorf("prune module stats: %w", err)
	}
	return nil
}

// Latest returns the most recent reading, or ErrNotFound for a module that has
// never sent one.
func (r *ModuleStatsRepo) Latest(ctx context.Context, integrationID uuid.UUID) (*models.ModuleStats, error) {
	rows, err := r.s.pool.Query(ctx,
		scanStats+` WHERE integration_id = $1 ORDER BY at DESC LIMIT 1`, integrationID)
	if err != nil {
		return nil, fmt.Errorf("latest module stats: %w", err)
	}
	readings, err := collectStats(rows)
	if err != nil {
		return nil, err
	}
	if len(readings) == 0 {
		return nil, ErrNotFound
	}
	latest := readings[0]
	return &latest, nil
}

// Series returns readings for a module's graph, oldest first.
//
// from and to are clamped to the retention window: asking for a year of history
// returns the week we kept rather than pretending there is more.
func (r *ModuleStatsRepo) Series(ctx context.Context, integrationID uuid.UUID, from, to time.Time) ([]models.ModuleStats, error) {
	latest := time.Now().UTC()
	windowStart := latest.Add(-statsRetention)
	if from.Before(windowStart) {
		from = windowStart
	}
	if to.IsZero() || to.After(latest) {
		to = latest
	}

	rows, err := r.s.pool.Query(ctx,
		scanStats+` WHERE integration_id = $1 AND at >= $2 AND at <= $3 ORDER BY at ASC`,
		integrationID, from, to)
	if err != nil {
		return nil, fmt.Errorf("module stats series: %w", err)
	}
	return collectStats(rows)
}

const scanStats = `
	SELECT at,
	       storage_total_bytes, storage_used_bytes,
	       process_cpu_percent, process_memory_bytes,
	       host_cpu_percent, host_memory_total_bytes, host_memory_used_bytes, host_load1,
	       uptime_seconds, extra
	FROM module_stats`

func collectStats(rows pgx.Rows) ([]models.ModuleStats, error) {
	defer rows.Close()

	readings := []models.ModuleStats{}
	for rows.Next() {
		var stats models.ModuleStats
		err := rows.Scan(
			&stats.At,
			&stats.StorageTotalBytes, &stats.StorageUsedBytes,
			&stats.ProcessCPUPercent, &stats.ProcessMemoryBytes,
			&stats.HostCPUPercent, &stats.HostMemoryTotalBytes, &stats.HostMemoryUsedBytes, &stats.HostLoad1,
			&stats.UptimeSeconds, &stats.Extra,
		)
		if err != nil {
			return nil, fmt.Errorf("scan module stats: %w", err)
		}
		readings = append(readings, stats)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read module stats: %w", err)
	}
	return readings, nil
}
