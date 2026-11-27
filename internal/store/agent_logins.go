package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// AgentLoginStore keeps a member's logins of an agent: the default login,
// in their home, and the logins they added. A member who added none has no
// row, and every query is keyed by the member, so another member's login id
// reads as not found.
type AgentLoginStore interface {
	// ListAgentLogins returns member's logins of agent in order; none when
	// they added none.
	ListAgentLogins(ctx context.Context, member domain.MemberID, agent string) ([]domain.AgentLogin, error)
	// AddAgentLogin adds login, automatic and shared, just before the login
	// before, or last when before is empty, first writing the default
	// login's row when it is the member's first. ErrConflict when its name
	// or id is taken.
	AddAgentLogin(ctx context.Context, login domain.AgentLogin, before domain.LoginID) (domain.AgentLogin, error)
	// RenameAgentLogin renames an added login; ErrConflict when the name is
	// taken.
	RenameAgentLogin(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, name string) error
	// DeleteAgentLogin deletes an added login, and the default login's row
	// with the last one, so a member left with none has no row.
	DeleteAgentLogin(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID) error
	// OrderAgentLogins puts member's logins of agent in the order of ids,
	// which names each of them once; ErrConflict otherwise.
	OrderAgentLogins(ctx context.Context, member domain.MemberID, agent string, ids []domain.LoginID) error
	SetAgentLoginAutomatic(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, on bool) error
	// SetAgentLoginShared sets whether member's account shares reach the
	// login.
	SetAgentLoginShared(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, on bool) error
	// AgentLoginOwner returns the member who holds the added login id of
	// agent; ErrNotFound when none does.
	AgentLoginOwner(ctx context.Context, agent string, id domain.LoginID) (domain.MemberID, error)
}

// NewLoginID returns a new added login's id.
func NewLoginID() (domain.LoginID, error) {
	id, err := newID("lgn")
	return domain.LoginID(id), err
}

const agentLoginColumns = `member_id, agent, id, name, position, automatic, shared, created_at`

func scanAgentLogin(row interface{ Scan(...any) error }) (*domain.AgentLogin, error) {
	var (
		l         domain.AgentLogin
		automatic int
		shared    int
		created   int64
	)
	if err := row.Scan(&l.MemberID, &l.Agent, &l.ID, &l.Name, &l.Position, &automatic, &shared, &created); err != nil {
		return nil, err
	}
	l.Automatic, l.Shared, l.CreatedAt = automatic != 0, shared != 0, decodeTime(created)
	return &l, nil
}

func (d *DB) ListAgentLogins(ctx context.Context, member domain.MemberID, agent string) ([]domain.AgentLogin, error) {
	logins, err := listAgentLogins(ctx, d.db, member, agent)
	if err != nil {
		return nil, fmt.Errorf("store: list logins: %w", err)
	}
	return logins, nil
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listAgentLogins(ctx context.Context, q queryer, member domain.MemberID, agent string) ([]domain.AgentLogin, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+agentLoginColumns+` FROM agent_logins
		WHERE member_id = ? AND agent = ? ORDER BY position, created_at`, member, agent)
	if err != nil {
		return nil, err
	}
	logins, err := collect(rows, scanAgentLogin)
	if err != nil {
		return nil, err
	}
	return derefAll(logins), nil
}

func (d *DB) AddAgentLogin(ctx context.Context, login domain.AgentLogin, before domain.LoginID) (domain.AgentLogin, error) {
	if !login.ID.Added() {
		return domain.AgentLogin{}, fmt.Errorf("store: add login: %q is not an added login's id", login.ID)
	}
	created, timeErr := encodeTime(login.CreatedAt)
	if timeErr != nil {
		return domain.AgentLogin{}, fmt.Errorf("store: add login: %w", timeErr)
	}
	err := d.inTx(ctx, func(tx *sql.Tx) error {
		// A sign-in that ends after its member was removed or merged into
		// another adds nothing: no one could list, use or remove it.
		var mergedInto string
		err := tx.QueryRowContext(ctx, `SELECT merged_into FROM members WHERE id = ?`, login.MemberID).Scan(&mergedInto)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && mergedInto != "") {
			return fmt.Errorf("%w: member %s", ErrNotFound, login.MemberID)
		}
		if err != nil {
			return err
		}
		var next int
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(position) + 1, 0) FROM agent_logins WHERE member_id = ? AND agent = ?`,
			login.MemberID, login.Agent).Scan(&next); err != nil {
			return err
		}
		insert := `INSERT INTO agent_logins (` + agentLoginColumns + `) VALUES (?, ?, ?, ?, ?, 1, 1, ?)`
		if next == 0 {
			if _, err := tx.ExecContext(ctx, insert, login.MemberID, login.Agent, domain.DefaultLogin, domain.DefaultLogin, 0, created); err != nil {
				return mapConstraint(err, ErrNotFound)
			}
			next = 1
		}
		if before != "" {
			// A login removed meanwhile is no place to go before: the new one
			// goes last.
			var at int
			err := tx.QueryRowContext(ctx, `SELECT position FROM agent_logins WHERE member_id = ? AND agent = ? AND id = ?`,
				login.MemberID, login.Agent, before).Scan(&at)
			switch {
			case errors.Is(err, sql.ErrNoRows):
			case err != nil:
				return err
			default:
				next = at
				if _, err := tx.ExecContext(ctx, `UPDATE agent_logins SET position = position + 1 WHERE member_id = ? AND agent = ? AND position >= ?`,
					login.MemberID, login.Agent, next); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, insert, login.MemberID, login.Agent, login.ID, login.Name, next, created); err != nil {
			return mapConstraint(err, ErrNotFound)
		}
		login.Position, login.Automatic, login.Shared = next, true, true
		return nil
	})
	if err != nil {
		return domain.AgentLogin{}, fmt.Errorf("store: add login: %w", err)
	}
	return login, nil
}

func (d *DB) RenameAgentLogin(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, name string) error {
	if !id.Added() {
		return fmt.Errorf("store: rename login: %w", ErrNotFound)
	}
	res, err := d.db.ExecContext(ctx, `UPDATE agent_logins SET name = ? WHERE member_id = ? AND agent = ? AND id = ?`, name, member, agent, id)
	if err != nil {
		return fmt.Errorf("store: rename login: %w", mapConstraint(err, ErrNotFound))
	}
	return requireRow(res, "rename login")
}

func (d *DB) DeleteAgentLogin(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID) error {
	if !id.Added() {
		return fmt.Errorf("store: remove login: %w", ErrNotFound)
	}
	err := d.inTx(ctx, func(tx *sql.Tx) error {
		res, delErr := tx.ExecContext(ctx, `DELETE FROM agent_logins WHERE member_id = ? AND agent = ? AND id = ?`, member, agent, id)
		if delErr != nil {
			return delErr
		}
		if rowErr := requireRow(res, "remove login"); rowErr != nil {
			return rowErr
		}
		_, delErr = tx.ExecContext(ctx, `DELETE FROM agent_logins WHERE member_id = ?1 AND agent = ?2 AND id = ?3
			AND NOT EXISTS (SELECT 1 FROM agent_logins WHERE member_id = ?1 AND agent = ?2 AND id <> ?3)`, member, agent, domain.DefaultLogin)
		return delErr
	})
	if err != nil {
		return fmt.Errorf("store: remove login: %w", err)
	}
	return nil
}

func (d *DB) OrderAgentLogins(ctx context.Context, member domain.MemberID, agent string, ids []domain.LoginID) error {
	err := d.inTx(ctx, func(tx *sql.Tx) error {
		logins, err := listAgentLogins(ctx, tx, member, agent)
		if err != nil {
			return err
		}
		if len(logins) == 0 {
			return ErrNotFound
		}
		have := make([]domain.LoginID, len(logins))
		for i, l := range logins {
			have[i] = l.ID
		}
		want := slices.Clone(ids)
		slices.Sort(have)
		slices.Sort(want)
		if !slices.Equal(have, want) {
			return fmt.Errorf("%w: the order must name each of your logins once", ErrConflict)
		}
		for i, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE agent_logins SET position = ? WHERE member_id = ? AND agent = ? AND id = ?`, i, member, agent, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: order logins: %w", err)
	}
	return nil
}

func (d *DB) SetAgentLoginAutomatic(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, on bool) error {
	res, err := d.db.ExecContext(ctx, `UPDATE agent_logins SET automatic = ? WHERE member_id = ? AND agent = ? AND id = ?`, boolToInt(on), member, agent, id)
	if err != nil {
		return fmt.Errorf("store: set login automatic: %w", err)
	}
	return requireRow(res, "set login automatic")
}

func (d *DB) SetAgentLoginShared(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID, on bool) error {
	res, err := d.db.ExecContext(ctx, `UPDATE agent_logins SET shared = ? WHERE member_id = ? AND agent = ? AND id = ?`, boolToInt(on), member, agent, id)
	if err != nil {
		return fmt.Errorf("store: set login shared: %w", err)
	}
	return requireRow(res, "set login shared")
}

func (d *DB) AgentLoginOwner(ctx context.Context, agent string, id domain.LoginID) (domain.MemberID, error) {
	if !id.Added() {
		return "", fmt.Errorf("store: login owner: %w", ErrNotFound)
	}
	var member domain.MemberID
	err := d.db.QueryRowContext(ctx, `SELECT member_id FROM agent_logins WHERE agent = ? AND id = ?`, agent, id).Scan(&member)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("store: login owner: %w", ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("store: login owner: %w", err)
	}
	return member, nil
}

// mergeLogins lists member's logins of every agent, for a merge plan.
func mergeLogins(ctx context.Context, tx *sql.Tx, member domain.MemberID) ([]domain.AgentLogin, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+agentLoginColumns+` FROM agent_logins WHERE member_id = ? ORDER BY agent, position, id`, member)
	if err != nil {
		return nil, err
	}
	logins, err := collect(rows, scanAgentLogin)
	if err != nil {
		return nil, err
	}
	return derefAll(logins), nil
}

// mergeAgentLogins gives survivor absorbed's added logins, after its own,
// renaming one whose name survivor uses, each off for shares: survivor never
// chose to share them. absorbed's default login is its home, which the
// merge archives, so its row goes.
func mergeAgentLogins(ctx context.Context, tx *sql.Tx, survivor, absorbed domain.MemberID) error {
	agents, err := queryStrings(ctx, tx, `SELECT DISTINCT agent FROM agent_logins WHERE member_id = ? AND id <> ?`, absorbed, domain.DefaultLogin)
	if err != nil {
		return err
	}
	for _, agent := range agents {
		var moved, kept []domain.AgentLogin
		if moved, err = listAgentLogins(ctx, tx, absorbed, agent); err != nil {
			return err
		}
		if kept, err = listAgentLogins(ctx, tx, survivor, agent); err != nil {
			return err
		}
		names := map[string]bool{}
		next := 0
		for _, l := range kept {
			names[l.Name] = true
			next = max(next, l.Position+1)
		}
		if next == 0 {
			var created int64
			if created, err = encodeTime(moved[0].CreatedAt); err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO agent_logins (`+agentLoginColumns+`) VALUES (?, ?, ?, ?, 0, 1, 1, ?)`,
				survivor, agent, domain.DefaultLogin, domain.DefaultLogin, created); err != nil {
				return err
			}
			next = 1
		}
		for _, l := range moved {
			if !l.ID.Added() {
				continue
			}
			name := l.Name
			for n := 2; names[name]; n++ {
				suffix := fmt.Sprintf("-%d", n)
				name = l.Name[:min(len(l.Name), domain.MaxLoginNameLen-len(suffix))] + suffix
			}
			names[name] = true
			if _, err = tx.ExecContext(ctx, `UPDATE agent_logins SET member_id = ?, name = ?, position = ?, shared = 0 WHERE member_id = ? AND agent = ? AND id = ?`,
				survivor, name, next, absorbed, agent, l.ID); err != nil {
				return err
			}
			next++
		}
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM agent_logins WHERE member_id = ?`, absorbed)
	return err
}

// requireRow maps an update or delete that changed nothing to ErrNotFound.
func requireRow(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	if n == 0 {
		return fmt.Errorf("store: %s: %w", op, ErrNotFound)
	}
	return nil
}

func (d *DB) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
