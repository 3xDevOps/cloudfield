package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/fallback"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
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

// billedPerUse says that account's login is billed per use, when it is an
// added Console login.
func (s *Scheduler) billedPerUse(ctx context.Context, account domain.Account, agent string) (string, bool) {
	if !account.Login.Added() || s.cfg.Logins == nil {
		return "", false
	}
	identity, err := s.cfg.Logins.Identity(account.Member, agent, account.Login)
	if err != nil || !identity.PerUse() {
		return "", false
	}
	return s.accountLabel(ctx, account, agent) + "'s account is billed per use.", true
}

// annotateRun writes text to the run's terminal transcript.
func (s *Scheduler) annotateRun(ctx context.Context, run domain.RunID, text string) {
	if err := s.cfg.PTY.Annotate(context.WithoutCancel(ctx), ptyhost.RunSession(run), "Cloudfield", "", text); err != nil {
		slog.Warn("scheduler: note in the session's terminal", "run", run, "error", err)
	}
}

// notePerUse says in a session that launched on a login billed per use, in
// its log and its room, that it is billed per use. An Enhanced session's
// notice was written before its agent started.
func (s *Scheduler) notePerUse(ctx context.Context, run *domain.Run, billed string) {
	if !run.ACP {
		s.annotateRun(ctx, run.ID, billed)
	}
	f := s.usageFallbackSeam()
	if f == nil || f.Note == nil {
		return
	}
	if err := f.Note(ctx, run, billed, string(run.ID)+":per-use"); err != nil {
		slog.Warn("scheduler: post the login billed per use to the room", "run", run.ID, "error", err)
	}
}

// vendorLine is what Cloudfield keeps of a usage-limit message from inside
// a container: its first line, at most 200 characters.
func vendorLine(message string) string {
	line, _, _ := strings.Cut(message, "\n")
	line = strings.TrimSpace(line)
	if r := []rune(line); len(r) > 200 {
		line = string(r[:200])
	}
	return line
}
