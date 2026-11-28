# Review: distributed systems

Reviewer: a fresh-context reviewer who did not write the plan. Reviewed
`docs/plans/2026-11-27-multi-machine.md` at `535953ea3` against the code at
the same commit. Read only: grep and read, nothing built or run.

**Verified** means read in the code at the file and line given.
**Inferred** means reasoned from verified code or from how Linux, TCP or
OAuth behave; it was not run.

Severity:

- **Blocks**: the design does not hold as written.
- **Must fix**: the plan has to change before phase 1 starts.
- **Sentence**: worth a sentence in the plan.

Findings are ordered most severe first.

---

## 1. Two owners share one freezer bit, so the host thaws link-frozen VMs by itself

**Severity: Blocks.** "On reconnect the primary decides before any VM
thaws" (§6) cannot be enforced while the link freeze and the memory brake
write the same `cgroup.freeze` file.

**Verified:**

- A freeze is one bit per unit: `<cgroup>/aether.slice/<unit>/cgroup.freeze`
  (`internal/runtime/vm/freeze.go:39-41`). `Frozen()` returns every unit
  whose bit is 1, whoever set it. VMs that "a server that has since stopped
  froze ... count as the longest frozen" (`freeze.go:71-87`).
- At start, the brake sets `vmMemoryBraked = len(vms.Frozen()) > 0`
  (`internal/scheduler/vm_freeze.go:35`). From then on, whenever memory is
  at or above the floor and recovery is not running, every 500 ms it thaws
  the VM that has been frozen longest (`vm_freeze.go:134-146`).
- When the brake loop exits it thaws every frozen VM, if at least twice
  the floor is free (`vm_freeze.go:36-45`).
- While the brake holds, it thaws every VM in `guestsNeeded`, the holds
  (`vm_freeze.go:129-133`, `165-172`). `kill`, `stop` and `destroy` thaw
  first (`internal/runtime/vm/lifecycle.go:255-261`).
  `endFrozenSave` thaws after a failed save (`vm_freeze.go:240-252`).

**Scenario:**

1. The link is down for more than 30 s, so the timer freezes every VM on
   host B.
2. The host agent restarts, for example a crash, its self-update ("at its
   next idle moment", §5) or an OOM kill. The VMs live on in their own units.
3. The new agent's brake finds frozen VMs, sets `vmMemoryBraked`, sees
   memory above the floor (the VMs are frozen, so memory is calm), and
   thaws one VM every 500 ms.
4. The VMs now run without a link. If a person used phase 2's "start again
   elsewhere" meanwhile, two copies run.

The same happens without a restart: a primary hold (`guestsNeeded`)
mirrored to the host and not dropped when the link drops keeps its VM
thawed on every brake tick. A clean agent stop thaws everything through
the deferred thaw.

**Smallest fix:** give the link fence its own freezer level. Freeze the
host's session slice (or a per-host parent cgroup) for a link loss. Leave
the per-unit bit to the brake. In cgroup v2 a cgroup is frozen while any
ancestor is frozen, so nothing the brake does at the unit level can thaw
it. Only a thaw carrying the current link epoch from the primary clears
the slice bit. Holds from the primary are leases that lapse with the link.
State that the brake's exit thaw is off on hosts.

Note: under a frozen slice a `stop` waits `TimeoutStopSec` (15 s) for
SIGKILL, because a frozen task handles no SIGTERM
(`lifecycle.go:255-256`).

---

## 2. A primary restart leaves remote sessions headless, or rolls the update back

**Severity: Blocks.** Row 3 of the failure table ("The primary restarts |
Nothing | ... reattaches each VM as it does after a restart today, through
the tunnel") depends on a mechanism the plan itself lists as "Not
established". Every primary update triggers it.

**Verified:**

- Recovery waits for every active run (`internal/scheduler/recovery.go:688-735`,
  `wg.Wait()`). Only then does `RecoveryReady` close
  (`internal/scheduler/scheduler.go:841-881`).
- An update commits only after `Recovered()`
  (`cmd/aether-server/main.go:502-519`). Otherwise it rolls back after
  `trialDeadline = 5 * time.Minute` (`internal/serverupdate/trial.go:28-30`;
  `docs/failure-handling.md` row "An update still rolls back if a session
  has not finished 5 minutes after the database migration").
- The children dispatch, watches, push and rooms all wait for
  `RecoveryReady` (`internal/children/service.go:136-141`;
  `internal/server/svc_watch.go:27`, `svc_push.go:30`,
  `svc_collaboration.go:34`).
- In `recoverSupervised`, an `Inspect` error other than `ErrNotFound`
  adopts the run without attaching (`recovery.go:1483-1491`).
  `attachAndSupervise` has one caller, recovery itself (`recovery.go:1481`).
  An adopted run therefore never gets its terminal, ACP bridge or git
  watch back until a sleep and wake (E7 §1.4 found the same).
- Today the home file service runs in the VM's unit and "keeps running
  through a server restart" (`docs/vm-guest.md:220-223`). The plan serves
  it from the primary (§5), so it stops for the length of every restart.

**Scenario:**

1. The primary updates. Every host's link drops at the same moment.
2. Recovery starts before the hosts redial. For each remote run:
   - If the remote `Inspect` fails fast, the run is adopted headless for
     good. Enhanced sessions cannot be prompted or woken, and terminals
     are blank.
   - If `Inspect` waits for the host, recovery blocks. Then
     `RecoveryReady`, the children dispatch, waits and push all stall,
     and a host that is offline (or slow to redial) makes the update roll
     back at 5 minutes.
3. Meanwhile every remote VM freezes at 30 s (finding 5 covers what thaw
   does to it), and every home file call in flight stalls.

**Smallest fix:**

- Make "re-establish one run's PTY attachment, ACP bridge and git watch
  after a link drop, without a restart or a wake" a phase 1c deliverable.
- Recovery adopts remote runs at once and attaches them when their host's
  link comes up. `RecoveryReady` and the update commit must not wait for
  hosts.
- Correct the failure-table row: a primary restart freezes every remote
  session for its length plus the hosts' redial time.

---

## 3. Host commands are neither idempotent nor fenced

**Severity: Must fix.** §3 says host calls name the VM by the session's
id, "so a repeat is harmless". §6 says "a repeated call finds the VM that
exists".

**Verified:**

- `create` refuses a second call: "vm runtime: a VM named %s already
  exists" (`internal/runtime/vm/engine.go:240-241`).
- `restoreAsleep` infers the outcome of a failed `Restore` from a separate
  `Inspect` (10 s). If that reads "exited", it cold-starts: `Stop`
  "discards the saved state and a half-restored VM alike", and the session
  starts from disk without its processes (`internal/scheduler/sleep_vm.go:241-300`).
- `Inspect` does not take the VM's key lock (`lifecycle.go:542-575`), so it
  can read the record while a restore holds that lock.

**Scenarios:**

1. **Create retried after a blip.** The primary sends `create`, the link
   drops, and the host finishes. The primary either fails the launch
   (leaving a VM on the host with a failed run) or retries and gets
   "already exists" (another failed launch). Nothing runs twice, but a
   link blip fails launches, and the plan does not say which path is
   taken.
2. **Restore across a blip.** The primary's `Restore` returns a transport
   error while the host is still restoring. The link returns within 10 s,
   and `Inspect` reads a record mid-restore. If it reads "exited", the
   primary discards the saved state, losing every process of the session.
   Inferred: whether the record reads "exited" mid-restore was not traced.
   The defect is that the outcome of an ambiguous call is inferred from a
   racing read.
3. **Late execution.** A command sent on link epoch N is still queued
   behind the key lock on the host when the primary, on epoch N+1, sends
   `destroy` or `stop`. The two run in whatever order the lock grants.
   Inferred.

**Smallest fix:** every host call carries an operation id and the VM's
generation, which the primary writes in the database before the call.

- The host keeps the result per operation id. After a disconnect the
  primary asks for that result and does not re-derive it with `Inspect`.
- The host refuses any command older than the generation it last saw.
- `create` is idempotent on (run id, spec digest).
- Placement is written before `create`, with the generation, and a launch
  moves to another machine only after the first one confirms the
  operation did not run.

---

## 4. "Can never run twice" holds only for the primary's own commands, and the lease is undefined

**Severity: Must fix.** The Recommendation, §6 and the README say it
without qualification.

**Inferred** (Linux and TCP behaviour, applied to the plan's text):

- **The freeze clock.** The plan says "after 30 s without the link" but
  never defines "without the link". Liveness must be a host-initiated
  round trip acknowledged by the primary, timed on the host's monotonic
  clock. If the host counts "the TCP connection is open" or "I still
  receive data", then in a one-way partition (host→primary lost,
  primary→host fine) the primary stops hearing the host, and 60 s later
  offers "start again elsewhere", while the host still sees a live link
  and never freezes.
- **Suspend and pause.** systemd monotonic timers and Go's monotonic
  clock do not advance while the machine is suspended. A host can be a
  laptop, or itself a VM that its hypervisor pauses or live-migrates. The
  engine notes a host "is itself a VM"
  (`internal/runtime/vm/engine.go:541-544`, via E1 §5). Such a host can
  sleep for 10 minutes and resume. The primary offered "start again" at
  60 s. On resume, the host's VMs run for up to the rest of their 30 s
  before the timer fires. The plan's caveat names only "a host whose clock
  or systemd is broken".
- **External side effects.** Within the grace period, or after a resume,
  the old copy runs with no supervisor. It holds the workspace secrets
  given at create (§5), any gh token the agent has already read into
  memory, and open TLS connections. It can push, open pull requests or
  deploy. "Run twice" in the sense users care about (two agents acting on
  the world) is not prevented. It is bounded by the grace period plus
  detection time, and unbounded across a suspend.
- **Late snapshots.** Git snapshots the guest wrote before the freeze can
  sit in host socket buffers and arrive after the primary has declared the
  run lost. The tunnel accepts a stream "only if that VM is placed on that
  host" (§7). That check must be on the placement generation, not the
  host, or late data lands on a run that was declared lost.

**Smallest fix:** reword the claim: "the primary never drives two copies;
an old copy can act for at most the grace period after its last
acknowledged heartbeat, longer on a host that was suspended." Define the
heartbeat as a round trip with a host-side timeout. Never offer "start
again elsewhere" on a timeout alone: require either the host's
confirmation that it destroyed the VM, or a person stating the machine is
off (the `out-of-service` case the plan already cites).

---

## 5. A freeze does not "lose nothing": guest timers run through it

**Severity: Must fix.** Failure table row 2 says only "Time" is lost, and
§4 says a call in flight "is retried by the client with the same
idempotency key for up to 60 s".

**Verified:**

- **Guest timers.** The coordination client's retry window is 60 s from
  the first attempt, measured with `time.Since` in the guest
  (`internal/coordtransport/retry.go:20`, `client.go:187-196`). The home
  file service's call deadline is 2 minutes, measured in the guest
  (`internal/guest/home/fs_linux.go:34-36`, `104-121`).
- **Guest time during a pause.** #1159 measured the guest's clock 0.03 s
  behind the host after a 5 s pause (E1 §9). So guest time advances while
  vCPUs are stopped. Inferred: a cgroup freeze does not even tell KVM, so
  kvm-clock advances in the same way.
- **Coordination retries.** Only methods that carry an idempotency key or
  are on a read-only list are retried (`retry.go:51-79`). Anything else
  that was sent fails as "ambiguous" at once (`client.go:184-186`). A call
  that ran out its own deadline is never retried: "the server is up and
  slow" (`retry.go:41-45`). Default deadline 30 s, `coord.report` 2 min
  (`client.go:27-29`).
- **The primary's side of the tunnel.** The guest client retries
  `Unavailable` forever (`internal/guest/client.go:525-532`). Any other
  error code closes the attachment (`internal/guest/attach.go:53-62`).
  When the ACP output closes, the driver stops the keeper ("the agent's
  ACP server closed its output"). It then ends a one-shot (Background)
  session (`internal/scheduler/acp_end.go:188-190`, `223-225`).
- **No keepalive.** The helper client sets no keepalive
  (`internal/guest/client.go:154-166`), so a stalled stream that is not
  reset blocks forever.

**Scenarios:**

1. **The link stalls.** The host's tunnel end holds the guest's socket
   open instead of resetting it. A `coord.send` hangs until its 30 s
   deadline, counts as timed out, is not retried, and the agent gets
   `CodeUnavailable ... may already have applied`.
2. **Outage longer than 60 s** (30 s of grace and 30 s frozen). On thaw,
   every coordination call in flight is already past its window and
   returns `CodeUnavailable`.
3. **Outage longer than 2 minutes.** Every home file request in flight
   returns `EIO` on thaw (`fs_linux.go:118-119`). That includes the
   agent's conversation file and its login.
4. **The tunnel returns an error status.** If the host agent answers with
   a status ("VM frozen", "no link") instead of closing the stream, the
   ACP output closes. A Background session ends, and an interactive one
   shows "Enhanced agent exited".

**Smallest fix:** specify how each stream kind behaves when the link is
lost:

- Coordination and home connections are reset at once, never held open,
  and new dials are refused, so the clients' retry paths run.
- On the primary's side, a link loss surfaces only as a closed transport
  (`Unavailable`), never as a gRPC status.
- Before freezing, the host agent tells the guest helper over its local
  vsock to suspend the coordination and home client deadlines.
  Alternatively, accept and state the losses above.
- Replace "Time" in the failure table with the real list.

---

## 6. The home over the link is a network filesystem with NFS replay

**Severity: Must fix.** §5 says "Two VMs on two machines share one login
file exactly as two VMs on one machine do now."

**Verified:**

- The guest retries a call cut by a dropped connection up to 3 times. The
  code says: "An operation the service ran before the cut may run twice,
  as on NFS" (`internal/guest/home/fs_linux.go:102-114`).
- **Create.** The server creates with `O_CREAT|O_EXCL`. A replay of a
  caller's exclusive create returns `EEXIST` for a file the first try
  created (`internal/guest/home/server_io_linux.go:92-107`).
- **Rename and remove.** Neither carries a request id
  (`fs_linux.go:326-334`), so a replay after success returns `ENOENT`.
- **Append.** An `O_APPEND` write lands at the host's end of the file
  (`server_io_linux.go:210-231`), so a replay appends twice.
- **Truncate.** Open with `O_TRUNC` truncates on the server at once
  (`server_io_linux.go:24-50`, `92-115`). The write that follows is a
  separate call.
- Today these cuts happen only at a save or restore on one machine
  (`fs_linux.go:37-38`). On a link they happen on every drop.

**Scenarios** (inferred; which write pattern each agent uses was not
established):

1. **Blanked login.** An agent rewrites its login file in place: open with
   `O_TRUNC`, then write. The link drops between the two calls. The
   primary holds an empty login, and every session of that member, on
   every machine, now reads it. That is a wider blast radius than one
   machine has today.
2. **Atomic write reported as failed.** An agent writes `tmp` and renames
   it over the login. The rename runs and the reply is lost. The replay
   returns `ENOENT`, and the agent reports a failed write of a token that
   is in fact stored.
3. **Duplicated lines.** An `O_APPEND` replay duplicates lines in a JSONL
   conversation file in the home, which `--resume` then reads.
4. **Rotated token after a freeze.** A frozen VM holds an old access token
   and refresh token in memory. On thaw it refreshes with a refresh token
   another session already rotated. This exists today with saved and
   braked VMs; link freezes make it routine. Whether a vendor revokes the
   token family on reuse is not established.

**Smallest fix:**

- Give each mutating home request a request id, and keep a per-VM reply
  cache on the primary that outlives a tunnel reconnect.
- Gate it on the helper version, because saved VMs keep old helpers
  (`docs/vm-guest.md:729-735`).
- Make the spike in "What to measure" kill the link during writes, not
  only measure latency.
- Strike "exactly as ... now".

---

## 7. The guard, admission's save pass and reservations cannot run "on the host alone"

**Severity: Must fix.** §1 places "the memory brake and guard" on the
host. §2 says "The machine runs its own admission and answers", and phase
1c says "the brake and guard on the host".

**Verified:**

- The guard picks VMs from scheduler state: `s.runs`, ACP pending turns,
  idle facts, retained and held-awake runs
  (`internal/scheduler/vm_memory.go:251-291`). A save is a session
  transition under the run's `lifecycleMu` and is recorded in the sidecar
  (`vm_freeze.go:218-236`).
- The brake's target list also comes from `s.runs` (`vm_freeze.go:97-128`).
- Admission runs one save pass inside `reserve` (`internal/scheduler/capacity.go:197-211`),
  which can wait up to `memorySaveHold = 30 s` (`vm_memory.go:30`,
  `186-200`).
- `reserve` runs inside `authorizationMu`, which `launchRun`
  (`internal/sshd/handlers.go:288`) and the child dispatch
  (`internal/children/dispatch.go:109-111`) hold until `Admitted`, after
  `createRun` (`internal/scheduler/launch.go:316-405`).
- The disk guard pauses sessions and stops their VMs (E1 §4,
  `internal/scheduler/vm_disks.go`). It is also scheduler state, and the
  plan does not mention it.

**Scenario:**

1. Placement scores host B by counting idle paused memory as free (§2).
2. B's admission needs the primary to say which VMs are idle, and to save
   them as session transitions it owns.
3. While the round trips and the up-to-30 s save run, the primary holds
   `authorizationMu`, so every launch, handoff and child dispatch on the
   installation waits.
4. If either side restarts between reserve and release, the reservation
   leaks (the host keeps it) or vanishes (the host forgets it while the
   primary still counts it). The plan has no lease or TTL.

**Smallest fix:**

- The host keeps only the brake and the readings.
- Choosing VMs to save and saving them stay on the primary, driven as host
  calls.
- A host reserve is a lease keyed by a launch id, with a TTL. It never
  runs a save pass and has a deadline of seconds.
- No remote call runs under `authorizationMu`. When a host is short, the
  launch waits in the queue while the primary frees room asynchronously.

---

## 8. Identity fences: cloned hosts, older host disks and a restored primary

**Severity: Must fix.**

**Scenarios** (inferred from the plan's text):

1. **Clone race.** A person clones host B's VM or disk to make host C, a
   common way to add a machine. C has B's key, data directory and saved
   VMs. The rule "the primary keeps the link that is open and refuses the
   second" means whichever redials first after a blip becomes "B". If C
   wins, the primary thaws or wakes C's copies of B's sessions while B's
   own VMs stay frozen. Then B returns, is refused, and an admin
   "rejoins the clone under a new key" with both copies' disks live.
2. **Start counter.** The host "keeps a counter it raises on every
   start". A host restored from an older image whose agent then restarts
   a few times (a crash loop or reboots) passes the check. The check also
   gates only thaw, while a restored host's sessions are saved VMs that
   come back by restore or boot.
3. **Restored primary.** The primary is restored from backup (§6 row 4).
   On reconnect, "the primary lists the VMs it expects; the host destroys
   the rest" destroys every VM created after the backup. The restored
   bare repositories also lack their commits, so the VM's checkout disk
   is the only copy of that work.

**Smallest fix:**

- Use per-VM generations recorded in the database (finding 3), not a
  per-host start counter.
- When a key is presented on two connections, quarantine both and thaw
  nothing until a person acts.
- VMs the primary does not know are kept stopped and listed for an admin,
  never destroyed automatically.

---

## 9. Phase 1 has no way to end a session on an unreachable host

**Severity: Must fix.**

**Verified:**

- Kill returns the `Stop` error unless it is `ErrNotFound`
  (`internal/scheduler/steer.go:82-84`), so a session on an unreachable
  host cannot be killed.
- The per-parent `max_live` slot count comes from the database
  (`internal/children/dispatch.go:40-48`). A child stuck as "unreachable"
  holds its slot.
- §4 says waits on `run:<id>:finished` resolve "when a person declares the
  machine lost". "Declare lost" is phase 2 (§8), but phase 1 "Left
  undone: ... a lost host's sessions end".

**Scenario:** with phase 1 alone, a host dies for good. Its sessions
cannot be killed or closed, parents' slots stay busy, queued children wait
behind them indefinitely, and `--until` waits never resolve.

**Smallest fix:** move "declare lost" (end with `interrupted`/`machine
lost`, which needs no host) into phase 1. Define, for each of the E1 §6
sweeps, what "the machine is unreachable" means. It must be "unknown: do
nothing", never "gone". E1 §6 says its list "is not exhaustive", so
phase 1a needs a full pass, not the 20 listed.

---

## 10. One TCP connection carries heartbeats, bulk copies and per-call channels

**Severity: Must fix.**

**Verified:**

- The coordination client dials a new connection per tool call by design
  (`internal/coordtransport/client.go:118-124`). §4 says "Channels stay
  open and are reused", citing #1175's 453 ms per call with a channel per
  request.
- Bulk transfers go over the same link (§5): a saved environment on first
  use, "about 1 GB per member" of program disks, the checkout clone, and
  git packs.

**Inferred:** SSH multiplexes channels over one TCP stream. A 1 GB copy,
or a packet loss episode, delays the heartbeat behind the bulk data
(head-of-line blocking). If the heartbeat timeout is near the grace
period, a big image copy freezes every VM on the host. Mapping each
coordination call to a new SSH channel open adds a round trip per call,
the cost #1175 measured.

**Smallest fix:**

- Run heartbeats and control on a connection separate from bulk data, or
  rate-limit bulk data below the link's capacity.
- Make the freeze timeout tolerate the largest bulk transfer.
- Use a stream multiplexer inside one long-lived channel for per-call
  sockets, and say so.

---

## 11. The phase 0 queue: duplicate interactive launches and lock churn

**Severity: Must fix.** This is phase 0, the part recommended to ship
first.

**Verified:**

- Only children launch under a reserved id that makes a replay
  idempotent (`internal/children/dispatch.go:139`;
  `internal/scheduler/launch.go:296-315`). An interactive `run.launch`
  carries none (`internal/sshd/handlers.go:281-300`).
- The child loop reconciles every 3 s and on every `run.status` event on
  the bus (`internal/children/service.go:125-156`).
- Each attempt takes `authorizationMu` and runs `reserve`, including the
  up-to-30 s save pass (finding 7).

**Scenarios:**

1. **Duplicate launch.** An interactive launch now waits. The CLI's call
   times out or the SSH connection drops, and the person or client
   retries. There are two waiting launches, and later two sessions.
2. **Lock churn.** On a full machine with N queued children, every 3 s
   and every status change re-runs authorization and the save pass under
   the installation-wide lock. Interactive launches and handoffs queue
   behind them.
3. **Stale requests.** A launch that waited a day runs with the base
   commit and permissions of when it was asked. The child path already
   re-authorizes (`dispatch.go:109-127`). The interactive path is not
   specified.

**Smallest fix:**

- A waiting interactive launch gets a reserved run id at enqueue, which
  the client sends again on retry.
- Retries run a cheap capacity pre-check outside the lock and back off.
  They are triggered by capacity reports, not by every `run.status`.
- Re-authorize at dispatch, and say whether the base is captured at
  enqueue or at start.

---

## 12. Link-loss freeze against VMs that are starting, saving or restoring

**Severity: Must fix.**

**Verified:**

- `freeze` refuses a VM whose key lock is held: "it is starting,
  stopping, saving or restoring" (`internal/runtime/vm/freeze.go:53-57`).
- Boots hold the lock for up to 90 s (E1 §9 `bootTimeout`). A wake waits
  3 min plus 1 s per 20 MiB.

**Inferred:**

- If the link freeze goes through the engine, a VM booting or restoring
  when the link drops is not frozen and runs on.
- If the systemd timer writes the bit directly, it freezes the guest
  under a boot or restore, and that operation times out. The host's error
  path then kills the VM (`kill` thaws first,
  `internal/runtime/vm/lifecycle.go:257-261`). On the primary,
  `restoreAsleep` turns a failed restore into a cold start that discards
  the saved state (finding 3).

**Smallest fix:** state which path the timer uses. With the slice-level
freezer (finding 1), say that operations under way at a link loss are
abandoned without discarding a saved state, and are resolved by
operation id on reconnect.

---

## 13. The phases do not ship one at a time as tabled

**Severity: Must fix.**

**Verified** (from the plan and the code):

- **1c needs 1d.** Phase 1c promises that "Everything about the session
  works as on machine one", but these are in 1d:
  - The checkout disk is built from the bare repository's local path
    (`git clone --no-hardlinks`; E7 §2.3, `internal/gitengine/vm.go:118-143`),
    so a host cannot create a session without 1d's clone over SSH.
  - Without the image store on the host, no VM can be created.
  - Without per-host egress and DNS, the VM cannot reach a model API.
- **The older-disk fence is too late.** Its admin choice is in phase 2,
  but the hazard exists from the first remote session (findings 4 and 8).
- **"Declare lost" is too late** (finding 9).
- **Phase 0 is larger than 4 PRs.** It changes the launch protocol from a
  synchronous answer or `-32004` to a waiting row, adds an idempotency
  token (finding 11), adds dashboard rows and Settings → Machines (a UI
  change, so a design review loop), and adds counters.

**Smallest fix:** merge 1c and 1d, or reorder so that images, the
checkout disk and networking come before the first remote session. Move
the fence and "declare lost" into phase 1. Re-size phase 0.

---

## 14. Things called "unchanged" that change

**Severity: Sentence each, except where marked.**

- **Recovery.** It does not reattach "as it does after a restart today"
  (finding 2). **Must fix.**
- **The home file service.** Today it survives a server restart, and its
  failure ends the VM (`internal/runtime/vm/monitor_linux.go:28-33`).
  Under the plan it stops with the primary, and a relay failure must not
  end the VM. **Must fix** (findings 2 and 6).
- **Coordination.** It is only "unchanged" if the tunnel resets
  connections on link loss (finding 5).
- **The git client does not use the replaceable dialer.**
  `gitClient` calls `guest.DialGit` directly
  (`internal/runtime/vm/engine.go:164-176`). Only the helper uses the
  `dial` field the plan cites (`engine.go:83`).
- **Mount planning reads the engine's own disk.** `planMounts` stats each
  mount's host path, finds sockets in shared directories and decides
  forwarding (`internal/runtime/vm/share.go:48-120`). Those paths are on
  the primary, so the split must send these facts with the create call.
- **Guest clock.** `SyncClock(time.Now())` uses the caller's clock
  (`internal/runtime/vm/save.go:364-367`). After a thaw the plan has "the
  primary" set the guest's clock. It should use the host's clock, which
  is the one the guest's kvm-clock follows.
- **Wakes.** "A turn that starts but times out unconfirmed goes
  unrecorded, and its mail wakes again" (`internal/coord/acp_wake.go:179-181`).
  A prompt cut by a link drop is therefore delivered twice. `Wakeable` is
  in-memory and does not know the host is unreachable, so the 30 s sweep
  keeps trying.
- **The disk guard and OOM readings** are per host and scheduler-coupled
  (finding 7). The plan lists neither.

---

## 15. "What not to build" items that the design quietly needs

**Severity: Sentence.**

- **A network filesystem for homes.** The home over the link is one, with
  NFS replay semantics (finding 6). "No shared network storage" is true
  only of the data directory.
- **Leases and fencing epochs:** link epochs, per-VM generations, and
  reservation leases (findings 1, 3, 7 and 8). This is the part of the
  consensus toolbox that one primary still needs.
- **A durable operation log** on the primary for host commands
  (finding 3).

---

## 16. The comparison undersells the full host

**Severity: Sentence**, for the "Where to cut" table and Decision 1.

**Inferred:**

- **Primary restarts.** Under the VM host, every primary restart (every
  update, with migrations that "took 1-2 minutes in testing",
  `docs/failure-handling.md`) freezes every remote session and costs what
  finding 5 lists. The full host keeps working through primary restarts,
  not only through link outages. The table's "A link drop" row omits this.
- **Pulled toward the full host anyway.** The VM host already needs
  scheduler state on the host for the guard (finding 7), a reattach path
  (finding 2) and an operation log (finding 3).
- **The primary's limits.** The primary's CPU and memory bound every
  host. 2026-11-26 was a memory outage on the server that would become
  the primary, and the plan acknowledges this.

The VM host may still be the right first step. Decision 1 should weigh
"every update pauses every remote session" explicitly, not only "link
drops".

---

## Not checked

- Whether the record reads "exited" for a VM mid-restore (finding 3,
  scenario 2).
- How Claude Code, Codex and opencode write their login and conversation
  files (finding 6).
- Whether any vendor revokes a refresh-token family on reuse (finding 6).
- `internal/gitengine/vm_watch.go`'s handling of snapshots for a run that
  is no longer active (finding 4, late snapshots).
