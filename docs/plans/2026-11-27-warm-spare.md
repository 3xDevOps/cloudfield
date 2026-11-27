# A Warm Spare Session per Active Member

**Goal:** a member who launches an Enhanced session gets an agent that takes
the first prompt in about two seconds or less when a spare matches, instead
of the 5-10 s the owner reports for a cold launch. The server keeps at most
one session VM per active member started ahead of the click, and a launch
that matches it takes it over. A launch that does not match runs cold, as
today.

**Status:** draft, 2026-11-27, not yet reviewed by the owner. The owner
ruled out agent snapshots and warm agent templates on 2026-11-18; this plan
keeps a live process, never a snapshot or a clone, and work item 3, the
first that makes a spare, waits for his decision on the three points marked
*Open for the owner*. Everything marked *Assumption* is this plan's choice.
Everything marked *To measure* is settled by the first work item that can
measure it. The [Review](#review) section lists what review changed.

Terms used below. An **Enhanced** session is one whose agent the server
drives over the Agent Client Protocol (ACP); a **Standard** session runs the
agent's terminal UI as the VM's main process. The **role card** and the
**memory brief** are the instructions and the pinned team notes Cloudfield
gives an agent at its start. The **accepted base** is the commit of the
workspace's source repository that new sessions start from. The **permission
tier** (root, default, isolated) is how much of the VM the agent may change.
The **memory guard** is the server loop that saves session VMs to disk when
the host runs short of memory.

## What was measured

| Step | Time | Where |
| --- | --- | --- |
| One traced cold launch, first host trace to agent ready | about 10.3 s: 4.3 s on the host before the guest kernel (not attributed), 1.8 s boot, mounts and checkout, 0.4 s waiting for the host, 3.7 s agent start | A running session VM, Claude Code 2.1.332, 10-17% CPU steal |
| VM `Start` to ready, standard image | 2.95 s | KVM CI runners (nested KVM, an upper bound) |
| Agent start in a VM whose page cache is warm (`initialize` + `session/new`) | 216-388 ms + 1393-1985 ms | KVM CI runners, Claude Code 2.1.287, adapter 0.86.0 |
| A session made ahead: `session/set_mode` at the click | 3-6 ms | same |
| First command in a running VM | 25 ms | same |
| Codex 0.167.2 `app-server`: spawn to `initialize`, then `thread/start` | 245-324 ms, then 223-307 ms | same |

Not measured: a branch rename after boot, on the server and in the guest
(the git setup at boot takes 64-90 ms), and a claim end to end.

An idle Enhanced Claude Code session holds 420 MiB (adapter 115, Claude 269,
Cloudfield's MCP server 37), and a session VM at ready with no agent holds
411-598 MiB of host memory. One spare costs 0.8-1.0 GiB, before the member's
own MCP servers and the guest page cache.

One measurement decides the design. An agent session made ahead was given a
renamed branch, a new commit and an edited `CLAUDE.md` before its first
prompt, with a local stand-in for the model API recording the request
(Claude Code 2.1.332, adapter 0.86.0, one run):

- The first request still carried the old branch name, the old commit list
  and the old `CLAUDE.md`. Claude Code reads its git context and its
  instruction files at `session/new`, not at the first prompt.
- The session made no model request in the 4 s before its prompt. That run
  used an API key, an empty home, no MCP servers and
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`, none of which a real
  session has. *To measure* before work item 3 ships: a parked session in a
  session VM on a subscription login, for 15 minutes. An agent whose parked
  session spends usage gets no spare.

So a session made ahead is correct only when nothing it read has changed by
the click. A claim that renames the branch must make the agent session
again.

## Rules

- At most one spare per member, only while the member is active, only from
  memory the host does not need (see [Memory](#memory)). Spares are the
  first thing given back.
- Making a spare is authorized exactly as a launch by that member is, every
  time one is made.
- A spare belongs to one member and is only ever claimed by a launch of
  that member, for the workspace, account and login it was made for.
- A claimed spare is the session a cold launch would have made: same run
  ID format, branch name, coordination identity, environment, role card,
  hooks, memory brief, account and login, permission tier, mode, model,
  effort, usage accounting, events, timeline, insights and subsession
  setting. Where that cannot be shown, the launch is a miss.
- A miss is today's cold launch. What a spare may add to it is bounded and
  measured: see [Locks](#locks).
- No option, for members or operators.

## The design in one paragraph

A spare is a real Enhanced session with no task, launched ahead under its
own run ID through the launch handler and the existing provisioning path,
and kept out of every member-facing surface until a launch claims it. The
run ID is minted when the spare is made and becomes the ID of the session
that claims it. Because the ID never changes, nothing keyed by it has to be
bound late: the VM's name and systemd unit, the checkout key, the
`/run/aether` directory, the coordination token and socket and the
environment are already the claimed session's. A claim fills in what only
the click knows: the task, the branch name when the task changes it, model,
effort, mode, the subsession setting and the linked issue.

## What is fixed when the spare starts, and what a claim sets

| Identity | Today | With a spare |
| --- | --- | --- |
| Run ID | Minted by `CreateRun` at launch | Minted when the spare is made. The claiming launch returns it. A launch that brings its own ID (a subsession, `ReservedRunID`) is a miss |
| VM name, unit name, record | The run ID (`internal/runtime/vm`) | Unchanged: already the claimed session's |
| `/run/aether`, coordination token, socket to run mapping | Made per run in `coordinationMounts` | Unchanged: made for the spare's run ID |
| Member whose home the VM mounts, workspace, account, login | Row fields at creation, mounts at boot | Fixed at boot. A claim must match all four |
| `AETHER_RUN_ID`, workspace secrets, account and agent environment, git author, setup script | Built in `containerSpec` | Unchanged, and part of the comparison below |
| Role card and memory brief | The system prompt at `session/new` for agents that take one (`briefedInstructions`), else sent with the first message (`withInstructions`) | Built for the spare with the same function. A claim builds them again and compares; a difference makes the agent session again. First-message agents get them with the task |
| Task | First prompt after `session/new` | First prompt at the claim |
| Branch | `<prefix><task-slug>-<short id>`, or `<prefix><short id>` with no task; the prefix is `cloudfield/run-` (`internal/gitengine/slug.go`) | The spare has the no-task name. A launch with a task renames it on the server and in the guest, then makes the agent session again |
| Base commit | Captured at launch | The accepted base when the spare was made, read without a fetch. A launch whose captured base differs is a miss |
| Mode, model, effort | Calls after `session/new`; opencode takes the model in the VM environment | The same calls at the claim. Where the environment carries one, a different value is a miss |
| Subsession setting of this launch | Recorded with the row before the agent starts | A setting that differs from the one the spare's agent started under makes the agent session again |
| Launch time, `Via`, linked issue | Row fields at creation | Written at the claim, in the transaction that unhides the row |
| Image, permission tier, limits, mounts, agent programs, egress | VM configuration at boot | Compared; any difference is a miss |

## Spare states

The spare manager keeps one record per member with a state: `starting`,
`ready`, `claiming`, `destroying`. Every change of state happens under the
manager's lock.

- Only a `ready` spare is claimed, and the claim moves it to `claiming`
  first. Two launches at once: one claims, the other runs cold.
- A spare in `claiming` is a launch. Replacement, inactivity and the memory
  rules no longer touch it; a kill reaches it as it reaches a launch that
  is provisioning.
- Everything that destroys a spare moves it to `destroying` first, so a
  launch finds no spare and runs cold.

The run row of a spare stays in the `provisioning` status for as long as it
is a spare, and its state transitions are not recorded.

## Claim

A launch request reaches the claim after today's validation,
`AuthorizeLaunch`, login check and base capture, so every refusal a cold
launch gives is given first. The base capture is a network fetch for a
workspace with a source mirror unless the launch form already refreshed it
(#1166, a draft today), and a claim pays it like any launch. Memory
admission moves after the claim attempt: a claim starts no VM and reserves
nothing.

1. Take the member's spare if it is `ready`. A spare that is still starting
   is not waited for.
2. Compare the launch with the spare at VM level: member, workspace,
   account, login, agent, base commit, and a fingerprint of everything
   `containerSpec` puts in the VM (image, user, limits, mounts, environment
   including workspace secrets, setup script, program disks, egress
   policy). Any difference is a **miss**: the spare's reservations are
   released, its VM is destroyed in the background, and the launch goes on
   to admission and runs cold.
3. Compare what the agent session read at its start: the instructions
   text, the branch name the launch would get, the subsession setting, the
   session's age and a digest of the agent's configuration files (below).
   If all are equal the claim is **exact**; otherwise it is **warm**.
4. In one transaction: write the launch's fields to the run row, clear the
   `spare` mark, and record the state transitions a cold launch has
   recorded by this point, stamped now. The row is still `provisioning`.
5. Warm only: rename the branch if the task changes it, then start the
   agent again through the driver's existing new-session open.
6. Set mode, model and effort, send the task, then move the row to
   `running` and publish the events a cold launch publishes.

Computing the fingerprint must not provision anything. Today the code that
builds the specification also reserves the run's user, applies ownership,
writes the coordination files and prepares egress (`runPlan`,
`coordinationMounts`); work item 3 separates the reading from the applying.

| Claim | When | Expected, after the base capture |
| --- | --- | --- |
| Exact | No task, and nothing the session read has changed | Bookkeeping plus 3-6 ms of agent calls. *To measure* end to end |
| Warm | A task that names the branch, a changed role card, agent setting or subsession setting, a session older than 15 minutes | A branch rename plus a warm agent start of 1.6-2.4 s. *To measure* end to end |
| Miss | Anything at VM level differs, no spare, spare not ready | Today's cold launch |

A dashboard launch almost always has a task, so most claims are warm.

*Open for the owner:* if a claimed session may keep the no-task branch name
whatever its task, every claim with unchanged settings is exact. This plan
assumes no: the branch name stays what a cold launch gives.

After step 4 the session is a launch that is provisioning, with its task on
the row, visible and killable. A failure in steps 5 and 6 fails it the way a
failed cold launch fails and destroys the VM; the claim checks for a kill
where provisioning does. A server that stops between steps 4 and 6 finds a
`provisioning` row at its next start and marks the launch interrupted, as it
does for a cold launch today: the member sees the failure and the task. A
claim never falls back to cold after step 4.

## The configuration digest

An exact claim needs proof that the files the agent read at its start are
the ones a cold launch would read now. The checkout is covered by the base
commit: a spare's checkout is untouched. The member's home is not, and all
of a member's sessions share it. Each agent profile in `internal/harness`
lists the home paths its agent reads at start (for Claude Code: user
settings, user `CLAUDE.md`, plugins, skills, agents, commands, MCP
configuration and the login file). The server hashes their content when the
spare's session is made and again at the claim. The home is a folder on the
server, so this is local file reads.

Claude Code writes under `~/.claude/plugins` on every start (`.in_use`
markers, `data/`, `store/`), so the list leaves out what the agent itself
rewrites. *To measure:* the exact list per agent, from what the agent opens
during `session/new`, and whether an exact claim ever holds while another
session of the member is running. A profile with no list never claims
exactly.

A session made ahead also ages: it holds the date and the output of the
member's own SessionStart hooks from its start. A session older than 15
minutes is claimed warm. *Assumption.* It is not made again on a timer.

## Lifecycle

**Active** means that in the last 10 minutes the member opened the
dashboard's launch form or launched a session themselves. *Assumption.* The
dashboard reports the form with the request that refreshes the base
(#1166). An open event stream does not count, and neither does a launch by
a schedule, a template or a subsession.

- **Made** when a member becomes active and after each claim or miss, for
  the workspace, agent, account, login, permission tier and limits of the
  launch form's current choice or else of the member's last launch, which
  the manager keeps in memory. Each make runs `AuthorizeLaunch` for that
  member, account and workspace and the login check, holding the
  authorization lock as a launch does, and uses the accepted base without
  fetching.
- **Replaced** (destroyed, then made again while the member is active) when
  the accepted base moves, the member's image or logins change, an agent
  program updates, or the form's choice changes.
- **Destroyed** when the member goes inactive, on a miss, when memory is
  needed, when a revocation or deletion reaches it (below), and at server
  start. A spare is never recovered after a restart: recovery deletes
  `spare` rows with their VM, unit, checkout, branch and coordination
  directory.
- **Not retried.** A make that fails or is refused, and a spare destroyed
  for memory, are not made again until the member's next launch or next
  opening of the launch form.
- A spare is never saved to disk, frozen, or closed for being idle. Each of
  the scheduler's loops over live sessions (idle sleep, idle close, freeze,
  out-of-memory watch, disk guard) gets a stated answer for a spare in work
  item 3; the first three skip it.

## Revocation and deletion

Today a workspace revoke, an account share revoke, a role change, a member
removal, an account move and a workspace delete each look at the live
sessions and refuse or fence them, in a handler that holds the
authorization lock. With spares, each handler first destroys every spare it
would have counted and waits for the VM to be gone, then goes on as today. A spare never blocks
one of them and never survives one. Because a make holds the same lock for
its authorization and row creation, none can start in between.

## Hidden until claimed

The run row carries a `spare` mark. Server-internal code sees the row like
any other: recovery, cache ownership, storage accounting, coordination for
the run itself (its MCP server reads its own status at start) and the
revocation paths above. Member-facing surfaces do not:

- No session list, count, peer roster, insight, usage or cost answer
  includes the run, and a lookup by ID from a member answers not found.
- No event of the run is published and none is written to the timeline,
  whichever package would publish it (the scheduler, and the git engine's
  branch events). The claim writes what a cold launch writes, stamped at
  the claim.
- The run takes no coordination mail and cannot be named as a parent.

Two things stay visible, on purpose:

- An operator sees a spare. The storage page lists its disks as the
  member's spare, and the server logs each make and destroy with the
  member, the workspace and the reason.
- The spare's branch exists in the workspace repository while the spare
  lives, with no commits of its own, and a member who fetches sees it.
  Destroying the spare deletes it.

Work item 1 lists every store query and every event publisher that touches
runs with its answer for a `spare` row.

## What runs before anyone clicks

Making a spare boots a VM, which runs the workspace's setup script with the
workspace's secrets, and makes an agent session, which runs the member's and
the checkout's SessionStart hooks and starts their MCP servers. This is
what a launch by that member would run, it is authorized as one, and it
happens once per make: when the member becomes active, after a launch, and
when a base, image, login or program change replaces the spare. A hook that
notifies or syncs fires each time.

*Open for the owner:* whether hooks and setup scripts may run for a session
nobody has asked for yet.

## Memory

The memory guard starts saving sessions when available host memory falls
below the floor (`memoryFloor`: the larger of 2 GiB and a tenth of the
host) and stops at twice the floor. Launch admission keeps the floor plus
512 MiB per starting VM. Spares live only above all of that:

- A spare is made only while the guard is calm, no session it saved is
  waiting to wake, and available memory, less what admissions in flight
  have reserved, less 1 GiB for the spare, stays at or above twice the
  floor. This check never saves a session and never reclaims anything to
  make room.
- Each guard check destroys every spare when available memory is below
  twice the floor, before it considers saving a session.
- A launch or wake whose admission comes up short destroys spares and
  measures again before it saves an idle session or refuses.

On an 8 GiB host the floor is 2 GiB, so a spare needs 5 GiB available and
such a host holds one rarely. On a 64 GiB host the bar is 13.8 GiB
available. Above the bar, spares hold 0.8-1.0 GiB per active member.

*Open for the owner:* that cost. His constraint for this work is no
significant rise in memory use; a spare uses only memory that is free
beyond what the guard asks for and is the first thing returned, but it is
0.8-1.0 GiB per active member while that holds.

## Locks

Two locks a make holds can delay someone else's launch:

- The authorization lock, held from `AuthorizeLaunch` to the created row. A
  make does not fetch, so it holds it for local work only.
- The cache locks of the member's home and of the account owner
  (`lockRunCaches`), held until the VM has started. Any launch that needs
  one of them, the member's own or another member's on the same shared
  account, cancels the make, and so does a launch of the member that finds
  the spare still `starting`.

A miss on a `ready` spare waits for the spare to release its run user and
its cache ownership before the cold launch provisions.

*To measure* in work item 3, on KVM: how long a cancelled make keeps the
cache locks, and how long a miss waits for the release. Each must stay under
50 ms, or the make and the release are changed before spares ship.
*Assumption.*

## Other agents and Standard sessions

| Session | Spare | Why |
| --- | --- | --- |
| Claude Code, Enhanced | Exact and warm claims | Measured above |
| Codex, Enhanced | Warm claims only | `thread/start` takes the folder, model and instructions per thread and costs 223-307 ms with no saving from parking a thread; the spare saves the boot, the server work and the cold page cache. Not measured through `codex-acp` |
| Other Enhanced agents | Warm claims only | Nothing measured; the warm claim uses only the path every Enhanced launch uses |
| Standard, any agent | None in the first version | The agent is the VM's main process and its task and instructions are its arguments, fixed at boot |
| Background (one-shot) | None in the first version | A different role card and approval policy; rare from the dashboard |

One spare serves one agent, so a member who switches agents misses until
the launch form's choice replaces the spare.

## Left out of the first version

- Fast-forwarding a spare whose base moved. The spare is replaced instead.
  *To measure:* the miss rate this causes on a busy repository.
- Waiting for a spare that is still starting.
- Making an aged agent session again on a timer.
- Spares for subsessions, extra repositories, and launches from another
  session's commit.
- Claude Code's `prewarm()` and `claim()` (329 ms measured). The adapter
  does not call them, and a claim cannot set the environment a session
  needs.

## Work items

1. **Spare rows.** The `spare` mark, the member-facing filters, the event
   and timeline gate, recovery that deletes spare rows. No spare is made
   yet.
2. **Branch rename after boot.** The git engine and the guest git service
   rename a run branch that has no commits of its own. Measured.
3. **Spare and exact claim** for Claude Code with no task, after the
   owner's decisions and the usage measurement: the manager and its states,
   authorization on each make, the memory rules, the revocation and
   deletion paths, the fingerprint, the claim, the lock measurements. KVM
   measurement.
4. **Warm claim:** instructions, digest, age and subsession comparison,
   rename, new agent session. All Enhanced agents.
5. **Launch form** reports its choice.
6. Documentation: `docs/enhanced-sessions.md`, `docs/vm-guest.md`,
   `docs/failure-handling.md` (memory pressure, what stays after a server
   restart), `docs/security.md` and `docs/privacy.md` (what runs before a
   click), `docs/teams.md` (revocation), `docs/environments.md` (setup
   script), `docs/insights.md`.

Items 1 and 2 change no behavior.

## Test plan

- **Parity.** Launch one session cold and one by claim with the same
  request, then compare the run as the API returns it, its event kinds in
  order, its timeline, its state transitions and insights, its environment
  as a process in the VM sees it, its branch, its subsession tools, the
  first request the agent sends to a stand-in model API (system prompt, git
  context, instruction files), and its usage rows. Only IDs and times may
  differ. Runs for an exact and a warm claim.
- **Every miss and every downgrade has a test:** another workspace, agent,
  account or login, image, permission tier, limits, changed workspace
  secret or setup script, changed agent program, moved base, extra
  repository, reserved run ID, Standard mode, spare not ready give a cold
  launch; a task, a linked issue, a changed brief, a subsession setting,
  each digest path changed, and an aged session give a warm claim.
- **Isolation.** A second member's launch with the same workspace and agent
  never claims the first member's spare. A spare is absent from every
  member-facing list and count, and from a member's lookup by ID. No spare
  is made for a member who may not launch in that workspace or on that
  account.
- **Revocation.** Each of workspace revoke, share revoke, role change,
  member removal, account move and workspace delete, run while a spare is
  alive, succeeds and leaves no VM of the spare.
- **Claim races.** Two launches at once: one claims, one runs cold, each
  with its own task. A destroy that races a claim yields either a claim or
  a cold launch, never a failed one.
- **Stopped mid-claim.** The server is stopped after step 4; at its next
  start the launch shows as interrupted with its task.
- **Memory.** No spare is made below the bar, and making one saves no
  session; a guard check below twice the floor destroys spares first; a
  launch that would be refused destroys spares and is admitted.
- **Locks.** A launch that arrives while a spare is being made, by the same
  member and by another member on the same account, finishes within the
  cold launch's time plus the measured bound.
- **KVM** (`make test-kvm`): member active, spare ready, launch, first
  prompt accepted, printing click to ready for an exact claim, a warm claim
  and a cold launch in the same run, and the host memory the spare holds. A
  second launch right after takes the cold path.
- **Restart.** A server restart with a spare alive leaves no VM, unit, run
  row, checkout, branch or coordination directory of it.

## Review

Two adversarial reviews of the first draft changed these:

- A spare is authorized as a launch on every make, and every revocation and
  deletion path destroys spares first. The draft made spares from stored
  defaults with no permission check and hid them from the paths that
  enforce a revoke.
- Hiding is a member-facing rule, not a store filter, so recovery, cache
  ownership and the run's own coordination still see the row.
- The spare's row stays `provisioning` until its task is sent, so a server
  that stops mid-claim leaves an interrupted launch, not an idle session
  that never got its prompt. Spare states make a claim exclusive.
- Spares are made only above twice the memory floor by a check that saves
  nothing, and are destroyed before the guard or an admission saves a
  session. The draft used launch admission, which saves idle sessions to
  make room.
- A claim reserves no memory; the comparison covers workspace secrets and
  the setup script and must not provision.
- "Active" no longer follows an open event stream, and nothing is made
  again on a timer or retried after a failure.
- The memory cost is 0.8-1.0 GiB, not 0.8; the usage, rename and lock
  numbers are marked unmeasured.
