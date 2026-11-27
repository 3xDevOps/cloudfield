//go:build integration

package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// TestIntegrationAgentLoginMethodsReachTheScheduler: on a real server the
// sign-in seam is wired, so agent.login.add reserves a sign-in instead of
// answering that the server keeps no added logins, and agent.login.list
// answers for the default login.
func TestIntegrationAgentLoginMethodsReachTheScheduler(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := &coordEnv{rt: newE2ERuntime(), image: "e2e/fake"}
	srv := e.seed(ctx, t, false)
	ctrl, client := srv.control(t, e.ada.key)
	defer client.Close()
	var list protocol.AgentLoginListResult
	if err := ctrl.Call(protocol.MethodAgentLoginList, protocol.AgentLoginParams{Agent: "claude"}, &list); err != nil {
		t.Fatalf("agent.login.list: %v", err)
	}
	if len(list.Logins) != 1 || list.Logins[0].ID != "default" {
		t.Fatalf("logins = %+v; want the default login", list.Logins)
	}
	var added protocol.AgentLoginAddResult
	if err := ctrl.Call(protocol.MethodAgentLoginAdd, protocol.AgentLoginAddParams{Agent: "claude", Name: "work"}, &added); err != nil {
		t.Fatalf("agent.login.add: %v", err)
	}
	if !strings.HasPrefix(added.LoginID, "lgn-") {
		t.Fatalf("agent.login.add = %+v; want a reserved login id", added)
	}
}
