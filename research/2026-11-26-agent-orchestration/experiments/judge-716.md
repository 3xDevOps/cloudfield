# Verdict: review experiment 716 (commit a3171a811)

Lines are at a3171a811 unless marked `main`.

## 1. Known defect (#1038: `raise` re-read vs `decide` handoff gap)

- **A: MISS.** No finding covers the window between `decide`'s move to approved (card.go:346) and its `answer` (card.go:376) racing `raise`'s re-read (card.go:203-213). A#5 is about the live waiter's later claim write (confirm.go:110-124), which is a different path and a different failure.
- **B: MISS.** B lists "The pending re-check in `raise` after registering a wait" under "Checked and found sound".
- **C: PARTIAL.** C#4 targets the same reuse path in `raise`: "`raise` reuses any pending card with the same call and never looks at `ExpiresAt`". Its failure is a near-expiry wait, not the lost decision. C's "Considered and not reported" says it traced approve-during-reuse and found it sound.

## 2. Cluster table

| # | Issue | A# | B# | C# | Stated severity |
|---|---|---|---|---|---|
| 1 | Parked Stop approved after the run finished destroys the retained container | 1 | - | - | A blocker |
| 2 | Forget card's summary keeps the preference plaintext in `assistant_cards` | 2 | - | - | A blocker |
| 3 | env.save with nil params gives an empty `json.RawMessage` that breaks `assistant.status` marshaling | 3 | - | - | A blocker |
| 4 | Fallback-launch account names sit outside the compared summary | 4 | - | - | A should-fix |
| 5 | A failed live-path claim leaves the card approved, and it runs on restart | 5 | - | - | A should-fix |
| 6 | Reset during a server-run call: the call runs with no record and the card stays `running` (C adds held-action swapping between equal calls) | 6 | 3 | 3 | A should-fix, B minor, C should-fix |
| 7 | Declined card's note lands in the new conversation after a racing reset | 7 | - | - | A should-fix |
| 8 | An approval right after restart runs `runParked` without waiting for `RecoveryReady` | 8 | - | 2 | A should-fix, C should-fix |
| 9 | Web `ownerOf` gives a late outcome to another pending call | 9 | - | - | A should-fix |
| 10 | Uncertain PR creation is recorded as failed, so the panel offers Retry | 10 | - | - | A should-fix |
| 11 | Live data in summaries (checks, close timestamp, notice count, title) fails late approvals and dedupe | - | 1 | - | B should-fix |
| 12 | A failed reset withdraws the cards of a conversation that survives | - | 2 | - | B minor |
| 13 | A call told "expired" can still run later when the expiry write fails | - | 4 | - | B minor |
| 14 | Panel hides approved and running cards unless one is pending | - | 5 | 5 | B minor, C minor |
| 15 | Origin is the last prompt's, not the turn's (latent) | - | 6 | - | B minor |
| 16 | One undecodable card row breaks `assistant.status` | - | 7 | 6 | B minor, C minor |
| 17 | Crash between the Done record and `finishCard` makes a success show as Failed | - | - | 1 | C should-fix |
| 18 | A re-ask reuses a near-expiry or expired pending card | - | - | 4 | C minor |

## 3. Validation

Checked set: every blocker (A1-A3) plus each reviewer's top 3 (B1-B3, C1-C3). After dedup that is rows 1, 2, 3, 6, 8, 11, 12 and 17.

- **Row 1 (A1): REAL.** The run.kill summary carries only id, label and owner (internal/assistant/tools/tools.go:404-414). `runParked` checks only summary equality (card.go:541-545). `runKill` calls `Runs.Kill` (internal/sshd/handlers.go:414-415). On a terminal retained entry, `kill` calls `expireRetainedLocked` and then `dismissFinished` (internal/scheduler/steer.go:54-63). `expireRetainedLocked` destroys the container (internal/scheduler/supervise.go:507). **NOT-FIXED:** main card.go:602 and tools.go:413 are unchanged.
- **Row 2 (A2): REAL.** The forget summary embeds `pref.Text` (internal/assistant/tools/act.go:318), and `cardRow` stores it (card.go:252). The only delete of `assistant_cards` is the prune of finished cards older than the retention period, and it runs only inside `AddAssistantCard` for the same member (internal/store/assistant_cards.go:86-88). docs/privacy.md does not list this copy among what forgetting leaves behind. **NOT-FIXED:** main still has only that one DELETE.
- **Row 3 (A3): REAL.** `saveEnvironment` passes nil params (internal/ops/assistant.go:109). The socket client omits nil params (internal/coordtransport/client.go:134). `run` keeps `req.Params` nil through `statedReason`/`Prepare` (tools.go:735, 998) into the confirmation (tools.go:302). `cardRow` stores `""` (card.go:252), and `cardOf` sets `json.RawMessage("")` (card.go:264). I ran a scratch Go program outside the repo: that value is non-nil and `json.Marshal` fails with "unexpected end of JSON input". `sendStatus` then returns the error and the stream ends (internal/sshd/assistant.go:453-461). **NOT-FIXED:** the conversion at main card.go:303/315 is unchanged.
- **Row 6 (A6/B3/C3): REAL.** `runParked` writes `noteAction` on `conv`, then `hold(conv, …)`, then calls `srv.Call` without holding `m.op` (card.go:548-560). A reset can cancel `conv` in that window (assistant.go `resetLocked` → `endConv`). `take` drops held actions whose conv is cancelled (confirm.go:407). For a confirmed-tier, non-reversible method, `actionCaller.Call` then passes the call straight to the caller with no record (confirm.go:428-430; tiers at tools.go:60-111). `withdrawAll` skips running cards (card.go:444), so nothing finishes the card. **NOT-FIXED:** main confirm.go:447-449 and card.go:617-618 are unchanged.
- **Row 8 (A8/C2): REAL.** `decide` spawns `runParked(m, id, nil)` (card.go:377). `Resume` passes `RecoveryReady()` (card.go:577). `sched.Start`, which runs `recoverRuns` before closing `recoveryReady` (internal/scheduler/scheduler.go:715-729), runs concurrently with `ssh.Serve` (internal/server/server.go:734-746). In that window run.mode_switch, which is confirmed-tier (tools.go:89), finds no entry and errors (internal/scheduler/mode_switch.go:93-98). **NOT-FIXED:** main card.go:434 still passes nil.
- **Row 11 (B1): REAL.** These summaries embed live data: the merge summary has pending and failing check counts (act.go:189-197), the close summary has the close-check `ObservedAt` timestamp (act.go:108-110; internal/sshd/close_check.go:158-160), and the watch.delete summary has the notice count (act.go:285-290). A strict `fresh.Summary != c.Summary` check refuses the run (card.go:543-545). **NOT-FIXED:** main card.go:602 is the same; act.go only changed for the rename (#995).
- **Row 12 (B2): REAL.** `withdrawAll` runs before `DiscardAssistantState` and `ResetAssistantSession` (assistant.go:704-712). If either fails, the reset returns before `endConv` and the generation bump, so the cards stay withdrawn. **NOT-FIXED:** main assistant.go:738-746 keeps the same order.
- **Row 17 (C1): REAL.** `noteOutcome` writes the transcript record before `finishCard` (confirm.go:369-375). After a restart, `Resume` records Failed under the same action id, `row.ActionID`, which `claim` set to `a.ID` (card.go:467-468, 601-604). The web client keeps the latest record per action id (web/src/lib/assistant-actions.ts:403-407). The failure needs a crash after the Done record was persisted. **NOT-FIXED:** main confirm.go:388-394 is unchanged.

The known defect itself is **LATER-FIXED** in 82edc8324 (#1038): `decide` now takes `m.raising`.

## 4. Counts per reviewer

| Reviewer | Findings | Checked | REAL | NOT REAL | UNVERIFIED | Unique issues | Unique and REAL |
|---|---|---|---|---|---|---|---|
| A | 10 | 5 (A1, A2, A3, A6, A8) | 5 | 0 | 0 | 8 (rows 1, 2, 3, 4, 5, 7, 9, 10) | 3 (rows 1-3; the other 5 were not checked) |
| B | 7 | 3 (B1, B2, B3) | 3 | 0 | 0 | 4 (rows 11, 12, 13, 15) | 2 (rows 11, 12; 13 and 15 not checked) |
| C | 6 | 3 (C1, C2, C3) | 3 | 0 | 0 | 2 (rows 17, 18) | 1 (row 17; 18 not checked) |

Known defect: A MISS, B MISS, C PARTIAL (C#4, same `raise` reuse path, different failure).

## 5. What distinguishes the reviewers

A produced the most findings and the only blockers. Its findings go beyond the card state machine into privacy, serialization, scheduler side effects and web attribution, and all five I checked were real. B and C stayed inside the card lifecycle and overlap on several issues (rows 6, 14, 16). B's unique issue is that live data in summaries defeats late approval; C's is the crash-ordering record. All three traced the `raise`/`decide` concurrency, and none found the lost-decision gap: B and C explicitly judged that area sound.
