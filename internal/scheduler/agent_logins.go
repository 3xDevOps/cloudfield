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
// waits for its terminal; loginSignInTimeout is how long the sign-in may
// then run; loginSignInKept is how long its result waits to be collected.
const (
	loginSignInOpenWithin = 10 * time.Minute
	loginSignInTimeout    = 15 * time.Minute
	loginSignInKept       = 10 * time.Minute
)

// ErrNoAddedLogins refuses an added login on a server or for an agent that
// keeps none.
var ErrNoAddedLogins = errors.New("only Claude Code keeps added logins")

// loginSignIn is the sign-in of one new login. done closes once login or
// err is set.
type loginSignIn struct {
	member  domain.MemberID
	agent   string
	id      domain.LoginID
	name    string
	started bool
	ended   bool
	timer   *time.Timer
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
// returns its id; the terminal opened with that id runs the sign-in.
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
		if other.member == member && other.agent == agent && other.name == name {
			return "", fmt.Errorf("%w: a sign-in of a login named %q is already running", store.ErrConflict, name)
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
// sign-in command there on a PTY session it returns. When the command ends
// the container goes, and the login is added when the agent saved one.
func (s *Scheduler) StartLoginSignIn(ctx context.Context, member domain.MemberID, id domain.LoginID, cols, rows uint) (ptyhost.SessionKey, error) {
	s.mu.Lock()
	sign, err := s.signInOf(member, id)
	if err == nil && sign.started {
		err = fmt.Errorf("%w: the sign-in of login %s has started already", store.ErrConflict, id)
	}
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	sign.started = true
	sign.timer.Stop()
	sign.timer = time.AfterFunc(loginSignInTimeout, func() {
		if err := s.removeByCreationKey(context.Background(), loginSignInCreationKey(id)); err != nil {
			slog.Warn("scheduler: end a sign-in that ran too long", "login", id, "error", err)
		}
	})
	s.mu.Unlock()
	key := ptyhost.LoginSignInSession(member, id)
	holder := &terminalSupervision{member: member}
	att, err := s.startSignInContainer(ctx, sign, holder, cols, rows)
	if err == nil {
		ending := &endingAttachment{Attachment: att, end: func() { go s.finishLoginSignIn(sign, holder) }}
		if err = s.cfg.PTY.StartSession(ctx, key, ending); err != nil {
			_ = att.Close()
		}
	}
	if err != nil {
		err = fmt.Errorf("scheduler: start the sign-in of login %s: %w", id, err)
		s.cleanUpSignIn(context.WithoutCancel(ctx), sign, holder)
		s.endLoginSignIn(sign, domain.AgentLogin{}, err)
		return "", err
	}
	return key, nil
}

func (s *Scheduler) startSignInContainer(ctx context.Context, sign *loginSignIn, holder *terminalSupervision, cols, rows uint) (runtime.Attachment, error) {
	profile, err := s.addedLoginProfile(sign.agent)
	if err != nil {
		return nil, err
	}
	member, err := s.cfg.Store.GetMember(ctx, sign.member)
	if err != nil {
		return nil, err
	}
	if _, err := s.cfg.Logins.Create(sign.member, sign.agent, sign.id); err != nil {
		return nil, err
	}
	unlock := s.lockRunCaches(member.ID, member.ID)
	defer unlock()
	run := &domain.Run{MemberID: member.ID, AccountMemberID: member.ID, AccountLogin: sign.id, Harness: sign.agent}
	plan, err := s.BuildEnvironmentPlan(ctx, run, nil, member, profile, EnvironmentPurposeLogin)
	if err != nil {
		return nil, err
	}
	if err := s.reserveTerminalUser(ctx, holder, plan.User); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if holder.userReservation != nil {
		holder.userReservation.owner = "the sign-in of login " + string(sign.id)
	}
	s.mu.Unlock()
	if err := s.applyRunOwnership(nil, &domain.Run{}, member.ID, plan.Mounts, plan.User); err != nil {
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

// cleanUpSignIn removes a sign-in's container and its hold on the member's
// container user.
func (s *Scheduler) cleanUpSignIn(ctx context.Context, sign *loginSignIn, holder *terminalSupervision) {
	if err := s.removeByCreationKey(ctx, loginSignInCreationKey(sign.id)); err != nil {
		slog.Warn("scheduler: remove a sign-in's container", "login", sign.id, "error", err)
	}
	s.releaseTerminalReservation(holder)
}

// finishLoginSignIn runs once the sign-in command ended: it removes the
// container, then adds the login the agent saved or removes its directory.
func (s *Scheduler) finishLoginSignIn(sign *loginSignIn, holder *terminalSupervision) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	s.mu.Lock()
	sign.timer.Stop()
	s.mu.Unlock()
	s.cleanUpSignIn(ctx, sign, holder)
	if err := s.cfg.PTY.RemoveSessionTranscript(ctx, ptyhost.LoginSignInSession(sign.member, sign.id)); err != nil {
		slog.Warn("scheduler: remove a sign-in's transcript", "login", sign.id, "error", err)
	}
	login, err := s.settleLoginSignIn(ctx, sign)
	s.endLoginSignIn(sign, login, err)
}

// endLoginSignIn records the sign-in's first result, removing the login's
// directory when it failed, and forgets the sign-in once the result has
// waited long enough to be collected.
func (s *Scheduler) endLoginSignIn(sign *loginSignIn, login domain.AgentLogin, err error) {
	s.mu.Lock()
	ended := sign.ended
	sign.ended = true
	s.mu.Unlock()
	if ended {
		return
	}
	if err != nil {
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
		return domain.AgentLogin{}, errors.New("Claude Code saved an Anthropic Console API key, which Cloudfield does not keep as an added login: Claude Code copies it into backups in your home, where it would outlive the login. Keep the key as your default login or as a workspace secret (docs/harnesses.md#use-an-api-key); nothing was added")
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
	if def, defErr := s.cfg.Homes.DefaultIdentity(sign.member, sign.agent); defErr == nil && identity.SameAccount(def) {
		return domain.AgentLogin{}, same(string(domain.DefaultLogin))
	}
	for _, l := range logins {
		if !l.ID.Added() {
			continue
		}
		if other, otherErr := s.cfg.Logins.Identity(sign.member, sign.agent, l.ID); otherErr == nil && identity.SameAccount(other) {
			return domain.AgentLogin{}, same(l.Name)
		}
	}
	if err := s.cfg.Logins.Settle(sign.member, sign.agent, sign.id, identity); err != nil {
		return domain.AgentLogin{}, err
	}
	login, err := s.cfg.Store.AddAgentLogin(ctx, domain.AgentLogin{
		MemberID: sign.member, Agent: sign.agent, ID: sign.id, Name: sign.name, CreatedAt: time.Now().UTC(),
	}, s.addedLoginPlace(sign, identity, logins))
	if errors.Is(err, store.ErrConflict) {
		return domain.AgentLogin{}, fmt.Errorf("the name %q was taken while you signed in; add the login again under another name", sign.name)
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
func (s *Scheduler) LoginSignInResult(ctx context.Context, member domain.MemberID, id domain.LoginID) (domain.AgentLogin, error) {
	s.mu.Lock()
	sign, err := s.signInOf(member, id)
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
	if err := s.cfg.Homes.PrepareLoginMountpoint(member, paths[0], false); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	if err := s.cfg.Homes.PrepareLoginMountpoint(member, memberhome.AnthropicProfileDir, true); err != nil {
		return nil, fmt.Errorf("scheduler: %w", err)
	}
	mounts := []runtime.Mount{
		{HostPath: filepath.Join(dir, memberhome.LoginCredentials), ContainerPath: path.Join(home, paths[0])},
		{HostPath: filepath.Join(dir, memberhome.LoginAnthropic), ContainerPath: path.Join(home, memberhome.AnthropicProfileDir)},
	}
	configDir := filepath.Join(s.accountConfigRoot(), string(run.ID))
	if purpose == EnvironmentPurposeLogin {
		configDir = dir
		backups := path.Join(profile.AccountConfig.Dir, memberhome.LoginBackups)
		if err := os.Mkdir(filepath.Join(dir, memberhome.LoginBackups), 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		if err := s.cfg.Homes.PrepareLoginMountpoint(member, backups, true); err != nil {
			return nil, fmt.Errorf("scheduler: %w", err)
		}
		mounts = append(mounts, runtime.Mount{HostPath: filepath.Join(dir, memberhome.LoginBackups), ContainerPath: path.Join(home, backups)})
	}
	config, err := s.accountConfigMounts(configDir, member, profile, home)
	if err != nil {
		return nil, err
	}
	env[profile.AccountConfig.Env] = path.Join(home, profile.AccountConfig.Dir)
	env["ANTHROPIC_CONFIG_DIR"] = path.Join(home, memberhome.AnthropicProfileDir)
	return append(mounts, config...), nil
}

// ReconcileLogins makes the login directories match the rows at start: a
// directory a merge gave another member moves to them, and one with no row,
// a sign-in the server stopped during, goes with its container.
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
		_, signing := s.loginSignIns[l.ID]
		s.mu.Unlock()
		if signing {
			continue
		}
		owner, err := s.cfg.Store.AgentLoginOwner(ctx, l.Agent, l.ID)
		switch {
		case errors.Is(err, store.ErrNotFound):
			errs = append(errs, s.removeByCreationKey(ctx, loginSignInCreationKey(l.ID)), s.cfg.Logins.Remove(l.Member, l.Agent, l.ID))
		case err != nil:
			errs = append(errs, err)
		case owner != l.Member:
			errs = append(errs, s.cfg.Logins.Move(l.Member, owner, l.Agent, l.ID))
		}
	}
	return errors.Join(errs...)
}
