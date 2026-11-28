# 04 Other systems: placement, join, failure, double-run, moves

Collected 2026-11-27 from pages fetched in this run. Rules:
[COLLECTOR-RULES.md](COLLECTOR-RULES.md). Each bullet ends with the URL read.
**Design** = what the authors say the system does. **Measured** = a number
the authors publish. Where a page said nothing, the bullet starts
**Not established:**. Pages marked "main" are the current docs/source
branch, which can be newer than the release a user runs. Several fetches
returned a model-made excerpt of the page rather than the raw text; quotes
are as returned and short.

## A. HashiCorp Nomad

1. Source of truth
   - **Design** Servers form one Raft group per region; "run 3 or 5 Nomad servers per region"; quorum is a majority, so 3 servers tolerate 1 failure, 5 tolerate 2; "If quorum is lost, the cluster cannot commit new log entries." https://developer.hashicorp.com/nomad/docs/architecture/cluster/consensus
   - **Design** Committed log entries update cluster state held in MemDB on each server; reads default to the leader, `stale` lets any server answer. https://developer.hashicorp.com/nomad/docs/architecture/cluster/consensus
   - **Design** `bootstrap_expect` is required; "A value of `1` does not provide any fault tolerance and is not recommended for production use cases." https://developer.hashicorp.com/nomad/docs/configuration/server
2. Join
   - **Design** The client lists servers (`servers`, or `server_join` with `retry_join`); the page says that list is "used to register the client with the server nodes." The client page says nothing on which side opens connections. https://developer.hashicorp.com/nomad/docs/configuration/client
   - **Design** Source: the client dials a server for unary and streaming RPC (`net.DialTimeout("tcp", server.Addr...)`), and also accepts server-opened streams "on pre-existing connection[s]" the client made; no NAT or reverse-connection comment in the file. https://raw.githubusercontent.com/hashicorp/nomad/main/client/rpc.go
   - **Not established:** an official statement that a client behind NAT with no inbound port works. Looked in the client, architecture and security pages. The security page lists clients and servers sharing RPC on 4647/TCP and does not say which side initiates connections. https://developer.hashicorp.com/nomad/docs/architecture/security
   - **Not established:** a join token or per-node identity credential. Join is by address plus mTLS certificates (below); workload-identity JWTs found are per task, not per node. https://developer.hashicorp.com/nomad/docs/concepts/workload-identity (via search result, summary only)
3. Placement
   - **Design** Two phases: filter feasible nodes (datacenter, node pool, health, drivers, constraints), then rank; "Scoring is primarily based on bin packing", plus an automatic job anti-affinity that discourages two instances of a group on one node. https://developer.hashicorp.com/nomad/docs/concepts/scheduling/scheduling
   - **Design** A client can set aside resources from scheduling: `reserved` with `cpu`, `cores`, `memory`, `disk`, `reserved_ports`. https://developer.hashicorp.com/nomad/docs/configuration/client
   - **Not established:** whether ranking uses measured usage rather than allocated resources; the scheduling page does not say.
   - **Design** Nothing fits: the scheduler cannot place all allocations, so the evaluation is blocked and "re-evaluates them as cluster resources change"; metric `nomad.blocked_evals.total_blocked` = "The number of blocked evaluations." https://developer.hashicorp.com/nomad/docs/operations/monitoring-nomad
4. Dead or disconnected host
   - **Design** Defaults: `min_heartbeat_ttl` "10s", `heartbeat_grace` "10s" ("additional time given beyond the heartbeat TTL"), `max_heartbeats_per_second` 50.0, `failover_heartbeat_ttl` "5m" after a leader election. https://developer.hashicorp.com/nomad/docs/configuration/server
   - **Design** Source: the timer is armed with `ttl + HeartbeatGrace`; the TTL is rate-scaled with the number of nodes, so it grows past 10s on larger clusters; on expiry the leader sets the node `down` ("Node heartbeat missed"), or `disconnected` if an allocation has an open disconnect window. https://raw.githubusercontent.com/hashicorp/nomad/main/nomad/heartbeat.go
   - **Design** Default for a service group: `lost_after` is `""`, "equivalent to ... "0s" where allocations will immediately be marked lost"; `replace` defaults to `true`. With `lost_after` > 0, allocations go `unknown` instead of lost. https://developer.hashicorp.com/nomad/docs/job-specification/disconnect
   - **Design** "When a node is lost, Nomad replaces the allocations instead and ignores the reschedule block"; service reschedule defaults `delay` "30s", `unlimited` true. https://developer.hashicorp.com/nomad/docs/job-specification/reschedule
5. Partition without death
   - **Design** `stop_on_client_after`: "a disconnected Nomad client will stop its allocations" after the duration; it needs the client process running and cannot be combined with `lost_after`. https://developer.hashicorp.com/nomad/docs/job-specification/disconnect
   - **Design** Older field `stop_after_client_disconnect` (deprecated in 1.8, removed in 1.10): "The replaced allocations will normally continue to run on the non-responsive client"; for exclusive resources you may want them stopped, but "the server may reschedule the group before the client has stopped the allocation." So the overlap is not closed. https://developer.hashicorp.com/nomad/docs/v1.9.x/job-specification/group ; removal: https://developer.hashicorp.com/nomad/docs/v1.9.x/upgrade/upgrade-specific (search result summary)
   - **Design** `max_client_disconnect`: "This can cause issues with stateful tasks or tasks with long restart times"; on reconnect, `reconcile` (default `best_score`) picks which of original and replacement to keep. https://developer.hashicorp.com/nomad/docs/v1.9.x/job-specification/group ; https://developer.hashicorp.com/nomad/docs/job-specification/disconnect
   - **Not established:** any lease or fencing token. None appears in the pages read.
6. Moving stateful work
   - **Design** No live migration or snapshot in the pages read. Host volumes: "The Nomad server ensures that the allocations are only scheduled on hosts that have a set of volumes." https://developer.hashicorp.com/nomad/docs/job-specification/volume
   - **Design** `sticky` (dynamic host volumes only): "Upon every reschedule and replacement, the task group always receives the volume with the same ID, if available." CSI uses `per_alloc`; most CSI plugins only support single-node access modes. https://developer.hashicorp.com/nomad/docs/job-specification/volume
7. Trust
   - **Design** mTLS "optionally" on all HTTP and RPC, one private CA; certificates carry role and region (`client.global.nomad`), not hostname; with `verify_server_hostname` false "its role and region will not be verified", so any CA-signed service could act as client or server in any region. https://developer.hashicorp.com/nomad/docs/secure/traffic/tls
   - **Design** "Nomad is not secure-by-default"; allocation data on a client is unencrypted ("any secrets in any of the allocation's file systems"); attackers with client memory can extract Vault tokens; client memory/config access is outside the threat model. https://developer.hashicorp.com/nomad/docs/architecture/security
8. Numbers
   - **Measured** Vendor benchmark: "2 million Docker containers in 22 minutes across 10 AWS regions", "nearly 1,500 containers per second", "3 Nomad schedulers", "more than 6,000 Nomad clients". https://www.hashicorp.com/blog/hashicorp-nomad-meets-the-2-million-container-challenge
   - **Not established:** failover time or placement latency for a small cluster.

## B. Kubernetes, Kata, KubeVirt

1. Source of truth
   - **Design** etcd: "Consistent and highly-available key value store for all API server data." Copies/consensus count not stated on this page. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/overview/components.md
2. Join
   - **Design** Kubelet registers itself (`--register-node` default true) and "kubelet connects to the API server, authenticates using the token"; the kubelet gets a bootstrap token and bootstrap kubeconfig, creates a CSR (`kubernetes.io/kube-apiserver-client-kubelet`), kube-controller-manager can auto-approve, certs rotate. The node dials out for this flow. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/access-authn-authz/kubelet-tls-bootstrapping.md ; https://kubernetes.io/docs/concepts/architecture/nodes/
   - **Design** The kubelet also "exposes an https endpoint for certain features" (serving cert is separate and not auto-approved by core controllers). https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/access-authn-authz/kubelet-tls-bootstrapping.md
   - **Not established:** a statement that a node behind NAT with no inbound port is supported (logs/exec need the API server to reach the kubelet endpoint above; not confirmed in pages read).
3. Placement
   - **Design** Filter then score; "If there is more than one node with equal scores, kube-scheduler selects one of these at random"; the resource filter checks "available resources to meet a Pod's specific resource requests". https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/scheduling-eviction/kube-scheduler.md
   - **Design** Nothing fits: "the pod remains unscheduled until the scheduler is able to place it." https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/scheduling-eviction/kube-scheduler.md
   - **Not established:** bin-pack versus spread default and measured-versus-requested signal; the page did not cover them.
4. Dead host
   - **Design** Heartbeats are Node `.status` updates plus a Lease per node in `kube-node-lease`; the control plane uses `spec.renewTime` to judge availability. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/architecture/leases.md
   - **Design** Defaults: kubelet `nodeStatusUpdateFrequency` "10s", `nodeLeaseDurationSeconds` 40; controller `--node-monitor-period` 5s, `--node-monitor-grace-period` 50s. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/config-api/kubelet-config.v1beta1.md ; https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/command-line-tools-reference/kube-controller-manager.md
   - **Design** The node's `Ready` becomes `Unknown`; "By default, the node controller waits 5 minutes" before eviction. The unreachable and not-ready taints are `NoExecute`; automatic tolerations keep pods bound "for 5 minutes" (`tolerationSeconds=300`, the `DefaultTolerationSeconds` default is 5 minutes). https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/architecture/nodes.md ; https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/scheduling-eviction/taint-and-toleration.md ; https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/access-authn-authz/admission-controllers.md
   - **Design** Eviction rate: `--node-eviction-rate` 0.1/s; if the unhealthy fraction is at least `--unhealthy-zone-threshold` (0.55) and the cluster has 50 nodes or fewer, "then evictions are stopped." https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/architecture/nodes.md
5. Partition without death
   - **Design** "A Pod is not deleted automatically when a node is unreachable"; it goes `Terminating`/`Unknown` and leaves the apiserver only if the Node object is deleted, the kubelet returns and removes it, or a user force-deletes. https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/
   - **Design** "StatefulSet ensures that, at any time, there is at most one Pod with a given identity"; force delete "can lead to the duplication of a still-running Pod", "you are asserting that the Pod in question will never again make contact". https://kubernetes.io/docs/tasks/run-application/force-delete-stateful-set-pod/
   - **Design** Non-graceful shutdown: an operator adds taint `node.kubernetes.io/out-of-service` after confirming the node is off; pods are force-deleted and volume detach happens immediately. If pod deletion fails for 6 minutes, "kubernetes will force detach volumes" (risk of corruption; `disable-force-detach-on-timeout` turns it off). https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/concepts/cluster-administration/node-shutdown.md
6. Moving stateful work
   - **Design** `local` PVs: "You must set a PersistentVolume `nodeAffinity`"; the scheduler uses it to place pods on the node; "subject to the availability of the underlying node"; "potential data loss". https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/storage/volume-types.md
   - **Design** KubeVirt live migration: PVC-backed VMs "must have a shared ReadWriteMany (RWX) access mode to be live migrated" unless the volume is also migrated; bridge pod-network binding not allowed; ports 49152, 49153 must be free. https://kubevirt.io/user-guide/compute/live_migration/
   - **Design** KubeVirt CPU: `host-model` is the default; `host-passthrough` "is only checked for the CPU vendor", use it "only when the source and target nodes have identical CPUs"; `LiveMigratable` "does not verify that another node satisfies the VMI's CPU model and feature requirements". https://kubevirt.io/user-guide/compute/virtual_hardware/
   - **Design** KubeVirt run strategy `RerunOnFailure` restarts after an uncontrolled stop; the page does not say if a crashed node's VM goes elsewhere. https://kubevirt.io/user-guide/compute/run_strategies/
   - **Design** Kata: lists Cloud Hypervisor, Firecracker, QEMU, Dragonball; only QEMU has GPU/TDX/SEV-SNP; page is "not prescriptive". https://github.com/kata-containers/kata-containers/blob/main/docs/hypervisors.md
   - **Design** Kata limitation page: "The runtime does not provide `checkpoint` and `restore` commands." The page is undated; may predate current code. https://github.com/kata-containers/kata-containers/blob/main/docs/Limitations.md
7. Trust
   - **Design** Node authorizer: kubelet must be in `system:nodes` as `system:node:<nodeName>`; it can read "secrets, configmaps, persistent volume claims and persistent volumes related to pods bound to the kubelet's node" and only pods bound to it. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/access-authn-authz/node.md
   - **Design** NodeRestriction: a kubelet "will only be allowed to modify their own `Node` API object" and cannot update or remove its taints. https://raw.githubusercontent.com/kubernetes/website/main/content/en/docs/reference/access-authn-authz/admission-controllers.md
8. Numbers
   - **Measured** KubeVirt defaults, not measurements: bandwidth per migration unlimited, `completionTimeoutPerGiB` 150s, `progressTimeout` 150s, 5 parallel migrations cluster-wide, 2 outbound per node. https://kubevirt.io/user-guide/compute/live_migration/
   - **Not established:** published failover or pod-start latency (not searched).

## C. Fly.io Machines

1. Source of truth
   - **Design** "Every worker is its own source of truth": each `flyd` keeps a BoltDB "append-only log of all the operations applied to the worker"; nothing cached. https://fly.io/blog/carving-the-scheduler-out-of-our-orchestrator/
   - **Design** Global view is Corrosion: a Rust program that "propagates a SQLite database with a gossip protocol", CRDT-based, "no locking, no central servers, and no distributed consensus"; they say Raft/Consul broke down over long distances. https://fly.io/blog/corrosion/
2. Join
   - **Design** "Our whole fleet is meshed with WireGuard; everything can talk directly to everything else." Join procedure and credentials: **Not established** (not in pages read). https://fly.io/blog/machine-migrations/
3. Placement
   - **Design** `flaps` collects capacity from every `flyd` in a region and runs a best-fit ranking to reserve space; orders are immediate-or-cancel, nothing queued. They say "Bin packing is wrong for platforms like Fly.io" (headroom per region). https://fly.io/blog/carving-the-scheduler-out-of-our-orchestrator/
   - **Design** Docs: "Placement can fail!", "we can run out of capacity in particular regions", retry is the caller's job. https://docs.fly.io/machines/overview
4. Dead host
   - **Design** "Volumes are pinned to physical hosts, so when there's a host outage the volume is unreachable." Recovery for one Machine is manual: list snapshots, create a volume from a snapshot, scale to one Machine; snapshots are every 24 hours. https://docs.fly.io/apps/trouble-host-unavailable/
   - **Design** Multi-node Postgres clusters fail over to a healthy node; single Machine has no failover. https://docs.fly.io/apps/trouble-host-unavailable/
   - **Design** Volume docs: daily snapshots kept 1 to 60 days (5 default); "Fly.io does not automatically replicate data among the volumes"; with one Machine and volume "you'll have downtime if there's a host or network failure." https://docs.fly.io/volumes/overview
5. Partition without death
   - **Design** "There's a one-to-one mapping between Machines and volumes." During an outage a deploy can leave an older Machine that "returns with the host, so you may need to destroy it". Fencing mechanism: **Not established**. https://docs.fly.io/volumes/overview ; https://docs.fly.io/apps/trouble-host-unavailable/
6. Moving stateful work
   - **Design** "a Fly App with an attached Volume is anchored to a particular worker physical"; migration is kill, `clone` (dm-clone over iSCSI, reads of uninitialised blocks pull from the original, "hydration"), boot on the new host; "you have to `kill` first, then `copy`, then `boot`" because the original is still writing. https://fly.io/blog/machine-migrations/
   - **Design** Suspend: "Suspend uses Firecracker snapshots to capture the entire VM state"; "not currently recommended for large machine memory sizes (> 2 GB)"; needs no swap, no schedule; "Snapshots aren't guaranteed to persist" (cold start after deploy, host migration, loss, maintenance); "On resume, the machine thinks its network connections are still live"; snapshot "isn't stored on a volume". https://docs.fly.io/reference/suspend-resume
   - **Design** Auto stop/start: `auto_stop_machines` is `off`, `stop` or `suspend`; proxy checks "every few minutes", stops at most one Machine per region per cycle; "never creates or destroys Machines". https://docs.fly.io/reference/fly-proxy-autostop-autostart
7. Trust
   - **Not established:** node-to-control-plane authentication and what a compromised host can read; not in pages read.
8. Numbers
   - **Measured** "At our 2020 scale, we could drain a fully loaded worker in just a handful of minutes" (stateless). No downtime figure for volume migration. https://fly.io/blog/machine-migrations/
   - **Measured** Corrosion "converges quickly (in seconds)"; "thousands of workers"; no latency figures. https://fly.io/blog/corrosion/
   - **Measured** Stop to start of a stopped Machine "well under a second"; suspend resume "typically a few hundred milliseconds". https://docs.fly.io/machines/overview ; https://docs.fly.io/reference/suspend-resume

## D. Firecracker and Cloud Hypervisor

Not schedulers. Questions 1 to 4 do not apply; 5 to 8 below.

1. Firecracker snapshot
   - **Design** A snapshot is a memory file, a microVM state file and "zero or more disk files"; disk files "have to be handled by the user". https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/snapshot-support.md
   - **Design** "Snapshots must be resumed on an software and hardware configuration which is identical to what they were generated on"; newer host kernels only in "limited cases" without guarantees; each binary supports one snapshot format version. https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/snapshot-support.md
   - **Design** Restoring more than once: "resuming execution from the same state more than once insecure" without a uniqueness mechanism; VMGenID makes guests >= 5.18 re-seed the PRNG, other state "still be replicated across multiple microVMs". https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/snapshot-support.md ; https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/random-for-clones.md
   - **Design** Network: "Guest network connectivity is not guaranteed to be preserved after resume"; clones resume "with the same IP address(es)", so each needs its own network namespace plus iptables NAT; "we don't claim this is a performant or secure setup." Open vsock connections are closed at snapshot; vhost-user block devices do not support snapshots. https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/network-for-clones.md ; https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/docs/snapshotting/snapshot-support.md
   - **Measured** Spec: boot "<= 125 ms" to guest init; VMM overhead "<= 5 MiB". No snapshot resume number on that page. https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/SPECIFICATION.md
2. Cloud Hypervisor snapshot
   - **Design** Pause, then snapshot to a directory (`config.json`, `memory-ranges`, `state.json`); restore comes up paused. Restore host/binary compatibility: **Not established** on that page. For `copyonwrite`, "The snapshot memory file must remain on disk **and unchanged** for the entire lifetime of the VM." https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/snapshot_restore.md
3. Cloud Hypervisor live migration
   - **Design** Unix socket for same-host; TCP/TLS "intended for migration from one machine (`src`) to another"; TCP uses mutual TLS; sender `ch-remote send-migration`, receiver `receive-migration` first. https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/live_migration.md
   - **Design** Kernel, initramfs and disk images "must be available at the same paths on both machines"; the page does not use the words shared storage. https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/live_migration.md
   - **Design** Versions: "Starting with `v54`, Cloud Hypervisor guarantees migration compatibility from the previous two versions"; backward migration unsupported. CPU: "reliable usage in a heterogeneous fleet need the use of a matching CPU profile", available since v55. https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/live_migration.md
   - **Design** `postcopy` is not supported with VFIO, vDPA, vfio-user, prefaulting or multiple connections; `downtime_ms` default 300. virtio-fs: **Not established** (neither live_migration.md nor fs.md mentions migration or snapshot for it; fs.md says `--fs` needs `--memory shared=on`). https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/live_migration.md ; https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/fs.md
   - **Design** CPU profiles: `host` is the default; "The caller must ensure that the resulting CPU description is consistent and supported by every host on which the VM will run." https://raw.githubusercontent.com/cloud-hypervisor/cloud-hypervisor/main/docs/cpu.md
   - **Measured** None published ("downtime_ms=200" is an example value only).

## E. E2B, Daytona, Modal

E2B (infra repo and docs)
1. Source of truth
   - **Design** Redis is "the source of truth for running sandboxes"; templates, builds, snapshots and teams live in PostgreSQL; the live node map is "discovered via Nomad, Kubernetes, or a static list". https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
2. Join and direction
   - **Design** A per-node orchestrator "drives Firecracker"; the API calls it over gRPC (`SandboxService.Create`) on port 5008; the BYOC control plane reaches orchestrators through an edge-controller "gRPC proxy"; VPC peering is optional. The control plane dials the node. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md ; https://docs.e2b.dev/byoc
   - **Not established:** node join credential and NAT behaviour.
3. Placement
   - **Design** "best-of-K": "sample K ready nodes, score by the higher of CPU load and hugepage-pool load, pick the lowest"; retries on exhausted nodes; a node that answers `ResourceExhausted` "stays eligible" with jittered backoff; K and overcommit tunable by feature flag. Measured load, not reserved. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
   - **Design** `Draining` and `ShuttingDown` exclude new placement while existing work stays reachable. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
4. Dead host
   - **Not established:** "The document does not describe recovery of sandboxes on a node that dies unexpectedly." Read the first 100,000 of 119,279 characters only. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
5. Double run
   - **Not established:** no lease or fencing in the text read. The pause transition holds a key with TTL 95 s and snapshot DB upsert plus node RPC share "a detached 80-second budget". https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
6. Moving
   - **Design** Pause diffs memory (dirty-page tracking) and rootfs (COW) against the template, caches locally, "uploads asynchronously to object storage"; resume reads no object storage if the snapshot is still in the local cache; memory loads lazily via userfaultfd. Resume can land elsewhere via object storage; which node is picked: **Not established**. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md
   - **Design** Network connections do not survive a pause: "you need to connect clients again." Paused sandbox "is kept indefinitely". https://docs.e2b.dev/sandbox/persistence
8. Numbers
   - **Measured** (docs claim) pause "about 4 seconds per 1 GiB of RAM", resume "about 1 second"; running limit 24 h Pro, 1 h Hobby. https://docs.e2b.dev/sandbox/persistence
   - **Measured** (budgets, not results) resume flight ceiling 90 s, auto-pause overstay 120 s. https://raw.githubusercontent.com/e2b-dev/infra/main/docs/ARCHITECTURE.md

Daytona
   - **Design** Sandbox manager (inside the API process) "schedules sandboxes onto runners, reconciles states, and enforces sandbox lifecycle management policies." Runners initiate: "Each runner polls the control plane API for jobs", so no inbound port to the runner is implied. https://www.daytona.io/docs/en/architecture/
   - **Design** Snapshots and volumes live in S3-compatible object storage; metadata in PostgreSQL. https://www.daytona.io/docs/en/architecture/
   - **Not established:** scheduling signals, heartbeat, runner-failure behaviour, join token (runners page returned 404; architecture page silent).

Modal
   - **Design** Memory snapshots "can only be restored on the same exact instance type", "which can cause scheduling delays"; taking one terminates the sandbox; expire after 7 days; no GPUs; alpha. https://modal.com/docs/guide/sandbox-memory-snapshots
   - **Design** gVisor checkpoint/restore; snapshots incompatible across CPU feature sets, sensitive to driver and runtime versions; live network connections not preserved; falls back to a normal start if restore fails. https://modal.com/blog/mem-snapshots
   - **Measured** "import torch" cold start about 5 s versus restore about 1.05 s p50 (0.69 s p0); Stable Diffusion function 13 s to 3.5 s; "about 2.5x faster". https://modal.com/blog/mem-snapshots
   - **Not established:** Modal worker placement, join, failure.

## F. Incus (LXD fork) clustering

1. Source of truth
   - **Design** "a Cowsql database replicated using the Raft algorithm"; default 3 voters and 2 stand-bys; a stand-by is promoted when a voter goes offline; available "as long as a majority of voters is online". https://linuxcontainers.org/incus/docs/main/explanation/clustering/ ; https://raw.githubusercontent.com/lxc/incus/main/doc/explanation/clustering.md
2. Join
   - **Design** `incus cluster add <name>` on an existing member makes a join token; `incus admin init` on the new server; the token holds "the fingerprint of the cluster certificate" and a single-use secret; expiry set by `cluster.join_token_expiry`. Joining erases the new server's local data. https://linuxcontainers.org/incus/docs/main/howto/cluster_form/
   - **Design** "Make sure to use an IP or DNS address that other servers can reach"; member latency "must not exceed 10 ms" and "the heartbeats between members are not supported over higher latency links". Members connect to each other; NAT is not addressed. https://linuxcontainers.org/incus/docs/main/howto/cluster_form/ ; https://raw.githubusercontent.com/lxc/incus/main/doc/explanation/clustering.md
3. Placement
   - **Design** Default: "the cluster member that has the lowest number of instances" (count, not resources); `scheduler.instance` is `all`, `manual` or `group`; cluster groups limit targets (e.g. GPUs). https://linuxcontainers.org/incus/docs/main/explanation/clustering/ ; https://linuxcontainers.org/incus/docs/main/reference/cluster_member_config/
   - **Not established:** behaviour when no member has capacity.
4. Dead host
   - **Design** `cluster.offline_threshold` default 20 s ("must be greater than 10 seconds"). `cluster.healing_threshold` default 0 = off; non-zero = seconds after which an offline member is evacuated. https://linuxcontainers.org/incus/docs/main/server_config/ ; https://linuxcontainers.org/incus/docs/main/explanation/clustering/
   - **Design** Healing "only applies to instances on shared storage and which don't use any local devices"; up to 5 attempts per run; a returning server must be restored manually. A permanently offline member is force-removed, after which the server "must be fully reinstalled". https://linuxcontainers.org/incus/docs/main/howto/cluster_manage/
5. Double run
   - **Not established:** a fencing mechanism; the pages say healing runs after the leader marks a member offline and the returned server "must manually restore" (no statement on stopping its old instances).
6. Moving
   - **Design** Containers must be stopped to move ("In most real-world scenarios, you should stop the container"). VMs: "you must either enable Live migration for virtual machines or stop it first"; live needs `migration.stateful` true. https://linuxcontainers.org/incus/docs/main/howto/move_instances/
   - **Design** Live migration needs "the CPU of the target server must have at least the same capabilities as the source CPU"; homogeneous clusters can set `instances.vm.cpu.x86_64.baseline=host` on a group; "creating a cluster group per platform is recommended". https://raw.githubusercontent.com/lxc/incus/main/doc/explanation/clustering.md
   - **Design** `cluster.evacuate` (default `auto`): migrate live only for VMs with `migration.stateful` and all devices migratable, else stop. Shared storage requirement for live migration: **Not established**. https://linuxcontainers.org/incus/docs/main/reference/instance_options/
   - **Design** Evacuate: "migrates all instances on the given server"; `incus cluster restore` moves them back. https://linuxcontainers.org/incus/docs/main/howto/cluster_manage/
7. Trust
   - **Not established:** what a joined member can do (member certificates/trust store); security page does not cover clustering.
8. Numbers
   - **Measured** None found; only thresholds above.

## Comparison

| System | Source of truth | Join direction and NAT | Nothing-fits behaviour | Dead-host default | Anti-double-run mechanism | Moves running stateful work? |
|---|---|---|---|---|---|---|
| Nomad | Raft on 3 or 5 servers per region | Client lists servers and registers; source shows client dials; NAT not documented | Blocked evaluation, retried as resources change | Down after TTL (min 10 s) + grace 10 s; allocations lost and replaced (`lost_after` 0s, `replace` true) | Optional `stop_on_client_after`; docs admit server may reschedule first; `reconcile` on return | No live move; sticky/host volume pins to host |
| Kubernetes | etcd behind the API server | Kubelet dials API server with bootstrap token, CSR; kubelet also serves https | Pod stays unscheduled | Unknown after 50 s grace; evicted after 300 s; StatefulSet pods stay Terminating | StatefulSet at-most-one; no auto force-delete; manual `out-of-service` taint | Local PV pins to node; KubeVirt live migration needs RWX storage and compatible CPU |
| Fly Machines | Per-host BoltDB (flyd) plus gossip SQLite (Corrosion), no consensus | Hosts on a WireGuard mesh; join steps not established | Immediate-or-cancel, request fails | Volume host unreachable; manual recovery from snapshot | Not established; old Machine may return with host and must be destroyed by user | Stop, block-clone volume, boot elsewhere; suspend is per Machine, <= 2 GB advised, may cold start |
| Firecracker / Cloud Hypervisor | n/a (VMM) | CH TCP migration: mTLS, receiver listens | n/a | n/a | Firecracker: restoring one snapshot twice is "insecure" without VMGenID/reseed; clones share IPs | FC: identical hardware and software; CH: remote live migration, same paths, matching CPU profile and version rules |
| E2B | Redis (running), PostgreSQL (persistent), node map from Nomad/K8s/static | API dials orchestrator over gRPC; join and NAT not established | Retry other nodes, backoff | Not established | Not established | Pause to object storage and resume (about 4 s/GiB pause, 1 s resume claimed); connections lost |
| Daytona | PostgreSQL metadata, S3 snapshots | Runners poll the control plane | Not established | Not established | Not established | Not established |
| Modal | Not established | Not established | Not established | Not established | Not established | Memory snapshot restores only on the same instance type; terminates source |
| Incus | Cowsql (Raft), 3 voters 2 stand-bys | Token from member; members reach each other, <= 10 ms latency | Not established | Offline at 20 s; healing off (0) by default | Not established | VM live migration with `migration.stateful`, CPU baseline; containers stop first |

## Not established

- Nomad: official statement on clients behind NAT; per-node identity or join token; whether ranking uses measured usage; failover time on a small cluster.
- Kubernetes: etcd replica and consensus detail in pages read; NAT'd kubelet support; bin-pack versus spread default; measured failover or pod-start numbers; whether KubeVirt reschedules a VM off a crashed node.
- Kata: current checkpoint/restore status (page may be old); Kata live migration.
- Fly: how a host joins; credentials and compromise blast radius; fencing between a returning host and a replacement; any volume-migration downtime figure.
- Cloud Hypervisor: restore requirements on host and binary version in `snapshot_restore.md`; virtio-fs migration and snapshot support; any measured downtime. Docs read are `main`.
- E2B: node-death recovery, join credential, fencing, which node a resume picks (only the last 19 KB of ARCHITECTURE.md unread).
- Daytona: scheduling signals, heartbeat, runner failure, registration (runners page 404).
- Modal: placement, join, failure handling.
- Incus: capacity-exhausted behaviour, fencing for healed instances, member trust model, shared-storage requirement for live migration, any measured numbers.
