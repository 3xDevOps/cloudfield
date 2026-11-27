# A Warm Spare Session VM per Active Member

**Goal:** when a member launches an Enhanced session, the server work before
the guest boots and the guest boot are already paid for, so the click waits
only for the agent to start. The server keeps at most one **spare** per
member who has the launch form open: a session VM booted ahead of the click
with no agent in it. A launch that matches the spare takes it over (a
**claim**). A launch that does not (a **miss**) runs cold, as today.

**Status:** draft, 2026-11-27, not yet reviewed by the owner. The owner
ruled out agent snapshots and warm agent templates on 2026-11-18; this plan
keeps a live VM, never a snapshot or a clone, and work item 3, the first
that makes a spare, waits for the owner's decision on the two points marked
*Open for the owner*. Everything marked *Assumption* is this plan's choice.
Everything marked *To measure* is settled by the first work item that can
measure it. The [Review](#review) section lists what review changed.

Terms used below. A session is a row with a **run ID** and, while it runs, a
VM. An **Enhanced** session is one whose agent the server drives over the
Agent Client Protocol (ACP); a **Standard** session runs the agent's
terminal UI as the VM's main process; a **Background** session runs one
prompt and ends; a **subsession** is a session another session's agent
starts. The **accepted base** is the commit of the workspace's source
repository that new sessions start from. The **setup script** is the
workspace's own script that runs in every new VM. The **permission tier**
(root, default, isolated) is how much of the VM the agent may change. The
**memory guard** saves session VMs to disk when the host runs short of
memory, and the **memory brake** freezes them first. The **authorization
lock** is the server-wide lock a launch holds from its permission check
until its session row exists, so that no revoke can fall in between.

## What was measured

| Step | Time | Where |
| --- | --- | --- |
| One traced cold launch, first host trace to agent ready | about 10.3 s: 4.3 s on the host before the guest kernel (not attributed) and 6.0 s in the VM, of which 1.8 s boot, mounts and checkout, 0.4 s waiting for the host and 3.7 s agent start | A running session VM on a loaded host, Claude Code 2.1.332, one launch |
| VM `Start` to ready, standard image | 2.95 s | KVM CI runners (nested KVM, an upper bound) |
| First command in a running VM | 25 ms | same |
| Session VM stop | 0.23 s | KVM CI runner, #1059 |

A session VM of the standard image holds 593-599 MiB of host memory once
its Docker daemon is up (KVM CI runners, no setup script).

Not measured: a claim end to end, and a branch rename after boot (the git
setup at boot takes 64-90 ms).

## What the spare does not hold

**No agent.** The first draft parked a whole agent session in the spare,
which answers a click in 3-6 ms (`session/set_mode`, measured). It is left
out:

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
  37, resident, measured).

**No warmed page cache.** The agent started in 1.6-2.4 s where its programs
were already in the guest's page cache, against 3.7 s in the traced launch.
Reading the programs ahead does not keep them there: the guest helper frees
a quarter of the idle page cache every 10 s
(`internal/guest/helper/trim_linux.go`), and holding them would cost about
0.6 GiB per spare (Claude binary 383 MiB, adapter 297 MB). So a
claim pays the agent start of a fresh VM.

Expected click to ready for a claim: the base capture, a branch rename and
the agent start, so about 6 s less than the 10.3 s traced. *To measure* end
to end on KVM.

## Rules

- At most one spare per member, only while the member has the launch form
  open, only from memory and disk the host does not need (see
  [Memory](#memory-and-disk)). Spares are the first thing given back.
- Making a spare passes every check a launch by that member passes, every
  time one is made. A refused or failed make tells nobody but the server
  log.
- A spare belongs to one member and is only ever claimed by a launch of
  that member that matches it.
- A spare never makes the server refuse or delay anyone. Wherever a live
  session would (its run user, its cache locks, a workspace delete, a
  revoke, a member merge), the spare is destroyed instead.
- A claimed spare is the session a cold launch would have made. From the
  claim on it runs the code a cold launch runs after its VM has started.
- No option, for members or operators.

## The design in one paragraph

A spare is a real Enhanced session with no task, launched ahead under its
own run ID through the launch handler and the existing provisioning path,
stopped at the point where its VM has started and its agent has not, and
kept out of every member-facing surface until a launch claims it. The run ID
is minted when the spare is made and becomes the ID of the session that
claims it. Because the ID never changes, nothing keyed by it has to be bound
late: the VM's name and systemd unit, the checkout, the `/run/aether`
directory, the session's coordination token and socket and the environment
are already the claimed session's. A claim writes what only the click knows,
renames the branch, and runs the rest of provisioning: agent start, agent
mode, model, effort, task.

## What is fixed when the spare starts, and what a claim sets

| Identity | Today | With a spare |
| --- | --- | --- |
| Run ID | Minted by `CreateRun` at launch | Minted when the spare is made. The claiming launch returns it |
| VM name, unit name, record | The run ID (`internal/runtime/vm`) | Unchanged: already the claimed session's |
| `/run/aether`, coordination token, socket to run mapping | Made per run in `coordinationMounts` | Unchanged: made for the spare's run ID |
| Member whose home the VM mounts, workspace, account | Row fields at creation, mounts at boot | Fixed at boot. A claim must match all three, after the launch has chosen its account |
| Image, user, permission tier, limits, mounts (including the login the VM mounts), environment (including `AETHER_RUN_ID`, workspace secrets, git author), setup script, agent programs, egress | Built in `containerSpec`, fixed at boot | A fingerprint of all of it is compared at the claim. Any difference is a miss. This covers an agent that takes its model or effort in the environment or its arguments, and a changed login |
| Base commit | Captured at launch | The accepted base when the spare was made, read without a fetch. A launch whose captured base differs, or a spare whose branch no longer points at its base, is a miss |
| Branch | `<prefix><task-slug>-<short id>`, or `<prefix><short id>` with no task (`internal/gitengine/slug.go`) | The spare has the no-task name. A launch with a task renames it on the server and in the guest before the agent starts |
| Everything else the launch request puts on the row and on the scheduler's entry: task, model, effort, fallback policy, requested account, base source and check time, `Via`, linked issue, launch time, idle clocks | Written at creation | Written at the claim, in the transaction that unhides the row |
| Instructions, subsession setting, agent mode, model, effort, task | Set when the agent starts and right after | The same code, run at the claim |

## Spare states

The spare manager keeps one record per member with a state: `starting`,
`ready`, `claiming`, `destroying`. Its lock guards these changes only:
nothing else is taken or waited for under it, so it is innermost to the
authorization lock, the memory guard's lock and the admission gate.

- Only a `ready` spare is claimed, and the claim moves it to `claiming`
  first. Two launches at once: one claims, the other runs cold.
- A destroy takes a spare only from `starting` or `ready`. A spare in
  `claiming` is a launch and nothing destroys it; when the claim's
  transaction commits, the manager drops the record.
- A destroy moves the spare to `destroying`, cancels its make if one is
  running, stops the VM, then deletes the checkout, branch and coordination
  directory, and the row last. A VM that cannot be removed keeps its row,
  so the next server start finds it and tries again. A destroy never goes
  through the kill or failure paths of a session, so it leaves no finished
  or failed session, no commit and no published branch.
- A make moves its spare from `starting` to `ready` after its VM has
  started. If the state is no longer `starting`, by the authorization lock
  before the row exists or at that move, the make stops and the destroy
  above runs. A make that fails is destroyed the same way.
- The manager watches the spare's unit. A VM that ends on its own is a
  destroy.

The run row of a spare stays in the `provisioning` status for as long as it
is a spare, and its state transitions are not recorded.

## Claim

Only the member's own launch of one Enhanced session in one repository can
claim. A subsession, a launch that brings its own run ID, a Standard or
Background session, a launch with extra repositories or from another
session's commit, and a launch by a schedule or template run cold and leave
the spare alone.

A launch that can claim reaches the claim after today's validation,
`AuthorizeLaunch`, budget and login checks, account choice and base capture,
holding the authorization lock as today. The base capture is a network
fetch for a workspace with a source mirror unless the launch form already
refreshed it (#1166, a draft today), and a claim pays it like any launch.

1. Take the member's spare if it is `ready`. A spare that is still starting
   is not waited for.
2. Check that the VM answers within a second (the 25 ms first command) and
   compare the launch with the spare: member, workspace, account, agent,
   base commit, branch tip and the fingerprint. A VM that does not answer
   or any difference is a **miss**: the spare is destroyed and the launch
   goes on to admission and runs cold. It waits for the spare's VM to be
   gone only if admission comes up short or its run user is still reserved
   by the spare.
3. Run memory admission's refusals (memory pressure, disk room) with no new
   reservation: the VM is already running. A refusal refuses the launch as
   it would a cold one, and the spare goes back to `ready`.
4. In one transaction: write the launch's fields to the run row, clear the
   `spare` mark, and record the state transitions a cold launch has
   recorded by this point, stamped now. If it fails, the spare is destroyed
   and the launch runs cold. Then the launch's `Created` callback runs
   (idempotency receipt, subsession setting), and the authorization lock is
   released, both as for a cold launch. The row is `provisioning`.
5. Rename the branch if the task changes it.
6. Run the rest of provisioning as a cold launch runs it: the access check
   it makes before its VM starts, made again here; agent start with the
   instructions built now, agent mode, model, effort, task; the move to
   `running`; supervision; and the check for a revoke that fell in between
   (`leaveIfRevoked`).

Computing the fingerprint must not provision anything. Today the code that
builds the specification also reserves the run's user, applies ownership,
writes the coordination files and prepares egress (`runPlan`,
`coordinationMounts`, `startRunContainer`); work item 3 separates the
reading from the applying.

After step 4 the session is a launch that is provisioning, with its task on
the row, visible and killable, and everything that can go wrong goes wrong
the way it does for a cold launch, because it is the same code. A server
that stops after step 4 finds a `provisioning` row at its next start and
marks the launch interrupted, as today: the member sees the failure and the
task. A claim never falls back to cold after step 4.

## Lifecycle

The dashboard reports the launch form and its choice (workspace, agent,
account, permission tier, limits) when the form opens, when a choice has
stayed unchanged for two seconds, and when it closes. *Assumption.* The
request that refreshes the base (#1166) can carry the report. An open event
stream is not a report, and neither is any launch.

- **Made** on a report that the form is open, after the base refresh that
  comes with it, for its choice, at most once per member in 30 seconds.
  *Assumption.* Each make runs what a launch runs before it starts a VM:
  `AuthorizeLaunch` for that member, account and workspace under the
  authorization lock, a read of the workspace budget that publishes
  nothing, the login check, and admission's pressure and disk refusals. It
  uses the accepted base without fetching. In place of admission's memory
  reservation it has its own bar (below).
- **Destroyed** when the form closes or its choice changes, when 10 minutes
  pass without a report, on a miss, when the accepted base moves, when the
  member's image changes or an agent program updates, when memory or disk
  is needed, when anything in [the next section](#never-in-the-way) reaches
  it, and at server start. A spare is never recovered after a restart:
  recovery destroys `spare` rows.
- **Made again** only by the next report from the form. Nothing is retried
  or made on a timer, so a push to the base branch starts no VM for
  anyone.
- A spare is never saved to disk, frozen, or closed for being idle, and it
  does not make the server busy for an update. Each of the scheduler's
  periodic loops over live sessions gets a stated answer for a spare in
  work item 3.

A member who clicks before the make finishes (a boot takes seconds) runs
cold. *To measure* in work item 3: the share of dashboard launches that
claim. If it is low, spares are not turned on.

## Never in the way

A session that is provisioning makes the server refuse or wait in several
places today. A spare must not, so each of these destroys the spare first,
waits until its VM is gone, and then goes on as if it had never existed:

- Workspace revoke, account share revoke, member removal, member merge and
  workspace delete, which look at live sessions to refuse, stop or move
  them. Each already holds the authorization lock, and a make creates its
  row under that lock, so no spare can appear behind it. The wait is the
  VM stop (0.23 s measured, 15 s at worst, the unit's stop timeout); these
  are rare, and today the same request is refused outright while a session
  is provisioning.
- A launch or terminal that needs the run user the spare reserved on the
  member's home or the account owner's login.
- Anything that takes a cache lock a make holds (`lockRunCaches`: the
  member's home and the account owner's): launches and terminals of both
  members, environment saves, GitHub connect, program updates, account
  moves. The lock is a plain mutex today; work item 3 adds the destroy
  before each of these takes it.

A login that is added, removed, reordered or switched needs no handler: it
changes which login a launch mounts, so the fingerprint makes the claim a
miss.

*To measure* in work item 3, on KVM: how long each of these waits for a
spare in `starting` and in `ready`. A wait over 0.3 s outside the revoke
and delete handlers changes the make before spares ship. *Assumption.*

## Hidden until claimed

The run row carries a `spare` mark. Server-internal code sees the row like
any other: recovery, cache ownership and storage accounting. Member-facing
surfaces do not:

- No session list, count, peer roster, insight, usage, cost, disk or
  memory-brief answer to a member includes the run, and a lookup by ID
  answers not found.
- No event of the run is published and none is written to the timeline,
  whichever package would publish it (the scheduler, and the git engine's
  branch events). The claim writes what a cold launch writes, stamped at
  the claim.
- The run's coordination socket refuses every call until the claim. No
  agent runs in a spare, so only a setup script could call it.
- A push to a spare's branch is refused.

Two things stay visible, on purpose:

- An admin sees spares on the storage page, listed as a member's spare with
  no link to a session, and the server logs each make and destroy with the
  member, the workspace and the reason. An admin can therefore see who has
  the launch form open.
- The spare's branch exists in the workspace repository while the spare
  lives, with no commits of its own, and a member who fetches sees it.

Work item 1 lists every store query over runs, every reader of the active
sessions and every event publisher with its answer for a `spare` row.

## What runs before anyone clicks

Booting a spare runs the workspace's setup script with the workspace's
secrets, in a VM that mounts the member's home and the account's login.
This is what a launch by that member would run, it passes a launch's
checks, and it happens only when that member opens the launch form or
changes its choice. No agent, hook or MCP server runs, and no model request
is made.

*Open for the owner:* whether a setup script may run for a session nobody
has asked for yet.

## Memory and disk

The memory brake freezes sessions and the guard starts saving them when
available host memory falls below the floor (`memoryFloor`: the larger of
2 GiB and a tenth of the host), or when the host stalls on memory; the
guard stops at twice the floor. Launch admission keeps the floor plus
512 MiB per starting VM. Spares live only above all of that:

- A make charges 1 GiB, counted with the admissions in flight: the 0.6 GiB
  measured plus room for what a setup script starts. *To measure:* what
  spares hold in production; the charge follows.
- A spare is made only while the guard is calm, no session it saved is
  waiting to wake, and available memory less the admissions in flight and
  the spare's charge stays at or above twice the floor. This check never
  saves a session and never reclaims anything to make room.
- The brake and the guard destroy every spare when available memory is
  below twice the floor or a stall lasts. A check that destroyed a spare
  does nothing else; the next check measures again.
- A launch or wake whose admission comes up short, or is refused for
  memory pressure, destroys spares, waits for their VMs to be gone and
  measures again before it saves an idle session or refuses.
- A spare's VM processes run with the highest out-of-memory score, reset
  to a session's before claim step 4, so the kernel takes a spare before a
  session.
- A make needs the disk room a launch needs and is refused once disk
  reclaim has started; disk reclaim destroys spares before it takes
  anything from a session.

On an 8 GiB host the floor is 2 GiB, so a spare needs 5 GiB available and
such a host holds one rarely. On a 64 GiB host the bar is 13.8 GiB
available. Above the bar, a spare holds about 0.6 GiB per member with the
launch form open.

*Open for the owner:* that cost. The owner's constraint for this work is no
significant rise in memory use; a spare uses only memory that is free
beyond what the guard asks for and is the first thing returned.

## Standard sessions and agents

A Standard session gets no spare in the first version: the agent is the
VM's main process, and its task and instructions are its arguments, fixed
at boot. Every Enhanced agent is served alike, because the spare holds no
agent. One spare is booted for one agent's programs and environment, so a
member who switches agents in the form gets a new spare.

## Left out of the first version

- An agent session made ahead and a warmed page cache (see above), and
  Claude Code's `prewarm()` and `claim()` (329 ms measured; the adapter
  does not call them).
- Starting the ACP adapter ahead (`initialize`, 216-388 ms): its arguments
  and environment can depend on the click.
- Fast-forwarding a spare whose base moved.
- Waiting for a spare that is still starting.
- Spares for the launches the [Claim](#claim) section lists as never
  claiming.

## Work items

1. **Spare rows.** The `spare` mark, the member-facing filters, the event
   and timeline gate, recovery that destroys spare rows. No spare is made
   yet.
2. **Branch rename after boot.** The git engine and the guest git service
   rename a run branch that has no commits of its own. This item measures
   it.
3. **Spare and claim**, after the owner's decisions: provisioning split at
   the point where the VM has started, the manager and its states, the
   checks on each make, the memory and disk rules, every destroy in
   [Never in the way](#never-in-the-way), the fingerprint, the claim, the
   wait measurements. KVM measurement of the claim and of the share of
   launches that claim.
4. **Launch form** reports its choice.
5. Documentation: `docs/enhanced-sessions.md`, `docs/vm-guest.md`,
   `docs/failure-handling.md` (memory pressure, what stays after a server
   restart), `docs/security.md` and `docs/privacy.md` (what runs before a
   click, what an admin sees), `docs/teams.md` (revocation),
   `docs/environments.md` (setup script).

Items 1 and 2 change no behavior.

## Test plan

- **Parity.** Launch one session cold and one by claim with the same
  request, then compare the run as the API returns it, its event kinds in
  order, its timeline, its state transitions and insights, its environment
  as a process in the VM sees it, its branch, its subsession tools, the
  first request the agent sends to a stand-in model API (system prompt, git
  context, instruction files), its usage rows, and its move to a fallback
  account at a usage limit. Only IDs and times may differ. A retry with the
  same idempotency key returns the same session.
- **Every miss has a test:** another workspace, agent or account, image,
  permission tier, limits, changed workspace secret or setup script,
  changed login, changed agent program, moved base, a commit pushed to the
  spare's branch, spare not ready, a VM that does not answer.
- **Launches that cannot claim** run cold and the spare is still `ready`
  afterwards.
- **Isolation.** A second member's launch with the same workspace and agent
  never claims the first member's spare. A spare is absent from every
  member-facing list and count, and from a member's lookup by ID. No spare
  is made for a member who may not launch in that workspace or on that
  account, or whose workspace is over budget, and that refusal publishes
  nothing. A spare's coordination socket refuses a call.
- **Never in the way.** Each of workspace revoke, share revoke, member
  removal, member merge and workspace delete, run while a spare is
  `starting` and while it is `ready`, succeeds and leaves no VM, unit or
  row of the spare. The account owner's launch and terminal succeed while
  another member's spare holds their login. A share revoked between claim
  steps 4 and 6 stops or moves the session as it does for a cold launch.
- **Claim races.** Two launches at once: one claims, one runs cold, each
  with its own task. Every destroy trigger, fired during a claim after
  step 4, leaves the session and its task untouched.
- **Failed make.** A setup script that fails leaves no row, VM, branch or
  member-facing trace, and the next make waits for the next report.
- **Stopped mid-claim.** The server is stopped after step 4; at its next
  start the launch shows as interrupted with its task.
- **Idle clocks.** A session claimed from a spare that waited 9 minutes is
  not put to sleep before a cold-launched one would be.
- **Memory and disk.** No spare is made below the bar or under memory
  pressure, and making one saves no session; the brake, a guard check below
  twice the floor, a lasting stall and a short admission each destroy
  spares before a session is frozen or saved; disk reclaim destroys spares
  first.
- **KVM** (`make test-kvm`): form reported, spare ready, launch, first
  prompt accepted, printing click to ready for a claim and a cold launch in
  the same run, and the host memory the spare holds. A second launch right
  after takes the cold path.
- **Restart.** A server restart with a spare alive leaves no VM, unit, run
  row, checkout, branch or coordination directory of it.

## Review

Four rounds of adversarial review changed these:

- The spare holds no agent and no warmed page cache.
- A spare passes a launch's checks on every make, and never makes the
  server refuse or wait: every handler that enforces a revoke or a deletion
  destroys spares first and waits for the VM to be gone.
- A destroy takes a spare only before it is claimed and deletes its row
  last, so no claimed session is deleted and no VM is left without a row.
- Hiding is a member-facing rule, not a store filter.
- The row stays `provisioning` and the claim runs the rest of a cold
  launch's provisioning, so a server that stops mid-claim leaves an
  interrupted launch with its task, and failures match a cold launch.
- Spares are made only above twice the memory floor by a check that saves
  nothing, and are destroyed before the brake, the guard, an admission or
  disk reclaim takes anything from a session.
- Only an open launch form makes a spare.
