package scheduler

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/quota"
)

// accountLabel names a login for notes, reasons and the continuation: the
// member's name, with an added login's name after it.
func (s *Scheduler) accountLabel(ctx context.Context, account domain.Account, agent string) string {
	name := s.memberName(ctx, account.Member)
	if !account.Login.Added() {
		return name
	}
	if l, ok := s.loginRow(ctx, account, agent); ok {
		return name + " (" + l.Name + ")"
	}
	return name + " (" + string(account.Login) + ")"
}

func (s *Scheduler) loginRow(ctx context.Context, account domain.Account, agent string) (domain.AgentLogin, bool) {
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account.Member, agent)
	if err != nil {
		return domain.AgentLogin{}, false
	}
	id := account.Login
	if id == "" {
		id = domain.DefaultLogin
	}
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == id })
	if i < 0 {
		return domain.AgentLogin{}, false
	}
	return logins[i], true
}

// readLogin reads a login's usage through the fallback's reader, afresh
// when refresh is set.
func readLogin(ctx context.Context, usage fallback.QuotaReader, account domain.Account, refresh bool) ([]quota.Provider, error) {
	if account.Login.Added() {
		logins, ok := usage.(fallback.LoginReader)
		if !ok {
			return nil, fmt.Errorf("added logins' usage cannot be read")
		}
		return logins.ReadLogin(ctx, account, refresh)
	}
	return usage.Read(ctx, account.Member, refresh)
}

// spentWindows is the windows of agent's usage that read 95% or more and
// have not reset, every window included; ok is false when none was read.
func spentWindows(providers []quota.Provider, agent string, now time.Time) (spent []quota.Window, ok bool) {
	for _, p := range providers {
		if p.Provider != agent || p.Status != "ok" {
			continue
		}
		ok = true
		for _, w := range p.Windows {
			if w.UsedPercent >= fallback.ExhaustedPercent && (w.ResetsAt == nil || w.ResetsAt.After(now)) {
				spent = append(spent, w)
			}
		}
	}
	return spent, ok
}

// checkTeammateSignal reads a login afresh before a move acts on a signal
// from a session its owner does not own: the signal comes from inside the
// container and can be forged. It returns why the session stays when the
// read shows no window spent, and "" when the move may go on.
func (s *Scheduler) checkTeammateSignal(ctx context.Context, f *UsageFallback, run *domain.Run, from domain.Account) string {
	if run.MemberID == from.Member && run.HomeMember() == from.Member {
		return ""
	}
	providers, err := readLogin(ctx, f.Usage, from, true)
	if err == nil {
		if spent, _ := spentWindows(providers, run.Harness, time.Now()); len(spent) > 0 {
			return ""
		}
	}
	return "the agent reported a usage limit that " + s.accountLabel(ctx, from, run.Harness) + "'s usage does not show"
}

// noteRunOut passes a login whose usage cannot be read over for a while,
// when its owner's own session reported it run out: a Console login out of
// credit is not chosen again by every launch.
func (s *Scheduler) noteRunOut(ctx context.Context, f *UsageFallback, run *domain.Run, from domain.Account) {
	if run.MemberID != from.Member || run.HomeMember() != from.Member {
		return
	}
	providers, err := readLogin(ctx, f.Usage, from, false)
	if err == nil {
		if _, read := spentWindows(providers, run.Harness, time.Now()); read {
			return
		}
	}
	fallback.NoteRunOut(from, time.Now())
}

// triedLogins ends the reason of a session that ran out on every login it
// could use with those logins and when the first of them is usable again:
// for each, the latest reset among its spent windows, and the earliest of
// those. It is empty for an account with one login, whose reason stays as
// before logins existed.
func (s *Scheduler) triedLogins(ctx context.Context, f *UsageFallback, run *domain.Run, measured fallback.Measured, from domain.Account) string {
	var tried []domain.Account
	for account, used := range measured {
		if used >= fallback.ExhaustedPercent && account.Member == from.Member {
			tried = append(tried, account)
		}
	}
	if len(tried) < 2 {
		return ""
	}
	slices.SortFunc(tried, func(a, b domain.Account) int { return strings.Compare(a.String(), b.String()) })
	names := make([]string, len(tried))
	var first *time.Time
	now := time.Now()
	for i, account := range tried {
		names[i] = s.accountLabel(ctx, account, run.Harness)
		providers, err := readLogin(ctx, f.Usage, account, false)
		if err != nil {
			continue
		}
		spent, _ := spentWindows(providers, run.Harness, now)
		var latest *time.Time
		for _, w := range spent {
			if w.ResetsAt != nil && (latest == nil || w.ResetsAt.After(*latest)) {
				latest = w.ResetsAt
			}
		}
		if latest != nil && (first == nil || latest.Before(*first)) {
			first = latest
		}
	}
	usable := "unknown"
	if first != nil {
		usable = first.UTC().Format(time.RFC3339)
	}
	return "; logins tried: " + strings.Join(names, ", ") + "; the first is usable again at " + usable
}
