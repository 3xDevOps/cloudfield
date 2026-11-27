//go:build perfseed

// Command seed fills a Cloudfield server's store with one large, invented,
// reproducible data set for dashboard performance measurements.
//
// Run it against the data directory of a stopped server that already has an
// admin member and its workspaces (measure/world.ts makes both):
//
//	go run -tags perfseed ./research/2026-11-26-dashboard-performance/seed --data-dir <dir>
//
// Everything but the clock is fixed by --seed: session times are offsets back
// from the moment the tool runs, so the dashboard's time windows hold the
// same sessions and events on every run.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"sort"
	"time"

	"github.com/3xDevOps/cloudfield/internal/domain"
	"github.com/3xDevOps/cloudfield/internal/events"
	"github.com/3xDevOps/cloudfield/internal/server"
	"github.com/3xDevOps/cloudfield/internal/store"
)

type pending struct {
	t time.Time
	e events.Event
}

func main() {
	dataDir := flag.String("data-dir", "", "the stopped server's data directory")
	seed := flag.Uint64("seed", 20261126, "random seed")
	days := flag.Int("days", 21, "days of history")
	sessions := flag.Int("sessions", 600, "sessions across all workspaces")
	live := flag.Int("live", 24, "sessions still running now, all in the busiest workspace")
	rereadEvery := flag.Duration("reread-every", time.Minute, "how often a waiting session's wait is read again (0: never)")
	flag.Parse()
	if err := run(*dataDir, *seed, *days, *sessions, *live, *rereadEvery); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
}

func run(dataDir string, seed uint64, days, sessions, live int, rereadEvery time.Duration) error {
	ctx := context.Background()
	db, err := store.Open(server.StorePath(dataDir))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	log, err := events.OpenSQLiteLog(server.StorePath(dataDir))
	if err != nil {
		return err
	}
	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		return err
	}
	members, err := db.ListMembers(ctx)
	if err != nil {
		return err
	}
	if len(workspaces) == 0 || len(members) == 0 {
		return fmt.Errorf("the store needs at least one workspace and one member")
	}
	sort.Slice(workspaces, func(i, j int) bool { return workspaces[i].Name < workspaces[j].Name })
	head, err := log.LastSeq(ctx)
	if err != nil {
		return err
	}

	rng := rand.New(rand.NewPCG(seed, seed))
	now := time.Now().UTC().Truncate(time.Second)
	span := time.Duration(days) * 24 * time.Hour
	var queue []pending
	emit := func(t time.Time, ws domain.WorkspaceID, run domain.RunID, p events.Payload) {
		queue = append(queue, pending{t, events.Event{Time: t, WorkspaceID: ws, RunID: run, Payload: p}})
	}

	type made struct {
		id         domain.RunID
		ws         domain.WorkspaceID
		start, end time.Time
	}
	var all []made
	msg := 0
	for i := range sessions {
		// The first workspace by name is the busy one: 60% of sessions.
		ws := workspaces[0]
		if len(workspaces) > 1 && rng.Float64() >= 0.6 {
			ws = workspaces[1+rng.IntN(len(workspaces)-1)]
		}
		isLive := i < live
		if isLive {
			ws = workspaces[0]
		}
		// Half of all sessions started in the last three days.
		age := time.Duration(rng.Float64() * float64(span))
		if rng.Float64() < 0.5 {
			age = time.Duration(rng.Float64() * float64(72*time.Hour))
		}
		length := 20*time.Minute + time.Duration(rng.ExpFloat64()*float64(3*time.Hour))
		if length > 30*time.Hour {
			length = 30 * time.Hour
		}
		if isLive {
			age = 30*time.Minute + time.Duration(rng.Float64()*float64(18*time.Hour))
			length = age
		} else if length > age {
			age = length + time.Minute
		}
		created := now.Add(-age)
		started := created.Add(20 * time.Second)
		end := created.Add(length)
		id := domain.RunID(fmt.Sprintf("run-seed%05d", i))
		status := domain.RunRunning
		var finished *time.Time
		if !isLive {
			switch r := rng.Float64(); {
			case r < 0.7:
				status = domain.RunCompleted
			case r < 0.85:
				status = domain.RunFailed
			default:
				status = domain.RunAbandoned
			}
			finished = &end
		}
		r := &domain.Run{
			ID: id, WorkspaceID: ws.ID, MemberID: members[rng.IntN(len(members))].ID,
			Task: fmt.Sprintf("Seeded task %d: change the %s module", i, []string{"checkout", "search", "billing", "import", "report"}[rng.IntN(5)]),
			Title:   fmt.Sprintf("Seeded session %d", i),
			Harness: "claude", Mode: domain.LaunchTUI, Status: status,
			Branch: fmt.Sprintf("cloudfield/seed-%05d", i), Worktree: "",
			CreatedAt: created, StartedAt: &started, FinishedAt: finished,
		}
		if err := db.CreateRunWithID(ctx, r); err != nil {
			return fmt.Errorf("session %d: %w", i, err)
		}
		all = append(all, made{id, ws.ID, created, end})

		seq := int64(0)
		statusEvent := func(t time.Time, from, to domain.RunStatus, activity domain.RunActivity, wait *domain.RunWait, reread bool) {
			seq++
			emit(t, ws.ID, id, events.RunStatusPayload{From: from, To: to, Activity: activity, StatusSeq: seq, Cause: events.CauseTurn, Wait: wait, Reread: reread})
		}
		statusEvent(created, domain.RunQueued, domain.RunProvisioning, "", nil, false)
		statusEvent(started, domain.RunProvisioning, domain.RunRunning, domain.ActivityWorking, nil, false)
		// Turns: work for a while, then wait (on CI, on agents, or on a person).
		at := started
		for {
			at = at.Add(2*time.Minute + time.Duration(rng.ExpFloat64()*float64(12*time.Minute)))
			if !at.Before(end) {
				break
			}
			waitFor := time.Minute + time.Duration(rng.ExpFloat64()*float64(25*time.Minute))
			until := at.Add(waitFor)
			if until.After(end) {
				until = end
			}
			switch r := rng.Float64(); {
			case r < 0.2:
				statusEvent(at, domain.RunRunning, domain.RunNeedsAttention, domain.ActivityNeedsYou, nil, false)
				statusEvent(until, domain.RunNeedsAttention, domain.RunRunning, domain.ActivityWorking, nil, false)
			default:
				activity := domain.ActivityWaitingCI
				if r < 0.6 {
					activity = domain.ActivityWaitingAgents
				}
				verified := at
				wait := &domain.RunWait{Kind: domain.WaitCondition, Detail: "pull request checks", VerifiedAt: &verified}
				statusEvent(at, domain.RunRunning, domain.RunRunning, activity, wait, false)
				if rereadEvery > 0 {
					for t := at.Add(rereadEvery); t.Before(until); t = t.Add(rereadEvery) {
						read := t
						statusEvent(t, domain.RunRunning, domain.RunRunning, activity, &domain.RunWait{Kind: domain.WaitCondition, Detail: "pull request checks", VerifiedAt: &read}, true)
					}
				}
				statusEvent(until, domain.RunRunning, domain.RunRunning, domain.ActivityWorking, nil, false)
			}
			at = until
		}
		if finished != nil {
			statusEvent(end, domain.RunRunning, status, domain.ActivityFinished, nil, false)
		}
	}

	// Agent mail: sessions of one workspace that overlap in time write to each other.
	for i, a := range all {
		for j := i + 1; j < len(all) && j < i+12; j++ {
			b := all[j]
			if a.ws != b.ws {
				continue
			}
			from, to := a.start, a.end
			if b.start.After(from) {
				from = b.start
			}
			if b.end.Before(to) {
				to = b.end
			}
			if !to.After(from) || rng.Float64() < 0.5 {
				continue
			}
			for t := from.Add(time.Duration(rng.ExpFloat64() * float64(20*time.Minute))); t.Before(to); t = t.Add(time.Duration(rng.ExpFloat64() * float64(40*time.Minute))) {
				msg++
				sender, receiver := a.id, b.id
				if rng.Float64() < 0.5 {
					sender, receiver = b.id, a.id
				}
				mid := fmt.Sprintf("msg-seed%07d", msg)
				emit(t, a.ws, sender, events.CoordMessagePayload{MessageID: mid, WorkspaceID: a.ws, FromRunID: sender, ToRunID: receiver, Kind: "message"})
				ack := t.Add(time.Duration(rng.ExpFloat64() * float64(3*time.Minute)))
				if ack.Before(to) {
					emit(ack, a.ws, receiver, events.CoordMessageAckedPayload{MessageID: mid, ToRunID: receiver, AckedAt: ack.Format(time.RFC3339Nano)})
				}
			}
		}
	}

	sort.SliceStable(queue, func(i, j int) bool { return queue[i].t.Before(queue[j].t) })
	counts := map[string]int{}
	for i, p := range queue {
		p.e.Seq = head + uint64(i) + 1
		p.e.ID = fmt.Sprintf("evt-seed%08d", i)
		p.e.Type = p.e.Payload.EventType()
		if err := log.Append(ctx, p.e); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
		kind := string(p.e.Payload.EventType())
		if s, ok := p.e.Payload.(events.RunStatusPayload); ok && s.Reread {
			kind += " (reread)"
		}
		counts[kind]++
		for _, w := range []struct {
			name string
			d    time.Duration
		}{{"1h", time.Hour}, {"6h", 6 * time.Hour}, {"24h", 24 * time.Hour}, {"7d", 7 * 24 * time.Hour}} {
			if p.e.WorkspaceID == workspaces[0].ID && now.Sub(p.t) <= w.d {
				counts["busy workspace, last "+w.name+": "+kind]++
			}
		}
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("sessions %d (live %d) in %d workspaces, events %d, first seq %d\n", sessions, live, len(workspaces), len(queue), head+1)
	for _, k := range keys {
		fmt.Printf("%8d  %s\n", counts[k], k)
	}
	return nil
}
