package edgestore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
)

// Device authorization states.
const (
	AuthPending  = "pending"
	AuthApproved = "approved"
	AuthDenied   = "denied"
)

// DeviceAuth is one device authorization request: a client install waiting
// for a person to confirm its user code.
type DeviceAuth struct {
	CodeHash     string
	UserCodeHash string
	Label        string
	Key          string
	// ClientAddr is the address the request came from, shown to the
	// person who confirms it.
	ClientAddr string
	Status     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastPollAt time.Time
	// SigninOrigin is the sign-in origin the request was made at, ""
	// when the edge does not record origins. Redemption copies it to the
	// device.
	SigninOrigin string
}

// CreateDeviceAuth stores a pending authorization and drops expired ones.
func (s *Store) CreateDeviceAuth(ctx context.Context, a DeviceAuth) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_authorizations WHERE expires_at <= ?`,
		unix(a.CreatedAt)); err != nil {
		return fmt.Errorf("edgestore: drop expired device authorizations: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO device_authorizations
		(code_hash, user_code_hash, label, public_key, client_addr, status, created_at, expires_at, last_poll_at, signin_origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, 0, NULLIF(?, ''))`,
		a.CodeHash, a.UserCodeHash, a.Label, a.Key, a.ClientAddr, AuthPending, unix(a.CreatedAt), unix(a.ExpiresAt), a.SigninOrigin); err != nil {
		return fmt.Errorf("edgestore: create device authorization: %w", conflict(err))
	}
	return nil
}

const deviceAuthCols = `code_hash, user_code_hash, label, public_key, client_addr, status,
	created_at, expires_at, last_poll_at, COALESCE(signin_origin, '')`

func scanDeviceAuth(row *sql.Row) (DeviceAuth, error) {
	var a DeviceAuth
	var created, expires, polled int64
	err := row.Scan(&a.CodeHash, &a.UserCodeHash, &a.Label, &a.Key, &a.ClientAddr, &a.Status,
		&created, &expires, &polled, &a.SigninOrigin)
	if errors.Is(err, sql.ErrNoRows) {
		return DeviceAuth{}, ErrNotFound
	}
	if err != nil {
		return DeviceAuth{}, err
	}
	a.CreatedAt, a.ExpiresAt = fromUnix(created), fromUnix(expires)
	if polled != 0 {
		a.LastPollAt = fromUnix(polled)
	}
	return a, nil
}

// PendingDeviceAuth returns the pending, unexpired authorization whose user
// code hashes to userCodeHash.
func (s *Store) PendingDeviceAuth(ctx context.Context, userCodeHash string, now time.Time) (DeviceAuth, error) {
	a, err := scanDeviceAuth(s.db.QueryRowContext(ctx, `SELECT `+deviceAuthCols+`
		FROM device_authorizations WHERE user_code_hash = ? AND status = ? AND expires_at > ?`,
		userCodeHash, AuthPending, unix(now)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: find device authorization: %w", err)
	}
	return a, err
}

// DecideDeviceAuth approves (for accountID) or denies the pending,
// unexpired authorization whose user code hashes to userCodeHash.
func (s *Store) DecideDeviceAuth(ctx context.Context, userCodeHash string, accountID int64, approve bool, now time.Time) error {
	status, account := AuthDenied, sql.NullInt64{}
	if approve {
		status, account = AuthApproved, sql.NullInt64{Int64: accountID, Valid: true}
	}
	err := oneRow(s.db.ExecContext(ctx, `UPDATE device_authorizations SET status = ?, account_id = ?
		WHERE user_code_hash = ? AND status = ? AND expires_at > ?`,
		status, account, userCodeHash, AuthPending, unix(now)))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: decide device authorization: %w", err)
	}
	return err
}

// PollDeviceAuth records a poll at now and returns the authorization as it
// was before, so LastPollAt is the previous poll.
func (s *Store) PollDeviceAuth(ctx context.Context, codeHash string, now time.Time) (DeviceAuth, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	a, err := scanDeviceAuth(tx.QueryRowContext(ctx, `SELECT `+deviceAuthCols+`
		FROM device_authorizations WHERE code_hash = ?`, codeHash))
	if errors.Is(err, ErrNotFound) {
		return DeviceAuth{}, err
	}
	if err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE device_authorizations SET last_poll_at = ? WHERE code_hash = ?`,
		unix(now), codeHash); err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return DeviceAuth{}, fmt.Errorf("edgestore: poll device authorization: %w", err)
	}
	return a, nil
}

// DeleteDeviceAuth drops an authorization that ended denied or expired.
func (s *Store) DeleteDeviceAuth(ctx context.Context, codeHash string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM device_authorizations WHERE code_hash = ?`, codeHash); err != nil {
		return fmt.Errorf("edgestore: delete device authorization: %w", err)
	}
	return nil
}

// Device is a client install holding a device token. AccountID is its
// account's row id; Account.ID the public id.
type Device struct {
	ID         string
	TokenHash  string
	AccountID  int64
	Account    edgeproto.AccountInfo
	Key        string
	Label      string
	CreatedAt  time.Time
	LastUsedAt time.Time
	// SigninOrigin is the sign-in origin the token was issued at, or
	// first seen at for one issued while the edge did not record origins;
	// "" while unknown.
	SigninOrigin string
}

// RedeemDeviceAuth consumes the approved, unexpired authorization under
// codeHash and creates d from it, in one transaction, so an authorization
// yields at most one device. d's account, key and label come from the
// authorization, and its sign-in origin too when record is set.
func (s *Store) RedeemDeviceAuth(ctx context.Context, codeHash string, d Device, record bool, now time.Time) (Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op after Commit
	err = tx.QueryRowContext(ctx, `DELETE FROM device_authorizations
		WHERE code_hash = ? AND status = ? AND expires_at > ?
		RETURNING account_id, public_key, label, CASE WHEN ? THEN COALESCE(signin_origin, '') ELSE '' END`, codeHash, AuthApproved, unix(now), record).
		Scan(&d.AccountID, &d.Key, &d.Label, &d.SigninOrigin)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	d.CreatedAt, d.LastUsedAt = now, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, token_hash, account_id, public_key, label, created_at, last_used_at, signin_origin)
		VALUES (?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''))`, d.ID, d.TokenHash, d.AccountID, d.Key, d.Label, unix(now), unix(now), d.SigninOrigin); err != nil {
		return Device{}, fmt.Errorf("edgestore: create device: %w", err)
	}
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts a WHERE a.id = ?`,
		d.AccountID).Scan(accountDest(&id, &d.Account)...); err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Device{}, fmt.Errorf("edgestore: redeem device authorization: %w", err)
	}
	return d, nil
}

// seenAt records the sign-in origin given twice as a parameter on a row
// that has none, marked as seen rather than issued there. SQLite reads the
// row's old values on the right of every assignment.
const seenAt = `signin_origin_seen = CASE WHEN signin_origin IS NULL AND ? <> '' THEN 1 ELSE signin_origin_seen END,
	signin_origin = COALESCE(signin_origin, NULLIF(?, ''))`

const deviceCols = `d.id, d.token_hash, d.public_key, d.label, d.created_at, d.last_used_at, COALESCE(d.signin_origin, ''), ` + accountCols

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var created, used int64
	dest := append([]any{&d.ID, &d.TokenHash, &d.Key, &d.Label, &created, &used, &d.SigninOrigin}, accountDest(&d.AccountID, &d.Account)...)
	if err := row.Scan(dest...); err != nil {
		return Device{}, err
	}
	d.CreatedAt, d.LastUsedAt = fromUnix(created), fromUnix(used)
	return d, nil
}

// UseDevice returns the device holding the token that hashes to tokenHash,
// when its account is not blocked, and records its use at now. A device
// with no sign-in origin is recorded as seen at signin, unless signin is
// "".
func (s *Store) UseDevice(ctx context.Context, tokenHash, signin string, now time.Time) (Device, error) {
	err := oneRow(s.db.ExecContext(ctx, `UPDATE devices SET last_used_at = ?, `+seenAt+`
		WHERE token_hash = ? AND `+notBlocked("devices.account_id"), unix(now), signin, signin, tokenHash))
	if errors.Is(err, ErrNotFound) {
		return Device{}, err
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: use device: %w", err)
	}
	d, err := scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+`
		FROM devices d JOIN accounts a ON a.id = d.account_id WHERE d.token_hash = ?`, tokenHash))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, ErrNotFound
	}
	if err != nil {
		return Device{}, fmt.Errorf("edgestore: use device: %w", err)
	}
	return d, nil
}

// Devices lists an account's devices, newest first.
func (s *Store) Devices(ctx context.Context, accountID int64) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+`
		FROM devices d JOIN accounts a ON a.id = d.account_id
		WHERE d.account_id = ? ORDER BY d.created_at DESC, d.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("edgestore: list devices: %w", err)
	}
	defer rows.Close() //nolint:errcheck // read-only iteration
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("edgestore: list devices: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("edgestore: list devices: %w", err)
	}
	return out, nil
}

// DeleteDevice revokes one of accountID's devices by deleting it, token
// hash included.
func (s *Store) DeleteDevice(ctx context.Context, accountID int64, deviceID string) error {
	err := oneRow(s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND account_id = ?`, deviceID, accountID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("edgestore: delete device: %w", err)
	}
	return err
}
