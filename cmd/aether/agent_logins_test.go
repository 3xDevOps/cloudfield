package main

import (
	"bytes"
	"testing"

	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestPrintLoginsInPriorityOrder(t *testing.T) {
	var out bytes.Buffer
	err := printLogins(&out, []loginUsage{
		{
			AgentLogin: protocol.AgentLogin{ID: "lgn-test1", Name: "work", Kind: "subscription", Plan: "max", Email: "ada@work.example.test", Automatic: true, Shared: true, Position: 3},
			Usage:      &protocol.UsageProvider{Provider: "claude", Status: "ok", Windows: []protocol.UsageWindow{{Label: "5h", UsedPercent: 12}, {Label: "7d", UsedPercent: 40.4}}},
		},
		{
			AgentLogin: protocol.AgentLogin{ID: "default", Name: "default", Kind: "subscription", Plan: "pro", Email: "ada@home.example.test", Automatic: true, Shared: false, Position: 0},
			UsageError: "rpc error -32010: account.usage: usage service not configured",
		},
		{
			AgentLogin: protocol.AgentLogin{ID: "lgn-test2", Name: "console", Kind: "console", Email: "ada@org.example.test", PerUse: true, Automatic: false, Shared: true, Position: 5},
			Usage:      &protocol.UsageProvider{Provider: "claude", Status: "unavailable", Error: "no usage for a Console login"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `#  LOGIN    KIND          PLAN  EMAIL                  USAGE           AUTOMATIC  SHARES  BILLED
1  work     subscription  max   ada@work.example.test  5h 12%, 7d 40%  on         on      -
2  default  subscription  pro   ada@home.example.test  not read        on         off     -
3  console  console       -     ada@org.example.test   unavailable     off        on      per use
usage of login default: rpc error -32010: account.usage: usage service not configured
usage of login console: no usage for a Console login
`
	if out.String() != want {
		t.Fatalf("printLogins =\n%s\nwant\n%s", out.String(), want)
	}
}

func TestDescribeLoginNamesWhatTheSignInRecorded(t *testing.T) {
	for _, tc := range []struct {
		login protocol.AgentLogin
		want  string
	}{
		{protocol.AgentLogin{Name: "work", Kind: "subscription", Plan: "max", Email: "ada@work.example.test", OrganizationID: "org-test", OrganizationName: "Test Org"},
			"work: subscription, plan max, ada@work.example.test, organisation Test Org"},
		{protocol.AgentLogin{Name: "console", Kind: "console", Email: "ada@org.example.test", OrganizationID: "org-test", PerUse: true},
			"console: console, ada@org.example.test, organisation org-test, billed per use"},
		{protocol.AgentLogin{Name: "bare"}, "bare: kind unknown"},
	} {
		if got := describeLogin(tc.login); got != tc.want {
			t.Errorf("describeLogin(%+v) = %q, want %q", tc.login, got, tc.want)
		}
	}
}

func TestLoginIDResolvesTheCallersOwnLogin(t *testing.T) {
	logins := []protocol.AgentLogin{{ID: "default", Name: "default"}, {ID: "lgn-test1", Name: "work"}}
	if id, err := loginID(logins, "work"); err != nil || id != "lgn-test1" {
		t.Fatalf("loginID(work) = %q, %v", id, err)
	}
	if id, err := loginID(logins, "default"); err != nil || id != "default" {
		t.Fatalf("loginID(default) = %q, %v", id, err)
	}
	if id, err := loginID(logins, "lgn-test1"); err != nil || id != "lgn-test1" {
		t.Fatalf("loginID(an id) = %q, %v; want the id", id, err)
	}
	if _, err := loginID(logins, "personal"); err == nil || err.Error() != `you have no login named "personal"; cloudfield agent logins lists yours` {
		t.Fatalf("loginID(personal) = %v; want the names it takes", err)
	}
}
