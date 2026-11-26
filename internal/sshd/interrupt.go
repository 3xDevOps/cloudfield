package sshd

import (
	"context"
	"encoding/json"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/permissions"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func init() {
	registerGuarded(protocol.MethodRunInterrupt, permissions.Steer, runTarget, (*Server).runInterrupt)
}

func (s *Server) runInterrupt(ctx context.Context, member domain.MemberID, raw json.RawMessage) (any, *protocol.Error) {
	p, perr := decodeParams[protocol.RunIDParams](raw)
	if perr != nil {
		return nil, perr
	}
	run, err := s.cfg.Store.GetRun(ctx, domain.RunID(p.RunID))
	if err != nil {
		return nil, rpcError(err)
	}
	// Outside control admission, which a sleep's surface revocation waits for.
	if err = s.cfg.Runs.SettleSleep(ctx, run.ID); err != nil {
		return nil, rpcError(err)
	}
	return struct{}{}, s.admitWithoutLease(ctx, member, run.ID, protocol.MethodRunInterrupt, "the turn is theirs to interrupt", func() error {
		return s.cfg.Runs.Interrupt(ctx, run)
	})
}
