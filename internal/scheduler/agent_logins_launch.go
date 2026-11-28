package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/quota"
	"github.com/3xDevOps/cloudfield/internal/runtime"
)

// addedLoginMounts mounts run's added login from its directory over the
// default login's three stores: the subscription sign-in file, the
// Anthropic profile directory, and a copy of the member's configuration
// without the account keys, as a session on a shared account gets. A
// sign-in's copy is scratch in the login's directory, and Claude Code's
// backups of it stay there too, out of the member's home.
func (s *Scheduler) addedLoginMounts(run *domain.Run, launcher domain.MemberID, profile harness.Profile, home string, env map[string]string, purpose EnvironmentPurpose, isolated bool) ([]runtime.Mount, error) {
	if s.cfg.Logins == nil {
		return nil, fmt.Errorf("scheduler: %w", ErrNoAddedLogins)
	}
	if err := carriesAddedLogin(profile); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if s.sessionVMs() {
		return nil, errors.New("scheduler: session VMs cannot verify an added login's mounts yet, so they do not run on one; launch on your default login")
	}
	dir, err := s.cfg.Logins.Path(run.AccountMemberID, profile.Name, run.AccountLogin)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(dir); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("scheduler: login %s has no directory on this server; it was removed", run.AccountLogin)
	}
	member := launcher
	// A session's login is mounted by what its files hold now, which must
	// still be what it signed in as: a session could plant another kind of
	// sign-in in a directory it could write.
	console := false
	if purpose != EnvironmentPurposeLogin {
		recorded, idErr := s.cfg.Logins.Identity(run.AccountMemberID, profile.Name, run.AccountLogin)
		if idErr != nil {
			return nil, idErr
		}
		shape, shapeErr := s.cfg.Logins.Inspect(run.AccountMemberID, profile.Name, run.AccountLogin)
		if shapeErr != nil {
			return nil, shapeErr
		}
		switch shape.Kind {
		case recorded.Kind:
		case "":
			return nil, fmt.Errorf("scheduler: %w: login %s: %s", ErrSignedOut, run.AccountLogin, noSignInNow)
		default:
			return nil, fmt.Errorf("scheduler: login %s no longer holds the %s sign-in it was added with; its owner removes it and adds it again", run.AccountLogin, recorded.Kind)
		}
		// Checked again on the variables this plan sets and the home's
		// settings: one added after the launch was admitted would pay instead
		// of the login.
		if payer := s.payer(env, launcher, profile.Name, run.Mode); payer != "" {
			return nil, EnvironmentPays(run.AccountLogin, payer)
		}
		// Empty, not absent: an image's own ENV would fill an absent one.
		for _, key := range append(slices.Clone(profile.EnvPassthrough), signInKeys(profile.Name, run.Mode)...) {
			if _, set := env[key]; !set {
				env[key] = ""
			}
		}
		console = shape.Kind == memberhome.LoginConsole
		if isolated && console {
			return nil, errors.New("scheduler: an Isolated session cannot use a Console login: it is a whole directory the session could change for later sessions; launch it at Default, or on another login")
		}
		if run.AccountMemberID != launcher {
			logins, listErr := s.cfg.Store.ListAgentLogins(context.Background(), run.AccountMemberID, profile.Name)
			if listErr != nil {
				return nil, listErr
			}
			i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == run.AccountLogin })
			if i < 0 || !logins[i].Shared || !logins[i].Automatic {
				return nil, fmt.Errorf("%w: login %s of this account is not available to you", permissions.ErrDenied, run.AccountLogin)
			}
		}
	}
	paths, err := profile.LoginPaths()
	if err != nil || len(paths) != 1 {
		return nil, fmt.Errorf("scheduler: %s has no single login file to mount an added login over", profile.Name)
	}
	if err = s.cfg.Homes.PrepareLoginMountpoint(member, paths[0], false); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if err = s.cfg.Homes.PrepareLoginMountpoint(member, memberhome.AnthropicProfileDir, true); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	// Subpath mounts: the runtime checks each one's inode in the container
	// before the agent starts, so a dropped mount fails the launch instead of
	// signing the agent in with the default login under this one's name.
	// The store the login does not use is read-only, so nothing can plant
	// a sign-in of the other kind there.
	sign := purpose == EnvironmentPurposeLogin
	mounts := []runtime.Mount{
		{HostPath: dir, Subpath: memberhome.LoginCredentials, ContainerPath: path.Join(home, paths[0]), ReadOnly: !sign && console},
		{HostPath: dir, Subpath: memberhome.LoginAnthropic, ContainerPath: path.Join(home, memberhome.AnthropicProfileDir), ReadOnly: !sign && !console},
	}
	configDir := filepath.Join(s.accountConfigRoot(), string(run.ID))
	if purpose == EnvironmentPurposeLogin {
		configDir = dir
		backups := path.Join(profile.AccountConfig.Dir, memberhome.LoginBackups)
		if err = os.Mkdir(filepath.Join(dir, memberhome.LoginBackups), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		if err = s.cfg.Homes.PrepareLoginMountpoint(member, backups, true); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		mounts = append(mounts, runtime.Mount{HostPath: dir, Subpath: memberhome.LoginBackups, ContainerPath: path.Join(home, backups)})
	}
	config, err := s.accountConfigMounts(configDir, member, profile, home, true)
	if err != nil {
		return nil, err
	}
	if purpose != EnvironmentPurposeLogin {
		if err = writeFileIn(configDir, accountConfigVolume, nil); err != nil {
			return nil, fmt.Errorf("scheduler: mark the session's account config volume: %w", err)
		}
	}
	for i, m := range config {
		if !m.ReadOnly {
			config[i].HostPath, config[i].Subpath = filepath.Dir(m.HostPath), filepath.Base(m.HostPath)
		}
	}
	env[profile.AccountConfig.Env] = path.Join(home, profile.AccountConfig.Dir)
	env["ANTHROPIC_CONFIG_DIR"] = path.Join(home, memberhome.AnthropicProfileDir)
	// The login's own profile is its directory's active one; a workspace
	// variable or image ENV naming another would sign Claude Code in with
	// none. Claude Code reads an empty one as unset.
	env["ANTHROPIC_PROFILE"] = ""
	return append(mounts, config...), nil
}

// addedLoginPath reports whether host is where an added login's subpath
// mounts come from, and whose ownership pass hands them over: owner's, for
// a directory of owner's logins, or the session's own, empty, for its copy
// of the configuration. Only what such a mount exposes is handed to the
// container's user, never the directory around it.
func (s *Scheduler) addedLoginPath(host string) (owner domain.MemberID, ok bool) {
	if rel, err := filepath.Rel(s.accountConfigRoot(), host); err == nil && rel != "." && filepath.IsLocal(rel) {
		return "", true
	}
	root := loginsRoot(s.cfg.Logins)
	if rel, err := filepath.Rel(root, host); root != "" && err == nil && rel != "." && filepath.IsLocal(rel) {
		first, _, _ := strings.Cut(filepath.ToSlash(rel), "/")
		return domain.MemberID(first), true
	}
	return "", false
}

func loginsRoot(l *memberhome.Logins) string {
	if l == nil {
		return ""
	}
	return l.Root()
}

// addedLoginProblem is why an added login cannot sign its agent in, read
// from its own files; empty when it can.
func (s *Scheduler) addedLoginProblem(account domain.Account, agent string) (string, error) {
	identity, err := s.cfg.Logins.Inspect(account.Member, agent, account.Login)
	if err != nil {
		return "", err
	}
	switch identity.Kind {
	case memberhome.LoginConsole:
		return "", nil
	case memberhome.LoginSubscription:
		data, err := s.cfg.Logins.ReadCredential(account.Member, agent, account.Login, 1<<20)
		if err != nil {
			return "", err
		}
		return quota.LoginProblem(agent, data, time.Now()), nil
	}
	return noSignInNow, nil
}

// noSignInNow is why a login whose files hold no sign-in cannot serve: a
// session on it may be rewriting its file in place as it refreshes, or the
// login was signed out.
const noSignInNow = "it holds no sign-in right now; a session on it may be refreshing it, so launch again; a login that stays so was signed out"

// loginRefusal is why a session in member's home, Isolated or not, may not
// run on login unless its owner names it, its automatic switch apart: it is
// not open to member, cannot sign in, or cannot be mounted. It is empty
// when the login is eligible. It reads member homes, login directories and
// the store only, and leaves where the agent's installation comes from to
// the caller.
func (s *Scheduler) loginRefusal(ctx context.Context, member domain.MemberID, login domain.Account, harnessName string, isolated bool) (string, error) {
	if login.Login.Added() {
		return s.addedLoginRefusal(ctx, member, login, harnessName, isolated)
	}
	if shareErr := s.checkDefaultLoginShared(ctx, member, login.Member, harnessName); shareErr != nil {
		return "it is not open to you", nil
	}
	state, refusal, err := s.CheckSharedLaunch(ctx, member, login.Member, harnessName)
	switch {
	case err != nil:
		return "", err
	case state != SharedLaunchable:
		return cmp.Or(refusal, "it cannot be shared"), nil
	case s.cfg.Homes == nil:
		return "", nil
	}
	profile, _, err := s.launchProfile(ctx, member, login.Member, harnessName)
	if err != nil {
		return "", err
	}
	if problem, blocks, loginErr := s.LoginProblem(member, login.Member, harnessName, profile); loginErr == nil && blocks {
		return problem, nil
	}
	// Beside added logins, a default login that cannot sign in would start
	// the agent signed out while another login could serve.
	if added, err := s.keepsAddedLogin(ctx, harnessName, login.Member); err != nil || !added {
		return "", err
	}
	return s.defaultSignInProblem(login.Member, harnessName, profile)
}

// keepsAddedLogin reports whether any of members keeps an added login of
// agent.
func (s *Scheduler) keepsAddedLogin(ctx context.Context, agent string, members ...domain.MemberID) (bool, error) {
	for _, member := range members {
		logins, err := s.cfg.Store.ListAgentLogins(ctx, member, agent)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(logins, func(l domain.AgentLogin) bool { return l.ID.Added() }) {
			return true, nil
		}
	}
	return false, nil
}

// defaultSignInProblem is why member's default login of agent, which
// profile describes, cannot sign it in; empty when it can. It holds no
// sign-in at all: no login file Cloudfield reads, and no apiKeyHelper or
// variable that signs agent in set in ~/.claude/settings.json. Or its
// subscription sign-in file is signed out, which Claude Code sends all the
// same, ahead of a Console sign-in beside it. Files it cannot read are an
// errLoginUnread.
func (s *Scheduler) defaultSignInProblem(member domain.MemberID, agent string, profile harness.Profile) (string, error) {
	identity, err := s.cfg.Homes.DefaultIdentity(member, agent)
	if err != nil && identity.Kind != memberhome.LoginSubscription {
		return "", fmt.Errorf("%w: %w", errLoginUnread, err)
	}
	var problem string
	switch identity.Kind {
	case "":
		problem = "it holds no sign-in"
	case memberhome.LoginSubscription:
		problem, _ = s.signInFileProblem(member, agent, profile)
	}
	if problem == "" {
		return "", nil
	}
	key, err := s.settingsSignIn(member, agent, nil)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errLoginUnread, err)
	}
	if key != "" {
		return "", nil
	}
	return problem, nil
}

// errLoginUnread marks a default login whose files cannot be read to tell
// whether it signs in. A launch that names no login starts on it in its
// place, counting its usage unknown.
var errLoginUnread = errors.New("its files cannot be read")

// addedLoginRefusal is loginRefusal for an added login: it must still
// exist, be member's own or on for shares and automatic, have mounts the
// session can take and the runtime can verify, and sign in.
func (s *Scheduler) addedLoginRefusal(ctx context.Context, member domain.MemberID, account domain.Account, agent string, isolated bool) (string, error) {
	if _, err := s.addedLoginProfile(ctx, member, account.Member, agent); err != nil {
		return err.Error(), nil
	}
	if s.sessionVMs() {
		return "session VMs cannot verify an added login's mounts yet", nil
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account.Member, agent)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == account.Login })
	if i < 0 || (member != account.Member && (!logins[i].Shared || !logins[i].Automatic)) {
		return "it is not open to you", nil
	}
	if refusal, err := s.addedLoginUnmountable(account, agent, isolated); err != nil || refusal != "" {
		return refusal, err
	}
	return s.addedLoginProblem(account, agent)
}

// addedLoginUnmountable is why a session, Isolated or not, cannot mount
// account's added login: its files no longer hold the kind of sign-in it
// was added with, or it is a Console login's directory an Isolated session
// could change for later sessions. Empty when it can.
func (s *Scheduler) addedLoginUnmountable(account domain.Account, agent string, isolated bool) (string, error) {
	shape, err := s.cfg.Logins.Inspect(account.Member, agent, account.Login)
	if err != nil {
		return "", err
	}
	recorded, err := s.cfg.Logins.Identity(account.Member, agent, account.Login)
	if err != nil {
		return "", err
	}
	switch {
	case shape.Kind != "" && shape.Kind != recorded.Kind:
		return fmt.Sprintf("it no longer holds the %s sign-in it was added with; its owner removes it and adds it again", recorded.Kind), nil
	case isolated && shape.Kind == memberhome.LoginConsole:
		return "an Isolated session cannot use a Console login: it is a whole directory the session could change for later sessions; launch it at Default, or on another login", nil
	}
	return "", nil
}

// checkAddedLogin refuses a launch in member's home, Isolated or not, on an
// added login that member may not use, that the session cannot mount or
// that cannot sign its agent in.
func (s *Scheduler) checkAddedLogin(ctx context.Context, member domain.MemberID, account domain.Account, agent string, isolated bool) error {
	if _, err := s.addedLoginProfile(ctx, member, account.Member, agent); err != nil {
		return fmt.Errorf("scheduler: %w", err)
	}
	if s.sessionVMs() {
		return errors.New("scheduler: session VMs cannot verify an added login's mounts yet, so they do not run on one; launch on the default login")
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account.Member, agent)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == account.Login })
	if i < 0 || (member != account.Member && (!logins[i].Shared || !logins[i].Automatic)) {
		return fmt.Errorf("%w: login %s of this account is not available to you", permissions.ErrDenied, account.Login)
	}
	refusal, err := s.addedLoginUnmountable(account, agent, isolated)
	if err != nil {
		return err
	}
	if refusal != "" {
		return fmt.Errorf("scheduler: login %s: %s", account.Login, refusal)
	}
	problem, err := s.addedLoginProblem(account, agent)
	if err != nil {
		return err
	}
	if problem != "" {
		return fmt.Errorf("scheduler: %w: login %s: %s; %s", ErrSignedOut, logins[i].Name, problem, readdSteps(agent, logins[i].Name))
	}
	return nil
}

// addedLoginName is the name of account's added login of agent, or its id
// when the name cannot be read.
func (s *Scheduler) addedLoginName(ctx context.Context, account domain.Account, agent string) string {
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account.Member, agent)
	if i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == account.Login }); err == nil && i >= 0 {
		return logins[i].Name
	}
	return string(account.Login)
}

// LoginPayer is what signs agent in, in mode, in a session in home's home in
// workspace, whatever added login it mounts: a workspace or server
// variable, or home's ~/.claude/settings.json. Empty when nothing does.
func (s *Scheduler) LoginPayer(ctx context.Context, workspace domain.WorkspaceID, home domain.MemberID, agent string, mode domain.LaunchMode) (string, error) {
	profile, ok := harness.Lookup(agent)
	if !ok || workspace == "" {
		return "", nil
	}
	return s.loginPayer(ctx, workspace, home, agent, profile, mode)
}

// checkDefaultLoginShared refuses a launch in member's home on another
// member's default login when its owner turned it off for shares or
// automatic off: only its owner names a login.
func (s *Scheduler) checkDefaultLoginShared(ctx context.Context, member, account domain.MemberID, agent string) error {
	if member == account {
		return nil
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account, agent)
	if err != nil {
		return err
	}
	if i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == domain.DefaultLogin }); i >= 0 && (!logins[i].Shared || !logins[i].Automatic) {
		return fmt.Errorf("%w: the default login of this account is not available to you", permissions.ErrDenied)
	}
	return nil
}

// StartLogin is the login a launch by member on account starts from when
// none is named: the account's first login in its owner's order that member
// may use without naming it, automatic and, for a teammate, on for shares.
// An account none of whose logins member may use is not shared with them;
// on their own account the launch is refused, naming each login and why it
// does not serve, since one that is automatic off serves only when named.
func (s *Scheduler) StartLogin(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, harness string, mode domain.LaunchMode, permission domain.Permission) (domain.LoginID, error) {
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account, harness)
	if err != nil || len(logins) == 0 {
		return "", err
	}
	// A variable or a setting that signs the agent in pays whatever the
	// login: such a launch chooses between no logins.
	if payer, payerErr := s.LoginPayer(ctx, workspace, member, harness, mode); payerErr != nil || payer != "" {
		return "", payerErr
	}
	var refused []string
	for _, l := range logins {
		if account != member && (!l.Automatic || !l.Shared) {
			continue
		}
		if !l.Automatic {
			refused = append(refused, l.Name+" is automatic off")
			continue
		}
		login := domain.Account{Member: account, Login: loginOf(l.ID)}
		refusal, refusalErr := s.loginRefusal(ctx, member, login, harness, permission == domain.PermissionIsolated)
		if errors.Is(refusalErr, errLoginUnread) {
			return login.Login, nil
		}
		if refusalErr != nil {
			refusal = refusalErr.Error()
		}
		if refusal == "" {
			return login.Login, nil
		}
		refused = append(refused, l.Name+": "+refusal)
	}
	if account != member {
		if len(refused) == 0 {
			return "", fmt.Errorf("%w: none of this account's logins is available to you, so it is not shared with you", permissions.ErrDenied)
		}
		return "", fmt.Errorf("%w: none of this account's logins can serve you: %s", permissions.ErrDenied, strings.Join(refused, "; "))
	}
	return "", fmt.Errorf("%w: %s; name one with cloudfield session --login <name>, or let Cloudfield choose it with cloudfield agent logins auto <name> on", ErrNoStartLogin, strings.Join(refused, "; "))
}

// ChildLogin is the login a subsession of owner starts from on account.
// inherited is its parent's current login there ("default" for the
// default login), or empty when the subsession named the account itself:
// then, as for a launch that names none, the account's first eligible login
// serves. The parent's login is inherited when it is automatic, owner owns
// it or it is on for shares, it can still run, and it is not billed per
// use, whichever its id: one named for the parent alone, or billed per use,
// is chosen for anew.
func (s *Scheduler) ChildLogin(ctx context.Context, workspace domain.WorkspaceID, owner, account domain.MemberID, inherited domain.LoginID, harness string, mode domain.LaunchMode, permission domain.Permission) (domain.LoginID, error) {
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account, harness)
	if err != nil || len(logins) == 0 || inherited == "" {
		if err != nil || len(logins) == 0 {
			return "", err
		}
		return s.StartLogin(ctx, workspace, owner, account, harness, mode, permission)
	}
	i := slices.IndexFunc(logins, func(l domain.AgentLogin) bool { return l.ID == inherited })
	inherits := i >= 0 && logins[i].Automatic && (owner == account || logins[i].Shared)
	login := domain.Account{Member: account, Login: loginOf(inherited)}
	if inherits {
		perUse, perUseErr := s.LoginPerUse(ctx, owner, login, harness)
		inherits = perUseErr == nil && !perUse
	}
	if payer, payerErr := s.LoginPayer(ctx, workspace, owner, harness, mode); payerErr != nil || payer != "" {
		return "", payerErr
	}
	if inherits {
		if refusal, refusalErr := s.loginRefusal(ctx, owner, login, harness, permission == domain.PermissionIsolated); refusalErr == nil && refusal == "" {
			return login.Login, nil
		}
	}
	return s.StartLogin(ctx, workspace, owner, account, harness, mode, permission)
}

// LoginPerUse reports whether login of agent, in a session in home's home,
// is billed per use: a Console login, or, once home keeps login rows, home's
// own default login kept as an API key or a Console profile or signed in by
// ~/.claude/settings.json other than with a subscription token, which Claude
// Code prefers to its login file. Another member's default login is shared only as a subscription, and a
// member who keeps no rows is shown as before logins existed. A default
// login whose files cannot be read is not billed per use: the flag only
// orders and labels, and the read must not fail what would run.
func (s *Scheduler) LoginPerUse(ctx context.Context, home domain.MemberID, login domain.Account, agent string) (bool, error) {
	perUse, _, err := s.loginBilling(ctx, home, login, agent)
	return perUse, err
}

// LoginUsageUnknown reports whether a launch that names no login counts the
// usage of login, its start, as unknown, so that it yields to a login with
// usage known to be left: login is billed per use, or it is home's own
// default login whose kind its files do not tell, beside added logins.
func (s *Scheduler) LoginUsageUnknown(ctx context.Context, home domain.MemberID, login domain.Account, agent string) (bool, error) {
	perUse, unread, err := s.loginBilling(ctx, home, login, agent)
	return perUse || unread, err
}

// loginBilling is LoginPerUse, and whether login is a default login whose
// kind cannot be read.
func (s *Scheduler) loginBilling(ctx context.Context, home domain.MemberID, login domain.Account, agent string) (perUse, unread bool, err error) {
	if login.Login.Added() {
		identity, err := s.cfg.Logins.Identity(login.Member, agent, login.Login)
		if err != nil {
			return false, false, fmt.Errorf("scheduler: whether the login is billed per use: %w", err)
		}
		return identity.PerUse(), false, nil
	}
	if login.Member != home || s.cfg.Homes == nil {
		return false, false, nil
	}
	if keeps, err := s.cfg.Store.ListAgentLogins(ctx, login.Member, agent); err != nil || len(keeps) == 0 {
		return false, false, err
	}
	if settings, settingsErr := s.cfg.Homes.ClaudeSettings(home); settingsErr == nil && agent == "claude" {
		// A subscription's own token, as `claude setup-token` makes it.
		env := maps.Clone(settings.Env)
		delete(env, "CLAUDE_CODE_OAUTH_TOKEN")
		if settings.APIKeyHelper || signingKey(env, agent, "") != "" {
			return true, false, nil
		}
	}
	identity, idErr := s.cfg.Homes.DefaultIdentity(login.Member, agent)
	return identity.PerUse(), idErr != nil && identity.Kind == "", nil
}

func loginOf(id domain.LoginID) domain.LoginID {
	if id.Added() {
		return id
	}
	return ""
}

// LoginInUse lists, with the member who owns each, the sessions a removal
// of an added login must wait for: those on it that have not ended, those a
// move is taking to it, and the subsessions queued or launching on it.
// Callers hold the launch authorization mutex, under which launches and
// moves choose their login.
func (s *Scheduler) LoginInUse(ctx context.Context, account domain.Account) (map[domain.RunID]domain.MemberID, error) {
	// Before recovery has rebuilt the live sessions and read every retained
	// container's record, a retained container on the login could go unseen.
	select {
	case <-s.recoveryReady:
	default:
		return nil, errors.New("the server is still recovering its sessions; try again in a moment")
	}
	if !s.retainedListed.Load() {
		if unread, _ := s.retainedUnread.Load().(string); unread != "" {
			return nil, fmt.Errorf("the server could not read the record of a kept session container, so that container may still mount this login: %s; the server reads its records again every hour, and the server's operator repairs or removes that file if this persists", unread)
		}
		return nil, errors.New("the server has not yet read the record of every kept session container, so one may still mount this login; try again in a moment")
	}
	runs, err := s.cfg.Store.ListActiveRuns(ctx)
	if err != nil {
		return nil, err
	}
	inUse := make(map[domain.RunID]domain.MemberID)
	for _, r := range runs {
		if r.Account() == account {
			inUse[r.ID] = r.MemberID
		}
	}
	var moving, held []domain.RunID
	s.mu.Lock()
	for id, entry := range s.runs {
		if entry.accountSwap != nil && entry.accountSwap.to() == account {
			moving = append(moving, id)
		}
		if entry.containerID != "" {
			held = append(held, id)
		}
	}
	s.mu.Unlock()
	for _, id := range moving {
		r, getErr := s.cfg.Store.GetRun(ctx, id)
		if getErr != nil {
			return nil, fmt.Errorf("session %s moving to the login: %w", id, getErr)
		}
		inUse[id] = r.MemberID
	}
	// A session that ended keeps its container while it may be reopened,
	// still mounting the login.
	for _, id := range held {
		r, getErr := s.cfg.Store.GetRun(ctx, id)
		if getErr != nil {
			// Not named: it may be a teammate's session.
			return nil, fmt.Errorf("a kept session container's session could not be read, so it may still mount this login: %w", getErr)
		}
		if r.Account() == account {
			inUse[id] = r.MemberID
		}
	}
	if children, ok := s.cfg.Store.(interface {
		ChildrenInState(context.Context, domain.ChildState) ([]*domain.ChildReservation, error)
	}); ok {
		for _, state := range []domain.ChildState{domain.ChildQueued, domain.ChildLaunching} {
			reserved, listErr := children.ChildrenInState(ctx, state)
			if listErr != nil {
				return nil, listErr
			}
			for _, c := range reserved {
				if (domain.Account{Member: c.AccountID, Login: c.AccountLogin}) == account {
					inUse[c.RunID] = c.OwnerID
				}
			}
		}
	}
	return inUse, nil
}
