package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// ErrTooManyWaiting refuses a launch intent over its member's cap.
var ErrTooManyWaiting = errors.New("store: too many launches already wait")

// LaunchIntentStore keeps the launches that wait for room.
type LaunchIntentStore interface {
	// CreateLaunchIntent records in as waiting under a new session ID.
	// A member's open intent under the same key is returned instead, with
	// existing true. More than maxOpen open intents of the member is
	// ErrTooManyWaiting.
	CreateLaunchIntent(ctx context.Context, in *domain.LaunchIntent, maxOpen int) (intent *domain.LaunchIntent, existing bool, err error)
	GetLaunchIntent(ctx context.Context, id domain.RunID) (*domain.LaunchIntent, error)
	// LaunchIntentByKey returns the member's latest intent under key.
	LaunchIntentByKey(ctx context.Context, member domain.MemberID, key string) (*domain.LaunchIntent, error)
	// LaunchIntents lists intents in the given states, the oldest first;
	// member "" lists every member's.
	LaunchIntents(ctx context.Context, member domain.MemberID, states ...domain.LaunchIntentState) ([]*domain.LaunchIntent, error)
	// ClaimLaunchIntent moves a waiting intent to starting for one attempt.
	// ErrConflict when it is not waiting.
	ClaimLaunchIntent(ctx context.Context, id domain.RunID, at time.Time) error
	// ReleaseLaunchIntent returns a starting intent to waiting with the
	// refusal its attempt got. ErrConflict when it is not starting.
	ReleaseLaunchIntent(ctx context.Context, id domain.RunID, refusal string) error
	// EndLaunchIntent moves an open intent to a final state that is not
	// started, with why. ErrConflict when it is not open.
	EndLaunchIntent(ctx context.Context, id domain.RunID, to domain.LaunchIntentState, reason string, at time.Time) error
	// CreateRunForIntent creates the intent's session and marks the intent
	// started in one transaction, completing its launch receipt.
	// ErrConflict, with nothing written, when the intent is not starting.
	CreateRunForIntent(ctx context.Context, r *domain.Run) error
	// RecoverLaunchIntents settles the intents a stopped server left
	// starting: started when the session exists, waiting otherwise.
	RecoverLaunchIntents(ctx context.Context) error
	// PassLaunchIntents counts one more launch admitted ahead of every
	// waiting intent, and returns how many it passed.
	PassLaunchIntents(ctx context.Context) (int64, error)
}

const launchIntentCols = `id, member_id, workspace_id, idempotency_key, method, params, base_commit, base_branch, via, state, refusal,
	attempts, passed, created_at, attempted_at, ended_at`

func scanLaunchIntent(row interface{ Scan(...any) error }) (*domain.LaunchIntent, error) {
	var in domain.LaunchIntent
	var params string
	var created, attempted int64
	var ended sql.NullInt64
	if err := row.Scan(&in.ID, &in.MemberID, &in.WorkspaceID, &in.IdempotencyKey, &in.Method, &params, &in.BaseCommit, &in.BaseBranch, &in.Via,
		&in.State, &in.Refusal, &in.Attempts, &in.Passed, &created, &attempted, &ended); err != nil {
		return nil, err
	}
	in.Params = []byte(params)
	in.CreatedAt, in.AttemptedAt = time.Unix(0, created).UTC(), time.Unix(0, attempted).UTC()
	if ended.Valid {
		at := time.Unix(0, ended.Int64).UTC()
		in.EndedAt = &at
	}
	return &in, nil
}

func (d *DB) CreateLaunchIntent(ctx context.Context, in *domain.LaunchIntent, maxOpen int) (*domain.LaunchIntent, bool, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, fmt.Errorf("store: create launch intent: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // committed below
	if in.IdempotencyKey != "" {
		prior, err := scanLaunchIntent(tx.QueryRowContext(ctx, `SELECT `+launchIntentCols+` FROM launch_intents
			WHERE member_id = ? AND idempotency_key = ? AND state IN ('waiting', 'starting')`, in.MemberID, in.IdempotencyKey))
		if err == nil {
			return prior, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, false, fmt.Errorf("store: create launch intent: %w", err)
		}
	}
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM launch_intents WHERE member_id = ? AND state IN ('waiting', 'starting')`, in.MemberID).Scan(&open); err != nil {
		return nil, false, fmt.Errorf("store: create launch intent: %w", err)
	}
	if open >= maxOpen {
		return nil, false, ErrTooManyWaiting
	}
	id, ts, err := prepareCreate("run", in.CreatedAt)
	if err != nil {
		return nil, false, err
	}
	out := *in
	out.ID, out.CreatedAt, out.AttemptedAt, out.State = domain.RunID(id), ts, ts, domain.IntentWaiting
	if out.IdempotencyKey == "" {
		out.IdempotencyKey = id
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO launch_intents (`+launchIntentCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, NULL)`,
		out.ID, out.MemberID, out.WorkspaceID, out.IdempotencyKey, out.Method, string(out.Params), out.BaseCommit, out.BaseBranch, out.Via,
		out.State, out.Refusal, ts.UnixNano(), ts.UnixNano()); err != nil {
		return nil, false, fmt.Errorf("store: create launch intent: %w", mapConstraint(err, ErrNotFound))
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("store: create launch intent: %w", err)
	}
	return &out, false, nil
}

func (d *DB) GetLaunchIntent(ctx context.Context, id domain.RunID) (*domain.LaunchIntent, error) {
	in, err := scanLaunchIntent(d.db.QueryRowContext(ctx, `SELECT `+launchIntentCols+` FROM launch_intents WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: launch %s", ErrNotFound, id)
	}
	if err != nil {
		return nil, fmt.Errorf("store: launch intent: %w", err)
	}
	return in, nil
}

func (d *DB) LaunchIntentByKey(ctx context.Context, member domain.MemberID, key string) (*domain.LaunchIntent, error) {
	in, err := scanLaunchIntent(d.db.QueryRowContext(ctx, `SELECT `+launchIntentCols+` FROM launch_intents
		WHERE member_id = ? AND idempotency_key = ? ORDER BY created_at DESC, id DESC LIMIT 1`, member, key))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: launch under idempotency key %q", ErrNotFound, key)
	}
	if err != nil {
		return nil, fmt.Errorf("store: launch intent: %w", err)
	}
	return in, nil
}

func (d *DB) LaunchIntents(ctx context.Context, member domain.MemberID, states ...domain.LaunchIntentState) ([]*domain.LaunchIntent, error) {
	query := `SELECT ` + launchIntentCols + ` FROM launch_intents WHERE (?1 = '' OR member_id = ?1) AND state IN (?` + repeatPlaceholders(len(states)-1) + `)
		ORDER BY created_at, id`
	args := []any{member}
	for _, s := range states {
		args = append(args, s)
	}
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: launch intents: %w", err)
	}
	defer rows.Close()
	var out []*domain.LaunchIntent
	for rows.Next() {
		in, err := scanLaunchIntent(rows)
		if err != nil {
			return nil, fmt.Errorf("store: launch intents: %w", err)
		}
		out = append(out, in)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: launch intents: %w", err)
	}
	return out, nil
}

func (d *DB) moveLaunchIntent(ctx context.Context, what string, id domain.RunID, query string, args ...any) error {
	if err := notFoundOnZeroRows(d.db.ExecContext(ctx, query, args...)); err != nil {
		if errors.Is(err, ErrNotFound) {
			return fmt.Errorf("%w: launch %s is not in the state to %s", ErrConflict, id, what)
		}
		return fmt.Errorf("store: %s launch intent: %w", what, err)
	}
	return nil
}

func (d *DB) ClaimLaunchIntent(ctx context.Context, id domain.RunID, at time.Time) error {
	return d.moveLaunchIntent(ctx, "claim", id,
		`UPDATE launch_intents SET state = 'starting', attempts = attempts + 1, attempted_at = ? WHERE id = ? AND state = 'waiting'`, at.UTC().UnixNano(), id)
}

func (d *DB) ReleaseLaunchIntent(ctx context.Context, id domain.RunID, refusal string) error {
	return d.moveLaunchIntent(ctx, "release", id,
		`UPDATE launch_intents SET state = 'waiting', refusal = ? WHERE id = ? AND state = 'starting'`, refusal, id)
}

func (d *DB) EndLaunchIntent(ctx context.Context, id domain.RunID, to domain.LaunchIntentState, reason string, at time.Time) error {
	if to.Open() || to == domain.IntentStarted {
		return fmt.Errorf("store: end launch intent: %q is not an ending", to)
	}
	return d.moveLaunchIntent(ctx, "end", id,
		`UPDATE launch_intents SET state = ?, refusal = ?, ended_at = ? WHERE id = ? AND state IN ('waiting', 'starting')`, to, reason, at.UTC().UnixNano(), id)
}

func (d *DB) CreateRunForIntent(ctx context.Context, r *domain.Run) error {
	if r == nil || r.ID == "" {
		return errors.New("store: create an intent's session: id is required")
	}
	return d.createRun(ctx, r, true, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE launch_intents SET state = 'started', refusal = '', ended_at = ? WHERE id = ? AND state = 'starting'`,
			time.Now().UTC().UnixNano(), r.ID)
		if err != nil {
			return fmt.Errorf("start launch intent: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return fmt.Errorf("%w: launch %s is no longer starting", ErrConflict, r.ID)
		}
		// The receipt answers a retry of the request with this session.
		if _, err := tx.ExecContext(ctx, `UPDATE run_launch_receipts SET run_id = ?1 WHERE run_id = '' AND (member_id, idempotency_key) IN
			(SELECT member_id, idempotency_key FROM launch_intents WHERE id = ?1)`, r.ID); err != nil {
			return fmt.Errorf("complete the launch receipt: %w", err)
		}
		return nil
	})
}

func (d *DB) RecoverLaunchIntents(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx, `UPDATE launch_intents SET state = 'started', refusal = '', ended_at = ?
		WHERE state = 'starting' AND id IN (SELECT id FROM runs)`, time.Now().UTC().UnixNano()); err != nil {
		return fmt.Errorf("store: recover launch intents: %w", err)
	}
	if _, err := d.db.ExecContext(ctx, `UPDATE launch_intents SET state = 'waiting' WHERE state = 'starting'`); err != nil {
		return fmt.Errorf("store: recover launch intents: %w", err)
	}
	return nil
}

func (d *DB) PassLaunchIntents(ctx context.Context) (int64, error) {
	res, err := d.db.ExecContext(ctx, `UPDATE launch_intents SET passed = passed + 1 WHERE state = 'waiting'`)
	if err != nil {
		return 0, fmt.Errorf("store: pass launch intents: %w", err)
	}
	return res.RowsAffected()
}
