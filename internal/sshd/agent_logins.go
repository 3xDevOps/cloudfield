package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/memberhome"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/ptyhost"
	"github.com/3xDevOps/cloudfield/internal/store"
)

func init() {
	registerMethod(protocol.MethodAgentLoginList, (*Server).agentLoginList)
	registerMethod(protocol.MethodAgentLoginAdd, (*Server).agentLoginAdd)
	registerMethod(protocol.MethodAgentLoginResult, (*Server).agentLoginResult)
	registerMethod(protocol.MethodAgentLoginRename, (*Server).agentLoginRename)
	registerMethod(protocol.MethodAgentLoginRemove, (*Server).agentLoginRemove)
	registerMethod(protocol.MethodAgentLoginOrder, (*Server).agentLoginOrder)
	registerMethod(protocol.MethodAgentLoginAutomatic, (*Server).agentLoginAutomatic)
	registerMethod(protocol.MethodAgentLoginShare, (*Server).agentLoginShare)
}

// LoginSignIns runs the sign-ins of new logins, each in a container of its
// own; the scheduler implements it beside RunController.
type LoginSignIns interface {
	BeginLoginSignIn(ctx context.Context, member domain.MemberID, agent, name string) (domain.LoginID, error)
	StartLoginSignIn(ctx context.Context, member domain.MemberID, id domain.LoginID, cols, rows uint) (ptyhost.SessionKey, error)
	LoginSignInResult(ctx context.Context, member domain.MemberID, agent string, id domain.LoginID) (domain.AgentLogin, error)
	// EndLoginSignIns ends member's running sign-ins before the member is
	// removed or merged into another.
	EndLoginSignIns(ctx context.Context, member domain.MemberID) error
	// ReconcileLogins moves the login directories a merge gave another
	// member to them and removes those with no row.
	ReconcileLogins(ctx context.Context, member domain.MemberID) error
	// LoginInUse lists the sessions on an added login, moving to it, or
	// queued to start on it, each with the member who owns it.
	LoginInUse(ctx context.Context, account domain.Account) (map[domain.RunID]domain.MemberID, error)
	// RevokeLogin moves teammates' running sessions off a login its owner
	// turned off for shares.
	RevokeLogin(login domain.Account)
}

var errNoAddedLogins = &protocol.Error{Code: protocol.CodeUnavailable, Message: "this server keeps no added logins"}

func (s *Server) loginSignIns() (LoginSignIns, *protocol.Error) {
	if s.cfg.LoginSignIns == nil || s.cfg.Logins == nil {
		return nil, errNoAddedLogins
	}
	return s.cfg.LoginSignIns, nil
}

// agentLoginCaller checks the caller and the agent, and returns the
// caller's logins of it.
func (s *Server) agentLoginCaller(ctx context.Context, member domain.MemberID, agent string) ([]domain.AgentLogin, *protocol.Error) {
	if agent != "claude" {
		return nil, invalidParams("agent must be claude: only Claude Code keeps added logins")
	}
	if err := s.checkMember(ctx, member); err != nil {
		return nil, rpcError(err)
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, member, agent)
	if err != nil {
		return nil, rpcError(err)
	}
	return logins, nil
}

// addedLogin finds the caller's added login id among logins.
func addedLogin(logins []domain.AgentLogin, id string) (domain.AgentLogin, *protocol.Error) {
	if domain.LoginID(id) == domain.DefaultLogin {
		return domain.AgentLogin{}, invalidParams("the default login is the one in your home; it is not renamed or removed here")
	}
	for _, l := range logins {
		if l.ID == domain.LoginID(id) {
			return l, nil
		}
	}
	return domain.AgentLogin{}, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("you have no login %q", id)}
}

func (s *Server) agentLoginList(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginParams](raw)
	if perr != nil {
		return nil, perr
	}
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	if len(logins) == 0 {
		logins = []domain.AgentLogin{{MemberID: member, Agent: p.Agent, ID: domain.DefaultLogin, Name: string(domain.DefaultLogin), Automatic: true, Shared: true}}
	}
	out := protocol.AgentLoginListResult{Agent: p.Agent, Logins: make([]protocol.AgentLogin, 0, len(logins))}
	for _, l := range logins {
		out.Logins = append(out.Logins, s.agentLoginWire(l))
	}
	return out, nil
}

// agentLoginWire describes the caller's own login l, with what Claude Code
// recorded at sign-in. Nothing of the login's credentials is read here.
func (s *Server) agentLoginWire(l domain.AgentLogin) protocol.AgentLogin {
	var (
		identity memberhome.LoginIdentity
		err      error
	)
	switch {
	case l.ID.Added() && s.cfg.Logins != nil:
		identity, err = s.cfg.Logins.Identity(l.MemberID, l.Agent, l.ID)
	case !l.ID.Added() && s.cfg.Homes != nil:
		identity, err = s.cfg.Homes.DefaultIdentity(l.MemberID, l.Agent)
	}
	if err != nil {
		slog.Warn("sshd: read a login's identity", "member", l.MemberID, "login", l.ID, "error", err)
	}
	return protocol.AgentLogin{
		ID: string(l.ID), Name: l.Name, Kind: identity.Kind, Plan: identity.Plan, Email: identity.Email,
		OrganizationID: identity.OrganizationID, OrganizationName: identity.OrganizationName,
		PerUse: identity.PerUse(), Automatic: l.Automatic, Shared: l.Shared, Position: l.Position,
	}
}

func (s *Server) agentLoginAdd(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginAddParams](raw)
	if perr != nil {
		return nil, perr
	}
	if _, perr = s.agentLoginCaller(ctx, member, p.Agent); perr != nil {
		return nil, perr
	}
	if err := domain.ValidateLoginName(p.Name); err != nil {
		return nil, invalidParams(err.Error())
	}
	signIns, perr := s.loginSignIns()
	if perr != nil {
		return nil, perr
	}
	if perr = s.mayLaunch(ctx, member, protocol.MethodAgentLoginAdd); perr != nil {
		return nil, perr
	}
	id, err := signIns.BeginLoginSignIn(ctx, member, p.Agent, p.Name)
	if err != nil {
		return nil, rpcError(err)
	}
	return protocol.AgentLoginAddResult{LoginID: string(id)}, nil
}

func (s *Server) agentLoginResult(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginParams](raw)
	if perr != nil {
		return nil, perr
	}
	if _, perr = s.agentLoginCaller(ctx, member, p.Agent); perr != nil {
		return nil, perr
	}
	signIns, perr := s.loginSignIns()
	if perr != nil {
		return nil, perr
	}
	login, err := signIns.LoginSignInResult(ctx, member, p.Agent, domain.LoginID(p.LoginID))
	if err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	return s.agentLoginWire(login), nil
}

func (s *Server) agentLoginRename(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginRenameParams](raw)
	if perr != nil {
		return nil, perr
	}
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	login, perr := addedLogin(logins, p.LoginID)
	if perr != nil {
		return nil, perr
	}
	if err := domain.ValidateLoginName(p.Name); err != nil {
		return nil, invalidParams(err.Error())
	}
	if err := s.cfg.Store.RenameAgentLogin(ctx, member, p.Agent, login.ID, p.Name); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("you already have a login named %q", p.Name)}
		}
		return nil, rpcError(err)
	}
	noteChange(ctx)
	return struct{}{}, nil
}

// agentLoginRemove deletes an added login's directory and then its row,
// under the launch authorization lock. It does not sign the login out at
// Anthropic.
func (s *Server) agentLoginRemove(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginParams](raw)
	if perr != nil {
		return nil, perr
	}
	if s.cfg.Logins == nil {
		return nil, errNoAddedLogins
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	login, perr := addedLogin(logins, p.LoginID)
	if perr != nil {
		return nil, perr
	}
	if signIns := s.cfg.LoginSignIns; signIns != nil {
		inUse, err := signIns.LoginInUse(ctx, domain.Account{Member: member, Login: login.ID})
		if err != nil {
			return nil, rpcError(err)
		}
		if len(inUse) > 0 {
			// The owner sees their own sessions; a teammate's are counted, not
			// named.
			var own []string
			teammates := 0
			for id, owner := range inUse {
				if owner == member {
					own = append(own, string(id))
				} else {
					teammates++
				}
			}
			slices.Sort(own)
			var parts []string
			if len(own) > 0 {
				parts = append(parts, "your sessions "+strings.Join(own, ", ")+" (end them)")
			}
			if teammates > 0 {
				parts = append(parts, fmt.Sprintf("%d of your teammates' sessions, which hold it until they end and their containers go (revoking the share you gave a teammate, with cloudfield account revoke <member-id>, moves their running sessions off it)", teammates))
			}
			return nil, &protocol.Error{Code: protocol.CodeConflict, Message: fmt.Sprintf("login %q is in use by %s", login.Name, strings.Join(parts, " and "))}
		}
	}
	// The directory goes first, wherever a merge left it, so a removal that
	// fails half way keeps the row and can be asked again.
	stored, err := s.cfg.Logins.List("")
	if err != nil {
		return nil, rpcError(err)
	}
	for _, l := range stored {
		if l.Agent == p.Agent && l.ID == login.ID {
			if err := s.cfg.Logins.Remove(l.Member, l.Agent, l.ID); err != nil {
				return nil, rpcError(fmt.Errorf("remove login %q: %w", login.Name, err))
			}
		}
	}
	if err := s.cfg.Store.DeleteAgentLogin(ctx, member, p.Agent, login.ID); err != nil {
		return nil, rpcError(err)
	}
	noteChange(ctx)
	return struct{}{}, nil
}

func (s *Server) agentLoginOrder(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginOrderParams](raw)
	if perr != nil {
		return nil, perr
	}
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	if len(logins) == 0 {
		return nil, invalidParams("you have only the default login; add a login first")
	}
	ids := make([]domain.LoginID, len(p.LoginIDs))
	for i, id := range p.LoginIDs {
		ids[i] = domain.LoginID(id)
	}
	if err := s.cfg.Store.OrderAgentLogins(ctx, member, p.Agent, ids); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, invalidParams("login_ids must name each of your logins once, default included")
		}
		return nil, rpcError(err)
	}
	noteChange(ctx)
	return struct{}{}, nil
}

func (s *Server) agentLoginAutomatic(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginAutomaticParams](raw)
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	if len(logins) == 0 {
		return nil, invalidParams("you have only the default login; add a login first")
	}
	if err := s.cfg.Store.SetAgentLoginAutomatic(ctx, member, p.Agent, domain.LoginID(p.LoginID), p.Automatic); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("you have no login %q", p.LoginID)}
		}
		return nil, rpcError(err)
	}
	noteChange(ctx)
	return struct{}{}, nil
}

// launchLogin resolves the login a launch by member on account, admitted
// already, starts from: the one its owner named, or the account's first login in its
// owner's order that member may use without naming it. Only a login's owner
// may name it. An account none of whose logins member may use is not shared
// with them; on their own account the launch is refused, naming each login
// and why it does not serve.
func (s *Server) launchLogin(ctx context.Context, workspace domain.WorkspaceID, member, account domain.MemberID, harness, named string, mode domain.LaunchMode) (domain.LoginID, *protocol.Error) {
	if account == "" {
		account = member
	}
	logins, err := s.cfg.Store.ListAgentLogins(ctx, account, harness)
	if err != nil {
		return "", rpcError(err)
	}
	if named != "" {
		if account != member {
			return "", &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunLaunch + ": only a login's owner names it; a launch on a teammate's account is served in their order"}
		}
		if domain.LoginID(named) == domain.DefaultLogin {
			return "", nil
		}
		login, perr := addedLogin(logins, named)
		if perr != nil {
			return "", perr
		}
		return login.ID, nil
	}
	login, err := s.cfg.Runs.StartLogin(ctx, workspace, member, account, harness, mode)
	if errors.Is(err, permissions.ErrDenied) {
		return "", &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunLaunch + ": " + err.Error()}
	}
	if err != nil {
		return "", rpcError(err)
	}
	return login, nil
}

// mayLaunch requires the permission a launch needs: a sign-in runs a
// container, as a session does.
func (s *Server) mayLaunch(ctx context.Context, member domain.MemberID, method string) *protocol.Error {
	actor, err := resolveActor(ctx, s.cfg.Store, member)
	if err != nil {
		return rpcError(err)
	}
	if err := permissions.Check(permissions.Launch, actor, permissions.Target{}); err != nil {
		return &protocol.Error{Code: protocol.CodeDenied, Message: method + ": " + err.Error()}
	}
	return nil
}

func (s *Server) agentLoginShare(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.AgentLoginShareParams](raw)
	if perr != nil {
		return nil, perr
	}
	s.authorizationMu.Lock()
	defer s.authorizationMu.Unlock()
	logins, perr := s.agentLoginCaller(ctx, member, p.Agent)
	if perr != nil {
		return nil, perr
	}
	if len(logins) == 0 {
		return nil, invalidParams("you have only the default login; add a login first")
	}
	if err := s.cfg.Store.SetAgentLoginShared(ctx, member, p.Agent, domain.LoginID(p.LoginID), p.Shared); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("you have no login %q", p.LoginID)}
		}
		return nil, rpcError(err)
	}
	noteChange(ctx)
	if !p.Shared && s.cfg.LoginSignIns != nil {
		login := domain.Account{Member: member}
		if id := domain.LoginID(p.LoginID); id.Added() {
			login.Login = id
		}
		s.cfg.LoginSignIns.RevokeLogin(login)
	}
	return struct{}{}, nil
}
