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
   disk. Agents on different machines talk as they do today, because the
   part that carries their messages does not move.
3. A new session or sub-session goes to an allowed machine with the most
   free memory. If none has room it waits in line; it is not refused.
4. A session stays on the machine it started on. A host that loses its
   link freezes its VMs until the link is back, and Cloudfield never
   starts a second copy of a session by itself.
5. No BullMQ, Redis or other queue product. One table in the database we
   already have is enough, and a queue's answer to a quiet worker is to
   run the work again, which a live session must never get.

## Phases

| Phase | What you get | Size (my estimate) |
| --- | --- | --- |
| 0. One machine that waits and explains | A full server queues launches and sub-sessions instead of dropping them, and shows why. Every refusal is counted | 6 to 8 pull requests |
| Spike | Two real machines, scripts only. Pass or fail on seven questions | No product code |
| 1. A second machine takes session VMs | Add a machine with one command. Sessions start there and work with the others as today. A dead machine can be declared lost and its sessions forked | About 20 pull requests |
| 2. Operating several machines | Draining, updates, key replacement | About 5 |
| 3. Move a sleeping session | Only if phase 0's numbers show it is needed | Not sized |

Phase 0 is worth shipping even if phase 1 never happens. It fixes the part
of 2026-11-26 that a second machine would not have fixed: launches that
vanish and no view of why.

## What a second machine does not fix, and what it costs

- Sessions asleep on a full machine stay there. Their disks and saved
  memory are on that machine.
- The primary is still one machine. When it restarts or updates, every
  VM on every host pauses until it is back.
- A host is trusted with everything inside the sessions placed on it. So
  a new host is allowed nothing until a super admin lists workspaces for
  it and each member turns it on for themselves.

## Decisions only you can make

1. **Is it acceptable that work on a host pauses when its link drops and
   whenever the primary restarts?** Recommended: yes, if the spike passes.
   It keeps one scheduler and one database, and limits phase 1 to machines
   on one LAN or a direct tailnet path. If not, the design to examine is a
   host that supervises its own sessions, which is a larger build.
2. **One home served from the primary, or a sign-in per machine?**
   Recommended: one home, for machines you trust as much as the primary,
   if it passes its tests. People sign in once and no login is copied. The
   cost: a compromised host can take the real logins of everyone who
   allowed it. A home per machine limits that, costs a sign-in per
   machine, and is the fallback.
3. **May a host connect through the hosted edge?** Recommended: no. The
   link needs LAN-grade speed, and the relay is capped and metered.
4. **Phase 0 now, then the spike, then decide on phase 1?** Recommended:
   yes.

## How the plan was attacked

Three reviewers with fresh contexts: Opus as a distributed-systems
engineer (16 findings), Opus as a security engineer (22), and `omp` on
`openai-codex/gpt-6.2-astra` (22). The first complete draft did not
survive them: the memory brake would have undone the link freeze, a
primary restart would have left remote sessions unattached, a session on
a tailnet host could have signed in to the primary as its owner, and "can
never run twice" claimed more than silence can prove. The plan's Review
section lists each change and the five points rejected with reasons.

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
