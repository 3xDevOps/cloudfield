package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/control"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
)

func init() {
	registerGuarded(protocol.MethodRunInputRespond, permissions.Steer, runTarget, (*Server).runInputRespond)
}

type controlHeldError struct{ holder domain.MemberID }

func (e *controlHeldError) Error() string { return "session control is held by " + string(e.holder) }

// runInputRespond is run.input.answer for a caller with no ACP stream.
func (s *Server) runInputRespond(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunInputRespondParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.RequestID == "" || p.OptionID == "" {
		return nil, invalidParams("request_id and option_id are required")
	}
	if strings.HasPrefix(p.RequestID, domain.SudoRequestPrefix) {
		if perr = s.decideSudo(ctx, member, domain.RunID(p.RunID), p.RequestID, p.OptionID); perr != nil {
			return nil, perr
		}
		return protocol.RunInputRespondResult{Status: protocol.RunInputRespondAnswered}, nil
	}
	run, perr := s.acpRun(ctx, p.RunID)
	if perr != nil {
		return nil, perr
	}
	if perr = s.connectorAnswerRefusal(ctx, run, p.RequestID); perr != nil {
		return nil, perr
	}
	if err := s.cfg.Runs.SettleSleep(ctx, run); err != nil {
		return nil, rpcError(err)
	}
	status := protocol.RunInputRespondAnswered
	perr = s.admitWithoutLease(ctx, member, run, protocol.MethodRunInputRespond, "the request is theirs to answer", func() error {
		err := s.cfg.Runs.ACPAnswer(run, p.RequestID, p.OptionID, p.Values)
		if errors.Is(err, acphost.ErrAlreadyAnswered) {
			status, err = protocol.RunInputRespondAlreadyAnswered, nil
		}
		return err
	})
	if perr != nil {
		return nil, perr
	}
	return protocol.RunInputRespondResult{Status: status}, nil
}

// admitWithoutLease runs act for a caller with no ACP stream, with Steer
// rechecked under the run's control admission lock, so no lease can be
// taken between the check and act: with nobody in control, or the caller in
// control from another session, it acts; while another member holds
// control, even in a reconnect window, it refuses rather than displacing
// them. theirs says what method would have taken from that member.
func (s *Server) admitWithoutLease(ctx context.Context, member domain.MemberID, run domain.RunID, method, theirs string, act func() error) *protocol.Error {
	admitted := func() error {
		if err := checkSteer(ctx, s.cfg.Store, member, run); err != nil {
			return err
		}
		return act()
	}
	var err error
	if s.cfg.Control == nil {
		err = admitted()
	} else {
		err = s.cfg.Control.AdmitSnapshot(string(run), func(holder control.Snapshot, held bool) error {
			if held && holder.MemberID != member {
				return &controlHeldError{holder: holder.MemberID}
			}
			return admitted()
		})
	}
	var heldErr *controlHeldError
	if !errors.As(err, &heldErr) {
		return acpError(err)
	}
	name := string(heldErr.holder)
	if holder, getErr := s.cfg.Store.GetMember(ctx, heldErr.holder); getErr == nil {
		name = holder.DisplayName
	}
	data, _ := json.Marshal(map[string]string{"reason": protocol.ErrorReasonControlHeld, "member_id": string(heldErr.holder)})
	return &protocol.Error{Code: protocol.CodeConflict, Data: data, Message: fmt.Sprintf(
		"%s: %s controls session %s, so %s; ask them, or take control in the dashboard", method, name, run, theirs)}
}

// decideSudo answers a Default run's sudo request whoever holds control.
// Allowing it needs the run's owner or an admin: root in the container acts
// on the owner's home and credentials.
func (s *Server) decideSudo(ctx context.Context, member domain.MemberID, run domain.RunID, request, option string) *protocol.Error {
	if perr := s.connectorAnswerRefusal(ctx, run, request); perr != nil {
		return perr
	}
	if option != domain.SudoAllow && option != domain.SudoDeny {
		return invalidParams(fmt.Sprintf("a sudo request's option is %s or %s, not %q", domain.SudoAllow, domain.SudoDeny, option))
	}
	allow := option == domain.SudoAllow
	if allow {
		// A handoff or a demotion takes this lock, so the check below still
		// holds when the decision lands.
		s.authorizationMu.Lock()
		defer s.authorizationMu.Unlock()
		actor, err := resolveActor(ctx, s.cfg.Store, member)
		if err != nil {
			return rpcError(err)
		}
		target, err := resolveRunTarget(ctx, s.cfg.Store, run)
		if err != nil {
			return rpcError(err)
		}
		if err = permissions.Check(permissions.Sudo, actor, target); err != nil {
			return &protocol.Error{Code: protocol.CodeDenied, Message: protocol.MethodRunInputRespond + ": " + err.Error()}
		}
	}
	err := s.cfg.Runs.DecideSudo(ctx, run, request, allow, member)
	if errors.Is(err, scheduler.ErrSudoNotPending) {
		return &protocol.Error{Code: protocol.CodeNotFound, Message: fmt.Sprintf("%s: session %s: %v", protocol.MethodRunInputRespond, run, err)}
	}
	if err != nil {
		return rpcError(err)
	}
	return nil
}
