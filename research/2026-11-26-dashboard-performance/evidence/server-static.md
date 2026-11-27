# Server-side cost behind the dashboard's requests (static analysis)

Read-only analysis of the checkout at `/workspace` (branch
`cloudfield/run-performance-overhaul-of-the-clou-d4aesztxwh`, head `6abcb62ee`).
Nothing was built, run or measured. "q" = one SQL statement. Line numbers are
from the files as they are at that head.

## 1. One `workspace.timeline` call, end to end

### 1.1 Transport into dispatch

- Server-hosted dashboard (`servergw`): every HTTP request is authorised afresh
  with a tailscaled LocalAPI WhoIs over its unix socket, uncached
  (`internal/servergw/servergw.go:68-73`, `internal/sshd/whois.go:73-108`),
  then `GetMemberByTailnetLogin` (1 q, `internal/sshd/whois.go:180-188`). The
  call runs in-process: `webgate.handleAPI` -> `backend.Call` -> `sshd.Local.Call`
  -> `dispatch` (`internal/webgate/api.go:116`, `internal/servergw/backend.go:18-20`,
  `internal/sshd/local.go:58-72`). The result bytes are written as-is
  (`internal/webgate/api.go:263-270`).
- Local gateway (`cloudfield gui`): each call opens its own SSH channel
  ("roundTrip runs one call on its own channel", `internal/localgw/backend.go:286`).
  Server side, `serveControlFrame` re-reads the member once per channel before
  the first line (`internal/sshd/control.go:64`), then `handleRequest` ->
  `dispatch` (`internal/sshd/control.go:278-289`), and the response is
  marshalled again by `writeJSONLine` (`internal/sshd/control.go:237-243, 267-274`).

### 1.2 Dispatch, guard, handler (fixed cost, O(1))

| Step | Where | Store work |
|---|---|---|
| version gate | `control.go:296-300`, `clientgate.go:65-79` | none |
| `memberFor` | `control.go:307`, `errors.go:32-41` | `GetMember` = member row + `MemberWorkspaces` UNION (2 q, `store/sqlite.go:644-662`, `store/workspace_access.go:52-82`) |
| guard `resolveActor` | `guard.go:98` | `GetMember` again (2 q) |
| `workspaceTarget` | `guard.go:42-60` | `GetWorkspace` (1 q, `store/sqlite.go:264`) |
| workspace RLock + re-check | `guard.go:116-124` | `GetWorkspace` again (1 q) |
| handler `resolveActor` | `sshd/timeline.go:39` | `GetMember` a third time (2 q) |
| `auditCall` | `control.go:325-331`, `audit.go:121` | returns early for dashboard reads |

So 8 store queries before the handler reads anything (9 on the server gateway
with the tailnet lookup, plus one more `GetMember` per call on the SSH path).
The workspace `RWMutex` is only write-locked by `workspace.delete`, which uses
`TryLock` (`internal/sshd/admin.go:109-112`), so readers never wait on it.

`eventScope` (`internal/sshd/workspace_scope.go:133-146`) costs nothing up
front: `Run` and `Sees` are lazy closures.

### 1.3 Reader

`Reader.Before` (`internal/timeline/timeline.go:143-192`):

1. `prepare` -> `LastSeq` (`timeline.go:206-216`): 1 q on the event-log read
   pool, `SELECT MAX(COALESCE((SELECT MAX(seq) FROM events),0), COALESCE((SELECT seq FROM sqlite_sequence WHERE name='events'),0))`
   (`internal/events/sqlitelog.go:281-290`). O(log n) on the rowid.
2. Loop (`timeline.go:160-185`): `batch = max(limit, minBatch)` = 1000 for Lanes.
   The loop stops when the page is full, the cursor reaches 1, or
   `scanned >= limit*scanBudget` (20 x limit = 20,000 rows). `scanWindow`/`scanWindows`
   (10,000 seqs x 8 reads) apply only when no workspace is given
   (`timeline.go:162`, `timeline.go:26-34`). Lanes passes a workspace, so the
   window does not apply and `after = 0`.
3. Per batch: `EventLog.ReadBefore` (`sqlitelog.go:149-154`) -> `readQuery`
   (`sqlitelog.go:213-277`).

### 1.4 The SQL

Event log and store share one SQLite file: the server opens the log on
`StorePath(cfg.DataDir)` (`internal/server/server.go:321`); the store's v12
comment says so (`internal/store/migrate.go:320-323`).

Schema (`internal/events/sqlitelog.go:18-33`, mirrored by migrations):

```sql
CREATE TABLE events (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE,
  ts INTEGER NOT NULL, workspace_id TEXT NOT NULL, run_id TEXT NOT NULL,
  actor_id TEXT NOT NULL, type TEXT NOT NULL, payload TEXT NOT NULL);
CREATE INDEX idx_events_workspace_seq  ON events (workspace_id, seq);          -- migrate.go:555
CREATE INDEX idx_events_run            ON events (run_id);                     -- migrate.go:1803 (v73)
CREATE INDEX idx_events_coord_to_run   ON events (json_extract(payload,'$.to_run_id'))
  WHERE type = 'coord.message';                                                -- migrate.go:1905 (v77)
CREATE INDEX idx_events_type           ON events (type);                       -- migrate.go:2586 (v109)
CREATE INDEX idx_events_run_type       ON events (run_id, type);               -- migrate.go:2587
CREATE INDEX idx_events_workspace_type ON events (workspace_id, type);         -- migrate.go:2588
```

No `ANALYZE` is ever run (no hit for `ANALYZE` in Go sources; the comment at
`sqlitelog.go:208` and `store/memory.go:911` say so), so each lookup names its
index with `INDEXED BY`.

Lanes (workspace + 3 types, no run) takes the `index = "idx_events_workspace_type"`
branch (`sqlitelog.go:247-248`) and builds one limited arm per distinct type
(`sqlitelog.go:252-260`). Literal text for `ReadBefore(after=0, before=c, limit=1000)`:

```sql
SELECT seq, id, ts, workspace_id, run_id, actor_id, type, payload FROM events
WHERE seq IN (
  SELECT seq FROM (SELECT seq FROM events INDEXED BY idx_events_workspace_type
    WHERE seq > ? AND seq <= ? AND workspace_id = ? AND type = ? ORDER BY seq DESC LIMIT ?)
  UNION ALL SELECT seq FROM (... same, type = 'coord.message' ...)
  UNION ALL SELECT seq FROM (... same, type = 'coord.message.acked' ...)
) ORDER BY seq DESC LIMIT ?
```

The test `TestSQLiteLogReadsTypesThroughIndexes` asserts the plan
`SEARCH events USING COVERING INDEX idx_events_workspace_type (workspace_id=? AND type=? AND rowid>? AND rowid<?)`
and no `TEMP B-TREE` / `SCAN events`, ASC and DESC
(`internal/events/sqlitelog_test.go:335-411`, step at `:398`, check at `:406`).
That test runs on 7 rows; the plan on a large file is not established, but
`INDEXED BY` forces the index. Cost per batch: up to 3 x 1000 covering-index
entries plus 1000 rowid lookups. **O(rows returned x types), not O(log).**

Other shapes the same reader produces:

- **Run filter** (session log, trace, diff checkpoints pass `run_id`):
  `prepare` turns it into `Runs: [run]` (`timeline.go:212-214`), so `readQuery`
  takes the union path, skips every type index (`sqlitelog.go:244`) and filters
  type as `+type IN (...)` (`sqlitelog.go:262-267`):

  ```sql
  SELECT <cols> FROM events WHERE seq > ? AND seq <= ? AND workspace_id = ?
    AND +type IN (?, ...) AND run_id = ?
  UNION ALL SELECT <cols> FROM events WHERE seq > ? AND seq <= ? AND workspace_id = ?
    AND +type IN (?, ...) AND type = 'coord.message'
    AND json_extract(payload, '$.to_run_id') = ? AND run_id <> ?
  ORDER BY seq DESC LIMIT ?
  ```

  Asserted plan: `idx_events_run (run_id=? AND rowid>? AND rowid<?)` and
  `idx_events_coord_to_run (<expr>=? AND rowid>? AND rowid<?)`
  (`sqlitelog_test.go:299-310`, filter without types). `idx_events_run` holds
  only `run_id`, so every index entry costs a table-row read to test `type`.
  For a sparse type in a busy run, such as `run.diff` with limit 50
  (`web/src/store/diff.ts:303-310, 328`), that is **O(run's events in range)**.
  `idx_events_run_type` exists but this path never uses it. The
  `case f.Run != ""` branch (`sqlitelog.go:245-246`) is unreachable from
  the timeline, which sets `Runs`, not `Run`.
- **No types** (Activity feed, `web/src/routes/team/sync.ts:176, 231`): plain
  range plus `workspace_id = ?` with no type index. Detail types (`run.agent`,
  `run.diff`, acks, `run.pr`, presence, title) are decoded and only then dropped
  in Go (`timeline.go:40-47, 234`), up to 20 x limit rows per page. **O(log
  span)**, including JSON-decoding every agent event.

### 1.5 Per-row work in Go

- Row scan + `DecodePayload` (`json.Unmarshal` into the typed payload) for
  every row returned, **inside the `rows.Next` loop**, so the read
  connection stays held through decoding (`sqlitelog.go:164-189`,
  `internal/events/event.go:656-667`). O(rows returned).
- `keeps` (`timeline.go:218-235`): actor filter; `Reachable` when
  `Scope.Workspace` is set (always, `sshd/timeline.go:49`); `VisibleTo`;
  timeline kinds; rereads; detail types. For Lanes types:
  - `Reachable` (`internal/events/stream.go:176-205`) calls `scope.Run(ev.RunID)`
    for every event with a run, and `scope.Run(ToRunID)` for `coord.message`
    (`NamedRuns`, `internal/events/coord.go:23-28`).
  - `scope.Run` = `runScopes.reaches` (`workspace_scope.go:116-130`): super
    admin returns at once. Anyone else pays **one `GetRun` per distinct run
    per request** (cached only within the request). `GetRun` = the
    `runSnapshotQuery` join (LEFT JOIN `run_children`, LEFT JOIN `room_messages`
    with NOT EXISTS, two correlated `run_messages` subqueries, GROUP BY) plus
    `attachExtras` = **2 q per run** (`internal/store/runs.go:200-236`,
    `internal/store/run_repos.go:68-80`). This is N+1 on distinct runs, not on
    rows.
  - `Sees` is never called for these payloads: none implement `memberNaming`
    (`stream.go:155-169`).
  - `VisibleTo` / `Scoped`: type assertions only (`stream.go:127-143`).
    `Scoped` rewrites only `OverlapPayload`.
- **Rereads are kept for Lanes.** `keeps` drops a `run.status` reread only when
  `NoRereads` is set or no types were given (`timeline.go:231`). The handler
  never sets `NoRereads` (`sshd/timeline.go:43-50`) and the wire params have no
  such field (`internal/protocol/timeline.go:15-24`); only `changes.go:150`
  sets it. The scheduler publishes a same-state `run.status` with
  `Reread: true` whenever a wait's source was read again at least
  `verifiedStep` (1 minute) after the shown one, on each 30 s sweep
  (`internal/scheduler/activity.go:315-335`, `wait.go:228-236`,
  `state.go:360`, `scheduler.go:756-757, 931`).
- `slices.Reverse` (`timeline.go:186`).

### 1.6 Encoding the result

- Per event: `json.Marshal(ev.Payload)` into a `json.RawMessage`, plus an
  RFC3339Nano time string (`sshd/timeline.go:66-81`).
- `dispatch` then `json.Marshal`s the whole result (`control.go:328`), which
  re-validates and compacts every payload RawMessage. The SSH path marshals the
  `Response` envelope once more (`control.go:267-274`; `Result` is a
  RawMessage, `internal/protocol/protocol.go:185-190`).
- Payload bytes per event: decoded once, encoded once, compacted once (server
  gateway) or twice (SSH). No response compression on `/api/v1`
  (`webgate/api.go:263-270`; gzip exists only for static files,
  `webgate/static.go:84-91`).

### 1.7 Summary

| Step | Order |
|---|---|
| WhoIs, `GetMember` x3, `GetWorkspace` x2 | O(1) per request |
| `LastSeq` | O(log n) |
| SQL, Lanes shape | O(limit x types) index entries + O(limit) row fetches |
| SQL, run-filtered shape with sparse types | **O(events of the run in range)** |
| SQL, no-types shape | **O(events of the workspace in range)**, capped at 20 x limit per page |
| Decode, keeps, encode, compact | O(rows read) |
| `GetRun` for reachability | O(distinct runs on the page), non-super-admin only |
| Pages per Lanes view | client loops serially, 1000 per page, to `historyBudget` = 5000 per workspace (`web/src/routes/board/lanes/use-lane-data.ts:12, 35-49`; `web/src/store/lanes.ts:12, 15`) |

## 2. Other dashboard RPCs

Fixed per-call cost from 1.2 (WhoIs, `GetMember` x1-3, `GetWorkspace` x0-2)
applies to all of these. `registerMethod` handlers skip the guard; their own
`resolveActor` calls are listed.

### 2.1 Cold open (hydration, `web/src/store/sync.ts:172-262`)

In parallel: `server.info`, `workspace.list`, `member.list`, `run.list`,
`run.overlaps`, `capabilities` (`sync.ts:182-202`). Then, in parallel:
`member.device.list`, `watch.list`, `member.merge.pending`, `server.edge`
(`sync.ts:246-251`). `linear.status` runs without being awaited (`sync.ts:253`).
The `/ws/events` socket opens with `replay: false` on first connect
(`web/src/lib/stream.ts:145-150`).

- **`server.info`** (`sshd/handlers.go:39-68`): `GetMember` (2 q), a server
  mutex for two fields. Cheap.
- **`workspace.list`** (`handlers.go:70-84`): `resolveActor` (2 q),
  `ListWorkspaces` (1 q). Cheap.
- **`member.list`** (`handlers.go:108-139`): `resolveActor` (2 q),
  `visibleMembers` = `WorkspaceMembers` + `ListMembers` + `ListMerges` for
  non-super-admins (`workspace_scope.go:166-211`), then `ListMembers` and
  `ListMerges` again. 4-7 q. Duplicate reads, small tables.
- **`run.list`** with no params (the hydration call, `web/src/lib/api.ts:536-537`)
  (`handlers.go:333-386`):
  - `resolveActor` (2 q), `ListWorkspaces` (1 q), then **one
    `ListRunsByWorkspace` per reachable workspace** (`handlers.go:356-363`).
    Each is the `runSnapshotQuery` join over every run of the workspace,
    archived ones included (`store/runs.go:238-241`), plus `attachExtras`,
    which for more than one run reads **the whole `run_repos` table with no
    WHERE** (`store/run_repos.go:78-79, 106-118`). So `run_repos` is read once
    per workspace.
  - `runSnapshot` per run (`handlers.go:415-456`): `Paused`, `Sleep`, `Cause`,
    `Wait`, `Liveness`, `PendingInputs`, `Switching` and `Retention` each take
    the scheduler's global `s.mu` (`scheduler/steer.go:621-637`,
    `sleep.go:55-68`, `wait.go:204-212`, `liveness.go:15-23`,
    `report.go:318-326`, `mode_switch.go:45-52`, `storage.go:37-41`): **8
    acquisitions per run**. For every run the scheduler does not supervise
    (finished runs), `retentionLocked` does `os.ReadFile` of the run's sidecar
    **while holding `s.mu`** (`scheduler/storage.go:62-72`,
    `scheduler/state.go:691-702`). Multi-repo runs add a `GetWorkspace` each
    (`handlers.go:446-452`), an N+1.
  - `fillWaitConditions` (`sshd/run_wait.go:38-70`): 1 q `IN (all run ids)` with
    a correlated MAX subquery (`store/watch_run.go:258-282`), `resolveActor`
    again (2 q). For non-super-admins with PR waits, `ListWorkspaces` +
    `ListWorkspaceMirrors`. A `GetRun` per wait's target run
    (`run_wait.go:84-108`).
  - Totals: ~5 + 2W q (W = workspaces), hundreds of scheduler-lock
    acquisitions, and one sidecar open per finished run. O(runs ever created).
- **`run.overlaps`** (`sshd/overlap.go:17-76`): `resolveActor` (2 q),
  `Index.Overlaps` = `ListActiveRuns` (runSnapshot join + full `run_repos`
  read, `overlap/index.go:244-249`) then `refresh(ctx, true)`. **This read can
  publish `run.overlap` events, i.e. write to the event log**
  (`overlap/index.go:342-356`). Then **one `GetRun` (2 q) per distinct run in
  the overlap set, even for a super admin** (`overlap.go:31-45`).
- **`member.device.list`** (`sshd/identity.go:147-160`): `GetMember` +
  `visibleMembers` (3-4 q) + device list.
- **`member.merge.pending`** (`sshd/merge.go:206-211`): `PendingMerges`.
- **`watch.list`** (`sshd/watch.go:30-32` -> `watch/api.go:524-540`):
  `ListWatches` + per-watch conversion in Go. Its full cost is not established.
- **`server.edge`** (`sshd/server_edge.go:20-35`): no store work. The cost of
  `edge.Owner()` is not established.
- **`linear.status`** (`internal/linear/api/service.go:19-28`): for a member who
  connected Linear, **an external Linear API call (`Client.Viewer`) on every
  hydration**.

### 2.2 Board and Map

`web/src/routes/board/*.tsx` outside `lanes/` calls no read RPCs of its own.
The only `api.` calls there are `runInject` and the device approve/revoke
actions; Map (`board-map.tsx`, `run-map.tsx`, `map-*.tsx`) has none. Both
render hydrated store data plus the live stream. Server load while they are
open comes from the stream and from event-triggered refetches:
`run.wait.changed`, `run.children` and `run.mode` each make **every open
dashboard call `run.get`** (`web/src/store/sync.ts:609-624`), and `run.status`
for an unknown run does too (`sync.ts:488-503`). `run.get`
(`handlers.go:388-411`) = `reachableRun` (`GetMember` 2 q + `GetRun` 2 q) +
`RunPullRequests` + `runSnapshot` + `fillWaitConditions` (1 q + `GetMember`
2 q), so about 12 q including dispatch, per event per client.

### 2.3 Lanes

- `workspace.timeline` per section 1: each workspace's pages are serial, and
  workspaces run in parallel (`use-lane-data.ts:20-63, 72`).
- `run.acp.history` (limit 1000) when a session's sub-lanes are opened
  (`use-lane-data.ts:103-121`); cost as in 2.4.

### 2.4 Opening a session

- **ACP stream** `/ws/acp/{run}` (`sshd/acp_stream.go:52-`): `GetRun`, then
  `ACPSubscribe` (`scheduler/acp_session.go:48-81`). A live session replays
  from its in-memory index. A finished one opens the JSONL item log via
  `ReadOnlyLogCache` (8 entries, `acphost/log_cache.go:9, 28-65`). **On a cache
  miss it indexes the whole file**, reading every line and decoding each
  header (`acphost/log.go:122-140`). Replay is capped at the last
  `ReplayWindow` = 200 items (`acphost/session.go:552-556`, `log.go:327-349`).
  Items are read with one `ReadAt` of the byte span plus a `json.Unmarshal`
  per item (`log.go:351-369`).
- **`run.acp.history`** (`sshd/acp_rpc.go:98-120`): guard (`runTarget` =
  `GetRun` 2 q + `GetWorkspace`), `acpRun` = another `GetRun` (2 q). The same
  log path as above (`acp_session.go:91-104, 126-141`), limit up to 1000
  (`protocol/acp_rpc.go:15`). Then `it.Wire` re-marshals each item with
  truncation (`acp_rpc.go:113-118`). O(items on the page), plus
  O(file size) on an index-cache miss.
- **`run.acp.item`**: the same open, then one item (`acp_rpc.go:122-139`).
- **Session log** for live non-Enhanced runs (`web/src/routes/run/session-view.tsx:396`,
  `web/src/store/sessions.ts:32-35, 337-363`): `workspace.timeline` with
  `run_id` and types `run.agent, workspace.timeline, run.status, run.review`,
  1000 per page, up to 2000. This is the run-filtered SQL from 1.4. `run.agent`
  is dense, so few rows are skipped, but each `run.agent` payload is decoded
  and re-encoded.
- **Trace** (`web/src/routes/run/trace-data.ts:65`, `web/src/store/trace-log.ts:7-10, 80-110`):
  the same, with types `run.status, run.cost`, 500 per page, up to 2000. These
  types are sparse within a run, so the scan is **O(run's events)**.
- **Messages**: `coord.messages.list` (`sshd/coord_messages.go:33-115`).
  `ListRunMessages` pages in Go with a `Shows` callback and re-queries until
  the page fills (`store/coord_messages.go:83-110`). For every wait-wake
  message, **`storedWait` = `GetRunWait` + `GetRun` (3 q), even for a super
  admin** (`coord_messages.go:59-68`, `run_wait.go:126-140`). A `GetWatch` or
  `GetRun` may follow, and `waitVisibility` runs once. `redactWaitWakes` adds a
  `GetRun` per distinct target run (`coord_messages.go:126-146`). This is an
  N+1 on wake messages.
- **`run.room.list`**: one paginated store call (`sshd/collaboration.go:166-186`).
- **Terminal history** (TUI runs, `sshd/terminal.go:111-123` ->
  `ptyhost/history.go:143-237`): process-wide semaphore of 4 reads and 2
  searches (`history.go:50-51`). It flushes the live transcript under the
  session lock and pages at most 200 records (`history.go:26-27`).

### 2.5 Changes view

- **Diff checkpoints**: `workspace.timeline` with `run_id` and `types:['run.diff']`,
  limit 50 (`web/src/store/diff.ts:303-346`). The run-filtered `+type` scan
  from 1.4 walks every event of the run until it finds 50 diffs. Each
  `run.diff` payload carries the file list.
- **`run.git.status`** (`sshd/runrepo.go:211-247`): `runRepoAuthority`
  (`runrepo.go:85-173`) runs an `authorize` closure under the **process-wide
  `authorizationMu`** (`runrepo.go:101-102`, `launch_admission.go:78-88`).
  Each pass is `checkMember` (2 q), `resolveActor` (2 q),
  `resolveRunTarget` (`GetRun` 2 q + `GetWorkspace` 1 q), `GetRun` (2 q) and
  `ResolveLaunchAccount` (q count not established). The closure is wrapped
  into `execution.Authorize` (`runrepo.go:162-167`) and **runs again before
  every container command** (`internal/runrepo/service.go:84`). Each command is
  a container exec (`service.go:91`). The status path issues `git status`,
  `git log -1`, `git remote`, one `git remote get-url` per remote,
  `git for-each-ref` and `git var GIT_AUTHOR_IDENT`
  (`internal/runrepo/git.go:26, 87, 95, 112, 133, 490`), so at least 6 execs
  and 7 authorise passes (about 9+ q each) serialised on one global mutex.
- **`run.git.diff`** (`runrepo.go:249-260`): the same authority, then
  `Diff` (`runrepo/git.go:166-198`: merge-base and diff execs).
- **`files.diff`** per clicked file (`sshd/files.go:261-285` ->
  `gitengine/files.go:830-912`): creates a temp dir, copies the checkout's
  index, runs `checkCaptureBounds` (`git` listing, `gitengine/snapshot.go:82-`),
  **`git add -A` of the whole worktree into a scratch index**, then
  `checkStagedBounds`, then `git diff --cached <base> -- <path>`. Host-side
  `exec.CommandContext` (`files.go:921-946`). **O(worktree) per file.**
- **`run.pr.status`** for a finished run: an external GitHub lookup per
  unmerged recorded PR plus a store write (`runrepo.go:410-440`).

### 2.6 Files view

- **`files.tree`** (`sshd/files.go:51-90` -> `gitengine/files.go:951-975`):
  for a run, reads run metadata from disk, then `git ls-tree` on the host
  checkout (`files.go:91-121`) or a VM guest RPC. For a workspace, `git ls-tree`
  on the bare repo.
- **`files.read`** (`sshd/files.go:92-135` -> `gitengine/files.go:978-1003`):
  opens the file in the checkout, or `git cat-file` for a ref. For a workspace
  ref, `isMirroredBase` runs 2-3 `git config --get` execs per read
  (`gitengine/mirror.go:505-525`).

### 2.7 Patterns across handlers

- `GetMember` is fetched 2-4 times per guarded request (1.2), and again in
  handlers such as `fillWaitConditions` and `runRepoAuthority`. Nothing caches
  it within a request.
- `GetRun` is the heavy `runSnapshotQuery` join plus `attachExtras`, and it is
  used purely as a reachability check in `runScopes`, `run.overlaps`,
  `coord.messages.list` and the timeline.
- Nothing on these paths shells out except the files, changes and PR handlers.
  `run.list` touches the filesystem (sidecars), and ACP history opens JSONL files.

## 3. Locks and SQLite connections

- **SQLite setup.** Store: WAL, `busy_timeout(5000)`, `synchronous(NORMAL)`,
  `secure_delete(1)`, `_txlock=immediate`, **no `SetMaxOpenConns`** (unbounded),
  16 idle (`internal/store/sqlite.go:52-60`). Event log writer: its own
  `sql.DB` with `SetMaxOpenConns(1)` (`events/sqlitelog.go:57-63`). Event log
  readers: `query_only`, `busy_timeout(5000)`, **`SetMaxOpenConns(4)`**
  (`sqlitelog.go:45, 68-74`). There is no `cache_size`, `mmap_size` or
  `wal_autocheckpoint` setting; the only explicit checkpoint is in
  `store/preference.go:287`. Store and event log share one write lock; the
  store comment says so (`store/sqlite.go:42-43`).
- **The 4 event-log read connections are shared server-wide** by every
  `workspace.timeline` page (`LastSeq` + each batch), every replaying
  subscriber (`events/inproc.go:344-366`), the `Get` lookup `Publish` does
  for caller-supplied IDs (`inproc.go:72-75`) and `Append`'s collision lookup
  (`sqlitelog.go:97`). A query holds its connection while every row is
  decoded (`sqlitelog.go:158-189`). Lanes reads all workspaces in parallel, so
  four workspaces fill the pool. Further reads queue in `database/sql` with
  no timeout but the request context.
- **Bus lock across disk writes.** `InProc.Publish` holds `b.mu` while it
  assigns the seq, runs `log.Append` (an autocommit INSERT on the single
  writer connection) and fans out to every subscriber (`inproc.go:77-142`).
  Concurrent publishers serialise on the disk write by design
  (`inproc.go:44-52`). `Subscribe` takes the same lock (`inproc.go:167-172`).
- **Scheduler lock across store writes, event appends and fsync.**
  `refreshActivities` holds `s.mu` over every live run
  (`scheduler/activity.go:340-348`). `transitionOutcomeLocked`, under `s.mu`,
  can write and fsync a sidecar (`state.go:333-340`, `state.go:675-682`), runs
  the store `UpdateRunStatus` transaction (`state.go:350`) and publishes, i.e.
  `Append`s, under the bus lock (`state.go:374, 383`). Meanwhile `run.list`
  and `run.get` take `s.mu` 8 times per run (2.1).
- **Global `authorizationMu`** is held across about 10 store queries on every
  `run.git.*` / `run.pr.*` authorisation, and before every container command
  in them (2.5). `run.launch` holds it from admission until the run row exists
  (`sshd/handlers.go:288-289`, `scheduler/launch.go:416-417`). There are 61
  `authorizationMu` / `lockAuthorization` sites in `internal/sshd`.
- **Per-workspace `RWMutex`**: readers only; delete uses `TryLock` (1.2).
- **ACP log**: `Log.mu` is held only while capturing the byte span; reads run
  outside it (`acphost/log.go:232-261`). Index building on a cache miss holds
  the per-path cache entry lock (`log_cache.go:29-31`).
- **What a busy host (many agents writing events) does to a reader**:
  - WAL lets readers run beside the writer, so the timeline SQL itself is not
    blocked by writes.
  - Writes serialise three ways: through `InProc.mu`, the single writer
    connection, and the file's one write lock shared with store transactions
    (`busy_timeout` 5 s). While a publish waits, any goroutine holding the
    scheduler lock waits with it, and so does every `run.list` / `run.get`
    `runSnapshot`.
  - Event-log reads compete for 4 connections with replaying subscribers and
    publish-path lookups.
  - Each live dashboard stream decodes nothing extra, but every event is
    re-marshalled once per subscriber (`stream.go:108`, `stream.go:208-222`).
  - Continuous readers can delay WAL checkpoints. Whether the WAL grows large
    on a real host is not established.
  - modernc SQLite is pure Go (`sqlitelog.go:13`). On a one-CPU host,
    decoding, SQL and the agents' own work share that CPU. The magnitude is
    not established.

## 4. Live event stream (`/ws/events`)

- Path: `webgate.handleEvents` (`internal/webgate/events.go:23-79`) ->
  `backend.Events` -> `sshd.serveEvents` (`internal/sshd/events.go:21-95`;
  in-process via `Local.Events`, `local.go:76-89`).
- On connect: `checkMember` (2 q), `streamAccess.load` -> `resolveActor` (2 q)
  (`events.go:38-44, 135-142`), then `events.SubscribeWire` with the client's
  filter (`stream.go:24-46`). It also spawns `revokeOnPolicyChange`, which
  re-checks membership **every 3 s for the stream's life**
  (`events.go:71`, `sshd/attach.go:1064-1088`, `sync.go:58-61`).
- **What the dashboard asks for**: `{ replay: seq > 0, after_seq: seq }`, with
  **no workspace and no types** (`web/src/lib/stream.ts:145-150`). `lastSeq`
  is not saved in the snapshot cache (no hit in
  `web/src/lib/snapshot-cache.ts`), so a cold open subscribes live with no
  replay.
- **Reconnect**: replays every event with `lastSeq < seq <= joinSeq`
  (`inproc.go:150-178, 292-366`), in batches of 512 (`replayBatch`,
  `inproc.go:14`). The SQL is the plain range
  `SELECT <cols> FROM events WHERE seq > ? AND seq <= ? ORDER BY seq ASC LIMIT 512`
  (`sqlitelog.go:213-277`, no filters). That includes every `run.agent`,
  `run.diff`, `run.cost` and reread. All of it is decoded, run through
  `deliverable`, re-marshalled and written. **O(events since the cursor, all
  workspaces)**. Live events wait in a 256-event buffer
  (`events/bus.go:100`); overflow drops the oldest and ends the stream
  (`stream.go:102-104`), which forces a resubscribe and another replay.
- Per-subscriber cost per event:
  - `Filter.Matches` under the bus lock (`bus.go:48-67`): trivial for the
    dashboard's empty filter.
  - In the stream goroutine, `deliverable` -> `Reachable`
    (`stream.go:145-153, 176-205`). `scope.Workspace` calls
    `access.current`, which reloads the actor (2 q) when `accessGen` changed or
    3 s passed (`events.go:144-149`). `scope.Run` does a `GetRun` per new run
    id, cached for the stream's life (`events.go:91`).
  - `Scoped`, `wireEvent` (`json.Marshal` of the payload) and
    `writeJSONLine` (marshal again) (`stream.go:108-114`, `events.go:94`).
    The same event is marshalled once per open dashboard.
  - `ServerScope` events load `memberSight` lazily (3 q) after each reload
    (`events.go:152-158`).

## 5. Measurement hooks that exist

- Go benchmarks: exactly one, `BenchmarkReadOnlyLogCacheHistory`
  (`internal/acphost/log_cache_test.go:321`). None for the event log, the
  timeline reader, the store or `dispatch`.
- Query-plan tests (assert plans, not timings):
  `internal/events/sqlitelog_test.go:252-311` (one run),
  `:335-411` (type indexes), `queryPlan` helper `:313-333`;
  `internal/store/indexes_test.go:8-49` (run snapshot, latest run diff, steer
  requests). They are reusable for `EXPLAIN QUERY PLAN` on a copy of a real
  database.
- Concurrency test: `TestSQLiteLogReadsAndAppendsDoNotWaitForEachOther`
  (`sqlitelog_test.go:413`).
- Metrics: only the Edge relay serves `GET /metrics`
  (`cmd/aether-edge/metrics.go:47-50`). The server has no `/metrics`, no
  `net/http/pprof`, no `expvar` and no `Server-Timing` (no hits in `internal/`
  or `cmd/` outside the Edge relay; `httptrace` appears only in
  `internal/edge/relay/http.go:306-314`).
- Timing or slow-query logs on the RPC, store and event paths: none found.
  `dispatch` records no duration (`control.go:295-335`), and `auditCall`
  writes no timing and skips dashboard reads (`audit.go:121`).
- Dashboard-side: `RUN_PERF=1` micro-benchmarks in `web/src/store/perf.test.ts:1`
  and `web/src/store/session-perf.test.tsx:9`; Playwright e2e suite under
  `web/e2e/`.
- `system.stats` RPC (`internal/sysstats`) reports host CPU and memory; it was
  not examined further.

## Suspects, ranked by expected cost

Assumed store: weeks of events (hundreds of thousands of rows), hundreds of
sessions, several workspaces. "Confirm" names the measurement that settles each.

1. **Lanes reads mostly rows it does not need.** `run.status` rereads (about
   one per waiting run per minute) and `coord.message.acked` are returned
   because the wire cannot ask for `NoRereads` (`sshd/timeline.go:43-50`,
   `timeline.go:231`, `protocol/timeline.go:15-24`). That multiplies the
   serial 1000-row pages per workspace and fills the 5000-event budget
   (`lanes.ts:15`) before the window is covered, which leaves bars
   uncoloured. *Confirm:* on a copy of a real `aether.db`,
   `SELECT workspace_id, type, json_extract(payload,'$.reread') AS reread, count(*) FROM events WHERE type IN ('run.status','coord.message','coord.message.acked') GROUP BY 1,2,3;`
   plus the same restricted to `ts > now-7d`. Then count `workspace.timeline`
   calls per Lanes open with and without rereads.
2. **Per-page cost of a 1000-event timeline page is all Go work.** Decode
   1000 payloads while holding a read connection, `Reachable`, `json.Marshal`
   per payload, then 1-2 compaction passes (`sqlitelog.go:164-189`,
   `sshd/timeline.go:66-81`, `control.go:328`, `:267-274`). Non-super-admins
   add 2 q per distinct run. *Confirm:* a Go benchmark of `Reader.Before`
   (limit 1000, the Lanes types) against a copied real DB, split into SQL
   time, decode, encode and `GetRun` count. Also time one
   `workspace.timeline` through `dispatch` as super admin and as a
   collaborator.
3. **Run-filtered timeline reads scan the run's whole event range.** Trace
   (`run.status, run.cost`), diff checkpoints (`run.diff`, limit 50) and the
   session log go through `idx_events_run` with `+type` and a row fetch per
   entry; `idx_events_run_type` goes unused (`sqlitelog.go:244, 262-273`).
   *Confirm:* `EXPLAIN QUERY PLAN` of `readQuery(Filter{Workspace:w, Runs:[r], Types:['run.diff']}, 0, head, 50, "DESC")`
   on a real DB. Then `SELECT count(*) FROM events WHERE run_id=?` against the
   matching count for the busiest run, and a timing of that query.
4. **`run.list` at hydration is O(all runs) under the scheduler lock.** It
   makes one `ListRunsByWorkspace` plus a full `run_repos` read per
   workspace, 8 `s.mu` acquisitions per run, a sidecar `ReadFile` under
   `s.mu` per unsupervised run, and `GetWorkspace` per multi-repo run
   (`handlers.go:356-363, 415-456`, `storage.go:62-72`). Because `s.mu` is
   held across store writes, event appends and fsync (`state.go:333-388`),
   latency tracks agent activity. *Confirm:* time `run.list` on a copied DB
   with the real state dir. Count `openat` calls with `strace -c -e openat`
   on the server during one call. Take a mutex profile around it
   (`runtime.SetMutexProfileFraction` in a test harness; there is no pprof
   endpoint).
5. **Four event-log read connections serve the whole server.** Parallel Lanes
   reads, session and trace reads, replays and publish lookups all queue on
   them (`sqlitelog.go:45, 73`). *Confirm:* log
   `l.reads.Stats().WaitCount/WaitDuration` before and after opening Lanes on
   a multi-workspace server, or count concurrent `workspace.timeline` calls
   in flight.
6. **Reconnect replay is unfiltered and O(gap).** All event types for all
   workspaces are decoded and marshalled per subscriber
   (`stream.ts:149`, `inproc.go:344-366`). A 256-event buffer overflow during
   replay restarts it (`bus.go:100`, `stream.go:102-104`). *Confirm:* the
   event count between a client's `lastSeq` and head after a typical
   disconnect, and the replay time for 10k/100k events with a test
   subscriber.
7. **Write-path serialisation stalls readers indirectly.** `InProc.mu` is held
   across `Append` (`inproc.go:77-142`), store and log share the write lock
   with `busy_timeout` 5 s, and `s.mu` is held across both. *Confirm:* time
   `Append` and `UpdateRunStatus` under a synthetic burst of N publishers,
   alongside the latency of a concurrent `run.get`.
8. **`run.git.status` / `run.git.diff`.** At least 6 container execs, each
   preceded by an authorise pass of about 9+ q under the global
   `authorizationMu` (`runrepo.go:101-167`, `runrepo/service.go:84-91`,
   `runrepo/git.go:26-133, 490`). *Confirm:* count store queries and execs
   per call with a counting `store.Store` wrapper in a test, and time one
   call on a live run.
9. **`files.diff` runs `git add -A` of the whole worktree per clicked file**
   (`gitengine/files.go:830-912`). *Confirm:* time `FileDiff` against
   worktree size, e.g. 1k and 50k files.
10. **ACP history of a finished session indexes the whole JSONL on a cache
    miss**; the cache holds 8 entries (`acphost/log_cache.go:9, 49-64`,
    `log.go:128-140`). *Confirm:* extend `BenchmarkReadOnlyLogCacheHistory` to
    a cold cache with a long real log, and count misses when Lanes opens
    sub-lanes for more than 8 sessions.
11. **Per-request fixed overhead.** An uncached WhoIs round trip to tailscaled
    per HTTP request on the server gateway (`servergw.go:68-73`); 8+ store
    queries before every guarded handler (1.2); an SSH channel per call on the
    local gateway (`localgw/backend.go:286`). *Confirm:* time `LocalWhoIs.WhoIs`
    and count queries per `dispatch` with a counting store wrapper.
12. **Event-triggered `run.get` fan-out.** Every open dashboard calls `run.get`
    (about 12 q) on each `run.wait.changed` / `run.children` / `run.mode`
    (`sync.ts:609-624`). *Confirm:* count those events per hour in the log
    (`SELECT type, count(*) FROM events WHERE ts > ? GROUP BY type`) and
    multiply by open dashboards.
13. **`run.overlaps` and `coord.messages.list` N+1s.** `GetRun` per overlapped
    run even for a super admin, and a read that can publish
    (`overlap.go:31-45`, `overlap/index.go:342-356`). `storedWait` (3 q) per
    wait-wake message (`coord_messages.go:59-68`). *Confirm:* count queries
    per call with the counting wrapper on a store with many wait wakes and
    active overlaps.
14. **Activity feed (no types) decodes and drops detail events**, up to
    20 x limit rows per page (`timeline.go:162, 234`). *Confirm:* time
    `Reader.Page` with no types on the busiest workspace and count rows
    scanned against rows returned.
