package main

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// A launch the server keeps for want of room is asked after until its
// session exists, and the refusal is said once each time it changes.
func TestLaunchWaitsForRoomAndSaysWhy(t *testing.T) {
	answers := []protocol.RunLaunchResult{
		{Waiting: &protocol.LaunchWaiting{ID: "run-w", ExpiresAt: "2026-11-29T00:00:00Z", Refusal: "scheduler: insufficient host memory headroom: 1 available bytes"}},
		{Waiting: &protocol.LaunchWaiting{ID: "run-w", ExpiresAt: "2026-11-29T00:00:00Z", Refusal: "scheduler: insufficient host memory headroom: 1 available bytes"}},
		{Waiting: &protocol.LaunchWaiting{ID: "run-w", ExpiresAt: "2026-11-29T00:00:00Z", Refusal: "scheduler: insufficient host memory headroom: 2 available bytes"}},
		{Run: protocol.Run{ID: "run-w", Status: "running"}, Replayed: true},
	}
	calls := 0
	launch := &fakeReply{byParams: func(raw json.RawMessage) any {
		var p protocol.RunLaunchParams
		if err := json.Unmarshal(raw, &p); err != nil || !p.Wait || p.IdempotencyKey != "k-w" {
			t.Errorf("run.launch params = %s, want the same key and wait each time", raw)
		}
		calls++
		return answers[calls-1]
	}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunLaunch: launch})
	var said strings.Builder
	res, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-w", Wait: true}, &said, time.Millisecond)
	if err != nil || res.Run.ID != "run-w" || calls != 4 {
		t.Fatalf("launchWaiting = %+v, %v after %d calls; want the session after four", res, err, calls)
	}
	if got := strings.Count(said.String(), "waiting for room on the server"); got != 2 ||
		!strings.Contains(said.String(), "1 available bytes") || !strings.Contains(said.String(), "2 available bytes") || !strings.Contains(said.String(), "until 2026-11-29T00:00:00Z") {
		t.Fatalf("said %d times, want once for each refusal:\n%s", got, said.String())
	}
}

// An interrupt while the launch waits cancels it on the server, and the
// command ends as one interrupted with nothing started.
func TestInterruptCancelsAWaitingLaunch(t *testing.T) {
	launched := make(chan struct{}, 8)
	launch := &fakeReply{byParams: func(json.RawMessage) any {
		launched <- struct{}{}
		return protocol.RunLaunchResult{Waiting: &protocol.LaunchWaiting{ID: "run-w", Refusal: "no room"}}
	}}
	cancelled := &fakeReply{result: protocol.LaunchWaiting{ID: "run-w", State: "cancelled"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunLaunch: launch, protocol.MethodLaunchWaitingCancel: cancelled})
	go func() {
		<-launched
		// launchWaiting is in its wait between two asks by now or soon.
		time.Sleep(50 * time.Millisecond)
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	var said strings.Builder
	_, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", Wait: true}, &said, time.Hour)
	if !errors.Is(err, errWaitCancelled) || !strings.Contains(err.Error(), "run-w") || !strings.Contains(err.Error(), "nothing started") {
		t.Fatalf("an interrupted wait = %v, want the launch cancelled", err)
	}
	if !strings.Contains(string(cancelled.params()), `"run-w"`) {
		t.Fatalf("launch.waiting.cancel params = %s, want the waiting launch", cancelled.params())
	}
	var status *exitStatusError
	if exit := launchError(err, protocol.RunLaunchParams{}, false); !errors.As(exit, &status) || status.code != 130 {
		t.Fatalf("exit of a cancelled wait = %v, want status 130", exit)
	}
}
