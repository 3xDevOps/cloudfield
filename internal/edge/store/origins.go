package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// RecordPairs records that the edge serves each relay origin with the
// sign-in origin it names, as of now. A relay origin keeps its row after
// the edge stops serving it.
func (s *Store) RecordPairs(ctx context.Context, relayToSignin map[string]string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: record origin pairs: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for relay, signin := range relayToSignin {
		if _, err := tx.ExecContext(ctx, `INSERT INTO origin_pairs (relay_origin, signin_origin, served_at) VALUES (?, ?, ?)
			ON CONFLICT (relay_origin) DO UPDATE SET signin_origin = excluded.signin_origin, served_at = excluded.served_at`,
			relay, signin, unix(now)); err != nil {
			return fmt.Errorf("edgestore: record origin pair %s %s: %w", signin, relay, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("edgestore: record origin pairs: %w", err)
	}
	return nil
}

// UseConnector records a connector request at now for the claimed server
// serverID through the relay origin relay. A server that is not claimed
// records nothing.
func (s *Store) UseConnector(ctx context.Context, serverID, relay string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `INSERT INTO connector_use (server_id, relay_origin, last_used_at)
		SELECT id, ?, ? FROM servers WHERE id = ?
		ON CONFLICT (server_id, relay_origin) DO UPDATE SET last_used_at = excluded.last_used_at`,
		relay, unix(now), serverID); err != nil {
		return fmt.Errorf("edgestore: record connector request to %s through %s: %w", serverID, relay, err)
	}
	return nil
}

// Counts are how many were last used within a day, 7 days and 30 days of
// a report, and in all.
type Counts struct {
	Day   int `json:"1d"`
	Week  int `json:"7d"`
	Month int `json:"30d"`
	Total int `json:"total"`
}

// EnrolledServer is a claimed server and its last enrollment.
type EnrolledServer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OwnerLogin   string    `json:"owner_login"`
	AgentVersion string    `json:"build"`
	EnrolledAt   time.Time `json:"last_enrolled_at"`
}

// OriginReport is who is left on one origin pair.
type OriginReport struct {
	RelayOrigin  string `json:"relay_origin"`
	SigninOrigin string `json:"signin_origin"`
	// Servers last enrolled at the relay origin.
	Servers []EnrolledServer `json:"servers"`
	// DeviceTokens issued at the sign-in origin, by their last use.
	DeviceTokens Counts `json:"device_tokens"`
	// SessionAccounts have an unexpired session issued at the sign-in
	// origin, by its last use.
	SessionAccounts Counts `json:"session_accounts"`
	// DeviceOnlyAccounts have a device token issued at the sign-in
	// origin and none recorded at another, by its last use.
	DeviceOnlyAccounts Counts `json:"device_only_accounts"`
	// ConnectorServers had connector requests through the relay origin,
	// by the last one.
	ConnectorServers Counts `json:"connector_servers"`
}

// OriginReports reports every origin pair the edge has recorded
// (RecordPairs), ordered by relay origin, as of now.
func (s *Store) OriginReports(ctx context.Context, now time.Time) ([]OriginReport, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT relay_origin, signin_origin FROM origin_pairs ORDER BY relay_origin`)
	if err != nil {
		return nil, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	var out []OriginReport
	for rows.Next() {
		var r OriginReport
		if err := rows.Scan(&r.RelayOrigin, &r.SigninOrigin); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return nil, fmt.Errorf("edgestore: read origin pairs: %w", err)
		}
		out = append(out, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	for i := range out {
		if err := s.fillReport(ctx, &out[i], now); err != nil {
			return nil, fmt.Errorf("edgestore: report on %s: %w", out[i].RelayOrigin, err)
		}
	}
	return out, nil
}

func (s *Store) fillReport(ctx context.Context, r *OriginReport, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.name, COALESCE(a.login, ''), COALESCE(s.agent_version, ''), s.enrolled_at
		FROM servers s LEFT JOIN accounts a ON a.id = s.owner_account_id
		WHERE s.relay_origin = ? ORDER BY s.id`, r.RelayOrigin)
	if err != nil {
		return err
	}
	r.Servers = []EnrolledServer{}
	for rows.Next() {
		var e EnrolledServer
		var at int64
		if err := rows.Scan(&e.ID, &e.Name, &e.OwnerLogin, &e.AgentVersion, &at); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return err
		}
		e.EnrolledAt = fromUnix(at)
		r.Servers = append(r.Servers, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, c := range []struct {
		dst   *Counts
		query string
		args  []any
	}{
		{&r.DeviceTokens, `SELECT last_used_at FROM devices WHERE signin_origin = ?`, []any{r.SigninOrigin}},
		{&r.SessionAccounts, `SELECT MAX(expires_at) - ? FROM web_sessions WHERE signin_origin = ? AND expires_at > ? GROUP BY account_id`,
			[]any{int64(edgeproto.SessionIdle), r.SigninOrigin, unix(now)}},
		{&r.DeviceOnlyAccounts, `SELECT MAX(d.last_used_at) FROM devices d WHERE d.signin_origin = ? AND NOT EXISTS (
			SELECT 1 FROM devices o WHERE o.account_id = d.account_id AND o.signin_origin IS NOT NULL AND o.signin_origin <> d.signin_origin)
			GROUP BY d.account_id`, []any{r.SigninOrigin}},
		{&r.ConnectorServers, `SELECT last_used_at FROM connector_use WHERE relay_origin = ?`, []any{r.RelayOrigin}},
	} {
		if err := s.count(ctx, c.dst, now, c.query, c.args...); err != nil {
			return err
		}
	}
	return nil
}

// count fills dst from query, which returns one last-use time per thing
// counted.
func (s *Store) count(ctx context.Context, dst *Counts, now time.Time, query string, args ...any) error {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var used int64
		if err := rows.Scan(&used); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return err
		}
		age := now.Sub(fromUnix(used))
		dst.Total++
		if age <= 30*24*time.Hour {
			dst.Month++
		}
		if age <= 7*24*time.Hour {
			dst.Week++
		}
		if age <= 24*time.Hour {
			dst.Day++
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}

// UnaddressedDevice is a device token with no recorded sign-in origin.
type UnaddressedDevice struct {
	ID         string
	Account    edgeproto.Account
	Label      string
	LastUsedAt time.Time
}

// UnaddressedDevices lists the device tokens with no recorded sign-in
// origin last used before before, oldest first.
func (s *Store) UnaddressedDevices(ctx context.Context, before time.Time) ([]UnaddressedDevice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT d.id, a.provider, a.subject, a.email, a.login, d.label, d.last_used_at
		FROM devices d JOIN accounts a ON a.id = d.account_id
		WHERE d.signin_origin IS NULL AND d.last_used_at < ? ORDER BY d.last_used_at, d.id`, unix(before))
	if err != nil {
		return nil, fmt.Errorf("edgestore: list devices without a sign-in origin: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []UnaddressedDevice
	for rows.Next() {
		var d UnaddressedDevice
		var used int64
		a := &d.Account
		if err := rows.Scan(&d.ID, &a.Provider, &a.Subject, &a.Email, &a.Login, &d.Label, &used); err != nil {
			return nil, fmt.Errorf("edgestore: list devices without a sign-in origin: %w", err)
		}
		d.LastUsedAt = fromUnix(used)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list devices without a sign-in origin: %w", err)
	}
	return out, nil
}

// EndUnaddressedDevices deletes the devices ids that still have no
// recorded sign-in origin and were last used before before, and returns
// the ids it deleted: a device used since it was listed stays.
func (s *Store) EndUnaddressedDevices(ctx context.Context, ids []string, before time.Time) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("edgestore: end devices: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var ended []string
	for _, id := range ids {
		var got string
		err := tx.QueryRowContext(ctx, `DELETE FROM devices WHERE id = ? AND signin_origin IS NULL AND last_used_at < ? RETURNING id`,
			id, unix(before)).Scan(&got)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("edgestore: end device %s: %w", id, err)
		}
		ended = append(ended, got)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("edgestore: end devices: %w", err)
	}
	return ended, nil
}
