package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/domain"
	edgeagent "github.com/3xDevOps/cloudfield/internal/edge/agent"
	edgeproto "github.com/3xDevOps/cloudfield/internal/edge/proto"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

// An admin on an approved connection gets a claim code bound to their own
// member, and claiming with it binds the account to that admin. A
// collaborator, a device awaiting approval and an owned server get none.
func TestServerEdgeClaimCode(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	asSuperAdmin(t, e)
	ctx := context.Background()
	admin := controlClient(t, e)
	method := protocol.MethodServerEdgeClaimCode
	var pe *protocol.Error
	if err := admin.Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeUnavailable {
		t.Fatalf("claim code with the edge off = %v, want CodeUnavailable", err)
	}
	edge := &fakeEdgeOwner{state: edgeproto.StateClaimed}
	e.srv.SetEdgeOwner(edge)

	bobKey, _ := addMember(t, e, "Bob", domain.RoleCollaborator, false)
	if err := controlAs(t, e, bobKey).Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeDenied {
		t.Fatalf("collaborator claim code = %v, want CodeDenied", err)
	}
	if err := identities(t, e).BindIdentity(ctx, &domain.Identity{Member: e.member.ID, Provider: "github", Subject: "2002", Login: "admin-alt"}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []domain.DeviceStatus{domain.DevicePending, domain.DeviceRegistered} {
		dev := &domain.Device{Member: e.member.ID, Provider: "github", Subject: "2002",
			Credential: edgeproto.DeviceKeyLine(newSigner(t).PublicKey()), Label: string(status), Status: status}
		if err := identities(t, e).RegisterDevice(ctx, dev); err != nil {
			t.Fatal(err)
		}
		if _, perr := e.srv.dispatch(withDevice(e.member.ID, dev), e.member.ID, method, json.RawMessage(`{}`)); perr == nil ||
			perr.Code != protocol.CodeDenied || !strings.Contains(perr.Message, "needs an approved device") {
			t.Fatalf("claim code from a %s device = %v, want CodeDenied", status, perr)
		}
	}
	issued := func() domain.MemberID {
		edge.mu.Lock()
		defer edge.mu.Unlock()
		return edge.issuedFor
	}
	if got := issued(); got != "" {
		t.Fatalf("a refused call issued a code for %s", got)
	}

	var res protocol.ServerEdgeClaimCodeResult
	if err := admin.Call(method, struct{}{}, &res); err != nil || res.Code == "" || res.ExpiresAt == "" {
		t.Fatalf("admin claim code = %+v, %v", res, err)
	}
	if got := issued(); got != e.member.ID {
		t.Fatalf("code issued for %q, want the calling admin %s", got, e.member.ID)
	}
	codes := newFakeClaimCode()
	codes.admin = string(issued())
	laptop := newSigner(t)
	c, banner, err := e.dialClaim(t, codes, claimGrantFor(octo, laptop), laptop, claimUser(t, res.Code, octo))
	if err != nil {
		t.Fatalf("claim with the issued code: %v (banner %q)", err, banner)
	}
	t.Cleanup(func() { _ = c.Close() })
	if m, err := identities(t, e).GetMemberByIdentity(ctx, octo.Provider, octo.Subject); err != nil || m.ID != e.member.ID || m.Role != domain.RoleSuperAdmin {
		t.Fatalf("claimed account is %+v, %v; want the admin %s", m, err, e.member.ID)
	}
	if deviceStatus(t, e, laptop) != domain.DeviceApproved {
		t.Fatal("the claiming device is not approved")
	}

	e.srv.SetEdgeOwner(&fakeEdgeOwner{owner: &octo, state: edgeproto.StateClaimed})
	if err := admin.Call(method, struct{}{}, nil); !errors.As(err, &pe) || pe.Code != protocol.CodeInvalidState ||
		!strings.Contains(pe.Message, "already owned at its edge by github octo") {
		t.Fatalf("claim code on an owned server = %v, want CodeInvalidState", err)
	}
}

// server.edge carries the notice the edge signed for the server's own
// address, to any member, and nothing for another address or none.
func TestServerEdgeNotice(t *testing.T) {
	t.Parallel()
	e := newTestEnv(t, nil)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleViewer, false)
	bob := controlAs(t, e, bobKey)
	const own, other = "https://edge.example", "https://edge2.example"
	elsewhere := edgeproto.Notice{State: edgeproto.NoticeRetired, Origin: "https://edge0.example", Replacement: other}
	for _, tc := range []struct {
		name    string
		notices edgeagent.StoredNotices
		err     error
		want    string
	}{
		{"unreadable", edgeagent.StoredNotices{}, errors.New("edgeagent: read /var/lib/aether/edge/keys/x/notices.json: unexpected end of JSON input"), ""},
		{"none", edgeagent.StoredNotices{}, nil, ""},
		{"another address", edgeagent.StoredNotices{Notices: []edgeproto.Notice{elsewhere}}, nil, ""},
		{"retiring", edgeagent.StoredNotices{Notices: []edgeproto.Notice{elsewhere, {State: edgeproto.NoticeRetiring, Origin: own, Replacement: other}}}, nil,
			`{"state":"retiring","replacement":"https://edge2.example"}`},
		{"retiring on a date", edgeagent.StoredNotices{Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: own, Replacement: other, Date: "2027-03-01"}}}, nil,
			`{"state":"retiring","replacement":"https://edge2.example","date":"2027-03-01"}`},
		{"first use", edgeagent.StoredNotices{FirstUse: true, Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: own, Replacement: other}}}, nil,
			`{"state":"retiring","replacement":"https://edge2.example","first_use":true}`},
		{"retired", edgeagent.StoredNotices{Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetired, Origin: own, Replacement: other}}}, nil,
			`{"state":"retired","replacement":"https://edge2.example"}`},
	} {
		e.srv.SetEdgeOwner(&fakeEdgeOwner{owner: &octo, state: edgeproto.StateClaimed, notices: tc.notices, noticesErr: tc.err})
		var raw map[string]json.RawMessage
		if err := bob.Call(protocol.MethodServerEdge, struct{}{}, &raw); err != nil {
			t.Fatal(err)
		}
		if got := string(raw["notice"]); got != tc.want {
			t.Errorf("%s: notice %s, want %q", tc.name, got, tc.want)
		}
		if got := fmt.Sprint(raw); strings.Contains(got, "notices.json") || raw["edge_url"] == nil {
			t.Errorf("%s: server.edge = %s", tc.name, got)
		}
	}
}

// After edge move and before the restart, the notice says a restart is
// pending, and to super admins which origin the options file now names,
// canonical; a file naming the running address, the same origin with its
// port written out, none or no origin says nothing.
func TestServerEdgeNoticeAfterAMove(t *testing.T) {
	t.Parallel()
	const own, other = "https://edge.example", "https://edge2.example"
	notices := edgeagent.StoredNotices{Notices: []edgeproto.Notice{{State: edgeproto.NoticeRetiring, Origin: own, Replacement: other}}}
	plain := `{"state":"retiring","replacement":"https://edge2.example"}`
	pending := `{"state":"retiring","replacement":"https://edge2.example","restart_pending":true}`
	moved := `{"state":"retiring","replacement":"https://edge2.example","restart_pending":true,"moved_to":"https://edge2.example"}`
	path := filepath.Join(t.TempDir(), "server.conf")
	e := newTestEnv(t, func(c *Config) { c.ConfigPath = path })
	asSuperAdmin(t, e)
	admin := controlClient(t, e)
	bobKey, _ := addMember(t, e, "Bob", domain.RoleViewer, false)
	bob := controlAs(t, e, bobKey)
	e.srv.SetEdgeOwner(&fakeEdgeOwner{notices: notices})
	for _, tc := range []struct{ config, admin, member string }{
		{"", plain, plain},
		{"edge-url = " + own + ":443\n", plain, plain},
		{"edge-url = " + other + "\n", moved, pending},
		{"edge-url = https://EDGE2.example:443\n", moved, pending},
		{"edge-url = <b>edge</b>\n", plain, plain},
	} {
		if err := os.WriteFile(path, []byte(tc.config), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, call := range []struct {
			client interface {
				Call(string, any, any) error
			}
			want string
		}{{admin, tc.admin}, {bob, tc.member}} {
			var raw map[string]json.RawMessage
			if err := call.client.Call(protocol.MethodServerEdge, struct{}{}, &raw); err != nil {
				t.Fatal(err)
			}
			if got := string(raw["notice"]); got != call.want {
				t.Errorf("config %q: notice %s, want %s", tc.config, got, call.want)
			}
		}
	}
}
