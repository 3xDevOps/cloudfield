//go:build integration

package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/children"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestIntegrationStuckSession holds one session's lifecycle lock with a
// pause its runtime never answers, then has the session's parent cancel it
// again and again, each call giving up. Another member must still launch,
// the parent's socket must still answer, a cancel whose caller gave up must
// name the session it waited for, and none of them may stop the session
// once the pause lets go.
func TestIntegrationStuckSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	t.Setenv("AETHER_FAKE_AGENT", "fake-agent {task}")
	e, srv := newCoordEnv(ctx, t, false)
	release := make(chan struct{})
	defer close(release)

	const (
		parentTask = "stuck parent task"
		childTask  = "stuck child task"
		otherTask  = "another member's task"
		// coordSlots is the coordination socket's connection cap for one session.
		coordSlots = 16
		gaveUp     = 2 * time.Second
		prompt     = 20 * time.Second
	)
	socketOf := func(run string) string { return filepath.Join(e.coordDir(run), coordtransport.SocketName) }
	e.e2e(t).script(parentTask, func(c *e2eContainer) {
		var receipt protocol.ChildReceipt
		if err := coordtransport.Call(ctx, socketOf(c.spec.CreationKey), protocol.MethodChildStart, protocol.ChildStartParams{
			Harness: "fake", Purpose: "stuck work", Task: childTask, IdempotencyKey: "stuck-1",
		}, &receipt); err != nil {
			c.output("agent-error: child start: " + err.Error() + "\r\n")
			return
		}
		c.output("child:" + receipt.RunID + "\r\n")
		<-release
	})
	e.e2e(t).script(childTask, func(*e2eContainer) { <-release })
	e.e2e(t).script(otherTask, func(*e2eContainer) { <-release })

	ctrl, client := srv.control(t, e.ada.key)
	enabled := true
	if err := ctrl.Call(protocol.MethodChildSettings, protocol.ChildSettingsParams{Enabled: &enabled}, nil); err != nil {
		t.Fatalf("child.settings: %v", err)
	}
	parent := e.launch(t, ctrl, parentTask, "fake")
	att := openAttach(t, client, parent.ID)
	childID := waitValue(t, att, "child:")
	waitRun(t, ctrl, childID, func(r protocol.Run) bool { return r.Status == string(domain.RunRunning) })

	paused, unstick := e.e2e(t).holdPause(childID)
	defer unstick()
	pauseCtrl, _ := srv.control(t, e.ada.key)
	pauseDone := make(chan error, 1)
	go func() {
		pauseDone <- pauseCtrl.Call(protocol.MethodRunPause, protocol.RunIDParams{RunID: childID}, nil)
	}()
	select {
	case <-paused:
	case <-time.After(prompt):
		t.Fatal("run.pause never reached the runtime")
	}

	var agents sync.WaitGroup
	for range coordSlots {
		agents.Go(func() {
			callCtx, stop := context.WithTimeout(ctx, gaveUp)
			defer stop()
			if err := coordtransport.Call(callCtx, socketOf(parent.ID), protocol.MethodChildCancel, protocol.ChildRunParams{RunID: childID}, nil); err == nil {
				t.Errorf("child.cancel of session %s succeeded while its pause held it", childID)
			}
		})
	}
	agents.Wait()

	boCtrl, _ := srv.control(t, e.bo.key)
	launched := make(chan error, 1)
	go func() {
		launched <- boCtrl.Call(protocol.MethodRunLaunch, protocol.RunLaunchParams{
			WorkspaceID: string(e.ws.ID), Task: otherTask, Harness: "fake", Mode: string(domain.LaunchTUI),
		}, nil)
	}()
	select {
	case err := <-launched:
		if err != nil {
			t.Errorf("another member's run.launch: %v", err)
		}
	case <-time.After(prompt):
		t.Errorf("another member's run.launch did not answer within %v while session %s was stuck", prompt, childID)
	}

	statusCtx, stop := context.WithTimeout(ctx, prompt)
	err := coordtransport.Call(statusCtx, socketOf(parent.ID), protocol.MethodCoordStatus, nil, nil)
	stop()
	if err != nil {
		t.Errorf("the parent's coordination socket after its %d cancels gave up: %v", coordSlots, err)
	}

	raw, err := json.Marshal(protocol.ChildRunParams{RunID: childID})
	if err != nil {
		t.Fatal(err)
	}
	subsessions := childrenOf(t, srv.srv)
	cancelled := make(chan error, 1)
	go func() {
		callCtx, stop := context.WithTimeout(ctx, gaveUp)
		defer stop()
		_, err := subsessions.HandleAgent(callCtx, domain.RunID(parent.ID), protocol.MethodChildCancel, raw)
		cancelled <- err
	}()
	select {
	case err := <-cancelled:
		if err == nil || !strings.Contains(err.Error(), childID) || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
			t.Errorf("child.cancel after its caller gave up = %v, want an error naming session %s and the deadline", err, childID)
		}
	case <-time.After(prompt):
		t.Errorf("child.cancel was still waiting for session %s %v after its caller gave up", childID, prompt-gaveUp)
	}

	// No cancel that gave up may stop the session once its pause lets go.
	unstick()
	select {
	case err := <-pauseDone:
		if err != nil {
			t.Fatalf("run.pause: %v", err)
		}
	case <-time.After(prompt):
		t.Fatal("run.pause did not return once the runtime answered")
	}
	if child := waitRun(t, ctrl, childID, func(protocol.Run) bool { return true }); child.Status != string(domain.RunRunning) || !child.Paused {
		t.Errorf("session %s after the cancels that gave up = %s (paused %v), want it running and paused", childID, child.Status, child.Paused)
	}
	if err := coordtransport.Call(ctx, socketOf(parent.ID), protocol.MethodChildCancel, protocol.ChildRunParams{RunID: childID}, nil); err != nil {
		t.Errorf("child.cancel of session %s once it was free: %v", childID, err)
	}
	waitRun(t, ctrl, childID, func(r protocol.Run) bool { return domain.RunStatus(r.Status).Terminal() })
	assertNoAgentError(t, att)
}

func childrenOf(t *testing.T, srv *Server) *children.Service {
	t.Helper()
	for _, named := range srv.services {
		if svc, ok := named.svc.(*children.Service); ok {
			return svc
		}
	}
	t.Fatal("the server has no subsession service")
	return nil
}
