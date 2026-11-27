package sshd

import (
	"context"
	"slices"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

type warmingMirrors struct {
	*mirrorRPCFake
	warm func(domain.WorkspaceID)
}

func (w warmingMirrors) Warm(_ context.Context, ws domain.WorkspaceID) error {
	w.warm(ws)
	return nil
}

func TestRunLaunchRefreshesTheMirrorOutsideTheAuthorizationLock(t *testing.T) {
	var (
		e      *testEnv
		warmed []domain.WorkspaceID
	)
	e = newTestEnv(t, func(c *Config) {
		c.Services.Mirrors = warmingMirrors{&mirrorRPCFake{}, func(ws domain.WorkspaceID) {
			if !e.srv.authorizationMu.TryLock() {
				t.Error("the launch held the authorization lock while its mirror refreshed")
				return
			}
			e.srv.authorizationMu.Unlock()
			warmed = append(warmed, ws)
		}}
	})
	if _, perr := callJSON(t, e, e.member.ID, protocol.MethodRunLaunch, protocol.RunLaunchParams{
		WorkspaceID: string(e.ws.ID), Task: "build it", Harness: "claude",
	}); perr != nil {
		t.Fatal(perr)
	}
	if !slices.Equal(warmed, []domain.WorkspaceID{e.ws.ID}) {
		t.Fatalf("warmed = %v, want the launch's workspace once", warmed)
	}
}
