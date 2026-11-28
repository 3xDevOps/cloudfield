package server

import (
	"errors"
	"slices"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/coord"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// A spare session is hidden from every member, yet its own agent starts
// like any session's: the in-VM bridge reads coord.status once, at the
// agent's start, and a launch that claims the spare keeps that agent. So
// the spare's own calls resolve it, with the subsession tools a claimed
// session needs, while no peer and no member's read finds it.
func TestASpareSessionResolvesItselfAndNobodyElseDoes(t *testing.T) {
	t.Parallel()
	s, _, member, ws := newWorkspaceDeletionServer(t)
	ctx := t.Context()
	peer := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Task: "work", Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunRunning}
	spare := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunRunning, Spare: true}
	for _, r := range []*domain.Run{peer, spare} {
		if err := s.db.CreateRun(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	var coordination *coord.Service
	for _, n := range s.services {
		if svc, ok := n.svc.(*coord.Service); ok {
			coordination = svc
		}
	}
	if coordination == nil {
		t.Fatal("the server built no coordination service")
	}

	own, perr := coordination.Status(ctx, spare.ID)
	if perr != nil {
		t.Fatalf("coord.status of a spare session: %v", perr)
	}
	for _, method := range []string{protocol.MethodChildStart, protocol.MethodChildList} {
		if !slices.Contains(own.Capabilities, method) {
			t.Errorf("a spare's coord.status lists %v, want %s for the session that claims it", own.Capabilities, method)
		}
	}
	seen, perr := coordination.Status(ctx, peer.ID)
	if perr != nil {
		t.Fatalf("coord.status of the member's session: %v", perr)
	}
	if slices.ContainsFunc(seen.Peers, func(p protocol.CoordPeer) bool { return p.RunID == string(spare.ID) }) {
		t.Fatalf("a member's session sees the spare among its peers: %+v", seen.Peers)
	}
	if _, err := s.db.GetRun(ctx, spare.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a member's read of the spare = %v, want not found", err)
	}
}

// Removing a spare goes through the session deletion every session takes,
// with the evidence service the server wires: it must not need to see the
// spare's row.
func TestASpareSessionIsDeletedWithTheEvidenceServiceWired(t *testing.T) {
	t.Parallel()
	s, _, member, ws := newWorkspaceDeletionServer(t)
	ctx := t.Context()
	spare := &domain.Run{WorkspaceID: ws.ID, MemberID: member.ID, Harness: "claude", Mode: domain.LaunchACP, Status: domain.RunFailed, Spare: true}
	if err := s.db.CreateRun(ctx, spare); err != nil {
		t.Fatal(err)
	}
	if err := s.sched.DeleteRun(ctx, spare.ID, ""); err != nil {
		t.Fatalf("delete a spare session: %v", err)
	}
	if spares, err := s.db.ListSpareRuns(ctx); err != nil || len(spares) != 0 {
		t.Fatalf("spare sessions after the deletion = %v, %v", spares, err)
	}
}
