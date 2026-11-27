package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// ProvisionalReportPrefix starts the correlation ID of a provisional report:
// the last message of a child run whose turn ended without a report.
const ProvisionalReportPrefix = "provisional:"

// CoordReportedSince reports whether run reserved a report at or after since;
// a zero since matches any report.
func (d *DB) CoordReportedSince(ctx context.Context, run domain.RunID, since time.Time) (bool, error) {
	var after int64
	if !since.IsZero() {
		var err error
		if after, err = encodeTime(since); err != nil {
			return false, fmt.Errorf("store: coord reported since: %w", err)
		}
	}
	var reported bool
	if err := d.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM coord_reports WHERE run_id = ? AND created_at >= ?)`, run, after,
	).Scan(&reported); err != nil {
		return false, fmt.Errorf("store: coord reported since: %w", err)
	}
	return reported, nil
}

// CoordStateReportedSince is the outcome of run's latest needs-input, waiting
// or answered report that took effect at or after since, or "" for none.
func (d *DB) CoordStateReportedSince(ctx context.Context, run domain.RunID, since time.Time) (string, error) {
	var after int64
	if !since.IsZero() {
		var err error
		if after, err = encodeTime(since); err != nil {
			return "", fmt.Errorf("store: coord state reported since: %w", err)
		}
	}
	var outcome string
	err := d.db.QueryRowContext(ctx,
		`SELECT outcome FROM coord_state_reports WHERE run_id = ? AND created_at >= ? AND applied_at IS NOT NULL
		 ORDER BY created_at DESC LIMIT 1`, run, after).Scan(&outcome)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: coord state reported since: %w", err)
	}
	return outcome, nil
}

// RetireProvisionalReports retires the undelivered provisional reports from
// sent to to before cutoff.
func (d *DB) RetireProvisionalReports(ctx context.Context, from, to domain.RunID, cutoff time.Time) error {
	now, err := encodeTime(time.Now().UTC())
	if err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	before, err := encodeTime(cutoff)
	if err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	if _, err := d.db.ExecContext(ctx,
		`UPDATE run_messages SET retired_at = ?
		 WHERE from_run = ? AND to_run = ? AND correlation_id LIKE ? || '%' AND created_at < ?
		   AND delivery_token = '' AND `+unreadMessage,
		now, from, to, ProvisionalReportPrefix, before,
	); err != nil {
		return fmt.Errorf("store: retire provisional reports: %w", err)
	}
	return nil
}
