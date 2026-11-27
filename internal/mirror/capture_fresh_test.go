package mirror

import (
	"context"
	"errors"
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
	now = warmed.Add(captureFresh - time.Second)
	git.refreshErr = errors.New("upstream answered 502")
	if err = svc.Warm(ctx, "w"); err != nil || git.refreshCalls != 1 {
		t.Fatalf("a second launch's warm of a refresh the first can still start from = %v after %d refreshes, want none", err, git.refreshCalls)
	}
	git.refreshErr = nil
	capture(1, warmed)
	now = warmed.Add(captureFresh)
	if err = svc.Warm(ctx, "w"); err != nil || git.refreshCalls != 2 {
		t.Fatalf("a launch's warm of a refresh no capture starts from = %v after %d refreshes, want a second", err, git.refreshCalls)
	}
	launched := now
	now = now.Add(captureFresh / 2)
	git.refreshErr = errors.New("upstream answered 502")
	if err = svc.WarmAhead(ctx, "w"); err != nil || git.refreshCalls != 2 {
		t.Fatalf("warm ahead of a refresh a launch's warm accepted = %v after %d refreshes, want none until its capture", err, git.refreshCalls)
	}
	git.refreshErr = nil
	capture(2, launched)
	now = launched.Add(captureFresh)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = svc.WarmAhead(cancelled, "w"); err != nil || git.refreshCalls != 3 || git.refreshCtxErr != nil || !git.refreshBounded {
		t.Fatalf("warm ahead for a caller that left = %v after %d refreshes on a context with error %v, bounded %v; want a third that outlives the caller and has a deadline",
			err, git.refreshCalls, git.refreshCtxErr, git.refreshBounded)
	}
	now = now.Add(captureFresh / 2)
	if err = svc.WarmAhead(ctx, "w"); err != nil || git.refreshCalls != 4 {
		t.Fatalf("warm ahead in the second half of the window = %v after %d refreshes, want a fourth", err, git.refreshCalls)
	}
	capture(4, now)

	git.branchCommit = "1111111111111111111111111111111111111111"
	if _, err = svc.Capture(ctx, "w", ""); err != nil || git.refreshCalls != 5 {
		t.Fatalf("capture of a base that moved since the refresh = %v after %d refreshes, want a fifth", err, git.refreshCalls)
	}
	git.branchCommit = ""

	now = now.Add(captureFresh)
	capture(6, now)
}
