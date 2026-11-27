package sshd

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/scheduler"
)

func TestRunOptionSetNeedsNoLeaseAndYieldsToAnotherMembersControl(t *testing.T) {
	e, run := newACPEnv(t)
	e.runs.acpStream = scheduler.ACPStream{Items: make(chan acphost.Item)}
	_, bob := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	_, vic := addMember(t, e, "Vic", domain.RoleViewer, false)
	set := func(member domain.MemberID, id domain.RunID, model, effort string) *protocol.Error {
		t.Helper()
		_, perr := callJSON(t, e, member, protocol.MethodRunOptionSet, protocol.RunOptionSetParams{RunID: string(id), Model: model, Effort: effort})
		return perr
	}

	raw, perr := callJSON(t, e, e.member.ID, protocol.MethodRunOptionSet, protocol.RunOptionSetParams{RunID: string(run.ID), Model: "Opus", Effort: "high"})
	if perr != nil || string(raw) != `{"model":"opus","effort":"high"}` {
		t.Fatalf("model and effort with nobody in control = %s, %v; want the values the agent reports", raw, perr)
	}
	if perr = set(e.member.ID, run.ID, "", ""); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("neither a model nor an effort = %v, want CodeInvalidParams", perr)
	}
	if perr = set(vic.ID, run.ID, "opus", ""); perr == nil || perr.Code != protocol.CodeDenied {
		t.Fatalf("model from a viewer = %v, want CodeDenied", perr)
	}

	writer, _, err := e.srv.Local(bob.ID).ACP(t.Context(), protocol.ACPStreamRequest{RunID: string(run.ID), Write: true, ControlSessionID: "bob-tab"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writer.Close() }()
	perr = set(e.member.ID, run.ID, "sonnet", "")
	var data struct {
		Reason   string `json:"reason"`
		MemberID string `json:"member_id"`
	}
	want := fmt.Sprintf("run.option.set: Bob controls session %s, so its model and effort are theirs to change; ask them, or take control in the dashboard", run.ID)
	if perr == nil || perr.Code != protocol.CodeConflict || perr.Message != want || json.Unmarshal(perr.Data, &data) != nil ||
		data.Reason != protocol.ErrorReasonControlHeld || data.MemberID != string(bob.ID) {
		t.Fatalf("model while Bob controls the session = %+v", perr)
	}
	if perr = set(bob.ID, run.ID, "", "low"); perr != nil {
		t.Fatalf("effort from the controller's member = %v", perr)
	}
	admitted := slices.DeleteFunc(e.runs.Calls(), func(call string) bool { return !strings.HasPrefix(call, "acp.set_model_effort:") })
	if !slices.Equal(admitted, []string{
		fmt.Sprintf("acp.set_model_effort:%s:%s:Opus:high", run.ID, e.member.ID),
		fmt.Sprintf("acp.set_model_effort:%s:%s::low", run.ID, bob.ID),
	}) {
		t.Fatalf("changes that reached the agent = %v, want only the two admitted, each with its sender", admitted)
	}

	want = fmt.Sprintf("run.option.set: session %[1]s is a Standard session, where the model and effort change only inside the agent's own terminal: "+
		"run cloudfield attach %[1]s and change them there, or run cloudfield session switch %[1]s --mode enhanced first", e.run.ID)
	if perr = set(e.member.ID, e.run.ID, "opus", ""); perr == nil || perr.Code != protocol.CodeInvalidState || perr.Message != want {
		t.Fatalf("model of a Standard session = %+v, want CodeInvalidState and %q", perr, want)
	}
	background := &domain.Run{WorkspaceID: e.ws.ID, MemberID: e.member.ID, Task: "t", Harness: "claude", Mode: domain.LaunchHeadless, Status: domain.RunRunning}
	if err = e.store.CreateRun(t.Context(), background); err != nil {
		t.Fatal(err)
	}
	if perr = set(e.member.ID, background.ID, "opus", ""); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("model of a Background session that runs no ACP agent = %+v, want CodeInvalidParams", perr)
	}
	e.runs.setErr(fmt.Errorf("%w: effort", scheduler.ErrNoSuchOption))
	if perr = set(bob.ID, run.ID, "", "high"); perr == nil || perr.Code != protocol.CodeInvalidParams {
		t.Fatalf("effort of an agent that offers none = %+v, want CodeInvalidParams", perr)
	}
}
