package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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
		// Checked again on the variables this plan sets: one added after the
		// launch was admitted would pay instead of the login.
		if signsIn(env, profile.Name, run.Mode) {
			return nil, EnvironmentPays(run.AccountLogin)
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
	// Beside added logins, a default login that holds no sign-in at all
	// would start the agent signed out while another login could serve.
	logins, err := s.cfg.Store.ListAgentLogins(ctx, login.Member, harnessName)
	if err != nil || !slices.ContainsFunc(logins, func(l domain.AgentLogin) bool { return l.ID.Added() }) {
		return "", err
	}
	// One whose files cannot be read is left to the checks above, as before.
	if identity, idErr := s.cfg.Homes.DefaultIdentity(login.Member, harnessName); idErr == nil && identity.Kind == "" {
		return "it holds no sign-in", nil
	}
	return "", nil
}

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
		return fmt.Errorf("scheduler: %w: login %s: %s; its owner signs in again with cloudfield agent login %s", ErrSignedOut, account.Login, problem, agent)
	}
	return nil
}

// EnvironmentSignsIn reports whether a variable of workspace signs agent
// in, whatever login a session mounts.
func (s *Scheduler) EnvironmentSignsIn(ctx context.Context, workspace domain.WorkspaceID, agent string, mode domain.LaunchMode) (bool, error) {
	profile, ok := harness.Lookup(agent)
	if !ok || workspace == "" {
		return false, nil
	}
	return s.environmentSignsIn(ctx, workspace, agent, profile, mode)
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
	// A workspace variable that signs the agent in pays whatever the login:
	// such a launch chooses between no logins.
	if byEnv, envErr := s.EnvironmentSignsIn(ctx, workspace, harness, mode); envErr != nil || byEnv {
		return "", envErr
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
	return "", fmt.Errorf("%w: %s; name one with cloudfield session --login <name>", ErrNoStartLogin, strings.Join(refused, "; "))
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
		var identity memberhome.LoginIdentity
		var idErr error
		switch {
		case login.Login.Added():
			identity, idErr = s.cfg.Logins.Identity(account, harness, login.Login)
		case s.cfg.Homes != nil:
			identity, idErr = s.cfg.Homes.DefaultIdentity(account, harness)
		}
		inherits = idErr == nil && !identity.PerUse()
	}
	if byEnv, envErr := s.EnvironmentSignsIn(ctx, workspace, harness, mode); envErr != nil || byEnv {
		return "", envErr
	}
	if inherits {
		if refusal, refusalErr := s.loginRefusal(ctx, owner, login, harness, permission == domain.PermissionIsolated); refusalErr == nil && refusal == "" {
			return login.Login, nil
		}
	}
	return s.StartLogin(ctx, workspace, owner, account, harness, mode, permission)
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
		return nil, errors.New("the server could not yet read the record of every kept session container, so one may still mount this login; try again in a few minutes, and see the server log if it persists")
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
