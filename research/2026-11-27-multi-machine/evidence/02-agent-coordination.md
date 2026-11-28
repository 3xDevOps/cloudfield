# 02: How agents cooperate today, and where each mechanism assumes one machine

Collector: agent coordination. Rules: [COLLECTOR-RULES.md](COLLECTOR-RULES.md).

Code read at `9cdd039a1` (main when the collector started). Read only: no build, no tests, no probing of a live server. Open PRs #1150, #1151, #1165, #1170 and #1179 were read through `gh pr view` (descriptions only). The earlier collector file `research/2026-11-26-agent-orchestration/evidence/2b-comms-waits-durability.md` on draft PR #1130 is cited as "PR #1130 file 2b" instead of re-deriving its tables.

Tags: **Design** is what code or docs say. **Measured** is a number a PR author measured and wrote down. **Not established** is a claim the collector could not confirm from a source it read.

## 0. Cross-cutting facts every section relies on

- **Design.** One server process builds the coordination service, the scheduler, the watch service, the subsession service, team memory and the event bus from one dependency set. `coord.New` receives `Reports`, `Waits`, `Outcomes`, `Sudo`, `Development` and `Waker` as the scheduler value `d.Runs`, so calls from the socket into the scheduler are plain Go method calls. `internal/server/svc_coord.go:33-62`
- **Design.** The coordination root is `<data>/coord`; each session gets `<data>/coord/<run>/coord3.sock`. `internal/server/svc_coord.go:34`, `internal/coord/socket.go:85-124`, `docs/coordination.md:14-21`
- **Design.** All durable coordination state is one SQLite file opened with WAL, `busy_timeout(5000)` and immediate write transactions. The code comment says the store and the event log "share one write lock". `internal/store/sqlite.go:42-54`
- **Design.** The event bus is `events.InProc`: one `seq` counter, one subscriber map and one mutex. Publish assigns the sequence number, writes the log and fans out under that lock. `internal/events/inproc.go:20-27`, `internal/events/inproc.go:44-52`
- **Design.** The server's private per-session state lives in a state directory as JSON "sidecar" files (`sidecar` struct) next to an in-memory map `Scheduler.runs`. `internal/scheduler/state.go:416-456`, `internal/scheduler/scheduler.go:269-270`
- **Design.** Agents run in Docker containers through a local Docker client (`client.New(client.FromEnv)`) or in Cloud Hypervisor microVMs; both receive the session's coordination directory from the server's local disk. `internal/runtime/docker.go:120`, `internal/scheduler/coordination.go:218`, `internal/runtime/vm/share.go:48-119`

## 1. Authentication of a session to the coordination service

### (a) Path

- **Design.** The identity is the socket. The server binds one listener per session; a connection accepted by a session's listener is treated as that session. `serve` receives `run` from the listener, and "nothing on the wire names a sender". `internal/coord/socket.go:380-384`, `internal/coord/socket.go:418-421`
- **Design.** The container has no token, session flag or credential. `cloudfield-internal` always dials `/run/aether/coord3.sock`. `docs/coordination.md:64-67`, `internal/coordtransport/paths.go:8-12`, `internal/coordtransport/paths.go:35`
- **Design.** The server creates `<data>/coord/<run>` at mode 0755 and the socket at 0666 because the agent is not root. The code states "the mount is the only thing that grants access, so the socket's own mode carries no authorization". `internal/coord/socket.go:49-59`, `internal/coord/socket.go:358`
- **Design.** In a Docker container the directory is bind-mounted read-only at `/run/aether`. `internal/scheduler/coordination.go:218`
- **Design.** In a microVM a unix socket cannot cross the share, so the guest helper listens on `/run/aether/coord3.sock` and relays each connection to vsock port N on the host. Cloud Hypervisor delivers it to the host socket `<vsock socket>_<N>`, a symlink the server points at the session's real `coord3.sock`. `docs/vm-guest.md:305-312`, `internal/runtime/vm/share.go:51-55`, `internal/runtime/vm/share.go:85-87`, `internal/runtime/vm/share.go:145-161`, `internal/guest/helper/forward_linux.go:178-195`
- **Design.** The server only forwards a directory into a VM when "only root can write" it, because Cloud Hypervisor connects by path as root. `internal/runtime/vm/share.go:44-47`, `internal/runtime/vm/share.go:121-128`
- **Design.** There is no peer-credential check anywhere in the coordination package. A search for `SO_PEERCRED`, `Ucred` and `GetsockoptUcred` in `internal` found no non-test hit.
- **Design.** Parameters cannot name an identity. `run_id` is refused case-insensitively on `dev.*`; `run_id`, `workspace_id` and `member_id` are refused on `memory.*`. `internal/coord/socket.go:631-646`, `internal/coord/memory.go:40-71`
- **Design.** Each call re-reads the run and its owner's access: `resolveRun` calls `Store.GetRun`; `ownerReaches` calls `Store.GetMember` and the permission check. `internal/coord/mailbox.go:510-519`, `internal/coord/peers.go:80-98`
- **Design.** An Isolated session is refused `coord.send`, `coord.ask`, `coord.reply` and `memory.add`. `internal/coord/socket.go:667-685`

### (b) Guarantees

- **Design.** Per socket: at most 16 concurrent connections, idle connections reaped after 5 minutes, request lines capped at 64 KiB. `internal/coord/socket.go:61-76`, `internal/coord/socket.go:399-406`, `internal/coord/socket.go:443-453`
- **Design.** Per-session request budgets: 30 burst, 1 per second for ordinary requests, with separate buckets for hooks, lifecycle `run.report` and turn-state `run.report`. Malformed and unknown requests spend the ordinary bucket. `internal/coord/mailbox.go:39-45`, `internal/coord/mailbox.go:180-238`
- **Design.** Older wire sockets (`coord.sock`, `coord2.sock`) are deleted at recovery, not served. `internal/coord/socket.go:25-41`, `internal/coord/socket.go:294-301`
- **Design.** The client retries a missing or refused socket with backoff for up to 60 s across a server restart. `docs/coordination.md:299-335`

### (c) Single-machine assumptions

- `internal/coord/socket.go:335-385`: the listener is a unix socket on the server's local disk; the path must fit `sun_path` (107 bytes), so the data directory depth is a limit (`internal/coord/socket.go:43-47`, `internal/coord/socket.go:341-347`).
- `internal/coord/coord.go:205-234`: listeners, request buckets, run lifecycle references and wake bookkeeping are in-process maps guarded by one `sync.Mutex`.
- `internal/coord/coord.go:243-262`, `internal/coord/coord.go:412-452`: per-run reference counts and a `closing` flag decide when a run's handlers drain; this exists only inside one process.
- `internal/coord/socket.go:259-317`: restart recovery lists the local `<data>/coord` directory, asks the store for active runs and `RetainsContainer`, rebinds those and deletes every other directory and retires its mail (`socket.go:307-313`). A session whose directory sits on another machine would fall in the delete branch if its store row were not "active here".
- `internal/scheduler/coordination.go:218`, `internal/runtime/docker.go:120`: the directory is a bind mount into a container made by the local Docker daemon.
- `internal/runtime/vm/share.go:54`, `internal/guest/helper/forward_linux.go:181`: for VMs, identity is carried by a per-VM vsock socket link under that VM's directory on the host.
- `internal/coord/mailbox.go:510-519`: every authenticated call reads the shared SQLite store.
- `internal/scheduler/coordination.go:385-433`, `internal/server/svc_coord.go:72`: the CLI/bridge binary is staged by digest on the server's disk (`<data>/runtime/bin/aether-server-<digest>`) and mounted into every container.

## 2. send / ask / reply / inbox / ack (mail)

### (a) Path of one `send`

- **Design.** In the session, `aether-internal send` (CLI) or the MCP tool `aether_send` calls `coordtransport.Call`, which dials `/run/aether/coord3.sock` once per attempt and speaks NDJSON JSON-RPC 2.0. `internal/mcpbridge/tools.go:93`, `internal/coordtransport/client.go:113-132`, `docs/coordination.md:83-84`
- **Design.** The server's `serve` loop spends the budget, then `handleParsed` calls `Service.Send` and `sendMessage`. `internal/coord/socket.go:464-487`, `internal/coord/socket.go:536-542`, `internal/coord/mailbox.go:267-290`, `internal/coord/mailbox.go:1205-1299`
- **Design.** `sendMessage` order: idempotency lookup by `(from_run, key)`; per-run send bucket; `GetRun` for sender and target; `reachable` (the sender's owner must reach the target's workspaces, otherwise the target looks nonexistent); refuse a finished target; `authorize`. `internal/coord/mailbox.go:1209-1263`, `internal/coord/peers.go:22-76`
- **Design.** `authorize` allows (1) parent or child through `Children.Related`, (2) a radar peer in overlap or in its 10-minute grace, (3) otherwise the sender owner's `message` permission on the target. `internal/coord/peers.go:22-58`, `internal/coord/coord.go:39-41`, `docs/coordination.md:151-168`
- **Design.** The write is one SQLite transaction that checks the recipient's unread count (< 100), inserts into `run_messages` and enqueues a `coord_audit_publications` row. `internal/store/coord.go:312-351`
- **Design.** After commit, the server publishes the `coord.message` audit event through an outbox and calls `wakeInbox(to)`. `internal/coord/mailbox.go:1285-1297`, `internal/coord/mailbox.go:1118-1161`
- **Design.** `ask` stores kind `question` whose id is its correlation id; `reply` looks the question up in the store, routes to its sender and skips `authorize`. `internal/store/coord.go:274-277`, `internal/coord/mailbox.go:323-385`
- **Design.** `inbox` calls `Service.Inbox`, which calls `DeliverRunMessages`: in one transaction it acknowledges the batch named by the presented token, then returns the outstanding batch under its token or stamps the oldest unread messages with a new token. `internal/coord/mailbox.go:390-507`, `internal/store/coord.go:512-592`
- **Design.** With `--wait N` (≤ 30 s) the server parks the request on an in-process channel in `inboxWaiters[run]`, and `wakeInbox` closes it. `internal/coord/mailbox.go:438-479`, `internal/coord/mailbox.go:1329-1383`
- **Design.** The CLI keeps the last ack token in `$TMPDIR/aether-inbox-<run>-ack` inside the container and sends it on the next read. `internal/coordcli/inbox.go:25-52`
- **Design.** Tables written: `run_messages` (send/ask/reply/ack), `coord_audit_publications` (audit outbox), and the `events` log for `coord.message` and `coord.message.acked`. `internal/store/migrate.go:529`, `internal/store/migrate.go:898`, `docs/coordination.md:472-483`
- **Design.** The receiving session learns by: an `inbox --wait` long poll; a native wake; a boundary hook; a Codex app-server prompt; or an Enhanced `session/prompt`. Details in section 3. `docs/coordination.md:337-367`

### (b) Guarantees

- **Design.** Delivery is at least once, oldest first (`ORDER BY created_at, rowid`), one frozen batch per token until acknowledged; tokens survive a server restart while the session's coordination data is retained. `internal/store/coord.go:538-565`, `docs/coordination.md:262-285`
- **Design.** Mutations need an idempotency key (≤ 256 bytes, no control lines). The same key with the same inputs returns the original receipt; different inputs conflict. A unique index on `(from_run, idempotency_key)` enforces it across restarts. `internal/coord/mailbox.go:1308-1327`, `internal/store/coord.go:292-348`, `docs/coordination.md:286-297`
- **Design.** Limits: body 4 KiB; inbox 100 unacknowledged (the count is inside the INSERT, so a full inbox is an explicit `ErrInboxFull`, never a silent drop); batch 16 KiB with at least one message; send burst 5 then 1 per 5 s; inbox read burst 10 then 1 per second; no peer cap. `internal/protocol/coord.go:35-42`, `internal/store/coord.go:315-347`, `internal/store/coord.go:609-625`, `internal/coord/mailbox.go:23-37`, `docs/coordination.md:219-255`
- **Design.** A receipt means "durably stored", not read. `docs/coordination.md:294-297`
- **Design.** A finished session cannot be messaged (`-32004`). When a session's sandbox is released its unread mail is retired (`retired_at`), keeps listing, and is never delivered. `internal/coord/mailbox.go:1253-1258`, `internal/store/coord.go:594-607`, `internal/coord/socket.go:250`
- **Design.** Rate-limit buckets are in-memory maps in the service and are deleted when a run is released; `spend` writes nothing to the store. `internal/coord/coord.go:206-212`, `internal/coord/mailbox.go:1385-1414`, `internal/coord/socket.go:231-244`
- **Design.** A message to a subsession a member controls is stored and shown as `held`. `internal/coord/mailbox.go:1163-1185`, `docs/coordination.md:1350-1353`
- Per-limit table, ack-token behavior and the "no 8-peer cap" correction: PR #1130 file 2b section 1.

### (c) Single-machine assumptions

- `internal/coord/mailbox.go:1329-1383`: the long-poll wake is a Go channel in `s.inboxWaiters`, closed by `wakeInbox` in the same process that took the send. A send handled by process B cannot close a channel parked in process A.
- `internal/coord/mailbox.go:1385-1414`, `internal/coord/coord.go:206-212`: send, inbox and transport rate limits are per-process in-memory token buckets.
- `internal/coord/coord.go:234`, `internal/coord/report.go:345-369`: the "unread mail refuses a success report" one-shot flag `mailRefusals` is an in-process map.
- `internal/store/coord.go:312-351`, `internal/store/sqlite.go:52-53`: the inbox cap, idempotency index and ack tokens depend on one SQLite file and its single write lock.
- `internal/coord/mailbox.go:1118-1161`, `internal/coord/coord.go:225-226`: the audit outbox publishes to the in-process bus; its page cursors `reportCursor` and `auditCursor` are in memory.
- `internal/events/inproc.go:20-27`: `coord.message` reaches dashboards only through this process's bus.
- `internal/coordcli/inbox.go:25`: the ack token file is in the container's `$TMPDIR`; it is local to the container, not the server.
- `internal/coord/peers.go:22-58`: authorization consults `radar.authorized`, which is in-process (section 6), and `Children.Related`, which reads the store.
- `internal/coord/peers.go:119-168`: `status` lists peers from `Store.ListLiveRunSummaries(self.WorkspaceID)`; the code treats "live run on the server" and "live run in the database" as the same set.

## 3. Wakes (mail or a report wakes an idle or sleeping recipient)

### (a) Path

- **Design.** After any mail append, `wakeInbox(run)` closes the inbox waiter and calls `offerWakeLocked`, which does two things: signals every registered `hookWaiter` of that run, and starts `wakeEnhancedLocked`. `internal/coord/mailbox.go:1375-1383`, `internal/coord/acp_wake.go:50-56`, `internal/coord/acp_wake.go:91-112`
- **Design (Enhanced, ACP).** `wakeEnhanced` asks `Waker.Wakeable(run)`, lists unread ids from the store, passes `WakeAdmission`, then `Waker.Wake(prompt)`. `Scheduler.Wake` sends one `session/prompt` through `WakeEnhanced`: `IdleEnhanced` check, `holdAwake` (wakes a sleeping container or VM), `acp.live`, `PromptIdle`. `internal/coord/acp_wake.go:114-200`, `internal/scheduler/acp_session.go:258-296`, `internal/scheduler/acp_session.go:331-385`
- **Design.** The ACP conversation is spoken by an `acphost.Session` inside the server process, through a short-lived bridge exec attached to a keeper process in the container. `internal/scheduler/acp_driver.go:55-70`, `internal/scheduler/acp_keeper.go:195-212`, `internal/acpkeeper/protocol.go:1-12`
- **Design (Standard Claude Code).** A managed `asyncRewake` waiter runs in the container: `aether-internal hook claude wake` long-polls `coord.hook.status` with `wait_seconds` 30. The server registers a `hookWaiter`, waits, runs `WakeAdmission` and writes a response with `wake_admitted: true`; the waiter exits 2, which makes Claude Code start a turn. `internal/coordcli/hook_claude_wake.go:23-30`, `internal/coordcli/hook_claude_wake.go:98-110`, `internal/coord/hook_wait.go:108-158`, `internal/coord/hook_socket.go:40-115`, `docs/coordination.md:344-349`
- **Design (Standard pi, oh-my-pi, OpenCode).** The native integration calls `hook <harness> wake` for bounded observations and starts a turn with its own API. `docs/coordination.md:823-846`, `internal/coordcli/hook_wake.go:23-60`
- **Design (Standard Codex).** The server prompts the terminal's app-server: `Scheduler.Wake` calls `appServer.wake` when the run is attached to an app-server. `internal/scheduler/acp_session.go:289-294`, `internal/scheduler/acp_session.go:322-327`, `docs/coordination.md:355-361`
- **Design (sleeping Standard VM).** `Wake` first calls `WakeRun` when `asleepStandardVM`; the saved VM resumes and its hook then takes the mail. `internal/scheduler/acp_session.go:271-288`, `internal/scheduler/acp_session.go:299-310`, `internal/scheduler/sleep.go:81-88`
- **Design (admission).** `coordWakeAdmission` re-reads the run inside the run-control admission lock and refuses unless the run is interactive, not protected, and `running` or `needs-attention`. A child run is admitted with `TryAdmitUnheld`, which also refuses while a member controls it. `internal/server/svc_coord_wake.go:31-61`
- **Design (boundary hooks).** Claude Code, Copilot, Gemini and Cursor command hooks add the inbox notice and overlap notice at their next supported event and record what they announced in `$TMPDIR/aether-hook-<run>-<harness>.json` in the container. `docs/coordination.md:350-354`, `docs/coordination.md:385-397`, `internal/coordcli/hooks.go:225`

### (b) Guarantees

- **Design.** A wake is an owed state, retried on mail arrival, a turn end, resume, protection or control change (`consume` calls `WakeIdle` on those events), a sequence of delays 1, 2, 4, 8 s, and a 30 s sweep over every listener. `internal/coord/coord.go:384-391`, `internal/coord/acp_wake.go:23-25`, `internal/coord/acp_wake.go:58-88`
- **Design.** Each set of unread message ids wakes at most once, tracked in `enhancedWoken` (memory). A prompt the agent refused counts as delivered for the sessions opened so far (`enhancedOpens`, `enhancedRefusals`). `internal/coord/acp_wake.go:114-215`, `internal/coord/coord.go:217-222`
- **Design.** An active `inbox --wait` takes priority over any native wake (`inboxConsumers`). `internal/coord/acp_wake.go:91-93`, `internal/coord/hook_wait.go:199-203`
- **Design.** Admission uses `TryAdmit`, which returns `ErrAdmissionBusy` at once when the run's lock is held. `internal/control/lease.go:345-372`, `internal/server/svc_coord_wake.go:57-61`
- **Design.** The final native write is bounded to 3 s and admission to 2 s; the Enhanced wake context is 20 s. `internal/server/svc_coord_wake.go:40-47`, `internal/coord/hook_socket.go:16-20`, `internal/coord/acp_wake.go:17`
- **Design, when the recipient cannot be woken.** The mail stays in `run_messages` unacknowledged; only a protected session, a controlled child, a Background session (not interactive), a paused or switching Enhanced session, or an agent with no idle wake is left to read it on its own. Nothing is typed into a terminal. `internal/server/svc_coord_wake.go:48-51`, `internal/scheduler/acp_session.go:331-345`, `docs/coordination.md:337`, `docs/coordination.md:422-426`, `internal/scheduler/launch.go:905-913`
- **Design.** Server-side consequences of an unwoken mail: a success or failure report is refused while mail is unread (`-32002`), a child's parent gets a notice after the child sat quiet for 1 hour or stopped on an error for 1 minute, and a resolved wait whose wake was not delivered for 5 minutes makes the run need a member. `internal/coord/report.go:345-369`, `internal/children/dispatch.go:281-285`, `internal/scheduler/wait.go:50-54`
- **Design.** Before PR #1150 a Standard session launched with no task loaded no idle wake; the Claude `HoldTurnEnd` check still keys on `launchMode == ACP || task != ""`. `internal/scheduler/report_gate.go:33-35`
- **Open PR #1150.** Loads the managed hooks and idle wake for every coordinated Standard launch, task or not, and records `idle_wake` in the sidecar. OpenCode cannot be woken before its first prompt. Not verified through Docker by the author.
- **Open PR #1151.** Refuses `subsession start --mode standard` for an agent mail cannot wake (Copilot, Gemini, Cursor, Antigravity) and defaults Antigravity to Background.
- **Open PR #1165.** Forwards a Standard subsession's turn end without a report to its parent as a provisional report, for Claude Code, Codex, pi, oh-my-pi and OpenCode; the held message is memory-only, so a server restart inside a reminded continuation forwards `(none)`.

### (c) Single-machine assumptions

- `internal/coord/coord.go:152`, `internal/server/svc_coord.go:58`: `Waker` is the scheduler value in the same process; `Wakeable`, `Wake` and `InTurn` are Go calls.
- `internal/scheduler/acp_session.go:299-345`, `internal/scheduler/scheduler.go:269-270`: `IdleEnhanced`, `asleepStandardVM` and `attachedAppServer` read `Scheduler.runs` and the in-process `acpDriver.runs` (`internal/scheduler/acp_driver.go:63-70`).
- `internal/coord/coord.go:214-222`, `internal/coord/hook_wait.go:22-26`, `internal/coord/hook_wait.go:161-188`: hook waiters, woken sets, refusals and per-run wake locks are in-process maps and channels.
- `internal/coord/hook_socket.go:40-115`: a Standard wake needs the long-poll connection to be held open on the very process that appends the mail, because `signal()` closes a channel.
- `internal/control/lease.go:152-177`, `internal/control/lease.go:355-372`: the run-control lock used for wake admission is an in-memory per-run mutex; only surface leases are persisted (`PersistSurface`, `lease.go:64-69`).
- `internal/scheduler/acp_keeper.go:195-212`, `internal/scheduler/acp_driver.go:499-501`: the server reaches the keeper and the Codex app-server through managed execs on its own runtime (`runtime.ManagedExecRuntime`).
- `internal/scheduler/sleep.go:159-181`: `holdAwake` and `wake` start the stopped container or VM through the local runtime.
- `internal/scheduler/provisional.go:33`, `internal/scheduler/report_gate.go:54`: the 15 s provisional timer and the held-stop timer are `time.AfterFunc` values in memory and are lost on restart.
- `internal/coordcli/hook_claude_wake.go:26-40`: the waiter's claim and woken-id records are files in the container's `$TMPDIR`.
- Not covered: how a wake would reach an agent whose runtime is on another host; no code path for it exists in the files read.

## 4. Reports and wait conditions

### (a) Path

- **Design, lifecycle report.** Agent hooks run `/opt/aether/aether-server report <harness>`, which sends `run.report` on the session socket. `Service.Report` calls `Reports.HoldTurnEnd` (for a Claude `Stop`) and `Reports.ReportAgentState`, both the scheduler. `docs/coordination.md:1590-1598`, `cmd/aether-server/main.go:87`, `internal/coord/report.go:269-326`
- **Design, terminal outcome (`success`, `failure`, `blocked`).** `CoordReport` takes the per-run `reportLock`, refuses unread mail and unfinished subsessions, `ReserveCoordReport` (table `coord_reports`), captures evidence (git evidence commit plus PTY transcript, 2-minute budget), `FinalizeCoordReport`, `enqueueChildReport`, then `publishReportEvidence` and `applyRunOutcome`, which calls `Outcomes.FinishReported` or `ReportBlocked` on the scheduler. `internal/coord/mailbox.go:528-725`, `internal/coord/mailbox.go:842-922`, `internal/coord/report.go:239-266`, `docs/coordination.md:1560-1583`
- **Design, state outcomes (`needs-input`, `waiting`, `answered`).** `reportState` records a receipt in `coord_state_reports` (with `--until`, `Until.RegisterWait` writes the receipt and the conditions in one transaction), calls `Outcomes.ReportNeedsInput|ReportWaiting|ReportAnswered`, then `MarkCoordStateReportApplied`, then `SettleRunWaits`, then publishes a timeline event. `internal/coord/report.go:124-267`, `internal/watch/wait.go:51-100`
- **Design, who evaluates a wait.** The watch service in the server process. A `--until` condition is a row in the `watches` table with source `agent` and `owner_run_id`. The service follows the bus from the saved `watch_cursor`, ticks every 15 s, polls a pull request every 1 min while checks run and every 5 min otherwise, and reads a `run:` target on its events and every 10 min. A wait expires after 7 days. `internal/watch/service.go:33`, `internal/watch/service.go:164-198`, `internal/watch/pr_source.go:27-28`, `internal/watch/wait.go:24-27`, `internal/store/watch_notify.go:228-242`
- **Design, when the report is registered.** `SettleRunWaits` reads the new conditions inline for at most 10 s, and a condition already resolved is returned in the report result instead of waking. `internal/watch/wait.go:94-104`, `internal/coord/report.go:196-209`
- **Design, how the wake arrives.** `deliverWait` claims the delivery in the store (lease and attempt count), then calls `notifier.NotifyRun(owner, owner, body, correlation, key)`, which is `coord.Service.NotifyRun`: append to the owner's inbox and `wakeInbox`. `internal/watch/wait.go:522-562`, `internal/coord/children.go:67-87`
- **Design, who judges a run's wait in the UI.** The scheduler reads the conditions again through `WaitConditions` on every `run.wait.changed` and classifies them with `classifyWait`: a source unread for 1 hour (`unreadBound`) or a resolved wait whose wake is undelivered for 5 minutes (`wakeGrace`) makes the run need a member. `internal/scheduler/wait.go:50-54`, `internal/scheduler/wait.go:73-200`, `docs/watches.md:497-507`
- **Design, client side.** `cloudfield wait` is a client of `attention.list` and the status event stream; PR #1170 only adds support for several session ids and changes no server code.

### (b) Guarantees

- **Design.** Every report takes a fresh idempotency key; a reused key with different inputs is a conflict; a report that cannot take effect is refused, not dropped. `internal/coord/report.go:124-150`, `internal/coord/report.go:230-237`, `docs/coordination.md:1410-1420`
- **Design.** Terminal reports use a durable publication outbox: pending rows in `coord_report_publications`, retried with delays 1 s doubling to 32 s, permanent conflicts quarantined, event ids deterministic so a retry does not duplicate. `internal/coord/mailbox.go:929-1034`, `internal/coord/mailbox.go:1080-1088`, `docs/coordination.md:1569-1578`
- **Design.** A report refused for unread mail is refused once; one `coord.inbox` call afterwards lets the next report through. `internal/coord/report.go:345-369`
- **Design.** Wait conditions: at most 4 per report, grammar `pr:<owner>/<repo>#<n>:merged | :checks[@sha] | run:<id>:finished`, at most 30 waiting reports with conditions per hour, `run:` targets must be in the same workspace. `internal/protocol/wait.go:13`, `internal/protocol/wait.go:36`, `internal/store/watch_run.go:133`, `docs/coordination.md:1503-1512`
- **Design.** Wake delivery is claimed before it is written and keyed `wait:<id>:<outcome>`, so a retry, restart or late event does not wake twice. `internal/watch/wait.go:546-562`, `docs/watches.md:464-466`
- **Design.** Waits survive a restart: the receipt and conditions commit together and are due at once. `docs/watches.md:486-491`
- **Measured (PR #1179, open).** On `main` (`6abcb62ee`), with real Docker and Enhanced sessions: a second `success` under a new key in one turn gave the parent 2 inbox messages and 2 wakes; `success` then `answered` replaced the shown outcome; an `answered`-only turn was followed 15 s later by a mail saying the session ended without a report. The PR makes the repeat harmless (one mail per outcome per turn) and leaves "3 mails 8 s apart then `success`" at 4 messages and 4 wakes.
- PR #1130 file 2b sections 2 and 3: outcome list, size caps, `merged` and `checks` resolution rules, the `merged` queue-read gap, and the 7-day expiry.

### (c) Single-machine assumptions

- `internal/coord/coord.go:495-504`: `reportLock` is a per-run `sync.Mutex` in the service; `child.start` takes the same lock (`internal/coord/children.go:48`).
- `internal/coord/coord.go:223`, `internal/coord/mailbox.go:766-799`: an in-memory cache of evidence packets; it falls back to the store when missing.
- `internal/server/svc_coord.go:43-46`, `internal/coord/report.go:269-326`: `Reports`, `Outcomes` and `Waits` are direct calls into the scheduler's in-memory run map.
- `internal/watch/service.go:98-123`, `internal/watch/service.go:164-198`: one watch service per database; the cursor is a single row (`watch_cursor id = 1`, `internal/store/watch_notify.go:228-242`), so a second evaluator would share and race on one cursor.
- `internal/watch/service.go:60-65`: `Runs` (`PendingInputs`, `Paused`, `ACPPending`) is answered by the local scheduler.
- `internal/watch/wait.go:522-562`: the wake goes through `coord.Service.NotifyRun`, which appends and then wakes in the same process (section 3).
- `internal/scheduler/wait.go:175-200`: the scheduler reads conditions through a store interface; `entry.waitsFailingSince` and `entry.turnStartedAt` live on the in-memory run entry.
- `internal/scheduler/report_gate.go:25-57`: whether a turn owes a report, whether a Stop was held and the `reportAsked` marker are fields on the in-memory run entry.
- `internal/scheduler/provisional.go:21-45`: the provisional report decision reads `entry.turnStartedAt` and `entry.waits()` after a local timer.
- `internal/coord/mailbox.go:675-682`, `internal/evidence/evidence.go:31-32`: evidence capture reads the server-side checkout and the PTY transcript the server holds (16 MiB cap).

## 5. Subsessions

### (a) Path

- **Design, start.** `subsession start` (CLI) or `aether_subsession_start` (MCP) sends `child.start`. The socket handler takes the run's `reportLock` and calls `children.Service.HandleAgent`. `internal/mcpbridge/children.go:30-49`, `internal/coord/children.go:29-65`, `internal/children/agent.go:37-80`
- **Design.** `start` validates (task ≤ 24 KiB, purpose, key), replays a prior reservation by scanning the parent's children for the same key, resolves policy, chooses harness, account and mode, calls `Git.PublishRunBranch(parent)`, and inserts a `run_children` row in state `queued` under `(parent_run_id, idempotency_key)`. `internal/children/agent.go:20`, `internal/children/agent.go:126-186`, `internal/store/children.go:121-163`
- **Design.** A reconcile loop (every 3 s, on a kick, and on every `run.status` event) dispatches queued children: it counts busy children against the workspace `child_max_live` (default 8, admin maximum 64), marks one `launching` and starts `go s.launch`. `internal/children/service.go:109`, `internal/children/service.go:143-160`, `internal/children/dispatch.go:23-60`, `internal/store/migrate.go:2069`, `internal/domain/children.go:31`
- **Design.** `launch` re-resolves policy, re-runs the budget gate and `Authorize` under `AuthorizationMu`, then calls `Runs.LaunchWithOptions` with `ReservedRunID`, `FromCommit` and `FromBranch`; the child is an ordinary session on the same scheduler. `internal/children/dispatch.go:64-156`, `internal/scheduler/launch.go:268`
- **Design, limits and refusals.** One level deep; no Background parent; a parent that cannot wake on mail is refused; a handed-off session is refused; a subsession cannot start subsessions. `internal/children/policy.go:51-61`
- **Design, which account.** With `--account`, that account (the owner's own or one shared with them); otherwise the parent's account if the child uses the parent's harness and the parent runs on its owner's home; otherwise the owner's own. The account must pass `Authorize`, not be turned off for subsessions, and the agent must be installed and signed in. `internal/children/accounts.go:171-203`
- **Design, how the child's report reaches the parent.** A child `coord.report` is finalized, then `enqueueChildReport` looks up `Children.ReportRecipient`, appends a mail with key `coord-report-<outcome>:<report>:<parent>` and correlation = report id, and calls `wakeInbox(parent)`. Other parent notices (turn end without report, ended without final report, quiet for 1 h, stopped for 1 min, refused to start) come from `ProvisionalReport` and the reconcile loop through `Mailer.Notify`. `internal/coord/mailbox.go:801-840`, `internal/coord/provisional.go:21-67`, `internal/children/dispatch.go:167-263`, `internal/children/dispatch.go:281-379`
- **Design, needs-input.** The question goes to the owner and `Children.NeedsInput` mails the parent. `internal/coord/report.go:215-217`, `internal/children/coord.go:90-97`
- **Design, cancel.** `cancelChild` takes `AuthorizationMu`, re-reads the child and calls `Runs.StopForParent` (a kill by the parent) or marks a queued child cancelled. `internal/children/agent.go:282-330`, `internal/scheduler/steer.go:23-25`
- **Design, fetch, container child.** (1) If the child is live, `PublishRunBranch(child)` runs `git fetch <scratch> +refs/heads/<branch>:refs/heads/<branch>` into the workspace bare repository on the server's disk, where `<scratch>` is the run's server-owned snapshot store (a bare repository on the server's disk) into which `checkoutHeadSource` first writes a scratch ref naming the checkout's validated HEAD, so Git never reads the checkout's own config. (2) `FetchRunBranch` runs `git -C <parent checkout> fetch --no-tags <workspace repo path> +refs/heads/<branch>:refs/remotes/children/<run-id>`. Both are filesystem paths, not network remotes. `internal/children/agent.go:332-353`, `internal/gitengine/checkout.go:405-440`, `internal/gitengine/safe_git.go:288-304`, `internal/gitengine/repos.go:152-173`
- **Design, fetch, VM child or VM parent.** For a VM child, `publishVMHead` copies the commit's objects from the guest through the guest git service (`fetchVMObjects`, `maxVMHistoryBytes`), writes them to a per-session store on the server, then fetches into the workspace repo. For a VM parent, `vmFetchBranch` packs objects from the workspace repo (`packObjects`, haves = fork point and the parent's branch tip) and sends the pack through the guest git service (`Install`). The guest git service is a gRPC service on vsock port 1100. `internal/gitengine/vm_watch.go:383-435`, `internal/gitengine/vm_commit.go:144-173`, `internal/guest/config.go:21-23`
- **Design, how a child's checkout starts.** A container child's checkout is `git clone --local --no-checkout <workspace repo> <checkout>`; a VM child's disk comes from `git clone --no-hardlinks --no-checkout`. `internal/gitengine/checkout.go:163`, `internal/gitengine/vm.go:137`
- **Design, publish at start.** The parent's committed HEAD is published to the workspace repo and becomes `BaseCommit`; uncommitted changes are not included. `internal/children/agent.go:155-165`, `docs/coordination.md:1300-1302`

### (b) Guarantees

- **Design.** `child.start` is idempotent per `(parent, key)` and per request hash; a different request under the same key is `-32003`. `internal/children/agent.go:172-186`, `internal/children/agent.go:179-210`, `internal/store/children.go:127-131`
- **Design.** Queued, not refused, when a parent has `child_max_live` busy children; oldest first. A child that reported or waits on a human holds no slot. `internal/children/dispatch.go:23-60`, `docs/plans/2026-10-26-subsessions.md:81-90`
- **Design.** A restart returns a `launching` reservation without a run row to `queued` and marks one with a row `started`. `internal/children/service.go:191-210`
- **Design.** The child's report reaches the parent with the same durable at-least-once mail as peer messages; if the parent's inbox is full the report outbox retries. `docs/coordination.md:1321-1330`, `internal/coord/mailbox.go:801-805`
- **Design.** A handoff of either session detaches the child; the parent can then neither cancel nor fetch it. `internal/children/agent.go:66-68`, `internal/children/agent.go:282-300`
- **Design.** When the parent ends, working children are stopped and queued ones cancelled; a person closing the parent closes the children the same way. `internal/children/dispatch.go:167-263`, `docs/coordination.md:1355-1369`
- Delivery, notice timings and the failure-path table: PR #1130 file 2b sections 2 and 4.

### (c) Single-machine assumptions

- `internal/children/service.go:22-33`: `Runs` is the local scheduler; `LaunchWithOptions` starts the child's container or VM on this host.
- `internal/children/service.go:35-38`, `internal/gitengine/repos.go:124-173`: `Git` is the local git engine; its argument is a run id, and it resolves an on-disk checkout path and an on-disk workspace repo path (`e.existingCheckoutPath`, `e.existingRepoPath`, `internal/gitengine/engine.go:399-410`).
- `internal/gitengine/checkout.go:163`, `internal/gitengine/checkout.go:431`, `internal/gitengine/safe_git.go:296-303`, `internal/gitengine/repos.go:171`: the clone, the publish fetch (from the run's snapshot store) and the parent-side fetch all use local paths for source and destination.
- `internal/gitengine/vm_commit.go:159-160`, `internal/gitengine/vm_watch.go:403`: VM fetch and publish stream objects to or from the guest over vsock, brokered by the server.
- `internal/children/dispatch.go:109-110`, `internal/children/agent.go:282-288`, `internal/children/service.go:73-77`: the `AuthorizationMu *sync.Mutex` is shared in memory with the SSH launch path and the handoff path.
- `internal/children/service.go:88-96`, `internal/children/control.go:36-41`: reconcile ticker, kick channel and `settingsMu` are in-process.
- `internal/children/dispatch.go:56`: `go s.launch(ctx, c)` is a goroutine on this process; the `launching` state is not owned by a worker id.
- `internal/children/accounts.go:171-203`: "installed" and "signed in" are read from the member's home on this server through `Agents().ListAgents`.
- `internal/children/dispatch.go:281-379`: notice spells come from store queries plus `Runs.Paused(run)`, answered by the local scheduler.
- `internal/coord/provisional.go:21-67`, `internal/scheduler/provisional.go:21-45`: the provisional report is triggered by an in-memory timer on the scheduler that owns the child.
- `internal/gitengine/checkout.go:163`, `internal/gitengine/engine.go:131`: a container checkout is hardlinked from the workspace repo; the engine serializes snapshot work with in-process mutexes (`alternatesMu`).

## 6. status, peers and overlap warnings

### (a) Path

- **Design.** `coord.status` is served by `Service.Status`: it reads the run, memory and development capabilities, the child lineage, `statusPeers`, and the unread count. `internal/coord/mailbox.go:58-131`
- **Design.** Peers are parent and children (`lineage`), the radar's active and grace peers, then other live runs in the workspace the sender's owner may message, at most 32 with at most 16 files each. `internal/coord/peers.go:119-238`, `internal/protocol/coord.go:129-131`
- **Design.** File lists come from `run.diff` events. For a container session, a `diffWatch` in the server process watches the checkout directory on the server's disk with fsnotify (falling back to polling), snapshots on quiescence and publishes `RunDiffPayload` to the bus. `internal/gitengine/diffwatch.go:114-130`, `internal/gitengine/diffwatch.go:222-257`, `internal/gitengine/diffwatch.go:898-915`
- **Design.** For a VM session, the server follows the guest git service's watch stream and re-checks what the VM claims against the tree it fetched, then publishes the same event. `internal/gitengine/vm_watch.go:28-34`, `internal/gitengine/vm_watch.go:75-117`, `internal/gitengine/vm_watch.go:176-200`
- **Design.** `overlap.Index` consumes `run.diff` and `run.status` from the bus into an in-memory map `run -> {workspace, files, live}`, rebuilds from a full replay from sequence 0 at start, and computes overlap per `(workspace, path)`. It publishes `run.overlap`. `internal/overlap/index.go:63-79`, `internal/overlap/index.go:96-136`, `internal/overlap/index.go:205-239`, `internal/overlap/index.go:360-408`
- **Design.** `coord.radar` folds `run.overlap` events into grace windows and re-reads the index at most once per second. `internal/coord/radar.go:47-65`, `internal/coord/radar.go:74`, `internal/coord/coord.go:384-404`
- **Design.** Hooks and the Enhanced wake announce overlaps from `coord.hook.status` (`overlaps`, up to 32 sessions and 16 files each); an Enhanced session gets overlap text only inside a wake prompt while it works. `internal/coord/hook_wait.go:80-101`, `internal/coord/acp_wake.go:140-171`, `docs/coordination.md:379-383`
- **Design.** Each peer carries the other session's activity and wait from the scheduler (`Waits.Wait`). `internal/coord/peers.go:173-175`, `internal/scheduler/wait.go:204-213`

### (b) Guarantees

- **Design.** Advisory only: nothing locks, pauses or arbitrates. `internal/coord/coord.go:1-18`
- **Design.** A snapshot fires after a quiet period, at least `MinInterval` apart, and unconditionally after `MaxInterval`; the VM watch is limited to a burst of 2 snapshots per `MinInterval`. `internal/gitengine/diffwatch.go:36-41`, `internal/gitengine/vm_watch.go:50-61`
- **Design.** Dropped bus events are recovered by a replay from the last cursor; at start the index replays the whole `run.diff` history. `internal/overlap/index.go:116-136`, `internal/overlap/index.go:155-200`
- **Design.** Two runs overlap only when workspace and path both match. `internal/overlap/index.go:360-380`

### (c) Single-machine assumptions

- `internal/gitengine/diffwatch.go:114-130`, `internal/gitengine/diffwatch.go:222-257`: fsnotify on a local directory (`e.existingCheckoutPath(run)`); the inotify budget is shared with the run containers.
- `internal/overlap/index.go:63-79`: the index is a single in-memory map; there is no durable overlap state ("the index holds no durable state").
- `internal/overlap/index.go:96-114`, `internal/overlap/index.go:121-136`: rebuild is a full replay of `run.diff` from the single event log.
- `internal/events/inproc.go:20-27`: `run.diff`, `run.overlap` and `run.status` reach the index and the radar through the in-process bus.
- `internal/coord/radar.go:47-65`: grace windows are an in-memory map in the coordination service.
- `internal/server/svc_coord.go:40`, `internal/server/svc_coord.go:94-105`: the radar is looked up through `lazyRadar` on the same server's `sshd.Config`.
- `internal/coord/peers.go:119-168`: `statusPeers` joins the store's live run list with the index and the scheduler's wait text.
- `internal/gitengine/diffwatch.go:898-915`: `publishRunDiff` publishes straight to the local bus; a `Relay` seam exists (`internal/gitengine/engine.go:82-90`) but the coordination code does not use it.

## 7. Team memory (add, search, pin, brief)

### (a) Path

- **Design.** In the session, `aether-internal memory ...` or MCP `aether_memory_search` and `aether_memory_add` send `memory.add|search|show|brief` on the session socket. `internal/mcpbridge/memory.go:16`, `internal/mcpbridge/memory.go:65-108`, `internal/coordcli/memory.go`
- **Design.** `handleMemory` rejects identity fields, refuses a finished session, calls `Memory.HandleAgent`, which fills workspace, author, harness and commit from the run. `internal/coord/memory.go:40-75`, `internal/memory/agent.go:36-79`
- **Design.** `memory.add` checks push permission, kind and caps, runs a gitleaks scan (`secretscan.Scan`), takes the `agentWrites` mutex, counts the run's agent notes (limit 20), reads the run's latest HEAD from the `run.diff` events in the event log, inserts into `memory` and publishes `memory.change`. `internal/memory/memory.go:68-181`, `internal/memory/memory.go:296-325`
- **Design.** Search is SQLite FTS5: `memory_fts MATCH ?` ordered by `bm25(memory_fts, 4.0, 1.0, 2.0) / (1 + age_days/30)`. `internal/store/memory.go:422-470`, `internal/store/migrate.go:1877`
- **Design.** Pin, unpin and remove are member methods over SSH, not on the session socket. `internal/coord/memory.go:12-21`
- **Design.** The brief is built by `Brief`: pinned titles, up to 5 sessions finished in the last 48 hours, live-session and note counts, at most 3072 bytes. It reaches a new session once at launch, inside the run instructions (`briefedInstructions`), or on demand through `memory.brief`. `internal/memory/brief.go:26-90`, `internal/scheduler/coauthors.go:55-74`, `docs/memory.md:42-71`

### (b) Guarantees

- **Design.** Caps: text 1024 bytes, title 120, 20 files of 512 bytes, 20 agent notes per session, search limit 100. PR #1130 file 2b section 5; `docs/coordination.md:1201-1214`.
- **Design.** A duplicate text returns the existing note; an agent may replace only an unpinned note an agent wrote. `docs/coordination.md:1204-1214`
- **Design.** Agents cannot pin (`-32001`); notes are framed as data, not instructions. `internal/memory/memory.go:86-93`, `internal/memory/brief.go:20-24`
- **Design.** Notes live in the server's database and are searched by SQLite full text, with no embeddings. `docs/memory.md:9-10`, `docs/plans/2026-10-19-team-memory.md:20-23`, `docs/plans/2026-10-19-team-memory.md:104`

### (c) Single-machine assumptions

- `internal/store/memory.go:422-470`, `internal/store/migrate.go:1877`: search and brief are SQL against one SQLite database with an FTS5 virtual table.
- `internal/memory/memory.go:50-55`, `internal/memory/memory.go:105-110`: the per-run note limit is enforced by an in-process mutex around count-then-insert.
- `internal/memory/memory.go:296-325`: the cited commit comes from paging the event log backwards for `run.diff`, so memory depends on the single event log and on the diff watch (section 6).
- `internal/scheduler/coauthors.go:55-74`: the brief is built by the scheduler at launch, in the same process, from `Memory.Brief`.
- `internal/memory/memory.go:42-48`: `Bus` and `Log` for `memory.change` are the in-process bus and event log.

## 8. The session MCP tools

### (a) Path

- **Design.** The MCP server is the staged server binary run as `/opt/aether/aether-server mcp`: a stdio process started by the agent harness inside the session. For Enhanced sessions the server passes it in ACP `session/new`, `session/resume` and `session/load`; elsewhere it is manual. `internal/scheduler/acp_driver.go:585-591`, `cmd/aether-server/main.go:85-86`, `docs/mcp-bridge.md:37-60`
- **Design.** It talks only to `/run/aether/coord3.sock`, dialing per tool call, and holds no state that a restart could invalidate. Tools: `aether_status`, `aether_send`, `aether_inbox`, `aether_ask`, `aether_reply`, `aether_report`, `aether_memory_search`, `aether_memory_add`, and, when `child.options` is in the status capabilities, four subsession tools. `internal/mcpbridge/mcpbridge.go:1-9`, `internal/mcpbridge/mcpbridge.go:51-84`, `internal/mcpbridge/tools.go:19-26`, `internal/mcpbridge/children.go:16-23`
- **Design.** The bridge's instructions are the role card from one `coord.status` call with a 2 s limit. `internal/mcpbridge/mcpbridge.go:89-103`
- **Design.** The acknowledgement token is promoted only after the response is written to the MCP stream, keyed by the JSON-RPC call id; a bridge killed mid-call leaves the batch unacknowledged. `internal/mcpbridge/gate.go:13-57`
- **Design.** A separate assistant tool set uses `/run/aether-assistant/assistant.sock` in a member's environment terminal. `internal/mcpbridge/assistant.go:12-14`

### (b) Guarantees

- **Design.** Same wire guarantees as the CLI: the tool maps to the socket call; MCP callers must supply the idempotency key. `docs/coordination.md:286-292`
- **Design.** At most 8 staged inbox tokens are held for unwritten responses; dropping one costs one redelivered batch. `internal/mcpbridge/gate.go:60-65`

### (c) Single-machine assumptions

- `internal/mcpbridge/mcpbridge.go:51-56`, `internal/coordtransport/paths.go:35`: the socket path is the in-session mount path; the bridge needs the server's socket to appear there.
- `internal/scheduler/acp_driver.go:585-591`: the command `/opt/aether/aether-server` is the host-staged binary mounted into the session.
- `internal/mcpbridge/gate.go:42-65`: token staging state is in the bridge process; it is per session and local to it.
- Tools are a thin layer: every cross-machine assumption of the tools is inherited from sections 1 to 7.

## 9. Watches and PR or CI waits that call GitHub

### (a) Path

- **Design.** The process that polls is the server's `watch.Service`. The `PRs` source reads the pull request and its head's checks with the watch owner's GitHub login; for a session wait the owner is the member whose home the session runs in (its launcher, even after a handoff). `internal/watch/pr_source.go:71-79`, `docs/watches.md:405-420`
- **Design.** Credentials: `github.Service.Credentials` reads the member's native `gh` credential file from the member home on the server's disk (`homes.ReadCredential`), then verifies it against `/user` on every call; `PRs` caches the verified token per member for 10 minutes. `internal/github/github.go:65-91`, `internal/watch/pr_source.go:35`, `internal/watch/pr_source.go:178-195`
- **Design.** Polling, not webhooks: "GitHub sends Aether no webhooks". 1 min while checks run or a review is requested, 5 min otherwise. `internal/watch/pr_source.go:22-28`
- **Design.** GitHub reads are limited to one in flight per member and 4 across members; a GitHub rate limit is remembered per member until reset. `internal/watch/pr_source.go:37`, `internal/watch/pr_source.go:71-79`, `internal/watch/pr_source.go:156-175`
- **Design.** Delivery of a session wait is the inbox write described in section 4. Member watches also deliver to Web Push and the assistant. `internal/watch/service.go:67-77`, `docs/watches.md:145-183`
- **Design.** The agent's own `gh` inside the session is separate: it uses the credentials mounted into that session, not this path. `docs/coordination.md:1057-1085`

### (b) Guarantees

- **Design.** Evaluation failures retry with backoff (to 15 minutes); a wait whose source is unreadable for 1 hour is broken and the session needs you. `internal/watch/service.go:39-40`, `internal/scheduler/wait.go:50-54`
- **Design.** A pull request read serves every watch on the same pull request for 15 s. `internal/watch/pr_source.go:29-32`
- **Design.** Watches expire after 14 days by default (maximum 90); a wait after 7 days. `internal/watch/service.go:36-38`, `internal/watch/wait.go:24`
- PR #1130 file 2b section 3: the `merged` queue-read gap, `checks` outcomes and expiry.

### (c) Single-machine assumptions

- `internal/github/github.go:67-79`: the credential is read from the member home on the server's filesystem.
- `internal/watch/pr_source.go:71-100`: `PRs.members`, the per-member turn channel, the global slot channel, token cache and rate-limit memory are in-process.
- `internal/watch/service.go:98-123`, `internal/store/watch_notify.go:228-242`: one service and one cursor row per database.
- `internal/watch/service.go:60-65`: run state (`Paused`, pending inputs) comes from the local scheduler.
- `internal/watch/wait.go:522-562`: the wake needs `coord.Service.NotifyRun` in the same process (section 3).
- Outbound traffic only: the poller makes HTTPS calls from the server host to GitHub; no inbound path to the host is used. `internal/watch/pr_source.go:22-28`

## 10. `/run/aether/co-authors` and other files the server writes into a session

### (a) Path

- **Design.** `<data>/coord/<run>/co-authors` holds one `Co-authored-by` trailer per steerer and owner (less the container's own author). The scheduler rewrites it whenever a steerer is added, a handoff happens or a member's identity changes, by `WriteCoAuthors`, which renames a temp file over it. The agent is told to read it before each commit. `internal/scheduler/coauthors.go:30-37`, `internal/scheduler/coauthors.go:375-402`, `internal/coord/socket.go:136-203`
- **Design.** The same directory carries what `Provision` writes at launch: the harness assets (status settings, extensions, an instructions file such as `instructions.md`, OpenCode discovery files) and `next-command`, a file the in-container supervisor reads to decide what to run after a mode switch or a VM resume. `internal/coord/socket.go:85-124`, `internal/scheduler/coordination.go:150-185`, `internal/scheduler/supervisor.go:15-31`, `internal/scheduler/mode_switch.go:299`, `internal/scheduler/mode_switch.go:362`, `internal/scheduler/sleep_vm.go:373`, `internal/harness/harness.go:362-378`
- **Design.** Screenshots and captures are written by the server under `<coordDir>/captures` and appear as `/run/aether/captures/<id>.png`. `internal/scheduler/development_artifact.go:28-46`, `internal/scheduler/development_artifact.go:162`
- **Design.** The server also sets these environment variables in the session: `AETHER_RUN_ID`, `AETHER_ENHANCED`, `AETHER_MANAGED_HOOKS`, `AETHER_BACKGROUND`, `AETHER_IDLE_WAKE`, `AETHER_ROLE_CARD`, `AETHER_HARNESS`. `internal/coordtransport/paths.go:36-60` (`RunIDEnv` at :38, `HarnessEnv` at :56)
- **Design.** Inside the session, files the CLI and hooks keep: `$TMPDIR/aether-inbox-<run>-ack`, `$TMPDIR/aether-hook-<run>-<harness>.json`, `-claude-wake`, `-claude-woken.json`. `internal/coordcli/inbox.go:25`, `internal/coordcli/hooks.go:225`, `internal/coordcli/hook_claude_wake.go:26-40`
- **Design.** The sidecar `<state>/<run>.json` holds `CoordDir`, bridge digest and path, reporter and wake flags and the mode; it is how a restarted server finds the run's directory. `internal/scheduler/state.go:416-456`, `internal/scheduler/development_artifact.go:28-46`

### (b) Guarantees

- **Design.** `co-authors` is replaced by rename, so a reader gets the whole old or whole new list; it is read-only to the container (mode 0444, directory mounted read-only). `internal/coord/socket.go:136-170`, `internal/coord/socket.go:172-203`
- **Design.** `commitAll` on the server also appends the trailers to the server's own `wip:` and `aether:` commits from the store, independent of the file. `internal/scheduler/coauthors.go:209-246`
- **Design.** The Docker mount is read-only; in a VM the directory is served by the host file service over vsock (`aether.mount=home,...`) so a rewrite is visible without remounting. `docs/vm-guest.md:214-224`, `internal/runtime/vm/share.go:106-114`, `internal/guest/home/home.go:1-17`

### (c) Single-machine assumptions

- `internal/coord/socket.go:136-203`, `internal/scheduler/coauthors.go:395-402`: written by the server onto the server's local disk; there is no push to a remote session.
- `internal/scheduler/coauthors.go:375-394`: `coordDir` and the per-run `coAuthorMu` live on the in-memory run entry.
- `internal/scheduler/development_artifact.go:28-46`: capture location is derived from the local coordination directory.
- `internal/runtime/vm/share.go:106-114`, `internal/guest/home/home.go:1-17`: for VMs the directory is served by a host file service on a vsock port reached through a Cloud Hypervisor socket on the same host.
- `internal/scheduler/coordination.go:597-643`: collection of staged binaries scans local sidecar files and the local `<data>/runtime/bin`.

## 11. `dev.terminal`, `dev.browser` and artifacts through the same channel

### (a) Path

- **Design.** These are `dev.*` methods on the same session socket. The coordination service checks an explicit allow-list, rejects identity fields and calls `Development.HandleAgent`, which is `Scheduler.HandleAgent` then `CallDevelopment`. `internal/coord/socket.go:610-665`, `internal/scheduler/development.go:159-171`, `internal/scheduler/development.go:264-330`
- **Design, wake.** A call that starts work (terminal start, control acquire, browser open and the like) first wakes the run with `holdAwake`, which starts a stopped container or VM on the local runtime. `internal/scheduler/development.go:279-300`, `internal/scheduler/sleep.go:159-181`
- **Design, terminals.** The server starts the process through `runtime.ManagedExecRuntime` (`StartExecTTY`) in the session's container, under a terminal keeper reached through a bridge exec; the terminal list is persisted as JSON under the server's state directory. `internal/scheduler/dev_terminal.go:93-130`, `internal/scheduler/dev_terminal.go:454-470`, `internal/scheduler/dev_terminal_keeper.go:119-126`
- **Design, browser.** The `browser.Manager` keeps a journal directory per session on the server and talks to the companion over HTTP on a unix socket at `<journal>/control/browser.sock`. For Docker the companion container runs with `NetworkMode: container:<run container>`, sharing the session's network namespace. For a VM the server relays that host socket to vsock port 2001 in the guest. `internal/browser/manager.go:113-116`, `internal/browser/manager.go:205`, `internal/browser/client.go:18-30`, `internal/runtime/docker_browser.go:56`, `internal/runtime/vm/browser.go:154`, `internal/guest/config.go:33-36`
- **Design, artifacts.** `dev.artifact.*` and `dev.terminal.screenshot` write PNGs and metadata into the capture directory (section 10); `artifact retain` copies them into evidence on the server. `internal/scheduler/development.go:317-320`, `internal/scheduler/development_artifact.go:28-46`, `docs/coordination.md:1023-1055`
- **Design, control.** `dev.control.*` leases are held by `control.Service`; input is fenced by lease generation. `internal/scheduler/development.go:309-310`, `internal/control/lease.go:152-177`

### (b) Guarantees

- **Design.** At most 4 agent-started terminals per session; retained and transient captures are each bound to 64 captures and 128 MiB per session, 8 MiB per capture; requests and responses stay within 64 KiB. `docs/coordination.md:886-889`, `docs/coordination.md:1036-1038`, `internal/scheduler/development_artifact.go:23-24`
- **Design.** Stale fences return `-32003`; a request sent without an idempotency key is not retried after the connection fails. `docs/coordination.md:322-329`, `docs/coordination.md:975-983`
- **Design.** A server restart leaves terminals and their keeper running; the next server process reattaches. `docs/coordination.md:908-912`

### (c) Single-machine assumptions

- `internal/scheduler/development.go:279-300`: waking the run is a call into the scheduler that owns the run entry, on the host that runs the container.
- `internal/scheduler/dev_terminal.go:93-101`, `internal/scheduler/dev_terminal.go:454-470`: terminals need the local runtime's managed exec and a local PTY host.
- `internal/scheduler/dev_terminal.go:103-130`: the terminal list is a JSON file in `StateDir`.
- `internal/browser/manager.go:113-116`, `internal/browser/manager.go:205`: browser state and its control socket are on the server's disk.
- `internal/runtime/docker_browser.go:56`: the companion joins the run container's network namespace, so both must be on the same Docker daemon.
- `internal/runtime/vm/browser.go:154`: for VMs the control socket is relayed to the guest by vsock through the local Cloud Hypervisor socket.
- `internal/scheduler/development_browser.go:90-99`: browser capacity is reserved against this host's resources (`reserveCapacity`).
- `internal/scheduler/development_artifact.go:28-46`: captures and retained artifacts live under the local coordination directory.
- `internal/control/lease.go:152-177`: the lease is held in memory; only surface holder and generation are persisted.

## Summary table

"yes" means the mechanism, as coded, depends on it. "Host network" asks whether it needs the session and server to share an IP network or network namespace; the transports below are unix sockets, vsock, bind mounts or Docker network namespaces, not TCP between the session and the server.

| Mechanism | Source of truth today | Transport from session to server | Same process | Same database | Same filesystem | Same host network |
| --- | --- | --- | --- | --- | --- | --- |
| 1. Session authentication | The per-session unix socket and listener map; run and owner read from SQLite | `/run/aether/coord3.sock`; VM: guest relay to vsock to `<vsock socket>_<N>` to host socket | yes: `coord.go:205-234`, `socket.go:370-385` | yes: `mailbox.go:510-519` | yes: socket and `<data>/coord` on server disk, `socket.go:335-385`, `coordination.go:218` | no (no IP); local IPC only: `share.go:51-55` |
| 2. send / ask / reply / inbox / ack | `run_messages` rows with delivery tokens | Coordination socket, NDJSON JSON-RPC | yes: inbox waiters and buckets, `mailbox.go:1329-1414` | yes: `store/coord.go:312-351`, `sqlite.go:52-53` | yes: ack token only in container `$TMPDIR`, `coordcli/inbox.go:25` | no |
| 3. Wakes | Unread rows plus in-memory owed-wake sets; scheduler run entry | Hook long poll on the socket; ACP prompt through keeper bridge exec; Codex app-server exec | yes: `acp_wake.go:50-112`, `hook_wait.go:161-188`, `svc_coord.go:58` | yes for unread ids, `acp_wake.go:114-130` | yes: container-local claim files and local runtime exec, `hook_claude_wake.go:26-40`, `acp_keeper.go:195-212` | no for the wake; yes for exec: managed exec on the local runtime |
| 4. Reports and waits | `coord_reports`, `coord_state_reports`, `watches` rows; scheduler run entry judges UI state | Coordination socket (`coord.report`, `run.report`); hooks run `aether-server report` | yes: `report.go:269-326`, `coord.go:495-504`, `watch/service.go:98-123` | yes: `watch_notify.go:228-242`, `mailbox.go:528-725` | yes: evidence reads the checkout and PTY transcript, `mailbox.go:675-682` | no (GitHub is outbound HTTPS only) |
| 5. Subsessions | `run_children` rows; child is a session on the same scheduler; branches in the workspace bare repo | `child.*` on the socket; child launch is a Go call; fetch is local git or vsock git service | yes: `dispatch.go:56`, `dispatch.go:109-110`, `service.go:22-33` | yes: `store/children.go:121-163` | yes: `checkout.go:163`, `repos.go:171`, `vm_commit.go:159-160` | no (local paths and vsock) |
| 6. status, peers, overlap | In-memory `overlap.Index` fed by `run.diff` events; radar grace map | Socket (`coord.status`, `coord.hook.status`); diff watch is server-side fsnotify or guest git stream | yes: `overlap/index.go:63-79`, `radar.go:47-65`, `inproc.go:20-27` | yes: replay from the event log, `index.go:96-136` | yes: fsnotify on the checkout, `diffwatch.go:222-257` | no |
| 7. Team memory | `memory` table and FTS5 index in SQLite | Socket (`memory.*`) | yes: `memory.go:54`, `memory.go:107` | yes: `store/memory.go:422-470` | no (nothing read from disk) | no |
| 8. Session MCP tools | None of its own; stateless stdio bridge over mechanisms 1 to 7 | stdio MCP to socket, `/opt/aether/aether-server mcp` | no (runs in the session); every call lands in the server process | yes (via the socket calls) | yes: staged binary and socket mounted from the server, `acp_driver.go:585-591`, `coordination.go:385-433` | no |
| 9. Watches and PR or CI waits | `watches`, `watch_notifications`, `watch_deliveries`, `watch_cursor`, `watch_pr_snapshots` | None from the session except the `report --until` call; server polls GitHub | yes: `pr_source.go:71-100`, `service.go:98-123` | yes: `watch_notify.go:228-242` | yes: GitHub token read from the member home on server disk, `github.go:67-79` | no (outbound HTTPS to GitHub) |
| 10. `co-authors` and other written files | Store (steerers) rendered to `<data>/coord/<run>/co-authors`; harness assets; `next-command`; `captures/` | Files in the read-only `/run/aether` mount (Docker bind mount; VM host file service over vsock) | yes: `coauthors.go:375-402` | yes for steerers, `coauthors.go:191-207` | yes: `socket.go:136-203`, `guest/home/home.go:1-17` | no |
| 11. `dev.terminal`, `dev.browser`, artifacts | Scheduler run entry, state-dir JSON, browser journal dir, capture dir | Socket (`dev.*`); server drives the container through managed exec, the browser through a unix socket | yes: `development.go:264-330` | partly: terminals and captures are files, not tables | yes: `dev_terminal.go:103-130`, `browser/manager.go:113-116`, `development_artifact.go:28-46` | yes for the browser: `docker_browser.go:56` (shares the run container's netns) |

## Not established

- **Not established:** whether anything outside `internal/coord`, `internal/scheduler` and `internal/server` holds a long-lived reference to a `coord.Service` method (for example connector or assistant code). Searched only for the callers listed in `svc_coord.go`.
- **Not established:** the exact behavior of a wake against a busy run lock. `docs/coordination.md:410-413` says a wake waits up to 2 s (native) or 20 s (Enhanced) for a busy lock and also says "Busy session control refuses it at once"; the admission code uses `TryAdmit`, which returns `ErrAdmissionBusy` immediately (`internal/control/lease.go:345-372`, `internal/server/svc_coord_wake.go:57-61`). Whether another path waits was not traced.
- **Not established:** the guest-side git service and file service (`internal/guest/helper`), and what they would do if the host socket moved to another host. Only the host-side callers and `docs/vm-guest.md` were read.
- **Not established:** what the pi, oh-my-pi and OpenCode native integrations do beyond calling `hook <harness> wake`; the embedded extension sources were not read.
- **Not established:** which `acphost` per-session state (conversation log, queue) is on disk versus in memory. `internal/acphost` was not read beyond the calls the scheduler makes; section 3 relies on `scheduler/acp_driver.go` and `scheduler/acp_session.go`.
- **Not established:** how `attention.list` and the `cloudfield wait` event subscription are served (PR #1170 is client-only; the server methods were not read).
- **Not established:** how a restarted server rebuilds the in-memory `enhancedWoken`, `enhancedOpens` and `mailRefusals` sets: no persistence was found in `internal/coord`, and nothing restores them in the files read, so after a restart a wake may be offered once more for mail already announced. Not run.
- **Not established:** whether `internal/ompbroker` (the omp auth broker with `broker.sock`) participates in coordination; only its socket name was seen.
- **Not established:** behavior of the open PRs after merge; #1150, #1151, #1165, #1170 and #1179 were read as descriptions only and may change `Wake`, `ProvisionalReport`, `turnReport` and `HoldTurnEnd` details cited here.
- **Not established:** any measurement of coordination latency, wake latency or throughput on the current design; no live run was made. Wake latency figures are quoted only through PR #1130 file 2b section 4 (documented there as doc-reported).
