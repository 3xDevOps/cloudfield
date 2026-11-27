package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/cli"
	"github.com/3xDevOps/cloudfield/internal/envname"
	"github.com/3xDevOps/cloudfield/internal/protocol"
)

func TestPrintLoginsInPriorityOrder(t *testing.T) {
	var out bytes.Buffer
	err := printLogins(&out, []loginUsage{
		{
			AgentLogin: protocol.AgentLogin{ID: "lgn-test1", Name: "work", Kind: "subscription", Plan: "max", Email: "ada@work.example.test", OrganizationID: "org-test1", OrganizationName: "Test Team", Automatic: true, Shared: true, Position: 3},
			Usage:      &protocol.UsageProvider{Provider: "claude", Status: "stale", Error: "usage endpoint answered 429; showing the last read", Windows: []protocol.UsageWindow{{Label: "5h", UsedPercent: 12}, {Label: "7d", UsedPercent: 40.4}}},
		},
		{
			AgentLogin: protocol.AgentLogin{ID: "default", Name: "default", Kind: "subscription", Plan: "pro", Email: "ada@home.example.test", Automatic: true, Shared: false, Position: 0},
			UsageError: "rpc error -32010: account.usage: usage service not configured",
		},
		{
			AgentLogin: protocol.AgentLogin{ID: "lgn-test2", Name: "console", Kind: "console", Email: "ada@org.example.test", OrganizationID: "org-test2", PerUse: true, Automatic: false, Shared: true, Position: 5},
			Usage:      &protocol.UsageProvider{Provider: "claude", Status: "unavailable", Error: "no usage for a Console login"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `#  LOGIN    KIND          PLAN  EMAIL                  ORGANISATION  USAGE           AUTOMATIC  SHARES  BILLED
1  work     subscription  max   ada@work.example.test  Test Team     5h 12%, 7d 40%  on         on      -
2  default  subscription  pro   ada@home.example.test  -             not read        on         off     -
3  console  console       -     ada@org.example.test   org-test2     unavailable     off        on      per use
usage of login work: usage endpoint answered 429; showing the last read
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

	renamed := []protocol.AgentLogin{{ID: "lgn-test2", Name: "lgn-test1"}, {ID: "default", Name: "default"}, {ID: "lgn-test1", Name: "work"}}
	if _, err := loginID(renamed, "lgn-test1"); err == nil || err.Error() != `"lgn-test1" names two of your logins, as the id of login work and as the name of the login with id lgn-test2; use work or lgn-test2` {
		t.Fatalf("loginID(one login's id and another's name) = %v; want both named", err)
	}
	for value, want := range map[string]string{"work": "lgn-test1", "lgn-test2": "lgn-test2", "default": "default"} {
		if id, err := loginID(renamed, value); err != nil || id != want {
			t.Errorf("loginID(%s) = %q, %v; want %s", value, id, err, want)
		}
	}
}

// unlinked points the CLI at an empty config directory, so a command that
// reaches the server fails with cli.ErrNotLinked instead of dialling one.
func unlinked(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(cli.ConfigDirEnv, dir)
	t.Setenv(envname.New(cli.ConfigDirEnv), dir)
}

func TestAgentLoginRefusesAnEmptyAs(t *testing.T) {
	unlinked(t)
	for _, args := range [][]string{{"claude", "--as="}, {"claude", "--as", ""}} {
		if err := agentLogin(args); err == nil || err.Error() != "--as needs a name: cloudfield agent login claude --as <name>" {
			t.Errorf("agentLogin(%q) = %v; want the empty name refused before any sign-in", args, err)
		}
	}
}

func TestAgentLoginRefusesAddedLoginsOfAnotherAgentBeforeConnecting(t *testing.T) {
	unlinked(t)
	for _, args := range [][]string{{"codex", "--as", "work"}, {"codex", "--attach", "lgn-test1"}} {
		if err := agentLogin(args); err == nil || err.Error() != "only Claude Code keeps added logins, so codex has none; cloudfield agent login codex signs in its one login" {
			t.Errorf("agentLogin(%q) = %v; want the refusal before any connection", args, err)
		}
	}
}

// refusedTerminal opens no terminal: the server refuses it as the CLI's
// TerminalStream returns a refusal, and opened counts the attempts.
func refusedTerminal(opened *int, code int, message string) func() (*cli.TerminalStream, protocol.TerminalResponse, error) {
	return func() (*cli.TerminalStream, protocol.TerminalResponse, error) {
		*opened++
		return nil, protocol.TerminalResponse{Code: code, Error: message}, errors.New("cli: terminal: " + message)
	}
}

func TestFollowSignInWaitsAfterALostTerminal(t *testing.T) {
	result := &fakeReply{result: protocol.AgentLogin{ID: "lgn-test1", Name: "work", Kind: "subscription", Plan: "max"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentLoginResult: result})
	var out, errOut bytes.Buffer
	lost := func() (*cli.TerminalStream, protocol.TerminalResponse, error) {
		return nil, protocol.TerminalResponse{}, errors.New("cli: write terminal header: EOF")
	}
	if err := followSignIn(&out, &errOut, c, "claude", "lgn-test1", lost); err != nil {
		t.Fatal(err)
	}
	if got, want := errOut.String(), "cli: write terminal header: EOF\nwaiting for the sign-in of login lgn-test1 to end; Ctrl-C stops waiting and leaves it running\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if got, want := out.String(), "added login work: subscription, plan max\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestFollowSignInRefusesAnUnknownIDWithoutWaiting(t *testing.T) {
	result := &fakeReply{result: protocol.AgentLogin{ID: "lgn-test1", Name: "work"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentLoginResult: result})
	var out, errOut bytes.Buffer
	opened := 0
	err := followSignIn(&out, &errOut, c, "claude", "lgn-test1", refusedTerminal(&opened, protocol.CodeNotFound, "store: not found: no sign-in of a login lgn-test1"))
	if err == nil || err.Error() != "cli: terminal: store: not found: no sign-in of a login lgn-test1" {
		t.Errorf("followSignIn = %v; want the server's refusal alone", err)
	}
	if opened != 1 || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("opened %d times, stdout %q, stderr %q; want one attempt and nothing else printed", opened, out.String(), errOut.String())
	}
}

func TestFollowSignInReportsASignInThatEndedOnce(t *testing.T) {
	result := &fakeReply{result: protocol.AgentLogin{ID: "lgn-test1", Name: "work", Kind: "subscription", Plan: "max"}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentLoginResult: result})
	var out, errOut bytes.Buffer
	opened := 0
	ended := refusedTerminal(&opened, protocol.CodeConflict, "store: conflict: the sign-in of login lgn-test1 has ended; add the login again")
	if err := followSignIn(&out, &errOut, c, "claude", "lgn-test1", ended); err != nil {
		t.Fatal(err)
	}
	if out.String() != "added login work: subscription, plan max\n" || errOut.Len() != 0 {
		t.Errorf("stdout %q, stderr %q; want the added login alone", out.String(), errOut.String())
	}

	result.err = &protocol.Error{Code: protocol.CodeInternal, Message: "the sign-in ended before Claude Code saved a login; nothing was added"}
	out.Reset()
	err := followSignIn(&out, &errOut, c, "claude", "lgn-test1", ended)
	if err == nil || err.Error() != "rpc error -32603: the sign-in ended before Claude Code saved a login; nothing was added" {
		t.Errorf("followSignIn = %v; want the server's reason", err)
	}
	if out.Len() != 0 || errOut.Len() != 0 || opened != 2 {
		t.Errorf("opened %d times, stdout %q, stderr %q; want one attempt each and only the reason", opened, out.String(), errOut.String())
	}
}

func TestFollowSignInShowsAFailedStartOnce(t *testing.T) {
	const failed = "scheduler: start the sign-in of login lgn-test1: runtime: image e2e/fake not found"
	result := &fakeReply{err: &protocol.Error{Code: protocol.CodeInternal, Message: failed}}
	c := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentLoginResult: result})
	var out, errOut bytes.Buffer
	opened := 0
	err := followSignIn(&out, &errOut, c, "claude", "lgn-test1", refusedTerminal(&opened, protocol.CodeInternal, failed))
	if err == nil || err.Error() != "rpc error -32603: "+failed {
		t.Errorf("followSignIn = %v; want the failed start", err)
	}
	if opened != 1 || out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("opened %d times, stdout %q, stderr %q; want the failure shown once", opened, out.String(), errOut.String())
	}
}

// silentControl is a control channel that never answers, as a server whose
// sign-in has not ended does not answer agent.login.result.
func silentControl(t *testing.T) *protocol.Client {
	server, client := net.Pipe()
	go func() { _, _ = io.Copy(io.Discard, server) }()
	c := protocol.NewClient(client)
	t.Cleanup(func() { _ = c.Close(); _ = server.Close() })
	return c
}

func retryTimes(t *testing.T, pause, limit time.Duration) {
	oldPause, oldLimit := signInRetryPause, signInRetryLimit
	signInRetryPause, signInRetryLimit = pause, limit
	t.Cleanup(func() { signInRetryPause, signInRetryLimit = oldPause, oldLimit })
}

func TestFollowSignInRetriesASignInStillStarting(t *testing.T) {
	retryTimes(t, time.Millisecond, 30*time.Millisecond)
	var out, errOut bytes.Buffer
	opened := 0
	starting := refusedTerminal(&opened, protocol.CodeConflict, "store: conflict: the sign-in of login lgn-test1 is starting; open its terminal again in a moment")
	err := followSignIn(&out, &errOut, silentControl(t), "claude", "lgn-test1", starting)
	if err == nil || err.Error() != "the sign-in of login lgn-test1 is still starting after 30ms; open its terminal again with:\n  cloudfield agent login claude --attach lgn-test1" {
		t.Errorf("followSignIn = %v; want the command to run again", err)
	}
	if got, want := errOut.String(), "cli: terminal: store: conflict: the sign-in of login lgn-test1 is starting; open its terminal again in a moment\n"+
		"trying again every 1ms for up to 30ms; Ctrl-C stops trying and leaves the sign-in running\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
	if opened < 2 || out.Len() != 0 {
		t.Errorf("opened %d times, stdout %q; want it opened again", opened, out.String())
	}
}

func TestFollowSignInStopsRetryingAtItsDeadlineWhileAnOpenHangs(t *testing.T) {
	retryTimes(t, time.Millisecond, 50*time.Millisecond)
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	opened := 0
	refused := refusedTerminal(&opened, protocol.CodeConflict, "store: conflict: the sign-in of login lgn-test1 is starting; open its terminal again in a moment")
	open := func() (*cli.TerminalStream, protocol.TerminalResponse, error) {
		if opened == 0 {
			return refused()
		}
		<-hang
		return nil, protocol.TerminalResponse{}, errors.New("cli: terminal: closed")
	}
	done := make(chan error, 1)
	go func() {
		var out, errOut bytes.Buffer
		done <- followSignIn(&out, &errOut, silentControl(t), "claude", "lgn-test1", open)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.HasPrefix(err.Error(), "the sign-in of login lgn-test1 is still starting after 50ms;") {
			t.Errorf("followSignIn = %v; want it to give up at the deadline", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("followSignIn waited on a hung open past its deadline")
	}
}

func TestAgentLoginTakesItsFlagsBeforeOrAfterTheAgent(t *testing.T) {
	unlinked(t)
	for _, args := range [][]string{{"claude", "--as", "work"}, {"--as", "work", "claude"}, {"--attach", "lgn-test1", "claude"}} {
		if err := agentLogin(args); !errors.Is(err, cli.ErrNotLinked) {
			t.Errorf("agentLogin(%q) = %v; want it parsed and on its way to the server", args, err)
		}
	}
	if err := agentLogin([]string{"--as", "work"}); err == nil || !strings.HasSuffix(err.Error(), "usage: cloudfield agent login <name> [--as <login> | --attach <login-id>]") {
		t.Errorf("agentLogin without an agent = %v; want the usage", err)
	}
}

func TestAddedLoginSignInNeedsAServerThatKeepsLogins(t *testing.T) {
	agents := &fakeReply{result: protocol.AgentListResult{Agents: []protocol.AgentInfo{{Name: "claude", LoginCommand: "claude auth login", Installed: true}}}}
	old := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodServerInfo: {result: protocol.ServerInfoResult{ServerVersion: "v0.9.0", Features: []string{protocol.FeaturePermission}}},
		protocol.MethodAgentList:  agents,
	})
	for _, flag := range []string{"--as", "--attach"} {
		if _, _, err := loginAgent(old, "claude", flag); err == nil || err.Error() != flag+`: server v0.9.0 lacks the "logins" feature, so it keeps only the login in your home; update the server with cloudfield server update` {
			t.Errorf("loginAgent(%s) on a server without logins = %v; want the refusal before any terminal opens", flag, err)
		}
	}
	listOnly := fakeControlMethods(t, map[string]*fakeReply{protocol.MethodAgentList: agents})
	if agent, found, err := loginAgent(listOnly, "claude", ""); err != nil || !found || agent.Name != "claude" {
		t.Errorf("loginAgent of the default login = %+v, %v, %v; want the agent, read as before", agent, found, err)
	}
	current := fakeControlMethods(t, map[string]*fakeReply{
		protocol.MethodServerInfo: {result: protocol.ServerInfoResult{ServerVersion: "v0.10.0", Features: []string{protocol.FeatureLogins}}},
		protocol.MethodAgentList:  agents,
	})
	if _, found, err := loginAgent(current, "claude", "--attach"); err != nil || !found {
		t.Errorf("loginAgent(--attach) on a server with logins = %v, %v", found, err)
	}
}

func TestAddRefusedNamesTheRunningSignInsAttachLine(t *testing.T) {
	const ends = "that sign-in ends by itself 15 minutes after its terminal opened, or 10 minutes after it started if none did; "
	for _, tc := range []struct{ message, want string }{
		{`the sign-in of login "work" (lgn-test1) is still running; finish it first`,
			"rpc error -32003: the sign-in of login \"work\" (lgn-test1) is still running; finish it first\n" + ends +
				"to finish it sooner, return to it with:\n  cloudfield agent login claude --attach lgn-test1"},
		{`the sign-in of login "lgn-old" (lgn-test2) is still running; finish it first`,
			"rpc error -32003: the sign-in of login \"lgn-old\" (lgn-test2) is still running; finish it first\n" + ends +
				"to finish it sooner, return to it with:\n  cloudfield agent login claude --attach lgn-test2"},
		{`the sign-in of login "work" is still running; finish it first`,
			"rpc error -32003: the sign-in of login \"work\" is still running; finish it first\n" + ends +
				"to finish it sooner, run the cloudfield agent login claude --attach <login-id> line it printed when it started"},
	} {
		err := addRefused(&protocol.Error{Code: protocol.CodeConflict, Message: tc.message}, "claude")
		if err == nil || err.Error() != tc.want {
			t.Errorf("addRefused(%q) =\n%v\nwant\n%s", tc.message, err, tc.want)
		}
	}
}
