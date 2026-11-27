# Cloudfield on several machines: the short version

Written 2026-11-27. The full plan is
[`docs/plans/2026-11-27-multi-machine.md`](../../docs/plans/2026-11-27-multi-machine.md).
Nothing is built.

## Recommendation

1. One server stays the **primary** and keeps everything it has today:
   database, members, mail between agents, git, dashboard, and the
   scheduler that supervises every session.
2. Other machines join as **hosts** that only run session VMs. A host dials
   out to the primary (works behind NAT), and the primary drives each VM
   over that link with the protocol it already uses for VMs on its own
   disk. Agents on different machines talk exactly as they do today,
   because the part that carries their messages does not move.
3. A new session or sub-session goes to the machine with the most free
   memory. If none has room it waits in line; it is not refused.
4. A session stays on the machine it started on. A host that loses its
   link freezes its VMs until the link is back, so a session can never run
   twice.
5. No BullMQ, Redis or other queue product. A queue re-runs work when a
   worker goes quiet. That is right for jobs and wrong for a live session.

## Phases

| Phase | What you get | Size |
| --- | --- | --- |
| 0. One machine that waits and explains | A full server queues launches and sub-sessions instead of dropping them, and shows why. Every refusal is counted | About 4 pull requests |
| 1. A second machine takes session VMs | Add a machine with one command. Sub-sessions and new sessions start there and work with the others as today | About 12 to 15 pull requests |
| 2. Losing a machine cleanly | Mark a dead machine lost and start its sessions again from their branches elsewhere | About 5 pull requests |
| 3. Move a sleeping session | Only if phase 0's numbers show it is needed | Not sized |

Phase 0 is worth shipping even if phase 1 never happens. It fixes the part
of 2026-11-26 that a second machine would not have fixed: launches that
vanish and no view of why.

## What a second machine does not fix

- Sessions asleep on a full machine. Their disks and saved memory are on
  that machine. They wake when it has room again.
- The primary is still one machine. If it is down or out of memory, every
  host pauses. With hosts, the primary can be set to run no sessions
  itself.

## Decisions only you can make

1. **Is a host that pauses when its link drops acceptable?** Recommended:
   yes. It keeps one scheduler and one database, and it limits phase 1 to
   machines on one LAN or a direct tailnet path. The alternative, hosts
   that keep working alone, is three to four times the build.
2. **May a member's one agent login be served to another machine, or do
   people sign in per machine?** Recommended: served from the primary.
   People sign in once and no login is copied. The cost: a compromised
   host can take the real login of anyone whose session ran there, so
   members and workspaces can list the machines they allow.
3. **May a host connect through the hosted edge?** Recommended: no. The
   link needs LAN-grade speed, and the relay is capped and metered.
4. **Build phase 1 now, or after two weeks of phase 0's numbers and a
   two-machine spike?** Recommended: ship phase 0, run the spike, then
   decide.

## Where the evidence is

| File | What it holds |
| --- | --- |
| [`evidence/01-capacity-and-vm-runtime.md`](evidence/01-capacity-and-vm-runtime.md) | How one server decides it is full, sleeps, wakes and recovers sessions; pull requests #1153 to #1172 |
| [`evidence/02-agent-coordination.md`](evidence/02-agent-coordination.md) | Every way agents cooperate and where each assumes one machine |
| [`evidence/03-state-and-trust.md`](evidence/03-state-and-trust.md) | The database, git, homes and logins, the edge, updates, roles |
| [`evidence/04-other-systems-placement.md`](evidence/04-other-systems-placement.md) | Nomad, Kubernetes, Fly Machines, Firecracker, Cloud Hypervisor, E2B, Incus |
| [`evidence/05-job-queues.md`](evidence/05-job-queues.md) | BullMQ, River, asynq, NATS JetStream, Temporal |
| [`evidence/06-dev-environments-runners-orchestrators.md`](evidence/06-dev-environments-runners-orchestrators.md) | Coder, Gitpod, CI runner fleets, agent runners, Tailscale |
| [`evidence/07-seam-analysis.md`](evidence/07-seam-analysis.md) | Where the code can be cut between primary and host. It changed the plan's recommendation |
| [`reviews/`](reviews/) | The three attacks on the plan and what changed |

The collectors' rules are in
[`evidence/COLLECTOR-RULES.md`](evidence/COLLECTOR-RULES.md). Pages from
other systems were read through a fetch tool that summarises; quotes are as
returned.
