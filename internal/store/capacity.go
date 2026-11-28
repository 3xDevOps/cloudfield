package store

import (
	"context"
	"fmt"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// CapacityStore counts what the server did for want of memory or disk, a
// row per hour and counter, so the counts outlast a restart and stay small.
type CapacityStore interface {
	// CountCapacityEvent adds e to its hour's counter.
	CountCapacityEvent(ctx context.Context, e domain.CapacityEvent) error
	// CapacityCounts returns the counters of the hours from since on, the
	// oldest hour first.
	CapacityCounts(ctx context.Context, since time.Time) ([]domain.CapacityCount, error)
	// PruneCapacityCounts deletes the counters of the hours before before.
	PruneCapacityCounts(ctx context.Context, before time.Time) error
}

func (d *DB) CountCapacityEvent(ctx context.Context, e domain.CapacityEvent) error {
	waited := int64(e.Waited / time.Second)
	_, err := d.db.ExecContext(ctx, `
INSERT INTO capacity_counts (hour, kind, op, cause, count, available_bytes, floor_bytes, used_bytes, idle_hour_bytes, vm_bytes,
	waited_seconds, max_waited_seconds, last_at, last_run, last_text)
VALUES (?1, ?2, ?3, ?4, 1, ?5, ?6, ?7, ?8, ?9, ?10, ?10, ?11, ?12, ?13)
ON CONFLICT(hour, kind, op, cause) DO UPDATE SET
	count = count + 1,
	available_bytes = available_bytes + excluded.available_bytes,
	floor_bytes = floor_bytes + excluded.floor_bytes,
	used_bytes = used_bytes + excluded.used_bytes,
	idle_hour_bytes = idle_hour_bytes + excluded.idle_hour_bytes,
	vm_bytes = vm_bytes + excluded.vm_bytes,
	waited_seconds = waited_seconds + excluded.waited_seconds,
	max_waited_seconds = max(max_waited_seconds, excluded.max_waited_seconds),
	last_run = CASE WHEN excluded.last_at >= last_at THEN excluded.last_run ELSE last_run END,
	last_text = CASE WHEN excluded.last_at >= last_at THEN excluded.last_text ELSE last_text END,
	last_at = max(last_at, excluded.last_at)`,
		e.At.UTC().Truncate(time.Hour).Unix(), e.Kind, e.Op, e.Cause,
		int64(e.AvailableBytes), int64(e.FloorBytes), int64(e.UsedBytes), int64(e.IdleHourBytes), int64(e.VMBytes),
		waited, e.At.UTC().UnixNano(), e.Run, e.Text)
	if err != nil {
		return fmt.Errorf("store: count a capacity event: %w", err)
	}
	return nil
}

func (d *DB) PruneCapacityCounts(ctx context.Context, before time.Time) error {
	if _, err := d.db.ExecContext(ctx, `DELETE FROM capacity_counts WHERE hour < ?`, before.UTC().Truncate(time.Hour).Unix()); err != nil {
		return fmt.Errorf("store: prune capacity counts: %w", err)
	}
	return nil
}

func (d *DB) CapacityCounts(ctx context.Context, since time.Time) ([]domain.CapacityCount, error) {
	rows, err := d.db.QueryContext(ctx, `
SELECT hour, kind, op, cause, count, available_bytes, floor_bytes, used_bytes, idle_hour_bytes, vm_bytes,
	waited_seconds, max_waited_seconds, last_at, last_run, last_text
FROM capacity_counts WHERE hour >= ? ORDER BY hour, kind, op, cause`, since.UTC().Truncate(time.Hour).Unix())
	if err != nil {
		return nil, fmt.Errorf("store: capacity counts: %w", err)
	}
	defer rows.Close()
	var out []domain.CapacityCount
	for rows.Next() {
		var c domain.CapacityCount
		var hour, available, floor, used, idle, vm, waited, maxWaited, lastAt int64
		if err := rows.Scan(&hour, &c.Kind, &c.Op, &c.Cause, &c.Count, &available, &floor, &used, &idle, &vm,
			&waited, &maxWaited, &lastAt, &c.LastRun, &c.LastText); err != nil {
			return nil, fmt.Errorf("store: capacity counts: %w", err)
		}
		c.Hour, c.LastAt = time.Unix(hour, 0).UTC(), time.Unix(0, lastAt).UTC()
		c.AvailableBytes, c.FloorBytes, c.UsedBytes, c.IdleHourBytes, c.VMBytes = uint64(available), uint64(floor), uint64(used), uint64(idle), uint64(vm)
		c.Waited, c.MaxWaited = time.Duration(waited)*time.Second, time.Duration(maxWaited)*time.Second
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: capacity counts: %w", err)
	}
	return out, nil
}
