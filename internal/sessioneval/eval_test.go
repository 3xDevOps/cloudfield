//go:build sessioneval

package sessioneval

import (
	"cmp"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func envOr(key, def string) string {
	return cmp.Or(os.Getenv(key), def)
}

// TestSessionEval drives the real Claude Code agent through every scenario.
// It spends model tokens, so it never runs in CI; see docs/testing.md.
func TestSessionEval(t *testing.T) {
	scenarios, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	runs, err := strconv.Atoi(envOr("CLOUDFIELD_SESSION_EVAL_RUNS", "3"))
	if err != nil || runs < 1 {
		t.Fatalf("CLOUDFIELD_SESSION_EVAL_RUNS must be a positive number")
	}
	parallel, err := strconv.Atoi(envOr("CLOUDFIELD_SESSION_EVAL_PARALLEL", "4"))
	if err != nil || parallel < 1 {
		t.Fatalf("CLOUDFIELD_SESSION_EVAL_PARALLEL must be a positive number")
	}
	maxUSD, err := strconv.ParseFloat(envOr("CLOUDFIELD_SESSION_EVAL_MAX_USD", "5"), 64)
	if err != nil || maxUSD <= 0 {
		t.Fatalf("CLOUDFIELD_SESSION_EVAL_MAX_USD must be a positive number")
	}
	mode := Mode(envOr("CLOUDFIELD_SESSION_EVAL_MODE", string(ModeEnhanced)))
	if mode != ModeStandard && mode != ModeEnhanced {
		t.Fatalf("CLOUDFIELD_SESSION_EVAL_MODE must be %s or %s", ModeStandard, ModeEnhanced)
	}
	model := os.Getenv("CLOUDFIELD_SESSION_EVAL_MODEL")
	label := strings.NewReplacer("/", "-", " ", "-").Replace(envOr("CLOUDFIELD_SESSION_EVAL_LABEL", "unlabeled")) + "-" + string(mode)
	out := envOr("CLOUDFIELD_SESSION_EVAL_OUT", filepath.Join(os.TempDir(), "session-eval"))
	if err = os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	helper, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	picked := scenarios
	if only := os.Getenv("CLOUDFIELD_SESSION_EVAL_ONLY"); only != "" {
		picked = nil
		for id := range strings.SplitSeq(only, ",") {
			i := slices.IndexFunc(scenarios, func(s Scenario) bool { return s.ID == strings.TrimSpace(id) })
			if i < 0 {
				t.Fatalf("CLOUDFIELD_SESSION_EVAL_ONLY: unknown scenario %q", id)
			}
			picked = append(picked, scenarios[i])
		}
	}
	agent := Claude(model, maxUSD)
	var (
		mu      sync.Mutex
		results []Result
		wg      sync.WaitGroup
		slots   = make(chan struct{}, parallel)
	)
	write := func() string {
		used := cmp.Or(model, "default")
		if i := slices.IndexFunc(results, func(r Result) bool { return r.Model != "" }); i >= 0 {
			used = results[i].Model
		}
		md := Summary("real Claude Code (model "+used+"), "+string(mode)+" session, "+label+", "+strconv.Itoa(runs)+" sessions per scenario", picked, results)
		raw, _ := json.MarshalIndent(results, "", " ")
		if err := os.WriteFile(filepath.Join(out, label+".json"), raw, 0o600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(out, label+".md"), []byte(md), 0o600); err != nil {
			t.Error(err)
		}
		return md
	}
	deadline, hasDeadline := t.Deadline()
	for _, s := range picked {
		for n := 1; n <= runs; n++ {
			wg.Add(1)
			slots <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-slots }()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				if hasDeadline {
					var stop context.CancelFunc
					ctx, stop = context.WithDeadline(ctx, deadline.Add(-time.Minute))
					defer stop()
				}
				r := RunOnce(ctx, agent, helper, s, mode, n)
				mu.Lock()
				if err := os.WriteFile(filepath.Join(out, label+"-"+s.ID+"-"+strconv.Itoa(n)+".jsonl"), r.Transcript, 0o600); err != nil {
					t.Error(err)
				}
				if r.Error != "" {
					t.Errorf("%s run %d: %s", s.ID, n, r.Error)
				}
				results = append(results, r)
				write()
				mu.Unlock()
			}()
		}
	}
	wg.Wait()

	t.Logf("results written to %s\n%s", out, write())
}
