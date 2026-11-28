# Security review: Running Cloudfield on Several Machines

Reviewed: `docs/plans/2026-11-27-multi-machine.md` at `535953ea3` (draft
pull request #1186). Reviewer did not write the plan. Read-only: code was
read and grepped, nothing was built or run.

Each finding gives a severity, the attack, the evidence, and the smallest
change to the plan. **Verified** means read in the code or docs at the
cited line. **Inferred** means a reasoned consequence that nothing here ran.

Severities:

- **Blocks**: the design as written cannot be accepted until this changes.
- **Must fix**: the plan states something false or leaves out a rule the
  build needs. Fix the text before phase 1.
- **Sentence**: true but unstated; one sentence in the plan or a guide.

## Summary, most severe first

| # | Severity | Finding |
| --- | --- | --- |
| 1 | Blocks | On a tailnet, any session VM on a host reaches the primary as the host node's owner, usually the super admin. The host's own link authenticates as that member too |
| 2 | Blocks | Placement trusts host-reported capacity, and machine lists have no default. One compromised host can pull every new session to itself, with every member's login and every workspace's secrets |
| 3 | Must fix | "A member can name the machines their sessions may use; their logins then reach no other machine" is false. Shared accounts, sub-sessions, usage fallback, revoke moves and the omp broker all serve a login its owner never placed |
| 4 | Must fix | Stream rights follow placement, which is never rewritten. They do not follow liveness. Home, signing, coordination and broker streams stay open to a host after the session ends |
| 5 | Must fix | The primary must map each VM's vsock port to a socket from its own plan. Any path the host names reaches any socket on the primary |
| 6 | Must fix | Moving the home file service onto the primary drops its sandbox and its per-VM bounds. One host can exhaust the primary's file descriptors, and only `openat2` stands between it and `aether.db` |
| 7 | Must fix | "What a compromised host gets" leaves things out. Its write access to the home reaches every future session of that member and the member's environment container on the primary. The gh token can plant a signing key that outlives removal |
| 8 | Must fix | "At rest a host holds no home and no key" is false. Workspace secrets sit in `vm.json`, login copies on state disks, tokens in saved memory and swap, and full workspace clones in prepared disks |
| 9 | Must fix | "Git fetches of the base commits only" cannot be done with the existing transport. It serves every session branch of the workspace |
| 10 | Must fix | Join: minting moves from the console to a remote call, nothing compares fingerprints before a host is turned on, a malicious link can enrol a machine, and a former server can join with its old homes and keys still on disk |
| 11 | Must fix | Host key lifecycle: whoever holds a stolen key wins once the real link drops. There is no rotation, and removing a host revokes no credential |
| 12 | Must fix | The host role shares the members' SSH port, but the plan sets no rules for keeping the roles apart, no allowed channels and no per-host caps |
| 13 | Must fix | The freeze and "declare lost" depend on the host cooperating; they do not control it. A stale twin keeps the login and gh token, and one refresh can sign the member out everywhere |
| 14 | Must fix | A host can exhaust the primary's disk, CPU and member signing slots. Limits are per session, and a host multiplies them by its placed sessions |
| 15 | Sentence | Everything recorded about a host's session is attested only by that host: transcripts, exit codes, evidence, costs. So are host-call replies such as memory use |
| 16 | Sentence | Sessions on a host reach that host's own services, its LAN and its tailnet peers |
| 17 | Sentence | The signing agent signs any payload, not only commits |
| 18 | Sentence | A host can reach sessions on other machines through mail, team memory, `child.start` and the shared repository |
| 19 | Sentence | Workspace admins and machine lists: who sets them, and naming a machine at launch |
| 20 | Sentence | Phase 1 already lets a member's personal machine join with a super admin's code. Root on a host is equivalent to super admin for the sessions placed there |
| 21 | Sentence | Versions: a host's reported release is not a control, hosts can update ahead of the primary, and raising the link minimum freezes VMs |
| 22 | Sentence | Privacy and audit: `privacy.md` promises data stays "on your server", and `member remove` erases the home. Hosts break both |

## Findings

### 1. On a tailnet, a session VM on a host signs in to the primary as the host node's owner

**Severity: blocks.** Phase 1 names "over a tailnet" as a supported link path
(plan line 236).

Attack:

1. A super admin joins machine two, a node on their own tailnet that is not
   tagged. That is the default for a personal tailnet.
2. Any member launches a Default or Root session that lands on machine two.
   Egress control is off by default (`docs/security.md:27`).
3. The agent, or a prompt that steers it, runs `ssh -p 2222 <primary
   tailnet IP>` with no key. It can find the address by scanning
   100.64.0.0/10 on port 2222. Or it opens `https://<primary tailnet
   name>:<web-port>`.
4. The host forwards the VM's packet out of `tailscale0` and masquerades it
   to the host's tailnet address. The primary's `tailscaled` WhoIs answers
   with the host node's owner, and the primary signs the session in as that
   member with no credential. With the dashboard listener the session gets
   the owner's dashboard.
5. If that member is the super admin, any session on the host now holds
   super admin on the primary: every workspace, members, roles, `server
   update`, and running code on the server's Docker daemon
   (`docs/security.md:1291-1293`).

Second effect, same cause: the host agent's own link. With tailnet sign-in
on, the primary sets `NoClientAuth` (`internal/sshd/server.go:324-327`), and
Go's SSH client always tries `none` first
(`golang.org/x/crypto@v0.57.0/ssh/client_auth.go:79`). So a link dialled
over the tailnet is signed in as a member before it presents the host key.

Evidence:

- Verified: an open VM reaches tailnet peers by design, through the uplinks,
  masqueraded (`internal/runtime/vm/network.go:162-165`, `199-209`).
- Verified: only 100.100.100.100 is dropped, not the primary's address
  (`internal/runtime/vm/network.go:179`).
- Verified: tailnet sign-in maps any untagged node to its owner's member
  (`internal/sshd/whois.go:127-157`).
- Verified: the documented boundary is "any process on the server host that
  connects to the host's own tailnet address is served as the node's
  owner ... SSH on port `2222` has the same boundary"
  (`docs/security.md:744-748`). On a host, every session VM is such a
  process for the primary's address.
- Verified: egress-limited sessions are safe, because the proxy refuses
  100.64.0.0/10 (`internal/egress/proxy.go:56-58`, `431-432`).
- Inferred: today a VM on the primary that dials the primary's own tailnet
  address arrives from its 10.200.x address, which WhoIs does not resolve.
  So the hole is new with hosts. The same hole already exists for two
  separate servers on one tailnet, shape (c), and should be stated there
  too.

Smallest change to the plan:

- A host must be a tagged tailnet node; `tailscaled` refuses tagged nodes in
  WhoIs.
- The primary also refuses tailnet identity, on SSH and on `web-port`, from
  the address of any joined host.
- Each host drops VM traffic to every address of the primary, whatever the
  workspace's egress setting.
- The link signs in under a fixed SSH user, such as `aether-host`. The
  `none` callback refuses that user, and the public-key callback checks it
  only against host keys.

### 2. Placement trusts host-reported capacity, and machine lists have no default

**Severity: blocks**, because decision 2 is framed on this claim.

The plan says "A host gets it only for the sessions placed on it" (line
721). Placement is computed from what the host reports, and the host also
answers its own reserve call (lines 297-316).

Attack:

1. Root on any turned-on host, such as a member's workstation a super admin
   joined, or a host taken over, patches the host agent. It reports
   unlimited `MemAvailable`, no stall, and plenty of memory the guard could
   free. It accepts every reserve call.
2. Placement filters by "allowed for the workspace" and scores by memory, so
   every new session and sub-session in every workspace that allows the
   host lands there. The plan does not say whether a list is allow-all
   until someone sets it. Read as allow-all, that is every workspace.
3. Over days the host collects the home of every member who launches, every
   account owner whose login is used (finding 3), and every workspace's
   secrets and source. The decision-2 trade-off ("a compromised host can
   take the real login of anyone whose session ran there") becomes "of
   everyone" without anyone noticing.

Evidence:

- Verified in the plan: the inputs are self-reported (lines 297-301), and
  the host's own admission decides the reserve (lines 312-316).
- Inferred: nothing in the plan bounds how many sessions one host may hold.
- E4 B7 cites Kubernetes' NodeRestriction: a kubelet "cannot update or
  remove its taints". That is the part of the node rules the plan lacks. A
  node must not be able to make itself more attractive.

Smallest change to the plan:

- Lists default to deny. A login is served to a host only after its account
  owner allows that host. Secrets and source reach a host only after a
  workspace admin allows it.
- A super admin sets a cap of live sessions per host, shown in
  **Settings → Machines**.
- Restate decision 2 with the honest comparison:

| | Served login, default allow | Served login, default deny | Per-machine sign-in |
| --- | --- | --- | --- |
| Logins a compromised host takes | Every member's, given time | Those of members who opted in | Those of members who signed in there |
| Who decides | Nobody | Each account owner | Each member |
| Revoke one machine | Every exposed member signs out everywhere | Same, for fewer members | Sign out that machine's login only |

  Per-machine sign-in limits damage by construction, at the cost of the
  latency and the remote home manager the plan names (line 579). Served
  logins with default deny come close to it. With default allow they do
  not.

### 3. Per-member machine lists do not cover the ways a login reaches a VM

**Severity: must fix.** Plan lines 737-738: "A member can name the machines
their sessions may use. Their logins then reach no other machine."

A login reaches a session on another member's account in five ways that the
plan's "their sessions" does not cover:

1. **Account sharing.** A recipient's session mounts the owner's login file
   read-write (`docs/security.md:328-333`). The recipient launched it, so
   the recipient's list applies.
2. **Sub-sessions.** A recipient's agent may start a sub-session on the
   owner's account (`docs/security.md:323-326`). `child.start` takes an
   account (`internal/children/agent.go:160-161`). It is admitted if the
   account is shared with the parent's owner (`agent.go:228-230`), then
   placed by score.
3. **Usage fallback.** Fallback moves a live session onto a shared account
   "without them picking it" (`docs/security.md:439-441`). Placement is
   never rewritten (plan line 647), so the owner's login is served to
   whatever machine the session is already on.
4. **Revoke moves.** Revoking a share moves each live session to "another
   account they may use" (`docs/security.md:416-421`). The machine stays
   the same.
5. **The omp broker.** "Every VM, sessions and the member's environment
   terminal alike, reaches the broker" (`docs/harnesses.md:746-748`). A
   claude session of an account therefore exposes that account's omp access
   tokens and API keys too.

Attack chain, with finding 2 or simply by luck:

1. Alice shares her account with Bob. Alice lists only the primary.
2. Bob's session lands on compromised host H.
3. H, speaking on the coordination socket of Bob's session, calls
   `child.start` with account Alice.
4. The child is placed by score on H, and H reads Alice's login through the
   file service.

Smallest change to the plan:

- The list belongs to the owner of every login a session would mount. That
  is the account owner, and the omp broker's account.
- Placement checks it, and so does every account move: fallback, revoke and
  #1149's login moves.
- A move onto an account that does not allow the session's machine is
  refused with a stated reason and ends the session as a failed move does
  today. It never serves the login.

### 4. Stream rights follow placement, not liveness

**Severity: must fix.** Plan line 441: "the primary accepts that only for VMs
placed on that host". Line 647: placement "is never rewritten". Line 718:
signing "while the session lives".

Attack:

1. A session on H finishes, or is closed, sleeps, or is retained.
2. H keeps opening tunnel streams that name that VM: the file service, the
   signing socket, coordination, the omp broker. The VM is still "placed on
   that host", so the plan's rule admits them.
3. Signing is per member, not per session. The primary keeps one listener
   per member for its whole life (`internal/signing/agent.go:41-80`,
   `maxConns`/`idleTimeout` at `27-28`). A stream routed by placement alone
   signs as that member for as long as H stays joined.

Evidence: verified that the signing listener is per member and the key is
read on every request (`internal/signing/agent.go:37-40`, `175-189`). The
rest is inferred from the plan's text.

Smallest change to the plan: the primary admits a stream only while it has
booted that VM and the session is live, meaning not ended, not saved and not
declared lost. It closes every stream of a session within seconds of its
end, of a share revoke, or of the host's removal. "While the session lives"
then becomes a rule the primary enforces.

### 5. The port-to-socket map must come from the primary

**Severity: must fix**, as a stated rule.

Today the server computes each VM's map from vsock port to host socket
(`internal/runtime/vm/share.go:47-53`). It trusts only sockets in
root-owned directories, because "Cloud Hypervisor connects by path as root,
so a socket a guest could replace with a symlink would reach any host
socket" (`share.go:44-47`, `rootOnly` at `121-128`). Verified.

Attack: if a tunnel stream names a port and the host agent supplies or
influences the path, H can reach any Unix socket the primary's process can
open. That includes Docker's socket, another member's signing agent,
another account's omp broker, keeper sockets under `/run/aether-acp`, and
the dictation engine.

Smallest change to the plan: the host names only a VM and a port. The
primary looks the port up in the plan it made for that VM and refuses any
port outside it.

### 6. The file service on the primary loses its sandbox and its bounds

**Severity: must fix.** Plan line 701: "The home file service the primary
runs for a VM ... as the per-VM service does today."

Today the service is the VM's monitor, running in the VM's own systemd unit
(`internal/runtime/vm/monitor_linux.go:28-33`). That unit:

- hides the whole data directory under a tmpfs and binds back only that
  VM's paths (`docs/security.md:78-88`);
- has its own `MemoryMax` (`docs/vm-guest.md:257-258`);
- has its own file-descriptor table.

The service runs as root with `CAP_DAC_OVERRIDE` (`docs/security.md:89-94`)
and allows 16,384 open handles, 128 streams and 16 MiB replies per VM
(`internal/guest/home/server_linux.go:24-39`). All verified.

Attack, if the primary runs it in-process:

1. H opens 16,384 handles for each of its VMs. They count against the
   primary's own file-descriptor limit. The primary then cannot open
   `aether.db`, accept SSH or write transcripts, and every machine stalls.
2. Path resolution with `openat2` and `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS`
   (`server_linux.go:139-165`) becomes the only barrier between a remote
   party and `aether.db`, `signing-keys/` and every home. Today a flaw there
   is contained by the unit's mount namespace.

Smallest change to the plan: run one sandboxed unit per remote VM on the
primary, built like today's monitor unit: tmpfs over the data directory,
only that session's shares revealed, `MemoryMax`, and its own process. Its
socket is spliced into the tunnel.

### 7. "What a compromised host gets" is incomplete

**Severity: must fix.** Plan lines 704-724.

What the plan leaves out:

- **The whole home, read and write.** A Default or Root session's home share
  is the member's whole home (`docs/security.md:145`, `194-206`). So the
  host gets every agent's login, not only the session's agent, plus
  `~/.ssh`, terminal uploads and any client key a member left there
  (`docs/security.md:654-656`).
- **Lateral movement.** Write access lets H plant `~/.claude/settings.json`
  hooks, `.bashrc` or `~/.local/bin/gh`, which runs on the member's next
  `cloudfield github connect` (`docs/security.md:503-504`). Those run in
  every future session of that member on every machine. They also run in
  the member's environment terminal, a container on the primary that shares
  its kernel (plan lines 781-782). "It does not get ... sessions on other
  machines" (line 722) is false through the home.
- **A key that outlives removal.** The gh token carries
  `admin:ssh_signing_key`. H can register a signing key of its own, which
  GitHub shows as Verified after H is removed (`docs/security.md:500-503`).
- **New sessions elsewhere.** H can start sessions on other machines through
  `child.start`, with a task of its choosing, on the parent's or a shared
  account, up to `child_max_live`.
- **omp tokens** of every account of a session placed there (finding 3).

Smallest change to the plan: list each of these. Say that "signs out and in
again" is not enough. The member must revoke at the vendor and at GitHub,
with the steps in `docs/security.md:521-541`, and remove signing keys they
did not add. Finding 11 makes this list part of removal.

### 8. "At rest a host holds no home and no key" is false

**Severity: must fix.** Plan lines 743-746.

What a host keeps on disk:

- **Workspace secrets.** `vm.json` stores the VM's full environment, secret
  values included, and the setup script, so the VM can boot again
  (`internal/runtime/vm/record.go:53-73`; filled at
  `internal/runtime/vm/engine.go:288-290`). It is written with mode 0600 by
  `os.CreateTemp` (`record.go:179-191`). Root on the host reads it until the
  VM is destroyed. Verified. The kernel command line carries nothing secret
  (`docs/vm-guest.md:109-113`), verified.
- **Login copies.** A `copilot` or `antigravity` session gets a copy of the
  login at each cold start, seeded from the home onto the VM's state disk
  (`docs/vm-guest.md:127`, `internal/runtime/vm/share.go:71-72`,
  `docs/security.md:351-354`). State disks outlive the VM
  (`docs/vm-guest.md:140`). Verified.
- **Tokens in memory images.** Saved memory, including VMs that #1159 saves
  after an hour, and the guest swap file on the data disk hold the agent's
  live access and refresh tokens, the gh token once read, and the secret
  environment. Inferred.
- **Full workspace repositories.** A prepared checkout disk is a full `git
  clone` of the workspace repository, so it holds every session branch of
  every member (`internal/gitengine/vm.go:136-140`). Verified.
- **Saved environments and member caches** copied to the host (plan line
  531).

Smallest change to the plan: list these. Removal destroys them, or else
counts as exposure of every secret, login and repository listed. `member
remove`, deleting a session and deleting a workspace reach the hosts.

### 9. Git fetch scope

**Severity: must fix.** Plan lines 698-699: "Git fetches of the base commits
of sessions placed on it, from those workspaces only."

The existing transport serves `git-upload-pack <workspace>` for the whole
repository (`internal/sshd/session.go:150-205`). The only hidden namespace
is `refs/aether` (`internal/gitengine/engine.go:316-336`). Every
`cloudfield/run-*` branch of every member is advertised, and the prepared
disk clones all of them (finding 8). Verified.

So a host receives the whole repository, including session branches of
members who do not allow that host. The access check is per member today
(`gitAccess`, `session.go:173`); a host is not a member. Nothing in the plan
says `receive-pack` is refused for a host.

Smallest change to the plan: either have the primary build a pack of the
base commit's history only and send it over the link, or state honestly
that a host gets the whole repository of each allowed workspace. Either
way, state that `receive-pack` is refused for host keys.

### 10. Joining

**Severity: must fix.** Plan lines 221-234 and 679-686.

**Minting moves off the console.** The edge claim code is printed by `sudo
cloudfield-server edge claim-code`, and the claimant is trusted "because the
code came from the machine's console" (`docs/edge.md:219-221`, `236-238`).
The join code is created by "a super admin" on the primary, which reads as
a dashboard or CLI call.

- Can a connector mint one, or the assistant, or Slack, or an approval rule?
  A steered assistant on a super admin's account, with auto-approve on,
  could mint one and pass it to an agent-controlled machine.
- Fix: mint only with `sudo cloudfield-server host code` on the primary, or
  refuse the method to connectors, Slack, the assistant's tool socket and
  approval rules, and require an approved device.

**No fingerprint check before turn-on.** The host pins the primary, but the
primary pins whatever key used the code first. The code travels by chat or
email.

1. A thief joins their own machine within the 30 minutes.
2. The real host fails with the code already used.
3. A super admin mints another code and later sees two joined machines,
   both named by what they report about themselves.

- Fix: `host join` prints the host key's fingerprint, the joined row shows
  it, and turning a host on asks for it. A code used by an unexpected key
  is shown as such and removed, not just replaced.

**A malicious link.** `host join '<link>'` trusts whatever primary the link
names. Joining gives that primary VMs with network access on the machine's
LAN and tailnet (finding 16), and an attack surface that runs as root
(Cloud Hypervisor and the monitor; `docs/security.md:57-67`).

- Fix: `host join` shows the primary's name and server id and what joining
  grants, and asks for confirmation.
- Fix: the host-call API names no host path, kernel argument or disk file.
  The host builds the VM configuration itself.
- Fix: state that a host's root trusts the primary's super admins with that
  machine's networks.

**A former server joining.** Machine two may have been a standalone server,
shape (c), with its own `aether.db`, homes, `signing-keys/` and edge
enrolment.

- Fix: `host join` refuses a data directory that holds a database or homes,
  and refuses while an edge is configured.

**What the host can do before turn-on** is unspecified. Every host call and
stream must check "turned on" as well as placement. Until then the link
carries only heartbeats.

### 11. Host key lifecycle and what removal revokes

**Severity: must fix.**

- **Stolen key.** Plan line 640: "Two hosts present one key: the primary
  keeps the link that is open and refuses the second."
  1. Someone takes the host key from a backup or a cloned disk.
  2. They wait for the real link to drop: a reboot, a LAN outage, or
     traffic they cause.
  3. They connect, claim the placed VMs, and receive home and signing
     streams for them (finding 4 limits this to live sessions).
  4. The real host is then the one refused. The start counter (line 638)
     guards against rollback, not cloning, and the host reports it itself.

  Fix: when a key is presented while its link is open, or right after a
  drop with a mismatched counter, close both links, mark the host
  suspected, and require a rejoin under a new key.
- **No rotation.** Add `cloudfield-server host rekey` over the open link.
  Store the key root-only, outside anything a session's unit can reveal.
- **Removal.** Plan line 686: "drops its key and closes its link". It
  revokes no vendor login, gh token, signing key on GitHub or workspace
  secret. Removal should produce a rotation list: each account owner whose
  login was served (finding 3), the gh tokens, the workspaces whose secrets
  were sent, and the steps from finding 7.

### 12. The host role on the members' SSH port

**Severity: must fix**, as a set of rules.

`authenticate` resolves a key in this order: member key, then direct
device, then the fresh-server bootstrap, then invite codes in the user name
(`internal/sshd/server.go:334-392`). Tailnet sign-in runs first as `none`
(finding 1). Edge connections take only the grant's device key
(`internal/sshd/edge.go:62-120`, per E3 §7).

Rules the plan must state:

- A key is unique across members, devices and hosts, in one index. Adding a
  member key that equals a host key is refused, and so is the reverse. A
  join must never fall into the fresh-server bootstrap or invite branches.
- The host role is chosen by its SSH user (finding 1) and refused on the
  edge path and on `none`.
- A host connection gets its own channel handler. It gets no
  `aether-control` and no other member subsystem, no `exec` but the scoped
  fetch of finding 9, no `direct-tcpip` and no global requests. The plan's
  "no access to the control channel" (line 690) should name what is
  refused, not only what is not granted.
- Per-host caps on channels, streams and bytes (finding 14).

### 13. The freeze and "declare lost" depend on the host cooperating

**Severity: must fix**, in how the plan describes them. Plan lines 32-33: "A
session therefore never runs unsupervised and can never run twice."

The host applies the freeze. A compromised host ignores it; the plan admits
a broken one may too (line 662).

The stale twin has:

- the login's access and refresh tokens in agent memory;
- the gh token, if read;
- the egress the host chooses.

Attack, or plain failure:

1. The host is cut off and declared lost, and the session is started again
   elsewhere.
2. The twin refreshes the login with the vendor. Claude Code and Codex
   rotate the refresh token (`docs/security.md:242-244`). The write-back
   cannot reach the primary's home.
3. The primary's copy now holds a revoked refresh token, and the member's
   new session and all their other sessions are signed out. Or the twin
   keeps a working chain the member does not know about.
4. The twin keeps pushing to GitHub with the gh token.

The same happens on an honest host when a refresh is in flight at link
loss and the person then declares the host lost. Inferred from the refresh
behaviour the docs measured.

Smallest change to the plan:

- Call the freeze cooperation, not a control.
- "Declare lost" revokes the host's key unless a person confirms the
  machine is off.
- The "start again elsewhere" dialog lists the credentials that were
  exposed (finding 11).
- Change "can never run twice" to "never runs twice while the host follows
  the protocol".

### 14. A host can exhaust the primary

**Severity: must fix.**

Limits that exist are per session:

- coordination buckets of 30 burst and 1 a second
  (`internal/coord/mailbox.go:39-45`, per E2);
- VM watch bursts of 2 per interval (`internal/gitengine/vm_watch.go:50-61`,
  per E2);
- packs of 128 MiB and 512 MiB for indexing, moved HEADs up to 4 GiB, and a
  128 MiB disk floor shared server-wide (`docs/vm-guest.md:660-694`);
- 16 signing connections per member (`internal/signing/agent.go:27-28`).

A grep found no cap on transcript size in `internal/ptyhost` and no home
quota in `internal/memberhome`. `repos/` is never freed
(`docs/failure-handling.md:303-310`, per E3 §3).

Attack:

1. H forges at full link speed for every placed VM, and pulls more VMs to
   itself (finding 2). It sends git snapshots and 4 GiB HEADs, which are
   published into `repos/`, endless terminal output and home writes.
2. The primary's disk reaches its floor, and every launch, wake, snapshot
   and evidence write fails on every machine.
3. H also holds a member's 16 signing slots, so that member's commits fail
   to sign on every machine.

Today one malicious VM is bounded by its own CPU and 128 GiB disk. A host
is not.

Smallest change to the plan: add per-host caps on concurrent streams,
bytes per day into git and transcripts, and home writes. Reserve disk per
host. Give each remote VM its own signing connection slots.

### 15. Records and host-call replies are attested only by the host

**Severity: sentence.**

Transcripts, exit codes, captures, cost events, evidence trees and the
commits a report rests on come through a protocol the host ends. Root in a
VM can already forge these for its own session. A host forges them for
every session placed there.

The plan calls the guest protocol "unchanged". Its peer was a process on
the primary's own machine and is now another machine's root. SSH with
pinned keys protects transit, not the endpoint.

New, and not true of a guest: host-call replies become untrusted input.
These include `MemoryUse` and `SavedMemory`, read from the unit's cgroup
today (`docs/vm-guest.md:497-510`); freeze state; and the `vm.json` and
`launch.json` files that today are bound read-only so the unit cannot
rewrite them (`docs/security.md:84-88`).

Sentence: "A host's sessions are recorded as that host reports them. The
primary treats every host reply as untrusted input."

### 16. Network blast radius per host

**Severity: sentence.**

For open taps, the VM firewall's input chain accepts by default and forward
lets VMs out through every uplink (`internal/runtime/vm/network.go:186-204`).
On a host that means the host machine's own services, its LAN (perhaps a
different network from the primary's) and its tailnet peers. The masquerade
keeps a privileged source port on that LAN (`docs/security.md:1347-1351`).

Sentence in the plan and in `docs/security.md` Limits: "Sessions reach the
host they run on and its networks, as they reach the server host today."

### 17. The signing agent signs any payload

**Severity: sentence.** `memberAgent.SignWithFlags` signs whatever bytes it
is given, with no `sshsig` namespace check
(`internal/signing/agent.go:199-209`). Verified.

A host can sign commits for any repository, with any author and date, and
push them with the gh token. The plan's "have commits signed as that member
while the session lives" should read "sign any payload with that member's
key while ...". `docs/security.md:251` ("a signature only covers commits in
its own checkout") is already inaccurate today.

### 18. Reaching sessions on other machines

**Severity: sentence.** Plan line 722: "It does not get ... sessions on
other machines."

Through mail to "live sessions its owner may message in any workspace the
owner can reach" (`docs/security.md:1103-1105`), planted team memory notes,
`child.start`, and the shared workspace repository, H can steer sessions it
does not run. That includes sessions on the primary, prompting them to
print their own home.

These are the channels any session has. Add "except through the channels
any session has", so readers do not assume isolation.

### 19. Roles and machine lists

**Severity: sentence**, plus one rule.

The plan does not say who sets a workspace's list. If a workspace admin
can narrow workspace W to host X, and X is a machine that admin
administers, every member's sessions in W go to X.

Today that admin can already steer those sessions and make the agent print
the home (`docs/security.md:300-303`, `docs/teams.md:212`), but each act is
recorded on the timeline, and protected sessions refuse it. A host read is
silent and ignores protection.

Rules:

- Only super admins attach and configure hosts.
- A workspace list may only exclude hosts; it never forces placement onto
  one.
- "Start a session on machine two by naming it" (1c) obeys every list.

### 20. Phase 1 already allows a member's personal machine

**Severity: sentence.** Nothing records who has root on a host, and a
super admin can hand a code to anyone. The "deferred" case therefore exists
from phase 1, with a super admin's consent.

State in **Who can join**: "Root on a host can do to every session placed
there what a super admin can do to it. Join only machines whose
administrators you would make super admin."

### 21. Versions

**Severity: sentence.**

- A host's reported release and link version are its own claims. No
  security check may depend on the negotiated version; the primary enforces
  every rule on its side.
- Hosts self-update on their own schedule from the channel, so a host can
  run ahead of the primary, despite "the primary updates first".
- The host's binary is the VM helper and the source of its kernel hardening
  (`docs/vm-guest.md:79-90`). A host that refuses updates keeps older
  hardening for every VM it starts.
- Raising the link minimum after a security fix freezes the VMs of every
  host not yet updated. Say that this is the path for a security fix.

### 22. Privacy and audit

**Severity: sentence.**

- `docs/privacy.md` says remote-development data is recorded "on your
  server" (line 350) and that "The shared app browser runs on your server"
  (line 372). On a host the browser, its cookies and the checkout are on
  another machine.
- "`cloudfield member remove` ... erases your member home" (lines 621-629)
  leaves login copies, saved memory and caches on hosts (finding 8).
- Add audit history entries for join, turn-on, turn-off, removal and
  declare-lost.
- Record, for each session, which machine served which account's login and
  which workspace's secrets. The launcher is not the login owner on shared
  accounts. Members and account owners should see that list. Removal's list
  (line 746) is built from it.
- Citation error: the plan quotes "what a server holds is its
  administrator's" as `docs/privacy.md:621-629`. The phrase is at line 344.

## Claims in the plan checked and found true

- The kernel command line carries no secrets (`docs/vm-guest.md:109-113`).
- Path resolution in the file service is beneath the share with no symlinks
  (`internal/guest/home/server_linux.go:129-165`).
- The guest gRPC connection has no transport security
  (`internal/guest/client.go:154-156`).
- The egress proxy refuses tailnet addresses for egress-limited sessions
  (`internal/egress/proxy.go:431-432`).

## Not established

- How the host agent's create call will carry mounts, disks and kernel
  arguments. Findings 5 and 10 assume the worst until the plan states it.
- Whether a vendor tolerates one login refreshed from two machines (plan,
  E3 §4). Finding 13 does not depend on it.
- Whether git's protocol v2 lets a host `want` objects that only refs under
  `refs/aether` reach. Finding 9 holds without it.
