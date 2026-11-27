package sshd

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodRunOptionSet, permissions.Steer, runTarget, (*Server).runOptionSet)
}

// runOptionSet is run.acp.set_option for a caller with no ACP stream.
func (s *Server) runOptionSet(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunOptionSetParams](raw)
	if perr != nil {
		return nil, perr
	}
	if p.Model == "" && p.Effort == "" {
		return nil, invalidParams("model or effort is required")
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	switch {
	case run.Mode == domain.LaunchTUI:
		return nil, &protocol.Error{Code: protocol.CodeInvalidState, Message: fmt.Sprintf(
			"%s: session %[2]s is a Standard session, where the model and effort change only inside the agent's own terminal: "+
				"run cloudfield attach %[2]s and change them there, or run cloudfield session switch %[2]s --mode enhanced first",
			protocol.MethodRunOptionSet, run.ID)}
	case !run.ACP:
		return nil, invalidParams(fmt.Sprintf("session %s does not run its agent over ACP", run.ID))
	}
	// The change wakes the session, outside control admission.
	if err = s.cfg.Runs.WakeRun(ctx, run.ID, member); err != nil {
		return nil, rpcError(err)
	}
	var res protocol.RunOptionSetResult
	perr = s.admitWithoutLease(ctx, member, run.ID, protocol.MethodRunOptionSet, "its model and effort are theirs to change", func() error {
		optionCtx, cancel := context.WithTimeout(ctx, acpOptionTimeout)
		defer cancel()
		res.Model, res.Effort, err = s.cfg.Runs.ACPSetModelEffort(optionCtx, run, member, p.Model, p.Effort)
		return err
	})
	if perr != nil {
		return nil, perr
	}
	return res, nil
}
