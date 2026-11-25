package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/permissions"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// LinearService is the seam for members' Linear connections. Satisfied by
// *linear.Service. Every method acts on the caller's own key.
type LinearService interface {
	Status(ctx context.Context, member domain.MemberID) (protocol.LinearStatusResult, error)
	Connect(ctx context.Context, member domain.MemberID, p protocol.LinearConnectParams) (protocol.LinearStatusResult, error)
	Disconnect(member domain.MemberID) error
	Teams(ctx context.Context, member domain.MemberID) (protocol.LinearTeamsResult, error)
	Issues(ctx context.Context, member domain.MemberID, p protocol.LinearIssuesParams) (protocol.LinearIssuesResult, error)
	Issue(ctx context.Context, member domain.MemberID, p protocol.LinearIssueParams) (protocol.LinearIssue, error)
}

func init() {
	registerGuarded(protocol.MethodLinearStatus, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, _ struct{}) (any, error) {
		return svc.Status(ctx, m)
	}))
	registerGuarded(protocol.MethodLinearConnect, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearConnectParams) (any, error) {
		return svc.Connect(ctx, m, p)
	}))
	registerGuarded(protocol.MethodLinearDisconnect, permissions.View, nil, linearMethod(func(svc LinearService, _ context.Context, m domain.MemberID, _ struct{}) (any, error) {
		return struct{}{}, svc.Disconnect(m)
	}))
	registerGuarded(protocol.MethodLinearTeams, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, _ struct{}) (any, error) {
		return svc.Teams(ctx, m)
	}))
	registerGuarded(protocol.MethodLinearIssues, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearIssuesParams) (any, error) {
		return svc.Issues(ctx, m, p)
	}))
	registerGuarded(protocol.MethodLinearIssue, permissions.View, nil, linearMethod(func(svc LinearService, ctx context.Context, m domain.MemberID, p protocol.LinearIssueParams) (any, error) {
		return svc.Issue(ctx, m, p)
	}))
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
		if err != nil {
			return nil, rpcError(err)
		}
		return out, nil
	}
}
