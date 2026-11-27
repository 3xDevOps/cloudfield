package server

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/sshd"
	"github.com/3xDevOps/cloudfield/internal/templates"
)

// Task templates and their cron schedules (). The cron loop fires
// templates unattended, which is why it re-checks the schedule creator's
// launch capability on every fire and launches through the same guarded
// controller the RPC handlers use.
func init() {
	registerService("templates", func(d Deps) (Service, error) {
		svc, err := templates.New(templates.Config{
			Store: d.Store,
			Bus:   d.Bus,
			Runs:  guardedRuns{ssh: d.SSH},
		})
		if err != nil {
			return nil, err
		}
		d.SSH.Services.Templates = svc
		return svc, nil
	})
}

// guardedRuns launches through the same controller the RPC handlers use,
// read at launch time rather than at build time so a template launch or a
// cron fire goes through every gate another service decorated it with -
// the workspace budget () above all - no matter which service was
// built first.
type guardedRuns struct{ ssh *sshd.Config }

func (g guardedRuns) LaunchWithOptions(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, task, harness string, mode domain.LaunchMode, opts domain.LaunchOptions) (*domain.Run, error) {
	runs, ok := g.ssh.Runs.(sshd.RunLauncherWithOptions)
	if !ok {
		return nil, errors.New("templates: the session controller cannot launch with options")
	}
	logins, err := g.ssh.Store.ListAgentLogins(ctx, account, harness)
	if err != nil || len(logins) == 0 {
		if err != nil {
			return nil, err
		}
		return runs.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, opts)
	}
	// A template names no login: like any launch that names none, it starts
	// from the account's first eligible login and falls back among the
	// account's other logins, measured before the mutex as run.launch does.
	// The run keeps the template's own fallback for later moves.
	policy := domain.AccountFallback{Order: []domain.MemberID{account}}
	measure := func(login domain.LoginID) (fallback.Measured, error) {
		return sshd.MeasureUnnamed(ctx, g.ssh, member, domain.Account{Member: account, Login: login}, harness, policy)
	}
	login, err := g.ssh.Runs.StartLogin(ctx, workspace, member, account, harness, mode, opts.Permission)
	if err != nil {
		return nil, err
	}
	measured, err := measure(login)
	if err != nil {
		return nil, err
	}
	// Held, as run.launch holds it, from choosing the login until the run
	// is stored, so the login cannot be removed in between; a login chosen
	// otherwise there is measured once more.
	g.ssh.AuthorizationMu.Lock()
	unlock := sync.OnceFunc(g.ssh.AuthorizationMu.Unlock)
	defer unlock()
	again, err := g.ssh.Runs.StartLogin(ctx, workspace, member, account, harness, mode, opts.Permission)
	if err != nil {
		return nil, err
	}
	if again != login {
		login = again
		if measured, err = measure(login); err != nil {
			return nil, err
		}
	}
	byEnv := sync.OnceValues(func() (bool, error) { return g.ssh.Runs.EnvironmentSignsIn(ctx, workspace, harness, mode) })
	chosen, excluded := fallback.Select(ctx, g.ssh.Store, measured, member, domain.Account{Member: account, Login: login}, harness, []domain.WorkspaceID{workspace}, policy,
		sshd.Launchable(g.ssh.Runs, byEnv, member, account, harness, mode, opts.Permission))
	if excluded != nil {
		slog.Warn("templates: logins left out of a launch", "member", member, "error", excluded)
	}
	opts.AccountLogin = chosen.Login
	admitted := opts.Admitted
	opts.Admitted = func() {
		unlock()
		if admitted != nil {
			admitted()
		}
	}
	return runs.LaunchWithOptions(ctx, workspace, member, account, task, harness, mode, opts)
}
