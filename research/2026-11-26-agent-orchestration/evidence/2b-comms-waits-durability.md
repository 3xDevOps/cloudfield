# 2b: Comms, waits and durability between parent and child sessions

Sources: this repo at `81d14a23b`. CLI help comes from running `/usr/local/bin/aether-internal <cmd> --help` (read-only). Tags: DESIGN-FACT (code or doc says so), ADVICE (doc or help text recommends it), OBSERVED-ONCE, MEASURED.

## 1. Mail: send / ask / reply / inbox

| Claim | Exact value / quote | Source | Tag |
|---|---|---|---|
| Body cap for send, ask and reply. The server refuses a longer body with an error. It does not truncate | `CoordMaxBodyBytes = 4 << 10`; `"body exceeds %d bytes"` | internal/protocol/coord.go:37; internal/coord/mailbox.go:338-340, cli.go:714 | DESIGN-FACT |
| Inbox depth cap. Sends past it get an error, not a silent drop | `CoordMaxUnread = 100`; `ErrInboxFull` "Nothing is ever dropped silently"; `"%s: session %s inbox is full (%d unacknowledged messages)"` | protocol/coord.go:40; store/coord.go:15-18; mailbox.go:1275-1278 | DESIGN-FACT |
| Batch size: the oldest messages that fit in 16 KiB, always at least one | `RunMessageBatchBytes = 16 << 10`, plus 120 bytes overhead per message | store/coord.go:612-625 | DESIGN-FACT |
| Delivery order and guarantee | "Delivery is at least once"; oldest-first; "The batch is frozen until acknowledged" | docs/coordination.md:264-278 | DESIGN-FACT |
| Acked mail is gone for the agent | "acknowledged mail cannot be read again from inside the session"; the help text says "never pipe it through head, tail or a count". Members can still read it through Activity > Agent messages / `coord.messages.list` | coordination.md:1128-1130, 444-450; `inbox --help` | DESIGN-FACT |
| The CLI acks implicitly: the next `inbox` call sends the saved token | `$TMPDIR/aether-inbox-<session>-ack` | coordination.md:279-284 | DESIGN-FACT |
| Send rate limit | burst 5, then 1 per 5 s; error `rate limit exceeded (burst 5, 1 message per 5s)` (CodeConflict) | mailbox.go:25-27, 1233-1238 | DESIGN-FACT |
| Inbox read rate limit | burst 10, then 1 per second; a refused read saves no token | mailbox.go:34-36, 410-414; coordination.md:232-234 | DESIGN-FACT |
| Transport budget, per session | 30 per burst, refilling at 1 per second. Separate buckets for hook, lifecycle (`run.report`) and "turn state". Over budget returns an error: `coord: %s request rate limit exceeded` (Unavailable, or Conflict for transport) | mailbox.go:42-44, 225-249 | DESIGN-FACT |
| An over-budget lifecycle `run.report` fails without blocking the agent, so a turn-end signal can be lost | "A lifecycle callback may fail without blocking the agent; the session then falls back to its normal stall handling" | coordination.md:1593-1595 | DESIGN-FACT |
| Peer cap: none in current code | "A session has no limit on how many distinct sessions it messages". Commit ee1e46ce9 (#1108, 2026-11-24): "Docs no longer claim a peer limit, an 8-correspondent cap ... none of which the code has" | coordination.md:219; `git show ee1e46ce9` | DESIGN-FACT |
| Long poll limit | `inbox --wait` at most 30 s (`CoordMaxInboxWaitSeconds = 30`) | protocol/coord.go:42 | DESIGN-FACT |
| Mail held while a member controls a subsession | `recipient_status` / `last_sent.state` = `held`, delivered on release; "Do not resend it" | coordination.md:1350-1353 | DESIGN-FACT |
| An owed wake is retried | "at least every 30 seconds, until a turn starts or the mail is acknowledged"; `wakeSweepInterval = 30 * time.Second`; Enhanced wake waits up to 20 s for the control lock (`enhancedWakeTimeout`) | coordination.md:369-372; coord/acp_wake.go:17,21 | DESIGN-FACT |
| An Enhanced session hears about mail only between turns | "Mail that arrives during a turn waits until the turn ends" | enhanced-sessions.md:303-304 | DESIGN-FACT |

## 2. Reports

| Claim | Exact value / quote | Source | Tag |
|---|---|---|---|
| Outcomes | success, failure, blocked, needs-input, waiting, answered | mailbox.go:542-546 | DESIGN-FACT |
| Durable summary cap. A longer summary is refused, not cut | `CoordMaxSummaryBytes = 4 << 10`; `"summary exceeds %d bytes"` | protocol/coord.go:46; mailbox.go:561-565 | DESIGN-FACT |
| The stored summary is flattened to one line | `cleanCoordText` turns control characters into spaces, then `strings.Join(strings.Fields(text), " ")`, so newlines and indentation are lost | mailbox.go:1424-1440 | DESIGN-FACT |
| Status-reason cap of 256 runes, applied before storage, for blocked, waiting and answered | `maxReportReason = 256`; `maxPublicRunStatusReason = 256`; needs-input is exempt ("shows whole") | coord/report.go:22, 322-328; scheduler/state.go:62; scheduler/reported.go:467-470 | DESIGN-FACT |
| "Cut near 300 chars" refuted for reports | The durable report and the parent's inbox copy are both whole up to 4 KiB (`Body: report.Summary`, mailbox.go:826). The cuts that exist: 256 runes on the status reason (stored on the run), 512 chars in the dashboard activity line (display only; `bounded(p.summary, 512)`, web/src/components/event-words.tsx:130), 3000 bytes on summaries quoted in child idle/ended notices (`maxNoticeSummary = 3000`, children/dispatch.go:158), and 300 runes on third-party `detail` quoted in wait wakes (`maxUntrusted = 300`, watch/wait_pr.go:193). The last one is probably the source of "300" | as cited | DESIGN-FACT |
| Child success, failure or blocked goes to the parent inbox as `kind: report` with `outcome`, and wakes it | `enqueueChildReport` → `appendRunMessage` → `wakeInbox`; kind tag at mailbox.go:491-498; if the inbox is full "the report outbox retries" | mailbox.go:801-840 | DESIGN-FACT |
| Child needs-input reaches the parent as a notice, and wakes it | `"Subsession <id> asks its owner: <q>\nIf you can answer within its task, reply with aether-internal send --to <id>."`. The error is discarded: `_ = s.notify(...)` | children/coord.go:90-97 | DESIGN-FACT |
| Child **answered**, or a turn with no report: the child's last message goes to the parent as a provisional report after 15 s | `provisionalGrace = 15 * time.Second`; "a waiting outcome keeps it back; an answered one does not"; body is cut to 4 KiB minus the header | scheduler/provisional.go:14-46; coord/provisional.go:15-66 | DESIGN-FACT |
| Child **waiting** does NOT wake the parent | No forward (provisional.go:36 `!entry.waits() || entry.answered()`). The parent hears only after the 1 h quiet-spell notice (`idleNoticeAfter = time.Hour`) | children/dispatch.go:291, 307-313 | DESIGN-FACT |
| Docs are incomplete about the answered forward | coordination.md:1328-1331 mentions only "A turn that ends without a report" | vs scheduler/provisional.go:18-20 | DESIGN-FACT |
| A child stopped on an error tells its parent after 1 min | `stoppedNoticeAfter = time.Minute`: disconnected, a failed turn, interrupted, usage limit, `wait broken:` | dispatch.go:295-330 | DESIGN-FACT |
| Success and failure are refused while mail is unread (blocked is exempt). The refusal is one-shot: any later `coord.inbox` call lets the next report through | `"coord.report: %d unread %s (from %s). Run aether-internal inbox, handle them, then report again."` (-32002) | coord/report.go:345-369; mailbox.go:613-619 | DESIGN-FACT |
| Success is refused while subsessions are unfinished (failure is allowed) | `"coord.report: %d subsessions are unfinished (aether-internal subsession list); wait for their reports and report waiting --for agents, or stop them with subsession cancel"` | children/coord.go:73-86 | DESIGN-FACT |
| The Stop hook holds a turn that owes a report once | Claude Code `{"decision":"block","reason":"Report your state with aether_report ..."}`; the next stop is applied; the server ends the turn itself after 1 min of silence | coordination.md:1614-1628 | DESIGN-FACT |

## 3. Waits (`report waiting --until`)

| Claim | Exact value / quote | Source | Tag |
|---|---|---|---|
| Grammar, at most 4 conditions | `pr:<owner>/<repo>#<n>:merged, pr:<owner>/<repo>#<n>:checks[@<head-sha>] or run:<run-id>:finished`; `CoordMaxUntil = 4` | protocol/wait.go:13,36 | DESIGN-FACT |
| `merged` resolves on any of three outcomes, so it does not sleep on | merged → `merged`; `snap.State == "closed"` → `closed`; a merge-queue removal after the wait began that stands for `queueSettle = time.Minute` → `removed-from-queue`, detail `reason: <GitHub reason>` such as `failed_checks`. Next step: "fix it, queue it again and wait again" | watch/wait_pr.go:79-104, 190, 250-251; watches.md:429 | DESIGN-FACT |
| `merged` gap 1: a merge-queue read error does not resolve the wait, only annotates it | `"; can't read its merge queue (...)"`. The PR read still succeeds, so the source counts as read, and the wait sleeps until merge, close or 7-day expiry | wait_pr.go:91-94 | DESIGN-FACT |
| `merged` gap 2: a removal before the wait was registered is ignored | "A removal before the wait began is the state it waits from" | wait_pr.go:95-96 | DESIGN-FACT |
| `checks` outcomes | passed, failed (once nothing counted is pending), cancelled, superseded (a named SHA was replaced; 5 min grace `namedHeadGrace`), closed | wait_pr.go:106-150, 184 | DESIGN-FACT |
| `run:<id>:finished` | Resolves on merged, abandoned, failed, interrupted, completed, `reported-success` (live + Activity finished), or `reported-failure`. Same workspace only. Not on waiting or answered | watch/wait.go:399-418, 161-184 | DESIGN-FACT |
| Polling | PR every 1 min while checks are pending, every 5 min otherwise (`PRPollBusy`/`PRPollIdle`); runs on each event and every 10 min; failed reads back off from 30 s to at most 15 min | watch/pr_source.go:27-28; wait.go:27; evaluate.go:174-180; service.go:40 | DESIGN-FACT |
| Expiry | `WaitExpiry = 7 * 24 * time.Hour`. Wakes with "nothing resolved in time" | wait.go:24, 235 | DESIGN-FACT |
| Already resolved at report time | Read inline for up to `SettleBound = 10 * time.Second`; next action: `"do not end your turn waiting: ... act on it now, as no wake follows"` | wait.go:94; coord/report.go:196-209 | DESIGN-FACT |
| Rate cap | `MaxWaitReportsPerHour = 30`, refused with an error | store/watch_run.go:133-137 | DESIGN-FACT |
| Broken-wait detection makes the session Needs you | `wakeGrace = 5 * time.Minute` (resolved but not delivered); `unreadBound = time.Hour` (source unread) | scheduler/wait.go:47-53; failure-handling.md:91-93 | DESIGN-FACT |
| A protected session is never woken by mail or wait wakes | Wake admission refuses `current.Protected`: `"coord wake: session is not an eligible live interactive session"`. The wake message is still written to the inbox (delivered), so the wait does not count as broken | server/svc_coord_wake.go:48-51; help: "mail never wakes a protected session, so wait in your turn" | DESIGN-FACT + ADVICE |
| Report waiting only where mail resumes an idle agent | Enhanced, managed Claude Code, native pi/omp/OpenCode; elsewhere use `inbox --wait 30` | `report --help`; coordination.md:1497-1501 | ADVICE |
| An unconditioned wait goes stale | After 3× `--stall-threshold` (30 min default): `no update for <duration>: <summary>` | failure-handling.md:69-76 | DESIGN-FACT |
| Waits survive a restart | "a server that stops before reading them reads them when it starts"; delivery is keyed so it never wakes twice | watches.md:464-466, 486-488 | DESIGN-FACT |

## 4. Durability

| Event | What happens | Source | Tag |
|---|---|---|---|
| Server restart | Socket, unread mailbox, ack tokens and timeline are kept. The client retries for 60 s (100 ms doubling to 2 s). The Enhanced agent keeps running under `acp-keeper` and is adopted. Report publication goes through a durable outbox | coordination.md:274-275, 299-307, 1611-1612, 1636-1637; enhanced-sessions.md:349-361 | DESIGN-FACT |
| Host reboot, or Docker restart without live-restore | `interrupted`; "Commits the partial work as `wip: <task>`, publishes the session branch"; cannot be reopened | failure-handling.md:18 | DESIGN-FACT |
| Parent dies (failed, killed, interrupted, deleted) | Working children are killed and queued ones cancelled, "and so is a subsession that already reported its result". A kill commits `wip:` and publishes the branch (`finalize` → `commitAll` + `PublishRunBranch`) | coordination.md:1362-1365; children/dispatch.go:225-238; scheduler/steer.go:23-25, supervise.go:185-196 | DESIGN-FACT |
| Parent's agent dies but the session stays live (Enhanced `disconnected`, Standard agent exits to a shell) | The child keeps running. Its report sits unread: "messages wait unread until a person presses **Retry Enhanced**" | dispatch.go:320-321; failure-handling.md:25 | DESIGN-FACT |
| Unpushed child work | `subsession fetch` publishes the child's HEAD (committed only; `publishRunBranch` fetches the branch ref and does not commit) to `refs/remotes/children/<run-id>`. For a finished child it uses the branch published at its end. A handoff detaches the child, after which fetch is refused | children/agent.go:332-353; gitengine/checkout.go:405-433; coordination.md:1314-1319 | DESIGN-FACT |
| Subsession start | "uncommitted changes are not included" | `subsession --help`; coordination.md:1301-1302 | DESIGN-FACT |
| A VM dies before its final commit | Boots the VM without the agent to commit and publish; retries 15 min doubling to 6 h. Once the VM is gone: `work could not be captured: ...; the checkout's changes since the branch's last published commit are lost` | failure-handling.md:1004-1020; scheduler/reported.go:36 | DESIGN-FACT |
| `vm runtime: <action> VM <key>: it is not running` | Returned by `requireRunning` for resume, exec, attach, save, address, browser and disks when the unit is inactive or `rec.Exited` | runtime/vm/lifecycle.go:49-61 | DESIGN-FACT |
| Sleep | Enhanced Claude Code container after 30 min (`docker stop`, so processes, `/loop` and `ScheduleWakeup` are lost); a VM after 5 min (full save, nothing lost). Mail and `--until` wakes wake it | failure-handling.md:22-23, 816-827, 894-896 | DESIGN-FACT |
| Wake latency | Container wake accepted "about 1.6 seconds ... up to 23 seconds on a loaded one"; VM 0.7-1.5 s on a test host | failure-handling.md:811-814, 909-912 | MEASURED (doc-reported) |
| Stall | `--stall-threshold` 10 min; Enhanced `--first-event-grace` 3 min, `--stream-quiet-grace` 5 min; "Cloudfield restarts nothing on its own" | failure-handling.md:1123-1125, 1178 | DESIGN-FACT |
| DB durability | "a power loss or kernel crash can drop the last few" writes | failure-handling.md:1201-1203 | DESIGN-FACT |
| Mail of a destroyed sandbox | "retires the session's unread mail" (`last_sent.state = retired`) | coordination.md:1645-1648, 192-194 | DESIGN-FACT |

## 5. Team memory

| Claim | Value | Source | Tag |
|---|---|---|---|
| Kinds | decision, convention, gotcha, preference; `outcome` is server-written on success or failure | memory.md:29, 121-125 | DESIGN-FACT |
| Size caps | text `MemoryMaxBodyBytes = 1 << 10`; title 120 bytes; files 20 × 512 bytes; 20 agent notes per session | protocol/memory.go:40-43; store/memory.go:51-53 | DESIGN-FACT |
| How notes reach new sessions | Only through the launch brief: pinned titles plus up to 5 sessions from the last 48 h, at most 3072 bytes (`MemoryBriefMaxBytes = 3 << 10`, `MemoryBriefMaxRuns = 5`, `MemoryBriefWindow = 48h`). Unpinned notes are reached "only when it searches" | protocol/memory.go:47-50; memory.md:44-71 | DESIGN-FACT |
| Pinning | Members only; agents cannot pin. Budget: 1024 bytes for workspace pins, 512 per member; a pin costs its title + 64 bytes | store/memory.go:54-56; memory.md:75-88 | DESIGN-FACT |
| Search | SQLite FTS, bm25 weighted to newer notes; default 10, max 100 | coordination.md:1183-1186; protocol/memory.go:45-46 | DESIGN-FACT |
| Secret scan | gitleaks; agent add refused with `-32001` | coordination.md:1172-1181 | DESIGN-FACT |

## Could not establish

- The exact error text the hook reporter prints when `run.report` is over budget, and whether the harness surfaces it. I read only the server side (mailbox.go:240-249) and did not trace `aether-server report`.
- Whether `entry.waits()` is true for needs-input, which would decide if a needs-input turn also suppresses the provisional forward. I did not trace `cutWaiting`.
- Whether `subsession fetch` works at all on a VM child whose VM is dead (it goes through `publishVM`, which I did not read).
- No live runs, so nothing here is OBSERVED-ONCE.

## Surprises

- The "8-peer cap" in the user's memory notes is stale. #1108 says the code never had one.
- Report summaries lose all newlines (`strings.Fields`), so a multi-line child report arrives in the parent as one line.
- A child's `answered` report wakes the parent (as a provisional forward). `waiting` stays silent for up to 1 h.
- Failure is not refused for unfinished subsessions; only success is.
- A `merged` wait can sleep up to 7 days if GitHub's merge-queue GraphQL read keeps failing while the PR stays open. A failed queue read does not count as an unreadable source, so the 1 h broken-wait alarm never fires.
