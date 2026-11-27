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

// family launches a parent whose first turn starts a subsession, and plays
// script as the subsession's second turn, with $CLI naming the coordination
// command and PARENT the parent. The second turn starts once the server
// shows the first one's report, so it has both turn boundaries before
// script reports.
func (e *reportRepeatEnv) family(script string) (parent, child domain.RunID) {
	e.t.Helper()
	e.n++
	ready := acpmock.PromptRun + coordtransport.CLIPath + " report --outcome waiting --summary ready --idempotency-key ready"
	parent = e.launch(fmt.Sprintf("%s%s subsession start --harness fake --mode enhanced --purpose 'report twice' --task '%s' --idempotency-key child-%d",
		acpmock.PromptRun, coordtransport.CLIPath, ready, e.n))
	items := waitTurns(e.ctx, e.t, e.srv, parent, 1)
	var tree protocol.RunChildrenResult
	if status := postJSON(e.t, e.web+"/api/v1/run.children", `{"run_id":"`+string(parent)+`"}`, &tree); status != http.StatusOK || len(tree.Children) != 1 {
		e.t.Fatalf("run.children status %d: %+v; the parent said %q", status, tree, assistantText(items))
	}
	child = domain.RunID(tree.Children[0].RunID)
	waitTurns(e.ctx, e.t, e.srv, child, 1)
	for e.get(child).Reason != "ready" {
		select {
		case <-e.ctx.Done():
			e.t.Fatalf("subsession %s never showed its waiting report: %+v", child, e.get(child))
		case <-time.After(100 * time.Millisecond):
		}
	}
	prompt := acpmock.PromptRun + "CLI=" + coordtransport.CLIPath + "; " + strings.ReplaceAll(script, "PARENT", string(parent))
	for {
		err := e.srv.sched.WakeEnhanced(e.ctx, child, prompt)
		if err == nil {
			return parent, child
		}
		select {
		case <-e.ctx.Done():
			e.t.Fatalf("prompt %s: %v", child, err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (e *reportRepeatEnv) get(run domain.RunID) protocol.Run {
	e.t.Helper()
	var got protocol.RunResult
	if status := postJSON(e.t, e.web+"/api/v1/run.get", `{"run_id":"`+string(run)+`"}`, &got); status != http.StatusOK {
		e.t.Fatalf("run.get status %d", status)
	}
	return got.Run
}

// mail is every message from sent to to, newest first.
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
// replied, or sends its findings as mail before its report. Every success,
// failure or blocked report is mailed: a second one cannot be told from news.
func TestIntegrationRepeatReports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	e := newReportRepeatEnv(ctx, t)
	defer e.stop(t)
	report := func(outcome, summary, key string) string {
		return fmt.Sprintf("$CLI report --outcome %s --summary %s --idempotency-key %s", outcome, summary, key)
	}
	// run plays script, which makes reports of them, as a subsession's turn
	// and returns the mail it left its parent, the turns that mail started
	// there, and itself. Every report must have been accepted.
	run := func(t *testing.T, script string, reports int, wait time.Duration) ([]string, int, protocol.Run) {
		t.Helper()
		e.t = t
		parent, child := e.family(script)
		defer e.kill(parent, child)
		said := assistantText(waitTurns(ctx, t, e.srv, child, 2))
		if got := strings.Count(said, `"report_id"`) - 1; got != reports {
			t.Fatalf("%d of %d reports accepted: %s", got, reports, said)
		}
		time.Sleep(wait)
		e.settle(child, parent)
		return e.mail(child, parent), e.wakes(parent), e.get(child)
	}

	t.Run("a second success is sent too", func(t *testing.T) {
		mail, _, child := run(t, report("success", "first", "a")+"; "+report("success", "again", "b"), 2, 0)
		if len(mail) != 2 || mail[0] != "again" || mail[1] != "first" || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q, child %q; want both reports", mail, child.Reason)
		}
	})
	t.Run("answered after success keeps the success", func(t *testing.T) {
		mail, wakes, child := run(t, report("success", "done", "a")+"; "+report("answered", "replied", "b"), 2, 0)
		if len(mail) != 1 || mail[0] != "done" || wakes != 1 || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q in %d wakes, child %q (%s); want the reported success kept", mail, wakes, child.Reason, child.Activity)
		}
	})
	t.Run("success after waiting wins", func(t *testing.T) {
		mail, _, child := run(t, report("waiting", "on-ci", "a")+"; "+report("success", "done", "b"), 2, 0)
		if len(mail) != 1 || mail[0] != "done" || child.Reason != domain.ReportedSuccessReason {
			t.Fatalf("parent mail %q, child %q; want the success", mail, child.Reason)
		}
	})
	t.Run("an answered turn is forwarded as one", func(t *testing.T) {
		mail, _, _ := run(t, report("answered", "replied", "a"), 1, 20*time.Second)
		if len(mail) != 1 || !strings.HasPrefix(mail[0], "Provisional report: this session ended its turn with no success, failure or blocked report; its last report was answered") {
			t.Fatalf("parent mail %q, want the answered turn's last message under a header that names its report", mail)
		}
	})
	t.Run("findings in three mails and a report", func(t *testing.T) {
		send := func(part string) string { return "$CLI send --to PARENT --body " + part }
		mail, wakes, _ := run(t, send("part-1")+"; sleep 8; "+send("part-2")+"; sleep 8; "+send("part-3")+"; sleep 8; "+report("success", "sent-my-findings", "a"), 1, 0)
		if len(mail) != 4 {
			t.Fatalf("parent mail %q, want three parts and the report", mail)
		}
		t.Logf("one review in %d messages woke its parent %d times", len(mail), wakes)
	})
}
