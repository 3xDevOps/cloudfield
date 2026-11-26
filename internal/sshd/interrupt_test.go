package sshd

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
)

func TestRunInterruptNeedsNoLeaseAndYieldsToAnotherMembersControl(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	_, vic := addMember(t, e, "Vic", domain.RoleViewer, false)
	interrupt := func(member domain.MemberID, id domain.RunID) *protocol.Error {
		t.Helper()
		_, perr := callJSON(t, e, member, protocol.MethodRunInterrupt, protocol.RunIDParams{RunID: string(id)})
		return perr
	}

	if perr := interrupt(e.member.ID, run.ID); perr != nil {
		t.Fatalf("interrupt with nobody in control = %v", perr)
	}
	if perr := interrupt(vic.ID, run.ID); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("interrupt from a viewer = %v, want CodeDenied", perr)
	}

	writer, _, err := e.srv.Local(bob.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "bob-tab"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	perr := interrupt(e.member.ID, run.ID)
	var data struct {
		Reason   string `json:"reason"`
		MemberID string `json:"member_id"`
	}
	want := fmt.Sprintf("run.interrupt: Bob controls session %s, so the turn is theirs to interrupt; ask them, or take control in the dashboard", run.ID)
	if perr == nil || perr.Code != protocol.CodeConflict || perr.Message != want || json.Unmarshal(perr.Data, &data) != nil ||
		data.Reason != protocol.ErrorReasonControlHeld || data.MemberID != string(bob.ID) {
		t.Fatalf("interrupt while Bob controls the session = %+v", perr)
	}
	if perr := interrupt(bob.ID, run.ID); perr != nil {
		t.Fatalf("interrupt from the controller's member = %v", perr)
	}
	admitted := 0
	for _, call := range e.runs.Calls() {
		if call == "interrupt:"+string(run.ID) {
			admitted++
		}
	}
	if admitted != 2 {
		t.Fatalf("calls = %v, want only the two admitted interrupts", e.runs.Calls())
	}

	e.runs.setErr(fmt.Errorf("%w: cloudfield attach %s", scheduler.ErrNoInterrupt, e.run.ID))
	if perr := interrupt(e.member.ID, e.run.ID); perr == nil || perr.Code != protocol.CodeInvalidState {
		t.Fatalf("interrupt of an agent that takes none = %+v, want CodeInvalidState", perr)
	}
}
