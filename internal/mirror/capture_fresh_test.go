package mirror

import (
	"context"
	"testing"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
)

func TestCaptureStartsFromARefreshYoungerThanCaptureFresh(t *testing.T) {
	git := &mirrorTestGit{}
	now := time.Date(2026, time.November, 26, 0, 0, 0, 0, time.UTC)
	svc, err := New(Config{Root: t.TempDir(), Store: newMirrorTestStore(), Git: git, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err = svc.Configure(ctx, "w", ConfigureRequest{SourceURL: "https://example.test/acme/repo", Branch: "main", Auth: domain.MirrorAuthPublic}); err != nil {
		t.Fatal(err)
	}
	capture := func(wantRefreshes int, wantChecked time.Time) {
		t.Helper()
		base, captureErr := svc.Capture(ctx, "w", "")
		if captureErr != nil || base.Commit != testSHA || base.Cached || !base.CheckedAt.Equal(wantChecked) || git.refreshCalls != wantRefreshes {
			t.Fatalf("capture = %+v, %v after %d refreshes; want checked %s after %d", base, captureErr, git.refreshCalls, wantChecked, wantRefreshes)
		}
	}
	if err = svc.Warm(ctx, "w"); err != nil || git.refreshCalls != 1 {
		t.Fatalf("warm = %v after %d refreshes, want one", err, git.refreshCalls)
	}
	warmed := now
	capture(1, warmed)
	now = now.Add(captureFresh/2 - time.Second)
	if err = svc.WarmAhead(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	capture(1, warmed)
	now = now.Add(captureFresh/2 - time.Second)
	if err = svc.Warm(ctx, "w"); err != nil {
		t.Fatal(err)
	}
	capture(1, warmed)
	now = now.Add(time.Second)
	if err = svc.WarmAhead(ctx, "w"); err != nil || git.refreshCalls != 2 {
		t.Fatalf("warm ahead in the second half of the window = %v after %d refreshes, want a second", err, git.refreshCalls)
	}
	capture(2, now)

	git.branchCommit = "1111111111111111111111111111111111111111"
	if _, err = svc.Capture(ctx, "w", ""); err != nil || git.refreshCalls != 3 {
		t.Fatalf("capture of a base that moved since the refresh = %v after %d refreshes, want a third", err, git.refreshCalls)
	}
	git.branchCommit = ""

	now = now.Add(captureFresh)
	capture(4, now)
}
