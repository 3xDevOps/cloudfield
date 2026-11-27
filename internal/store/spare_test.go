package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

// TestEveryRunsReadHidesSpares fails when this package reads the runs table
// itself, which holds spare runs, anywhere baseRunReads does not allow.
func TestEveryRunsReadHidesSpares(t *testing.T) {
	baseRunReads := []struct{ file, line, why string }{
		{"migrate.go", "", "migrations change the table itself, spares included"},
		{"runs.go", "`DELETE FROM runs WHERE id = ?`", "deleting a run deletes a spare too"},
		{"profile.go", "SELECT profile_snapshot_id FROM runs", "pruning must keep the profile snapshot a live spare pinned"},
	}
	read := regexp.MustCompile(`\b(FROM|JOIN) runs\b`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
	lines:
		for i, line := range strings.Split(string(src), "\n") {
			if !read.MatchString(line) {
				continue
			}
			for _, allowed := range baseRunReads {
				if allowed.file == file && strings.Contains(line, allowed.line) {
					continue lines
				}
			}
			t.Errorf("%s:%d reads the runs table, so members would see spare runs: %s\n"+
				"read visible_runs instead, or add the line to baseRunReads with the reason it must see spares",
				file, i+1, strings.TrimSpace(line))
		}
	}
}

func TestVisibleRunsShowsALaterRunsColumn(t *testing.T) {
	path := templateDBPath(t)
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.db.Exec(`ALTER TABLE runs ADD COLUMN later TEXT NOT NULL DEFAULT 'shown'`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	run := mustCreateRun(t, db, mustCreateWorkspace(t, db).ID, mustCreateMember(t, db).ID, domain.RunQueued)
	var later string
	if err := db.db.QueryRow(`SELECT later FROM visible_runs WHERE id = ?`, run.ID).Scan(&later); err != nil || later != "shown" {
		t.Fatalf("visible_runs.later = %q, %v; want the column added to runs after the view", later, err)
	}
}

func TestSpareRunsAreHidden(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	w, extra := mustCreateWorkspace(t, db), mustCreateWorkspace(t, db)
	ada, bob := mustCreateMember(t, db), mustCreateTeammate(t, db)
	live := mustCreateRun(t, db, w.ID, ada.ID, domain.RunQueued)
	spare := &domain.Run{WorkspaceID: w.ID, MemberID: ada.ID, AccountMemberID: bob.ID, Harness: "claude",
		Mode: domain.LaunchTUI, Status: domain.RunQueued, Spare: true}
	if err := db.CreateRun(ctx, spare); err != nil {
		t.Fatal(err)
	}
	if err := db.SetRunRepos(ctx, spare.ID, []domain.RunRepo{{WorkspaceID: extra.ID, Name: "lib", Path: "/repos/lib", BaseBranch: "main"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []domain.RunID{live.ID, spare.ID} {
		toRunning(t, db, id, domain.ActivityNeedsYou)
		if err := db.PutRunCost(ctx, &RunCost{RunID: id, WorkspaceID: w.ID, MemberID: ada.ID, InputTokens: 10, CostUSD: 0.5, Metered: true}); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := db.GetRun(ctx, spare.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetRun(spare) = %v, want ErrNotFound", err)
	}
	if got, err := db.WithSpares().GetRun(ctx, spare.ID); err != nil || !got.Spare {
		t.Errorf("WithSpares().GetRun(spare) = %+v, %v; want the spare", got, err)
	}
	for name, list := range map[string]func(*DB) ([]*domain.Run, error){
		"ListRunsByWorkspace": func(d *DB) ([]*domain.Run, error) { return d.ListRunsByWorkspace(ctx, w.ID) },
		"ListRunsByMember":    func(d *DB) ([]*domain.Run, error) { return d.ListRunsByMember(ctx, ada.ID) },
		"ListActiveRuns":      func(d *DB) ([]*domain.Run, error) { return d.ListActiveRuns(ctx) },
	} {
		if got, err := list(db); err != nil || !slices.Equal(runIDs(got), []domain.RunID{live.ID}) {
			t.Errorf("%s = %v, %v; want only the live run", name, runIDs(got), err)
		}
		got, err := list(db.WithSpares())
		if err != nil || !slices.Equal(runIDs(got), []domain.RunID{live.ID, spare.ID}) || got[0].Spare || !got[1].Spare {
			t.Errorf("WithSpares().%s = %v, %v; want the live run, then the spare", name, runIDs(got), err)
		}
	}
	if got, err := db.ListLiveRunSummaries(ctx, w.ID, 100); err != nil || len(got) != 1 || got[0].ID != live.ID {
		t.Errorf("ListLiveRunSummaries = %d summaries, %v; want only the live run", len(got), err)
	}
	now := time.Now().UTC()
	day := Interval{Start: now.Add(-time.Hour), End: now.Add(time.Hour)}
	q := InsightsQuery{From: day.Start, To: day.End, Now: now, Buckets: []Interval{day}, Days: []Interval{day},
		Previous: Interval{Start: day.Start.Add(-2 * time.Hour), End: day.Start}}
	if insights, err := db.Insights(ctx, q); err != nil || insights.Totals.Launched != 1 || insights.Totals.Active != 1 || insights.Heatmap[0] != 1 ||
		len(insights.ByAgent) != 1 || insights.ByAgent[0].Launched != 1 || !slices.Equal(insights.NeedsYou, []domain.RunID{live.ID}) {
		t.Errorf("insights = %+v, %v; want only the live run", insights, err)
	}
	if brief, err := db.ReadMemoryBrief(ctx, MemoryBriefQuery{Workspace: w.ID, Member: ada.ID, Since: day.Start, Runs: 5}); err != nil || brief.LiveRuns != 1 {
		t.Errorf("memory brief = %+v, %v; want one live run", brief, err)
	}
	if got, err := db.WorkspaceMembers(ctx, []domain.WorkspaceID{w.ID}); err != nil || !slices.Equal(got, []domain.MemberID{ada.ID}) {
		t.Errorf("WorkspaceMembers = %v, %v; want Ada alone, not the spare's account", got, err)
	}
	if costs, _, err := db.ListWorkspaceCosts(ctx, w.ID); err != nil || len(costs) != 1 || costs[0].RunID != live.ID {
		t.Errorf("ListWorkspaceCosts = %d costs, %v; want only the live run's", len(costs), err)
	}
	if err := db.CheckWorkspaceDeletion(ctx, extra.ID); err != nil {
		t.Errorf("CheckWorkspaceDeletion(extra repository of the spare) = %v, want nil", err)
	}

	finished := time.Now().UTC()
	for _, id := range []domain.RunID{live.ID, spare.ID} {
		if stamp, err := db.UpdateRunStatus(ctx, id, domain.RunCompleted, "", "", nil, &finished); err != nil || stamp.StatusSeq != 4 {
			t.Fatalf("UpdateRunStatus(%s, completed) = %+v, %v; want status_seq 4", id, stamp, err)
		}
	}
	if got := runTransitions(t, db, spare.ID); len(got) != 0 {
		t.Errorf("spare transitions = %q, want none", got)
	}
	if got := runTransitions(t, db, live.ID); len(got) != 3 {
		t.Errorf("live run transitions = %q, want 3", got)
	}
	if got, err := db.FinishedRunsOf(ctx, ada.ID, bob.ID); err != nil || !slices.Equal(runIDs(got), []domain.RunID{live.ID}) {
		t.Errorf("FinishedRunsOf = %v, %v; want only the live run", runIDs(got), err)
	}
	if insights, err := db.Insights(ctx, q); err != nil || insights.Totals.Completed != 1 || len(insights.Recent) != 1 || insights.Success.RunsFinished != 1 {
		t.Errorf("insights after finishing = %+v, %v; want only the live run", insights, err)
	}

	if got, err := db.ListSpareRuns(ctx); err != nil || !slices.Equal(runIDs(got), []domain.RunID{spare.ID}) || !got[0].Spare {
		t.Errorf("ListSpareRuns = %v, %v; want the spare", runIDs(got), err)
	}
	if err := db.DeleteRun(ctx, spare.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.WithSpares().GetRun(ctx, spare.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("WithSpares().GetRun(deleted spare) = %v, want ErrNotFound", err)
	}
}

func TestClaimedSpareMatchesAColdLaunch(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	w := mustCreateWorkspace(t, db)
	ada, bob := mustCreateMember(t, db), mustCreateTeammate(t, db)
	launch := func() *domain.Run {
		return &domain.Run{WorkspaceID: w.ID, MemberID: ada.ID, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued,
			Permission: domain.PermissionRoot, Limits: domain.RunLimits{CPUs: 2}, BaseCommit: "abc123", BaseBranch: "main",
			Worktree: "/srv/checkouts/one", HarnessSessionID: "session-1",
			Task: "fix the auth bug", Branch: "cloudfield/run-fix-the-auth-bug", Model: "opus", Effort: "high", Via: "chatgpt",
			LinearIssueURL: "https://linear.app/acme/issue/ENG-1", RequestedAccountMemberID: bob.ID,
			Fallback: domain.AccountFallback{Order: []domain.MemberID{bob.ID}}, BaseSource: "mirror", BaseCheckedAt: time.Unix(1_800_000_000, 0).UTC()}
	}
	cold := launch()
	if err := db.CreateRun(ctx, cold); err != nil {
		t.Fatal(err)
	}
	toRunning(t, db, cold.ID, domain.ActivityWorking)

	spare := launch()
	spare.Spare, spare.Task, spare.Branch, spare.Model, spare.Effort, spare.Via, spare.LinearIssueURL = true, "", "cloudfield/spare", "", "", "", ""
	spare.RequestedAccountMemberID, spare.Fallback, spare.BaseSource, spare.BaseCheckedAt = "", domain.AccountFallback{Disabled: true}, "", time.Time{}
	if err := db.CreateRun(ctx, spare); err != nil {
		t.Fatal(err)
	}
	toRunning(t, db, spare.ID, domain.ActivityWorking)
	claim := launch()
	claim.ID = spare.ID
	if err := db.ClaimSpareRun(ctx, claim); err != nil {
		t.Fatal(err)
	}
	if claim.Status != domain.RunQueued || claim.CreatedAt.IsZero() || claim.StatusChangedAt == nil {
		t.Errorf("claimed run = %+v; want it queued and stamped", claim)
	}
	toRunning(t, db, spare.ID, domain.ActivityWorking)

	want, err := db.GetRun(ctx, cold.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.GetRun(ctx, spare.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []*domain.Run{want, got} {
		r.ID, r.CreatedAt, r.StartedAt, r.StatusChangedAt = "", time.Time{}, nil, nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("claimed spare = %+v\ncold launch   = %+v", got, want)
	}
	if got, want := runTransitions(t, db, spare.ID), runTransitions(t, db, cold.ID); !slices.Equal(got, want) {
		t.Errorf("claimed spare transitions = %q, cold launch's = %q", got, want)
	}
}

func TestClaimSpareRunNeedsASpare(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	w, ada := mustCreateWorkspace(t, db), mustCreateMember(t, db)
	normal := mustCreateRun(t, db, w.ID, ada.ID, domain.RunQueued)
	spare := &domain.Run{WorkspaceID: w.ID, MemberID: ada.ID, Harness: "claude", Mode: domain.LaunchTUI, Status: domain.RunQueued, Spare: true}
	if err := db.CreateRun(ctx, spare); err != nil {
		t.Fatal(err)
	}
	claim := func(id domain.RunID) error {
		return db.ClaimSpareRun(ctx, &domain.Run{ID: id, Mode: domain.LaunchTUI, Status: domain.RunQueued, Task: "claimed"})
	}
	for _, id := range []domain.RunID{normal.ID, "run-missing"} {
		if err := claim(id); !errors.Is(err, ErrNotFound) {
			t.Errorf("claim %s = %v, want ErrNotFound", id, err)
		}
	}
	if got, err := db.GetRun(ctx, normal.ID); err != nil || got.Task != normal.Task {
		t.Errorf("normal run after a claim = %+v, %v; want it unchanged", got, err)
	}
	if err := claim(spare.ID); err != nil {
		t.Fatal(err)
	}
	if err := claim(spare.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second claim = %v, want ErrNotFound", err)
	}
}

func toRunning(t *testing.T, db *DB, id domain.RunID, activity domain.RunActivity) {
	t.Helper()
	ctx := context.Background()
	started := time.Now().UTC()
	if _, err := db.UpdateRunStatus(ctx, id, domain.RunProvisioning, "", "", nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateRunStatus(ctx, id, domain.RunRunning, "", activity, &started, nil); err != nil {
		t.Fatal(err)
	}
}

func runTransitions(t *testing.T, db *DB, id domain.RunID) []string {
	t.Helper()
	rows, err := db.db.Query(`SELECT workspace_id || ' ' || from_status || ' -> ' || to_status || ' ' || reason || ' ' || activity
		FROM run_state_transitions WHERE run_id = ? ORDER BY rowid`, id)
	if err != nil {
		t.Fatal(err)
	}
	got, err := collect(rows, func(row interface{ Scan(...any) error }) (*string, error) {
		var s string
		return &s, row.Scan(&s)
	})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range got {
		out = append(out, *s)
	}
	return out
}

func runIDs(runs []*domain.Run) []domain.RunID {
	var ids []domain.RunID
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	return ids
}
