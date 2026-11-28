# Re-check of the revised multi-machine plan

Reviewed: `docs/plans/2026-11-27-multi-machine.md` at `def72e5b6`, against
the code at the same commit, the three reviews in this directory and
`research/2026-11-27-multi-machine/README.md`. Read only: grep and read,
nothing built or run. **Verified** means read at the cited line.
**Inferred** means reasoned from verified code, or from documented Linux or
systemd behaviour that was not run here.

Severities: **still blocks** (the design does not hold as written),
**must fix in the plan** (the text must change before phase 1),
**wording** (a citation, a contradiction or a sentence).

## Status of the blocking findings

| Finding | Status |
| --- | --- |
| DS 1, brake thaws the link freeze | Fixed for the whole-host freeze. **Still present** for any single VM the plan keeps frozen while the rest of the host runs (R1) |
| DS 2, primary restart leaves remote runs headless or rolls back the update | Fixed as design: reattach is a 1c deliverable, recovery does not wait for hosts (plan 810-829). Not yet specified per stream kind (plan 803-805 says so) |
| SEC 1, tailnet sign-in as the host node's owner | Fixed in the rules. The tag is required but not checked (R6) |
| SEC 2, placement trusts host reports, no default | Fixed: closed by default, cap per host, reports only order the set (plan 387-390, 953-972). What withdrawing an allowance does is not said (R7) |
| OMP 1, silence is not fencing | Fixed in the claim (plan 857-872). The rejected watchdog escalation leaves a gap (rejection 2). The stale copy can still damage Cloudfield (R4) |
| OMP 2, brake undoes the fence | Same as DS 1: fixed for the whole host, still present for single VMs (R1). The "one gate with independent reasons" part was not adopted |
| OMP 3, host calls not idempotent | Partly fixed. Generation semantics, durable receipt and the agent-crash case are open (R3), and a link freeze during a restore still drops saved memory (R2) |
| OMP 4, rollback resurrects or erases | Partly fixed: nothing unknown is deleted, generations per VM. A host image restored at the same generation, and revoked host keys after a primary restore, are open (R12) |
| OMP 5, home replays writes | Fixed in design: request ids, kept results, fail rather than replay (plan 696-703) |
| OMP 6, one token file is not a refresh lock | Gated by a test only (plan 704-709); see rejection 4 |
| OMP 7, placement follows the data | Mostly fixed (plan 959-970). Gaps: withdrawal, revoke moves, explicit machine naming, handoff (R7) |

## Findings, most severe first

### R1. A VM "kept frozen" alone is held only by the bit the brake thaws

**Still blocks.** The plan's own reasoning: "The memory brake freezes and
thaws single VMs through their own flag ... A link freeze on single VMs
would be undone by it" (plan 764-769). The fix freezes the group. But four
places keep **one** VM frozen while its host's other VMs run:

- "A host has a VM the primary did not place there | It stays frozen and is
  listed" (plan 843).
- "a VM the primary does not know is kept frozen and listed" after a
  primary restored from backup (plan 839).
- Reattach step 2 compares the presented VMs with the placements, then step
  3 thaws (plan 818-823). A VM that fails the comparison can only stay
  frozen through its own unit bit once the group is thawed.
- The fork: the old session "ends `interrupted`, and the primary refuses
  everything its machine sends for it" (plan 868-870). Nothing says what
  happens to the old VM when its host reconnects and the group thaws.

Verified: the brake thaws the longest-frozen VM every 500 ms once memory is
above the floor (`internal/scheduler/vm_freeze.go:134-146`), and
`Frozen()` returns every unit whose bit is 1, whoever set it
(`internal/runtime/vm/freeze.go:71-87`). A host-side brake built from this
code thaws the quarantined VM within seconds.

Scenario: a host is partitioned; a person forks session S; the host
returns. Its VMs are presented, S's VM fails the comparison, the group is
thawed, and the brake thaws S's VM. The stale copy now runs with the
member's gh token and workspace secrets in memory, unsupervised, with
network, for as long as nobody notices. The primary refuses its tunnel
streams, so its agent sees errors and may retry pushes to GitHub directly.

Smallest fix: one host-side execution gate per VM with independent,
persisted reasons (link, quarantine, memory), as OMP 2 proposed; the brake
clears only its own reason. A VM whose session the primary ended
(`interrupted`, forked, declared lost) is stopped on reattach, disks kept.
Add a failure-table row for "a host returns after its sessions were forked
or declared lost".

### R2. A link freeze during a restore drops the saved memory

**Still blocks** the Review claim that a lost answer can no longer "drop
saved memory" (plan 1165). DS 12 found this; the revision does not mention
it and the Review table does not list it.

Verified:

- The group freeze stops Cloud Hypervisor wherever it is, including mid
  restore; the per-VM freeze refused exactly this case ("it is starting,
  stopping, saving or restoring", `internal/runtime/vm/freeze.go:50-57`).
- The host's restore waits `restoreTimeout` (60 s) plus 1 s per 20 MiB
  (`internal/runtime/vm/save.go:22`, `38`, `330-331`). The host agent is
  not frozen, so the timeout runs while the monitor is. On timeout it kills
  the VM and returns the error, saved state kept (`save.go:340-343`).
- That is a **known** failure, not an unknown outcome, so "unknown outcomes
  resolved on the same host" does not apply. The primary's existing
  `restoreAsleep` then inspects, finds the VM exited, asks
  `coldStartRefusal`, which returns "" for a closed session or an Enhanced
  session whose agent resumes, and calls `Stop`, which "discards the saved
  state" (`internal/scheduler/sleep_vm.go:261-295`, `396-414`).

Scenario: the primary wakes an 8 GiB Enhanced session on a host (about
7 minutes of load). The link drops for over 30 s. The group freezes, the
restore times out, the host kills the VM. On reconnect the primary reads
"restore failed" and cold-starts the session: every process and terminal is
gone, though nothing was wrong with the saved state.

The same path ends boots: a create or boot frozen past `bootTimeout`
(90 s, `internal/runtime/vm/engine.go:57`) fails the launch.

Smallest fix: the host suspends an operation's deadline while the group is
frozen, or records an operation the freeze interrupted as *interrupted*,
saved state intact. The primary never cold-starts on that result, only on a
host-stated "cannot be restored" (`ErrNotSaved`, disks moved). Add link loss
during boot, save and restore to the spike's freeze gate and to the failure
table.

### R3. Generations and operation records are not defined enough to fence

**Must fix in the plan.** Plan 317-322: the primary writes "a generation for
the VM and the operation with its id" before calling; the host "refuses a
call for an older generation, so a command delayed past a reconnect cannot
undo a newer one".

- When the generation changes is not said. Written once at placement, it
  orders nothing between two calls on the same VM: a `stop` sent before a
  reconnect and a `restore` sent after carry the same generation, and DS 3
  scenario 3 (two commands granted in lock order) is still open. Operation
  ids are ids, not a sequence.
- When the host records is not said. If it records on completion, this
  interleaving fails a live launch: `create` X is queued behind the VM's key
  lock when the link drops; after reconnect the primary asks for X, the
  host has no record, the primary resends X; the first finishes, the second
  gets `a VM named … already exists` (`internal/runtime/vm/engine.go:240-241`)
  and that failure is recorded for X. The launch fails with a VM on the
  host. The same happens when the result is kept in memory and the agent
  restarts.
- The plan's "Not established" admits the agent-crash case still falls back
  to inference: "Whether a VM's record can read 'exited' partway through a
  restore, which the rule for unknown calls depends on" (plan 1135-1136).

Smallest fix: the host writes each operation durably on receipt, before it
acts, and serialises by operation id; a query for an id it never received
records a tombstone so the late original is refused; each VM carries a
sequence that every lifecycle call raises and the host refuses anything at
or below the last it applied. For an operation the agent died in, the host
reports *interrupted* and never cold-starts by itself.

### R4. The forked session's stale copy can still damage Cloudfield

**Must fix in the plan.** Plan 866-872: the primary refuses the old copy's
traffic and the dialog "names what it holds: the member's login, the gh
token and the workspace's secrets". That stops damage through the link. Two
paths do not use the link:

- **The login.** Claude Code and Codex rotate the refresh token on each
  refresh (`docs/security.md:242-244`). The old copy read the login from
  the served home before the cut. When it refreshes, the vendor rotates the
  token and the copy in `homes/<member>/` on the primary holds a spent one,
  which signs out every session of that member on every machine, the fork
  included. The shared-account plan saw one machine's version of this
  (`docs/plans/2026-11-03-shared-account-credentials.md:137-147`).
- **GitHub.** With the gh token the copy can push to the upstream a source
  mirror follows; the mirror "fetches it read-only and moves the base branch
  forward" (`docs/README.md:23`), and `--until` waits on GitHub resolve on
  what it does.

Also R1: on its host's return the old VM is thawed.

Smallest fix: the fork dialog offers, per login the old session could read,
"sign in again and revoke the old login at the vendor" before the fork
starts, and says that a mirrored base branch may move. The old VM is
stopped when its host returns (R1).

### R5. The freeze timer does not count through a suspend

**Must fix in the plan.** Liveness is "timed on the host with a clock that
keeps counting through a suspend" (plan 300-303), but the freeze is done by
"a timer in systemd that the host agent pushes back" (plan 850-854).
Inferred from systemd.timer(5): monotonic timers pause while the machine is
suspended. So the plan's own example, a suspended host, resumes with the
remainder of the grace period to run, and the plan concedes such a host
"may not freeze" (plan 859-861).

Smallest fix: freeze the group before every suspend (a unit ordered
`Before=sleep.target`), so a host always resumes frozen; the agent freezes
directly when its boot-time clock says the heartbeat is stale; the timer is
the backstop for a dead agent. This also removes "suspended" from the list
of hosts that may not freeze.

### R6. "A host on a tailnet must be a tagged node" is not checked

**Must fix in the plan.** Plan 291-295 states three rules; the first is the
one that closes the hole everywhere (WhoIs refuses tagged nodes,
`internal/sshd/whois.go:131-136`, `docs/networking.md:101-103`), but nothing
says who checks it. Tags can also be removed later in the tailnet admin.

Smallest fix: the primary runs WhoIs on the link's source address at join
and on every connect, and refuses to turn on, or keeps frozen, a host whose
node is untagged. "An address registered as a host" covers both the host's
IPv4 and IPv6 tailnet addresses. The `none` callback refuses the reserved
host user.

### R7. Placement checks the launch; nothing says what a withdrawal does

**Must fix in the plan.**

- A member turns a machine off for their home, or a super admin removes a
  workspace from a host's list, while sessions using them live or sleep
  there. The plan checks lists "again when the account changes" (plan 964-
  965) and reattach "applies ... any revocation" (plan 819-820). Whether a
  withdrawal stops serving the home, ends the session or only blocks new
  placements is not said, so the member's login keeps reaching the machine
  they just turned off.
- Revoke moves: "fallback skips that account" (plan 966-967) covers usage
  fallback. A revoke must move the session off the account
  (`docs/security.md:353-354`, "the session moves off your account or
  stops"); with no allowed account left on that machine, the outcome is not
  stated.
- Phase 1c lets a person "start a session on machine two by naming it"
  (plan 1047). That the named machine must pass the same intersection is not
  said.
- A handoff changes the run's owner and keeps its home and account
  (`internal/store/store.go:184-186`, `internal/scheduler/account_revoke.go:117-120`).
  No new member's login reaches the host, so it is safe, but the rule
  should say the new owner is not checked and why.

Smallest fix: one sentence per case: withdrawal stops serving the home at
once and ends the member's sessions there as a failed revoke move does; a
revoke move with no allowed account stops the session; a named machine is
filtered like any other.

### R8. "No login is copied" survived the revision

**Must fix in the plan.** The Review table says "'A host holds no home and no
key' was false" was fixed (plan 1174), and OMP 14 asked "Do not claim no
logins are copied". Still in the plan:

- The homes table: "Logins copied | Never | Never" (plan 688).
- Decision 2: "People sign in once and no login is copied" (plan 1111);
  README line 59 the same.
- "At rest on a host: VM disks, VM records with the environment, saved
  memory, images, and its own key. No home" (plan 994-996).

Against: "`copilot` and `antigravity` sessions get a copy of their login at
each cold start; on a host that copy is on the host's disk" (plan 723-725,
`docs/security.md:357-360`), and tokens in saved memory and swap (plan 931).

Smallest fix: "Logins copied: only `copilot` and `antigravity`, as today";
drop "no login is copied" from decision 2 and the README; list login copies
and in-memory tokens under "At rest".

### R9. The frozen group is not named, and on the primary it would hold more than VMs

**Must fix in the plan.** Plan 760 says "the group that holds them all". In
this repository that is `aether.slice`: every VM unit is started with
`--slice=aether.slice` (`internal/runtime/vm/unit.go:37`,
`internal/runtime/docker_cgroup.go:15`) and the brake writes only
`aether.slice/<unit>/cgroup.freeze` (`internal/runtime/vm/freeze.go:39-41`),
never the slice's own file. So the layout allows it.

What else is in that slice:

- On a host: session VMs and the VM image check, which also runs as a VM
  (`internal/runtime/vm/images.go:109-110`). The development browser runs
  inside the session VM (`internal/runtime/vm/browser.go:18-20`). Freezing
  the slice there freezes only session work and image checks; an image
  check under a frozen slice times out.
- On the primary: the omp auth brokers ("the omp auth broker runs in them
  too", `internal/runtime/vm/unit.go:30-31`, `internal/server/server.go:359-361`),
  dictation VMs (`internal/runtime/vm/dictation.go:60-71`), environment
  terminals and the assistant, and with the Docker runtime every container,
  browser and dictation container (`internal/runtime/docker.go:160-165`,
  `docker_browser.go:107-113`, `docker_dictation.go:88`). The same freeze in
  the primary role would stop every omp login of every remote session too.

Not established: whether systemd leaves a raw write to a slice it manages
in place across `daemon-reload` and unit starts and stops inside it.

Smallest fix: name `aether.slice`, say the freeze exists only in the host
role, and add `daemon-reload` and a unit stop under the frozen slice to the
spike's freeze gate.

### R10. The spike does not test the transitions the design depends on

**Must fix in the plan.** The freeze gate is "A VM frozen 5 minutes mid-turn"
(plan 1034). Not covered: agent death during each lifecycle call (OMP 2
asked for it), link loss during boot, save and restore (R2), a host that
returns after a fork (R1), the systemd timer firing with the agent dead, a
suspend (R5). Smallest fix: add one row listing them, passing when no saved
state is dropped and no ended session's VM runs.

### R11. The rejected watchdog escalation leaves an unbounded window

**Must fix in the plan.** See rejection 2 below.

### R12. Rollback cases left open

**Must fix in the plan.**

- A host image restored after the last lifecycle call has the same
  generation and older disks (OMP 4: "Boot counts alone cannot detect
  rollback of guest data under an unchanged identity"). Plan 842 claims
  generations catch "a host that returns with older disks". The git watch
  honours a reported HEAD, reset included (OMP 12, citing
  `internal/gitengine/vm_watch.go:383-429`), so the session branch on the
  primary can move back. Fix: on reattach, keep a VM frozen whose HEAD does
  not descend from the commit the primary last recorded.
- A primary restored from an older backup trusts host keys revoked after
  that backup (OMP 4). Plan 839 says only that the old primary must be off.
  Fix: after a primary restore, every host joins again before it is served.

### R13. Citations that do not support their sentence

**Wording.**

- Plan 715-718: the file service on the primary "keeps what protects it
  today: a process of its own per VM, in a unit that cannot see the rest of
  the data directory, with its own limit on open files
  (`internal/guest/home/server_linux.go:24-39`)". Those lines hold the
  request, stream and handle bounds only. Today the service runs inside the
  VM's monitor, in the VM's unit (`docs/vm-guest.md:218-221`), and "the
  unit's `MemoryMax` bounds the rest" (`docs/vm-guest.md:255-256`). On the
  primary there is no VM unit, so the new unit needs its own `MemoryMax`
  and sandbox; cite the unit's sandbox in `internal/runtime/vm/engine.go`
  (properties at about 625-640) and the doc lines.
- Plan 791-792: "#1159 measured it 0.03 s behind after 5 s". E1 line 377
  records that for a Cloud Hypervisor pause, not a cgroup freeze. Say so, or
  make it a spike measurement.
- Checked and supported: `internal/sshd/session.go:56-135`,
  `internal/sshd/whois.go:127-157`, `internal/runtime/vm/network.go:209`,
  `docs/networking.md:101-102`, `internal/coord/socket.go:61-76`,
  `internal/runtime/vm/engine.go:240-241`,
  `internal/scheduler/sleep_vm.go:241-300`,
  `internal/scheduler/recovery.go:1298-1360`,
  `internal/children/service.go:189-221`,
  `internal/children/dispatch.go:17-48`,
  `internal/scheduler/capacity.go:197-211`,
  `internal/scheduler/acp_end.go:188-190`, `221-226`,
  `internal/guest/attach.go:13-15`, `26-74`,
  `internal/scheduler/recovery.go:1481-1491`,
  `internal/scheduler/sleep_vm.go:207`,
  `internal/runtime/vm/save.go:365-368`, `docs/security.md:242-244`,
  `docs/plans/2026-11-03-shared-account-credentials.md:7-10`, `137-147`,
  `internal/guest/home/fs_linux.go:102-114`, `docs/vm-guest.md:252-255`,
  `docs/harnesses.md:738-754`, `docs/security.md:351-365`,
  `internal/store/runs.go:187-192`, `internal/children/dispatch.go:109-140`,
  `internal/runtime/vm/unit.go:20-21`,
  `internal/scheduler/vm_freeze.go:36-45`, `129-146`,
  `internal/runtime/vm/freeze.go:49-98`. "Removing a host" has no
  citations.

### R14. A child returns to `queued` only for a refusal before its row exists

**Wording.** Plan 438-440: "a capacity refusal returns the child to `queued`
instead of `refused`". The launch writes the row before it calls the host
(plan 411-413), and a child whose row exists is counted started
(`internal/children/service.go:189-221`). So only a reserve refusal
re-queues; a `create` refused after the row fails the child. Say so.

### R15. Contradictions inside the plan and with the README

**Wording.**

- Recommendation 3: a launch that fits nowhere "is not refused" (plan
  31-32; README 17-18), against "One that no machine may ever take ... is
  refused at once" (plan 432-434).
- Recommendation 3: a machine "its workspace and its members allow" (plan
  29-30), against four parties: workspace, extra workspaces, home member,
  account owner (plan 959-965).
- Recommendation 2: a host runs "session VMs and nothing else" (plan
  25-26), but it also runs the egress proxy and DNS (plan 666), the image
  build (plan 659) and the freeze timer.
- "A host fetches nothing by name" (plan 917), against "Each host builds the
  standard image from the signed release" and self-updates from it (plan
  659, 743-745), and "fetches it before the VM is created" (plan 396).
  Say "nothing from the primary by name".
- Recommendation 4 and the failure row "The primary restarts or updates |
  The same, on every host at once" (plan 837): VMs pause only once a
  restart outlasts the 30 s grace; a shorter one leaves them running
  unsupervised.
- "A host's key is replaced by joining again" (plan 904), and `host join`
  refuses a data directory that holds a database or homes (plan 237-238),
  against phase 2 "replacing a host's key in place" (plan 1060). Say whether
  a re-join keeps the host's identity and its placements.
- README 32: "Add a machine with one command". The plan needs a code on the
  primary, `host join`, a fingerprint check, a super admin's turn-on and
  per-member allowances (plan 240-258).
- README 56: the self-supervising host "is a larger build". The plan says it
  is "not sized" and withdrew "three to four times" (plan 1138-1139,
  1190-1194).
- README 23-24: "a queue's answer to a quiet worker is to run the work
  again". The plan narrowed this: BullMQ may also fail the job (plan 477),
  and the reason is that one table suffices (plan 1183).

## The five rejections

1. **"Compare the full host as a narrower design before recommending."**
   Defensible as scope: the plan names it as the cut to examine and lists
   it under Not established. The README contradicts it by calling it "a
   larger build" (R15).
2. **"The host's watchdog should stop a VM it cannot confirm frozen."** The
   reviewer was partly right. The stated reason, that it guards "a failure
   the plan no longer relies on" (plan 1196-1197), is false: the plan still
   relies on the freeze for host memory without a link ("The VMs are then
   frozen and take no more memory", plan 374-375) and for bounding
   unsupervised running to the grace period (plan 863-864). A freeze that
   never lands, which the timer only "keeps trying" (plan 854-855), makes
   that window unbounded. A middle path keeps the processes: when the group
   does not report `frozen` within a bound, the host drops all VM tap
   traffic with its firewall, which already exists per host
   (`internal/runtime/vm/network.go:195-209`), and reports.
3. **"A stream multiplexer for the link."** Defensible. Two connections on
   one path still share its bottleneck queue, so a bulk transfer can still
   raise the heartbeat's round trip; the spike's heartbeat gate (plan 1036)
   measures exactly that.
4. **"Use one refresher or a broker per login."** The rejection answers a
   proposal OMP 6 did not make: it asked for a per-harness lock contract or
   one refresher per rotating login, and warned against bringing back the
   removed mechanism. A single race test (plan 707-709) can pass by luck.
   The evidence already splits the agents: Claude Code takes a directory
   lock, `~/.claude/.oauth_refresh.lock`, which an atomic `mkdir` through
   the file service holds across VMs once replays are deduplicated; Codex
   takes `flock` on `auth.lock`
   (`docs/plans/2026-11-03-shared-account-credentials.md:42`, `55`), which
   holds only within one VM (`docs/vm-guest.md:252-255`). Fix: state this per
   agent; for an agent whose lock does not span VMs, keep all sessions of
   one login on one machine as the first fallback, before a home per
   machine.
5. **"Do not count RPCs as effort."** Defensible: the counts are labelled
   protocol surface, and the sizes are labelled estimates (plan 197-198,
   1006-1007).
