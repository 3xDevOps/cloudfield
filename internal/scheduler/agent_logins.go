package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/harness"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/runtime"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// loginSignInOpenWithin is how long a sign-in agent.login.add reserved
// waits for its terminal; loginSignInProvision bounds starting its
// container; loginSignInTimeout is how long the sign-in may then run;
// loginSignInKept is how long its result waits to be collected.
const (
	loginSignInOpenWithin = 10 * time.Minute
	loginSignInProvision  = 5 * time.Minute
	loginSignInTimeout    = 15 * time.Minute
	loginSignInKept       = 10 * time.Minute
)

// ErrNoAddedLogins refuses an added login on a server or for an agent that
// keeps none.
var ErrNoAddedLogins = errors.New("only Claude Code keeps added logins")

// loginSignIn is the sign-in of one new login. done closes once ended is
// set, with login or err.
type loginSignIn struct {
	member domain.MemberID
	agent  string
	id     domain.LoginID
	name   string
	// started: its terminal opened; created: this attempt made the login's
	// directory, which only it may remove.
	started bool
	created bool
	ended   bool
	timer   *time.Timer
	holder  *terminalSupervision
	done    chan struct{}
	login   domain.AgentLogin
	err     error
}

func loginSignInCreationKey(id domain.LoginID) string { return "login-signin-" + string(id) }

// addedLoginProfile is agent's profile when it keeps added logins.
func (s *Scheduler) addedLoginProfile(agent string) (harness.Profile, error) {
	profile, ok := harness.Lookup(agent)
	if s.cfg.Logins == nil || !ok || agent != "claude" || profile.AccountConfig == nil || profile.LoginCommand == "" {
		return harness.Profile{}, ErrNoAddedLogins
	}
	return profile, nil
}

// BeginLoginSignIn reserves a new login of agent named name for member and
// returns its id; the terminal opened with that id runs the sign-in. A
// member runs one sign-in at a time.
func (s *Scheduler) BeginLoginSignIn(ctx context.Context, member domain.MemberID, agent, name string) (domain.LoginID, error) {
	if _, err := s.addedLoginProfile(agent); err != nil {
		return "", err
	}
	if err := domain.ValidateLoginName(name); err != nil {
		return "", err
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, member, agent)
	if err != nil {
		return "", err
	}
	for _, l := range logins {
		if l.Name == name {
			return "", fmt.Errorf("%w: you already have a login named %q", store.ErrConflict, name)
		}
	}
	id, err := store.NewLoginID()
	if err != nil {
		return "", err
	}
	sign := &loginSignIn{member: member, agent: agent, id: id, name: name, done: make(chan struct{})}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.loginSignIns {
		if other.member == member && !other.ended {
			return "", fmt.Errorf("%w: the sign-in of login %q is still running; finish it first", store.ErrConflict, other.name)
		}
	}
	if s.loginSignIns == nil {
		s.loginSignIns = make(map[domain.LoginID]*loginSignIn)
	}
	s.loginSignIns[id] = sign
	sign.timer = time.AfterFunc(loginSignInOpenWithin, func() {
		s.mu.Lock()
		unopened := !sign.started
		sign.started = true
		s.mu.Unlock()
		if unopened {
			s.endLoginSignIn(sign, domain.AgentLogin{}, errors.New("the sign-in terminal was not opened in time; nothing was added"))
		}
	})
	return id, nil
}

// signInOf returns member's sign-in id; another member's reads as unknown.
// Caller holds s.mu.
func (s *Scheduler) signInOf(member domain.MemberID, id domain.LoginID) (*loginSignIn, error) {
	sign := s.loginSignIns[id]
	if sign == nil || sign.member != member {
		return nil, fmt.Errorf("%w: no sign-in of a login %s", store.ErrNotFound, id)
	}
	return sign, nil
}

// StartLoginSignIn runs the sign-in member reserved as id: it creates the
// login's directory and a container of its own that mounts the member's
// home with the new login over the default one, and runs the agent's own
// sign-in command there on a PTY session it returns. A terminal opened
// again while the sign-in runs reattaches to it. When the command ends the
// container goes, and the login is added when the agent saved one.
func (s *Scheduler) StartLoginSignIn(ctx context.Context, member domain.MemberID, id domain.LoginID, cols, rows uint) (ptyhost.SessionKey, error) {
	key := ptyhost.LoginSignInSession(member, id)
	s.mu.Lock()
	sign, err := s.signInOf(member, id)
	switch {
	case err != nil:
	case sign.ended:
		err = fmt.Errorf("%w: the sign-in of login %s has ended; add the login again", store.ErrConflict, id)
	case sign.started && slices.Contains(s.cfg.PTY.ActiveSessions(string(key)), key):
		s.mu.Unlock()
		return key, nil
	case sign.started:
		err = fmt.Errorf("%w: the sign-in of login %s is starting; open its terminal again in a moment", store.ErrConflict, id)
	}
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	sign.started = true
	sign.timer.Stop()
	sign.holder = &terminalSupervision{member: member}
	s.mu.Unlock()
	att, err := s.startSignInContainer(ctx, sign, cols, rows)
	if err == nil {
		ending := &endingAttachment{Attachment: att, end: func() { go s.finishLoginSignIn(sign) }}
		if err = s.cfg.PTY.StartSession(ctx, key, ending); err != nil {
			_ = att.Close()
		}
	}
	if err != nil {
		err = fmt.Errorf("scheduler: start the sign-in of login %s: %w", id, err)
		s.endLoginSignIn(sign, domain.AgentLogin{}, errors.Join(err, s.cleanUpSignIn(context.WithoutCancel(ctx), sign)))
		return "", err
	}
	s.mu.Lock()
	sign.timer = time.AfterFunc(loginSignInTimeout, func() {
		if removeErr := s.removeByCreationKey(context.Background(), loginSignInCreationKey(id)); removeErr != nil {
			slog.Warn("scheduler: end a sign-in that ran too long", "login", id, "error", removeErr)
		}
	})
	s.mu.Unlock()
	return key, nil
}

func (s *Scheduler) startSignInContainer(ctx context.Context, sign *loginSignIn, cols, rows uint) (runtime.Attachment, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), loginSignInProvision)
	defer cancel()
	profile, err := s.addedLoginProfile(sign.agent)
	if err != nil {
		return nil, err
	}
	member, err := s.cfg.Store.GetMember(ctx, sign.member)
	if err != nil {
		return nil, err
	}
	release, err := s.reserveCapacity(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if _, err = s.cfg.Logins.Add(sign.member, sign.agent, sign.id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	sign.created = true
	s.mu.Unlock()
	run := &domain.Run{MemberID: member.ID, AccountMemberID: member.ID, AccountLogin: sign.id, Harness: sign.agent}
	plan, err := s.BuildEnvironmentPlan(ctx, run, nil, member, profile, EnvironmentPurposeLogin)
	if err != nil {
		return nil, err
	}
	if err = s.reserveTerminalUser(ctx, sign.holder, plan.User); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if sign.holder.userReservation != nil {
		sign.holder.userReservation.owner = "the sign-in of login " + string(sign.id)
	}
	s.mu.Unlock()
	if err = s.applyRunOwnership(nil, &domain.Run{}, member.ID, plan.Mounts, plan.User); err != nil {
		return nil, err
	}
	limits, err := s.runLimits(ctx, domain.RunLimits{})
	if err != nil {
		return nil, err
	}
	cid, err := s.createContainer(ctx, runtime.Spec{
		Name:             "aether-login-" + string(sign.id),
		Image:            plan.Image,
		Env:              plan.Env,
		WorkingDir:       plan.Home,
		Command:          assistantIdle,
		Mounts:           plan.Mounts,
		User:             plan.User,
		CreationKey:      loginSignInCreationKey(sign.id),
		CPULimit:         limits.CPUs,
		MemoryLimitBytes: limits.MemoryBytes,
		PidsLimit:        limits.Pids,
	})
	if err != nil {
		return nil, err
	}
	if err := s.cfg.Runtime.Start(ctx, cid); err != nil {
		return nil, err
	}
	return s.cfg.Runtime.ExecTTY(ctx, cid, strings.Fields(profile.LoginCommand), plan.Home, cols, rows)
}

// cleanUpSignIn removes a sign-in's container, retrying a failed removal,
// and only once it is gone releases the member's container user and the
// sign-in's transcript. While the container may live, its directory stays:
// the next start finds the container by it.
func (s *Scheduler) cleanUpSignIn(ctx context.Context, sign *loginSignIn) error {
	var err error
	for wait := time.Second; ; wait *= 2 {
		if err = s.removeByCreationKey(ctx, loginSignInCreationKey(sign.id)); err == nil || wait > 4*time.Second {
			break
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return errors.Join(err, ctx.Err())
		}
	}
	if err != nil {
		s.mu.Lock()
		sign.created = false
		s.mu.Unlock()
		return fmt.Errorf("its container could not be removed, so nothing was added; the server removes it at its next start: %w", err)
	}
	s.releaseTerminalReservation(sign.holder)
	if err := s.cfg.PTY.RemoveSessionTranscript(ctx, ptyhost.LoginSignInSession(sign.member, sign.id)); err != nil {
		slog.Warn("scheduler: remove a sign-in's transcript", "login", sign.id, "error", err)
	}
	return nil
}

// finishLoginSignIn runs once the sign-in command ended: it removes the
// container, then adds the login the agent saved or removes its directory.
func (s *Scheduler) finishLoginSignIn(sign *loginSignIn) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.mu.Lock()
	sign.timer.Stop()
	s.mu.Unlock()
	if err := s.cleanUpSignIn(ctx, sign); err != nil {
		s.endLoginSignIn(sign, domain.AgentLogin{}, err)
		return
	}
	login, err := s.settleLoginSignIn(ctx, sign)
	s.endLoginSignIn(sign, login, err)
}

// endLoginSignIn records the sign-in's first result, removing a failed
// login's directory when this attempt created it, and forgets the sign-in
// once the result has waited long enough to be collected.
func (s *Scheduler) endLoginSignIn(sign *loginSignIn, login domain.AgentLogin, err error) {
	s.mu.Lock()
	ended, created := sign.ended, sign.created
	sign.ended = true
	s.mu.Unlock()
	if ended {
		return
	}
	if err != nil && created {
		if removeErr := s.cfg.Logins.Remove(sign.member, sign.agent, sign.id); removeErr != nil {
			slog.Warn("scheduler: remove a login that was not added", "login", sign.id, "error", removeErr)
		}
	}
	s.mu.Lock()
	sign.login, sign.err = login, err
	close(sign.done)
	s.mu.Unlock()
	time.AfterFunc(loginSignInKept, func() {
		s.mu.Lock()
		if s.loginSignIns[sign.id] == sign {
			delete(s.loginSignIns, sign.id)
		}
		s.mu.Unlock()
	})
}

// EndLoginSignIns ends member's running sign-ins, before the member is
// removed or merged into another: each one's container goes, and the login
// it would have added is refused once the member is gone.
func (s *Scheduler) EndLoginSignIns(ctx context.Context, member domain.MemberID) error {
	s.mu.Lock()
	var running []*loginSignIn
	for _, sign := range s.loginSignIns {
		if sign.member == member && !sign.ended {
			running = append(running, sign)
		}
	}
	s.mu.Unlock()
	var errs []error
	for _, sign := range running {
		s.mu.Lock()
		started := sign.started
		sign.started = true
		s.mu.Unlock()
		if !started {
			s.endLoginSignIn(sign, domain.AgentLogin{}, errors.New("the member was removed or merged; nothing was added"))
			continue
		}
		errs = append(errs, s.removeByCreationKey(ctx, loginSignInCreationKey(sign.id)))
	}
	return errors.Join(errs...)
}

// settleLoginSignIn adds the login the sign-in saved. It refuses a Console
// API key, a login that never signed in and one of an account the member
// already has a login of.
func (s *Scheduler) settleLoginSignIn(ctx context.Context, sign *loginSignIn) (domain.AgentLogin, error) {
	identity, err := s.cfg.Logins.Inspect(sign.member, sign.agent, sign.id)
	if err != nil {
		return domain.AgentLogin{}, err
	}
	switch identity.Kind {
	case "":
		return domain.AgentLogin{}, errors.New("the sign-in ended before Claude Code saved a login; nothing was added")
	case memberhome.LoginAPIKey:
		return domain.AgentLogin{}, errors.New("the sign-in saved an Anthropic Console API key, which Cloudfield does not keep as an added login: Claude Code copies it into backups in your home, where it would outlive the login. Keep the key as your default login or as a workspace secret (docs/harnesses.md#use-an-api-key); nothing was added")
	}
	lock := s.memberLock(&s.loginLocks, sign.member)
	lock.Lock()
	defer lock.Unlock()
	logins, err := s.cfg.Store.ListAgentLogins(ctx, sign.member, sign.agent)
	if err != nil {
		return domain.AgentLogin{}, err
	}
	same := func(name string) error {
		return fmt.Errorf("this sign-in is the account and organisation your login %q is signed in to, so it was not added; sign in to another account to add a login", name)
	}
	unread := func(name string, err error) error {
		return fmt.Errorf("your login %q could not be read to check this sign-in is another account, so it was not added: %w", name, err)
	}
	def, err := s.cfg.Homes.DefaultIdentity(sign.member, sign.agent)
	if err != nil {
		return domain.AgentLogin{}, unread(string(domain.DefaultLogin), err)
	}
	if def.Kind != "" && identity.SameAccount(def) {
		return domain.AgentLogin{}, same(string(domain.DefaultLogin))
	}
	for _, l := range logins {
		if !l.ID.Added() {
			continue
		}
		other, otherErr := s.cfg.Logins.Identity(sign.member, sign.agent, l.ID)
		if otherErr != nil {
			return domain.AgentLogin{}, unread(l.Name, otherErr)
		}
		if identity.SameAccount(other) {
			return domain.AgentLogin{}, same(l.Name)
		}
	}
	if err = s.cfg.Logins.Settle(sign.member, sign.agent, sign.id, identity); err != nil {
		return domain.AgentLogin{}, err
	}
	login, err := s.cfg.Store.AddAgentLogin(ctx, domain.AgentLogin{
		MemberID: sign.member, Agent: sign.agent, ID: sign.id, Name: sign.name, CreatedAt: time.Now().UTC(),
	}, s.addedLoginPlace(sign, identity, logins))
	switch {
	case errors.Is(err, store.ErrConflict):
		return domain.AgentLogin{}, fmt.Errorf("the name %q was taken while you signed in; add the login again under another name", sign.name)
	case errors.Is(err, store.ErrNotFound):
		return domain.AgentLogin{}, errors.New("the member was removed or merged while signing in; nothing was added")
	}
	return login, err
}

// addedLoginPlace is the login a new one goes just before: a subscription
// login goes last among the subscription logins, before the first login
// billed per use, and one billed per use goes last of all ("").
func (s *Scheduler) addedLoginPlace(sign *loginSignIn, identity memberhome.LoginIdentity, logins []domain.AgentLogin) domain.LoginID {
	if identity.PerUse() {
		return ""
	}
	if len(logins) == 0 {
		logins = []domain.AgentLogin{{ID: domain.DefaultLogin}}
	}
	for _, l := range logins {
		var other memberhome.LoginIdentity
		var err error
		if l.ID.Added() {
			other, err = s.cfg.Logins.Identity(sign.member, sign.agent, l.ID)
		} else {
			other, err = s.cfg.Homes.DefaultIdentity(sign.member, sign.agent)
		}
		if err == nil && other.PerUse() {
			return l.ID
		}
	}
	return ""
}

// LoginSignInResult waits for member's sign-in id to end and returns the
// login it added, or why it added none.
func (s *Scheduler) LoginSignInResult(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID) (domain.AgentLogin, error) {
	s.mu.Lock()
	sign, err := s.signInOf(member, id)
	if err == nil && sign.agent != agent {
		err = fmt.Errorf("%w: no sign-in of a %s login %s", store.ErrNotFound, agent, id)
	}
	s.mu.Unlock()
	if err != nil {
		return domain.AgentLogin{}, err
	}
	select {
	case <-sign.done:
	case <-ctx.Done():
		return domain.AgentLogin{}, ctx.Err()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return sign.login, sign.err
}

// endingAttachment calls end once, when the process's output ends or the
// attachment closes.
type endingAttachment struct {
	runtime.Attachment
	once sync.Once
	end  func()
}

func (a *endingAttachment) Stdout() io.Reader { return endingReader{a.Attachment.Stdout(), a} }

func (a *endingAttachment) Close() error {
	err := a.Attachment.Close()
	a.once.Do(a.end)
	return err
}

type endingReader struct {
	io.Reader
	a *endingAttachment
}

func (r endingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err != nil {
		r.a.once.Do(r.a.end)
	}
	return n, err
}

// addedLoginMounts mounts run's added login from its directory over the
// default login's three stores: the subscription sign-in file, the
// Anthropic profile directory, and a copy of the member's configuration
// without the account keys, as a session on a shared account gets. A
// sign-in's copy is scratch in the login's directory, and Claude Code's
// backups of it stay there too, out of the member's home.
func (s *Scheduler) addedLoginMounts(run *domain.Run, profile harness.Profile, home string, env map[string]string, purpose EnvironmentPurpose) ([]runtime.Mount, error) {
	if _, err := s.addedLoginProfile(profile.Name); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if s.sessionVMs() {
		return nil, errors.New("scheduler: session VMs cannot verify an added login's mounts yet, so they do not run on one; launch on your default login")
	}
	member := run.AccountMemberID
	dir, err := s.cfg.Logins.Path(member, profile.Name, run.AccountLogin)
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Lstat(dir); statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("scheduler: login %s has no directory on this server; it was removed", run.AccountLogin)
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
	mounts := []runtime.Mount{
		{HostPath: dir, Subpath: memberhome.LoginCredentials, ContainerPath: path.Join(home, paths[0])},
		{HostPath: dir, Subpath: memberhome.LoginAnthropic, ContainerPath: path.Join(home, memberhome.AnthropicProfileDir)},
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
		// The home's own configuration, where a default login's API key
		// lives, is read-only to the sign-in, which writes the copy instead.
		if _, statErr := s.cfg.Homes.LoginPathIsDir(member, profile.AccountConfig.File); statErr == nil {
			homePath, pathErr := s.cfg.Homes.Path(member)
			if pathErr != nil {
				return nil, fmt.Errorf("scheduler: %w", pathErr)
			}
			mounts = append(mounts, runtime.Mount{HostPath: homePath, Subpath: profile.AccountConfig.File, ContainerPath: path.Join(home, profile.AccountConfig.File), ReadOnly: true})
		}
	}
	config, err := s.accountConfigMounts(configDir, member, profile, home)
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
	return append(mounts, config...), nil
}

// addedLoginPath reports whether host is where an added login's mounts come
// from: its directory, or the session's copy of the configuration. Only what
// such a subpath mount exposes is handed to the container's user, never the
// directory around it.
func (s *Scheduler) addedLoginPath(host string) bool {
	for _, root := range []string{s.accountConfigRoot(), loginsRoot(s.cfg.Logins)} {
		if rel, err := filepath.Rel(root, host); root != "" && err == nil && rel != "." && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

func loginsRoot(l *memberhome.Logins) string {
	if l == nil {
		return ""
	}
	return l.Root()
}

// ReconcileLogins makes the login directories match the rows: a directory
// a merge gave another member moves to them, and one with no row, a
// sign-in that ended or that the server stopped during, goes with its
// container and transcript. A directory stays while its container may
// live, so a later reconcile finds the container again.
func (s *Scheduler) ReconcileLogins(ctx context.Context, member domain.MemberID) error {
	if s.cfg.Logins == nil {
		return nil
	}
	stored, err := s.cfg.Logins.List(member)
	if err != nil {
		return err
	}
	var errs []error
	for _, l := range stored {
		s.mu.Lock()
		sign := s.loginSignIns[l.ID]
		running := sign != nil && !sign.ended
		s.mu.Unlock()
		if running {
			continue
		}
		owner, err := s.cfg.Store.AgentLoginOwner(ctx, l.Agent, l.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			if removeErr := s.removeByCreationKey(ctx, loginSignInCreationKey(l.ID)); removeErr != nil {
				errs = append(errs, fmt.Errorf("login %s: %w", l.ID, removeErr))
				continue
			}
			if s.cfg.PTY != nil {
				errs = append(errs, s.cfg.PTY.RemoveSessionTranscript(ctx, ptyhost.LoginSignInSession(l.Member, l.ID)))
			}
			errs = append(errs, s.cfg.Logins.Remove(l.Member, l.Agent, l.ID))
		case err != nil:
			errs = append(errs, fmt.Errorf("login %s: %w", l.ID, err))
		case owner != l.Member:
			errs = append(errs, s.cfg.Logins.Move(l.Member, owner, l.Agent, l.ID))
		}
	}
	return errors.Join(errs...)
}
