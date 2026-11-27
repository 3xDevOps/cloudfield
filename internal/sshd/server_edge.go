package sshd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	edgeclient "github.com/3xDevOps/cloudfield/internal/edge/client"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/serversetup"
)

func init() {
	registerMethod(protocol.MethodServerEdge, (*Server).serverEdge)
	registerMethod(protocol.MethodServerEdgeClaimCode, (*Server).serverEdgeClaimCode)
}

func (s *Server) serverEdge(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	out := protocol.ServerEdgeResult{ServerID: edgeproto.ServerID(s.hostKey.PublicKey()), AccessPolicy: string(s.cfg.EdgeAccess)}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return out, nil
	}
	owner, err := edge.Owner()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: protocol.MethodServerEdge + ": " + err.Error()}
	}
	notices, err := edge.Notices()
	var msg string
	if err != nil {
		msg = err.Error()
	}
	if last := s.noticesErr.Swap(&msg); err != nil && (last == nil || *last != msg) {
		slog.Warn("sshd: server.edge shows no edge notice: the stored notices are unreadable", "error", err)
	}
	out.EdgeURL, out.Connected, out.Claimed = edge.Origin(), edge.Connected(), edge.EdgeState() == edgeproto.StateClaimed
	if owner != nil {
		out.Owner = &protocol.EdgeAccount{Provider: owner.Provider, Login: owner.Login}
	}
	if n, ok := notices.For(out.EdgeURL); ok {
		out.Notice = &protocol.EdgeNotice{State: n.State, Replacement: n.Replacement, Date: n.Date, FirstUse: notices.FirstUse}
		admin := s.superAdmin(ctx, member)
		if s.cfg.EdgeURLFlag {
			out.Notice.EdgeURLFlag = admin
		} else if moved := s.configuredEdgeURL(out.EdgeURL); moved != "" {
			out.Notice.RestartPending = true
			if admin {
				out.Notice.MovedTo = moved
			}
		}
	}
	return out, nil
}

// configuredEdgeURL is the canonical edge-url the options file names when
// it is another origin than running, the one the server runs at; empty
// when the file names running, none, or one that is not an origin, or
// cannot be read, which is logged once per error.
func (s *Server) configuredEdgeURL(running string) string {
	if s.cfg.ConfigPath == "" {
		return ""
	}
	values, err := serversetup.Load(s.cfg.ConfigPath)
	var msg string
	if err != nil {
		msg = err.Error()
	}
	if last := s.configErr.Swap(&msg); err != nil && (last == nil || *last != msg) {
		slog.Warn("sshd: server.edge cannot tell whether edge-url changed since the start", "error", err)
	}
	if err != nil || values["edge-url"] == "" {
		return ""
	}
	configured, err := edgeclient.Canonical(values["edge-url"])
	if err != nil {
		return ""
	}
	if now, err := edgeclient.Canonical(running); err == nil && now == configured {
		return ""
	}
	return configured
}

// serverEdgeClaimCode issues the calling super admin a claim code bound to
// their own member, so one who joined over an SSH key or a tailnet can
// connect their GitHub account and give the server an owner. The claim
// creates no member and raises no role.
func (s *Server) serverEdgeClaimCode(ctx context.Context, member domain.MemberID, _ json.RawMessage) (any, *protocol.Error) {
	const method = protocol.MethodServerEdgeClaimCode
	if perr := s.requireSuperAdmin(ctx, member, method); perr != nil {
		return nil, perr
	}
	if perr := s.requireApprovedCaller(ctx, method, true); perr != nil {
		return nil, perr
	}
	edge := s.currentEdgeOwner()
	if edge == nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": this server is not enrolled with an edge (edge-url is empty)"}
	}
	owner, err := edge.Owner()
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	if owner != nil {
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: this server is already owned at its edge by %s %s; aether member transfer moves ownership", method, owner.Provider, owner.Login)}
	}
	code, expires, err := edge.IssueClaimCode(member)
	if err != nil {
		return nil, &protocol.Error{Code: protocol.CodeUnavailable, Message: method + ": " + err.Error()}
	}
	slog.Info("sshd: a super admin issued a claim code for themself", "member", member)
	return protocol.ServerEdgeClaimCodeResult{Code: code, ExpiresAt: expires.UTC().Format(time.RFC3339)}, nil
}

// requireEdgeClaimed refuses method while the edge last reported this
// server as unclaimed: such an edge relays nothing for it and keeps no
// directory, so an invitation or a link could never be used. A claimed
// server that lost its owner keeps relaying for its members and passes.
func (s *Server) requireEdgeClaimed(method string) *protocol.Error {
	edge := s.currentEdgeOwner()
	if edge == nil || edge.EdgeState() != edgeproto.StateUnclaimed {
		return nil
	}
	return &protocol.Error{Code: protocol.CodeInvalidState, Message: method + ": the edge has not recorded this server yet, " +
		"so it relays nothing for it and keeps no directory; connect your GitHub account first: " +
		"Settings › Remote access › Connect my GitHub account in the dashboard, or aether edge connect"}
}
