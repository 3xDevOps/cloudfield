# Dev environments, CI runner fleets and agent orchestrators

Collector 06 for `docs/plans/2026-11-27-multi-machine.md`. Follows
[COLLECTOR-RULES.md](COLLECTOR-RULES.md). Every claim ends with the URL that
was fetched in this run. WebFetch returns a model-written extract of a page,
so quotes below are as that extract returned them; a claim marked **Not
established** was searched for and not found on the page named.

Marks: **Design** is what the docs or source say the system does.
**Measured** is a number someone observed (an incident count, a sample
command output). Documented limits and defaults are **Design**.

Questions: (1) split, (2) join, (3) streams, (4) assignment, (5) worker lost,
(6) credentials, (7) version skew.

---

## A. Coder (coder/coder)

### (1) Split

- **Design** `coderd` is the control plane, started with `coder server`; "It is a thin API that connects workspaces, provisioners and users." https://coder.com/docs/admin/infrastructure/architecture
- **Design** "`coderd` stores its state in Postgres and is the only component that communicates with Postgres." https://coder.com/docs/admin/infrastructure/architecture
- **Design** A workspace agent is "the Coder process that runs within a user's remote workspace"; it gives `coderd` and clients "a consistent interface" to the workspace. https://coder.com/docs/admin/infrastructure/architecture
- **Design** By default "the control plane runs multiple provisioner daemons"; each coderd replica hosts 3 Terraform provisioners by default. https://coder.com/docs/admin/infrastructure/architecture and https://coder.com/docs/admin/infrastructure/validated-architectures
- **Design** Workspace proxies hold no database: "Workspace proxies do not make any database connections." https://coder.com/docs/admin/workspace-proxies
- **Design** Database latency guidance: "We recommend keeping latency under 10 ms between the control plane and database." https://coder.com/docs/admin/infrastructure/validated-architectures
- **Design** HA: "Coder automatically enters HA mode when multiple instances simultaneously connect to the same Postgres endpoint"; with custom relays "Postgres is the sole rendezvous for the Coder nodes"; embedded-relay replicas need inter-node reachability (`CODER_CLUSTER_HOST`, TCP 6222). https://coder.com/docs/admin/networking/high-availability

### (2) Join

- **Design** External provisioners authenticate with `CODER_URL` plus a key; "Keys are scoped to an organization, and optionally to a specific set of tags"; global PSK is discouraged ("We do not recommend using global PSK"). https://coder.com/docs/admin/provisioners
- **Design** A search over versioned provisioner docs: the daemon is configured with the Coder URL and `CODER_PROVISIONER_DAEMON_KEY` and started with `coder provisioner start`; the search summary calls the outbound direction an inference, not a documented statement. https://coder.com/docs/@v2.15.0/admin/provisioners (search result summary only)
- **Not established:** a sentence stating that provisioners or agents dial out to `coderd`. Looked at https://coder.com/docs/admin/provisioners and https://coder.com/docs/admin/infrastructure/architecture; neither states the direction.
- **Design** Workspace proxy: each gets a token ("the token the workspace proxy will use to authenticate back to primary coderd"); "At no point should two workspace proxy instances share the same authentication token"; it needs port 443 and connectivity to the control plane. https://coder.com/docs/admin/workspace-proxies
- **Design** NAT rule for the control plane: "Workspaces connect to the control plane via its external address, set by ACCESS_URL." and "There must not be a NAT between workspaces and the control plane." https://coder.com/docs/admin/networking
- **Design** "Coder workspaces do not require any ports to be open." https://coder.com/docs/admin/infrastructure/validated-architectures

### (3) Streams

- **Design** "When possible, we establish direct connections between users and workspaces." https://coder.com/docs/admin/networking
- **Design** Dashboard web apps go through the control plane: "The control plane relays dashboard-initiated connections between the user and the workspace"; web terminal may be direct. https://coder.com/docs/admin/networking
- **Design** CLI (`coder ssh`, `coder port-forward`): direct when possible, "When NAT traversal fails, connections are relayed through the control plane"; "All user-workspace connections are end-to-end encrypted." https://coder.com/docs/admin/networking
- **Design** STUN: default is Google's public STUN server, "UDP packets outbound on `udp/3478`". The control plane also runs a built-in DERP relay. https://coder.com/docs/admin/networking
- **Design** If both peers are behind "hard" NAT "direct connections may take longer to establish or will not be established at all"; "peers fallback to DERP connections". https://coder.com/docs/admin/networking/stun
- **Design** Workspace proxies are relays for SSH, apps and port forwarding, "Dashboard connections and API calls ... are not served over workspace proxies"; if a selected proxy goes offline the session falls back to the primary, "could take up to 60 seconds". https://coder.com/docs/admin/workspace-proxies
- **Design** Latency expectation: "better than 400 ms round-trip latency to the control plane and to their workspace". https://coder.com/docs/admin/networking
- **Measured** Sample `coder ping` output on the troubleshooting page shows about 39 to 42 ms per round trip over a DERP region, with "You are connected via a DERP relay, not directly (p2p)." It is example output, not a benchmark. https://coder.com/docs/admin/networking/troubleshooting
- **Not established:** published throughput numbers. Both networking pages say nothing on throughput.

### (4) Assignment

- **Design** Pull model with tags: "A job with no explicit tags can only be run on a provisioner with no explicit tags. If a job has any explicit tags, it can only run on a provisioner with those explicit tags." https://coder.com/docs/admin/provisioners
- **Design** "Each provisioner runs a single concurrent workspace build." https://coder.com/docs/admin/provisioners
- **Design** No provisioner free: the job queues as Pending. Pending behind busy but active provisioners has no maximum wait; "Jobs with no eligible, progressing provisioner fail after 30 minutes" ("Build has been detected as pending for 30 minutes and has been terminated by the reaper."). https://coder.com/docs/admin/provisioners/manage-provisioner-jobs

### (5) Worker lost

- **Design** "A Running job fails when its provisioner sends no updates for 5 minutes." https://coder.com/docs/admin/provisioners/manage-provisioner-jobs
- **Design** Failed jobs are not retried: canceling "does not automatically retry the operation"; the user or admin triggers it again. https://coder.com/docs/admin/provisioners/manage-provisioner-jobs
- **Design** A provisioner is "making progress when it completed a job in the last 30 minutes, or when it is running a job that still sends updates." https://coder.com/docs/admin/provisioners/manage-provisioner-jobs
- **Not established:** what happens to a workspace (running or marked disconnected) when its agent disconnects. Looked at https://coder.com/docs/admin/templates/troubleshooting and a search for agent-disconnect lifecycle docs; only a 2023 issue about a workspace that stayed "running" but disconnected, closed as not reproducible, was found.
- **Not established:** agent heartbeat or `connection_timeout` defaults. The Terraform registry page returned no content.

### (6) Credentials

- **Design** Provisioners "Keep Coder unaware of cloud secrets, manage/rotate secrets on provisioner servers" and avoid "exposing APIs (Docker, Kubernetes, VMware) to the control plane." https://coder.com/docs/admin/provisioners
- **Design** "Unlike parameters, workspace secrets are never sent to the provisioner" and "cannot be read back through the API"; SSH keys "are held in-memory and never written to disk". https://coder.com/docs/admin/security/secrets
- **Not established:** what a compromised agent or provisioner can reach. https://coder.com/docs/admin/security/secrets does not discuss agent tokens or compromise.

### (7) Version skew

- **Not established:** coderd/agent/provisioner compatibility window or agent auto-update. https://coder.com/docs/install/upgrade and https://coder.com/docs/install/upgrade-best-practices cover neither; the first says only "Coder does not support rollbacks."

---

## B. Gitpod classic and Gitpod Flex / Ona

### Classic (Kubernetes)

- **Design** ws-manager (mk2) is a Kubernetes controller: "orchestrates the creation, monitoring, and deletion of workspace pods"; workspace state is `Workspace` custom resources, reconciled; it has a timeout controller. https://raw.githubusercontent.com/gitpod-io/gitpod/main/memory-bank/components/ws-manager-mk2.md (repo file)
- **Design** ws-daemon "runs on each Kubernetes node in the Gitpod cluster", "Manages periodic backups of workspace content" and syncs content to storage backends (Minio, GCloud). https://raw.githubusercontent.com/gitpod-io/gitpod/main/memory-bank/components/ws-daemon.md (repo file)
- **Design** ws-proxy routes by hostname pattern, "Listens on port 2200 for SSH connections", reads workspace info from the Kubernetes CRDs; port URLs follow `<port>-<workspace-id>.ws.<region>.<domain>`. https://raw.githubusercontent.com/gitpod-io/gitpod/main/memory-bank/components/ws-proxy.md (repo file)
- **Design** ws-manager-bridge "subscribes to workspace status updates from the workspace manager" and writes instance state (phase, condition, ports, timestamps) to the database; a bridge controller "Manages the lifecycle of bridges to workspace clusters"; config `CONTROLLER_MAX_DISCONNECT_SECONDS` is "Maximum time to wait for reconnection". https://raw.githubusercontent.com/gitpod-io/gitpod/main/memory-bank/components/ws-manager-bridge.md (repo file)
- **Not established:** cluster registration command, cluster states (available/cordoned/draining) and placement rule. The bridge file does not describe them; a search surfaced a mention of a cluster-state API only through a mirror. Looked at the bridge file above and a web search.
- **Not established:** how a dead workspace is judged dead; the bridge file lists "Periodically check workspace instances" and nothing more.

### Why they left Kubernetes (stateful environments on nodes)

All from https://ona.com/stories/we-are-leaving-kubernetes.

- **Design** State: environments "cannot be moved from one node to another", and many gigabytes of source, caches and data are "costly to migrate".
- **Design** Failure tolerance: developers "don't take kindly to losing any source code changes."
- **Design** CPU: environments "will require several cores within a few 100ms"; the team "can not predict when CPU bandwidth is needed, but only understand when it would have been needed."
- **Design** Noisy neighbours: co-location "incurs significant noisy neighbor effects leading to a detrimental user experience."; VS Code "disconnects because VS Code server is starved for CPU time."
- **Design** Memory: fixed per-environment allocation is "very limiting"; overbooking was "near impossible" until Kubernetes 1.22 swap, which "works well in practice."
- **Design** Storage: local SSD RAID 0 is fast but "the failure of any single disk would result in complete data loss" and ties data to a node; block storage has "considerably lower throughput"; PVCs brought "unpredictable attachment and detachment timing" and "AZ locality constraints".
- **Design** Backup and restore is "an expensive operation"; compressing archives "consumes most available CPU on a node", so they used uncompressed tar to and from S3; without IO limits "other workspaces might starve for IO bandwidth".
- **Design** Startup: minimising start time was the "primary goal" and conflicted with dense packing; hot standby would be "prohibitively expensive".
- **Design** Micro-VM experiment: Firecracker memory snapshots gave "near-instant full machine resume", but "uVMs introduced more overhead than containers"; large memory snapshots "made scheduling and startup harder"; storage was "the crucial element".
- **Design** Verdict: "Kubernetes is not the right choice for building development environments."; "We learned the difference between application and system workloads the hard way."

### Gitpod Flex / Ona runners

- **Design** Split: management plane "Hosted by Ona. Handles coordination, identity, and configuration"; runners "Run in your own infrastructure" for provisioning, source access, secret injection, agent execution, builds. "Your source code and SCM credentials are handled by runners, not the management plane." https://ona.com/docs/ona/understanding/architecture.md
- **Design** The management plane handles "Authentication, org settings, guardrails, coordination"; the overview table answers "No" to "Touches your code"; the runner "clones repositories using credentials stored on the runner, not the management plane" and "delivers secrets into environments at startup". https://ona.com/docs/ona/runners/overview
- **Design** Runner egress allowlist includes `https://app.gitpod.io` and `https://app.ona.com`; "Updates, CLI binaries, and agent components are served from `app.gitpod.io/releases/*`"; blocking its own `/_health` egress "causes the runner to report degraded status." https://ona.com/docs/ona/runners/aws/detailed-access-requirements.md
- **Design** The AWS runner "automatically checks connectivity to all required endpoints on startup and every 5 minutes" and reports "a network readiness status" to the management plane; the ECS cluster has "a proxy for routing traffic to environments". https://ona.com/docs/ona/runners/aws/networking.md
- **Design** Capacity (AWS): small runner "up to 5,000 total environments and 300 running at the same time"; runner service scales 2 to 5 replicas. https://ona.com/docs/ona/runners/aws/capacity-planning
- **Not established:** whether the runner connects outbound only, whether SSH/IDE traffic passes through the management plane, runner auto-update, behaviour when a runner is offline, what happens when capacity is exhausted. Looked at the overview, architecture, networking, detailed-access-requirements and capacity-planning pages above.

---

## C. Daytona (additions only)

- **Design** "Each runner polls the control plane API for jobs and executes sandbox operations"; the sandbox manager "schedules sandboxes onto runners" (no algorithm given); a proxy "routes external traffic to the correct sandbox using host-based routing"; a daemon runs inside each sandbox. https://www.daytona.io/docs/en/architecture/
- **Design** Runner state values (search-result summary of the API, not a fetched docs page): initializing, ready, disabled, decommissioned, unresponsive; endpoints for scheduling status, draining and a runner healthcheck. https://apis.io/apis/daytona/daytona-runners-api/ (search result only; the page returned 404 when fetched)
- **Design** "Runners in draining mode now gracefully migrate sandbox backups to a new runner before decommissioning." https://www.daytona.io/changelog/runner-draining-api-indexing
- **Design** "Sandboxes marked as recoverable on draining runners are now recovered in-place by expanding disk by 5% and resetting state." https://www.daytona.io/changelog/runner-recovery-fuse-optimization
- **Measured** An incident report states that "the degradation was not fully captured by existing runner health checks" and "the sandbox orchestrator continued routing workloads to runners that were not operating at full capacity." https://status.app.daytona.io/incident/809233
- **Not established:** runner registration flow, heartbeat interval, the timeout that marks a runner unresponsive, and what happens to sandboxes on a runner that fails outright. `github.com/daytonaio/daytona` as fetched shows only `libs/` and a README stating "This repository is no longer maintained", so the server source was not readable; `https://www.daytona.io/docs/en/runners` returned 404.

---

## D. DevPod (loft-sh/devpod): no control plane

- **Design** "No need to install a server backend, DevPod runs solely on your computer." https://devpod.sh/docs/what-is-devpod
- **Design** "Devpod uses a client-agent architecture"; "DevPod deploys an agent to the machine running the container as well as to the container itself" (port forwarding, credential forwarding, log streaming). https://devpod.sh/docs/how-it-works/overview
- **Design** The agent starts an SSH server "using the STDIO of the secure tunnel", so the local client forwards ports over that tunnel. https://devpod.sh/docs/how-it-works/overview
- **Design** A machine is reached by a provider's `exec.command`, which DevPod "will use ... to inject itself into the environment and route all communication through the commands standard output and input"; the SSH provider does this over SSH. https://devpod.sh/docs/developing-providers/quickstart
- **Design** Add a provider with `devpod provider add <name>` (for example `devpod provider add ssh`). https://devpod.sh/docs/managing-providers/add-provider
- **Design** SSH provider needs: passwordless `ssh user@host`; remote user root or in the docker group; Linux only; options `HOST`, `PORT` (22), `EXTRA_FLAGS`, `AGENT_PATH` (default `/tmp/devpod/agent`), `DOCKER_PATH`. https://github.com/loft-sh/devpod-provider-ssh
- **Design** The agent also handles "auto-shutting down the VM after inactivity" and injecting Git and Docker credentials (`INJECT_GIT_CREDENTIALS`, "If DevPod should inject git credentials into the remote host"). https://devpod.sh/docs/developing-providers/quickstart and https://devpod.sh/docs/managing-providers/add-provider
- **Design** State files are named `workspace.json` and `machine.json` in the provider package (client-side config types with `Context` and `Origin` fields). https://pkg.go.dev/github.com/loft-sh/devpod/pkg/provider
- **Not established:** on-disk location of that state and whether workspaces survive loss of the client; docs pages for state location returned nothing. (4), (5), (7) do not apply: no scheduler, no server to compare versions with.

---

## E. CI runner fleets

### E1. GitHub Actions self-hosted runners

- **Design** Outbound only, long poll: "The self-hosted runner uses an HTTP(S) long poll that opens a connection to GitHub for 50 seconds"; "Only an outbound connection from the runner ... is required"; "There is no need for an inbound connection". https://docs.github.com/en/enterprise-server@3.13/actions/concepts/runners/communicating-with-self-hosted-runners (GitHub Enterprise Server 3.13 docs; the github.com reference page returned only "outbound HTTPS connections over port 443" and at least 70 kbps: https://docs.github.com/en/actions/reference/runners/self-hosted-runners)
- **Design** Join: "time-limited, GitHub runner registration token"; registration creates an RSA key pair, the service stores the public key and issues a `clientId`; the listener gets an OAuth token signed with the private key, which "grants permission to access the message queue (HTTP long poll)"; job messages are "encrypted with the runner's public key"; the job message carries a token that "is never persisted". https://github.com/actions/runner/blob/main/docs/design/auth.md
- **Design** Registration token "expires after one hour"; the REST API can "Generate a configuration that can be passed to the runner application at startup" (JIT config). https://docs.github.com/en/rest/actions/self-hosted-runners
- **Design** Assignment by labels: "GitHub looks for a runner that matches the job's `runs-on` labels and groups." If none is online and idle the job stays queued; "If the job remains queued for more than 24 hours, the job will fail." https://docs.github.com/en/actions/reference/runners/self-hosted-runners
- **Design** "If the runner doesn't pick up the assigned job within 60 seconds, the job is re-queued." https://docs.github.com/en/actions/reference/runners/self-hosted-runners
- **Design** Runner states: Idle "connected to GitHub and is ready", Active, Offline "not connected to GitHub". https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/monitoring-and-troubleshooting-self-hosted-runners
- **Design** Offline removal: "automatically removed from GitHub if it has not connected to GitHub Actions for more than 14 days"; ephemeral "more than 1 day"; JIT runners that never run a job are removed. https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/remove-runners
- **Design** Ephemeral: `--ephemeral`; the service "will then automatically de-register the runner after it has processed one job." https://docs.github.com/en/actions/reference/runners/self-hosted-runners
- **Design** Auto-update: the runner "automatically updates itself when a job is assigned to the runner" or within a week of release if idle; the minimum-version rule on this page is 63 days, not 30: "If you do not update your runner within 63 days of a new major, minor, or patch version becoming available" jobs are no longer assigned; the extract adds that critical security updates have a stricter rule (the stricter value was not retrieved). https://docs.github.com/en/actions/reference/runners/self-hosted-runners
- **Design** Security: "only use self-hosted runners with private repositories" because forks of a public repository "can potentially run dangerous code on your self-hosted runner machine" through a pull request. https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/manage-access
- **Design** Job token lifetime is the run, capped at the job timeout (default 6 hours) plus 10 minutes. https://github.com/actions/runner/blob/main/docs/design/auth.md
- **Design** The failure text a job shows when the runner vanishes: "lost communication with the server. Verify the machine is running and has a healthy network connection." https://github.com/actions/runner/issues/2040
- **Not established:** the server-side timeout before a job on a silent runner is failed, and whether the job is re-run. The runner docs pages say nothing about a runner that goes silent mid-job; the issue gives no value. Job is not retried by the platform in anything read; retries are workflow-level (not read).

### E2. GitLab Runner

- **Design** Polling: "By default, a GitLab Runner polls a GitLab instance for new CI/CD jobs periodically." Optional long polling: the server "hold[s] job requests from runners in a long poll until a new job is ready"; the runner gets a `204 No job` with a `last_update` token and resends it in `X-GitLab-Last-Update`; Workhorse holds the request up to `apiCiLongPollingDuration` (example `50s`, no default stated). https://docs.gitlab.com/ci/runners/long_polling/
- **Design** Config defaults: `check_interval` "The default value is `3`" seconds; `request_concurrency` "Default is `1`"; `concurrent` "Limits how many jobs can run concurrently, across all registered runners." https://docs.gitlab.com/runner/configuration/advanced-configuration/
- **Design** Auth: a runner gets an authentication token "that you use to register it" and "uses the token to authenticate with GitLab when picking up jobs from the job queue"; registration tokens are legacy. https://docs.gitlab.com/ci/runners/runners_scope/
- **Design** Matching by tags: "enter the job tags to specify jobs the runner can run", or "Run untagged jobs". https://docs.gitlab.com/ci/runners/runners_scope/
- **Design** Stuck handling: "If a job doesn't progress for an extended period, GitLab automatically drops it and records a specific `failure_reason`." Pending with matching runners: dropped after 24 hours; pending with no matching runners: 1 hour; running with no updates: 30 minutes; `server_timeout_running`: configured `timeout` plus 15 minutes. https://docs.gitlab.com/ci/jobs/job_troubleshooting/
- **Design** Retries are opt-in per job: `retry:when` lists `runner_system_failure` ("for example, job setup failed"), `no_updates_running`, `runner_interrupted` and others; `retry:max` is 0, 1 or 2. https://docs.gitlab.com/ci/yaml/#retrywhen
- **Design** Runner job timeout: runner maximum is used "if it is shorter than the job timeout defined in the project"; minimum 600 seconds. https://docs.gitlab.com/ci/runners/configure_runners/
- **Design** Version skew: "keep the GitLab Runner `major.minor` version the same as the GitLab major and minor version"; "Older runners may still work with newer GitLab versions, and vice versa"; "Backward compatibility is guaranteed between minor version updates." https://docs.gitlab.com/runner/#gitlab-runner-versions
- **Not established:** a runner auto-update mechanism or the "stale runner" cutoff; not on the pages read.

### E3. Buildkite agents

- **Design** "The agent works by polling Buildkite's agent API over HTTPS"; "no need to forward ports or provide incoming firewall access". https://buildkite.com/docs/agent/v3
- **Design** Dispatch: jobs go to "the first available agent within a queue", ordered by how recently the agent last completed a job; the agent's `queue` tag (one self-hosted queue per cluster) selects the queue; `ping-mode` default `auto` "prefer streaming, but fall back to polling". https://buildkite.com/docs/agent/v3/queues and https://buildkite.com/docs/agent/v3/configuration
- **Design** Tokens: a cluster agent token is "a long-lived credential"; the agent gets a session token for the connection lifetime and a job token per job (inherits the agent token's permissions); expiry optional, default never; "Revoke token: No new agents can start with this token, but agents that are already connected keep running." https://buildkite.com/docs/agent/v3/tokens
- **Design** Heartbeat: agents "regularly send heartbeat updates"; after "three consecutive minutes" with none the agent "is marked as lost within the next 60 seconds" and gets no further jobs. https://buildkite.com/docs/agent/lifecycle
- **Design** A job fails with exit status -1 "if communication with the agent has been lost." https://buildkite.com/docs/pipelines/configure/retry
- **Design** Lost-agent jobs are not retried by default: default `exit_status` is `"*"`, matching 1 to 255, which excludes -1; you add an explicit `exit_status: -1` rule; default retry limit two, maximum 10; `signal_reason` values include `agent_stop` and `agent_refused`. https://buildkite.com/docs/pipelines/configure/retry
- **Design** Graceful stop: `SIGTERM` makes the agent "gracefully disconnect, after completing any job"; `disconnect-after-job` and `disconnect-after-idle-timeout` (default 0 = no timeout) exist. https://buildkite.com/docs/agent/lifecycle and https://buildkite.com/docs/agent/v3/configuration
- **Design** Signed pipelines: moves "towards zero-trust CI/CD by further isolating you from Buildkite itself being compromised"; uploader agents hold the private key, verifying agents the public key; default `block` refuses unsigned jobs. https://buildkite.com/docs/agent/v3/signed-pipelines
- **Not established:** what a queue does when no agent is connected (waits indefinitely or times out) and agent self-update/version window. https://buildkite.com/docs/agent/v3/queues does not say; no version page was fetched.

---

## F. Agent orchestrators

### Claude Code self-hosted environments (runners)

- **Design** Split: "Anthropic's control plane places the session on your environment's queue, where a runner claims it, clones the repository ... and starts a Claude Code process on your host"; "Anthropic never connects into your network"; "The control plane remains Anthropic-hosted: session orchestration, queueing, and the claude.ai interface". https://code.claude.com/docs/en/self-hosted-environments
- **Design** Join: an "Environment secret" is "The single shared credential runners use to authenticate and register with the environment"; a runner "registers with the environment, receives a runner token, and polls for sessions." https://code.claude.com/docs/en/self-hosted-environments
- **Design** Streams: the session child "holds the session's event stream to `api.anthropic.com`", all outbound HTTPS; "Session streaming uses server-sent events over HTTPS, so a proxy in the path must not buffer responses." https://code.claude.com/docs/en/self-hosted-environments
- **Design** Assignment: sessions are routed "to an environment, not to an individual runner"; a runner "with free capacity claims the session and holds a lease"; each runner has a capacity; a runner "locks to that owner when it claims its first session" so code never mixes between owners. https://code.claude.com/docs/en/self-hosted-environments
- **Design** Liveness: the runner polls (10 s request timeout), "each poll refreshes the lease and doubles as the heartbeat"; "If the runner stops polling, its lease lapses after about 60 seconds. The server can then take a few minutes and sometimes longer to requeue the session for another runner." https://code.claude.com/docs/en/self-hosted-environments
- **Design** Crash versus clean stop: a signal-less host kill "is indistinguishable from a crash: the control plane records a lost worker rather than a clean release, and the session requeues to another runner"; `--retire-at` releases sessions so they "resume on a fresh runner when the user sends their next message"; "A turn that outlives the kill is still lost". https://code.claude.com/docs/en/self-hosted-environments
- **Design** Retry backoff: failed polls retry after a second or two, doubling up to 20 seconds. https://code.claude.com/docs/en/self-hosted-environments
- **Design** Credentials: the runner "authenticates with the environment secret"; "The environment secret can register runners and pick up any session queued on the environment"; "treat every runner host as reachable for code execution by everyone who can dispatch to it, and place on a runner host only data and credentials that all of those people are allowed to read"; run each runner in a fresh container or VM with `--capacity 1`; rotate the secret after suspected compromise, after which "Anthropic refuses every orchestrator and runner on the old secret." https://code.claude.com/docs/en/self-hosted-environments-deploy.md
- **Design** Version: runner requires Claude Code 2.1.224 or later; session children run the runner's binary with auto-update off; to hold a fleet pin the version and disable auto-updates; an old version can be refused a model ("Claude Code does not support this model"). Control-plane/runner skew window: **Not established** (the deploy page was read only to character 100,000 of 134,225). https://code.claude.com/docs/en/self-hosted-environments-deploy.md
- **Design** Where state lives: checkouts, artifacts and secrets "stay on the machines you provision"; the conversation and transcript go to `api.anthropic.com`. https://code.claude.com/docs/en/self-hosted-environments
- **Not established:** agent-to-agent messaging across machines. Not described on the pages read.

### Claude Code Remote Control and cloud sessions

- **Design** Remote Control: the session runs on your machine ("Claude keeps running locally the entire time"); it "makes outbound HTTPS requests only and never opens inbound ports"; it "registers with the Anthropic API and polls for work", and the server routes messages between the web/mobile client and the local session over a streaming connection. https://code.claude.com/docs/en/remote-control
- **Design** The transcript is stored on Anthropic servers while connected, which "lets the session reconnect after a network drop". https://code.claude.com/docs/en/remote-control
- **Design** Server mode (`claude remote-control`): `--capacity` default 32; `--spawn worktree` gives each on-demand session its own git worktree, `same-dir` shares one directory. https://code.claude.com/docs/en/remote-control
- **Design** Disconnect limits: server mode "gives up after roughly 10 minutes" of network outage and the process exits; interactive sessions keep retrying; 403 responses are retried up to three minutes; presence heartbeat failure disconnects after about 30 minutes. https://code.claude.com/docs/en/remote-control
- **Design** Cloud sessions: run on Anthropic VMs by default, or a self-hosted environment; GitHub credentials "never enter a session's VM" and git goes through a proxy that attaches the credential. After "a period of inactivity" the VM is reclaimed; reopening provisions a fresh VM and restores conversation history but not running shell commands or subagents. https://code.claude.com/docs/en/claude-code-on-the-web
- **Design** Follow-up from any machine: `claude -p "..." --cloud <session-id>` posts one message into a running cloud session. https://code.claude.com/docs/en/claude-code-on-the-web

### Cursor cloud agents and self-hosted workers

- **Design** Cloud agents "run in isolated VMs in the cloud"; Cursor "manages VM provisioning, isolation, snapshots, startup, artifacts, and capacity". https://cursor.com/docs/cloud-agent
- **Design** Split: "the Agent Loop (maintains state, calls inference) is separated from the process that executes tool calls"; `agent worker start` "opens a long-lived outbound connection to Cursor's cloud, and 'no inbound ports or ingress rules are required.'" Tool calls travel over that connection. https://cursor.com/docs/cloud-agent/self-hosted/private-workers
- **Design** Pool workers: "A worker opens a long-lived outbound HTTPS connection to Cursor's cloud"; labels are key-value pairs, `repo` and `pool` are reserved; "Each Cloud Agent session claims one worker at a time"; "Requests wait in the pool until an available worker claims them"; the team default pool "waits a limited time for that pool's workers, then fails if none connects." https://cursor.com/docs/cloud-agent/self-hosted-guides/pool
- **Design** Limits: "up to 200 workers per user and 1000 per team". https://cursor.com/docs/cloud-agent/self-hosted-guides/pool
- **Design** Worker lost: a claimed machine that goes offline gets a reconnect window "which defaults to `0`"; workspace state "doesn't carry over to a fresh machine unless the pool uses hibernation"; idle timeout defaults to 1 hour. https://cursor.com/docs/cloud-agent/self-hosted-guides/pool
- **Design** Credentials: pool workers use a service-account API key or a session token that "Serves one claim"; home-folder config persists across claims on the same machine, so a fresh worker per claim is recommended. https://cursor.com/docs/cloud-agent/self-hosted-guides/pool
- **Design** "My Machines": `agent worker start --name`; requires "a personal credential"; routed by `worker=<name>` or `machine=<name>`; a request runs only if the machine belongs to the requester and its registered repo matches; with no match "the request fails instead of falling back to another environment." https://cursor.com/docs/cloud-agent/self-hosted-guides/my-machines
- **Design** Version: "A running worker doesn't update itself, so run `agent update` and restart the worker." https://cursor.com/docs/cloud-agent/self-hosted-guides/pool
- **Design** "Your codebase stays in your environment. Only the chunks of files the model reads leave your network." https://cursor.com/docs/cloud-agent/self-hosted/private-workers
- **Not established:** what a compromised worker can access (the pool page does not say), and agent-to-agent messaging.

### Devin (Cognition) VPC

- **Design** "Brain: A stateless, cloud-based service" in Cognition's cloud; the devbox runs in a single-tenant VPC that Cognition manages; the customer VPC connects by AWS PrivateLink or IPSec. https://docs.devin.ai/enterprise/vpc/overview
- **Design** "On startup, Devin establishes a secure WebSocket connection to an isolated container in Cognition's tenant", needing HTTPS/443 egress. https://docs.devin.ai/enterprise/vpc/overview
- **Not established:** where session state is stored, assignment, worker-lost behaviour, versioning. The page does not say.

### OpenHands

- **Design** Agent Server "exposes SDK conversations and workspaces to remote clients through REST and WebSocket APIs"; workspace types `LocalWorkspace`, `DockerWorkspace` (spawns a container running the server), `RemoteAPIWorkspace` ("connects via HTTP to" a hosted server). https://docs.openhands.dev/sdk/arch/overview
- **Design** Runtime API: `APIRemoteWorkspace` provisions sandboxes, "pull[s] and run[s] the specified image"; the conversation "uses WebSocket communication with the remote server"; needs a runtime API key; the page states it is "designed primarily for benchmark evaluation at scale, not for building production applications." https://docs.openhands.dev/sdk/guides/agent-server/api-sandbox.md
- **Design** Legacy runtime doc: the backend talks to the in-container action execution server "over RESTful API, sending actions and receiving observations". https://docs.openhands.dev/openhands/usage/architecture/runtime (marked legacy)
- **Not established:** event store location, who dials whom for the remote runtime, runtime-lost behaviour, Kubernetes runtime design, agent-to-agent messaging. Looked at the three pages above and a search; the Sysbox install page was not fetched.

### Orca (stablyai/orca)

- **Design** Desktop app running agents in git worktrees; ways to run: local; SSH target ("Agents and `git worktree` run on the remote; the editor, diff, and UI stay on your laptop"; Orca sets up "its own managed server on the host on first connect"); Remote Orca Server ("The server owns projects, worktrees, terminals, and agent processes; clients are the UI"); per-worktree cloud VM. https://orca.dev/docs/ways-to-run
- **Design** Reachability: for the Remote Orca Server the page recommends Tailscale on both computers; no NAT-traversal or relay design is given. https://orca.dev/docs/ways-to-run
- **Design** Mobile pairing: one-time code, "The pairing exchange establishes a device token for that phone"; paths are Orca Relay (needs sign-in) or a LAN/Tailscale address; "The paired desktop owns the agent sessions"; LAN sessions drop when the desktop closes. https://orca.dev/docs/mobile
- **Design** Orchestration: a coordinating agent "splits the work, starts worker agents in their own workspaces, and brings their results back to you." https://raw.githubusercontent.com/stablyai/orca/main/README.md
- **Not established:** terminal streaming protocol, agent-to-agent message transport across machines, relay design, worker-lost behaviour.

### Claude Squad and Vibe Kanban

- **Design** Claude Squad: "tmux to create isolated terminal sessions for each agent" and "git worktrees"; config in `~/.claude-squad/config.json`. Multi-machine: **Not established** (not documented). https://github.com/smtg-ai/claude-squad
- **Design** Vibe Kanban: "each workspace gives an agent a branch, a terminal, and a dev server"; remote use through SSH access, a Cloudflare Tunnel or ngrok, or a `VK_TUNNEL` relay mode; README states "Vibe Kanban is sunsetting." State location: **Not established**. https://github.com/BloopAI/vibe-kanban

---

## G. Tailscale and Headscale

- **Design** Control plane versus data plane: the coordination server is "a shared drop box for public keys" and "carries virtually no traffic"; each node keeps its private key, uploads its public key and "a note about where that node can currently be found"; data is a mesh of direct connections. https://tailscale.com/blog/how-tailscale-works
- **Design** NAT traversal without open ports "based on the Internet STUN and ICE standards"; DERP servers "just blindly forward already-encrypted traffic"; "Each node is responsible for blocking incoming connections that should not be allowed". https://tailscale.com/blog/how-tailscale-works
- **Design** "All connections start as relayed through a DERP server, and Tailscale then tries to upgrade them to a direct connection"; then peer relay (a device in the same tailnet), else stays on DERP; rechecked periodically; DERP is "generally slower than direct connections and may offer lower maximum throughput"; peer relays "still introduce more latency than a fully direct connection"; all three are end-to-end encrypted. https://tailscale.com/kb/1257/connection-types
- **Design** Join: auth keys "let you register new nodes without needing to sign in using a web browser"; one-off or reusable; ephemeral keys remove the node "after the device goes offline"; expiry 1 to 90 days, default max 90; tagged keys give the device the tags' identity. https://tailscale.com/kb/1085/auth-keys
- **Design** Node key expiry: default "180 days"; when it lapses "connections to/from the given endpoint will stop working"; can be disabled per machine "such as trusted servers"; `tailscale up --force-reauth` renews. https://tailscale.com/kb/1028/key-expiry
- **Design** Tailnet lock: with a compromised coordination server "attackers can't send or receive traffic in your tailnet"; new nodes must be signed by trusted signing nodes; disabling needs a disablement secret. https://tailscale.com/kb/1226/tailnet-lock
- **Design** Headscale: "a self-hosted, open source alternative to the Tailscale control server", one tailnet, "focus is homelabbers and self-hosters"; handles "100s of devices (maybe more)" when the network changes little. https://headscale.net/stable/about/faq/
- **Design** Headscale embedded DERP: "Headscale provides an embedded DERP server"; STUN on udp/3478; the DERP map is customisable by YAML. https://headscale.net/stable/ref/derp/

---

## Comparison table

"Re-runs automatically" means the platform itself re-executes the lost work with no user rule.

| System | Control plane state | Worker dials out? | Stream path | No worker free | Worker lost | Re-runs automatically? |
|---|---|---|---|---|---|---|
| Coder | PostgreSQL via coderd only; proxies hold no DB | Not established for agents/provisioners (config points them at coderd URL); control plane must be reachable at ACCESS_URL with no NAT | Direct P2P via STUN/WireGuard, DERP relay through control plane when NAT traversal fails; dashboard apps relayed | Job Pending; fails after 30 min with no eligible progressing provisioner | Running job fails after 5 min without updates; agent-disconnect behaviour not established | No: user re-triggers |
| Gitpod classic | DB + ws-manager-bridge; workspace state in CRDs per cluster | Not established | Through ws-proxy per cluster (SSH 2200, hostname routing) | Not established | Not established (bridge has max-disconnect setting) | Not established |
| Gitpod Flex / Ona | Identity, config, guardrails, coordination; no code | Egress allowlist to app.gitpod.io; direction otherwise not established | Runner-side proxy to environments; path not established | Not established | Not established | Not established |
| Daytona | API + scheduler | Runners poll the control plane for jobs | Proxy by host routing | Not established | Health-check gap incident; draining migrates backups | Not established |
| DevPod | None (client-side) | n/a: client dials via provider command (SSH) | Stdio SSH tunnel client to agent | n/a | Not established | n/a |
| GitHub Actions | Queue, labels, runner registry | Yes, 50 s HTTPS long poll, outbound only | Not applicable (logs) | Queued; fails after 24 h | Runner offline; job error "lost communication"; timeout not established | Not established (no retry read) |
| GitLab Runner | Queue, runner tokens | Yes, polls `jobs/request`, optional long poll | Not applicable | Dropped after 1 h (no matching runner) or 24 h (matching) | Running job dropped after 30 min without updates | No: opt-in `retry:when` (max 2) |
| Buildkite | Queues, tokens, job state | Yes, polls (or streaming dispatch), outbound HTTPS | Not applicable | Not established | Lost after ~3 to 4 min without heartbeat; job exit -1 | No by default; explicit `exit_status: -1` rule |
| Claude Code self-hosted runners | Queue, session transcript and lease on Anthropic side | Yes, outbound HTTPS polling; Anthropic never connects in | Session child holds an SSE stream outbound to api.anthropic.com | Session waits on the environment queue | Lease lapses about 60 s; requeue takes minutes | Yes: session requeued to another runner |
| Claude Code Remote Control | Transcript on Anthropic | Yes, outbound HTTPS, registers and polls | Via Anthropic API, TLS | n/a (your machine) | Server mode exits after about 10 min outage | No (resume manually) |
| Cursor self-hosted workers | Agent loop and state in Cursor cloud | Yes, long-lived outbound HTTPS | Tool calls over the worker's outbound connection | Waits in pool; default pool fails after a limited time; My Machines fails | Reconnect window default 0; state lost without hibernation | Not established (a follow-up claims a new eligible worker) |
| Devin VPC | Stateless brain in Cognition cloud | Yes, WebSocket to Cognition tenant, 443 egress | Not established | Not established | Not established | Not established |
| OpenHands | Not established | Client dials runtime API over HTTP/WebSocket | WebSocket to remote server | Not established | Not established | Not established |
| Orca | Remote server owns projects and agents | Client dials server (SSH, pairing URL); Tailscale advised | Not established | n/a | Agents keep running on host, laptop reattaches | n/a |
| Tailscale / Headscale | Keys, policy, node locations; no traffic | Yes, nodes connect out; direct WireGuard when possible | Direct, else peer relay, else DERP over HTTPS | n/a | Expired key stops connections; ephemeral nodes removed | n/a |

## Not established

- Coder: direction of agent/provisioner connections (no sentence); behaviour of a workspace when its agent disconnects; agent heartbeat and `connection_timeout` defaults; what a compromised agent or provisioner can reach; coderd/agent/provisioner version compatibility and agent auto-update; published throughput.
- Gitpod classic: how a workspace cluster is registered and picked; dead-workspace detection; node-failure handling. Ona runners: connection direction, SSH/IDE path, runner auto-update, offline-runner behaviour, capacity-exhausted behaviour.
- Daytona: runner registration, heartbeat, unresponsive timeout, outright runner failure (repository fetched shows an archived README; runner docs page 404).
- DevPod: on-disk location of workspace/machine state.
- GitHub Actions: server-side timeout and outcome for a job on a runner that goes silent mid-job; the stricter critical-update version rule; the "30-day" rule in the brief does not match the docs read (63 days).
- GitLab: runner auto-update and stale-runner cutoff. Buildkite: queue behaviour with no connected agent; agent version window.
- Claude Code self-hosted runners: control-plane versus runner version skew window; agent-to-agent messaging. Cursor: compromised-worker exposure; Cursor's earlier-seen "10 per user, 50 per team" figures appeared only in a search summary and conflict with the pool guide's 200/1000, so only 200/1000 is reported.
- Devin: state storage, scheduling, failure. OpenHands: event store, remote-runtime dial direction, Kubernetes runtime, failure handling. Orca: terminal streaming, cross-machine agent messaging, relay details. Claude Squad, Vibe Kanban: multi-machine design.
- Agent-to-agent messaging across machines: no system read documents it.
