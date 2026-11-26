# Verdict: review experiment 547 (commit 175f1ad7c, #547)

All line numbers are at 175f1ad7c2594f76ba6bf0af7ef97dff0d741af4 unless a later commit is named.

## 1. Known defect (#1028, stale budget status read outside publishMu)

- **A: HIT.** A#1: "Budget status is read outside publishMu, so a stale state can be published last and desync `s.state`, which then suppresses later notices."
- **B: HIT.** B#2: "`publishMu` serializes publication, not the reads that decide what to publish."
- **C: HIT.** C#1: "Budget state is read outside `publishMu`, so stale states are still published out of order." (C also reproduced it with a scratch test.)

## 2. Cluster table

| # | Issue (short) | A# | B# | C# | Stated severity |
|---|---|---|---|---|---|
| 1 | Status read outside publishMu: a stale state is published last, `s.state` goes wrong, the next warn/cap is swallowed (known defect) | 1 | 2 | 1 | A should-fix; B should-fix; C should-fix |
| 2 | Cost `Start` now aborts server startup on any per-workspace budget/summary error, and does serial O(workspaces) scans | 2 | - | 4 | A should-fix; C minor |
| 3 | Edge-triggered notice lost for good: push drops the event, the server restarts during the quiet period, or the event lands before push subscribes; cost has already recorded the state (seeded at Start) and never re-emits it | 3 | 4 | 3 | A should-fix; B should-fix; C should-fix |
| 4 | Late out-of-order admin-lookup handoff after the watcher exits starts a fresh watcher (seq watermark gone), so a stale or duplicate warning is sent | 4 | 8 | - | A minor; B should-fix |
| 5 | Admin role checked when the event arrives, not at send time, so a demoted admin still gets the notice | 5 | 3 | 5 | A minor; B should-fix; C minor |
| 6 | Slack `Notice` retried after ambiguous success posts duplicate DMs | 6 | - | - | A minor |
| 7 | push wiring depends on svc_cost.go init running before svc_push.go (file-name order) | 7 | - | - | A minor |
| 8 | Startup scan widens the window for a buffer overflow before the first event; replay from `lastSeq`=0 re-applies pre-restart increments and double-counts spend | - | 1 | (4, aside) | B blocker |
| 9 | Failed event-log write after `s.state` is updated consumes the crossing | - | 5 | - | B should-fix |
| 10 | One send batch uses a payload decided once, so later devices/Slack get a cap notice after the cap is cleared | - | 6 | - | B should-fix |
| 11 | A pending warning replaced by a cap notice, then a cap-only raise (exceeded->warn, ignored as a fall), means neither notice is delivered | - | 7 | - | B should-fix |
| 12 | A device push failure (e.g. 503) sets `devices=false` and the alert is never retried on that device | - | 9 | - | B should-fix |
| 13 | Cold-open budget link shows workspace B while `activeWorkspace` stays A (launch dialog targets A) | - | 10 | - | B should-fix |
| 14 | Permanent Slack errors (channel_not_found, token_revoked, ...) on `Notice` retried every minute for as long as the budget stays exceeded | - | - | 2 | C should-fix |

C#4 cites the cluster 8 overflow and replay-from-0 path only as a side effect of a long boot. It is counted under cluster 2.

## 3. Validation

Checked: the one blocker (B#1), every reviewer's top 3 (A1-3, B1-3, C1-3), and, as time allowed, A4-A7 and B4-B9. B#10 was not checked.

| Cluster (findings) | Verdict | Evidence at 175f1ad7c | Later main |
|---|---|---|---|
| 1 (A1, B2, C1) | **REAL** | `internal/cost/service.go`: `status()` is called before any lock in SetBudget :369, Admit :386 and announce :453. `publish` :467-473 takes publishMu and then overwrites `s.state[ws]` with the caller's possibly stale `st.State`. announce :458-463 then sees no change, or publishes a fall, which push ignores at `internal/push/budget.go:40`. | **LATER-FIXED** feda56d28 (#1028) |
| 2 (A2, C4) | **REAL** | `internal/cost/service.go:83-91`: any `status()` error returns from Start. `internal/server/services.go:103-104` aborts startServices on any Start error. | **NOT-FIXED** (Start is identical on origin/main) |
| 3 (A3, B4, C3) | **REAL** (drop and restart paths traced; C3's pre-subscribe window is plausible but not traced: service order is cost before push, by init order) | push consume never checks `Dropped()` and never replays (`internal/push/service.go:229-248`, subscribe without Replay at :190). Pending notices live only in the in-memory `s.notices` map (`internal/push/budget.go:78-95`). Cost seeds `s.state` from the DB at Start (`internal/cost/service.go:83-94`), so after a restart no rise is re-published. | **NOT-FIXED** (origin/main push consume still has no drop or replay handling) |
| 4 (A4, B8) | **REAL** | `internal/push/budget.go:44-60`: each event's lookup goroutine retries independently. `unwatchBudget` :136 deletes the watcher and its seq watermark. A late `due()` :81-86 starts a new watcher whose `due.seq`=0 accepts the old seq :121-123. `sendBudget` :149 sends whenever the current state equals that state. | **NOT-FIXED** (due/watchBudget unchanged on main) |
| 5 (A5, B3, C5) | **REAL** | The role filter runs only at `internal/push/budget.go:49`. `sendBudget` :143-180 re-reads the budget but not the member. | **LATER-FIXED** 5cbb20ca7 (#837): sendBudget re-checks `workspaceAdmins` before sending |
| 6 (A6) | **REAL** (needs Slack to accept the post and then fail the response) | `internal/slack/notify.go:157` returns any post error. `internal/push/budget.go:177-178` reports not done and :108-110 retries, posting a new message each time. | **NOT-FIXED** |
| 7 (A7) | **REAL** as a latent dependency, with no failure today | `internal/server/svc_push.go:17-18,35` reads `d.SSH.Services.Costs` set by `svc_cost.go:17`. `Costs` is an interface (`internal/sshd/services.go:18`), so a missing cost builder makes `push.New` fail loudly (`internal/push/service.go:154`). | **NOT-FIXED** |
| 8 (B1, blocker) | **REAL** in mechanism. The trigger needs 257 or more matching events during the Start scan, which was not traced. The hazard existed before this change; the change widens it. | `inproc.go:254-257` drops the oldest event past 256, and `out` is unbuffered (:151). `cost/service.go:157-160` sees `Dropped()>0` before handling the first event. :135-138 resubscribes with `AfterSeq=lastSeq`=0. inproc replay reads the whole log. `record` :254-257 adds a replayed `Increment` (published by `scheduler/acp_driver.go:815`) onto the stored row. | **NOT-FIXED** (`lastSeq` is still not seeded on main) |
| 9 (B5) | **REAL** | `internal/cost/service.go:470-479`: `s.state` is written before `bus.Publish`, and an error is only logged. | **NOT-FIXED** |
| 10 (B6) | **REAL** | `internal/push/budget.go:156-177`: the notice is built once and sent to every subscription, then to the sink, with no re-read between sends. | **NOT-FIXED** |
| 11 (B7) | **REAL** | A higher seq replaces the pending warn (`budget.go:121-123`). exceeded->warn is dropped as a fall (:40-41). sendBudget finds warn != exceeded and returns done (:149-150). | **NOT-FIXED** |
| 12 (B9) | **REAL** | `internal/push/budget.go:170-175`: a send error is logged, then `*devices=false`. The Slack `Notice` returns nil with no link (`notify.go:137-138`), so the watcher finishes. | **NOT-FIXED** |
| 13 (B10) | **UNVERIFIED** (not checked in the time box) | - | UNKNOWN |
| 14 (C2) | **REAL** | `notify.go:157` returns raw `apiError` codes, and `Notice` does not use `lost()` (`api.go:108-118`). `budget.go:108-110` retries every `retry` while `sendBudget` keeps finding the state standing (:149). | **NOT-FIXED** (Notice unchanged on main) |

## 4. Counts per reviewer

| Reviewer | Findings | Checked | REAL | NOT REAL | UNVERIFIED | Unique issues | Unique REAL |
|---|---|---|---|---|---|---|---|
| A | 7 | 7 | 7 | 0 | 0 | 2 (A6, A7) | 2 |
| B | 10 | 9 | 9 | 0 | 1 (B10, unchecked) | 6 (B1, B5, B6, B7, B9, B10) | 5 |
| C | 5 | 5 | 5 | 0 | 0 | 1 (C2) | 1 |

## 5. What distinguishes the reviewers

All three found the known defect. C alone reproduced it with a running test, and also found the unbounded Slack retry loop on a permanent error. B covered the most ground: many specific delivery-ordering interleavings, a replay double-count it rated blocker (a pre-existing hazard the change widens), and the only dashboard finding. A was the fastest (about 2.5 minutes) and surfaced the same top issues as the others, plus two low-impact uniques: duplicate DMs on retry, and init-order wiring.
