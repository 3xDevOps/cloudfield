//go:build integration

package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/acphost"
	"github.com/3xDevOps/cloudfield/internal/acphost/acpmock"
	"github.com/3xDevOps/cloudfield/internal/coordtransport"
	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/protocol"
	"github.com/3xDevOps/cloudfield/internal/store"
)

// reportRepeatEnv is an Enhanced server whose sessions start subsessions
// that run one shell line as their only turn.
type reportRepeatEnv struct {
	*enhancedServer
	t   *testing.T
	ctx context.Context
	n   int
}

func newReportRepeatEnv(ctx context.Context, t *testing.T) *reportRepeatEnv {
	t.Helper()
	env := startEnhancedServer(ctx, t)
	var settings protocol.ChildSettings
	if status := postJSON(t, env.web+"/api/v1/child.settings", `{"enabled":true}`, &settings); status != http.StatusOK || !settings.Enabled {
		t.Fatalf("child.settings status %d: %+v", status, settings)
	}
	return &reportRepeatEnv{enhancedServer: env, t: t, ctx: ctx}
}

func (e *reportRepeatEnv) launch(task string) domain.RunID {
	e.t.Helper()
	var raw json.RawMessage
	params, _ := json.Marshal(protocol.RunLaunchParams{WorkspaceID: string(e.ws.ID), Task: task, Harness: "fake", Mode: string(domain.LaunchACP)})
	if status := postJSON(e.t, e.web+"/api/v1/run.launch", string(params), &raw); status != http.StatusOK {
		e.t.Fatalf("run.launch status %d: %s", status, raw)
	}
	var launched protocol.RunResult
	if err := json.Unmarshal(raw, &launched); err != nil {
		e.t.Fatal(err)
	}
	return domain.RunID(launched.Run.ID)
}

// family launches a parent whose first turn starts a subsession running
// script, with $CLI naming the coordination command and PARENT the parent,
// and returns both once the parent's turn ended. The script starts once the
// server has the subsession's turn start, which waits for its launch to
// finish.
func (e *reportRepeatEnv) family(script string) (parent, child domain.RunID) {
	e.t.Helper()
	e.n++
	if strings.Contains(script, "'") {
		e.t.Fatalf("script %q must not contain a single quote", script)
	}
	task := fmt.Sprintf("%sprintf '%%s' '%sCLI=%s; sleep 5; %s' | sed \"s/PARENT/$AETHER_RUN_ID/g\" >/tmp/task && %s subsession start --harness fake --mode enhanced --purpose 'report twice' --task-file /tmp/task --idempotency-key child-%d",
		acpmock.PromptRun, acpmock.PromptRun, coordtransport.CLIPath, script, coordtransport.CLIPath, e.n)
	parent = e.launch(task)
	items := waitTurns(e.ctx, e.t, e.srv, parent, 1)
	var tree protocol.RunChildrenResult
	if status := postJSON(e.t, e.web+"/api/v1/run.children", `{"run_id":"`+string(parent)+`"}`, &tree); status != http.StatusOK || len(tree.Children) != 1 {
		e.t.Fatalf("run.children status %d: %+v; the parent said %q", status, tree, assistantText(items))
	}
	return parent, domain.RunID(tree.Children[0].RunID)
}

func (e *reportRepeatEnv) get(run domain.RunID) protocol.Run {
	e.t.Helper()
	var got protocol.RunResult
	if status := postJSON(e.t, e.web+"/api/v1/run.get", `{"run_id":"`+string(run)+`"}`, &got); status != http.StatusOK {
		e.t.Fatalf("run.get status %d", status)
	}
	return got.Run
}

// mail is every message from sent to to, oldest first.
func (e *reportRepeatEnv) mail(from, to domain.RunID) []string {
	e.t.Helper()
	page, err := e.srv.Store().ListRunMessages(e.ctx, store.RunMessageFilter{WorkspaceID: e.ws.ID, RunID: to, Limit: 50})
	if err != nil {
		e.t.Fatal(err)
	}
	var bodies []string
	for _, m := range page.Items {
		if m.FromRun == from && m.ToRun == to {
			bodies = append(bodies, m.Body)
		}
	}
	return bodies
}

// wakes counts the turns mail started in run.
func (e *reportRepeatEnv) wakes(run domain.RunID) int {
	e.t.Helper()
	page, err := e.srv.sched.ACPHistory(run, 0, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	n := 0
	for _, it := range page.Items {
		if it.Kind == acphost.KindMessage && it.Message.Role == "user" && strings.Contains(it.Message.Text, "unacknowledged inbox item") {
			n++
		}
	}
	return n
}

func (e *reportRepeatEnv) kill(runs ...domain.RunID) {
	e.t.Helper()
	for _, run := range runs {
		if status := postJSON(e.t, e.web+"/api/v1/run.kill", `{"run_id":"`+string(run)+`"}`, &json.RawMessage{}); status != http.StatusOK {
			e.t.Errorf("run.kill %s status %d", run, status)
		}
	}
}

func (e *reportRepeatEnv) settle(runs ...domain.RunID) {
	e.t.Helper()
	for _, run := range runs {
		quiesce(e.ctx, e.t, e.srv, run)
	}
}

// TestIntegrationRepeatReports: what a parent and a person get when a
// subsession reports more than once in a turn, reports only that it
// replied, or sends its findings as mail before its report.
func TestIntegrationRepeatReports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	e := newReportRepeatEnv(ctx, t)
	defer e.stop(t)
	report := func(outcome, summary, key string) string {
		return fmt.Sprintf("$CLI report --outcome %s --summary %s --idempotency-key %s", outcome, summary, key)
	}
	// run plays script as a subsession's only turn and returns the mail it
	// left its parent, the turns that mail started there, and itself.
	run := func(t *testing.T, script string, wait time.Duration, started ...func(parent, child domain.RunID)) ([]string, int, protocol.Run) {
		t.Helper()
		e.t = t
		parent, child := e.family(script)
		defer e.kill(parent, child)
		for _, fn := range started {
			fn(parent, child)
		}
		waitTurns(ctx, t, e.srv, child, 1)
		time.Sleep(wait)
		e.settle(child, parent)
		return e.mail(child, parent), e.wakes(parent), e.get(child)
	}

	t.Run("a second success in one turn is sent once", func(t *testing.T) {
		mail, wakes, child := run(t, report("success", "first", "a")+"; "+report("success", "again", "b"), 0)
		if len(mail) != 1 || mail[0] != "first" || wakes != 1 || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q in %d wakes, child %q; want the success once", mail, wakes, child.Reason)
		}
	})
	t.Run("a second success after new mail is sent too", func(t *testing.T) {
		mail, _, _ := run(t, report("success", "first", "a")+"; until $CLI inbox | grep -q follow-up; do sleep 1; done; "+report("success", "second", "b"), 0,
			func(parent, child domain.RunID) {
				for len(e.mail(child, parent)) == 0 {
					select {
					case <-ctx.Done():
						t.Fatal("the first success never reached the parent")
					case <-time.After(200 * time.Millisecond):
					}
				}
				sender := e.launch(acpmock.PromptRun + coordtransport.CLIPath + " send --to " + string(child) + " --body follow-up")
				t.Cleanup(func() { e.kill(sender) })
			})
		if len(mail) != 2 || mail[0] != "second" || mail[1] != "first" {
			t.Fatalf("parent mail %q, want both results", mail)
		}
	})
	t.Run("a failure after a success is sent too", func(t *testing.T) {
		mail, _, child := run(t, report("success", "done", "a")+"; "+report("failure", "broke", "b")+"; "+report("failure", "broke-again", "c"), 0)
		if len(mail) != 2 || mail[0] != "broke" || mail[1] != "done" || child.Reason != domain.ReportedFailureReason {
			t.Fatalf("parent mail %q, child %q; want the success, then the failure once", mail, child.Reason)
		}
	})
	t.Run("answered after success keeps the success", func(t *testing.T) {
		mail, wakes, child := run(t, report("success", "done", "a")+"; "+report("answered", "replied", "b"), 0)
		if len(mail) != 1 || mail[0] != "done" || wakes != 1 || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q in %d wakes, child %q (%s); want the reported success kept", mail, wakes, child.Reason, child.Activity)
		}
	})
	t.Run("success after waiting wins", func(t *testing.T) {
		mail, _, child := run(t, report("waiting", "on-ci", "a")+"; "+report("success", "done", "b"), 0)
		if len(mail) != 1 || mail[0] != "done" || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q, child %q; want the success", mail, child.Reason)
		}
	})
	t.Run("an answered turn is forwarded as one", func(t *testing.T) {
		mail, _, _ := run(t, report("answered", "replied", "a"), 20*time.Second)
		if len(mail) != 1 || !strings.HasPrefix(mail[0], "Provisional report: this session ended its turn after reporting answered") {
			t.Fatalf("parent mail %q, want the answered turn's last message under a header that names its report", mail)
		}
	})
	t.Run("findings in three mails and a report", func(t *testing.T) {
		send := func(part string) string { return "$CLI send --to PARENT --body " + part }
		mail, wakes, _ := run(t, send("part-1")+"; sleep 8; "+send("part-2")+"; sleep 8; "+send("part-3")+"; sleep 8; "+report("success", "sent-my-findings", "a"), 0)
		if len(mail) != 4 {
			t.Fatalf("parent mail %q, want three parts and the report", mail)
		}
		t.Logf("one review in %d messages woke its parent %d times", len(mail), wakes)
	})
}
