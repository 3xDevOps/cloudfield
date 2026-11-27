# Cloudfield on several machines: the short version

Written 2026-11-27. The full plan is
[`docs/plans/2026-11-27-multi-machine.md`](../../docs/plans/2026-11-27-multi-machine.md).
Nothing is built.

## Recommendation

1. One server stays the **primary**: database, members, mail between
   agents, git, dashboard. Other machines join it as **hosts** that only
   run sessions.
2. A host runs the same binary, dials out to the primary (works behind
   NAT), and has its own VMs, disks and agent sign-ins.
3. A new session or sub-session goes to the machine with the most free
   memory. If none has room it waits in line; it is not refused.
4. A session stays on the machine it started on. Nothing restarts a
   session on another machine by itself, so a session can never run twice.
5. No BullMQ, Redis or other queue product. A queue re-runs work when a
   worker goes quiet. That is right for jobs and wrong for a live session.

## Phases

| Phase | What you get | Size |
| --- | --- | --- |
| 0. One machine that waits and explains | A full server queues launches and sub-sessions instead of dropping them, and shows why. Every refusal is counted | About 4 pull requests |
| 1. A second machine takes sessions | Add a machine with one command. Sub-sessions and new sessions start there. Agents on both machines message, wake and fetch each other's work as today | About the size of the microVM migration |
| 2. Losing a machine cleanly | Mark a dead machine lost and start its sessions again from their branches elsewhere, safely | About 6 pull requests |
| 3. Move a sleeping session | Only if phase 0's numbers show it is needed | Not sized |

Phase 0 is worth shipping even if phase 1 never happens. It fixes the part
of 2026-11-26 that a second machine would not have fixed: launches that
vanish and no view of why.

## What a second machine does not fix

Sessions asleep on a full machine. Their disks and saved memory are on that
machine. They wake when it has room again.

## Decisions only you can make

1. **Sign in to agents once per machine, or copy logins between
   machines?** Recommended: once per machine. Claude Code and Codex replace
   their refresh token on every refresh, and the repository's own rule is
   that a second copy can revoke the real login. Cost: more clicks, and a
   machine you have not signed in to takes none of your sessions.
2. **May a host connect through the hosted edge?** Recommended: not yet.
   Tailnet, LAN or a public address first. A whole machine's git and
   terminal traffic through the relay needs an edge change and a decision
   on who pays for it.
3. **Whose machines?** Recommended: a super admin adds machines for the
   whole team, and a workspace can list which machines it may use. Letting
   a member attach a personal machine for their own sessions comes later.
4. **Build phase 1 now, or after two weeks of phase 0's numbers?**
   Recommended: ship phase 0, look at the numbers, then decide.

## Where the evidence is

| File | What it holds |
| --- | --- |
| [`evidence/01-capacity-and-vm-runtime.md`](evidence/01-capacity-and-vm-runtime.md) | How one server decides it is full, sleeps, wakes and recovers sessions; pull requests #1153 to #1172 |
| [`evidence/02-agent-coordination.md`](evidence/02-agent-coordination.md) | Every way agents cooperate and where each assumes one machine |
| [`evidence/03-state-and-trust.md`](evidence/03-state-and-trust.md) | The database, git, homes and logins, the edge, updates, roles |
| [`evidence/04-other-systems-placement.md`](evidence/04-other-systems-placement.md) | Nomad, Kubernetes, Fly Machines, Firecracker, Cloud Hypervisor, E2B, Incus |
| [`evidence/05-job-queues.md`](evidence/05-job-queues.md) | BullMQ, River, asynq, NATS JetStream, Temporal |
| [`evidence/06-dev-environments-runners-orchestrators.md`](evidence/06-dev-environments-runners-orchestrators.md) | Coder, Gitpod, CI runner fleets, agent runners, Tailscale |
| [`evidence/07-seam-analysis.md`](evidence/07-seam-analysis.md) | Where the code can be cut between primary and host |
| [`reviews/`](reviews/) | The three attacks on the plan and what changed |

The collectors' rules are in
[`evidence/COLLECTOR-RULES.md`](evidence/COLLECTOR-RULES.md). Pages from
other systems were read through a fetch tool that summarises; quotes are as
returned.
