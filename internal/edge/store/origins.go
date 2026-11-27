package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// OriginsSchema is the first schema version that records origins.
const OriginsSchema = 2

// recordingGap is the longest time between a recording edge's clean stop
// and its next start with recording on that keeps one recording period. A
// start later than that, or after an unclean stop, begins a new period. A
// shorter run without recording in between writes nothing and so goes
// unseen.
const recordingGap = 10 * time.Minute

// Run is what RecordPairs writes, in one transaction, as a recording run
// starts.
type Run struct {
	// Pairs maps each relay origin the edge serves to its sign-in origin.
	Pairs map[string]string
	// Retire deletes the device tokens, sessions and device authorizations
	// whose sign-in origin is one of RetiredSignIns or that have none.
	Retire         bool
	RetiredSignIns []string
	// MovedSignIns are sign-in origins whose device authorizations are
	// deleted, since the pair that started them is no longer theirs.
	MovedSignIns []string
}

// RecordPairs deletes the sign-ins run retires and records, as of now,
// that the edge serves each relay origin of run.Pairs with the sign-in
// origin it names and records origins, all in one transaction, and
// returns how many sign-ins it retired. A relay origin
// keeps its row after the edge stops serving it, and every pairing ever
// written to origin_pairs, by any binary, stays in origin_pair_history
// (Pairings). The recording period
// goes on when the edge's last recording run stopped cleanly
// (StopRecording) within recordingGap, and starts at now otherwise.
func (s *Store) RecordPairs(ctx context.Context, run Run, now time.Time) (SignIns, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SignIns{}, fmt.Errorf("edgestore: record origin pairs: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	var deleted SignIns
	if run.Retire {
		if deleted, err = deleteSignIns(ctx, tx, run.RetiredSignIns); err != nil {
			return SignIns{}, err
		}
	}
	for _, signin := range run.MovedSignIns {
		if _, err := tx.ExecContext(ctx, `DELETE FROM device_authorizations WHERE signin_origin = ?`, signin); err != nil {
			return SignIns{}, fmt.Errorf("edgestore: delete the device sign-ins of %s: %w", signin, err)
		}
	}
	for relay, signin := range run.Pairs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO origin_pairs (relay_origin, signin_origin, served_at) VALUES (?, ?, ?)
			ON CONFLICT (relay_origin) DO UPDATE SET signin_origin = excluded.signin_origin, served_at = excluded.served_at`,
			relay, signin, unix(now)); err != nil {
			return SignIns{}, fmt.Errorf("edgestore: record origin pair %s %s: %w", signin, relay, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO origin_recording (id, since, started_at) VALUES (1, ?, ?)
		ON CONFLICT (id) DO UPDATE SET started_at = excluded.started_at, stopped_at = NULL,
			since = CASE WHEN stopped_at >= started_at AND stopped_at BETWEEN ? AND ? THEN since ELSE excluded.since END`,
		unix(now), unix(now), unix(now.Add(-recordingGap)), unix(now)); err != nil {
		return SignIns{}, fmt.Errorf("edgestore: record origin pairs: recording period: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return SignIns{}, fmt.Errorf("edgestore: record origin pairs: %w", err)
	}
	return deleted, nil
}

// StopRecording records that the edge, which records origins, stops at
// now.
func (s *Store) StopRecording(ctx context.Context, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE origin_recording SET stopped_at = ?`, unix(now)); err != nil {
		return fmt.Errorf("edgestore: record the end of origin recording: %w", err)
	}
	return nil
}

// useConnectorQuery runs at most once an hour per server and relay origin.
const useConnectorQuery = `INSERT INTO connector_use (server_id, relay_origin, last_used_at)
	SELECT id, ?, ? FROM servers WHERE id = ?
	ON CONFLICT (server_id, relay_origin) DO UPDATE SET last_used_at = excluded.last_used_at`

// UseConnector records a connector request at now for the claimed server
// serverID through the relay origin relay. A server that is not claimed
// records nothing.
func (s *Store) UseConnector(ctx context.Context, serverID, relay string, now time.Time) error {
	if _, err := s.db.ExecContext(ctx, useConnectorQuery, relay, unix(now), serverID); err != nil {
		return fmt.Errorf("edgestore: record connector request to %s through %s: %w", serverID, relay, err)
	}
	return nil
}

// SignIns counts device tokens, browser sessions and pending device
// authorizations.
type SignIns struct {
	Devices        int `json:"devices"`
	Sessions       int `json:"sessions"`
	Authorizations int `json:"authorizations"`
}

var signInTables = []string{"devices", "web_sessions", "device_authorizations"}

// deleteSignIns deletes, in tx, the device tokens, sessions and device
// authorizations whose sign-in origin is one of origins or that have none,
// and returns how many it deleted.
func deleteSignIns(ctx context.Context, tx *sql.Tx, origins []string) (SignIns, error) {
	where := `signin_origin IS NULL`
	if len(origins) > 0 {
		where += ` OR signin_origin IN (?` + strings.Repeat(`, ?`, len(origins)-1) + `)`
	}
	args := make([]any, len(origins))
	for i, o := range origins {
		args[i] = o
	}
	var out SignIns
	for i, n := range []*int{&out.Devices, &out.Sessions, &out.Authorizations} {
		res, err := tx.ExecContext(ctx, `DELETE FROM `+signInTables[i]+` WHERE `+where, args...)
		if err != nil {
			return SignIns{}, fmt.Errorf("edgestore: delete sign-ins: %s: %w", signInTables[i], err)
		}
		deleted, err := res.RowsAffected()
		if err != nil {
			return SignIns{}, fmt.Errorf("edgestore: delete sign-ins: %s: %w", signInTables[i], err)
		}
		*n = int(deleted)
	}
	return out, nil
}

// SignInOrigins lists the sign-in origins that device tokens, sessions and
// device authorizations record.
func (s *Store) SignInOrigins(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT signin_origin FROM devices WHERE signin_origin IS NOT NULL
		UNION SELECT signin_origin FROM web_sessions WHERE signin_origin IS NOT NULL
		UNION SELECT signin_origin FROM device_authorizations WHERE signin_origin IS NOT NULL ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list sign-in origins: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []string
	for rows.Next() {
		var o string
		if err := rows.Scan(&o); err != nil {
			return nil, fmt.Errorf("edgestore: list sign-in origins: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list sign-in origins: %w", err)
	}
	return out, nil
}

// Pairing is a relay origin and a sign-in origin the edge recorded
// serving together, last at ServedAt. Current is set when it is the relay
// origin's latest.
type Pairing struct {
	RelayOrigin, SigninOrigin string
	ServedAt                  time.Time
	Current                   bool
}

// Pairings lists every pairing the edge recorded serving, current and
// earlier. origin_pairs also holds a pairing written to a database
// restored from before origin_pair_history existed.
func (s *Store) Pairings(ctx context.Context) ([]Pairing, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT h.relay_origin, h.signin_origin, MAX(h.served_at), EXISTS (SELECT 1 FROM origin_pairs o
			WHERE o.relay_origin = h.relay_origin AND o.signin_origin = h.signin_origin)
		FROM (SELECT relay_origin, signin_origin, served_at FROM origin_pair_history
			UNION ALL SELECT relay_origin, signin_origin, served_at FROM origin_pairs) h GROUP BY 1, 2 ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list origin pairings: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only
	var out []Pairing
	for rows.Next() {
		var p Pairing
		var servedAt int64
		if err := rows.Scan(&p.RelayOrigin, &p.SigninOrigin, &servedAt, &p.Current); err != nil {
			return nil, fmt.Errorf("edgestore: list origin pairings: %w", err)
		}
		p.ServedAt = fromUnix(servedAt)
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list origin pairings: %w", err)
	}
	return out, nil
}

// Recorded reports whether the edge ever began a recording period.
func (s *Store) Recorded(ctx context.Context) (bool, error) {
	var ok bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM origin_recording)`).Scan(&ok); err != nil {
		return false, fmt.Errorf("edgestore: read the recording period: %w", err)
	}
	return ok, nil
}

// SignInsWithoutOrigin counts the device tokens, sessions and device
// authorizations that have no recorded sign-in origin.
func (s *Store) SignInsWithoutOrigin(ctx context.Context) (SignIns, error) {
	var out SignIns
	if err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM devices WHERE signin_origin IS NULL),
		(SELECT COUNT(*) FROM web_sessions WHERE signin_origin IS NULL),
		(SELECT COUNT(*) FROM device_authorizations WHERE signin_origin IS NULL)`).
		Scan(&out.Devices, &out.Sessions, &out.Authorizations); err != nil {
		return SignIns{}, fmt.Errorf("edgestore: count sign-ins without an origin: %w", err)
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

// SeenServer is a claimed server and its last enrollment or claim.
type SeenServer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OwnerLogin   string    `json:"owner_login"`
	AgentVersion string    `json:"build"`
	LastSeenAt   time.Time `json:"last_seen_at"`
}

// SigninCounts are the device tokens and sessions of one sign-in origin.
type SigninCounts struct {
	// DeviceTokens were issued at the sign-in origin, and
	// DeviceTokensSeen issued while the edge did not record origins and
	// first used there since; by last use.
	DeviceTokens     Counts `json:"device_tokens"`
	DeviceTokensSeen Counts `json:"device_tokens_seen"`
	// SessionAccounts have an unexpired session issued or first seen at
	// the sign-in origin, by its last use.
	SessionAccounts Counts `json:"session_accounts"`
	// DeviceOnlyAccounts have a device token at the sign-in origin and
	// none recorded at another, by its last use; a token with no
	// recorded origin does not count as another.
	DeviceOnlyAccounts Counts `json:"device_only_accounts"`
}

// OriginReport is who the edge last saw on one origin pair.
type OriginReport struct {
	RelayOrigin  string `json:"relay_origin"`
	SigninOrigin string `json:"signin_origin"`
	// SigninCountedUnder is the relay origin of the pair the sign-in
	// origin is counted under when the edge pairs it with that one more
	// recently; this pair's sign-in counts are then zero.
	SigninCountedUnder string `json:"signin_counted_under,omitempty"`
	// Servers last enrolled or claimed at the relay origin.
	Servers []SeenServer `json:"servers"`
	SigninCounts
	// ConnectorServers had connector requests through the relay origin,
	// by the last one.
	ConnectorServers Counts `json:"connector_servers"`
}

// UnpairedSignin is a sign-in origin that device tokens or sessions name
// and that no recorded pair names any more.
type UnpairedSignin struct {
	SigninOrigin string `json:"signin_origin"`
	SigninCounts
}

// Unknown is what has no recorded origin: device tokens and accounts with
// an unexpired session by last use, and claimed servers not seen since the
// edge began recording.
type Unknown struct {
	DeviceTokens    Counts `json:"device_tokens"`
	SessionAccounts Counts `json:"session_accounts"`
	Servers         int    `json:"servers"`
}

// Report is who the edge saw on each origin pair it recorded, ordered by
// relay origin. The last known recording period began at RecordingSince;
// its last run started at RecordingStarted and stopped cleanly at
// RecordingStopped, zero while it runs or after an unclean stop. Each is
// zero when unknown.
type Report struct {
	RecordingSince   time.Time        `json:"recording_since"`
	RecordingStarted time.Time        `json:"recording_started"`
	RecordingStopped time.Time        `json:"recording_stopped"`
	Origins          []OriginReport   `json:"origins"`
	Unpaired         []UnpairedSignin `json:"unpaired"`
	Unknown          Unknown          `json:"unknown"`
}

// ErrNoOrigins reports a database at a schema that records no origins.
// ReadRetiring returns it as a *NoOriginsError.
var ErrNoOrigins = errors.New("edgestore: the database records no origins")

// NoOriginsError is ErrNoOrigins for the database at Path, at schema
// version Schema as read in the report's transaction.
type NoOriginsError struct {
	Path   string
	Schema int
}

func (e *NoOriginsError) Error() string {
	return fmt.Sprintf("%v: %s is at schema version %d", ErrNoOrigins, e.Path, e.Schema)
}

func (e *NoOriginsError) Unwrap() error { return ErrNoOrigins }

// ReadRetiring reports, as of now, who the edge at path saw on each origin
// pair, through a read-only connection and one read transaction, so it
// neither migrates nor locks the database. It returns ErrNoOrigins below
// OriginsSchema.
func ReadRetiring(ctx context.Context, path string, now time.Time) (Report, error) {
	db, err := openRO(path)
	if err != nil {
		return Report{}, err
	}
	defer db.Close() //nolint:errcheck // read-only
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Report{}, fmt.Errorf("edgestore: read origins: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // read-only
	sch, err := readSchema(tx)
	switch {
	case err != nil:
		return Report{}, err
	case sch.Version < OriginsSchema:
		return Report{}, &NoOriginsError{Path: path, Schema: sch.Version}
	case sch.CompatFloor > SchemaVersion():
		return Report{}, fmt.Errorf("edgestore: %s is at schema version %d with compat floor %d, and this binary supports %d",
			path, sch.Version, sch.CompatFloor, SchemaVersion())
	}
	return retiring(ctx, tx, now)
}

func retiring(ctx context.Context, tx *sql.Tx, now time.Time) (Report, error) {
	out := Report{Origins: []OriginReport{}, Unpaired: []UnpairedSignin{}}
	var since, started, stopped sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT since, started_at, stopped_at FROM origin_recording`).Scan(&since, &started, &stopped)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Report{}, fmt.Errorf("edgestore: read the recording period: %w", err)
	}
	for _, t := range []struct {
		dst *time.Time
		v   sql.NullInt64
	}{{&out.RecordingSince, since}, {&out.RecordingStarted, started}, {&out.RecordingStopped, stopped}} {
		if t.v.Valid {
			*t.dst = fromUnix(t.v.Int64)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.relay_origin, p.signin_origin, (
			SELECT o.relay_origin FROM origin_pairs o WHERE o.signin_origin = p.signin_origin
			ORDER BY o.served_at DESC, o.relay_origin LIMIT 1)
		FROM origin_pairs p ORDER BY p.relay_origin`)
	if err != nil {
		return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	for rows.Next() {
		var r OriginReport
		var owner string
		if err = rows.Scan(&r.RelayOrigin, &r.SigninOrigin, &owner); err != nil {
			rows.Close() //nolint:errcheck // the scan error takes precedence
			return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
		}
		if owner != r.RelayOrigin {
			r.SigninCountedUnder = owner
		}
		out.Origins = append(out.Origins, r)
	}
	if err = errors.Join(rows.Err(), rows.Close()); err != nil {
		return Report{}, fmt.Errorf("edgestore: read origin pairs: %w", err)
	}
	for i := range out.Origins {
		r := &out.Origins[i]
		if err = servers(ctx, tx, r); err != nil {
			return Report{}, fmt.Errorf("edgestore: report on %s: %w", r.RelayOrigin, err)
		}
		if err = count(ctx, tx, &r.ConnectorServers, now, `SELECT last_used_at FROM connector_use WHERE relay_origin = ?`, r.RelayOrigin); err != nil {
			return Report{}, fmt.Errorf("edgestore: report on %s: %w", r.RelayOrigin, err)
		}
		if r.SigninCountedUnder == "" {
			if err = signinCounts(ctx, tx, &r.SigninCounts, r.SigninOrigin, now); err != nil {
				return Report{}, fmt.Errorf("edgestore: report on %s: %w", r.SigninOrigin, err)
			}
		}
	}
	unpaired, err := tx.QueryContext(ctx, `SELECT signin_origin FROM devices WHERE signin_origin IS NOT NULL
		UNION SELECT signin_origin FROM web_sessions WHERE signin_origin IS NOT NULL
		EXCEPT SELECT signin_origin FROM origin_pairs ORDER BY 1`)
	if err != nil {
		return Report{}, fmt.Errorf("edgestore: read unpaired sign-in origins: %w", err)
	}
	for unpaired.Next() {
		var u UnpairedSignin
		if err = unpaired.Scan(&u.SigninOrigin); err != nil {
			unpaired.Close() //nolint:errcheck // the scan error takes precedence
			return Report{}, fmt.Errorf("edgestore: read unpaired sign-in origins: %w", err)
		}
		out.Unpaired = append(out.Unpaired, u)
	}
	if err = errors.Join(unpaired.Err(), unpaired.Close()); err != nil {
		return Report{}, fmt.Errorf("edgestore: read unpaired sign-in origins: %w", err)
	}
	for i := range out.Unpaired {
		u := &out.Unpaired[i]
		if err = signinCounts(ctx, tx, &u.SigninCounts, u.SigninOrigin, now); err != nil {
			return Report{}, fmt.Errorf("edgestore: report on %s: %w", u.SigninOrigin, err)
		}
	}
	if err = count(ctx, tx, &out.Unknown.DeviceTokens, now, `SELECT last_used_at FROM devices WHERE signin_origin IS NULL`); err != nil {
		return Report{}, fmt.Errorf("edgestore: report on sign-ins without an origin: %w", err)
	}
	if err = count(ctx, tx, &out.Unknown.SessionAccounts, now, `SELECT MAX(expires_at) - ? FROM web_sessions
		WHERE signin_origin IS NULL AND expires_at > ? GROUP BY account_id`, int64(edgeproto.SessionIdle), unix(now)); err != nil {
		return Report{}, fmt.Errorf("edgestore: report on sign-ins without an origin: %w", err)
	}
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE relay_origin IS NULL`).Scan(&out.Unknown.Servers); err != nil {
		return Report{}, fmt.Errorf("edgestore: report on servers without an origin: %w", err)
	}
	return out, nil
}

func servers(ctx context.Context, tx *sql.Tx, r *OriginReport) error {
	rows, err := tx.QueryContext(ctx, `SELECT s.id, s.name, COALESCE(a.login, ''), COALESCE(s.agent_version, ''), s.enrolled_at
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
	return errors.Join(rows.Err(), rows.Close())
}

func signinCounts(ctx context.Context, tx *sql.Tx, c *SigninCounts, signin string, now time.Time) error {
	for _, q := range []struct {
		dst   *Counts
		query string
		args  []any
	}{
		{&c.DeviceTokens, `SELECT last_used_at FROM devices WHERE signin_origin = ? AND signin_origin_seen IS NULL`, []any{signin}},
		{&c.DeviceTokensSeen, `SELECT last_used_at FROM devices WHERE signin_origin = ? AND signin_origin_seen = 1`, []any{signin}},
		{&c.SessionAccounts, `SELECT MAX(expires_at) - ? FROM web_sessions WHERE signin_origin = ? AND expires_at > ? GROUP BY account_id`,
			[]any{int64(edgeproto.SessionIdle), signin, unix(now)}},
		{&c.DeviceOnlyAccounts, `SELECT MAX(last_used_at) FROM devices WHERE signin_origin IS NOT NULL
			GROUP BY account_id HAVING MIN(signin_origin) = ? AND MAX(signin_origin) = ?`, []any{signin, signin}},
	} {
		if err := count(ctx, tx, q.dst, now, q.query, q.args...); err != nil {
			return err
		}
	}
	return nil
}

// count fills dst from query, which returns one last-use time per thing
// counted.
func count(ctx context.Context, tx *sql.Tx, dst *Counts, now time.Time, query string, args ...any) error {
	rows, err := tx.QueryContext(ctx, query, args...)
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
