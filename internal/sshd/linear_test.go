package sshd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/Aether/internal/domain"
	"github.com/3xDevOps/Aether/internal/linear"
	"github.com/3xDevOps/Aether/internal/protocol"
)

// Each member's Linear methods use only their own key, and a connector is
// refused every one of them whatever its scopes.
func TestLinearUsesTheCallersOwnKey(t *testing.T) {
	t.Parallel()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := map[string]string{"key-alice": "Alice L", "key-bob": "Bob L"}[r.Header.Get("Authorization")]
		if name == "" {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"name":"` + name + `","organization":{"name":"Acme","urlKey":"acme"},"assignedIssues":{"nodes":[{"identifier":"CLO-1","title":"` + name + `'s issue","url":"https://linear.app/acme/issue/CLO-1","state":{"name":"Todo","type":"unstarted","color":"#fff"}}]}}}}`))
	}))
	t.Cleanup(api.Close)
	dir := filepath.Join(t.TempDir(), "linear")
	e := newTestEnv(t, func(c *Config) {
		c.Services.Linear = &linear.Service{Keys: linear.NewKeys(dir), Client: &linear.Client{URL: api.URL, HTTP: api.Client()}}
	})
	aliceKey, alice := addMember(t, e, "Alice", domain.RoleCollaborator, false)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleViewer, false)
	aliceC, bobC := controlAs(t, e, aliceKey), controlAs(t, e, bobKey)

	var status protocol.LinearStatusResult
	if err := aliceC.Call(protocol.MethodLinearStatus, nil, &status); err != nil || status.Connected {
		t.Fatalf("alice's status before connecting = %+v, %v; want not connected", status, err)
	}
	var issues protocol.LinearIssuesResult
	issuesErr := aliceC.Call(protocol.MethodLinearIssues, nil, &issues)
	if rpcCode(issuesErr) != protocol.CodeInvalidState || !strings.Contains(issuesErr.Error(), "Linear is not connected: save a personal API key in Settings → Linear") {
		t.Fatalf("alice's issues without a key = %v", issuesErr)
	}
	if err := aliceC.Call(protocol.MethodLinearConnect, protocol.LinearConnectParams{APIKey: "key-wrong"}, nil); err == nil || !strings.Contains(err.Error(), "Authentication required") {
		t.Fatalf("connect with a refused key = %v, want Linear's own error", err)
	}
	if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
		t.Fatalf("a refused key left %s behind: %v", dir, statErr)
	}
	if err := aliceC.Call(protocol.MethodLinearConnect, protocol.LinearConnectParams{APIKey: "key-alice"}, &status); err != nil || status.Name != "Alice L" || !status.Connected {
		t.Fatalf("alice connects = %+v, %v", status, err)
	}

	if err := bobC.Call(protocol.MethodLinearStatus, nil, &status); err != nil || status.Connected {
		t.Fatalf("bob's status after alice connected = %+v, %v; want not connected", status, err)
	}
	if err := bobC.Call(protocol.MethodLinearConnect, protocol.LinearConnectParams{APIKey: "key-bob"}, nil); err != nil {
		t.Fatalf("bob connects: %v", err)
	}
	if err := aliceC.Call(protocol.MethodLinearIssues, nil, &issues); err != nil || len(issues.Issues) != 1 || issues.Issues[0].Title != "Alice L's issue" {
		t.Fatalf("alice's issues = %+v, %v", issues, err)
	}
	if err := bobC.Call(protocol.MethodLinearIssues, nil, &issues); err != nil || len(issues.Issues) != 1 || issues.Issues[0].Title != "Bob L's issue" {
		t.Fatalf("bob's issues = %+v, %v", issues, err)
	}

	info, err := os.Stat(filepath.Join(dir, string(alice.ID)+".key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("alice's key file = %v, %v; want mode 0600", info, err)
	}

	connector := e.srv.ConnectorLocal(ConnectorGrant{Member: alice.ID}, "con-test", "ChatGPT",
		[]string{ConnectorScopeRead, ConnectorScopeWrite, ConnectorScopeControl})
	for _, method := range []string{
		protocol.MethodLinearStatus, protocol.MethodLinearConnect, protocol.MethodLinearDisconnect,
		protocol.MethodLinearTeams, protocol.MethodLinearIssues, protocol.MethodLinearIssue,
	} {
		params, _ := json.Marshal(map[string]string{"api_key": "key-alice", "identifier": "CLO-1"})
		if _, perr := connector.Call(context.Background(), method, params); perr == nil || perr.Code != protocol.CodeDenied {
			t.Errorf("%s from a connector = %v, want denied", method, perr)
		}
		if _, ok := connectorMethodScopes[method]; ok {
			t.Errorf("%s has connector scopes", method)
		}
	}

	if err := aliceC.Call(protocol.MethodLinearDisconnect, nil, nil); err != nil {
		t.Fatalf("alice disconnects: %v", err)
	}
	if err := aliceC.Call(protocol.MethodLinearStatus, nil, &status); err != nil || status.Connected {
		t.Fatalf("alice's status after disconnecting = %+v, %v", status, err)
	}
	if err := bobC.Call(protocol.MethodLinearStatus, nil, &status); err != nil || !status.Connected {
		t.Fatalf("bob's status after alice disconnected = %+v, %v; want still connected", status, err)
	}
}
