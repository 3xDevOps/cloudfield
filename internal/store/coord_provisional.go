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

// CoordReportsSince is run's finalized reports reserved at or after since,
// oldest first.
func (d *DB) CoordReportsSince(ctx context.Context, run domain.RunID, since time.Time) ([]*CoordReport, error) {
	after, err := encodeTime(since)
	if err != nil {
		return nil, fmt.Errorf("store: coord reports since: %w", err)
	}
	rows, err := d.db.QueryContext(ctx,
		`SELECT `+coordReportCols+` FROM coord_reports WHERE run_id = ? AND state = ? AND created_at >= ?
		 ORDER BY created_at, id`, run, CoordReportFinalized, after)
	if err != nil {
		return nil, fmt.Errorf("store: coord reports since: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var reports []*CoordReport
	for rows.Next() {
		report, err := scanCoordReport(rows)
		if err != nil {
			return nil, fmt.Errorf("store: coord reports since: %w", err)
		}
		reports = append(reports, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: coord reports since: %w", err)
	}
	return reports, nil
}

// LastRunMessageDeliveredAt is when an inbox read last returned mail to to
// for the first time, zero when none did.
func (d *DB) LastRunMessageDeliveredAt(ctx context.Context, to domain.RunID) (time.Time, error) {
	var last sql.NullInt64
	if err := d.db.QueryRowContext(ctx, `SELECT MAX(delivered_at) FROM run_messages WHERE to_run = ?`, to).Scan(&last); err != nil {
		return time.Time{}, fmt.Errorf("store: last message delivered to %s: %w", to, err)
	}
	if !last.Valid {
		return time.Time{}, nil
	}
	return decodeTime(last.Int64), nil
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
