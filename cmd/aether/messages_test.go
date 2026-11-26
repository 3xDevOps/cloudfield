package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/protocol"
)

func TestMessagesPrintsAgentMailOldestFirstWithoutControlCharacters(t *testing.T) {
	const asked, replied, reported = "2026-11-26T10:00:00Z", "2026-11-26T10:00:05Z", "2026-11-26T10:01:00Z"
	delivered, acked := "2026-11-26T10:00:01Z", "2026-11-26T10:00:02Z"
	list := &fakeReply{result: protocol.CoordMessagesListResult{Messages: []protocol.RunMessage{
		{ID: "msg-3", FromRunID: "run-child", ToRunID: "run-parent", Kind: protocol.CoordMessageKindReport, CorrelationID: "rep-1", Body: "tests pass", CreatedAt: reported, Outcome: "success", Summary: "tests pass", NextAction: "review\x1b[2J the branch"},
		{ID: "msg-2", FromRunID: "run-parent", ToRunID: "run-child", Kind: "reply", CorrelationID: "msg-1", Body: "use main", CreatedAt: replied, DeliveredAt: &delivered},
		{ID: "msg-1", FromRunID: "run-child", ToRunID: "run-parent", Kind: "question", CorrelationID: "msg-1", Body: "which branch?\n\x1b]0;spoof\x07second line\r\n", CreatedAt: asked, DeliveredAt: &delivered, AckedAt: &acked},
	}}}
	c := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodRunGet:            {result: protocol.RunResult{Run: protocol.Run{ID: "run-child", WorkspaceID: "ws-1"}}},
		protocol.MethodCoordMessagesList: list,
	})
	var b bytes.Buffer
	if err := printMessages(c, &b, "", protocol.CoordMessagesListParams{RunID: "run-child", CorrelationID: "msg-1", Before: "cursor-1", Limit: 3}, false); err != nil {
		t.Fatal(err)
	}
	var sent protocol.CoordMessagesListParams
	if err := json.Unmarshal(list.params(), &sent); err != nil {
		t.Fatal(err)
	}
	if want := (protocol.CoordMessagesListParams{WorkspaceID: "ws-1", RunID: "run-child", CorrelationID: "msg-1", Before: "cursor-1", Limit: 3}); sent != want {
		t.Errorf("coord.messages.list params = %+v, want the session's workspace and every filter: %+v", sent, want)
	}
	want := shortTime(asked) + "  run-child -> run-parent  question  acknowledged  thread msg-1\n" +
		"  which branch?\n" +
		"   ]0;spoof second line \n" +
		shortTime(replied) + "  run-parent -> run-child  reply  delivered  thread msg-1\n" +
		"  use main\n" +
		shortTime(reported) + "  run-child -> run-parent  report  sent  thread rep-1\n" +
		"  tests pass\n" +
		"  outcome: success\n" +
		"  next action: review [2J the branch\n"
	if got := b.String(); got != want {
		t.Errorf("output:\n%q\nwant:\n%q", got, want)
	}
	if strings.ContainsAny(b.String(), "\x1b\x07\r") {
		t.Errorf("control bytes reached the terminal output: %q", b.String())
	}
}
