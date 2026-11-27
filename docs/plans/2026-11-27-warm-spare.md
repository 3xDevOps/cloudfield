# A Warm Spare Session VM per Active Member

**Goal:** when a member launches an Enhanced session, the server work before
the guest boots, the guest boot and the cold page cache are already paid
for, so the click waits only for the agent to start. The server keeps at
most one session VM per active member booted ahead of the click, with no
agent in it, and a launch that matches it takes it over. A launch that does
not match runs cold, as today.

**Status:** draft, 2026-11-27, not yet reviewed by the owner. The owner
ruled out agent snapshots and warm agent templates on 2026-11-18; this plan
keeps a live VM, never a snapshot or a clone, and work item 3, the first
that makes a spare, waits for the owner's decision on the two points marked
*Open for the owner*. Everything marked *Assumption* is this plan's choice.
Everything marked *To measure* is settled by the first work item that can
measure it. The [Review](#review) section lists what review changed.

Terms used below. An **Enhanced** session is one whose agent the server
drives over the Agent Client Protocol (ACP); a **Standard** session runs the
agent's terminal UI as the VM's main process. The **accepted base** is the
commit of the workspace's source repository that new sessions start from.
The **permission tier** (root, default, isolated) is how much of the VM the
agent may change. The **memory guard** is the server loop that saves session
VMs to disk when the host runs short of memory. The **authorization lock**
is the server-wide lock a launch holds from its permission check until its
session row exists, so that no revoke can fall in between.

## What was measured

| Step | Time | Where |
| --- | --- | --- |
| One traced cold launch, first host trace to agent ready | about 10.3 s: 4.3 s on the host before the guest kernel (not attributed) and 6.0 s in the VM, of which 1.8 s boot, mounts and checkout, 0.4 s waiting for the host and 3.7 s agent start | A running session VM on a loaded host, Claude Code 2.1.332, one launch |
| VM `Start` to ready, standard image | 2.95 s | KVM CI runners (nested KVM, an upper bound) |
| First command in a running VM | 25 ms | same |
| Session VM stop | 0.23 s | KVM CI runner, #1059 |
| Agent start with a warm page cache (`initialize` + `session/new`) | 216-388 ms + 1393-1985 ms | A running session VM, one vCPU, Claude Code 2.1.332, adapter 0.86.0 |
| Codex 0.167.2 `app-server`: spawn to `initialize`, then `thread/start` | 245-324 ms, then 223-307 ms | same |

A session VM at ready with no agent holds 411-598 MiB of host memory (KVM
CI runners).

Not measured: a claim end to end, a branch rename after boot (the git setup
at boot takes 64-90 ms), how much page cache the pre-read below adds, and
whether a pre-read alone reaches the warm agent start above.

## Why the spare holds no agent

The first draft parked a whole agent session in the spare, which answers a
click in 3-6 ms (`session/set_mode`, measured). It is left out:

- Claude Code reads its git context and instruction files at `session/new`.
  Measured with a stand-in for the model API: a session made ahead, then
  given a renamed branch, a new commit and an edited `CLAUDE.md`, still sent
  the old ones with its first prompt. A launch with a task renames the
  branch, and the instructions Cloudfield gives an agent carry the
  workspace's live session count, so a session made ahead is almost never
  the one a cold launch would make.
- It runs the member's and the checkout's SessionStart hooks and MCP
  servers, and holds the account's login in a process, before anyone
  clicks.
- It holds 420 MiB more (adapter 115, Claude 269, Cloudfield's MCP server
  37, measured).

## Rules

- At most one spare per member, only while the member is active, only from
  memory and disk the host does not need (see [Memory](#memory-and-disk)).
  Spares are the first thing given back.
- Making a spare passes every check a launch by that member passes, every
  time one is made, and a refused make is silent.
- A spare belongs to one member and is only ever claimed by a launch of
  that member that matches it.
- A claimed spare is the session a cold launch would have made. From the
  claim on it runs the code a cold launch runs after its VM has started.
- A miss is today's cold launch. What a spare may add to it is bounded and
  measured: see [Locks](#locks).
- No option, for members or operators.

## The design in one paragraph

A spare is a real Enhanced session with no task, launched ahead under its
own run ID through the launch handler and the existing provisioning path,
stopped at the point where its VM has started and its agent has not, and
kept out of every member-facing surface until a launch claims it. The run ID
is minted when the spare is made and becomes the ID of the session that
claims it. Because the ID never changes, nothing keyed by it has to be bound
late: the VM's name and systemd unit, the checkout key, the `/run/aether`
directory, the coordination token and socket and the environment are
already the claimed session's. A claim writes what only the click knows,
renames the branch, and runs the rest of provisioning: agent start, mode,
model, effort, task.

While it waits, the guest helper reads the agent's program files once so
they are in the page cache.

## What is fixed when the spare starts, and what a claim sets

| Identity | Today | With a spare |
| --- | --- | --- |
| Run ID | Minted by `CreateRun` at launch | Minted when the spare is made. The claiming launch returns it. A launch that brings its own ID (a subsession, `ReservedRunID`) never claims |
| VM name, unit name, record | The run ID (`internal/runtime/vm`) | Unchanged: already the claimed session's |
| `/run/aether`, coordination token, socket to run mapping | Made per run in `coordinationMounts` | Unchanged: made for the spare's run ID |
| Member whose home the VM mounts, workspace, account | Row fields at creation, mounts at boot | Fixed at boot. A claim must match all three |
| Image, user, permission tier, limits, mounts (including the login the VM mounts), environment (including `AETHER_RUN_ID`, workspace secrets, git author), setup script, agent programs, egress | Built in `containerSpec`, fixed at boot | A fingerprint of all of it is compared at the claim. Any difference is a miss. This covers an agent that takes its model or effort in the environment or its arguments |
| Base commit | Captured at launch | The accepted base when the spare was made, read without a fetch. A launch whose captured base differs is a miss |
| Branch | `<prefix><task-slug>-<short id>`, or `<prefix><short id>` with no task (`internal/gitengine/slug.go`) | The spare has the no-task name. A launch with a task renames it on the server and in the guest before the agent starts |
| Instructions, subsession setting, mode, model, effort, task | Set when the agent starts and right after | The same code, run at the claim |
| Launch time, idle clocks, `Via`, linked issue, idempotency receipt | Row fields at creation, the `Created` callback | Written at the claim, in the transaction that unhides the row |

## Spare states

The spare manager keeps one record per member with a state: `starting`,
`ready`, `claiming`, `destroying`. Its lock guards these changes only:
nothing else is taken or waited for under it, so it is innermost to the
authorization lock, the memory guard's lock and the admission gate.

- Only a `ready` spare is claimed, and the claim moves it to `claiming`
  first. Two launches at once: one claims, the other runs cold.
- A spare in `claiming` is a launch. Inactivity and the memory rules no
  longer touch it; a kill reaches it as it reaches a launch that is
  provisioning.
- Everything that destroys a spare moves it to `destroying` first, so a
  launch finds no spare and runs cold. A destroy stops the VM and deletes
  the row, checkout, branch and coordination directory. It does not go
  through the kill path, so it leaves no finished session and no commit.
- A make that reaches the authorization lock checks that it is still
  `starting` before it creates a row.

The run row of a spare stays in the `provisioning` status for as long as it
is a spare, and its state transitions are not recorded.

## Claim

Only the member's own launch of one Enhanced session in one repository can
claim. A subsession, a launch with a reserved run ID, a Standard or
Background session, a launch with extra repositories or from another
session's commit, and a launch by a schedule or template run cold and leave
the spare alone.

A launch that can claim reaches the claim after today's validation,
`AuthorizeLaunch`, budget and login checks and base capture, holding the
authorization lock as today, so every refusal a cold launch gives is given
first. The base capture is a network fetch for a workspace with a source
mirror unless the launch form already refreshed it (#1166, a draft today),
and a claim pays it like any launch. Memory admission moves after the claim
attempt: a claim starts no VM and reserves nothing.

1. Take the member's spare if it is `ready`. A spare that is still starting
   is not waited for.
2. Check that the VM answers (the 25 ms first command) and compare the
   launch with the spare: member, workspace, account, agent, base commit
   and the fingerprint. A VM that does not answer or any difference is a
   **miss**: the spare is destroyed, the launch waits until its VM is gone,
   then goes on to admission and runs cold.
3. In one transaction: write the launch's fields to the run row, clear the
   `spare` mark, record the state transitions a cold launch has recorded by
   this point, stamped now, and run the launch's `Created` callback. The
   authorization lock is released here, where a cold launch releases it.
   The row is `provisioning`.
4. Rename the branch if the task changes it.
5. Run the rest of provisioning as a cold launch runs it after its VM has
   started: the access re-check, agent start with the instructions built
   now, mode, model, effort, task, the move to `running`, supervision, and
   the check for a revoke that fell in between (`leaveIfRevoked`).

Computing the fingerprint must not provision anything. Today the code that
builds the specification also reserves the run's user, applies ownership,
writes the coordination files and prepares egress (`runPlan`,
`coordinationMounts`, `startRunContainer`); work item 3 separates the
reading from the applying.

After step 3 the session is a launch that is provisioning, with its task on
the row, visible and killable, and everything that can go wrong goes wrong
the way it does for a cold launch, because it is the same code. A server
that stops after step 3 finds a `provisioning` row at its next start and
marks the launch interrupted, as today: the member sees the failure and the
task. A claim never falls back to cold after step 3.

Expected click to ready for a claim: the base capture, a branch rename and
a warm agent start (1.6-2.4 s measured for `initialize` + `session/new`
alone), against about 10 s traced cold. *To measure* end to end on KVM.

## Lifecycle

**Active** means the member opened the dashboard's launch form in the last
10 minutes. *Assumption.* The dashboard reports the form and its choice
(workspace, agent, account, permission tier, limits) when it opens and when
a choice has stayed unchanged for two seconds; the request that refreshes
the base (#1166) can carry it. An open event stream does not count, and
neither does any launch.

- **Made** when the form reports, for its choice. Each make runs what a
  launch runs before admission: `AuthorizeLaunch` for that member, account
  and workspace under the authorization lock, the workspace budget check,
  the login check and the disk check. It uses the accepted base without
  fetching. In place of memory admission it has its own bar (below).
- **Destroyed** when the form's choice changes, when 10 minutes pass
  without a report, on a miss, when the accepted base moves, when the
  member's image changes, an agent program updates or a login of the
  account changes, when memory or disk is needed, when a revocation or
  deletion reaches it (below), and at server start. A spare is never
  recovered after a restart: recovery destroys `spare` rows.
- **Made again** only by the next report from the form. Nothing is retried
  or made on a timer, so a push to the base branch starts no VM for
  anyone.
- A spare is never saved to disk, frozen, or closed for being idle, and it
  does not make the server busy for an update. Each of the scheduler's
  periodic loops over live sessions gets a stated answer for a spare in
  work item 3.

A member who clicks before the make finishes (a boot takes seconds) runs
cold. *To measure* in work item 3: the share of dashboard launches that
claim.

## Revocation and deletion

Every handler that looks at a member's or a workspace's live sessions in
order to refuse, stop or move them holds the authorization lock: workspace
revoke, account share revoke, role change, member removal, member merge,
workspace delete, and adding or removing a login. With spares, each of
these, once it holds the lock, moves every spare it would have counted to
`destroying` and stops its VM, then goes on as today without counting it.
A spare never blocks one of them and never survives one, and because a
make creates its row under the same lock, none can appear in between.

*To measure:* whether any of these handlers has to wait for the VM to be
gone before its next step (a workspace delete during a make finds the
workspace locked, as it does during a launch), and how long the lock is
then held.

## Hidden until claimed

The run row carries a `spare` mark. Server-internal code sees the row like
any other: recovery, cache ownership, storage accounting and the handlers
above. Member-facing surfaces do not:

- No session list, count, peer roster, insight, usage, cost, disk or
  memory-brief answer includes the run, and a lookup by ID from a member
  answers not found.
- No event of the run is published and none is written to the timeline,
  whichever package would publish it (the scheduler, and the git engine's
  branch events). The claim writes what a cold launch writes, stamped at
  the claim.
- The run's coordination socket refuses every call until the claim. No
  agent runs in a spare, so only a setup script could call it.

Two things stay visible, on purpose:

- An operator sees a spare. The storage page lists its disks as the
  member's spare, and the server logs each make and destroy with the
  member, the workspace and the reason.
- The spare's branch exists in the workspace repository while the spare
  lives, with no commits of its own, and a member who fetches sees it.

Work item 1 lists every store query over runs, every reader of the active
sessions and every event publisher with its answer for a `spare` row.

## What runs before anyone clicks

Booting a spare runs the workspace's setup script with the workspace's
secrets, in a VM that mounts the member's home and the account's login.
This is what a launch by that member would run, it passes a launch's
checks, and it happens only when that member opens or changes the launch
form. No agent, hook or MCP server runs, and no model request is made.

*Open for the owner:* whether a setup script may run for a session nobody
has asked for yet.

## Memory and disk

The memory guard starts saving sessions when available host memory falls
below the floor (`memoryFloor`: the larger of 2 GiB and a tenth of the
host), or when the host stalls on memory, and stops at twice the floor.
Launch admission keeps the floor plus 512 MiB per starting VM. Spares live
only above all of that:

- A make charges 1 GiB, counted with the admissions in flight. *To
  measure:* what a spare holds once the pre-read is done; the charge
  becomes that number.
- A spare is made only while the guard is calm, no session it saved is
  waiting to wake, and available memory less the admissions in flight and
  the spare's charge stays at or above twice the floor. This check never
  saves a session and never reclaims anything to make room.
- Each guard check destroys every spare when available memory is below
  twice the floor or a stall lasts, before it considers saving a session.
- A launch or wake whose admission comes up short, or is refused for
  memory pressure, destroys spares and measures again before it saves an
  idle session or refuses.
- A spare's unit runs with the highest out-of-memory score, lowered to a
  session's at the claim, so the kernel takes a spare before a session.
- A make needs the disk room a launch needs and is refused once disk
  reclaim has started; disk reclaim destroys spares before it takes
  anything from a session.

On an 8 GiB host the floor is 2 GiB, so a spare needs 5 GiB available and
such a host holds one rarely. On a 64 GiB host the bar is 13.8 GiB
available. Above the bar, a spare holds 411-598 MiB plus the pre-read, per
member with the launch form open.

*Open for the owner:* that cost. The owner's constraint for this work is no
significant rise in memory use; a spare uses only memory that is free
beyond what the guard asks for and is the first thing returned.

## Locks

A make holds locks that others wait for:

- The authorization lock, from `AuthorizeLaunch` to the created row. A make
  does not fetch, so it holds it for local work only.
- The cache locks of the member's home and of the account owner
  (`lockRunCaches`), until the VM has started. They are also taken by
  launches of both members, terminals, environment saves, GitHub connect,
  program updates, account moves, member removal and merge. Anyone who
  waits for a cache lock a make holds cancels the make.
- The workspace's lock against deletion, as a launch holds it.

A launch of the member that finds the spare `starting` cancels the make
too. A miss on a `ready` spare waits for the spare's VM to be gone, which
frees its memory, its run user and its cache ownership, before the cold
launch is admitted.

*To measure* in work item 3, on KVM: how long a cancelled make keeps the
cache locks, and how long a miss waits. A session VM stop measured 0.23 s.
Each must stay under 0.3 s, or the make and the miss are changed before
spares ship. *Assumption.*

## Standard sessions

A Standard session gets no spare in the first version: the agent is the
VM's main process, and its task and instructions are its arguments, fixed
at boot. Every Enhanced agent is served alike, because the spare holds no
agent. One spare is booted for one agent's programs and environment, so a
member who switches agents in the form gets a new spare.

## Left out of the first version

- An agent session made ahead (see above), and Claude Code's `prewarm()`
  and `claim()` (329 ms measured; the adapter does not call them).
- Starting the ACP adapter ahead (`initialize`, 216-388 ms): its arguments
  and environment can depend on the click.
- Fast-forwarding a spare whose base moved.
- Waiting for a spare that is still starting.
- Spares for Standard and Background sessions, subsessions, extra
  repositories and launches from another session's commit.

## Work items

1. **Spare rows.** The `spare` mark, the member-facing filters, the event
   and timeline gate, recovery that destroys spare rows. No spare is made
   yet.
2. **Branch rename after boot.** The git engine and the guest git service
   rename a run branch that has no commits of its own. This item measures
   it.
3. **Spare and claim**, after the owner's decisions: provisioning split at
   the point where the VM has started, the manager and its states, the
   checks on each make, the memory and disk rules, the revocation and
   deletion handlers, the fingerprint, the pre-read, the claim, the lock
   measurements. KVM measurement.
4. **Launch form** reports its choice.
5. Documentation: `docs/enhanced-sessions.md`, `docs/vm-guest.md`,
   `docs/failure-handling.md` (memory pressure, what stays after a server
   restart), `docs/security.md` and `docs/privacy.md` (what runs before a
   click), `docs/teams.md` (revocation), `docs/environments.md` (setup
   script).

Items 1 and 2 change no behavior.

## Test plan

- **Parity.** Launch one session cold and one by claim with the same
  request, then compare the run as the API returns it, its event kinds in
  order, its timeline, its state transitions and insights, its environment
  as a process in the VM sees it, its branch, its subsession tools, the
  first request the agent sends to a stand-in model API (system prompt, git
  context, instruction files), and its usage rows. Only IDs and times may
  differ. A retry with the same idempotency key returns the same session.
- **Every miss has a test:** another workspace, agent or account, image,
  permission tier, limits, changed workspace secret or setup script,
  changed login, changed agent program, moved base, spare not ready, a VM
  that does not answer.
- **Launches that cannot claim** (subsession, reserved run ID, Standard,
  Background, extra repository, schedule) run cold and the spare is still
  `ready` afterwards.
- **Isolation.** A second member's launch with the same workspace and agent
  never claims the first member's spare. A spare is absent from every
  member-facing list and count, and from a member's lookup by ID. No spare
  is made for a member who may not launch in that workspace or on that
  account, or whose workspace is over budget. A spare's coordination socket
  refuses a call.
- **Revocation.** Each of workspace revoke, share revoke, role change,
  member removal, member merge, workspace delete and login removal, run
  while a spare is alive, succeeds and leaves no VM of the spare. A share
  revoked between claim steps 3 and 5 stops or moves the session as it does
  for a cold launch.
- **Claim races.** Two launches at once: one claims, one runs cold, each
  with its own task. A destroy that races a claim yields either a claim or
  a cold launch.
- **Stopped mid-claim.** The server is stopped after step 3; at its next
  start the launch shows as interrupted with its task.
- **Idle clocks.** A session claimed from a spare that waited 9 minutes is
  not put to sleep before a cold-launched one would be.
- **Memory and disk.** No spare is made below the bar, and making one saves
  no session; a guard check below twice the floor, a lasting stall and a
  short admission each destroy spares first; disk reclaim destroys spares
  first.
- **Locks.** A launch or a terminal that arrives while a spare is being
  made, by the same member and by the account's owner, finishes within its
  usual time plus the measured bound.
- **KVM** (`make test-kvm`): form reported, spare ready, launch, first
  prompt accepted, printing click to ready for a claim and a cold launch in
  the same run, and the host memory the spare holds. A second launch right
  after takes the cold path.
- **Restart.** A server restart with a spare alive leaves no VM, unit, run
  row, checkout, branch or coordination directory of it.

## Review

Three rounds of adversarial review changed these:

- The spare holds no agent. A parked agent session was rarely the session
  a cold launch would make, ran hooks and held a login before a click, and
  cost 420 MiB more.
- A spare passes a launch's checks on every make, and every handler that
  enforces a revoke or a deletion destroys spares first. The first draft
  made spares from stored defaults with no permission check and hid them
  from those handlers.
- Hiding is a member-facing rule, not a store filter.
- The row stays `provisioning` and the claim runs the rest of a cold
  launch's provisioning, so a server that stops mid-claim leaves an
  interrupted launch with its task, and failures match a cold launch.
- Spares are made only above twice the memory floor by a check that saves
  nothing, and are destroyed before the guard, an admission or disk reclaim
  takes anything from a session.
- Only an open launch form makes a spare. Nothing is made after a launch,
  on a base move, on a timer or after a failure.
