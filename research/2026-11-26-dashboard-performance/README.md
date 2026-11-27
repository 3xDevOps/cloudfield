# Why the dashboard is slow, measured (2026-11-26)

Phase 1 of the performance overhaul: the ranked causes on one page, then the evidence.
Every number below was taken in a sandbox (1 CPU) against a real `aether-server` and a
real `cloudfield gui` gateway holding one seeded store; the command that produced it is
named beside it. Nothing here was measured on a live server or on the hosted Edge.

## The symptom, reproduced

Lanes opens at once and its bars stay hollow ("History not loaded"). On the seeded store,
24h window, all workspaces (`measure/lanes-browser.ts … 24h 450`,
[evidence/lanes-browser.txt](evidence/lanes-browser.txt),
[screenshot](evidence/lanes-24h-settled.png)):

- Lanes makes 12 `workspace.timeline` requests carrying 4.44 MB of uncompressed JSON.
- The visible bars go from 100% hollow to 70% hollow in five steps, one step per
  serial request, and **stop there**: the view settles with 70% of the visible bar
  width never coloured (6h window: 43%), under the label "Older history not loaded".
- 73% of the events fetched for the busy workspace (3,639 of 5,000) are `run.status`
  rereads, which the view throws away
  (`measure/lanes-chain.ts`, [evidence/lanes-chain.txt](evidence/lanes-chain.txt)).

So "takes forever to be coloured in" is two things: a slow serial crawl, and bars that
are never coloured at all because the crawl stops at an event budget that rereads use up.

## Ranked causes

`r` is one round trip between the gateway and the server. On the path through the hosted
Edge relay each `r` crosses the relay; its size on a real link is not established (see
"What one recording would settle").

| # | Cause (where) | Measured | Fix | Expected gain | Risk | Size |
| - | --- | --- | --- | --- | --- | --- |
| 1 | **Lanes pages are mostly rereads.** A waiting session publishes a same-status `run.status` with `reread: true` as often as once a minute (`docs/failure-handling.md`); `workspace.timeline` returns them whenever `types` names `run.status` (`internal/timeline/timeline.go` `keeps`); Lanes drops them (`segments.ts` `statusChanges`). They fill the 1,000-event pages and the 5,000-event budget (`web/src/store/lanes.ts` `historyBudget`). | Seeded busy workspace, last 24h: 15,331 rereads of 19,856 lane events (77%). 6h window: budget reached at 3.08 h, 51% of the window. 24h: 13%. | Let Lanes ask for history without rereads. The reader already has the filter (`timeline.Filter.NoRereads`, used by `changes.list`); `workspace.timeline` does not expose it. | Seeded: 6h needs 2,405 events (3 pages, whole window coloured); 24h needs 4,525 (5 pages, whole window); the same five pages then reach back 24 h where they reached 3.08 h. | A new optional request field: **protocol change, ask first.** An older server ignores or refuses it; needs a fallback. | S |
| 2 | **Every request costs three serial round trips to the server.** The gateway opens a new SSH channel and a `control` subsystem for each call (`internal/localgw/backend.go` `callOnce`, `internal/cli/channel.go` `openSubsystem`): channel open, subsystem accept, then the call. The server's control channel already serves any number of calls (`internal/sshd/control.go` `serveControl` loops). | Measured on a link with 150 ms round trips: one call 453 ms (3.0r); Lanes' five serial pages 2.33 s ([evidence/fix2-channel-reuse-before-after.txt](evidence/fix2-channel-reuse-before-after.txt)). A stream's first byte: 4r, from code. | Keep control channels open in the gateway and reuse them. | Measured in #1178: 453 ms to 152 ms a call, Lanes' five pages 2.33 s to 0.84 s, cold open to Board 2.9 s to 2.2 s. | Concurrency and cancellation on a shared channel (the call watchdog closes the channel today); no protocol change. | M |
| 3 | **Lanes reads serially and stops at 5,000 events.** `readBack` awaits each 1,000-event page before asking for the next (`use-lane-data.ts`); bars before the oldest event read are hollow. | With 450 ms per request: 24h settles after 2.8 s with 70% hollow; without delay 1.1 s. After fix 1 the 7d window still needs 6,651 events on the seeded store, over budget. | After 1 and 2: decide whether the budget should count drawn events, or grow with the window. | Coloured bars for the whole 24h window on the seeded store after fix 1; 7d needs the budget decision. | Changing the budget changes what the view shows: **ask first.** | S |
| 4 | **Nothing between the gateway and the server is compressed**, and the browser's copy is not either (`internal/webgate/api.go` `writeResult`; SSH offers no compression; relay WebSockets have it off). | A 1,000-event page is 356,410 bytes and gzips to 21,634 (16x); `run.list` for 600 sessions is 350,941 bytes, 23,543 gzipped (15x) ([evidence/payload-sizes.txt](evidence/payload-sizes.txt)). Seeded data repeats more than real data; the real ratio is not established. | Compress large control responses between server and gateway. | An order of magnitude fewer bytes for Lanes pages and `run.list` on a slow or throttled link (the relay throttles to 256 KiB/s once an egress budget is spent, `internal/edge/relay/splice.go`; whether the hosted Edge has a budget is not established). | **Protocol change, ask first.** | M |
| 5 | **Cold open fetches every session ever, in serial stages.** `run.list` returns all sessions (585 bytes each). `hydrate` (`web/src/store/sync.ts`) awaits its first seven reads together, then awaits four extras (devices, watches, merge proposals, edge) before it marks the store hydrated; `refreshTeam` reads `budget.get` once per workspace (`web/src/routes/team/sync.ts`), and a session page reads each again (`web/src/store/cost.ts`). `capabilities` and `agent.list` are read twice. | Cold open: 25 requests, 368 KB, 95% of it `run.list`, 4 to 5 waves; on a 150 ms link the Board shows after 2.9 s on main and 2.2 s with fix 2 ([evidence/fix2-channel-reuse-before-after.txt](evidence/fix2-channel-reuse-before-after.txt)). A reload on a session: 37 requests, `budget.get` 8 times for 4 workspaces. | Do not hold hydration for the four extras; read budgets once. Paging `run.list` changes what loads when. | One round trip sooner to a hydrated Board (from code, to be measured in the fix). | Showing the Board before the extras changes when its strips appear: **ask first.** | S |
| 6 | **Leaving Lanes drops its history** (`web/src/store/lanes.ts` `holdLanes`); coming back reads it all again. | Leave to Board and back: 2 `workspace.timeline` requests, 745 KB, for a small workspace; the full chain again for a busy one. | Keep history while the events socket stays connected and append live events to it. | Second and later opens of Lanes cost no requests. | A held copy can go stale across a reconnect: **ask first.** | S |
| 7 | **A session view polls.** `dev.terminal.list` and `run.room.status` on timers (`web/src/routes/run/shells.tsx`, `room.ts`), `presence.heartbeat`, `disk`. | 10 requests in 30 idle seconds on one open session, each 3r today. | Fix 2 cuts each to 1r; replacing polls with events is a larger change. | 30r to 10r per 30 s per open session after fix 2. | Low. | with 2 |
| 8 | **Client work per page.** Each page replaces the workspace's history and re-derives every lane (`board-lanes.tsx` `statusChanges`, `buildLanes`, `mailOf`, `laneEdges`). | Renderer CPU while a 24h fill ran: 0.6 to 0.9 s script, 1.5 to 1.9 s all tasks, on this 1-CPU sandbox (CDP `Performance.getMetrics`). Not the bottleneck beside 1 to 4. | Revisit after 1: 73% fewer events to merge and scan. | Not established. | | |

Server time is not a cause on this store: a 1,000-event lane page takes 27x a trivial
call and `run.list` 33x, both under 16 ms median on an idle host
([evidence/server-times.txt](evidence/server-times.txt)); the lane query is index-served
(`internal/events/sqlitelog.go` `readQuery`). What a congested host does to those numbers
is not established here.

## What happened to each cause

| Cause | Outcome |
| --- | --- |
| 1 Lanes pages mostly rereads | Merged, #1180: `no_rereads` and `older_time` on `workspace.timeline`. 6h: 43% of visible bars never coloured to 0%; 24h: 70% to 0%. |
| 2 Three round trips per call | Merged, #1178: the gateway reuses control channels for an explicit list of reads when the server was heard from in the last second. 453 to 151 ms a read on a 150 ms link; Lanes' five pages 2,330 to 828 ms. |
| 3 Serial reads and the 5,000 budget | #1196 (ready, in the merge queue on 2026-11-27): the budget follows the window (5,000 / 10,000 / 25,000) with **Load more**; numbers in [evidence/cause3-budget.txt](evidence/cause3-budget.txt). Pages cannot be read in parallel: each cursor comes from the page before. |
| 4 No compression | Not started. Approved in principle: negotiated, both mixed-version directions tested, CPU cost measured on one CPU with a size threshold, security review. |
| 5 Cold open | **Tried and rejected**, #1193 (closed). See below. The duplicate budget read on reload remains. |
| 6 Leaving Lanes drops its history | **Tried and rejected**, #1208 (closed). See below. |
| 7 Session polls, 8 client work per page | Not built. 7 rides on cause 2 (each poll is one round trip when it follows other traffic). |
| Follow-up | Merged, #1189: a failed agent-mail or subsessions read in session Details shows the server's error with Retry. |

Running totals on the seeded store over a 150 ms link, main before the first fix against #1196's head ([evidence/running-totals.txt](evidence/running-totals.txt)): Lanes 6h settles in 3.2 s with 44% of visible bars never coloured, now 1.4 s with 0%; 24h 2.8 s / 70%, now 1.9 s / 0%; 7d 2.9 s / 68%, now 2.0 s / 0% with the whole window loaded; cold open until sessions show 2,562 ms, now 1,538 ms.

## Tried and rejected

### Cold open: do not mark the store hydrated before the optional reads land

#1193 stopped `hydrate` (`web/src/store/sync.ts`) awaiting four reads (devices, watches, merge proposals, the edge) before it marked the store hydrated. Measured on the seeded store over a 150 ms link, 9 visits each (`measure/cold-open.ts`): hydration 1,559 to 1,437 ms, about one round trip; the first session showed at the same time (1,548 against 1,512 ms), because sessions never waited for these reads.

It was closed because "hydrated" also clears the stale mark (`staleSince`), and for a moment the Board then acts on state it has not re-read. The reviewer (omp, `openai-codex/gpt-6.2-astra`) traced three cases; anyone trying this again has to close all three first:

1. **Deny can revoke an approved device.** `web/src/store/devices.ts` keeps the previous device list on a same-member retry. If another admin approved a pending device during an outage, the Board looks fresh while the list is still the old one, and Deny (`web/src/routes/board/device-card.tsx`) sends a revoke by id that the server applies to the now-approved device (`internal/sshd/identity.go`).
2. **A watched pull request offers Watch PR again.** Before `watch.list` lands, `web/src/routes/run/pull-request.tsx` shows Watch PR instead of Watching PR; submitting the dialog uses a new idempotency key, so the server creates a second watch (`internal/watch/api.go`).
3. **Needs you says "Nothing needs you".** `web/src/routes/needs-you/index.tsx` shows its empty state once the stale mark clears; a pending device or merge proposal then appears a round trip later. The empty Board (`web/src/routes/board/index.tsx`), the sidebar count and the app badge have the same gap.

A second reviewer (Opus) confirmed case 3 and the empty-Board flash independently.

### Lanes: do not keep history after the view leaves

#1208 kept `laneHistory` when the last Lanes view unmounted, kept appending live events to it, and dropped it every time the event stream came live. Measured gain: a return to Lanes made no `workspace.timeline` request where it made one per 1,000 events held (one 269 KB request on a small workspace). After #1178 each of those costs one round trip.

It was closed because a reconnect is not the only way held history goes stale, and on main "leave Lanes and come back" repairs each of these for free. Two reviewers (Opus, and omp `openai-codex/gpt-6.2-astra`) traced them:

1. **An event the client fails to apply is lost while the stream stays live.** `drain` in `web/src/store/sync.ts` takes the event off its queue; a `run.status` for a run not yet in the store whose `run.get` fails returns before `appendLaneEvent`; the hydration that follows repairs the Board, not the lane history. Reproduced. (The lost event is a bug on main in its own right.)
2. **Workspace access revoked and granted back.** The server filters the stream by current access (`internal/sshd/events.go`) and never replays what it skipped, with the socket open throughout. A run that finished meanwhile still draws as running.
3. **A history stuck at `loading: true`** after an identity change during a read (`hydrate` clears history, then awaits before it publishes the identity; `readBack` abandons the page without clearing the flag). On main leaving Lanes clears it. (Also a bug on main in its own right.)
4. **Memory and work with Lanes closed.** History is kept per workspace ever viewed at the highest budget raised, and every lane event copies the held array.
5. **Every reconnect re-reads everything for a mounted view**, on each phone wake or network switch, where main lets the server's replay catch up at no cost. And a write from before a server restore can still land in the fresh history (an event awaiting a fetch when the stream drops).

A version that closes all five has to drop the history on every hydration, access change and failed apply and bound what is kept unmounted: far more code than the round trips it saves.

## What I could not establish

- The real mix on the owner's server: rereads per hour, lane events per window, the number of
  workspaces in scope, sessions in `run.list`. The seed assumes waiting sessions reread
  once a minute, the documented upper bound; a server with few verified waits would see a
  smaller share for cause 1 and a larger one for 2 to 4.
- `r` and the throughput of the owner's link through the relay, and whether the relay's egress
  throttle applies.
- Server time under load (many agents writing events), and lock contention in `run.list`
  (read from code in [evidence/server-static.md](evidence/server-static.md), not measured).
- Opening a session with a long transcript, the terminal, and a reconnect after the link
  drops: read from code only ([evidence/network-static.md](evidence/network-static.md)
  sections 2.4, 2.5, 2.9); the seeded sessions have no transcripts.
- Memory growth over a long-open tab; bundle and font loading after #1103.

## What one recording would settle

A HAR of Lanes filling in, from the browser where it is slow: DevTools, Network, tick
**Preserve log**, reload the dashboard, open **Lanes**, choose **24h**, wait until the
bars stop changing, then **Export HAR (sanitized)**. It gives the number, size and timing
of the `workspace.timeline` requests (so `r` and the link's throughput), the share of
rereads inside them, and the cold-open waterfall.

## The data set and the tools

- [seed/main.go](seed/main.go): 600 sessions over 21 days in 4 workspaces (60% in one),
  73,462 lane events, fixed by `--seed`; times are offsets back from the moment it runs.
  Counts by type and window: [evidence/seed-summary.txt](evidence/seed-summary.txt). The 24
  sessions seeded as running are marked interrupted when the server restarts over them.
- [measure/world.ts](measure/world.ts) starts the server and gateway from `dist/` and seeds
  them. The server needs Docker to restart over a store that has members.
- [measure/lanes-chain.ts](measure/lanes-chain.ts), [lanes-browser.ts](measure/lanes-browser.ts),
  [journeys.ts](measure/journeys.ts), [server-times.ts](measure/server-times.ts): each file's
  header says what it measures and how to run it. The delay is added in the browser in
  front of every `/api/v1` request; 450 ms stands for three round trips of 150 ms.

There was no repeatable way to see a regression in any of this before: the server has one
Go benchmark (the ACP log cache) and no timing logs or metrics endpoint
([evidence/server-static.md](evidence/server-static.md) section 5). The smallest one is a
request-count assertion per journey in the e2e suite; each fix adds the one for its journey.

## Evidence

- [evidence/network-static.md](evidence/network-static.md): transport, limits, compression,
  and the request inventory per journey, from code (Sonnet subagent; the transport claims
  in causes 2 and 4 were re-read by the lead).
- [evidence/server-static.md](evidence/server-static.md): handlers, queries, locks and the
  event stream, from code (Opus subagent).
- [evidence/journeys-0ms.txt](evidence/journeys-0ms.txt), [journeys-450ms.txt](evidence/journeys-450ms.txt):
  requests, bytes and serial waves per journey.
- [evidence/lanes-chain.txt](evidence/lanes-chain.txt), [lanes-browser.txt](evidence/lanes-browser.txt),
  [server-times.txt](evidence/server-times.txt), [payload-sizes.txt](evidence/payload-sizes.txt).

The edge's `MaxHTTPConnsPerServer = 8` does not apply to the dashboard: it limits the
connector path only (`internal/edge/relay/http.go`); the dashboard's calls all ride one SSH
connection through the relay.
