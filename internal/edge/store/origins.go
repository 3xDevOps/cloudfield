package edgestore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// RecordPairs records that the edge serves each relay origin with the
// sign-in origin it names, as of now. A relay origin keeps its row, and
// when it was first recorded, after the edge stops serving it.
func (s *Store) RecordPairs(ctx context.Context, relayToSignin map[string]string, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("edgestore: record origin pairs: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	for relay, signin := range relayToSignin {
		if _, err := tx.ExecContext(ctx, `INSERT INTO origin_pairs (relay_origin, signin_origin, first_served_at, served_at) VALUES (?, ?, ?, ?)
			ON CONFLICT (relay_origin) DO UPDATE SET signin_origin = excluded.signin_origin, served_at = excluded.served_at`,
			relay, signin, unix(now), unix(now)); err != nil {
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

// Signins counts device tokens, browser sessions and pending device
// authorizations.
type Signins struct {
	Devices        int `json:"devices"`
	Sessions       int `json:"sessions"`
	Authorizations int `json:"authorizations"`
}

var signinTables = []string{"devices", "web_sessions", "device_authorizations"}

// DeleteSignins deletes, in one transaction, the device tokens, sessions
// and device authorizations whose sign-in origin is one of origins or
// that have none, and returns how many it deleted.
func (s *Store) DeleteSignins(ctx context.Context, origins []string) (Signins, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Signins{}, fmt.Errorf("edgestore: delete sign-ins: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	where := `signin_origin IS NULL`
	if len(origins) > 0 {
		where += ` OR signin_origin IN (?` + strings.Repeat(`, ?`, len(origins)-1) + `)`
	}
	args := make([]any, len(origins))
	for i, o := range origins {
		args[i] = o
	}
	var out Signins
	for i, n := range []*int{&out.Devices, &out.Sessions, &out.Authorizations} {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+signinTables[i]+` WHERE `+where, args...)
		if err != nil {
			return Signins{}, fmt.Errorf("edgestore: delete sign-ins: %s: %w", signinTables[i], err)
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return Signins{}, fmt.Errorf("edgestore: delete sign-ins: %s: %w", signinTables[i], err)
		}
		*n = int(deleted)
	}
	if err := tx.Commit(); err != nil {
		return Signins{}, fmt.Errorf("edgestore: delete sign-ins: %w", err)
	}
	return out, nil
}

// SigninsWithoutOrigin counts the device tokens, sessions and device
// authorizations that have no recorded sign-in origin.
func (s *Store) SigninsWithoutOrigin(ctx context.Context) (Signins, error) {
	var out Signins
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM devices WHERE signin_origin IS NULL),
		(SELECT COUNT(*) FROM web_sessions WHERE signin_origin IS NULL),
		(SELECT COUNT(*) FROM device_authorizations WHERE signin_origin IS NULL)`).
		Scan(&out.Devices, &out.Sessions, &out.Authorizations); err != nil {
		return Signins{}, fmt.Errorf("edgestore: count sign-ins without an origin: %w", err)
	}
	return out, nil
}

// Counts are how many were last used within a day, 7 days and 30 days of
// a report, and in all.
type Counts struct {
	Day   int `json:"1d"`
	Week  int `json:"7d"`
	Month int `json:"30d"`
	Total int `json:"total"`
}

// SeenServer is a claimed server and its last enrollment.
type SeenServer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OwnerLogin   string    `json:"owner_login"`
	AgentVersion string    `json:"build"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// OriginReport is who the edge last saw on one origin pair.
type OriginReport struct {
	RelayOrigin  string `json:"relay_origin"`
	SigninOrigin string `json:"signin_origin"`
	// Servers last enrolled at the relay origin.
	Servers []SeenServer `json:"servers"`
	// DeviceTokens were issued at the sign-in origin, and
	// DeviceTokensSeen issued while the edge did not record origins and
	// first used there since; by last use.
	DeviceTokens     Counts `json:"device_tokens"`
	DeviceTokensSeen Counts `json:"device_tokens_seen"`
	// SessionAccounts have an unexpired session issued or first seen at
	// the sign-in origin, by its last use.
	SessionAccounts Counts `json:"session_accounts"`
	// DeviceOnlyAccounts have a device token at the sign-in origin and
	// none recorded at another, by its last use.
	DeviceOnlyAccounts Counts `json:"device_only_accounts"`
	// ConnectorServers had connector requests through the relay origin,
	// by the last one.
	ConnectorServers Counts `json:"connector_servers"`
}

// Unknown is what has no recorded origin: device tokens and accounts with
// an unexpired session by last use, and claimed servers that have not
// enrolled since the edge began recording.
type Unknown struct {
	DeviceTokens    Counts `json:"device_tokens"`
	SessionAccounts Counts `json:"session_accounts"`
	Servers         int    `json:"servers"`
}

// Report is who the edge saw on each origin pair it recorded, ordered by
// relay origin, since RecordingSince, the first time it recorded a pair
// (zero when it never did).
type Report struct {
	RecordingSince time.Time      `json:"recording_since"`
	Origins        []OriginReport `json:"origins"`
	Unknown        Unknown        `json:"unknown"`
}

// Retiring reports, as of now, who the edge saw on each origin pair.
func (s *Store) Retiring(ctx context.Context, now time.Time) (Report, error) {
	out := Report{Origins: []OriginReport{}}
	var since *int64
	if err := s.db.QueryRowContext(ctx, `SELECT MIN(first_served_at) FROM origin_pairs`).Scan(&since); err != nil {
		return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	if since != nil {
		out.RecordingSince = fromUnix(*since)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT relay_origin, signin_origin FROM origin_pairs ORDER BY relay_origin`)
	if err != nil {
		return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	for rows.Next() {
		var r OriginReport
		if err := rows.Scan(&r.RelayOrigin, &r.SigninOrigin); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
		}
		out.Origins = append(out.Origins, r)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	for i := range out.Origins {
		if err := s.fillReport(ctx, &out.Origins[i], now); err != nil {
			return Report{}, fmt.Errorf("edgestore: report on %s: %w", out.Origins[i].RelayOrigin, err)
		}
	}
	idle := int64(edgeproto.SessionIdle)
	for _, c := range []countQuery{
		{&out.Unknown.DeviceTokens, `SELECT last_used_at FROM devices WHERE signin_origin IS NULL`, nil},
		{&out.Unknown.SessionAccounts, `SELECT MAX(expires_at) - ? FROM web_sessions WHERE signin_origin IS NULL AND expires_at > ?
			GROUP BY account_id`, []any{idle, unix(now)}},
	} {
		if err := s.count(ctx, c, now); err != nil {
			return Report{}, fmt.Errorf("edgestore: report on sign-ins without an origin: %w", err)
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE relay_origin IS NULL`).Scan(&out.Unknown.Servers); err != nil {
		return Report{}, fmt.Errorf("edgestore: report on servers without an origin: %w", err)
	}
	return out, nil
}

type countQuery struct {
	dst   *Counts
	query string
	args  []any
}

func (s *Store) fillReport(ctx context.Context, r *OriginReport, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.name, COALESCE(a.login, ''), COALESCE(s.agent_version, ''), s.enrolled_at
		FROM servers s LEFT JOIN accounts a ON a.id = s.owner_account_id
		WHERE s.relay_origin = ? ORDER BY s.id`, r.RelayOrigin)
	if err != nil {
		return err
	}
	r.Servers = []SeenServer{}
	for rows.Next() {
		var e SeenServer
		var at int64
		if err := rows.Scan(&e.ID, &e.Name, &e.OwnerLogin, &e.AgentVersion, &at); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return err
		}
		e.LastSeenAt = fromUnix(at)
		r.Servers = append(r.Servers, e)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, c := range []countQuery{
		{&r.DeviceTokens, `SELECT last_used_at FROM devices WHERE signin_origin = ? AND signin_origin_seen IS NULL`, []any{r.SigninOrigin}},
		{&r.DeviceTokensSeen, `SELECT last_used_at FROM devices WHERE signin_origin = ? AND signin_origin_seen = 1`, []any{r.SigninOrigin}},
		{&r.SessionAccounts, `SELECT MAX(expires_at) - ? FROM web_sessions WHERE signin_origin = ? AND expires_at > ? GROUP BY account_id`,
			[]any{int64(edgeproto.SessionIdle), r.SigninOrigin, unix(now)}},
		{&r.DeviceOnlyAccounts, `SELECT MAX(d.last_used_at) FROM devices d WHERE d.signin_origin = ? AND NOT EXISTS (
			SELECT 1 FROM devices o WHERE o.account_id = d.account_id AND o.signin_origin IS NOT NULL AND o.signin_origin <> d.signin_origin)
			GROUP BY d.account_id`, []any{r.SigninOrigin}},
		{&r.ConnectorServers, `SELECT last_used_at FROM connector_use WHERE relay_origin = ?`, []any{r.RelayOrigin}},
	} {
		if err := s.count(ctx, c, now); err != nil {
			return err
		}
	}
	return nil
}

// count fills c.dst from c.query, which returns one last-use time per
// thing counted.
func (s *Store) count(ctx context.Context, c countQuery, now time.Time) error {
	rows, err := s.db.QueryContext(ctx, c.query, c.args...)
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
		c.dst.Total++
		if age <= 30*24*time.Hour {
			c.dst.Month++
		}
		if age <= 7*24*time.Hour {
			c.dst.Week++
		}
		if age <= 24*time.Hour {
			c.dst.Day++
		}
	}
	return errors.Join(rows.Err(), rows.Close())
}
