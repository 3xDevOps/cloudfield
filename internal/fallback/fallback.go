// Package fallback decides which agent account a launch runs on: the account
// the launcher asked for, or, when that account's vendor usage is exhausted,
// another account the launcher may launch on.
package fallback

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// ExhaustedPercent is the used share of a quota window at which an account
// counts as exhausted: at 95% one long agent turn can spend the rest within
// minutes.
const ExhaustedPercent = 95

// readTimeout bounds how long a launch waits on one account's quota read. A
// slower read counts as unreadable; it carries on and fills the quota cache
// for the next launch.
const readTimeout = 3 * time.Second

// QuotaReader reads the vendor quota of a member's default login without
// exposing its credentials or the provider's responses to the caller.
type QuotaReader interface {
	Read(ctx context.Context, account domain.MemberID, refresh bool) ([]quota.Provider, error)
}

// LoginReader is a QuotaReader that also reads the quota of a member's
// added login. Without it, an added login's usage is unreadable.
type LoginReader interface {
	ReadLogin(ctx context.Context, account domain.Account, refresh bool) ([]quota.Provider, error)
}

// windows are the quota windows a run of each agent draws on, keyed by agent
// and quota provider, which share a name. Claude's per-model weekly windows
// are left out because Aether does not know which model a run uses.
var windows = map[string][]string{
	"claude": {"five_hour", "seven_day"},
	"codex":  {"primary", "secondary"},
}

// Reads reports whether Aether reads harness's usage, and so can move its
// runs to another account.
func Reads(harness string) bool {
	return windows[harness] != nil
}

func Policy(order []string, disabled bool) (domain.AccountFallback, error) {
	if disabled && len(order) > 0 {
		return domain.AccountFallback{}, errors.New("fallback_account_member_ids and no_fallback cannot both be set")
	}
	policy := domain.AccountFallback{Disabled: disabled}
	for _, id := range order {
		if id == "" {
			return domain.AccountFallback{}, errors.New("fallback_account_member_ids cannot contain an empty member ID")
		}
		policy.Order = append(policy.Order, domain.MemberID(id))
	}
	return policy, nil
}

// ResolveLaunchAccount returns the account a launch by actor runs on: actor's
// own, or requested when its owner has shared their agent login with actor.
// Shares are directional. Every launch, fallback candidates included, and
// every later use of a run's account checks it, so administrators do not
// acquire implicit credential-use authority.
func ResolveLaunchAccount(ctx context.Context, st store.Store, actor domain.MemberID, requested string) (domain.MemberID, error) {
	account := domain.MemberID(requested)
	if account == "" || account == actor {
		return actor, nil
	}
	shared, err := st.AccountSharedWith(ctx, account, actor)
	if err != nil {
		return "", err
	}
	if !shared {
		return "", fmt.Errorf("%w: member %s has not shared their account with you", permissions.ErrDenied, account)
	}
	owner, err := st.GetMember(ctx, account)
	if err != nil {
		return "", err
	}
	if owner.Pending {
		return "", fmt.Errorf("%w: account owner is pending admin approval", permissions.ErrDenied)
	}
	return account, nil
}

// ResolveLaunchAccountIn is ResolveLaunchAccount for a launch into
// workspaces. A run works with its account owner's login, so another
// member's account is usable only when that owner reaches every one of the
// workspaces too.
func ResolveLaunchAccountIn(ctx context.Context, st store.Store, actor domain.MemberID, requested string, workspaces []domain.WorkspaceID) (domain.MemberID, error) {
	account, err := ResolveLaunchAccount(ctx, st, actor, requested)
	if err != nil || account == actor {
		return account, err
	}
	owner, err := st.GetMember(ctx, account)
	if err != nil {
		return "", err
	}
	reach := permissions.ActorOf(owner)
	for _, ws := range workspaces {
		if !reach.Reaches(ws) {
			return "", fmt.Errorf("%w: the owner of account %s has no access to workspace %s", permissions.ErrDenied, account, ws)
		}
	}
	return account, nil
}

// Measured is the highest used share of each account's quota windows, for
// the accounts whose usage could be read, and Unknown for a candidate whose
// usage could not be read but may still be chosen.
type Measured map[domain.Account]float64

// Unknown is the Measured value of a candidate whose usage could not be read.
const Unknown = -1

// Measure reads the usage Select decides on: the requested login's, and,
// only when it is exhausted, every candidate's. It reads vendor quota, so
// callers run it before taking the shared authorization mutex, and under it
// only once more, bounded by readTimeout, for a login chosen again there;
// Select authorises each account again under that mutex.
func Measure(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, requested domain.Account, harness string, policy domain.AccountFallback) (Measured, error) {
	if policy.Disabled || usage == nil {
		return nil, nil
	}
	account, err := ResolveLaunchAccount(ctx, st, launcher, string(requested.Member))
	if err != nil {
		return nil, nil
	}
	requested.Member = account
	used, ok := usedPercent(ctx, usage, requested, harness)
	if !ok && ranOutRecently(requested, harness, time.Now()) {
		used, ok = 100, true
	}
	if !ok {
		return nil, nil
	}
	measured := Measured{requested: used}
	if used < ExhaustedPercent {
		return measured, nil
	}
	if err := measureCandidates(ctx, st, usage, launcher, requested, harness, policy, measured, false); err != nil {
		return nil, err
	}
	return measured, nil
}

// MeasureStart is Measure for a launch that named no login and starts on
// requested. A start billed per use (perUse) has no usage to read: it
// counts as Unknown, so Select takes the first candidate with usage known to
// be left, in its order, before it, and stays on it when none is.
func MeasureStart(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, requested domain.Account, harness string, policy domain.AccountFallback, perUse bool) (Measured, error) {
	if !perUse {
		return Measure(ctx, st, usage, launcher, requested, harness, policy)
	}
	if policy.Disabled || usage == nil {
		return nil, nil
	}
	account, err := ResolveLaunchAccount(ctx, st, launcher, string(requested.Member))
	if err != nil {
		return nil, nil
	}
	requested.Member = account
	measured := Measured{requested: Unknown}
	if err := measureCandidates(ctx, st, usage, launcher, requested, harness, policy, measured, false); err != nil {
		return nil, err
	}
	return measured, nil
}

// MeasureNext reads the usage Select decides on for a running run whose
// vendor reported its current login's usage exhausted: current and every
// login in used count as exhausted, so Select returns current when no other
// candidate qualifies. A candidate whose usage cannot be read is recorded
// as Unknown: the run has stopped either way, so Select tries it when no
// candidate has usage known to be left.
func MeasureNext(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, current domain.Account, used []domain.Account, harness string, policy domain.AccountFallback) (Measured, error) {
	if policy.Disabled || usage == nil {
		return nil, nil
	}
	measured := Measured{current: 100}
	for _, account := range used {
		measured[account] = 100
	}
	// An agent with no quota windows is never measured, so its candidates
	// stay out rather than all reading as Unknown.
	if err := measureCandidates(ctx, st, usage, launcher, current, harness, policy, measured, Reads(harness)); err != nil {
		return nil, err
	}
	return measured, nil
}

// MeasureAway reads the usage Select decides on for a running run that must
// leave current, whose owner revoked the share it ran on: current counts as
// exhausted, and a candidate whose usage cannot be read is Unknown for every
// agent, so the run moves wherever its agent can run rather than stop.
func MeasureAway(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, current domain.Account, harness string, policy domain.AccountFallback) (Measured, error) {
	measured := Measured{current: 100}
	if err := measureCandidates(ctx, st, usage, launcher, current, harness, policy, measured, true); err != nil {
		return nil, err
	}
	return measured, nil
}

// measureCandidates reads every candidate's usage. One whose usage cannot
// be read is recorded as Unknown when unknown is set, and at launch unless
// the candidate is legacy: a launch never fell back on another account's
// unreadable default login, and one where nobody added a login still does
// not.
func measureCandidates(ctx context.Context, st store.Store, usage QuotaReader, launcher domain.MemberID, requested domain.Account, harness string, policy domain.AccountFallback, measured Measured, unknown bool) error {
	candidates, err := candidates(ctx, st, launcher, requested, harness, policy)
	if err != nil {
		return err
	}
	var (
		mu   sync.Mutex
		wait sync.WaitGroup
	)
	known := maps.Clone(measured)
	for _, c := range candidates {
		if _, ok := known[c.account]; ok {
			continue
		}
		if _, err := ResolveLaunchAccount(ctx, st, launcher, string(c.account.Member)); err != nil {
			continue
		}
		wait.Go(func() {
			used, ok := usedPercent(ctx, usage, c.account, harness)
			if !ok && !unknown && c.legacy {
				return
			}
			if !ok && ranOutRecently(c.account, harness, time.Now()) {
				used = 100
			} else if !ok {
				used = Unknown
			}
			mu.Lock()
			measured[c.account] = used
			mu.Unlock()
		})
	}
	wait.Wait()
	return nil
}

// Launchable reports whether the launch can run on account as a fallback,
// without borrowing the owner's agent installation. It runs under the
// authorization mutex, so it reads local files only.
type Launchable func(ctx context.Context, account domain.Account) (bool, error)

// Select returns the login launcher's run launches on, from the usage
// Measure read, for a run in workspaces. The caller has admitted launcher's
// launch on requested and holds the shared authorization mutex until the
// scheduler's durable launch boundary, so each candidate needs only its
// account authorization checked here; a login Measure could not read, or
// launchable refuses, is never chosen. A requested login Measure recorded
// as Unknown is the first Unknown candidate. Candidates come in three groups: the
// requested account's other logins in its owner's order; the launcher's own
// logins when the account is a teammate's and the launcher added logins;
// and the other accounts, in the policy's order or by most usage left, each
// served by its first login with usage known to be left. The first with
// usage known to be left is taken; an Unknown one only when none is, in the
// same sequence. A nil launchable refuses nothing. When no candidate
// qualifies, the launch stays on the requested login. A candidate whose
// check fails is left out like a refused one, so it never fails a launch
// that can stay on the requested login; excluded joins those failures.
func Select(ctx context.Context, st store.Store, measured Measured, launcher domain.MemberID, requested domain.Account, harness string, workspaces []domain.WorkspaceID, policy domain.AccountFallback, launchable Launchable) (account domain.Account, excluded error) {
	if policy.Disabled {
		return requested, nil
	}
	used, ok := measured[requested]
	if !ok || (used != Unknown && used < ExhaustedPercent) {
		return requested, nil
	}
	candidates, err := candidates(ctx, st, launcher, requested, harness, policy)
	if err != nil {
		return requested, fmt.Errorf("list fallback accounts: %w", err)
	}
	var (
		failures []error
		unknown  *domain.Account
		best     *domain.Account
		bestUsed float64
		served   = map[domain.MemberID]bool{}
	)
	if used == Unknown {
		unknown = &requested
	}
	for _, c := range candidates {
		if c.account == requested || (c.group == otherAccounts && served[c.account.Member]) {
			continue
		}
		if _, authErr := ResolveLaunchAccountIn(ctx, st, launcher, string(c.account.Member), workspaces); authErr != nil {
			if !errors.Is(authErr, permissions.ErrDenied) && !errors.Is(authErr, store.ErrNotFound) {
				failures = append(failures, &Skip{Account: c.account, Reason: ReasonAccess, Err: authErr})
			}
			continue
		}
		used, ok := measured[c.account]
		if !ok || used >= ExhaustedPercent || (used == Unknown && unknown != nil) {
			continue
		}
		if launchable != nil {
			allowed, launchErr := launchable(ctx, c.account)
			if launchErr != nil {
				failures = append(failures, &Skip{Account: c.account, Reason: ReasonAgent, Err: launchErr})
				continue
			}
			if !allowed {
				continue
			}
		}
		if used == Unknown {
			unknown = &c.account
			continue
		}
		if c.group != otherAccounts || len(policy.Order) > 0 {
			return c.account, errors.Join(failures...)
		}
		served[c.account.Member] = true
		if best == nil || used < bestUsed || (used == bestUsed && c.account.Member < best.Member) {
			best, bestUsed = &c.account, used
		}
	}
	switch {
	case best != nil:
		return *best, errors.Join(failures...)
	case unknown != nil:
		return *unknown, errors.Join(failures...)
	}
	return requested, errors.Join(failures...)
}

// The reasons Skipped gives launchers for a left-out account. The errors
// behind them can name member homes and other members, so they stay in the
// server log.
const (
	ReasonAccess = "your access to the account could not be checked"
	ReasonAgent  = "the account's agent could not be checked"
	ReasonList   = "the fallback accounts could not be listed"
)

// Skip is a fallback candidate Select left out because checking it failed.
type Skip struct {
	Account domain.Account
	Reason  string
	Err     error
}

func (s *Skip) Error() string { return fmt.Sprintf("account %s: %v", s.Account, s.Err) }

func (s *Skip) Unwrap() error { return s.Err }

// Skipped lists what excluded, Select's second result, left out, each with
// its reason; a failure to list the candidates names no account.
func Skipped(excluded error) []protocol.SkippedAccount {
	if excluded == nil {
		return nil
	}
	errs := []error{excluded}
	if joined, ok := excluded.(interface{ Unwrap() []error }); ok {
		errs = joined.Unwrap()
	}
	out := make([]protocol.SkippedAccount, 0, len(errs))
	for _, err := range errs {
		var skip *Skip
		if errors.As(err, &skip) {
			out = append(out, protocol.SkippedAccount{AccountMemberID: string(skip.Account.Member), Error: skip.Reason})
			continue
		}
		out = append(out, protocol.SkippedAccount{Error: ReasonList})
	}
	return out
}

// The groups of candidates, tried in this order.
const (
	sameAccount = iota
	launcherOwn
	otherAccounts
)

type candidate struct {
	account domain.Account
	group   int
	// legacy: neither the launcher, the requested account nor the
	// candidate's account keeps an added login.
	legacy bool
}

// candidates lists the logins a run of launcher on requested may move to.
// The other accounts are policy's order, or launcher and every account
// shared with launcher; launcher's own logins form a group of their own
// only when requested is a teammate's and launcher added logins, so a
// launcher who added none is ranked with the teammates as before logins
// existed.
func candidates(ctx context.Context, st store.Store, launcher domain.MemberID, requested domain.Account, harness string, policy domain.AccountFallback) ([]candidate, error) {
	own, err := st.ListAgentLogins(ctx, launcher, harness)
	if err != nil {
		return nil, err
	}
	var out []candidate
	involved := hasAddedLogin(own)
	add := func(member domain.MemberID, group int) (bool, error) {
		logins, added, listErr := accountLogins(ctx, st, launcher, member, harness)
		for _, l := range logins {
			out = append(out, candidate{l, group, !involved && !added})
		}
		return added, listErr
	}
	requestedAdded, err := add(requested.Member, sameAccount)
	if err != nil {
		return nil, err
	}
	involved = involved || requestedAdded
	members := policy.Order
	if len(members) == 0 {
		owners, err := st.ListAccountOwners(ctx, launcher)
		if err != nil {
			return nil, err
		}
		members = []domain.MemberID{launcher}
		for _, owner := range owners {
			members = append(members, owner.ID)
		}
	}
	ownGroup := requested.Member != launcher && hasAddedLogin(own) && slices.Contains(members, launcher)
	if ownGroup {
		if _, err := add(launcher, launcherOwn); err != nil {
			return nil, err
		}
	}
	for _, member := range members {
		if member == requested.Member || (ownGroup && member == launcher) {
			continue
		}
		if _, err := add(member, otherAccounts); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// accountLogins is member's logins of harness that may serve launcher
// without being named: in their owner's order, automatic, and on for shares
// unless launcher owns them. A member who added none has their default
// login alone. added reports whether member keeps an added login.
func accountLogins(ctx context.Context, st store.Store, launcher, member domain.MemberID, harness string) (logins []domain.Account, added bool, err error) {
	rows, err := st.ListAgentLogins(ctx, member, harness)
	if err != nil || len(rows) == 0 {
		return []domain.Account{domain.AccountOf(member)}, false, err
	}
	var out []domain.Account
	for _, row := range rows {
		if !row.Automatic || (member != launcher && !row.Shared) {
			continue
		}
		account := domain.AccountOf(member)
		if row.ID.Added() {
			account.Login = row.ID
		}
		out = append(out, account)
	}
	return out, hasAddedLogin(rows), nil
}

func hasAddedLogin(rows []domain.AgentLogin) bool {
	return slices.ContainsFunc(rows, func(l domain.AgentLogin) bool { return l.ID.Added() })
}

// usedPercent reports the highest used share of account's quota windows in
// force for harness. ok is false when that usage cannot be read: an agent
// without readable quota, a missing or API-key login, a failed or stale read,
// or a row reporting none of those windows.
func usedPercent(ctx context.Context, usage QuotaReader, account domain.Account, harness string) (used float64, ok bool) {
	ids := windows[harness]
	if ids == nil {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, readTimeout)
	defer cancel()
	var (
		providers []quota.Provider
		err       error
	)
	if account.Login.Added() {
		logins, ok := usage.(LoginReader)
		if !ok {
			return 0, false
		}
		providers, err = logins.ReadLogin(ctx, account, false)
	} else {
		providers, err = usage.Read(ctx, account.Member, false)
	}
	if err != nil {
		return 0, false
	}
	now := time.Now()
	for _, provider := range providers {
		if provider.Provider != harness {
			continue
		}
		if provider.Status != "ok" {
			return 0, false
		}
		reported := false
		for _, window := range provider.Windows {
			if !slices.Contains(ids, window.ID) {
				continue
			}
			reported = true
			if window.ResetsAt == nil || window.ResetsAt.After(now) {
				used = max(used, window.UsedPercent)
			}
		}
		return used, reported
	}
	return 0, false
}

// ResolveRunLogin is ResolveLaunchAccountIn for the login a run is on: an
// actor who does not own it also needs its owner to have left it on for
// shares. Every check that asks whether a member holds a share of a run's
// account asks this.
func ResolveRunLogin(ctx context.Context, st store.Store, actor domain.MemberID, run *domain.Run, workspaces []domain.WorkspaceID) error {
	return ResolveLoginShared(ctx, st, actor, run.Account(), run.Harness, workspaces)
}

// ResolveLoginShared is ResolveRunLogin for a session on login, which
// actor need not own if its owner left it on for shares. Its automatic
// switch governs which login is chosen next, not a session already on it
// or a move already committed to it.
func ResolveLoginShared(ctx context.Context, st store.Store, actor domain.MemberID, login domain.Account, harness string, workspaces []domain.WorkspaceID) error {
	if _, err := ResolveLaunchAccountIn(ctx, st, actor, string(login.Member), workspaces); err != nil || actor == login.Member {
		return err
	}
	return LoginShared(ctx, st, login, harness)
}

// LoginShared refuses login for a member who does not own it unless its
// owner left it on for shares, wrapping ErrOffForShares when they turned it
// off. A member who keeps no login rows has only the default login, which
// is on for shares.
func LoginShared(ctx context.Context, st store.Store, login domain.Account, harness string) error {
	logins, err := st.ListAgentLogins(ctx, login.Member, harness)
	if err != nil || len(logins) == 0 {
		return err
	}
	id := cmp.Or(login.Login, domain.DefaultLogin)
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == id })
	switch {
	case i < 0:
		return fmt.Errorf("%w: login %s of this account is not shared with you", permissions.ErrDenied, id)
	case !logins[i].Shared:
		return fmt.Errorf("%w: login %s of this account is not shared with you: %w", permissions.ErrDenied, id, ErrOffForShares)
	}
	return nil
}

// runOutFor is how long a login whose usage cannot be read is passed over
// after its owner's session reported it run out.
const runOutFor = 30 * time.Minute

type runOut struct {
	account domain.Account
	agent   string
}

var runOuts = struct {
	sync.Mutex
	at map[runOut]time.Time
}{at: map[runOut]time.Time{}}

// NoteRunOut records that account's login of agent, whose usage cannot be
// read, ran out at.
func NoteRunOut(account domain.Account, agent string, at time.Time) {
	runOuts.Lock()
	defer runOuts.Unlock()
	runOuts.at[runOut{account, agent}] = at
}

// ForgetRunOuts drops what NoteRunOut recorded of member's logins of agent,
// once one of them is removed.
func ForgetRunOuts(member domain.MemberID, agent string) {
	runOuts.Lock()
	defer runOuts.Unlock()
	maps.DeleteFunc(runOuts.at, func(k runOut, _ time.Time) bool { return k.account.Member == member && k.agent == agent })
}

func ranOutRecently(account domain.Account, agent string, now time.Time) bool {
	runOuts.Lock()
	defer runOuts.Unlock()
	key := runOut{account, agent}
	at, ok := runOuts.at[key]
	if ok && now.Sub(at) >= runOutFor {
		delete(runOuts.at, key)
		return false
	}
	return ok
}

// ResolveLoginUse is ResolveLaunchAccountIn for a container that mounts
// login in actor's session: a login actor does not own must also be on for
// shares and automatic, as provisioning requires. A member who keeps no
// login rows has only the default login, which needs the share alone.
func ResolveLoginUse(ctx context.Context, st store.Store, actor domain.MemberID, login domain.Account, harness string, workspaces []domain.WorkspaceID) error {
	if _, err := ResolveLaunchAccountIn(ctx, st, actor, string(login.Member), workspaces); err != nil || actor == login.Member {
		return err
	}
	logins, err := st.ListAgentLogins(ctx, login.Member, harness)
	if err != nil || (len(logins) == 0 && !login.Login.Added()) {
		return err
	}
	id := cmp.Or(login.Login, domain.DefaultLogin)
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == id })
	switch {
	case i < 0:
		return fmt.Errorf("%w: login %s of this account is not available to you", permissions.ErrDenied, id)
	case !logins[i].Shared:
		return fmt.Errorf("%w: login %s of this account is not available to you: %w", permissions.ErrDenied, id, ErrOffForShares)
	case !logins[i].Automatic:
		return fmt.Errorf("%w: login %s of this account is not available to you: %w", permissions.ErrDenied, id, errAutomaticOff)
	}
	return nil
}

var (
	// ErrOffForShares is a login its owner turned off for shares.
	ErrOffForShares = errors.New("its owner turned it off for shares")
	errAutomaticOff = errors.New("its owner turned it automatic off, so only they start sessions on it")
)

// ResolveRunRelaunch is ResolveRunLogin for actor's reopening run, whose
// container mounts its login again. When the account's owner keeps login
// rows, actor, the session's owner and the member whose home it mounts each
// need ResolveLoginUse. A member who keeps none has only the default login,
// which needs actor's share alone, as before logins existed.
func ResolveRunRelaunch(ctx context.Context, st store.Store, actor domain.MemberID, run *domain.Run, workspaces []domain.WorkspaceID) error {
	account := run.Account()
	logins, err := st.ListAgentLogins(ctx, account.Member, run.Harness)
	if err != nil {
		return err
	}
	if len(logins) == 0 && !account.Login.Added() {
		_, err = ResolveLaunchAccountIn(ctx, st, actor, string(account.Member), workspaces)
		return err
	}
	for _, member := range []domain.MemberID{actor, run.MemberID, run.HomeMember()} {
		err := ResolveLoginUse(ctx, st, member, account, run.Harness, workspaces)
		if errors.Is(err, ErrOffForShares) || errors.Is(err, errAutomaticOff) {
			return fmt.Errorf("%w; a kept session cannot change login, so start a new session, or ask the login's owner to turn it on again", err)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
