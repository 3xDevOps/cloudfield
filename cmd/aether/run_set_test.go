package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestSessionSetSaysWhatChangedOrWhyNot(t *testing.T) {
	reply := &fakeReply{result: protocol.RunOptionSetResult{Model: "opus", Effort: "high"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunOptionSet: reply})
	var out bytes.Buffer
	if err := setSessionOptions(c, &out, "r1", "opus-plan", "high"); err != nil || out.String() != "session r1 set to model opus, effort high\n" {
		t.Fatalf("a model and an effort printed %q (%v), want the values the agent reports", out.String(), err)
	}
	if got := string(reply.params()); got != `{"run_id":"r1","model":"opus-plan","effort":"high"}` {
		t.Fatalf("run.option.set params = %s", got)
	}
	reply.answer(protocol.RunOptionSetResult{Model: "opus", Effort: "low"})
	out.Reset()
	if err := setSessionOptions(c, &out, "r1", "", "low"); err != nil || out.String() != "session r1 set to effort low\n" {
		t.Fatalf("an effort alone printed %q (%v)", out.String(), err)
	}
	if got := string(reply.params()); got != `{"run_id":"r1","effort":"low"}` {
		t.Fatalf("run.option.set params = %s, want the model left out", got)
	}

	reason := "run.option.set: session r2 is a Standard session, where the model and effort change only inside the agent's own terminal: " +
		"run cloudfield attach r2 and change them there, or run cloudfield session switch r2 --mode enhanced first"
	c = fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunOptionSet: {err: &protocol.Error{Code: protocol.CodeInvalidState, Message: reason}}})
	out.Reset()
	if err := setSessionOptions(c, &out, "r2", "opus", ""); err == nil || !strings.Contains(err.Error(), reason) || out.Len() != 0 {
		t.Fatalf("a Standard session = %v, printed %q; want the server's reason and nothing printed", err, out.String())
	}
}
