package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/linear/api"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// Each member's Linear methods use only their own key, and a connector is
// refused every one of them whatever its scopes.
func TestLinearUsesTheCallersOwnKey(t *testing.T) {
	t.Parallel()
	linearAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := map[string]string{"key-alice": "Alice L", "key-bob": "Bob L"}[r.Header.Get("Authorization")]
		if name == "" {
			_, _ = w.Write([]byte(`{"errors":[{"message":"Authentication required"}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"name":"` + name + `","organization":{"name":"Acme","urlKey":"acme"},"assignedIssues":{"nodes":[{"identifier":"CLO-1","title":"` + name + `'s issue","url":"https://linear.app/acme/issue/CLO-1","state":{"name":"Todo","type":"unstarted","color":"#fff"}}]}}}}`))
	}))
	t.Cleanup(linearAPI.Close)
	dir := filepath.Join(t.TempDir(), "linear")
	e := newTestEnv(t, func(c *Config) {
		c.Services.Linear = &api.Service{Keys: api.NewKeys(dir), Client: &api.Client{URL: linearAPI.URL, HTTP: linearAPI.Client()}}
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

// Removing a member deletes their saved Linear key and no one else's.
func TestMemberRemoveDeletesTheLinearKey(t *testing.T) {
	t.Parallel()
	keys := api.NewKeys(t.TempDir())
	e := newTestEnv(t, func(c *Config) { c.Services.Linear = &api.Service{Keys: keys} })
	asSuperAdmin(t, e)
	_, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	for _, id := range []domain.MemberID{target.ID, e.member.ID} {
		if err := keys.Put(id, "key-"+string(id)); err != nil {
			t.Fatal(err)
		}
	}
	if err := controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil); err != nil {
		t.Fatalf("member.remove: %v", err)
	}
	if _, err := keys.Get(target.ID); !errors.Is(err, api.ErrNotConnected) {
		t.Errorf("removed member's Linear key: %v, want it deleted", err)
	}
	if _, err := keys.Get(e.member.ID); err != nil {
		t.Errorf("another member's Linear key: %v, want it kept", err)
	}
}

// A key that cannot be deleted is reported once the member is removed, as
// a signing key failure is.
func TestMemberRemoveReportsALinearKeyItCannotDelete(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	e := newTestEnv(t, func(c *Config) { c.Services.Linear = &api.Service{Keys: api.NewKeys(dir)} })
	asSuperAdmin(t, e)
	_, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	if err := os.MkdirAll(filepath.Join(dir, string(target.ID)+".key", "stuck"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := controlClient(t, e).Call(protocol.MethodMemberRemove, protocol.MemberRemoveParams{MemberID: string(target.ID)}, nil)
	wantRefusal(t, err, "the member is removed, but its Linear key is not")
	if _, gerr := e.store.GetMember(context.Background(), target.ID); gerr == nil {
		t.Error("member still exists, want removed")
	}
}

// A connect that finishes after its member was removed leaves no key.
func TestLinearConnectLeavesNoKeyForARemovedMember(t *testing.T) {
	t.Parallel()
	var e *testEnv
	var target *domain.Member
	linearAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := e.store.DeleteMember(context.Background(), target.ID, ""); err != nil {
			t.Errorf("remove member mid-connect: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"name":"Target","organization":{"name":"Acme","urlKey":"acme"},"assignedIssues":{"nodes":[]}}}}`))
	}))
	t.Cleanup(linearAPI.Close)
	keys := api.NewKeys(t.TempDir())
	e = newTestEnv(t, func(c *Config) {
		c.Services.Linear = &api.Service{Keys: keys, Client: &api.Client{URL: linearAPI.URL, HTTP: linearAPI.Client()}}
	})
	targetKey, m := addMember(t, e, "Target", domain.RoleCollaborator, false)
	target = m
	err := controlAs(t, e, targetKey).Call(protocol.MethodLinearConnect, protocol.LinearConnectParams{APIKey: "key-target"}, nil)
	wantRefusal(t, err, "member no longer exists")
	if _, kerr := keys.Get(target.ID); !errors.Is(kerr, api.ErrNotConnected) {
		t.Errorf("removed member's Linear key: %v, want none", kerr)
	}
}

// When the member lookup after a connect fails for any reason but the member
// being gone, the key is deleted and the error says the member is unconfirmed.
func TestLinearConnectDeletesTheKeyItCannotConfirm(t *testing.T) {
	t.Parallel()
	var e *testEnv
	linearAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := e.store.Close(); err != nil {
			t.Errorf("close store mid-connect: %v", err)
		}
		_, _ = w.Write([]byte(`{"data":{"viewer":{"name":"Target","organization":{"name":"Acme","urlKey":"acme"},"assignedIssues":{"nodes":[]}}}}`))
	}))
	t.Cleanup(linearAPI.Close)
	keys := api.NewKeys(t.TempDir())
	e = newTestEnv(t, func(c *Config) {
		c.Services.Linear = &api.Service{Keys: keys, Client: &api.Client{URL: linearAPI.URL, HTTP: linearAPI.Client()}}
	})
	targetKey, target := addMember(t, e, "Target", domain.RoleCollaborator, false)
	err := controlAs(t, e, targetKey).Call(protocol.MethodLinearConnect, protocol.LinearConnectParams{APIKey: "key-target"}, nil)
	wantRefusal(t, err, "linear.connect: could not confirm the member still exists:")
	if strings.Contains(err.Error(), "no longer exists") {
		t.Errorf("error = %v, want no claim that the member is gone", err)
	}
	if _, kerr := keys.Get(target.ID); !errors.Is(kerr, api.ErrNotConnected) {
		t.Errorf("Linear key after an unconfirmed member: %v, want none", kerr)
	}
}
