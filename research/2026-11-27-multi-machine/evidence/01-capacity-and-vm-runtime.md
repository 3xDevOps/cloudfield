# 01. How one server decides it is full, and how it launches, sleeps, wakes and recovers sessions

Scope: one server on one host, as of `main` at 9cdd039a1 plus the pull requests named in each bullet. Nothing was built, run or probed. Tags follow `COLLECTOR-RULES.md`: **Design** is what the code or a guide says; **Measured** is a number a source reports it measured, with where.

Baseline of the VM speed pull requests, read with `gh pr view` on 3xDevOps/cloudfield on 2026-11-27:

- Merged and in the checkout: #1161 (dockerd not waited for), #1163 (wake does not wait for the snapshot delete), #1168 (frozen save waits for the agent session), #1169 (flaky tests). `git log` on this branch shows all four. PR #1161, #1163, #1168, #1169.
- Open, ready for review, not in the checkout: #1154 (helper disk hash), #1155 (wake on first keystroke), #1156 (helper redial every 10 ms), #1158 (per-step timing log), #1162 (25 ms power-off poll), #1164 (mirror fetch outside the authorization lock). PR #1154, #1155, #1156, #1158, #1162, #1164.
- Open drafts, not in the checkout: #1159 (idle VMs paused in memory), #1160 (a wake ends a sleep that has not paused the guest), #1166, #1167 (kept prepared checkout disk), #1172 (huge pages). PR #1159, #1160, #1166, #1167, #1172.
- Where a bullet says "after #nnnn", it describes the branch of that open PR, not `main`.

## 0. Shape that matters for every question

- **Design:** The server has one runtime value, `s.rt = cfg.Runtime`, built once at startup: Docker, or Docker plus the VM runtime when `session-runtime vm`. internal/server/server.go:328-369.
- **Design:** The `runtime.Runtime` interface comment names "remote runner nodes" as a possible future backend; no such backend exists in the tree. internal/runtime/runtime.go:1-5.
- **Design:** The scheduler holds one in-memory map of the runs it supervises, `runs map[domain.RunID]*supervised`. internal/scheduler/scheduler.go:270.
- **Design:** The server opens one SQLite database (`aether.db`) and puts the event log in the same file. internal/server/server.go:314-326.
- **Design:** The scheduler has no CPU-based admission and no limit on the number of sessions. The only `NumCPU` uses in the scheduler are the default per-session CPU ceiling and the browser gate. `grep NumCPU` over internal/scheduler, internal/runtime, internal/sshd, internal/server: internal/scheduler/capacity.go:71, internal/scheduler/development_lifecycle.go:29, internal/runtime/vm/engine.go:308.
- **Design:** `cmd/aether-server` has no flag for the memory floor, the PSI threshold, the number of sessions or the memory-guard thresholds; they are constants. Capacity flags that exist: `--min-free-disk`, `--run-cpus`, `--run-memory`, `--run-pids`, `--run-sleep-after`, `--run-container-ttl`, `--checkout-ttl`, `--session-runtime`, `--vm-kernel`, `--poll-interval`. cmd/aether-server/main.go:291-349; internal/scheduler/capacity.go:32-45.

## 1. Launch admission

### What is checked, in order

- **Design:** `LaunchWithOptions` runs `checkLogin`, then `reserveCapacity`, then captures the base, then creates the run row. A capacity refusal therefore returns before any run row, checkout or container exists. internal/scheduler/launch.go:316-323 (login, reserve), 342 (base capture), 399 (createRun).
- **Design:** The comment on `LaunchWithOptions` says the base is captured "before the run row is created, so a failed capture leaves no durable or in-memory run state". internal/scheduler/launch.go:266-267.
- **Design:** Admission is serialized by `capacityGate`, a channel of size 1, and each probe has a 5 s budget (`capacityProbeTimeout`). A caller that cannot get the gate in time gets `scheduler: host capacity unavailable: waiting for capacity probe: <ctx error>`. internal/scheduler/capacity.go:33, 309-317; internal/scheduler/scheduler.go:805.
- **Design:** Step 1, disk. For each measured filesystem the free bytes must be at least the floor, and `(free - floor) / 1 GiB` must exceed the number of provisionings already in flight. internal/scheduler/capacity.go:321-334, 391-393.
- **Design:** The disk floor is `--min-free-disk` when positive, else `max(5 GiB, min(total/20, 20 GiB))`. A negative `--min-free-disk` turns the disk check off. internal/scheduler/capacity.go:97-102, 322-324; docs/failure-handling.md:233-235, 1136.
- **Design:** Each provisioning counts 1 GiB toward the disk check (`startupDiskBytes`). internal/scheduler/capacity.go:34.
- **Design:** The filesystems measured are the data directory's filesystem ("Cloudfield data filesystem"), then the runtime's: Docker data and layer roots, containerd roots when Docker uses the containerd store, and with `vm` the "Session VM disks" filesystem under `<data dir>/vm`. internal/scheduler/capacity.go:274-295, 297-307; internal/runtime/docker_capacity.go:97-125; internal/runtime/vm/vm.go:368-380; internal/runtime/vm/stats.go:148-157.
- **Design:** Step 2, memory. `MemAvailable` must be at least `floor + claim + claimed`, where `floor = max(2 GiB, total/10)`, `claim` is 512 MiB for a launch (`startupMemoryBytes`), and `claimed` is what admitted starts have yet to take. internal/scheduler/capacity.go:37, 105-107, 213-220.
- **Design:** Memory ceilings (`--run-memory`) are never counted; only what sessions hold now. docs/failure-handling.md:330-331.
- **Design:** Step 3, stall. The server's own cgroup `memory.pressure` `some avg60` must stay under 10 (`maxMemoryPressure = 10.0`). internal/scheduler/capacity.go:44, 368-371.
- **Design:** The stall check is skipped (logged once) when the server has no cgroup of its own, runs in the root cgroup, or its cgroup has no `memory.pressure`. A read or parse error refuses with `ErrCapacityUnknown`. internal/scheduler/capacity.go:351-367; docs/failure-handling.md:369-374.
- **Design:** Docker's capacity probe returns an error unless the daemon is local: the socket must be `unix://`, and the `engine-id` file in Docker's data root must match the connected engine's ID. The code comment says "A Unix socket alone can be a remote forwarder". internal/runtime/docker_disk.go:179-211; internal/runtime/docker_capacity.go:34-37.
- **Design:** The same probe refuses unless Docker reports support for memory limit, swap limit, CPU CFS period and quota and PID limit. internal/runtime/docker_capacity.go:46-62.
- **Design:** With `session-runtime vm`, `Runtime.Capacity` calls Docker's `Capacity` first and fails if it fails, then appends the VM disk filesystem. So a VM host still needs a verified local Docker for admission. internal/runtime/vm/vm.go:368-380.
- **Design:** Host memory total and available come from `/proc/meminfo` (`MemTotal`, `MemAvailable`), parsed by the Docker runtime for admission and by `sysstats.HostMemory` for the guard. internal/runtime/docker_capacity.go:212-242; internal/scheduler/vm_memory.go:112-122.
- **Design:** A "browser" gate is the only CPU-count gate: `reserveBrowser` refuses when the number of reserved browsers reaches `runtime.NumCPU()`, with `scheduler: insufficient browser CPU capacity: each browser requires one CPU; <n> already reserved`. internal/scheduler/development_lifecycle.go:21-33; internal/scheduler/capacity.go:28.
- **Design:** No per-member or per-workspace session count limit is applied in `LaunchWithOptions`. What exists before it: a per-workspace metered-spend cap checked by `budgetGate` (`new session refused: $<spend> metered spend has reached the $<cap> cap`, a denial, not an availability error), the account-fallback measurement, and `AuthorizeLaunch` (permissions, pending members). Inside it: a per-member lock on home and login caches during a launch. For subsessions: the per-parent `max_live`. internal/sshd/cost.go:48-50; internal/cost/service.go:382-405; internal/sshd/launch_admission.go:25-54; internal/scheduler/launch.go:493; internal/children/dispatch.go:63-150; internal/store/migrate.go:2069; PR #1130 `2a-subsession-cost-limits.md`:12, 42.
- **Design:** `run.launch` holds a server-wide `authorizationMu` from authorization until `Admitted`, which `LaunchWithOptions` calls once the run row exists. internal/sshd/handlers.go:288, 321; internal/scheduler/launch.go:416-418.
- **Design:** Per-run limits come from the run's own override, else the admin's server limits (table `server_run_limits`), else flags, else defaults: CPUs `min(8, NumCPU)`, memory 8 GiB, pids 4096. internal/scheduler/capacity.go:68-95; internal/store/migrate.go:2207; docs/environments.md:287-303.
- **Design:** Every other container kind also reserves: agent logins, browser companions, the agent updater, usage fallback moves. internal/scheduler/agent_logins.go:191, internal/scheduler/development_browser.go:90 and 572, internal/scheduler/harness_update.go:239, internal/scheduler/usage_fallback.go:630.

### What a refused launch gets

- **Design:** The four errors are `scheduler: insufficient disk headroom`, `scheduler: insufficient host memory headroom`, `scheduler: insufficient browser CPU capacity` and `scheduler: host capacity unavailable`. internal/scheduler/capacity.go:25-30.
- **Design:** Disk refusal text: `scheduler: insufficient disk headroom: <filesystem> has <n> free bytes; reserve <n> bytes plus <n> bytes per provisioning (<n> already in flight)`. internal/scheduler/capacity.go:330-331; docs/failure-handling.md:269.
- **Design:** Memory refusal text: `scheduler: insufficient host memory headroom: <n> available bytes; reserve <n> bytes plus <n> bytes for this start and <n> bytes for starts in flight`. internal/scheduler/capacity.go:127-130.
- **Design:** Stall refusal text: `scheduler: insufficient host memory headroom: the server stalled on memory for <n>% of the last 60s (its cgroup's memory.pressure, some avg60); launches are refused at 10%`. internal/scheduler/capacity.go:369-370.
- **Design:** The server maps all of these to protocol code `CodeUnavailable` (-32004) with the comment "Capacity refusals are retryable availability failures, not bad requests". internal/sshd/errors.go:105-108; internal/protocol/protocol.go:168.
- **Design:** The server does not queue or retry a refused interactive launch. `reserve` makes one reclaim or save pass, measures again, and returns the error: "One reclaim or save pass, then fresh measurements. Never loop or reinterpret unavailable capacity." internal/scheduler/capacity.go:197-211.
- **Design:** On a disk refusal that pass is `reclaimDisk`; on a memory shortfall it is `saveIdleVMs`; a stall refusal and an unknown-capacity error get no pass. internal/scheduler/capacity.go:203-209.
- **Design:** The caller may retry: `run.launch` goes through `idempotentLaunch` with the client's idempotency key. internal/sshd/handlers.go:253.
- **Design:** The one real queue is for subsessions. `child.start` reserves a row in `run_children` with state `queued` and returns; `dispatch` starts queued children, oldest first, while the parent has fewer than `max_live` busy children (default 8, admin range 1 to 64). The queue is in the database. internal/store/children.go:148; internal/store/migrate.go:2024, 2069; internal/children/dispatch.go:17-48; PR #1130 `2a-subsession-cost-limits.md`:9, 42.
- **Design:** That queue is not host capacity: a queued child whose launch is then refused (capacity included) is set `refused`, the parent is notified `Subsession <id> (<purpose>) did not start: <reason>`, and nothing retries it. internal/children/dispatch.go:63-73, 150-152; PR #1130 `2a-subsession-cost-limits.md`:60.
- **Design:** A wake refused by admission returns its error to the request that caused it and keeps the session asleep, shown under **Asleep since**. docs/failure-handling.md:809-812; internal/scheduler/sleep.go:300-310.
- **Design:** Two callers retry a refused wake by themselves: the memory guard (for a session it saved while it worked; "trying again next check") and the 7-day idle close ("trying again at the next sweep"). internal/scheduler/vm_memory.go:515-516; internal/scheduler/idle_close.go:56-57.
- **Design:** `Relaunch` of a closed session reserves a wake too, so it can be refused the same way. internal/scheduler/recovery.go:141.
- **Design:** A server restart does not admit session recovery: it needs no reservation, with 4 sessions reconnected at a time (`runRecoveryConcurrency = 4`). internal/scheduler/recovery.go:30, 701-705; docs/failure-handling.md:40.

### How a wake is admitted (same gate)

- **Design:** A wake claims the size of its saved memory on disk, at least 512 MiB. A saved size that cannot be read counts as the VM's cap. internal/scheduler/capacity.go:173-190; internal/runtime/vm/save.go:191-224.
- **Design:** After the wake returns, the claim keeps counting what the VM has not loaded yet, until the VM holds all of it, stops, or has held no more for a minute (`memoryLoadTime`), checked every 5 s. internal/scheduler/capacity.go:38-41, 222-260; docs/failure-handling.md:336-343.
- **Design:** A boot of a stopped VM to commit its work (`reserveBoot`) checks memory only, so disk reclaim can run that boot. internal/scheduler/capacity.go:145-171; internal/scheduler/evidence.go:150-177.
- **Design:** All admission state is in memory in the server process: `capacityReservations`, `memoryClaims`. internal/scheduler/scheduler.go:243-245 (fields), internal/scheduler/capacity.go:376-377.

## 2. The memory guard and the brake

### What exists, and for which runtime

- **Design:** Two loops guard memory, and both exist only when the runtime can freeze or save VMs. `watchVMBrake` returns at once without `FreezeRuntime`; `watchVMMemory` returns at once without `SaveRuntime`. internal/scheduler/vm_freeze.go:30-34; internal/scheduler/vm_memory.go:39-42.
- **Design:** Only the VM runtime implements `SaveRuntime` and `FreezeRuntime`. `Save` on a non-VM ID fails with `vm runtime: only a session VM can be saved; Docker containers cannot`. internal/runtime/runtime.go:98-118; internal/runtime/vm/vm.go:394-400.
- **Design:** A Docker-only server has neither loop. Its answer to memory is refusal at admission: "Cloudfield never stops a running agent to make room", and "Running work in a container is never stopped to make room". docs/environments.md:313-314; docs/failure-handling.md:540.
- **Design:** The server starts both loops in `Scheduler.Start`: the brake before recovery, the guard after recovery finishes. internal/scheduler/scheduler.go:846-849, 897-901.
- **Design:** On a Docker-only server the only memory eviction is the kernel's: Docker session containers carry `oom_score_adj` 500, so the OOM killer takes them first. docs/failure-handling.md:516-533.

### The brake (freeze)

- **Design:** The brake reads `MemAvailable` every 500 ms (`vmMemoryBrake`). internal/scheduler/vm_freeze.go:18, 46-53.
- **Design:** Below the floor (`max(2 GiB, total/10)`) it freezes every session VM in its target list by writing `1` to the unit's `cgroup.freeze` under `/sys/fs/cgroup/aether.slice/aether-vm-<key>.service`. That needs no process, and no answer from systemd, Cloud Hypervisor or the guest. internal/scheduler/vm_freeze.go:73-83; internal/runtime/vm/freeze.go:31-38, 49-66.
- **Design:** At or above the floor it thaws one VM per check, the longest frozen first. If that takes the host below the floor, all freeze again. internal/scheduler/vm_freeze.go:84, 134-147.
- **Design:** The brake leaves a VM running while the server needs its guest: a launch in progress, a wake once admission lets it go ahead, a close, kill, finish, an idle save, post-restart take-up, an agent restart. internal/scheduler/vm_freeze.go:97-127, 174-190.
- **Design:** A frozen VM keeps its memory and answers nothing. The timeline note says `Paused: the server was running out of memory. It carries on by itself once memory is free again...`, at most once per 5 minutes per session. internal/scheduler/vm_freeze.go:19-24; docs/failure-handling.md:395-399.
- **Design:** A server that stops with less than twice the floor free leaves VMs frozen; the next server takes each session up asleep, marked to wake, and thaws nothing until it has taken every session up. internal/scheduler/vm_freeze.go:26-29, 36-45, 198-216; docs/failure-handling.md:412-431.
- **Design:** The frozen state lives in the kernel's cgroup, not in a file. After a restart `frozenVMs` finds frozen VMs by reading each unit's `cgroup.freeze`. internal/runtime/vm/freeze.go:49-52, 69-85.
- **Measured:** "On a 29 GiB host, three session VMs filling 5.5 GiB each at once took 1.4 GiB/s, 1.5 GiB/s at the peak, and ran the host out in about 10 seconds, before the first save had finished. That host's floor is 2.9 GiB... the freeze lands with at least 2.1 GiB left." docs/failure-handling.md:439-443; internal/scheduler/vm_freeze.go:15-18. The doc does not say which host or run produced the figure.

### The guard (save to disk)

- **Design:** The guard reads `/proc/meminfo` and `/proc/pressure/memory` every 5 s (`vmMemoryCheck`). internal/scheduler/vm_memory.go:18, 80, 127-143.
- **Design:** It starts when available memory falls below the floor, or when the host stalls: `full avg10` at 60 or more for 30 s, "systemd-oomd's defaults". It stops when available is at least twice the floor and the stall is under 60. internal/scheduler/vm_memory.go:19-23, 85-93.
- **Design:** While available is under twice the floor it saves one VM per check; above that, one more each time a stall lasts 30 s again, and then only a VM whose agent is idle. internal/scheduler/vm_memory.go:94-103, 302-322.
- **Design:** Order of candidates: rank 0, a closed session's VM or the running VM of a run recorded asleep; rank 1, an open session with an idle agent, the longest idle first; rank 2, any other, the one holding the most host memory first. internal/scheduler/vm_memory.go:234-242, 293-298.
- **Design:** Left out of the list: Background sessions (only Enhanced and Standard modes sleep), a session with a pending question, a VM already being saved, one whose save failed within 5 minutes (`memorySaveRetry`), and a run held awake for another reason. internal/scheduler/vm_memory.go:26, 262-280, 283-292.
- **Design:** The guest's memory number the guard ranks by is what the VM's unit holds of the host (cgroup working set), guest page cache included; a VM whose cgroup cannot be read counts as its unit's cap. internal/runtime/vm/stats.go:144-189; docs/vm-guest.md:497-505.
- **Design:** A VM is skipped when its memory is unknown or its saved memory would not leave the data filesystem `reserve + 4 GiB` (`reclaimHeadroom`) free, counting saves still being written. internal/scheduler/vm_memory.go:324-350; internal/scheduler/reclaim.go:16.
- **Design:** "Evicted" means saved: the VM's memory and device state go to `<data dir>/vm/<run id>/snapshot/` and the monitor exits. The session is recorded asleep and a later wake restores it. The guard never kills a session. internal/runtime/vm/save.go:41-130; internal/scheduler/vm_memory.go:58-59; docs/failure-handling.md:457-459.
- **Design:** A closed session's VM is stopped, not saved (it keeps its disks, no memory). internal/scheduler/vm_memory.go:370-389.
- **Design:** A frozen VM is saved as it stands, with neither the guest `Sync` nor the wait for dropped page cache, so its saved state also holds its page cache. internal/runtime/vm/save.go:41-46, 70-89; docs/vm-guest.md:525-529.
- **Design:** A session saved while its agent worked, or saved after the brake froze it, carries `memory_saved` in its sidecar and wakes on its own once memory is free: one at a time, the first saved first among those whose memory fits above twice the floor. internal/scheduler/vm_memory.go:431-444, 480-530; internal/scheduler/state.go:428.
- **Design:** An automatic wake that admission refuses is tried again next check; any other failure drops the mark and writes `Could not wake on its own once memory was free again: <error>. It wakes on its next use.` to the timeline. internal/scheduler/vm_memory.go:511-527.
- **Design:** One VM's save holds the guard at most 30 s (`memorySaveHold`); a save still running then continues alone and the next check measures again. internal/scheduler/vm_memory.go:30, 186-205; docs/failure-handling.md:484-489.
- **Design:** A save that fails leaves the VM running, stamps `saveFailedAt`, and the VM is out of the guard's reach for 5 minutes; a VM that died instead ends its session `failed`. internal/scheduler/sleep_vm.go:152-159; internal/scheduler/vm_memory.go:26; docs/failure-handling.md:490-495.
- **Design:** When nothing can be saved (no saved memory fits on disk, or every save fails), the VMs stay frozen until something else frees memory. A Background session's VM, a session waiting for an answer or changing mode or account, and one with a delivery, an exec or a browser operation in flight are never saved while everything else is not enough. docs/failure-handling.md:443-450.
- **Design:** Admission calls the same machinery once: `saveIdleVMs` saves only idle VMs, longest idle first, until `2 * floor + need` is free; it saves none when all idle VMs together hold too little to admit the start. internal/scheduler/vm_memory.go:145-184; internal/scheduler/capacity.go:207-208.
- **Design:** Memory taken by something the brake does not freeze (an environment terminal's VM, or faster than 500 ms) goes to the kernel OOM killer. `oom_score_adj`: 500 for Docker session containers, 0 for a VM's unit, -500 for the server. docs/failure-handling.md:516-533; cmd/aether-server/main.go:585-598.
- **Design:** The sessions' parent cgroup `aether.slice` has `CPUWeight=50` and `IOWeight=50` and no memory limit, and "A `MemoryHigh` on it would stop the memory guard" because the host cannot swap VM memory. docs/install.md:604-617.
- **Not established:** What the server does when memory is exhausted by Docker session containers and there are no VMs. No loop acts; only refusal at admission and the kernel's OOM killer apply. Looked for: any `Freeze`, `Pause` or eviction on a Docker-only runtime in internal/scheduler, none found.

### Memory per VM (inputs to the numbers above)

- **Design:** The guest boots with RAM `limit + limit/32 + 432 MiB` (`guestMiB`; `guestOverheadMiB` = 288 + 48 + 96). internal/runtime/vm/engine.go:58-63, 315-321; docs/environments.md:374-380.
- **Design:** The unit's `MemoryMax` is guest RAM plus 512 MiB (`unitOverheadMiB`) and `MemorySwapMax=0`. internal/runtime/vm/engine.go:64-67, 323-326, 631.
- **Design:** The VM monitor runs with `--memory size=<guestMiB>M,shared=on` and `--balloon size=0,free_page_reporting=on`; the kernel command line has `page_reporting.page_reporting_order=0`. internal/runtime/vm/engine.go:71-76, 545-546.
- **Design:** The guest returns memory by free page reporting, a helper trim every 10 s, and DAMON_RECLAIM with `min_age` 60 s. docs/vm-guest.md:149-210.
- **Measured:** "a 2 GiB session VM held 159 MiB of the host once booted" (hosted test runner). docs/failure-handling.md:333-335; PR #1130 `2a-subsession-cost-limits.md`:21.
- **Measured:** One idle VM's host memory in PR #1159's runs: frozen-session VM (1 GiB, checkout disk) 730 MiB awake and idle, 380 MiB paused for 40 s, 0 saved; stopped-vCPU VM (512 MiB) 786, 366, 0. Run 45926388941 (hosted KVM runner). PR #1159.
- **Measured:** PR #1172, hosted KVM runners, 1 CPU / 8 GiB VM: host memory held at ready 360 MiB on 4 KiB pages, 368 MiB on huge pages. PR #1172.

## 3. Idle handling: forms of sleep, where state lives, what a wake needs

### When a session sleeps

- **Design:** `sleepIdle` runs on every poll tick: `--poll-interval`, default 30 s, values above 1 minute act as 1 minute. internal/scheduler/scheduler.go:908-930; docs/failure-handling.md:1132.
- **Design:** `--run-sleep-after` defaults to 30 minutes (`DefaultRunSleepAfter`); for session VMs the default is 5 minutes (`DefaultVMSleepAfter`), unless the flag is set, in which case it applies to both. internal/scheduler/sleep.go:22-27; cmd/aether-server/main.go:385-389; docs/failure-handling.md:864-865, 1135. `0` keeps every session awake.
- **Design:** What sleeps: an Enhanced session, or a Standard (TUI) session in a VM. A container's Enhanced session sleeps only for an agent that restores its conversation (Claude Code; the code asks `resumes(harness)`). Background sessions never. internal/scheduler/sleep.go:564-570, 606-615; docs/failure-handling.md:766-775, 862-863.
- **Design:** A VM stays awake while its guest uses more than 50% of a CPU (`guestBusyPercent`); a container stays awake while a terminal has a foreground job. Other holds: a turn, a queued prompt, an open request, a background task, a paused or switching session. internal/scheduler/sleep.go:633-695, 742-749; docs/failure-handling.md:777-792, 866-878.
- **Design:** A sleep re-checks idleness under the run's lifecycle lock, then marks the run asleep before saving or stopping; a wake that finds the run falling asleep waits for the sleep to finish. internal/scheduler/sleep.go:713-775, 186-203.
- **Design:** `asleep_since`, `asleep_saved` and `wake_error` are in the run's sidecar file, not in the database. internal/scheduler/state.go:482-486.

### Forms of sleep

- **Design (container):** `docker stop`, never `docker rm`. Memory and CPU are freed; checkout, home, the container's disk layer, conversation and branch stay; processes, shell tabs, nested Docker and open connections are lost. docs/failure-handling.md:766-768, 822-833; internal/scheduler/sleep.go:792-875.
- **Design (VM saved to disk, on `main`):** `saveLive` detaches the agent, terminals and diff watch, then `engine.save` pauses the VM, runs the guest `Sync`, waits up to 40 s (`reportTimeout`) for the host to get dropped page cache back, runs `Sync` again, pauses, and calls Cloud Hypervisor `vm.snapshot` into `<data dir>/vm/<run id>/snapshot/`. Then it shuts the monitor down. internal/scheduler/sleep_vm.go:83-169; internal/runtime/vm/save.go:21-130; docs/vm-guest.md:512-529, 531-537.
- **Design (VM paused in memory, #1159, draft):** The sleep sweep only pauses an idle VM in host memory (`Runtime.Pause`) after the same detach. The next use resumes it. The memory guard saves paused VMs to disk "before the host is short": while less than twice the floor is free it saves paused VMs, longest asleep first, back to back; the sweep also saves a VM paused for an hour. A new `asleep_in_memory` field on the run and on `run.sleep` tells the two apart; the sidecar records `asleep_resident`. PR #1159.
- **Design (#1159):** A paused VM is lost on a host reboot or power loss, and by a kernel OOM kill of its hypervisor; the session then starts again from its disk on its next use. PR #1159 ("What this costs").
- **Design (#1159):** A paused VM is saved only while the data filesystem has its reserve plus 4 GiB plus the VM's memory free; on a fuller disk it stays paused. PR #1159 ("What this costs").
- **Design (#1160, draft, stacked on #1159):** A wake that finds the run falling asleep cancels the sleep if the guest is not yet paused; the sleep's wait for freed pages took 24.0 s of a 26.8 s save. PR #1160.
- **Design:** A VM with a checkout shared over virtio-fs cannot be saved: `vm runtime: cannot save VM <key>: it shares <paths> over virtio-fs, which would come back unusable after a restore`. Only sessions created before VM checkout disks have one. internal/runtime/vm/save.go:63-69; PR #1159 ("Not changed").
- **Design:** A closed session's VM is stopped, not saved, and keeps its disks for `--run-container-ttl` (default 720 h). internal/scheduler/sleep.go:791-875; docs/failure-handling.md:879-882, 1134; internal/scheduler/vm_memory.go:387-389.

### Where saved state lives (host paths, as templates)

- **Design:** Each VM's directory is `<data dir>/vm/<run id>/` (the creation key is the run ID). internal/runtime/vm/engine.go:112; internal/runtime/vm/record.go:15-44, 133-135; docs/environments.md:361-364.
- **Design:** The saved state is `<data dir>/vm/<run id>/snapshot/`, holding Cloud Hypervisor's `config.json`, `state.json` and `memory-ranges`. After a wake it is renamed `snapshot.used` and deleted in the background. internal/runtime/vm/record.go:43-44; internal/runtime/vm/save.go:101-105, 389-402, 408-437; internal/runtime/vm/kvm_memory_test.go:287; internal/runtime/vm/engine_test.go:601.
- **Design:** `<data dir>/vm/<run id>/vm.json` is the VM's record: `saved`, `resumed` (disk modification times), `frozen`, `exited`, `boots`, `net` (network index) and every disk. Other files in the directory: `launch.json`, `api.sock`, `vsock.sock`, `fs.sock`, `home.sock`, `root.img`, `data.img`, `cache.img`, `helper.img`, `browser.img`, `console.log`, `events.json`, `monitor.log`. internal/runtime/vm/record.go:16-44, 54-132.
- **Design:** A run's other durable pieces: the sidecar `<data dir>/scheduler/<run id>.json`, the agent-database disk directory `<data dir>/scheduler/vm-databases/<run id>/`, the checkout disk, and the member's home under `<data dir>/homes/<member>/`. internal/scheduler/state.go:416-487, 591-593; internal/scheduler/vm_sessions.go:35; docs/install.md:1140-1154.
- **Design:** The image store is `<data dir>/vm/images/` with `bases/`, `helpers/`, `prepared/`, `caches/`, `tmp/`, `tags.json`. internal/vmimage/store.go:550-554; internal/vmimage/cache.go:262; internal/server/vmruntime.go:32.
- **Design:** The backup guidance leaves out each VM's disks and saved memory: "a recovered server keeps those sessions' branches and history, and cannot reopen them." docs/install.md:1157-1166.

### How big a saved state is

- **Measured:** A 2 GiB test VM whose page cache held 1400 MiB wrote 137 MiB of memory in a 21 s save, against 1228 to 1315 MiB in 7 s before the cache wait (`TestKVMSaveWritesOnlyWhatTheSessionUses`). docs/vm-guest.md:521-524; docs/failure-handling.md:959-962.
- **Measured:** "918 MiB on disk for a VM that held 934 MiB, and 938 MiB held again after the wake." docs/failure-handling.md:337-339.
- **Measured:** On a hosted runner a VM with 1.3 GiB of saved memory woke in 1.9 to 8.5 s from disk and about 1 s with the file in the page cache. docs/failure-handling.md:907-910.
- **Measured:** Restore of 1.3 GiB from disk, median of 6: 3463 ms before #1163, 3371 and 3452 ms after (different runners). PR #1163.
- **Measured:** A 2 GiB VM holding 600 MiB: 1.18 s to the first command with the saved memory in the page cache, 2.80 s from disk, then 1.3 s more before the guest had its memory mapped again; the save before it took 27 s (median). PR #1159 ("What was slow"), run 45796405210.
- **Measured:** With huge pages on a restored guest the next snapshot was 1500 MiB against 990 MiB, which is why #1172 sets `memory.thp` to `false` in the saved `config.json`. PR #1172.
- **Design:** A snapshot's time budget assumes a write of at least 20 MiB/s (`snapshotRate`): "a 24 GiB guest gets over 20 minutes". internal/runtime/vm/save.go:35-38, 110.

### What a wake needs

- **Design:** Admission on memory equal to the saved size on disk (at least 512 MiB), through the same `capacityGate`; a disk check too. internal/scheduler/capacity.go:173-190, 300; docs/failure-handling.md:808-810.
- **Design:** The run's `<data dir>/vm/<run id>/` directory on the same host: `vm.json`, `launch.json` (the guest's mount indexes and the home file-service config are re-read from it), and the snapshot. internal/runtime/vm/save.go:314-321.
- **Design:** The same disk paths: `restore` records the modification time of every disk named in the snapshot's own `config.json`, by absolute path (`os.Stat(d.Path)`), and drops the saved state when any has moved. internal/runtime/vm/save.go:333-335, 406-437.
- **Design:** The restore starts `cloud-hypervisor --restore source_url=file://<snapshot dir>` in a new unit `aether-vm-<run id>.service` in `aether.slice`, from the `cloud-hypervisor` found on the host's `PATH` at that moment (the pinned `/usr/local/bin/cloud-hypervisor`, v54.1). internal/runtime/vm/save.go:318-321, 327; internal/runtime/vm/monitor_linux.go:23, 79; internal/serversetup/vmhost.go:35-41; internal/runtime/vm/unit.go:28-45.
- **Design:** The tap `aethervm<n>` and the address come from the VM's recorded index `net`; `ensureNetwork` rebuilds the firewall before the restore. internal/runtime/vm/save.go:322-326; internal/runtime/vm/network.go:23-95.
- **Design:** The restore loads all saved memory before the guest runs (default restore mode); the on-demand mode is not used because Cloud Hypervisor v54.1 takes an already-touched page as loaded, which zeroed saved pages and killed VMs by the third wake. docs/vm-guest.md:539-550.
- **Design:** The wake's time budget is 3 minutes plus 1 s per 20 MiB of saved memory; the load gets 60 s plus 1 s per 20 MiB of the unit's cap; the resume and the helper's answer get 90 s. internal/scheduler/sleep.go:34-37, 230-235; internal/runtime/vm/save.go:22, 330; docs/vm-guest.md:552-555.
- **Design:** After the restore the server sets the guest clock (`SyncClock`) and reattaches the agent, terminals and diff watch. internal/runtime/vm/save.go:365-368; internal/scheduler/sleep.go:320-336.
- **Design:** Every restore is guarded: while no disk has moved, a failed restore keeps the saved state for the next try; once the guest wrote a disk, the saved state is dropped and the session starts from its disk. internal/runtime/vm/save.go:288-299, 369-383; docs/vm-guest.md:557-586.
- **Design:** When the restore fails the scheduler starts the VM from its disk only for an Enhanced session of an agent that restores its conversation (or a closed session). A Standard session stays asleep with the restore error. internal/scheduler/sleep_vm.go:237-325, 389-415; docs/failure-handling.md:922-932.

### Versions, other hosts, other CPUs

- **Design:** The plan says: "Cloud Hypervisor... can share a host folder into a VM (virtio-fs), save and restore a running VM, and accept live migration from its two previous releases." It describes the monitor's capability; no code in `internal/runtime/vm`, `internal/vmimage` or `internal/guest` mentions migration (`grep -i migrat`). docs/plans/2026-11-13-microvm-sessions.md:14-17.
- **Design:** The plan's wake row: "Restore the saved VM. If it cannot be restored (another monitor or kernel version, a deleted file), cold-start from the disks and resume the conversation, as today." docs/plans/2026-11-13-microvm-sessions.md:71.
- **Design:** The guide: when the server "now runs another Cloud Hypervisor or guest kernel, or the file is gone or damaged", an Enhanced session of an agent that restores its conversation starts again from its disk; processes end. Any other open session stays asleep. docs/failure-handling.md:922-932.
- **Design:** A saved VM "keeps the [helper disk] it booted with" and a running or saved VM boots the new kernel only on its next cold start. docs/plans/2026-11-13-microvm-sessions.md:36-37; docs/environments.md:407-412 (program disks); docs/vm-guest.md:208-210; docs/install.md:803-806.
- **Design:** `guest.Protocol = 2`; "A helper in a VM restored from a save can be older than the server"; a different protocol makes `Ping` fail with `guest.ErrIncompatible` and the session needs a cold start. internal/guest/client.go:25-26, 255; docs/vm-guest.md:729-735.
- **Design:** The update path installs the release's Cloud Hypervisor and kernel before restart; "Running VMs keep theirs, and their helper disk, until they stop". docs/install.md:801-806.
- **Design:** The memory file is written by Cloud Hypervisor on the same host: the snapshot's `config.json` holds the absolute disk paths and the VM monitor opens them as root inside a unit whose mount namespace hides the data directory except the VM's own paths. internal/runtime/vm/save.go:408-437; docs/environments.md:358-361.
- **Design:** The monitor arguments pin no CPU model or CPU profile: `--cpus boot=<n>,nested=off`. internal/runtime/vm/engine.go:544.
- **Design:** The only note about hosts is that VMs need `/dev/kvm`, x86-64, and "Saved VMs need local NVMe storage to wake quickly"; on AWS the instance-store NVMe is lost when the instance stops or fails. docs/plans/2026-11-13-microvm-sessions.md:110-113; docs/install.md:85-90, 136-160.
- **Not established:** Restoring a saved VM on a different host, or after a Cloud Hypervisor or kernel upgrade, is described only as "cold-start from the disks" in the plan and the guide. No source states whether Cloud Hypervisor v54.1 restores across CPU models, and none describes moving `snapshot/` plus the disks to another host. Looked for: `grep -i "migrat\|cpu model\|cpu profile\|cpuid\|another host\|different host"` over docs/vm-guest.md, docs/environments.md, docs/failure-handling.md, docs/install.md, the plan, internal/runtime/vm, internal/vmimage, internal/guest.
- **Not established:** The rule that decides which Cloud Hypervisor releases accept a snapshot of another release. The plan's "two previous releases" is about live migration; the code does not state a snapshot compatibility rule. Looked for: the plan and docs/vm-guest.md "Save and restore", internal/runtime/vm/save.go.

## 4. Disk: watermarks, sweeps, per-session disks, shared caches, host ties

### Watermarks and the order disk is freed

- **Design:** Admission floor (refuse): `max(5 GiB, min(total/20, 20 GiB))` plus 1 GiB per provisioning in flight. internal/scheduler/capacity.go:34, 97-102, 391-393.
- **Design:** `reclaimHeadroom` is 4 GiB above the floor. Reclaim starts a filesystem 85% full or below floor plus headroom, frees "what nobody loses" down to 80% used, at most every 5 minutes, then frees what sessions lose until floor plus headroom is back. internal/scheduler/reclaim.go:16-25, 42-82.
- **Design:** Reclaim runs on every poll tick (30 s default) when its lock is free, and once before a disk refusal. internal/scheduler/scheduler.go:938-941; internal/scheduler/capacity.go:204-206.
- **Design:** "What nobody loses", every 5 minutes: unused standard and browser images, caches over budget or unused for long, leftover scratch. internal/scheduler/reclaim.go:42-82; docs/failure-handling.md:236-241.
- **Design:** Order of what sessions lose: (1) containers and VM disks of closed sessions, soonest to expire first; (2) saved memory of sleeping VMs whose agent restores its conversation, longest asleep first (a session whose conversation would not come back, and a session the guard saved while it worked, are kept); (3) checkouts of finished sessions (skipped when `--checkout-ttl` is negative); (4) other members' stopped environments. Then every cache, image and scratch directory nobody uses. internal/scheduler/reclaim.go:85-146, 180-224; docs/failure-handling.md:246-264.
- **Design:** After #1167 (draft), reclaim first drops every kept prepared checkout disk no session uses, before step (1). PR #1167.
- **Design:** The VM disk guard: when the data filesystem first falls below the floor it records each VM's disk use, and every 5 s (`vmDiskCheck`) pauses every session whose disks grew 64 MiB (`vmDiskGrowth`) since that point and stops its VM; a session that fills again within 10 minutes (`vmDiskRehold`) of a resume stays held. It resumes holds once free space is at least floor plus 4 GiB. internal/scheduler/vm_disks.go:15-30, 54-88, 90-117, 148-176; docs/failure-handling.md:272-291.
- **Design:** `--min-free-disk` negative turns off the disk check, the disk guard and reclaim; the memory check stays. internal/scheduler/capacity.go:322-324; internal/scheduler/reclaim.go:42-45; internal/scheduler/vm_disks.go:67; docs/failure-handling.md:1136.
- **Design:** Finished checkouts are removed after `--checkout-ttl` (72 h), by a sweep every hour and at start; the sweep reads every workspace's runs from the database and deletes the checkout path stored in `runs.worktree`. internal/scheduler/scheduler.go:914-916, 943-945; internal/scheduler/supervise.go:737-790; docs/failure-handling.md:1133.
- **Design:** The scratch sweep removes a directory under `<data dir>/scratch/`, `account-config/` or `scheduler/vm-databases/` once no active run, supervised run or pending launch owns it and it is older than 1 hour. internal/scheduler/scratch.go:20-23, 71-116.
- **Design:** Package caches: pool target 4 GiB, total target 16 GiB, inactive age 7 days, per-member cap 20 GiB. internal/scheduler/cache.go:23-28.
- **Design:** The image store sweep (run on every VM destroy) deletes a base, helper or prepared disk unless it is tagged or another file is hard-linked to it. internal/vmimage/store.go:381-431; docs/vm-guest.md:602-611.

### Per-session disks (session VM)

- **Design:** Root disk: `<data dir>/vm/<run id>/root.img`, a qcow2 layer over a read-only raw base disk, 64 GiB sparse (`rootSize`), with a hard link `root.img.<base id>.base` to the base in the VM's directory. internal/vmimage/store.go:31-33, 186-236; internal/runtime/vm/record.go:23.
- **Design:** `NewRootDisk` states: "path must be on the store's filesystem", because the pin is a hard link; a cross-device link fails with `<link> must be on the same filesystem as the image store <dir>`. internal/vmimage/store.go:186-190, 442-458.
- **Design:** Base disk: the session's image flattened to a read-only raw disk in `<data dir>/vm/images/bases/<id>/disk.raw`; one host cache for all sessions on that image. A saved environment is a base `local-*`, listed in `<data dir>/vm/images/tags.json`. internal/vmimage/store.go:128-190, 467-488, 550-554; docs/plans/2026-11-13-microvm-sessions.md:38; docs/install.md:1154.
- **Design:** Data disk: `<data dir>/vm/<run id>/data.img`, raw, 128 GiB sparse (`dataDiskBytes`); holds `/tmp` and `/var/lib/docker`. The plan also lists `/workspace` and `/repos/<name>`, which now live on checkout disks. internal/runtime/vm/engine.go:51-55, 266; docs/environments.md:386-395; docs/plans/2026-11-13-microvm-sessions.md:40; docs/vm-guest.md:138.
- **Design:** Checkout disk: `<data dir>/checkouts/<run id>/disk.qcow2`, a qcow2 layer over a prepared disk per workspace, base commit and owner in `<data dir>/vm/images/prepared/`; it follows the checkout's retention, not the VM's. docs/vm-guest.md:602-611.
- **Design:** Cache disk: `<data dir>/vm/<run id>/cache.img`, a qcow2 layer over the member's newest cache pool, 32 GiB sparse (`cacheSize`); the newest is dropped over 20 GiB (`cacheLimit`) or unused for 7 days (`cacheIdleAge`). A session's cache becomes the member's newest when its VM stops for good, not on an idle sleep. internal/vmimage/cache.go:16-27, 32-72; docs/environments.md:394-406.
- **Design:** State disk: `<data dir>/scheduler/vm-databases/<run id>/<n>/disk.img`, raw, 1 GiB sparse (`stateDiskBytes`), the session's own agent databases; it outlives a replaced VM. internal/runtime/vm/engine.go:51-55, 558-582; internal/scheduler/vm_sessions.go:25-35, 52-80.
- **Design:** Helper disk (`helper.img`, per server binary, read-only), browser disk (`browser.img`, read-only), and agent program disks (about 1 GB per member for omp, Claude Code and its adapter; one per version, deleted when a newer exists and no VM uses it). internal/runtime/vm/engine.go:470-487; docs/environments.md:407-412.
- **Design:** Per-VM files in `<data dir>/vm/<run id>/`: `monitor.log`, `console.log` (1 MiB), the sockets. docs/environments.md:361-364.
- **Design:** Disk copies use `cp --reflink=auto --sparse=always`; on XFS or Btrfs Save environment and cache promotion share blocks, on ext4 they copy. internal/vmimage/disk.go:99; internal/runtime/vm/commit.go:97; docs/environments.md:495-499; docs/install.md:152-155.
- **Measured:** Checkout disk: first session on a base builds the prepared disk in 1.7 s for 40,000 files on local NVMe; each later session's qcow2 layer takes 17 ms. docs/vm-guest.md:607-609. (#1167: 8.8 s for the same 40,000-file repo on a hosted KVM runner, 640 ms for a 2,000-file one; a kept prepared disk makes the next create 86 ms. PR #1167.)
- **Design:** Docker-session counterpart: checkout and home bind-mounted from `<data dir>/checkouts/<run id>/` and `<data dir>/homes/<member>/`; the writable layer lives in Docker's own storage, outside the data directory. internal/runtime/runtime.go:222-233; internal/disk/disk.go:37-39; docs/install.md:1145-1147.

### What is tied to this host's filesystem

- **Design:** "VM disks, checkouts and the image store must share one filesystem, because they hard-link each other." docs/install.md:155-157.
- **Design:** The disk checks measure the filesystem of `<data dir>`, Docker's roots and `<data dir>/vm`; each is a local path. internal/scheduler/capacity.go:297-307; internal/runtime/docker_capacity.go:97-125; internal/runtime/vm/stats.go:148-157.
- **Design:** `runs.worktree` stores the server-side absolute path of the checkout. internal/domain/domain.go:869-870; internal/store/migrate.go:442-458.
- **Design:** Snapshot `config.json` holds the absolute path of every disk. internal/runtime/vm/save.go:408-437.
- **Design:** Local NVMe is advised for saved memory; AWS instance storage "is lost when the instance stops or fails". docs/install.md:136-160; docs/plans/2026-11-13-microvm-sessions.md:112-113.
- **Design:** Unix socket paths under `<data dir>/coord/<run id>/` and `<data dir>/sign/<member>/` limit the data-directory path length (about 100 characters). internal/signing/agent.go:54-58; docs/install.md:1175-1176.
- **Design:** The data directory is one tree for `aether.db`, repos, checkouts, transcripts, homes, signing keys, coord sockets, scratch and `vm/`. internal/disk/disk.go:82-93, 227-267; docs/install.md:1140-1154.

## 5. Per-session resource shape

- **Design:** vCPUs: the CPU limit rounded up to whole vCPUs, capped at the host's CPU count (`min(host, max(1, ceil(limit)))`); no limit means every host CPU. The exact limit is enforced by the unit's `CPUQuota=<ceil(limit*100)>%`. internal/runtime/vm/engine.go:305-313, 634-636; docs/environments.md:374-376.
- **Design:** Default limit: CPUs `min(8, NumCPU)`, memory 8 GiB, pids 4096. internal/scheduler/capacity.go:68-75.
- **Design:** The VM monitor starts with `--cpus boot=<n>,nested=off`; the reason is stated: "With nested virtualization a guest rebooted on its second restore (Cloud Hypervisor v54.1 on a host that is itself a VM). Sessions run no VMs of their own." internal/runtime/vm/engine.go:541-544.
- **Design:** Memory: the session gets the whole limit; the guest RAM is `limit + limit/32 + 432 MiB`; swap is a quarter of the limit, a file on the data disk; the unit's `MemoryMax` adds 512 MiB and `MemorySwapMax=0`. internal/runtime/vm/engine.go:58-67, 315-326, 493-496, 631; docs/environments.md:374-385.
- **Design:** Free page reporting (`--balloon size=0,free_page_reporting=on`), a helper trim every 10 s and DAMON_RECLAIM give memory back. The balloon is never inflated to a size. internal/runtime/vm/engine.go:546; docs/vm-guest.md:149-210.
- **Design:** Huge pages (#1172, draft): the server sets `/sys/kernel/mm/transparent_hugepage/shmem_enabled` from `never` to `advise` when it starts with `session-runtime vm` and logs it, a host-wide kernel setting; a restore sets `memory.thp` to `false` in the saved `config.json`. PR #1172 (`internal/runtime/vm/hugepages.go`).
- **Measured (#1172):** hosted KVM runners, Cloud Hypervisor v54.1, 1 CPU / 8 GiB VM: VM start median 1.76 s on 4 KiB pages, 774 ms on huge pages (run 45911182852); unit CPU until ready 999 ms against 219 ms; host memory held at ready 360 against 368 MiB; Claude Code ACP adapter from spawn to `session/new` 2392 ms against 1243 ms (n=10, run 45909058850). PR #1172.
- **Measured (#1172):** Without the restore override a woken guest held 1580 MiB instead of 1012 MiB and its next snapshot was 1500 MiB instead of 990 MiB; it woke in 514 ms instead of 1153 ms. PR #1172.
- **Design:** Server reserve: memory floor `max(2 GiB, total/10)` is kept free from admission; each launch counts 512 MiB. internal/scheduler/capacity.go:37, 105-107.
- **Design:** The server runs as root in `aether-server.service` with `OOMScoreAdjust=-500`, `CPUWeight=10000`, `IOWeight=10000`, `MemoryLow=1G`; the sessions' parent `aether.slice` has `CPUWeight=50` and `IOWeight=50`; stop timeout `TimeoutStopSec=90`. internal/serversetup/unit.go:42-56, 109; docs/install.md:604-624; docs/failure-handling.md:421.
- **Design:** The server writes no limit for itself beyond that: no `MemoryMax` or `MemoryHigh`. internal/serversetup/unit.go:42-56; docs/install.md:610-616.
- **Design:** The unit of each VM sets `Type=exec`, `KillMode=control-group`, `TimeoutStopSec=15`, `PrivateMounts=yes`, no `OOMScoreAdjust`. internal/runtime/vm/engine.go:625-633.
- **Design:** Browser companion: one CPU each, at most `NumCPU` reserved. internal/scheduler/development_lifecycle.go:21-33.
- **Design:** A Docker session container gets `--cpus`, memory, swap and PID limits from the same ceiling. docs/environments.md:284-307.

## 6. Recovery after a server restart

### Where each kind of state lives

- **Design:** In the database (`aether.db`): the `runs` row (status, reason, branch, `worktree` path, limits and the rest of `runCols`), `run_children` reservations, `server_run_limits`, `member_terminals` (member, `container_id`, image), and the event log. internal/store/runs.go:187-193; internal/store/migrate.go:442-458, 600-612, 2024-2053, 2207; internal/server/server.go:321.
- **Design:** In a per-run sidecar file `<data dir>/scheduler/<run id>.json`: `container_id`, `workspace_id`, `mode`, `paused`, `disk_held`, `memory_saved`, `kill_requested`, `retained`, `retained_until`, `destroy_pending`, `evidence_pending`, `run_user`, `home`, `login_member`, `coord_dir`, `bridge_path`, agent session and keeper identity, `asleep_since`, `asleep_saved`, `wake_error`, `exit_observed`, `exit_code`. It is written atomically (temp file, fsync, rename). internal/scheduler/state.go:416-487, 591-593, 661-688.
- **Design:** The runtime's container ID is not in the `runs` table. For a session in a VM it is `vm-<run id>`, derived from the creation key, which is the run ID. internal/runtime/vm/vm.go:22, 100-122; internal/runtime/runtime.go:281-285.
- **Design:** In the VM engine's files: `<data dir>/vm/<run id>/vm.json` and neighbours (section 3). internal/runtime/vm/record.go:16-132.
- **Design:** In systemd and the kernel: one transient unit `aether-vm-<run id>.service` in `aether.slice` per VM, started with `systemd-run --collect`, outside the server's process tree; the cgroup `cgroup.freeze` flag; the tap `aethervm<n>`; the nftables table `inet aether_vm` (rebuilt at every start). internal/runtime/vm/unit.go:28-45; internal/runtime/vm/network.go:23-60; docs/environments.md:353-357, 450-452.
- **Design:** Host processes: Cloud Hypervisor and `virtiofsd` per VM, and a monitor (`aether-server vm-monitor`) that also serves the home file service. internal/runtime/vm/monitor.go:12-14; internal/runtime/vm/monitor_linux.go:23, 79, 318.
- **Design:** In the server's memory only: the `runs` map, `memoryClaims`, `capacityReservations`, `guestsNeeded`, `frozenAt`, helper clients, and the pty sessions of each terminal (rebuilt from the sidecar and the guest on recovery). internal/scheduler/scheduler.go:235-245, 270; internal/runtime/vm/engine.go:79-108.
- **Design:** Docker sessions: the container, its writable layer and its label-held creation key and fingerprint live in the Docker daemon on the host. internal/runtime/runtime.go:281-290; internal/runtime/docker_capacity.go:34-37.

### How the server re-attaches

- **Design:** `Scheduler.Start` first starts the brake, subscribes to usage limits, and calls `recoverRuns`, which loads every non-terminal run from the database with `ListActiveRuns` and recovers 4 at a time, then recovers terminals and development surfaces. The scheduler counts as started once every open session finished. internal/scheduler/scheduler.go:841-886; internal/scheduler/recovery.go:30, 688-742; docs/failure-handling.md:40.
- **Design:** For each run, `recoverSupervised` reads the sidecar, then `Runtime.Inspect(container_id)`: exited means finalize the exit; `ErrNotFound` means `didNotSurvive`; success means `attachAndSupervise`; any other error adopts the container without attaching and retries via the exit waiter. internal/scheduler/recovery.go:1399-1494, 1801.
- **Design:** For a VM, `Inspect` reads `vm.json` and the unit state; a booted VM whose unit is gone without an exit becomes `exited` with code 255 (`lostExitCode`). internal/runtime/vm/lifecycle.go:30-37, 381-401, 542-575.
- **Design:** `FindByCreationKey` for a session looks for `<data dir>/vm/<run id>/vm.json` first and Docker second. internal/runtime/vm/vm.go:271-285; internal/runtime/vm/lifecycle.go:577-583; internal/runtime/vm/record.go:200-216.
- **Design:** A run recorded asleep is adopted without attaching (`recoverAsleep`); a VM left running or frozen is saved as it stands, or woken if the guard marked it. internal/scheduler/sleep.go:914-945; internal/scheduler/vm_freeze.go:198-216.
- **Design:** The VM engine's `lingering` re-arms the power-off of every VM that exited and lingers; the egress gateway call at start rebuilds the VM firewall. internal/runtime/vm/lifecycle.go:209-226; internal/runtime/vm/vm.go:288-297.
- **Design:** On a server restart the guest's streams reconnect: the helper client redials by itself; the keeper and the agent are adopted. internal/runtime/vm/engine.go:146-160; docs/failure-handling.md:17.
- **Design:** An update is rolled back if a session has not finished recovery 5 minutes after the database migration. A VM's monitor runs in its own transient unit "outside the server's process tree, so a VM outlives a server restart or update". docs/failure-handling.md:40; internal/runtime/vm/unit.go:20-21.
- **Not established:** How long recovery takes per session. The server logs `scheduler: recovery: session done run=<id> took=<duration>`, but no source quotes a typical duration. Looked for: docs/failure-handling.md:40 and the PR descriptions #1140 to #1172.

### What marks a session lost

- **Design:** No sidecar for an active run: `interrupt`, reason `server restarted`. internal/scheduler/recovery.go:1408-1412, 1371-1398.
- **Design:** `Inspect` says the container is gone: `didNotSurvive`, which preserves the work as `wip: <task>` and ends the run `interrupted` with `server restarted`. internal/scheduler/recovery.go:1477-1479, 1624-1665; docs/failure-handling.md:18.
- **Design:** An exit code above 128 with no kill, report or usage limit (255 is the VM's `lostExitCode`, 137 `killedExitCode`) also takes `didNotSurvive`. internal/scheduler/recovery.go:1502-1507; internal/runtime/vm/lifecycle.go:30-37.
- **Design:** A guest that restarts on its own ends its VM (`the guest restarted on its own, which ends its VM`). internal/runtime/vm/lifecycle.go:306-343.
- **Design:** A launch caught between row and container: `recoverUnstarted` looks the container up by creation key and destroys it, or interrupts the run. internal/scheduler/recovery.go:1298-1360.
- **Design:** The host's OOM killer ending a VM's hypervisor ends its session: "the session VM holding the most memory, whose whole session ends". docs/failure-handling.md:527-531.

### Every place that assumes the runs in the database are on this host

This is the list asked for. Each entry reads every run (or every active run) from the shared database and acts on this host's runtime, files or processes, or treats a missing local container or file as "gone".

1. internal/scheduler/recovery.go:689, 702-725. `recoverRuns` takes every `ListActiveRuns` row and recovers it here; there is no host filter.
2. internal/scheduler/recovery.go:1408-1412. A run whose sidecar file is not in this host's `<data dir>/scheduler/` is `interrupt`ed (`server restarted`).
3. internal/scheduler/recovery.go:1477-1479 and 1624-1665. A run whose container this runtime cannot find is `didNotSurvive`: work preserved as `wip`, run `interrupted`.
4. internal/scheduler/recovery.go:1298-1360. `recoverUnstarted` asks this runtime `FindByCreationKey` and otherwise interrupts the run.
5. internal/scheduler/recovery.go:748-760. `cleanupTerminalSidecars` reads this host's sidecar directory.
6. internal/scheduler/recovery.go:955-1034. `cleanupTerminalCreationKeyContainers` walks every workspace's every terminal run and, for one with no sidecar here, looks for a leftover container on this runtime and destroys it.
7. internal/scheduler/supervise.go:746-756, 771-790. The checkout sweep marks `a.Worktree` of every active run in use and deletes finished runs' checkouts by the absolute `runs.worktree` path on this host's disk.
8. internal/scheduler/scratch.go:90-98. The scratch sweep treats a directory under `<data dir>/scratch`, `account-config` or `scheduler/vm-databases` as an orphan when no active run owns it.
9. internal/coord/socket.go:269-285. The coordination sweep lists `<data dir>/coord/` and compares with `ListActiveRuns`.
10. internal/adapter/manager.go:86-96. `scan` taps every running run found in the store.
11. internal/scheduler/account_revoke.go:94-100. `leaveRevokedAccounts` lists every active run but acts only when `s.runs[run.ID]` exists here; it skips the rest.
12. internal/scheduler/capacity.go:309-387 with internal/runtime/docker_capacity.go:97-125. Admission measures this host's memory, disks and Docker; it knows nothing about runs elsewhere.
13. internal/runtime/docker_disk.go:179-211. Docker capacity is refused unless the daemon is a local socket whose `engine-id` matches.
14. internal/scheduler/oomwatch.go:54-100. OOM kills are found by reading the host cgroup of each container's init PID (`InitPIDRuntime`).
15. internal/sshd/forward.go:74; internal/scheduler/scheduler.go:980-990. Port forwards dial `Runtime.ContainerIP`, a bridge address on this host.
16. internal/scheduler/launch.go:477-481, 548. `createCheckouts` and `StartDiffWatch` run in this server's git engine and checkouts directory.
17. internal/server/server.go:314-328. One database file, one runtime, one `ReposDir`, `CheckoutsDir`, `homes`.
18. internal/scheduler/idle_close.go:40-60. The 7-day idle close lists only runs in this process's `s.runs`.
19. internal/children/dispatch.go:35-48; internal/store/children.go:90-96. The per-parent `max_live` slot count is computed from the database across all hosts; it is host-neutral, but the launch it triggers goes to this runtime.
20. internal/scheduler/state.go:591-599. Sidecar and terminal-sidecar paths are under this host's `StateDir`.

The list is from reading the files named; it is not exhaustive for the whole repository (for example the dashboard's `Insights` views, sshd handlers that attach to a session's PTY, and `internal/gitengine` were not walked).

## 7. Session identity and ownership

- **Design:** There is no `host`, `node`, `runtime` or `server_id` column or field for a run. `grep -i "node_id|server_id|host_id|NodeID"` finds `ServerID` only in the edge protocol (edge enrollment, member invitations), and `NodeID` only in the browser and in the guest file service. internal/protocol/edge.go:12; internal/protocol/identity.go:163; internal/browser/validate.go:67; internal/guest/home/server_linux.go:221.
- **Design:** The `runs` table as created (migration at internal/store/migrate.go:442-458): `id`, `workspace_id`, `member_id`, `task`, `harness`, `mode`, `status`, `reason`, `branch`, `worktree`, `protected`, `profile_snapshot_id`, `tool_snapshot_id` (dropped later), `created_at`, `started_at`, `finished_at`.
- **Design:** Columns added by later migrations (`ALTER TABLE runs ADD COLUMN`, internal/store/migrate.go, lines 129-2668): `title`, `last_commit`, `last_commit_at`, `harness_session_id`, `account_member_id`, `base_commit`, `base_branch`, `base_source`, `base_checked_at`, `archived_at`, `home_member_id`, `outcome_unseen`, `acp`, `finish_unopened`, `status_changed_at`, `model`, `requested_account_member_id`, `fallback_disabled`, `fallback_order`, `activity`, `status_seq`, `review`, `review_changed_at`, `via`, `children`, `children_set_by`, `cpu_limit`, `memory_limit_bytes`, `pids_limit`, `effort`, `permission`, `refused_model`, `model_error`, `linear_issue_url` (and `child_refused`, `swarm_run` added then dropped at migrations on lines 2285 and 2393). The columns the code reads are `runCols`, internal/store/runs.go:187-193.
- **Design:** `domain.Run` has `Worktree string`, "the server-side path of the run's git worktree", and no host field. internal/domain/domain.go:814-870.
- **Design:** The only per-run link to a runtime is outside the database: the sidecar's `container_id` and the creation key (= run ID) the runtime holds. internal/scheduler/state.go:421; internal/runtime/runtime.go:281-285.
- **Design:** `member_terminals` stores `member_id`, `container_id`, `image`, `started_at` with no host. internal/store/migrate.go:608-613.
- **Design:** `run_children` has `parent_run_id`, `root_run_id`, `tree_key`, `workspace_id`, `owner_member_id`, `state` with no host. internal/store/migrate.go:2024-2051.
- **Design:** The schema version is `len(migrations)`, `SchemaVersion()`. internal/store/migrate.go:2715.
- **Design:** The edge's `ServerID` identifies a server to the edge relay, and one server is one `ServerID`. internal/protocol/edge.go:8-14; docs/networking.md:3-20.

## 8. Network of a session

### Server reaches the session

- **Design (VM):** The host talks to the guest helper over Cloud Hypervisor's vsock: `--vsock cid=3,socket=<data dir>/vm/<run id>/vsock.sock`; the helper API is vsock port 1024 and `engine.helper` dials `<dir>/vsock.sock`. internal/runtime/vm/engine.go:148-160, 532-533; internal/runtime/vm/share.go:22.
- **Design (VM):** Cloud Hypervisor's API socket is `<data dir>/vm/<run id>/api.sock`. internal/runtime/vm/engine.go:532; internal/runtime/vm/record.go:19.
- **Design (VM):** Each VM has one tap `aethervm<n>` (`aethervme<n>` with egress control) on a `/30` out of `10.200.0.0/16`: `.1` host end, `.2` guest. Index 0 is the proxy. The VM's recorded index is `net`; `ContainerIP` returns the guest's `.2` address. A product constant, not a deployment address. internal/runtime/vm/network.go:23-78; internal/runtime/vm/lifecycle.go:596-606.
- **Design:** SSH port forwards and `cloudfield forward` dial `ContainerIP` plus the port from the server host. internal/sshd/forward.go:53, 74; internal/scheduler/scheduler.go:980-990.
- **Design:** Served guest sockets are reached through vsock with `CONNECT <port>` and a zero byte; the dictation engine uses it. docs/vm-guest.md:329-338.
- **Design:** The development browser runs inside the session VM and is reached through a socket the helper serves. docs/environments.md:510-518; internal/runtime/vm/engine.go:497-499.
- **Design:** Docker sessions are reached through Docker's own API (exec, attach) and the container's bridge IP. internal/runtime/runtime.go:447-481.

### Session reaches the server

- **Design:** The coordination socket is `/run/aether/coord3.sock` in the session (`MountDir` plus `SocketName`), backed by the host directory `<data dir>/coord/<run id>/`. internal/coordtransport/paths.go:7-35; internal/server/svc_coord.go:23-34.
- **Design:** The signing agent socket is `/run/aether-signing/agent.sock`, backed by `<data dir>/sign/<member>/`. internal/coordtransport/paths.go:16-20; internal/signing/agent.go:41-59.
- **Design:** The server binary is bound read-only into the session at `/opt/aether/aether-server` (it serves the MCP and CLI entries). internal/coordtransport/paths.go:21-23; internal/scheduler/coordination.go:210-215.
- **Design (VM):** A Unix socket cannot cross virtio-fs or the home file service, so the helper listens on the path in the guest and relays each connection to vsock CID 2; Cloud Hypervisor delivers port N to the host socket `<vsock socket>_<N>`, which the server points at the real socket. The kernel line has `aether.sock=<port>,<path>`. docs/vm-guest.md:123, 305-327; internal/runtime/vm/share.go:22-55, 145-.
- **Design (VM):** The egress proxy is TCP `10.200.0.1:3128` (`proxyAddr`, `egress.Port = 3128`) on the dummy link `aether-vmproxy`; the proxy identifies a VM by its source address. A session with egress control reaches only that. internal/runtime/vm/network.go:30-42; internal/egress/hosts.go:16; internal/egress/proxy.go:127-137; docs/environments.md:445-449.
- **Design (VM):** DNS goes to `10.200.0.1`, where the server relays to the first three servers in the host's `/etc/resolv.conf`. internal/runtime/vm/dns.go; docs/environments.md:431-444.
- **Design (VM):** The nftables table `inet aether_vm` (file `<data dir>/vm/firewall.nft`), an `iptables` rule at the top of `FORWARD` and `INPUT`, and an `oifname` set of the host's LAN and VPN links decide where VM traffic goes. docs/environments.md:450-480.
- **Design (VM):** The home, login files and other host paths reach the guest through the file service `home.sock`, a vsock service the monitor runs on the host side. internal/runtime/vm/share.go:105-125; docs/vm-guest.md:212-260.
- **Design:** Clients (CLI, dashboard) reach the server on one SSH port, `:2222` by default; sessions' terminals, git and control travel over that connection. docs/networking.md:3-7.
- **Design:** Host-local things a session depends on: the Unix socket paths above, the bridge/tap addresses, the host resolver, the host's firewall tables, `aether.slice`, `/dev/kvm`. docs/environments.md:341-349, 419-480.

## 9. Launch steps and their measured times

The step names and order are #1158's log line (open PR, not in the checkout); times are from the sources named, none from a production launch of a whole session.

### Order of a launch (session VM)

1. `admission_ms`: request to reserved capacity. PR #1158 docs table.
2. `base_ms`: fetch of the repository mirror(s) and pick of the base commit. PR #1158; internal/scheduler/launch.go:342.
3. `checkout_ms`: create the checkout and branch. PR #1158; internal/scheduler/launch.go:477.
4. `cache_lock_ms`: record the checkout, wait for another launch using the same member's home or login. PR #1158; internal/scheduler/launch.go:493.
5. `plan_ms`: environment image, mounts, variables, the agent's files. PR #1158.
6. `image_ms`, `disks_ms`, `checkout_disk_ms`, `prepare_ms`: VM image, root/data/cache disks, checkout disk, state disk and record. PR #1158.
7. `helper_disk_ms`, `network_ms`, `unit_ms`, `hypervisor_ms`: helper disk, tap and firewall, `systemd-run`, Cloud Hypervisor's API socket. PR #1158.
8. `guest_boot_ms`, `guest_setup_ms`, `helper_boot_ms`: guest kernel boot to the helper answering, user and git setup, the setup script and main process. PR #1158.
9. `agent_ms`: attach to the agent; for Enhanced until the conversation is open. PR #1158.
10. Wake steps: `wait_for_save_ms`, `admission_ms`, `network_ms`, `unit_ms`, `hypervisor_ms`, `restore_ms`, `resume_ms`, `helper_ms`, `saved_state_ms`, `reattach_ms`. PR #1158.

### Numbers (Measured unless said)

- **Measured:** engine steps of one launch, hosted KVM run 45902107406, the test calls the engine directly: total 1279.5 ms; image 0.1; disks 46.9; checkout_disk 0; prepare 7.8; helper_disk 463.8; network 27; unit 44.3; hypervisor 10.6; guest_boot 649.5; guest_setup 0; helper_boot 28.6. PR #1158.
- **Measured:** the same run, a wake: total 310.9 ms; network 23.4; unit 39.8; hypervisor 11.5; restore 197.3; resume 3.5; helper 25.1. PR #1158.
- **Measured:** hashing the 76 MB test binary for the helper disk on every start: 49.6 to 52.1 ms and 55.2 to 55.7 ms (runs 45802613661, 45803785978; n=27, n=9). Rebuilding the helper disk after a sweep: 168 to 174 ms (n=9). #1154 removes both; the saving is not measured. PR #1154.
- **Measured:** wait for the helper after it listens, with the gRPC backoff: 321 ms median (run 45796381349, n=8); 70 ms median, p90 284 ms (run 45800596034). Six boots with the redial (#1156): the redialing client answered 872 to 953 ms after start began, the backoff client 957 to 1534 ms, backoff cost median 550 ms, min 21, max 581 (run 45911901017). PR #1156. The whole start did not move at the median in the first variant (1922 vs 1929 ms).
- **Measured:** waiting for dockerd in `Boot`: 1012 ms median of a 2951 ms engine launch (run 45800596034, n=8); 1101 to 1148 ms (run 45796381349). After #1161, merged: `Boot` 68 and 86 ms; `docker info` answered 1.16 and 1.43 s after `Boot`; an engine launch returned after 1.186 s and `docker info` as uid 1000 was answered 2.959 s after the start began. Starting dockerd costs about 970 ms of CPU. PR #1161.
- **Measured:** destroy 231 to 470 ms (n=41); power-off poll 200 ms to 25 ms: median 226 ms to 184 ms (n=6 each, run 45904472766). PR #1162.
- **Measured:** the restore deleted the saved state inside the wake, 194 ms in cache and 207 ms cold; `awaitRestored` polled at 200 ms. #1163, merged, moves the delete 19 to 71 ms after the wake returns; restore of 1.3 GiB median 3463 ms before, 3371 and 3452 after (different runners). PR #1163.
- **Measured:** upstream fetch under the server-wide authorization lock: `git fetch` 183, 194, 114, 123 ms; `gh api user` 251 ms; 0.3 to 0.45 s of lock hold per launch (a development host on 2026-11-26). PR #1164.
- **Measured:** prepared checkout disk build 1.2 s for this repository (clone 78 ms, checkout 205, chown 7, mkfs 869; a launch trace not in the repo); VM create 640 ms with a 2,000-file prepared disk built in it; 8.8 s for a 40,000-file repo; 86 ms with the kept disk (#1167). PR #1167; docs/vm-guest.md:607-609 (1.7 s and 17 ms).
- **Measured:** VM start median 1.76 s on 4 KiB pages, 774 ms with huge pages; unit CPU 999 against 219 ms; ACP adapter spawn to `session/new` 2392 against 1243 ms (n=10). PR #1172.
- **Measured:** pause 20.2 ms (frozen session) or 9.4 ms (stopped vCPUs); resume 9.4 and 9.7 ms; resume to the first command 42 and 29.5 ms (47.3 and 40.5 in a second run); saved paused 1.96 s and 1.55 s; restored and resumed 425 and 376 ms; guest clock behind the host after 5 s paused 0.03 s and 0.00 s (run 45902294894). The pause call took 77 ms and 12.1 s in run 45926388941 with a page cache to give back. PR #1159.
- **Measured:** a message to a sleeping session waited for the save: save median 27 s (run 45796405210); 24.0 s of 26.8 s was the wait for freed pages (#1160). PR #1159, #1160.
- **Measured:** a message to a sleeping Claude Code or Codex session running a dev server and a `docker compose` stack was accepted 0.7 to 1.5 s after it was sent; a start from disk after a failed restore took 5 s. docs/failure-handling.md:915-919.
- **Measured:** a message to a sleeping Claude Code container session was accepted about 1.6 s after it was sent on an idle host and up to 23 s on a loaded one (real-agent test). docs/failure-handling.md:817-820.
- **Measured:** Docker sessions: the ownership pass cost 2.8 s and 3.2 s per launch over a 188k-entry home, 0.47 and 0.55 s after #683. PR #1130 `2a-subsession-cost-limits.md`:14.
- **Measured (#1155):** 1.18 s to the first command with the saved memory in the page cache, 2.80 s from disk, for a 2 GiB VM holding 600 MiB. PR #1155.
- **Design:** The boot timeout is 90 s (`bootTimeout`); the restore load gets 60 s plus 1 s per 20 MiB of unit cap; a wake 3 minutes plus 1 s per 20 MiB of saved memory. internal/runtime/vm/engine.go:57; internal/runtime/vm/save.go:22; internal/scheduler/sleep.go:34-37.
- **Not established:** An end-to-end launch time from request to `running` on either runtime. PR #1130's file says "No doc or test reports one"; #1156 says its production-host figure "is not quoted as a number here"; #1158 only logs per-step times. Looked for: docs, PR #1130 `2a-subsession-cost-limits.md`:78, PRs #1154 to #1172.
- **Not established:** Any step time from a production server for the scheduler's own steps: "have not been seen on a running server". PR #1158 ("Verification").

## 10. What happened on 2026-11-26

- **Not established:** Nothing in `docs/`, the commit messages of `main` since 2026-11-24, or the descriptions of PRs #1140 to #1172 describes a memory exhaustion, an eviction, sessions that could not wake, or queued launches on 2026-11-26. Looked for: `git log --since=2026-11-24` bodies for `out of memory|ran out|exhaust|oom|could not wake|queued launch`; `gh pr view` bodies of #1140 to #1172 for the same words; `grep 2026-11-26` over docs. The only 2026-11-26 matches concern the edge's address change, the multiple-logins plan, and a CI flake.
- **Design (context, other dates):** The memory guard's commit says it was built because "the host OOM killer then ended whole VMs, 41 times in a week on <the production host>" and that the guard is "the one-host stand-in for the eviction Borg and Kubernetes do before the kernel OOM killer". 2026-11-18, eff9ce23a (#1054).
- **Design (context, other dates):** The brake's commit: "Three VMs filling 5.5 GiB each at once took 1.4 GiB/s of a 29 GiB host and the kernel killed a VM before the first save finished." 2026-11-18, 44fd0ddda (#1069).
- **Design (context, other dates):** The disk watermark commit: reclaim "never ran" at 93 to 94% because it started only at the reserve plus 4 GiB, "about 96% full" on the production host; "Open and Needs Attention sessions never expired, and held 53 GiB there". 2026-11-18, ff1c935d4 (#1048).
- **Design (context):** #1159's description says: "This must not significantly raise host memory use", under the heading "The owner's memory constraint", and lists the four measures taken. PR #1159.
- **Design (context):** #1164's measurements are from "the development host on 2026-11-26" and refer to a launch trace (`02-launch-trace.md`) that is not in the repository. PR #1164; PR #1156 ("the program's launch trace"); PR #1167.
- **Not established:** The contents of that launch trace, the program it belongs to, and whether any of 2026-11-26's events were captured there. The file is named in PRs #1156, #1164 and #1167 and is not in this checkout (`find research` in this checkout, and `git ls-tree -r 71e7cb661` for the only research commit that mentions performance, show no such file).
