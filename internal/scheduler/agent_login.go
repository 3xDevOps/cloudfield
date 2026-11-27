package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/quota"
)

// ErrSignedOut marks a launch refused because the account's login for the
// agent cannot sign it in.
var ErrSignedOut = errors.New("login is signed out")

type loginKey struct {
	account domain.Account
	agent   string
}

// loginRefusal is an agent's refusal for want of a sign-in. stamp is the
// account's login files then: the refusal stands until they change or a turn
// on the account ends well. at tells it from a later refusal, which whoever
// clears this one after reading the login must leave.
type loginRefusal struct {
	problem string
	stamp   string
	at      time.Time
}

// LoginProblem is why the login of agent, which profile describes, in
// account's home cannot sign the agent in for a launch by member: what its
// file holds, or the agent's own refusal. It is empty when the server knows
// of none, which a missing login is not. blocks is whether a launch is
// refused over it: only the file proves a login unusable, since a refusing
// agent may hold a stale copy of a sound one.
func (s *Scheduler) LoginProblem(member, account domain.MemberID, agent string, profile harness.Profile) (problem string, blocks bool, err error) {
	problem, err = s.loginFileProblem(member, account, agent, profile)
	if err != nil || problem != "" {
		return problem, problem != "", err
	}
	key := loginKey{domain.AccountOf(account), agent}
	s.mu.Lock()
	refusal, refused := s.loginRefusals[key]
	s.mu.Unlock()
	if !refused {
		return "", false, nil
	}
	stamp, err := s.loginStamp(account, agent, profile)
	if err != nil {
		return "", false, err
	}
	if refusal.stamp == stamp {
		return refusal.problem, false, nil
	}
	s.mu.Lock()
	if s.loginRefusals[key] == refusal {
		delete(s.loginRefusals, key)
	}
	s.mu.Unlock()
	return "", false, nil
}

// loginFileProblem is LoginProblem from the login file alone, for a session
// whose container mounts member's home. On member's own account a login the
// agent keeps elsewhere in the home signs it in whatever the file holds; an
// account share carries the file only. Such a login the server cannot read
// leaves the file's problem standing.
func (s *Scheduler) loginFileProblem(member, account domain.MemberID, agent string, profile harness.Profile) (string, error) {
	problem, err := s.signInFileProblem(account, agent, profile)
	if err != nil || problem == "" || member != account {
		return problem, err
	}
	own, ownErr := s.cfg.Homes.OwnerLogin(account, agent)
	switch {
	case ownErr != nil:
		return problem + "; a sign-in outside that file cannot be read: " + ownErr.Error(), nil
	case own != nil:
		return "", nil
	}
	return problem, nil
}

// signInFileProblem is why the login file of agent in account's home cannot
// sign it in, naming the file; empty when it can or there is none.
func (s *Scheduler) signInFileProblem(account domain.MemberID, agent string, profile harness.Profile) (string, error) {
	file := quota.LoginFile(agent)
	paths, err := profile.LoginPaths()
	if s.cfg.Homes == nil || err != nil || !slices.Contains(paths, file) {
		return "", nil
	}
	// ReadCredential creates a home that does not exist; LoginFound does not.
	if found, foundErr := s.cfg.Homes.LoginFound(account, []string{file}); foundErr != nil || !found {
		return "", foundErr
	}
	data, err := s.cfg.Homes.ReadCredential(account, file, maxSignInBytes)
	if err != nil {
		return "", err
	}
	if problem := quota.LoginProblem(agent, data, time.Now()); problem != "" {
		return "~/" + file + ": " + problem, nil
	}
	return "", nil
}

// loginStamp changes when a login of agent in account's home does: a login
// file, or a login the agent keeps elsewhere in the home. One of those the
// server cannot read is stamped like none, so the login file is still
// watched and a file that only became unreadable is no new sign-in.
func (s *Scheduler) loginStamp(account domain.MemberID, agent string, profile harness.Profile) (string, error) {
	if s.cfg.Homes == nil {
		return "", nil
	}
	paths, _ := profile.LoginPaths()
	stamp, err := s.cfg.Homes.LoginStamp(account, paths)
	if err != nil {
		return "", err
	}
	own, err := s.cfg.Homes.OwnerLogin(account, agent)
	if err != nil || own == nil {
		return stamp, nil
	}
	return stamp + own.File + " " + own.Stamp + "\n", nil
}

// loginRefused records that agent refused on account for want of a sign-in,
// while the account's login files read stamp. Only the agent's own message
// is kept, and bounded: the error around it can carry the agent's stderr,
// and everyone the account is shared with reads this.
func (s *Scheduler) loginRefused(account domain.Account, agent, stamp string, cause error) {
	message := "Authentication required"
	if refused := (*acp.RequestError)(nil); errors.As(cause, &refused) && refused.Message != "" {
		message = refused.Message
	}
	if runes := []rune(message); len(runes) > maxExitDetail {
		message = string(runes[:maxExitDetail-3]) + "..."
	}
	problem := "the agent refused: " + message
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loginRefusals == nil {
		s.loginRefusals = make(map[loginKey]loginRefusal)
	}
	s.loginRefusals[loginKey{account, agent}] = loginRefusal{problem: problem, stamp: stamp, at: time.Now()}
}

// signInSteps is where the owner of an account signs agent in again. Only a
// shipped agent has a login command.
func signInSteps(agent string, profile harness.Profile) string {
	if profile.LoginCommand == "" {
		return "aether terminal, then " + agent + "'s own sign-in"
	}
	return "dashboard Agents page, or aether agent login " + agent
}

// environmentSignsIn reports whether a variable the workspace sets signs
// agent in without the account's login: Claude Code's in every mode, Codex's
// CODEX_API_KEY in Background runs only (docs/harnesses.md).
func (s *Scheduler) environmentSignsIn(ctx context.Context, workspace domain.WorkspaceID, agent string, profile harness.Profile, mode domain.LaunchMode) (bool, error) {
	var keys []string
	switch {
	case agent == "claude":
		keys = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX"}
	case agent == "codex" && mode == domain.LaunchHeadless:
		keys = []string{"CODEX_API_KEY"}
	default:
		return false, nil
	}
	env, err := s.workspaceVariables(ctx, workspace, profile)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(keys, func(key string) bool { return env[key] != "" }), nil
}

// workspaceVariables are the variables a session of profile in workspace
// gets from the workspace and the server.
func (s *Scheduler) workspaceVariables(ctx context.Context, workspace domain.WorkspaceID, profile harness.Profile) (map[string]string, error) {
	ws, err := s.cfg.Store.GetWorkspace(ctx, workspace)
	if err != nil {
		return nil, fmt.Errorf("read workspace environment: %w", err)
	}
	ws, secrets, err := s.workspaceEnvironment(ctx, ws)
	if err != nil {
		return nil, err
	}
	return explicitEnvironment(profile, ws, secrets)
}

// signInKeys are the variables that sign agent in, in mode, whatever its
// login, as the installed agent names them.
func signInKeys(agent string, mode domain.LaunchMode) []string {
	switch {
	case agent == "claude":
		return []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_FEDERATION_RULE_ID", "ANTHROPIC_ORGANIZATION_ID",
			"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS",
			"CLAUDE_CODE_USE_ANTHROPIC_GOOGLE_CLOUD", "CLAUDE_CODE_USE_MANTLE", "CLAUDE_CODE_USE_GATEWAY"}
	case agent == "codex" && mode == domain.LaunchHeadless:
		return []string{"CODEX_API_KEY"}
	}
	return nil
}

// signsIn reports whether env signs agent in, in mode, whatever its login.
func signsIn(env map[string]string, agent string, mode domain.LaunchMode) bool {
	return signingKey(env, agent, mode) != ""
}

// signingKey is the first variable of env that signs agent in, in mode,
// whatever its login. Claude Code reads a CLAUDE_CODE_USE_ switch as on only
// when it is 1, true, yes or on, in any case, and signs in by workload
// identity federation only when both its rule and its organization are set.
func signingKey(env map[string]string, agent string, mode domain.LaunchMode) string {
	keys := signInKeys(agent, mode)
	if i := slices.IndexFunc(keys, func(key string) bool {
		switch {
		case strings.HasPrefix(key, "CLAUDE_CODE_USE_"):
			return slices.Contains([]string{"1", "true", "yes", "on"}, strings.ToLower(strings.TrimSpace(env[key])))
		case key == "ANTHROPIC_FEDERATION_RULE_ID" || key == "ANTHROPIC_ORGANIZATION_ID":
			return env["ANTHROPIC_FEDERATION_RULE_ID"] != "" && env["ANTHROPIC_ORGANIZATION_ID"] != ""
		}
		return env[key] != ""
	}); i >= 0 {
		return keys[i]
	}
	return ""
}

// ErrEnvironmentPays refuses an added login where another sign-in of the
// agent would pay instead of it.
var ErrEnvironmentPays = errors.New("a sign-in outside the login pays here")

// EnvironmentPays is why a session cannot run on login while payer, a
// loginPayer answer, signs its agent in.
func EnvironmentPays(login domain.LoginID, payer string) error {
	return fmt.Errorf("scheduler: %w: %s, so login %s would not pay; launch on the default login", ErrEnvironmentPays, payer, login)
}

// loginPayer is what signs agent in, in mode, in a session in home's home in
// workspace, whatever added login it mounts: a workspace or server variable,
// or home's ~/.claude/settings.json, which Claude Code prefers to a login
// file. Empty when nothing does.
func (s *Scheduler) loginPayer(ctx context.Context, workspace domain.WorkspaceID, home domain.MemberID, agent string, profile harness.Profile, mode domain.LaunchMode) (string, error) {
	var env map[string]string
	if signInKeys(agent, mode) != nil {
		var err error
		if env, err = s.workspaceVariables(ctx, workspace, profile); err != nil {
			return "", err
		}
	}
	return s.payer(env, home, agent, mode), nil
}

// payer is what signs agent in, in mode, whatever login a session in home's
// home with the variables env mounts: one of env, or home's
// ~/.claude/settings.json, whose env block Claude Code adds to them. A
// settings file the server cannot read may sign it in.
func (s *Scheduler) payer(env map[string]string, home domain.MemberID, agent string, mode domain.LaunchMode) string {
	if signsIn(env, agent, mode) {
		return "a workspace or server variable signs the agent in"
	}
	key, err := s.settingsSignIn(home, agent, env)
	switch {
	case err != nil:
		return "~/.claude/settings.json may sign the agent in and cannot be read: " + err.Error()
	case key == "apiKeyHelper":
		return "apiKeyHelper in ~/.claude/settings.json signs the agent in"
	case key == "ANTHROPIC_FEDERATION_RULE_ID":
		return "ANTHROPIC_FEDERATION_RULE_ID and ANTHROPIC_ORGANIZATION_ID, set under env in ~/.claude/settings.json or by the workspace or server, sign the agent in"
	case key != "":
		return key + " under env in ~/.claude/settings.json signs the agent in"
	}
	return ""
}

// settingsSignIn is what home's ~/.claude/settings.json sets that signs
// agent in, ahead of any login file, with the variables env beside it:
// "apiKeyHelper", the first variable of its env block over env that does,
// or empty for none.
func (s *Scheduler) settingsSignIn(home domain.MemberID, agent string, env map[string]string) (string, error) {
	if agent != "claude" || s.cfg.Homes == nil {
		return "", nil
	}
	settings, err := s.cfg.Homes.ClaudeSettings(home)
	switch {
	case err != nil:
		return "", err
	case settings.APIKeyHelper:
		return "apiKeyHelper", nil
	}
	merged := make(map[string]string, len(env)+len(settings.Env))
	maps.Copy(merged, env)
	maps.Copy(merged, settings.Env)
	return signingKey(merged, agent, ""), nil
}

// checkLogin refuses a launch of agent on account whose login cannot sign it
// in, unless the workspace's environment does.
func (s *Scheduler) checkLogin(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, agent string, profile harness.Profile, mode domain.LaunchMode) error {
	// A login the server cannot read is provisioning's refusal to make.
	problem, blocks, err := s.LoginProblem(member, account, agent, profile)
	if err != nil || !blocks {
		return nil
	}
	if signedIn, envErr := s.environmentSignsIn(ctx, workspace, agent, profile, mode); envErr != nil || signedIn {
		return envErr
	}
	return fmt.Errorf("scheduler: %s's %s %w: %s. Sign in, then launch again: %s",
		s.memberName(ctx, account), cmp.Or(profile.Label(), agent), ErrSignedOut, problem, signInSteps(agent, profile))
}

// runLogin is the login a run's agent signs in with. known is false when it
// cannot be resolved or a workspace variable signs the agent in instead.
type runLogin struct {
	known   bool
	account domain.Account
	owner   string
	// agent is the name the run launched and label what to call it; a
	// definition without an executable resolves to a profile that has
	// neither.
	agent   string
	label   string
	profile harness.Profile
	// name is the added login's, empty for the default login.
	name string
	// fileProblem is loginFileProblem's answer and stamp the login files'.
	fileProblem string
	stamp       string
}

func (s *Scheduler) runLogin(ctx context.Context, runID domain.RunID) runLogin {
	run, err := s.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return runLogin{}
	}
	login := runLogin{account: run.Account(), agent: run.Harness}
	if login.profile, _, err = s.launchProfile(ctx, run.MemberID, login.account.Member, run.Harness); err != nil {
		return runLogin{}
	}
	if byEnv, envErr := s.environmentSignsIn(ctx, run.WorkspaceID, login.agent, login.profile, run.Mode); envErr != nil || byEnv {
		return runLogin{}
	}
	if login.account.Login.Added() {
		login.name = s.addedLoginName(ctx, login.account, login.agent)
		if login.fileProblem, err = s.addedLoginProblem(login.account, login.agent); err != nil {
			return runLogin{}
		}
		if login.stamp, err = s.cfg.Logins.Stamp(login.account.Member, login.agent, login.account.Login); err != nil {
			return runLogin{}
		}
	} else {
		if login.fileProblem, err = s.loginFileProblem(run.HomeMember(), login.account.Member, login.agent, login.profile); err != nil {
			return runLogin{}
		}
		if login.stamp, err = s.loginStamp(login.account.Member, login.agent, login.profile); err != nil {
			return runLogin{}
		}
	}
	login.known, login.owner, login.label = true, s.memberName(ctx, login.account.Member), cmp.Or(login.profile.Label(), run.Harness)
	return login
}

// What Aether does about an agent's refusal for want of a sign-in.
type refusedHow int

const (
	// refusedRestarting: Aether is restarting the agent, which cures a
	// stale copy of a sound login and not a signed-out one.
	refusedRestarting refusedHow = iota
	// refusedAfterRestart: the restarted agent refused too.
	refusedAfterRestart
	// refusedUnrestarted: Aether restarts no agent over it: a refused open,
	// a Background run, or a run waiting for its turn to end.
	refusedUnrestarted
)

// loginRefusedAdvice records that an agent refused for want of login's
// sign-in and returns what follows the refusal in the run's reason, whose
// length cap keeps its end: whose login it is and where they sign in again.
func (s *Scheduler) loginRefusedAdvice(login runLogin, cause error, how refusedHow) string {
	if !login.known {
		switch how {
		case refusedRestarting:
			return "Cloudfield restarted the agent so it reads the account's login again: send the message again"
		case refusedAfterRestart:
			return "restarting the agent did not restore its sign-in, so this sandbox's copy of the login is stale or unreadable: start a new session"
		}
		return "sign the agent in from this session's Terminal tab"
	}
	s.loginRefused(login.account, login.agent, login.stamp, cause)
	steps, which := signInSteps(login.agent, login.profile), login.label+" login"
	if login.name != "" {
		steps, which = readdSteps(login.agent, login.name), which+" "+login.name
	}
	switch {
	case login.fileProblem != "" && how == refusedRestarting:
		return fmt.Sprintf("%s's %s is signed out. Sign in, then send the message again: %s", login.owner, which, steps)
	case login.fileProblem != "":
		return fmt.Sprintf("%s's %s is signed out. Sign in: %s", login.owner, which, steps)
	case how == refusedRestarting:
		return fmt.Sprintf("Cloudfield restarted the agent to reread %s's login: send the message again. Refused again? Sign in: %s", login.owner, steps)
	case how == refusedAfterRestart:
		return fmt.Sprintf("a restart did not help. Start a new session, or if %s's %s is signed out, sign in: %s", login.owner, which, steps)
	}
	return fmt.Sprintf("%s's %s may be signed out. Sign in: %s", login.owner, which, steps)
}

// readdSteps is how the owner of agent's added login name signs it in
// again: a login is signed in once, when it is added.
func readdSteps(agent, name string) string {
	return fmt.Sprintf("its owner removes it and adds it again, with cloudfield agent logins remove %s, then cloudfield agent login %s --as %s", name, agent, name)
}

// turnWorked forgets a refusal on the run's account once a turn there ends
// well, unless a workspace variable signed that turn's agent in.
func (s *Scheduler) turnWorked(ctx context.Context, runID domain.RunID) {
	s.mu.Lock()
	none := len(s.loginRefusals) == 0
	s.mu.Unlock()
	if none {
		return
	}
	run, err := s.cfg.Store.GetRun(ctx, runID)
	if err != nil {
		return
	}
	key := loginKey{run.Account(), run.Harness}
	s.mu.Lock()
	refusal, refused := s.loginRefusals[key]
	s.mu.Unlock()
	if !refused || !s.runLogin(ctx, runID).known {
		return
	}
	s.mu.Lock()
	if s.loginRefusals[key] == refusal {
		delete(s.loginRefusals, key)
	}
	s.mu.Unlock()
}
