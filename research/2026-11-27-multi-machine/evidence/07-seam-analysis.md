# 07. Where to cut: a thin remote runtime or a fat second server

Collector rules: [COLLECTOR-RULES.md](COLLECTOR-RULES.md). Code read at `275b84856` on this branch. Nothing was built or run; every count is a `grep` over non-test `.go` files and is approximate where it says so. **Design** is what the code says. **Assessment** is this collector's judgement from the cited code, not a fact about the code. Files 01, 02 and 03 were spot-checked: the citations used below were reread in the code; where a claim is taken from them without rereading, the bullet says so.

Names used below: **A** is the first machine (database of record, members, workspaces, mail, bare repositories, dashboard). **B** is the second machine (runs sessions).

## 0. Shape of the code at the seam

- **Design:** The server builds one runtime (`s.rt`), one git engine, one PTY host, one scheduler, one signing service and one store from one data directory, in one function. internal/server/server.go:314-511.
- **Design:** Background services get `Deps` with concrete types: `Runs *scheduler.Scheduler`, `Git *gitengine.Engine`, `PTY *ptyhost.Host`; only `Runtime` is an interface. internal/server/services.go:31-40.
- **Design:** The VM runtime is wired around `runtime.Runtime` in three places: the git engine gets the guest git service directly (`gitengine.VM{Disks: images, Guest: vms.Git}`), the VM runtime calls back into the git engine to build checkout disks (`s.git.VMCheckoutDisk`), and a second egress proxy is built on the concrete `*vm.Runtime`. internal/server/server.go:347-354, 455-456.
- **Design:** The omp auth broker manager is built from VM-package host helpers (`vm.SystemdUnits`, `vm.Unbind`, `vm.BindBeneath`). internal/server/server.go:359-365.
- **Design:** One `authMu` is shared by evidence, sshd and usage fallback; children take the same mutex from the sshd config. internal/server/server.go:512, 521, 563, 613; internal/server/svc_children.go:49.

## 1. Cut T: what crosses the link

### 1.1 Interfaces and their call sites

- **Design:** `runtime.Runtime` has 20 methods: Create, Start, Pause, Resume, Stop, Destroy, Attach, ContainerIP, ExecTTY, Exec, Wait, Inspect, FindByCreationKey, Commit, ImageLabels, ImageExists, ListImageTags, TagImage, ExportImage, RemoveImage. internal/runtime/runtime.go:447-510.
- **Design:** The scheduler calls it through `s.cfg.Runtime.<Method>(` at 137 sites: Inspect 20, FindByCreationKey 17, Exec 17, Stop 15, Destroy 10, Wait 9, Start 9, Pause 8, Resume 7, Attach 6, ExecTTY 4, RemoveImage 3, ListImageTags 3, ImageExists 2, ContainerIP 2, and one each of TagImage, ImageLabels, ExportImage, Create, Commit. (`grep -rhoE "cfg\.Runtime\.\w+\(" internal/scheduler`.)
- **Design:** The scheduler type-asserts the runtime to optional interfaces at 30 sites: `ManagedExecRuntime` 13, `FreezeRuntime` 6, `SaveRuntime` 3, `DiskUseRuntime` 2, `MemoryUseRuntime` 2, and one each of `CapacityRuntime`, `CaptureRuntime`, `InitPIDRuntime`, `SudoRuntime`. (`grep -rnoE "cfg\.Runtime\.\(runtime\.\w+\)" internal/scheduler`.)
- **Design:** Optional interfaces and their methods: `CapacityRuntime` 1, `InitPIDRuntime` 1, `MemoryEventsRuntime` 1, `MemoryUseRuntime` 1, `SaveRuntime` 5, `FreezeRuntime` 3, `CaptureRuntime` 1 (internal/runtime/runtime.go:54-128); `ManagedExecRuntime` 3 plus a `ManagedExec` handle of 7 methods (internal/runtime/managed_exec.go:23-33, 84-92); `BrowserRuntime` 2 (internal/runtime/browser.go:19-22); `DiskUseRuntime` 2 (internal/runtime/vm_disks.go:10-13); `SudoRuntime` 2 (internal/runtime/sudo.go:9-19). Total 29 methods beyond the 20.
- **Design:** Every `Attach`, `ExecTTY` and managed exec returns a `runtime.Attachment` (stdin, stdout, stderr, resize) that the PTY host or the ACP driver holds for the session's life. internal/runtime/runtime.go:423-443; internal/scheduler/seams.go:51-52; internal/scheduler/acp_driver.go:56-60.
- **Design:** Outside the scheduler the runtime is used by the browser manager (4 type references, 6 calls), publish to GitHub (`runrepo.New(d.Runtime.Exec)`), dictation, system stats, storage sizing and the root-owned directory remover. internal/browser/manager.go; internal/server/svc_runrepo.go:62; internal/server/svc_dictation.go:13; internal/server/svc_system.go:18; internal/server/storage.go:84, 109; internal/server/server.go:370.
- **Design:** sshd, gitengine, ptyhost and evidence make no direct `runtime.Runtime` calls; sshd reaches sessions through the scheduler and the PTY host, ptyhost only consumes `runtime.Attachment`. (grep count 0 for each; internal/scheduler/seams.go:51-65.)
- **Design:** The git engine reaches a VM session's checkout only through the guest git service: `e.cfg.VM.Guest(ctx, run)` in `vmGit`, used from vm.go, vm_commit.go, vm_files.go, vm_objects.go and vm_watch.go (14 call sites). internal/gitengine/vm_objects.go:308-312; internal/gitengine/vm.go:19-30.
- **Design:** The guest exposes three gRPC services: `Guest` 16 RPCs (process spawn, Read/Write streams, freeze, sync, clock, browser), `Git` 13 RPCs (Watch and Sync bidirectional streams, Objects, ReadFile and Install/WriteFile streams), `Home` 16 RPCs (a FUSE file service). internal/guest/guestpb/guest.proto:10-41; internal/guest/guestpb/git.proto:10-52; internal/guest/guestpb/home.proto:12-28.
- **Design:** Per VM the server process holds a helper client, a git client and a socket relay, each dialed once per VM lifetime; the dialer is a field (`dial func(vsockPath string) (helper, error)`). internal/runtime/vm/engine.go:79-108, 146-176.
- **Design:** The guest gRPC connection has no transport security (`insecure.NewCredentials()`) and dials a Unix socket that Cloud Hypervisor turns into vsock. internal/guest/client.go:146-170.
- **Design:** Process output is pulled by a blocking `Read` RPC per chunk of at most 64 KiB, at a byte offset the helper keeps, "so an attachment rides through a VM save and restore, and a new server can attach where an old one left". internal/guest/attach.go:13-15, 46-80; internal/guest/client.go:33; internal/guest/guestpb/guest.proto:16-18.
- **Design:** Sockets the session dials on the host are forwarded per VM: the helper relays a guest socket to vsock port N, Cloud Hypervisor connects to `<vsock socket>_<N>`, a link the server points at the real host socket (coordination, signing). The browser control socket and other served guest sockets go the other way through `relay`. internal/runtime/vm/share.go:22-60; internal/runtime/vm/relay.go:16-60; internal/guest/config.go:19-23, 33-36.
- **Design:** The member home reaches a VM through the `Home` file service, served by `aether-server vm-monitor` inside the VM's unit; "a failure of it ends the VM rather than leave it without its home". internal/runtime/vm/monitor_linux.go:28-33, 66-70.
- **Design:** Port forwards dial `ContainerAddr`, a host bridge or tap address. internal/sshd/forward.go:53, 68, 74; internal/scheduler/scheduler.go:989.
- **Design:** The egress proxy finds a session by its source IP through the runtime (`EgressCreationKey`), then reads `GetRun` and `GetWorkspace`, and rechecks the workspace every 10 s, closing the tunnel "once the list cannot be read". internal/egress/proxy.go:47, 228-252, 320-333.

### 1.2 Host reads that bypass every runtime interface

- **Design:** The memory brake reads host memory every 500 ms; the memory guard reads `/proc` meminfo and pressure directly (`sysstats.HostMemory(procRoot)`, `sysstats.HostMemoryStall(procRoot)`). internal/scheduler/vm_freeze.go:18; internal/scheduler/vm_memory.go:115, 128.
- **Design:** Admission reads the server's own cgroup `memory.pressure` through `/proc/<pid>/cgroup` of `os.Getpid()`. internal/scheduler/capacity.go:49-50, 357-359.
- **Design:** The OOM watch reads `/sys/fs/cgroup` and `/proc` for each container's init PID. internal/scheduler/oomwatch.go:41-42, 120-125.
- **Design:** Admission measures the data directory's filesystem; Docker's capacity probe refuses any daemon that is not a local `unix://` socket with a matching `engine-id`. internal/scheduler/capacity.go:274-307; internal/runtime/docker_disk.go:179-211.
- **Assessment:** Under Cut T these loops measure A while sessions consume B. The brake exists because fills of 1.4 GiB/s exhausted a host in about 10 s (docs/failure-handling.md:439-443, via 01 §2); a 500 ms loop on the far side of a network link is not the same control. The brake, guard, disk guard, OOM watch and capacity probe have to run on B, which makes B more than a runtime.

### 1.3 Host paths A reads or writes for one session

| Path (under the data dir unless absolute) | Who touches it | Under Cut T |
| --- | --- | --- |
| `checkouts/<run>/` (Docker clone; VM: `disk.qcow2` + run metadata), `checkouts/<run>.diffsnap/` | gitengine: internal/gitengine/checkout.go:125-183; internal/gitengine/snapshot.go:47-55; internal/gitengine/vm.go:112-129 | Docker: bind-mounted, so must be on B; VM disk must be on B (hard links to the image store, internal/vmimage/store.go:186-190); diffsnap can stay on A |
| `coord/<run>/` (socket, `co-authors`, harness assets, `next-command`, `captures/`) | coord: internal/coord/socket.go:85-124, 136-203; internal/scheduler/development_artifact.go:28-46 | stays on A; the socket needs a relay; files reach a VM through the home file service (internal/runtime/vm/share.go:105-125) |
| `sign/<member>/agent.sock`, `signing-keys/<member>` | internal/signing/agent.go:41-59; internal/signing/signing.go:63-79 | stays on A; socket relayed; each signature crosses the link |
| `runtime/bin/aether-server-<digest>` | internal/scheduler/coordination.go:385-433 (via 02 §1c) | Docker: bind mount, must exist on B; VM: helper disk from B's own binary |
| `homes/<member>/`, `home-caches/`, `logins/<member>/…`, `scheduler/account-config/<run>/` | memberhome: internal/server/server.go:406-414; internal/scheduler/scratch.go:18-19 | Docker: bind volume, must be on B (internal/runtime/docker.go:395-433 via 03 §4); VM: served by the monitor's file service, which could be tunnelled to A |
| `scheduler/<run>.json`, `scheduler/terminals/`, `scheduler/browser/`, `scheduler/vm-databases/<run>/` | internal/scheduler/state.go:591-596; internal/scheduler/vm_sessions.go:35 | sidecars stay on A; the VM state disk must be on B |
| `transcripts/<run>.cast`, `.screen`, `.retention`, `.items.jsonl` | ptyhost: internal/ptyhost/ptyhost.go:587-602 | stay on A |
| `evidence/` | internal/server/server.go:520 | stays on A |
| `vm/<run>/` (record, launch, sockets, disks, `snapshot/`), `vm/images/` | VM runtime: internal/runtime/vm/record.go:15-44 (via 01 §3) | must be on B |
| `/proc`, `/sys/fs/cgroup` | §1.2 | must be read on B |

- **Assessment:** For Docker sessions Cut T needs a shared filesystem for checkouts, homes, the coordination directory, the signing socket directory and the staged binary, because each is a bind mount by host path (internal/runtime/runtime.go:222-230, 270-277, 180-186; internal/runtime/browser.go:29). A shared filesystem also breaks the diff watch's fsnotify on the checkout (internal/gitengine/diffwatch.go:222-257, via 02 §6) and SQLite in the home. Docker sessions on B under Cut T are not credible.
- **Assessment:** For VM sessions every server-to-session path is already a gRPC call or a byte stream through a Unix socket; the host-only pieces are Cloud Hypervisor, systemd units, disks, taps, the firewall, DNS, egress, the monitor's file service and the memory loops.

### 1.4 What a link drop kills under Cut T

- **Design:** A runtime transport error never ends a session: `waitForExit` retries every error but `ErrNotFound`, and a failed wait logs "container wait inconclusive; retaining supervision". internal/scheduler/supervise.go:28-57, 70-86.
- **Design:** Detaching an `Attachment` never stops the container; a VM's helper keeps output by offset. internal/runtime/runtime.go:440-441; internal/guest/attach.go:13-15.
- **Design:** The ACP agent runs under a keeper that "outlives this server process"; the server speaks to it through a bridge exec. internal/scheduler/acp_driver.go:56-60.
- **Design:** The in-session coordination client retries repeatable calls for 60 s, then returns `CodeUnavailable`. internal/coordtransport/retry.go:14-32; internal/coordtransport/client.go:126-160.
- Lost while the link is down (from the cited design): live terminal and Enhanced views; prompts, steers and permission answers; git snapshots and branch publication; signing of commits; mail, reports, waits and memory after 60 s; port forwards; outbound connections of egress-controlled sessions within 10 s (internal/egress/proxy.go:47, 320-333); home file operations if the home is served from A.
- **Not established:** Whether the scheduler re-establishes one run's PTY attachment, ACP bridge or git `Watch` stream after a transient failure without a server restart or a wake. Looked at internal/scheduler/acp_end.go:18-60 (handles an agent that lost its conversation, not a lost bridge) and `reattach` in internal/scheduler/sleep_vm.go:207 (wake path only).

## 2. Cut F: dependencies of B's components on A

### 2.1 Store use by consumer

- **Design:** `store.Store` has 180 methods: 81 declared directly (with `Close`) plus 99 from 14 embedded interfaces; `*store.DB` has 377 exported methods. internal/store/store.go:44-246; (`grep -c` per embedded interface; `grep "^func (\w+ \*DB) [A-Z]"` over internal/store).
- **Design:** Consumers reach the other methods by type assertion: the scheduler to `*store.DB` (internal/scheduler/scheduler.go:789), `ReservedRunStore` (internal/scheduler/launch.go:882), `CoordTerminalReportStore` ×3, `EvidencePublicationStore` ×2, `EvidencePacketStore`, `RunWorktreeStore` (internal/scheduler/recovery.go:458; internal/scheduler/reported.go:276, 395; internal/scheduler/evidence.go:339, 525, 596; internal/scheduler/supervise.go:876); coord to `UnackedRunMessageIDsStore`, `CoordAuditStore`, `CoordOutboxCursorStore`, `CoordOutboxRetryStore` (internal/coord/hook_wait.go:69, 116; internal/coord/mailbox.go:1013, 1037, 1091, 1286); sshd to seven more (internal/sshd/attention.go:163; internal/sshd/connector.go:143; internal/sshd/run_wait.go:127; internal/sshd/audit.go:192, 242).
- **Design:** Several assertions are `ok`-guarded and silently skip a feature when the store lacks the method, for example the coordination audit outbox. internal/coord/mailbox.go:1013, 1286.
- Counts below match call names against the `*DB` method set, excluding `Close`; name collisions make them approximate.
  - **scheduler:** 53 distinct, about 216 sites, plus the three status writers taken as function values (internal/scheduler/state.go:343-350; internal/scheduler/reported.go:149-151). `GetRun` 89, `GetMember` 29, `GetWorkspace` 9, `GetTerminal` 9, `ListActiveRuns` 7, `ListMembers` 5.
  - **coord:** 38 distinct, about 61 sites; all mail, report, audit and outbox tables.
  - **children:** 23 distinct, about 68 sites (`run_children`, settings, limits, costs).
  - **watch:** 47 distinct, about 105 sites.
  - **memory:** 13 distinct, 18 sites.
  - **gitengine:** none; it uses the bus and the event log. internal/gitengine/engine.go:57-58.
  - **sshd:** about 113 distinct, about 303 sites.

### 2.2 Scheduler store calls, grouped

- **Launch:** `CreateRun`, `CreateRunWithID`, `SetRunCheckout`, `SetRunRepos`, `GetHarnessDefinition`, `ServerRunLimits`, `RunsWithExtraRepo`, `AccountSharedWith`, `AgentLoginOwner`, `ListAgentLogins`, plus `GetMember` ×2 and `GetWorkspace` at the start of `LaunchWithOptions`. internal/scheduler/launch.go:285-292, 399.
- **Per-session hot path, synchronous under the scheduler's global `s.mu`:** a transition calls `activityLocked`, which for a parked run reads `UnfinishedChildren` (subsession rows and unread mail), then writes `UpdateRunStatus` / `FinishRunReported` / `FinishRunByMember` and publishes `run.status`, all with `s.mu` held. internal/scheduler/state.go:295-297, 319, 343-350, 383-388; internal/scheduler/activity.go:170-176, 191-192, 233-236.
- **Design:** The status write returns a `RunStatusStamp` (`status_seq`, `outcome_unseen`, `finish_unopened`, review) that goes into the published event. internal/scheduler/state.go:350-358.
- **Design:** Every poll tick (default 30 s) `refreshActivities` repeats that derivation for every supervised run under `s.mu`; `runWaitLocked` also reads the `watches` rows through `WaitConditions.RunWaits`. internal/scheduler/activity.go:339-347, 325, 334; internal/scheduler/wait.go:175-180; internal/server/svc_watch_run.go:48.
- **Other per-session writes:** `UpdateRunCommit` on each branch publication, `SetRunTitle` on each terminal title, `AddRunSteerer` on a member's first input, `SetRunAgentSession`, `SetRunModel`, `RefuseRunModel`, `SetRunEffort`, `SetRunMode`, `SetRunAccount`. internal/scheduler/runmeta.go:14; internal/server/server.go:379-386, 393-398; internal/scheduler/coauthors.go:348.
- **Per-session reads:** `GetRun` (89 sites), `GetMember` for co-authors, steering, sudo and development actors, `ListRunSteerers`, `ListRunAccountSegments`, `LastCoordReportAt`, `ListRoomMessages`, coordination report rows and the evidence outbox. internal/scheduler/coauthors.go:197, 227; internal/scheduler/steer.go:672; internal/scheduler/sudo.go:162; internal/scheduler/development.go:242-246.
- **Sweeps over every run:** `ListActiveRuns` in recovery, the revoked-account check every poll, checkout and scratch sweeps. internal/scheduler/account_revoke.go:94-100; 01 §6 lists 20 such places.

### 2.3 Other dependencies of B-side components

- **Design:** Scheduler config: `Store`, `Runtime`, `Bus`, `WaitConditions`, `Git`, `PTY`, `Homes *memberhome.Manager`, `Logins *memberhome.Logins`, `OMPBrokers`, `Signing *signing.Service`, `Memory`, `Secrets *secretstore.Store`, `Egress`, `Control *control.Service`, `ReposDir`. internal/scheduler/scheduler.go:75-116.
- **Design:** The scheduler calls the member home manager at about 191 sites (locks, login mountpoints, caches, installations, credentials); sshd at about 61. `memberhome.Manager` has 60 exported methods and no interface. (grep over internal/scheduler, internal/sshd, internal/memberhome.)
- **Design:** The scheduler subscribes to the bus for usage limits. internal/scheduler/usage_fallback.go:466-468.
- **Design:** Event types the B-side packages publish: scheduler `run.status`, `run.retention`, `run.mode`, `timeline`, `run.usage_limit`, `run.model`, `run.input`, `run.finish_opened`, agent events, `run.title`, `run.sleep`, `run.review`, `run.outcome_seen`, `run.effort`, `run.deleted`, `run.cost`, `run.archived`, `run.account`, evidence packet and origin; gitengine `run.diff` and `git.branch`; the adapter manager `run.status`, agent events, cost and usage limits. ptyhost and acphost publish none. (`grep` of `*Payload` types per package; internal/events.)
- **Design:** The cost service records spend from `run.cost` events on the bus, not from the scheduler. internal/cost/service.go:132-141, 199-234.
- **Design:** The bus assigns `seq` under one mutex with the log write; Publish accepts a caller-supplied event ID and reconciles duplicates. internal/events/inproc.go:16-27, 44-53.
- **Design:** The scheduler logs and drops a failed publish: "the store, not the bus, is the source of truth". internal/scheduler/state.go:395-402.
- **Design:** A container checkout is `git clone --local` of the bare repository (hard links); a VM's prepared checkout disk is filled by `git clone --no-hardlinks` from the bare repository's local path. internal/gitengine/checkout.go:163 (via 02 §5); internal/gitengine/vm.go:118-129, 136-143.
- **Design:** A removed checkout's diff history borrows the workspace repository's objects through an `alternates` file. internal/gitengine/snapshot.go:607-650.
- **Design:** Evidence keeps refs under `refs/aether/evidence/` in the workspace repository. internal/gitengine/evidence.go:21.
- **Design:** The scheduler reads a member's signing key for server-made commits; sshd does too for file writes. internal/scheduler/coauthors.go (`cfg.Signing.Key`); internal/sshd/files.go (`cfg.Signing.Key`).
- **Design:** Workspace secrets are read at launch under a read lock and become container environment. internal/scheduler (`cfg.Secrets.RLock/Get/RUnlock`); docs/security.md:552-564 via 03 §4.

### 2.4 Classification

**(a) Needed only at launch; can travel in a launch spec:** member and account rows, workspace row (setup script, variables, egress policy), harness definition, pinned profile snapshot (`Profiles.Latest`, `PinRun`), server run limits, the share check, the chosen login, workspace secrets, the team-memory brief (`Memory.Brief`), the initial co-author list, the base commit and the objects to seed the checkout, the reserved run ID, the authorization and budget decisions. internal/scheduler/launch.go:285-418; internal/scheduler/coauthors.go:55-74 (via 02 §7); internal/cost/service.go:382-405.

**(b) Needed during the session, tolerant of delay; an ordered upstream (or downstream) stream:**
- Upstream: every event in §2.3, cost through `run.cost`, run-row fields B owns (status, activity, reason, title, commit, agent session, model, effort, mode, account, checkout), new steerers, branch publication into the bare repository, transcripts for logs and export after the session.
- Downstream: fields A owns that B's scheduler reads: `protected` (wake admission: internal/server/svc_coord_wake.go:48-49), owner after a handoff, archive, review; co-author refreshes (`RecordHandoff`, `RefreshMemberCoAuthors`); share revocations (`RevokeAccount`). internal/sshd/seams.go:100-102, 170-176.
- **Assessment:** None of the upstream writes is asynchronous today: each is a synchronous store call, the status write under `s.mu` (§2.2). Making them a stream means B owns `status_seq` for its runs or emits `run.status` only after A answers.

**(c) Needed synchronously; an RPC to A; what the session loses while the link is down:**
- Mail `coord.send/ask/reply/inbox` (one SQLite transaction with the inbox cap and idempotency index): no messaging. internal/store/coord.go:312-351 (via 02 §2).
- Terminal `coord.report`: reserve and finalize rows on A, evidence capture on B, refs into A's repository: the session cannot finish by report. internal/coord/mailbox.go:528-725 (via 02 §4).
- State reports with `--until`: no waits registered. internal/coord/report.go:124-267 (via 02 §4).
- `memory.*`: no team memory. internal/coord/memory.go:14-21.
- `child.start` (publishes the parent's branch into the bare repository), `child.cancel`, `child.fetch` (copies from the bare repository into the parent checkout): no subsessions. internal/children/agent.go:155, 344, 349.
- `coord.status` and `coord.hook.status`: peers, unread counts, overlaps from A's in-memory index. internal/coord/peers.go:119-238 (via 02 §6).
- Activity derivation and wait classification (`UnfinishedChildren`, `RunWaits`): the run's shown state stops updating or errors. internal/scheduler/activity.go:170-176; internal/scheduler/wait.go:175-180.
- Wake admission re-reads the run inside the control lock. internal/server/svc_coord_wake.go:31-61.
- Egress policy (`GetRun`, `GetWorkspace` per connection, recheck every 10 s): egress-controlled sessions lose outbound connections. internal/egress/proxy.go:228-252, 320-333.
- A usage-limit account move takes A's authorization mutex and reads quotas from homes. internal/server/server.go:611-624.
- Signing, if keys stay on A: commits in the session fail to sign.

### 2.5 What A reaches into a running session (would be proxied down the link)

- **SSH subsystems keyed by run:** `aether-attach` (control lease, `WakeRun`, `PTY.Attach`), `aether-acp` (`ACPSubscribe`), `aether-dev-browser` (`BrowserFrames`), `aether-dev-artifact` (`OpenArtifact`, `ReadImage`), `aether-sync` (`ServeVMSync`, `HoldRunAwake`), `aether-run-ssh` (`CheckRunExec`, `StartRunExec`), and `direct-tcpip` to `run:<id>` (`HoldRunAwake`, `ContainerAddr`). internal/sshd/session.go:96-131; internal/sshd/attach.go:238, 854; internal/sshd/acp_stream.go:126; internal/sshd/development_stream.go:100, 225, 330; internal/sshd/sync.go:213-237; internal/sshd/run_ssh.go:119, 151, 339; internal/sshd/forward.go:53-74.
- **`sshd.RunController`:** 65 methods; 37 take a run (kill, close, pause, resume, wake, sleep state, inject, ACP answer, cancel, set option, history, sudo decision, mode switch, relaunch, archive, review, handoff record, saved files), 9 resolve a launch against a member home (`DefaultMode`, `AgentModels`, `LoginProblem`, `FallbackLaunchable`, `CheckSharedLaunch`, `AgentSwitchable`, `WakeRefusal`, …), 18 act on a member's environment terminal or assistant, 1 is `HoldShell`. internal/sshd/seams.go:73-214.
- **Per-row decoration:** every run in a response reads 8 in-memory scheduler values (`Paused`, `Sleep`, `Cause`, `Wait`, `Liveness`, `PendingInputs`, `Switching`, `Retention`); `attention.list` reads 3 more. internal/sshd/handlers.go:414-440; internal/sshd/attention.go:123-126.
- **Run files and diffs:** `run.patch` and `files.tree/read/write/diff` on a run read the checkout or its snapshot store. internal/sshd/gateway.go:24-26; internal/sshd/files.go:19-31; internal/server/svc_gateway.go:10-11.
- **Other A services calling the scheduler for a run:** rooms (`Inject`: internal/server/svc_collaboration.go:28-31), publish to GitHub (`Runtime.Exec` in the session: internal/server/svc_runrepo.go:62), evidence capture over SSH (internal/sshd/handlers.go:804), watches (`PendingInputs`, `Paused`, `ACPPending`: internal/watch/service.go:60-65), children (`StopForParent`, `CloseRun`, `Review`, `Paused`, `LaunchWithOptions`: internal/children/service.go:22-32), storage gauge (`StorageRetention`, `CacheRetentions`: internal/server/storage.go:159-160, 241, 291), update idle check (`Busy`: internal/server/svc_serverupdate.go:26).
- **Mail wakes:** a message appended on A wakes the recipient through the scheduler (`Wakeable`, `Wake`, `InTurn`) and through waiters parked in A's coordination process. internal/coord/acp_wake.go:28; internal/server/svc_coord.go:58; 02 §3.

### 2.6 How the coordination front splits

- **Design:** The socket serves 48 methods: 9 coordination (`coord.status`, `coord.hook.status`, `coord.send`, `coord.ask`, `coord.reply`, `coord.report`, `coord.inbox`, `run.report`, `run.sudo`), 4 memory, 5 child, 30 development. internal/coord/socket.go:483, 523-588, 648-664; internal/coord/memory.go:14-21; internal/coord/children.go:104-111.
- **Store-only on A:** send, ask, reply, inbox, memory ×4, child list and options.
- **Scheduler-only on B:** `run.report` (`ReportAgentState`, `HoldTurnEnd`), `run.sudo`, all 30 `dev.*`. internal/coord/report.go:29-38; internal/coord/sudo.go:20; internal/scheduler/development.go:159-171 (via 02 §11).
- **Both:** `coord.report`, `coord.status`, `coord.hook.status`, `child.start`, `child.cancel`, `child.fetch`.
- **Design:** coord calls the scheduler through 15 methods (`ReportSink` 2, `OutcomeSink` 5, `SessionWaker` 3, `SudoRunner` 1, `Waits.Wait` 1, `DevelopmentService` 2, `RetainsContainer` 1); the scheduler calls coord through 8 (`Coordinator`: Provision, WriteCoAuthors, WriteFiles, Release, WakeIdle, EnhancedSessionOpened, ProvisionalReport, RemindReport). internal/coord/report.go:29-60; internal/coord/acp_wake.go:28; internal/coord/sudo.go:20; internal/coord/coord.go:95-113; internal/server/svc_coord.go:37-58; internal/scheduler/coordination.go:40.
- **Design:** One per-run `reportLock` covers both `coord.report` and `child.start`. internal/coord/coord.go:495-504; internal/coord/children.go:48 (via 02 §4).

## 3. Singletons and locks

| Singleton or lock | Where | Belongs on | Why |
| --- | --- | --- | --- |
| `authorizationMu` | internal/sshd/handlers.go:288; shared at internal/server/server.go:512-613, internal/server/svc_children.go:49 | A | Held from authorization until `Admitted`, after `checkLogin`, `reserveCapacity` and base capture (internal/scheduler/launch.go:316-418). Under either cut it is held across a call to B's capacity and B's view of the member home. |
| Event `seq` and log writer | internal/events/inproc.go:20-27, 44-53 | A | One gapless cursor; a second writer collides on the next publish (03 §1). B needs a forwarding `Bus`. |
| Watch cursor `watch_cursor id = 1` | internal/store/watch_notify.go:230, 242 | A | One evaluator per database (02 §4). |
| Overlap index | internal/overlap/index.go:63-79, 96-136 | A | Rebuilt by replay of `run.diff`; B's sessions read it through `coord.status`. |
| Coordination waiters, wake sets, rate buckets, `mailRefusals` | internal/coord/coord.go:205-234 (via 02 §1) | split | Inbox long-polls park where mail is appended (A); hook waiters and native wakes deliver into the agent (B). |
| Controller lease table | internal/server/server.go:441-444; internal/server/services.go:46-48 | open | "one process-wide controller lease table" used by attaches (A's sshd), rooms (A) and wake admission; input fencing happens at the PTY (B under Cut F). |
| `capacityGate`, `memoryClaims`, `capacityReservations` | internal/scheduler/scheduler.go:236-245; internal/scheduler/capacity.go:309-317 | per host (B for B's sessions) | Measures the host it runs on. |
| Memory brake, guard, disk guard, OOM watch, reclaim | internal/scheduler/scheduler.go:846-849, 897-941 | per host | Read local `/proc` and cgroups (§1.2). |
| Per-member home and login locks (`lockRunCaches`, `homeLocks`, `loginLocks`) | internal/scheduler/launch.go:493, 745; internal/scheduler/scheduler.go:287, 303 | wherever the home's files are | Serialize ownership walks and mounts in one home. |
| `scheduler.runs`, `acpDriver.runs`, PTY sessions | internal/scheduler/scheduler.go:270; internal/scheduler/acp_driver.go:61-70 | with the supervisor | In-memory supervision state; the sidecar is its durable half (internal/scheduler/state.go:413-456). |
| gitengine repository locks (`repoInitMu`, `fileWriteMu`, `repoMaintenanceMu`) vs snapshot locks (`snapshotLocks`, `alternatesMu`, `vmReceiveMu`) | internal/gitengine/engine.go:114-148 | repository locks with the bare repos (A); snapshot locks with the checkouts | All in-process; no file lock. |
| Children reconcile loop | internal/children/service.go:88-96, 109-160 (via 02 §5) | A | Reads `run_children`; launches through `Runs`. |
| Egress proxy, DNS relay, VM network index | internal/server/server.go:450-459; internal/runtime/vm/engine.go:101-104 | per host | Bound to the host's tap network. |
| Signing agent listeners | internal/signing/signing.go:30-39; internal/server/server.go:641-645 | where sessions mount them, unless relayed | One listener per member per process. |
| Edge host key and registration | 03 §8 | A only | A second host enrolling with the same key replaces the first at the relay. |

## 4. Size

### Cut T (both runtimes)

- **Packages touched:** runtime, runtime/vm, guest (dialer), scheduler (capacity, memory loops, OOM watch, reclaim, sweeps, mounts), gitengine (VM checkout disk), memberhome, signing, coord (socket relay), browser, egress, sshd (forwards), server.
- **New protocol surface:** 20 `Runtime` methods, 29 optional methods, 4 stream kinds (attachment, managed exec, image export, guest gRPC tunnel), 4 socket relays (coordination, signing, omp broker, browser control), a TCP tunnel for forwards, and remote host readings for the memory loops. About 50 RPCs and 8 stream kinds, plus a shared filesystem for Docker sessions.
- **Assessment:** The Docker half is not worth estimating further (§1.3).

### Cut T′: thin host for session VMs only (proposed third cut)

The code suggests a narrower cut: below `vm.Runtime`, not below `runtime.Runtime`.

- **Design reasons:** the guest protocol is versioned (`guest.Protocol`, `ErrIncompatible`: internal/guest/client.go:25-26 via 01 §3), its output streams resume by offset (internal/guest/attach.go:13-15), git snapshots count "as delivered only once the server acknowledges it" (docs/vm-guest.md:618-650 via 03 §2), the git engine already reaches VM checkouts only through it (§1.1), and the dialer is injectable (internal/runtime/vm/engine.go:83).
- **B runs:** a VM host agent for Cloud Hypervisor, units, disks, taps and firewall, save and restore, freeze, disk and memory use, capacity, the brake and memory guard, the image store, the egress proxy and the DNS relay, plus a byte tunnel to each VM's vsock ports.
- **A keeps:** everything else, unchanged: scheduler, coordination, git engine, PTY host, ACP host, store, bus, every lock in §3 except the per-host ones.
- **Packages touched:** runtime/vm (split `engine` into a host agent and a facade), guest (dialer), scheduler (per-host capacity and placement; memory loops move into the agent), gitengine (`VMCheckoutDisk`: the prepared disk must be filled on B, for example by cloning over A's existing `git-upload-pack` SSH transport: internal/sshd/session.go:82-96, 171), egress, sshd forwards, server wiring.
- **New protocol surface:** about 25 to 30 host-agent RPCs (create, boot, stop, destroy, save, restore, freeze, thaw, frozen, inspect, find, memory use, disk use, hold disks, memory events, capacity, image and program disks, checkout disk, sweeps), one byte-tunnel stream kind addressed by VM and vsock port (carrying the unchanged 16 + 13 guest RPCs and the forwarded sockets), one TCP tunnel for forwards, and either the `Home` file service (16 RPCs) tunnelled to A or homes on B.

### Cut F

- **Packages touched:** server (a restricted role), scheduler, coord, children, sshd, gitengine, evidence, events, store, watch, egress, memberhome, signing, ptyhost (transcripts for A's readers).
- **New protocol surface, upstream (B→A):** events (1 stream), B-owned run-row writes (about 12 kinds), coordination forwards (about 14 methods), git transfers (seed, publish, evidence refs, child fetch: about 4 streams), synchronous reads (run, member, workspace, `UnfinishedChildren`, `RunWaits`, egress policy). Downstream (A→B): the launch spec, 37 run-scoped controller methods, 9 home-reading launch checks, a live-state stream replacing the 11 per-row decorations, 3 wake methods, about 5 pushes (protect, handoff, co-authors, revoke, archive), 5 file and patch reads, and about 7 proxied stream kinds (§2.5). About 90 to 110 RPCs and 10 stream kinds.

### Seams that help and seams that are illusions

- **Helps:** `events.Bus` and `EventLog` are interfaces "so the bus could be externalized later without touching consumers", with caller-supplied IDs for idempotent retry. internal/events/bus.go:123-168; internal/events/inproc.go:50-52.
- **Helps:** `sshd.RunController` is one interface for all of sshd's calls into the scheduler; a routing implementation fits there. internal/sshd/seams.go:73-214.
- **Helps:** SSH subsystems are byte streams opened per channel; A could open the same subsystem on B and splice. internal/sshd/session.go:96-131.
- **Helps:** The in-session client speaks NDJSON JSON-RPC with no sender on the wire; identity is the listener, so a relay or method-level forwarder can bind identity on the link. internal/coord/socket.go:380-421 (via 02 §1); internal/coordtransport/client.go:116-124.
- **Helps:** The git transport already serves `git-upload-pack` and `git-receive-pack <workspace>` over SSH; B can seed and fetch with it. internal/sshd/session.go:82-96; internal/sshd/seams.go:18-21.
- **Partial:** the gitengine `Relay` reroutes only diff snapshots and moved HEADs (used inside the guest: internal/guest/helper/git_linux.go:205-214); checkout creation, publication, commits, evidence, patches and file reads still use local paths (`existingRepoPath` 23 sites, `snapshotStorePath` 26, `existingCheckoutPath` 13). internal/gitengine/engine.go:79-90; internal/gitengine/diffwatch.go:898-930.
- **Partial:** `store.Store` covers 180 of 377 methods; the rest arrive by assertion (§2.1).
- **Illusion:** `runtime.Runtime` looks location-neutral, but `Spec` carries host paths, Docker admission refuses a remote daemon, the scheduler reads `/proc` and cgroups itself, and the VM runtime is wired around the interface (§0, §1.2, §1.3).
- **Illusion:** `Deps.Runs` is `*scheduler.Scheduler`; coord, children, watch, push, rooms, storage, update, development and mirror call methods outside `RunController` (`RetainsContainer`, `RecoveryReady`, `UseCoordination`, `UseRunWaits`, `Busy`, `StorageRetention`, `CacheRetentions`, `Reattached`, `UseBaseCapture`, `UseUpdates`, `RequireCoordination`). internal/server/services.go:37; internal/server/svc_*.go (grep `d.Runs`).
- **Illusion:** `memberhome.Manager` and `signing.Service` are concrete types with file-system state used at about 250 sites. internal/scheduler/scheduler.go:92-99.

## 5. Recommendation

- **Assessment:** Cut T′, session VMs only. It leaves the database, the bus, the overlap index, the coordination service, every in-memory scheduler map and every A-side lock in one process, so no row gets two writers and no wake crosses processes. Its protocol is mostly the guest protocol the server already speaks, and recovery already treats runtime transport errors as "keep supervising" (internal/scheduler/supervise.go:73-86). Cut F needs a remote store under the scheduler's global lock (§2.2), a split coordination service (§2.6), co-owned run rows, and a routing proxy for 37 controller methods and 7 stream kinds, before it moves a single session.
- **Strongest argument against:** under T′ every byte the server handles for a B session crosses the link: terminal output as one blocking `Read` per 64 KiB, git packs, ACP traffic, signatures, and home file operations if homes stay on A. A link drop freezes the session's server-side view, its coordination after 60 s and its commit signing, and A stays the single supervisor and throughput bottleneck for every B session. Cut F keeps the session supervised, recorded and observable on B through a drop. T′ also cannot place a Docker session on B.
- **Smallest useful first step, on one machine:** keep a capacity-refused subsession queued for the existing reconcile loop instead of marking it `refused`. Today a queued child whose launch is refused, capacity included, becomes `refused` and nothing retries it (internal/children/dispatch.go:63-73, 150-152 via 01 §1). On one machine, a burst of subsessions on a full host waits instead of failing. It de-risks both cuts: the launch request is already a durable row the dispatcher re-authorizes under `AuthorizationMu` (internal/children/dispatch.go:64-156 via 02 §5), and that dispatcher is where host placement would go. It contradicts "Never loop or reinterpret unavailable capacity" (internal/scheduler/capacity.go:197-211) only at the queue level, which is a product decision.
- **Second step, needed by any cut, with no value on one machine:** a `host` column on `runs` and `member_terminals`, set at launch, with the 20 every-run-is-mine sites in 01 §6 filtered by it.

## 6. Riskiest unknowns, for a prototype

1. **Member homes and agent logins on two hosts.** Docker sessions need the home as a local bind mount (internal/runtime/docker.go:395-433 via 03 §4). VM sessions reach it through the monitor's file service, which forwards every file operation by path and ends the VM when the service fails (docs/vm-guest.md:212-232 via 03 §4; internal/runtime/vm/monitor_linux.go:28-33). Copying a login forks its refresh-token chain (docs/plans/2026-11-03-shared-account-credentials.md:137-147 via 03 §4). Only a prototype shows whether agents work with a home served from A over a real link, and what a stalled link does to a VM whose home service is a relay. **Not established:** any measurement of per-operation latency of the home service, local or remote.
2. **Reattachment after a transient link loss.** T′ needs the scheduler to re-establish one run's PTY attachment, ACP bridge and git `Watch` without a restart; that path is not established (§1.4). F needs B to buffer A-bound writes and events and reconcile co-owned run rows after the link returns. Neither exists. Also unknown: terminal throughput of a 64 KiB-per-round-trip `Read` loop on a real link.
3. **Memory safety and admission with a remote decider.** Capacity reservations and memory claims are in-process (internal/scheduler/scheduler.go:236-245); the brake acts every 500 ms (internal/scheduler/vm_freeze.go:18) and leaves a VM running while the server needs its guest (internal/scheduler/vm_freeze.go:97-127 via 01 §2). Whether a B-local brake and guard can keep that rule without the scheduler's per-run holds, and whether reserve → create → claim release stays correct when the reservation is made across the link, needs a prototype under a memory fill.
