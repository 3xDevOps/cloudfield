package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/linear/api"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// LinearService is the seam for members' Linear connections. Satisfied by
// *api.Service. Every method acts on the caller's own key.
type LinearService interface {
	Status(ctx context.Context, member domain.MemberID) (api.Status, error)
	Connect(ctx context.Context, member domain.MemberID, key string) (api.Status, error)
	Disconnect(member domain.MemberID) error
	Teams(ctx context.Context, member domain.MemberID) ([]api.Team, error)
	Issues(ctx context.Context, member domain.MemberID, f api.IssuesFilter) ([]api.Issue, error)
	Issue(ctx context.Context, member domain.MemberID, identifier string) (api.Issue, error)
}

func init() {
	registerGuarded(protocol.MethodLinearStatus, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, _ struct{}) (any, error) {
		status, err := svc.Status(ctx, m)
		return protocolLinearStatus(status), err
	}))
	connect := linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearConnectParams) (any, error) {
		key := strings.TrimSpace(p.APIKey)
		if key == "" {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "linear.connect: api_key is required"}
		}
		status, err := svc.Connect(ctx, m, key)
		return protocolLinearStatus(status), err
	})
	registerGuarded(protocol.MethodLinearConnect, permissions.View, nil, func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		out, perr := connect(s, ctx, member, raw)
		if perr != nil {
			return out, perr
		}
		// member.remove can finish while Linear validates the key, after
		// its own key deletion. The key is saved by now, so this check
		// sees the removal or the removal deletes the key.
		if _, err := s.cfg.Store.GetMember(ctx, member); err != nil {
			if delErr := s.disconnectLinear(member); delErr != nil {
				err = errors.Join(err, delErr)
			}
			if errors.Is(err, store.ErrNotFound) {
				return nil, rpcError(fmt.Errorf("linear.connect: member no longer exists: %w", err))
			}
			return nil, rpcError(fmt.Errorf("linear.connect: could not confirm the member still exists: %w", err))
		}
		return out, nil
	})
	registerGuarded(protocol.MethodLinearDisconnect, permissions.View, nil, linearMethod(func(svc LinearService, _ context.Context, m domain.MemberID, _ struct{}) (any, error) {
		return struct{}{}, svc.Disconnect(m)
	}))
	registerGuarded(protocol.MethodLinearTeams, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, _ struct{}) (any, error) {
		teams, err := svc.Teams(ctx, m)
		out := protocol.LinearTeamsResult{Teams: make([]protocol.LinearTeam, 0, len(teams))}
		for _, t := range teams {
			out.Teams = append(out.Teams, protocol.LinearTeam(t))
		}
		return out, err
	}))
	registerGuarded(protocol.MethodLinearIssues, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearIssuesParams) (any, error) {
		issues, err := svc.Issues(ctx, m, api.IssuesFilter{Team: strings.TrimSpace(p.Team), Query: strings.TrimSpace(p.Query)})
		out := protocol.LinearIssuesResult{Issues: make([]protocol.LinearIssue, 0, len(issues))}
		for _, i := range issues {
			out.Issues = append(out.Issues, protocolLinearIssue(i))
		}
		return out, err
	}))
	registerGuarded(protocol.MethodLinearIssue, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearIssueParams) (any, error) {
		identifier := strings.TrimSpace(p.Identifier)
		if identifier == "" {
			return nil, &protocol.Error{Code: protocol.CodeInvalidParams, Message: "linear.issue: identifier is required"}
		}
		issue, err := svc.Issue(ctx, m, identifier)
		return protocolLinearIssue(issue), err
	}))
}

func protocolLinearStatus(s api.Status) protocol.LinearStatusResult {
	return protocol.LinearStatusResult(s)
}

func protocolLinearIssue(i api.Issue) protocol.LinearIssue {
	out := protocol.LinearIssue{
		Identifier: i.Identifier, Title: i.Title, URL: i.URL,
		State:     protocol.LinearState(i.State),
		UpdatedAt: i.UpdatedAt,
	}
	if i.Team != nil {
		team := protocol.LinearTeam(*i.Team)
		out.Team = &team
	}
	if i.Assignee != nil {
		out.Assignee = &protocol.LinearAssignee{Name: i.Assignee.Name}
	}
	return out
}

func linearMethod[P any](call func(LinearService, context.Context, domain.MemberID, P) (any, error)) methodHandler {
	return func(s *Server, ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
		svc := s.cfg.Services.Linear
		if svc == nil {
			return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: "linear service not configured"}
		}
		p, perr := decodeParams[P](raw)
		if perr != nil {
			return nil, perr
		}
		out, err := call(svc, ctx, member, p)
		if errors.Is(err, api.ErrNotConnected) {
			return nil, &protocol.Error{Code: protocol.CodeInvalidState,
				Message: "Linear is not connected: save a personal API key in Settings → Linear"}
		}
		if err != nil {
			return nil, rpcError(err)
		}
		return out, nil
	}
}

func (s *Server) disconnectLinear(member domain.MemberID) error {
	if s.cfg.Services.Linear == nil {
		return nil
	}
	return s.cfg.Services.Linear.Disconnect(member)
}
