# A Warm Spare Session per Active Member

**Goal:** a member who launches an Enhanced session gets an agent that takes
the first prompt in well under a second on a hit and under about two seconds
otherwise, instead of the 5-10 s a cold launch takes today. The server keeps
at most one session VM per active member started ahead of the click, and a
launch that matches it takes it over. A launch that does not match runs
exactly as today.

**Status:** draft, 2026-11-27, not yet reviewed by the owner. Everything
marked *Assumption* is this plan's choice and stands unless the owner
changes it. Everything marked *To measure* is settled by the first work
item that can measure it. The owner ruled out "warm agent templates" on
2026-11-18 and reopened the question on 2026-11-26; this plan keeps a live
process, never a snapshot or a clone.

## What was measured

Claude Code 2.1.332 behind `claude-agent-acp` 0.86.0, one vCPU, in a
running session VM and on the KVM CI runners, 2026-11-26 and 2026-11-27.

| Step | Time |
| --- | --- |
| Cold launch, server work before the guest kernel starts | 0.3-4.3 s, with host load |
| Guest boot until the helper answers | 1-2 s |
| Agent start in a fresh VM (`initialize` + `session/new`) | about 3.7 s |
| The same agent start in a VM whose page cache is warm | 1.6-2.4 s (`initialize` 216-388 ms, `session/new` 1393-1985 ms) |
| A session made ahead: `session/set_mode` at the click | 3-6 ms |
| First command in a running or paused VM | 27-35 ms |
| Branch rename and git setup on an attached checkout | 64-90 ms |
| Codex 0.167.2 `app-server`: spawn to `initialize`, then `thread/start` | 245-324 ms, then 223-307 ms |

A parked Claude Code process holds 277 MiB, and a session VM at ready holds
411-598 MiB of host memory, so one spare costs about 0.8 GiB.

One measurement decides the design. An agent session made ahead was given a
renamed branch, a new commit and an edited `CLAUDE.md` before its first
prompt, with a local stand-in for the model API recording the request:

- The first request still carried the old branch name, the old commit list
  and the old `CLAUDE.md`. Claude Code reads its git context and its
  instruction files at `session/new`, not at the first prompt.
- The session made no model request before the first prompt, so a spare
  spends no usage.

So a session made ahead is correct only when nothing it read has changed by
the click. A claim that renames the branch or moves the base must make the
agent session again.

## Rules

- At most one spare per member, only while the member is active, never when
  the host lacks headroom. The spare is the first thing the memory guard
  gives up.
- A launch is never refused, queued or slowed because a spare exists or is
  being made.
- A claimed spare is the session a cold launch would have made: same run
  ID format, branch name, coordination identity, environment, role card,
  hooks, memory brief, account and login, permission tier, mode, model,
  effort, usage accounting, events, timeline and subsession lineage. Where
  that cannot be shown, the launch is a miss.
- A miss is today's cold launch, unchanged.
- A spare belongs to one member and is only ever claimed by a launch of
  that member, for the workspace, account and login it was made for.
- No option, for members or operators. The headroom rule is what keeps a
  small host from holding spares.

## The design in one paragraph

A spare is a real Enhanced session with no task, launched ahead under its
own run ID through the existing provisioning path, and hidden from every
member until a launch claims it. The run ID is minted when the spare is
made and becomes the ID of the session that claims it. Because the ID never
changes, nothing keyed by it has to be bound late: the VM's name and
systemd unit, the checkout key, the `/run/aether` directory, the
coordination token and socket, the environment and the role card are
already the claimed session's. A claim fills in what only the click knows:
the task, the branch name when the task changes it, model, effort, mode and
who launched it.

## What is fixed when the spare starts, and what a claim sets

| Identity | Today | With a spare |
| --- | --- | --- |
| Run ID | Minted by `CreateRun` at launch | Minted when the spare is made. The claiming launch returns it. A launch that brings its own ID (a subsession, `ReservedRunID`) is a miss |
| VM name, unit name, record | The run ID (`internal/runtime/vm`) | Unchanged: already the claimed session's |
| `/run/aether`, coordination token, socket to run mapping | Made per run in `coordinationMounts` | Unchanged: made for the spare's run ID |
| `AETHER_RUN_ID`, workspace, account and harness environment, git author | Built in `containerSpec` | Unchanged. Workspace, account, login and member are part of what a claim must match |
| Role card and memory brief | Sent as the system prompt at `session/new` (`briefedInstructions`) | Built for the spare with the same function. A claim builds them again for the launch and compares; a difference (a linked Linear issue, a changed brief) makes the agent session again with the new text. The first-message path (`withInstructions`) is not needed |
| Cloudfield's SessionStart hook | Reports under the run ID | Reports under the same run ID. Its report is held with the spare's other events until the claim |
| Task | First prompt after `session/new` | First prompt at the claim |
| Branch | `cloudfield/run-<task>-<id>`, or `cloudfield/run-<id>` with no task | The spare has the no-task name. A launch with a task renames it on the server and in the guest, then makes the agent session again |
| Base commit | Captured at launch | The spare's base. A launch whose captured base differs is a miss in the first version |
| Model, effort, mode | Calls after `session/new` | The same calls at the claim |
| Launch time, launcher, `Via`, linked issue | Row fields at creation | Written at the claim, in one transaction that also unhides the row |
| Image, permission tier, limits, mounts, agent programs, egress | VM configuration at boot | A claim builds the container specification a cold launch would use now and compares it with the spare's. Any difference is a miss |

## Claim

A launch request reaches the claim after today's validation, login check
and base capture, so every refusal a cold launch gives is given first.

1. Take the member's spare if it is ready. A spare that is still starting
   is not waited for.
2. Compare the launch with the spare. The VM-level comparison is the
   container specification (image, user, limits, mounts, environment,
   program disks) and the workspace, account, login, agent and base commit.
   Any difference is a **miss**: the spare is destroyed in the background
   and the launch runs cold.
3. Compare what the agent session read at its start: the instructions text,
   the branch name the launch would get, and a digest of the agent's
   configuration files (below). If all are equal the claim is **exact**;
   otherwise it is **warm**.
4. Write the launch's fields to the run row and unhide it.
5. Warm only: rename the branch if the task changes it, then start the
   agent again through the driver's existing new-session open.
6. Set mode, model and effort, publish the events a cold launch publishes,
   send the task.

| Claim | When | Expected click to ready |
| --- | --- | --- |
| Exact | No task, or nothing the session read has changed | 0.1-0.3 s: bookkeeping plus 3-6 ms of agent calls. *To measure* end to end |
| Warm | A task that names the branch, a changed role card or agent setting | 1.6-2.5 s: rename 64-90 ms plus a warm agent start |
| Miss | Anything at VM level differs, no spare, spare not ready | Today's cold launch |

*Open for the owner:* if a claimed session may keep the no-task branch name
`cloudfield/run-<id>` whatever its task, every claim with unchanged settings
is exact. This plan assumes no: the branch name stays what a cold launch
gives.

A failure in steps 4 to 6 fails the launch the way a failed cold launch
does today and destroys the VM. A claim never falls back to cold after step
4, because the session is already visible under its run ID.

## The configuration digest

An exact claim needs proof that the files the agent read at its start are
the ones a cold launch would read now. The checkout is covered by the base
commit: a spare's checkout is untouched. The member's home is not, so each
agent profile in `internal/harness` lists the home paths its agent reads at
start (for Claude Code: user settings, user `CLAUDE.md`, plugins, skills,
agents, commands, MCP configuration and the login file). The server hashes
their content when the spare's session is made and again at the claim. The
home is a folder on the server, so this is local file reads.

*To measure:* the exact list per agent, taken from what the agent opens
during `session/new`, and whether the agent rewrites any listed file on
every start, which would make every claim warm. A profile with no list
never claims exactly.

A session made ahead also ages: it holds the date and any output of the
member's own SessionStart hooks from its start. A spare's agent session
older than 15 minutes is made again in place while the member stays active.
*Assumption.*

## Lifecycle

**Active** means the member has an open dashboard event stream or launched
a session in the last 10 minutes. *Assumption.* Both are facts the server
already has.

- **Made** when a member becomes active, after each claim or miss, and
  after an invalidation, for the workspace, agent, account, permission tier
  and limits of the member's most recent launch (the stored launch
  defaults). The launch form re-targets the spare when the member picks
  another workspace or agent; the request that refreshes the base while a
  task is typed (#1166, a draft today) can carry that. Making a spare waits until no launch of that member
  is in flight and is cancelled when one arrives that cannot claim it,
  because provisioning holds the member's cache lock.
- **Replaced** when the workspace's accepted base moves, the member's
  image, logins or shares change, an agent program updates, or the server
  binary updates: destroyed and made again while the member is active.
- **Destroyed** when the member goes inactive, when the memory guard needs
  room, on a miss, and at server start. A spare is never recovered after a
  restart: recovery deletes hidden runs and their VMs.
- A spare is never saved to disk. If idle sessions come to be kept paused
  in memory (#1159, a draft today), a spare may be too, which costs 27-35 ms
  at the claim.

## Hidden until claimed

The run row carries a `spare` mark. While it is set:

- No list, count, peer roster, insight, usage or cost query returns the
  run, and a lookup by ID answers not found to every caller outside the
  scheduler.
- The scheduler publishes none of the run's events and writes none to the
  timeline. The claim writes the sequence a cold launch writes, stamped at
  the claim.
- The run takes no coordination mail and cannot be named as a parent.

The test that guards this compares a claimed session with a cold-launched
one field by field (below), and lists every store query over runs with its
expected answer for a hidden run.

## Memory

- Making a spare passes the same admission as a launch and additionally
  requires room for one more session start above the floor afterwards, so
  the next cold launch still fits.
- A launch whose admission fails destroys every spare and tries again
  before it refuses.
- The memory guard destroys spares before it saves any session, and counts
  a spare's VM in what it reports.
- With members active and no pressure, spares hold about 0.8 GiB per active
  member.

## Other agents and Standard sessions

| Session | Spare | Why |
| --- | --- | --- |
| Claude Code, Enhanced | Exact and warm claims | Measured above |
| Codex, Enhanced | Warm claims only | `thread/start` takes the folder, model and instructions per thread and costs 223-307 ms with no saving from parking a thread; the spare saves the boot, the server work and the cold page cache. Not measured through `codex-acp` |
| Other Enhanced agents | Warm claims only | Nothing measured; the warm claim uses only the path every Enhanced launch uses |
| Standard, any agent | None in the first version | The agent is the VM's main process and its task and instructions are its arguments, fixed at boot. A spare would need the helper to start the main process on a later call |
| Background (one-shot) | None in the first version | A different role card and approval policy; rare from the dashboard |

One spare serves one agent, so a member who alternates agents misses on
each switch until the launch form re-targets the spare.

## Left out of the first version

- Fast-forwarding a spare whose base moved. It needs the new commits
  carried into the guest and always costs a warm claim. The spare is
  replaced when the base moves instead. *To measure:* the miss rate this
  causes on a busy repository.
- Waiting for a spare that is still starting.
- Spares for subsessions, extra repositories, and launches from another
  session's commit.
- Claude Code's `prewarm()` and `claim()` (329 ms measured). The adapter
  does not call them, and a claim cannot set the environment a session
  needs.

## Work items

1. **Hidden runs.** The `spare` mark, the store filters, the event and
   timeline gate, recovery that deletes hidden runs. No spare is made yet.
2. **Branch rename after boot.** The git engine and the guest git service
   rename a run branch that no process has used.
3. **Spare and exact claim** for Claude Code with no task: the manager
   (active members, making, replacing, destroying), admission and guard
   rules, the specification comparison, the claim. KVM measurement.
4. **Warm claim:** instructions and digest comparison, rename, new agent
   session. All Enhanced agents.
5. **Launch form re-targeting** and the 15 minute refresh.
6. Documentation: `docs/enhanced-sessions.md`, `docs/vm-guest.md`,
   `docs/failure-handling.md` (memory pressure, what stays after a server
   restart).

Items 1 and 2 change no behavior. Item 3 is the first that makes a spare.

## Test plan

- **Parity.** Launch one session cold and one by claim with the same
  request, then compare the run as the API returns it, its event kinds in
  order, its timeline, its environment as a process in the VM sees it, its
  branch, the first request the agent sends to a stand-in model API (system
  prompt, git context, instruction files), and its usage rows. Only IDs and
  times may differ. Runs for an exact and a warm claim.
- **Every miss and every downgrade has a test:** another workspace, agent,
  account or login, image, permission tier, limits, changed agent program,
  moved base, extra repository, reserved run ID, Standard mode, spare not
  ready give a cold launch; a task, a linked issue, a changed brief, each
  digest path changed, and an aged session give a warm claim.
- **Isolation.** A second member's launch with the same workspace and agent
  never claims the first member's spare. A hidden run is absent from every
  list and count, and from another member's lookup by ID.
- **Memory.** A spare is not made without headroom; a launch that would be
  refused destroys the spare and is admitted; the guard destroys the spare
  before it saves a session.
- **Launch is never slowed.** A launch that arrives while the spare is
  being made runs cold and finishes within the cold launch's time.
- **KVM** (`make test-kvm`): member active, spare ready, launch, first
  prompt accepted, printing click to ready for an exact claim, a warm claim
  and a cold launch in the same run, and the host memory the spare holds. A
  second launch right after takes the cold path. Under memory pressure the
  guard destroys the spare.
- **Restart.** A server restart with a spare alive leaves no VM, unit, run
  row, checkout or coordination directory of it.
