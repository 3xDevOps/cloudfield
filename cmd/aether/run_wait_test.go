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
		{Waiting: &protocol.LaunchWaiting{ID: "run-w", ExpiresAt: "2026-11-29T00:00:00Z", Refusal: "scheduler: insufficient disk space: 3 available bytes"}},
		// Its session exists and is not provisioned yet: not the answer.
		{Run: protocol.Run{ID: "run-w", Status: "provisioning"}, Replayed: true},
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
	res, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", IdempotencyKey: "k-w", Wait: true}, &said, time.Millisecond, "aether session --idempotency-key k-w -- wait")
	if err != nil || res.Run.ID != "run-w" || res.Run.Status != "running" || res.Replayed || calls != 6 {
		t.Fatalf("launchWaiting = %+v, %v after %d calls; want the running session, as this command's own launch, after six", res, err, calls)
	}
	if got := strings.Count(said.String(), "waiting for room on the server"); got != 2 ||
		!strings.Contains(said.String(), "1 available bytes") || !strings.Contains(said.String(), "insufficient disk space: 3 available bytes") || !strings.Contains(said.String(), "launch run-w, until 2026-11-29T00:00:00Z") {
		t.Fatalf("said %d times, want once for each kind of refusal:\n%s", got, said.String())
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
	_, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", Wait: true}, &said, time.Hour, "aether session -- wait")
	var ended *errWaitEnded
	if !errors.As(err, &ended) || !strings.Contains(err.Error(), "run-w") || !strings.Contains(err.Error(), "nothing started") {
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

// A session that started after the wait and failed is an error, not a
// session line and exit 0.
func TestAWaitedLaunchThatFailsIsAnError(t *testing.T) {
	answers := []protocol.RunLaunchResult{
		{Waiting: &protocol.LaunchWaiting{ID: "run-w", Refusal: "no room"}},
		{Run: protocol.Run{ID: "run-w", Status: "failed", Reason: "image pull failed"}, Replayed: true},
	}
	calls := 0
	launch := &fakeReply{byParams: func(json.RawMessage) any { calls++; return answers[calls-1] }}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunLaunch: launch})
	var said strings.Builder
	_, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", Wait: true}, &said, time.Millisecond, "aether session -- wait")
	if err == nil || !strings.Contains(err.Error(), "run-w") || !strings.Contains(err.Error(), "image pull failed") {
		t.Fatalf("a waited launch that failed = %v, want an error naming the session and why", err)
	}
}

// When asking after a waiting launch fails, the command says the launch may
// still be waiting and how to follow or cancel it.
func TestALostWaitSaysHowToFollowTheLaunch(t *testing.T) {
	calls := 0
	launch := &fakeReply{byParams: func(json.RawMessage) any {
		calls++
		return protocol.RunLaunchResult{Waiting: &protocol.LaunchWaiting{ID: "run-w", Refusal: "no room"}}
	}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunLaunch: launch})
	go func() {
		for launch.params() == nil {
			time.Sleep(time.Millisecond)
		}
		launch.mu.Lock()
		launch.err = &protocol.Error{Code: protocol.CodeInternal, Message: "store: database is locked"}
		launch.mu.Unlock()
	}()
	var said strings.Builder
	_, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", Wait: true}, &said, 20*time.Millisecond, "aether session --idempotency-key k -- wait")
	if err == nil || !strings.Contains(err.Error(), "database is locked") || !strings.Contains(err.Error(), "the launch may still be waiting") || !strings.Contains(err.Error(), "aether session --idempotency-key k -- wait") {
		t.Fatalf("a failed look at a waiting launch = %v, want the error, that it may still wait, and the command to follow it", err)
	}
}

// An interrupt before the server's first answer still cancels a launch the
// server turns out to be keeping.
func TestInterruptBeforeTheAnswerCancelsAKeptLaunch(t *testing.T) {
	asked := make(chan struct{})
	launch := &fakeReply{byParams: func(json.RawMessage) any {
		close(asked)
		time.Sleep(100 * time.Millisecond)
		return protocol.RunLaunchResult{Waiting: &protocol.LaunchWaiting{ID: "run-w", Refusal: "no room"}}
	}}
	cancelled := &fakeReply{result: protocol.LaunchWaiting{ID: "run-w", State: "cancelled"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodRunLaunch: launch, protocol.MethodLaunchWaitingCancel: cancelled})
	go func() {
		<-asked
		_ = syscall.Kill(os.Getpid(), syscall.SIGINT)
	}()
	var said strings.Builder
	_, err := launchWaiting(c, protocol.RunLaunchParams{Task: "wait", Wait: true}, &said, time.Hour, "aether session -- wait")
	if err == nil || !strings.Contains(err.Error(), "nothing started") || !strings.Contains(string(cancelled.params()), `"run-w"`) {
		t.Fatalf("an interrupt before the answer = %v, cancel params %s; want the kept launch cancelled", err, cancelled.params())
	}
}
