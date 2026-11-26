# 2a. What a Cloudfield subsession is and costs

Sources are this repo at `81d14a23b` and commands run in this session. Tags: MEASURED, ADVICE, OBSERVED-ONCE, DESIGN-FACT.

## 1. Start path and resource bounds

| Claim | Evidence | Source | Tag |
|---|---|---|---|
| `start` is synchronous only up to a reservation. It validates the request, resolves policy, publishes the parent's branch, reserves a row and returns `queued`. Launch happens later, in a goroutine. | `base, err := s.cfg.Git.PublishRunBranch(...)` then `ReserveChild`, `s.Kick()`. `dispatch` runs `go s.launch(ctx, c)` | internal/children/agent.go:154-176; dispatch.go:46-57 | DESIGN-FACT |
| At dispatch the launch is re-admitted: policy, then the workspace cost budget, then usage-fallback measurement and authorization, then `LaunchWithOptions` with the reserved ID. | `cost.Admit`, `fallback.Measure`, `s.cfg.Authorize`, `fallback.Select` | dispatch.go:63-150 | DESIGN-FACT |
| Launch order: login check, capacity reservation, base taken from the parent's commit (`FromCommit`, so no mirror fetch), run row, checkout, profile pin, environment plan, access recheck, container or VM create and start (setup script included), diff watch, `running`. | | internal/scheduler/launch.go:315-421, 476-556 | DESIGN-FACT |
| Launches by one member are serialized. An exclusive per-member cache lock is held from profile pin until the container has started. | `lockRunCaches(run.HomeMember(), run.AccountMember())`, released after `writeSidecar` | launch.go:493-494, 699-711; internal/memberhome/cache.go:69-72 | DESIGN-FACT |
| The setup script has no time limit, and it blocks the same member's other launches. | "There is no time limit. The launch waits for it, and so does another launch by the same member or on the same agent account" | docs/environments.md:582-584 | DESIGN-FACT |
| Docker runtime only: an ownership pass costs time per launch under the member's locks. VMs skip the pass. | "188k entries ... 2.8s and 3.2s per launch ... so concurrent launches of one member queued", after the fix "0.47s and 0.55s"; VMs: "No ownership pass runs over the home" | `git show 8a65c5299` (#683); environments.md:404 | MEASURED (by the commit author) |
| Checkout disk in a VM: the first session on a base builds a prepared disk, and each later one gets a qcow2 overlay. | "1.7 seconds for 40,000 files on local NVMe ... constant time (17 ms)" | docs/vm-guest.md:598-600 | MEASURED (doc) |
| VM boot is bounded but has no stated typical time. | `bootTimeout = 90 * time.Second` | internal/runtime/vm/engine.go:56 | DESIGN-FACT |
| Default per-session ceilings | CPUs `min(8, NumCPU)`, memory `8<<30`, pids `4096`. The order is run override, then the admin's dashboard limits, then flags. | internal/scheduler/capacity.go:68-91; docs/environments.md:287-303 | DESIGN-FACT |
| A subsession does not inherit the parent's per-run `Limits` or `Effort`. `LaunchWithOptions` from children passes Model, Permission and extras only, and `ChildStartParams` has no effort or limits field. | | dispatch.go:131-134; internal/protocol/children.go:97-105 | DESIGN-FACT |
| VM sizing | vCPUs = `min(host, ceil(limit))` with `CPUQuota`. Guest RAM = limit + limit/32 + 432 MiB. Unit `MemoryMax` adds 512 MiB, with `MemorySwapMax=0`. Data disk 128 GiB sparse. | engine.go:50-67, 307-326, 625-635; environments.md:365-376 | DESIGN-FACT |
| Admission, memory | MemAvailable must cover `max(2 GiB, total/10)`, plus 512 MiB per launch, plus claims not yet taken. Memory ceilings are not counted. | capacity.go:36, 111-113, 214-219; docs/failure-handling.md:326-335 | DESIGN-FACT |
| One measured VM footprint | "a 2 GiB session VM held 159 MiB of the host once booted" | failure-handling.md:333-335 | MEASURED (doc) |

**This session (OBSERVED-ONCE).** `nproc` = 1. `free -m`: 8633 MiB total, 2047 MiB swap. It is a session VM: PID 1 is `init`, the cmdline holds `aether.*` keys, `aether.memory=8589934592,2147483648`, `/sys/fs/cgroup/session/memory.max` = 8589934592 and `pids.max` = 4096. That matches the default 8 GiB, plus a quarter as swap. I could not establish why it has 1 vCPU (a low admin CPU limit or a 1-CPU host). `cpu.pressure` read `some avg10=50.37 avg60=20.59` while 8 parallel collector subagents ran, so the subagents were CPU-starved.

## 2. What a subsession shares with its parent

| Claim | Evidence | Source | Tag |
|---|---|---|---|
| It starts from the parent's last commit, published by the server. Uncommitted work is left out. | "publishes the parent's branch and checks the subsession out from that commit; uncommitted changes are not included" | docs/coordination.md:1300-1302 | DESIGN-FACT |
| It gets its own branch: `cloudfield/run-<slug of task>-<id>` | `runBranchPrefix = "cloudfield/run-"`, `runBranch(prefix, task, id)` | internal/gitengine/slug.go:60-72 | DESIGN-FACT |
| It gets its own sandbox, checkout and branch, and is owned by the parent's owner. | | coordination.md:1259-1261 | DESIGN-FACT |
| It inherits the parent's extra repos and permission tier. | `Permission: parent.Permission`, `ExtraWorkspaces: workspaces[1:]` | dispatch.go:131-133; coordination.md:1302-1303 | DESIGN-FACT |
| It shares the owner's home, saved image and caches. `/aether-cache` is a copy-on-write layer over the member's newest caches. | | environments.md:19-23, 385-387 | DESIGN-FACT |
| It gets the workspace's variables and secrets. It does not get the parent's transcript. | "gets the workspace's environment and secrets like any session of the same owner; it does not get the parent's transcript" | docs/plans/2026-10-26-subsessions.md:147-149 | DESIGN-FACT |
| Account default: the parent's agent runs on the parent's account, and any other agent on the owner's own account. `--account` accepts an account shared with the owner. | `case req.Harness == p.parent.Harness ...: account = p.parent.AccountMember()` | internal/children/accounts.go:182-188; teams.md:795-804 | DESIGN-FACT |

## 3. Limits, modes, accounts

| Claim | Evidence | Source | Tag |
|---|---|---|---|
| Depth is 1. | "is itself a subsession of %s, and subsessions go one level deep" | internal/children/policy.go:55-56 | DESIGN-FACT |
| `max_live` defaults to 8 per parent and an admin can set 1..64. Starts past it queue, oldest first. There is no total limit. | `DEFAULT 8`; `MaxChildLimits = {MaxLive: 64}`; "There is no depth or total limit" | internal/store/migrate.go:2069; internal/domain/children.go:31; control.go:232-236; plans/2026-10-26-subsessions.md:77-82 | DESIGN-FACT |
| A slot is busy only while the child is launching, provisioning, or running with activity working / waiting-ci / waiting-tools / disconnected. A child that has reported, or is waiting on a human, holds no slot. | `childBusy` SQL | internal/store/children.go:93-95 | DESIGN-FACT |
| A parent can briefly exceed `max_live`. Budgets admit runs but do not stop running ones. | | plans/2026-10-26-subsessions.md:141-144 | DESIGN-FACT |
| Refusals | Parent is Background; parent is a subsession; parent was handed over; parent's agent cannot wake in Standard; children off for the session or the member; choice turned off; agent not installed or signed out; budget. | policy.go:50-66; accounts.go:189-203; dispatch.go:90-99 | DESIGN-FACT |
| Agents that cannot wake in Standard: copilot, gemini, cursor and antigravity have no `IdleWake`. claude, codex, pi, omp and opencode do. | | internal/harness/harness.go:438-610; capabilities.go:61-70 | DESIGN-FACT |
| Modes are standard, enhanced and background. The default is Enhanced where that agent's adapter is installed on the account. | `modeNames`; `DefaultMode` | accounts.go:23-26, 112-117, 207-213 | DESIGN-FACT |
| Model: `--model` is validated by `domain.ValidModel`. `options --harness` lists models. There is no effort flag. | | accounts.go:178-180; agent.go:103-118 | DESIGN-FACT |
| A choice counts as "exhausted" at 95% of quota, and fallback moves the child to another account. Only Claude Code and Codex report usage or move. | `ExhaustedPercent = 95` | internal/fallback/fallback.go:23-26; harnesses.md:1006-1007; teams.md:850-856 | DESIGN-FACT |
| Live options for this session | `allowed:true, max_live:8, busy:4, queued:0`. claude was `ready` (Enhanced) on the owner's account and on 2 shared accounts. On one shared account omp and cursor were `ready`. Codex, opencode, gemini, pi, copilot and antigravity were `not-installed` everywhere. | `aether-internal subsession options` | OBSERVED-ONCE |
| Advice the product gives agents | "Reserve a subsession for a large, independent stream of work that needs its own branch or pull request ... would run for hours; or needs another agent, model, or harness". Subagents "share your checkout, start in seconds". | `aether-internal skill subsessions`; coordination.md:644, 1252-1257 | ADVICE |
| A "/fast on" mechanism is **not in this repo**. | `grep -rn -i '/fast'` over internal, docs, cmd and web/src matched only "Fast-forwarded" in a test | command | DESIGN-FACT (absence) |

## 4. Admission under load, "coordination is not reachable", retries

| Claim | Evidence | Source | Tag |
|---|---|---|---|
| PSI gate: a launch is refused when the server cgroup's `memory.pressure some avg60` is 10% or more. It is skipped when the server runs in the root cgroup or has no PSI. | `maxMemoryPressure = 10.0` | capacity.go:41-43, 350-370; failure-handling.md:345-350 | DESIGN-FACT |
| On a memory shortfall the server tries one reclaim or save of idle VMs, then refuses. It never evicts running work. | "Never loop" | capacity.go:23, 196-210 | DESIGN-FACT |
| A refused queued child is set `refused` and the parent is notified. Nothing retries it. | `"Subsession %s (%s) did not start: %s"` | dispatch.go:64-73 | DESIGN-FACT |
| Idle sleep: VMs are saved after 5m idle, containers after 30m (Enhanced Claude only). A session waiting for CI or another session counts as idle, while one running subagents stays awake. | | failure-handling.md:766-791, 856-866, 1129 | DESIGN-FACT |
| "coordination is not reachable" comes from the agent-side CLI/MCP transport when the run socket fails: dial refused or missing, EOF, or the call's own timeout. Restart-type failures retry for up to 60 s, with backoff from 100 ms to 2 s. A missing socket gets a 2 s grace. A call that hit its own deadline (30 s default, `child.start` included) is not retried. | `unavailable`, `ambiguous`, `unavailableSent`, `unavailableUnsent` | internal/coordtransport/client.go:27-73, 147-210, 306-311; retry.go:20-31, 43-48, 84-106; coordcli/sudo.go:117-129 | DESIGN-FACT |
| Retrying `child.start` is safe. The CLI always sends an idempotency key, and the server returns the first receipt. | `mutationKey("child", ...)`; `priorStart` | coordcli/children.go:106-117; agent.go:137-139, 179-193 | DESIGN-FACT |
| The per-run request budget is shared by everything in the session, subagents included. | `requestBurst = 30`, `requestRefill = time.Second`; send `5` per `5s`; inbox `10` per `1s` | internal/coord/mailbox.go:22-44, 227-249 | DESIGN-FACT |
| That budget was hit here. My first `skill subsessions` call, while 8 collectors ran, returned `coord: transport request rate limit exceeded (burst 30, 1 request per 1s) [aether error -32003]`. The code is -32003, the same as an idempotency conflict. | | command output; mailbox.go:240-249 | OBSERVED-ONCE |

## 5. What an in-session subagent or workflow gets, derived from sizing

- **One machine.** It shares the session's single cgroup and VM: CPU = the run's CPU limit, here 1 vCPU and 50% CPU-pressure avg10 with 8 subagents. Memory = one `memory.max` (8 GiB here), and pids = 4096 for the whole session (capacity.go:68-73; observed). An OOM kills "a tool, the agent or the shell" in that one session (failure-handling.md:36). DESIGN-FACT + OBSERVED-ONCE
- **One checkout and branch**, at `/workspace`, with no isolation between subagents except git worktrees they make themselves. This session's tree has `.claude/worktrees/agent-*`, so grep hits duplicate there. OBSERVED-ONCE
- **One identity, account and quota.** A single `AETHER_ACCOUNT_MEMBER_ID` and one vendor login, so all subagents draw on one quota. A subsession can use another shared account and quota (accounts.go:182-188; teams.md:638-640). DESIGN-FACT
- **One coordination budget**: 30 burst, then 1 request/s per run (mailbox.go:42-44). Observed being exhausted. OBSERVED-ONCE
- **Same lifetime.** Subagents die with the session. A Background session's container is destroyed on exit (harnesses.md:606-611). DESIGN-FACT
- **No launch cost.** There is no admission check, setup script, member cache lock, VM boot or branch publish. A subsession pays all of these (sections 1 and 4). DESIGN-FACT

## Could not establish

- A typical cold-start time for a subsession, from request to `running`, on either runtime. No doc or test reports one. The only figures found are the 90 s boot timeout, the 1.7 s/17 ms checkout disk and #683's ownership-pass timings.
- Why this VM has 1 vCPU. Reading the server's limits or the host CPU count needs server access.
- What the 4 busy subsessions in `options` are. I did not list them, per the brief.
- Any per-harness or per-account concurrency cap beyond `max_live`, the per-member launch lock and the vendor's own limits. None was found.

## Surprises

- docs/harnesses.md:1000-1002 says "A subsession refuses them Standard, so they run Enhanced there" (copilot/gemini/cursor). The code applies `WakeRefusal` only to the **parent's** harness and mode (policy.go:59). `choose` does not refuse a Standard child of those agents. It defaults to Enhanced only when no mode is given.
- The plan doc is partly superseded. It says "A child works in the parent's primary repository only" and offers no shared-account choice (plans/2026-10-26-subsessions.md:57-59, 150). The code now passes extras and `--account`.
- A rate-limit refusal and an idempotency conflict share code -32003 on the transport path. An agent cannot tell them apart by code alone.
- Subagents in one session contend for a single vCPU here. Parallelism inside a session is bounded by the run's CPU limit, not by the number of subagents.
