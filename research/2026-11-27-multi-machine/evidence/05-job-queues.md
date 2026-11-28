# 05 - Job queues and workflow engines (collector: external sources only)

Rules: `COLLECTOR-RULES.md`. Every claim ends with the URL that was fetched in
this run (2026-11-27). Text in quotation marks is what the fetch tool returned
for that page. The fetch tool summarises a page with a small model, so a quote
can be a fragment of a longer sentence; no page was read in full by hand.
Nothing in this file is a statement about this repository.

## A. BullMQ

### A1. What must run, and licences

- **Design:** "BullMQ is an open source library for running background jobs and message queues on top of Redis(TM) or PostgreSQL." Redis is the default backend; it "works with Redis 6.2 or newer and compatible servers such as Valkey and Dragonfly"; PostgreSQL 13 or newer is the alternative. https://docs.bullmq.io/guide/introduction
- **Design:** The PostgreSQL backend is optional, "runs the exact same" API, needs PostgreSQL 13+ (14+ recommended), and "The Redis backend remains the default and the most battle-tested option." https://docs.bullmq.io/guide/postgresql
- **Design:** The v6.0.0 changelog entry (2026-07-30) is "release BullMQ v6 with pluggable queue backends", "Introduce the IQueueBackend abstraction, Redis and PostgreSQL backends". Most recent entry read: 6.7.10 (2026-11-24). https://docs.bullmq.io/changelog
- BullMQ licence: "Released under the MIT License." https://docs.bullmq.io/guide/introduction ; repository: "MIT license". https://github.com/taskforcesh/bullmq
- The README comparison table lists some features as BullMQ-Pro only (observables, group rate limiting, group support, batch support). Pro licence terms: **Not established** (not looked up). https://github.com/taskforcesh/bullmq
- Redis licence history, from Redis: "Redis 7.2.x of Redis Open Source and earlier versions remain subject to the BSD3 license. Redis Community Edition 7.4.x to 7.8.x remain subject to the dual RSALv2/SSPLv1 license." Version table: 7.2 and earlier BSD-3-Clause; 7.4 RSALv2 or SSPLv1; 8+ RSALv2 or SSPLv1 or AGPLv3. The change to RSALv2/SSPLv1 is dated "March 2024"; AGPLv3 was added "starting with the general availability release of Redis 8". https://redis.io/legal/licenses/
- Valkey, from its own site: "an open source (BSD) high-performance key/value datastore", "backed by the Linux Foundation". https://valkey.io/

### A2. Language and runtime

- The guide's code is TypeScript, which "runs on Node.js and Bun". https://docs.bullmq.io/guide/introduction
- Official non-Node implementations, from the README: "BullMQ is available natively in multiple languages:" Node.js/Bun, Python (`pip install bullmq`), Rust, Elixir, .NET, Go (`go get github.com/taskforcesh/bullmq-go`), PHP; "For other platforms, check out the BullMQ Proxy." https://raw.githubusercontent.com/taskforcesh/bullmq/master/README.md
- Official Go client exists: "The official Go port of BullMQ, the job queue for Redis and PostgreSQL." Go producers and workers can share queues with the Node.js, Python, Elixir, PHP, Rust and .NET ports. Requires "Go 1.26 or newer" and "Redis 6.2 or newer, or PostgreSQL 13 or newer". https://github.com/taskforcesh/bullmq/tree/master/golang
- **Maturity of the Go port:** README says "API changed before the first release"; pkg.go.dev shows a v0 module (v0.1.0 published Nov 25, 2026, marked not stable, MIT). https://github.com/taskforcesh/bullmq/tree/master/golang ; https://pkg.go.dev/github.com/taskforcesh/bullmq-go
- Go port limitations: no OpenTelemetry hooks; Redis Cluster lightly tested, Sentinel untested, CI runs on standalone Redis only; on PostgreSQL "Don't rely on `QueueEvents` to see every `completed` or `failed` event on PostgreSQL; read the job state instead."; sandboxed processors and legacy repeatable jobs unsupported. https://github.com/taskforcesh/bullmq/blob/master/golang/README.md
- Go port not implemented: global concurrency groups ("Pro-only feature"). https://github.com/taskforcesh/bullmq/blob/master/golang/FEATURE_PARITY.md

### A3. Delivery semantics and worker death (key question)

- "The queue aims for an "at least once" working strategy." "This means that in some situations, a job could be processed more than once." https://docs.bullmq.io/bull/important-notes (page is under the `/bull/` path, the older Bull library's notes; the same page is the one that carries the wording.)
- "When a worker is processing a job it will keep the job "locked" so other workers can't process it." Stalled: "the job will be considered stalled and is automatically restarted; it will be **double processed**." Advice: "you should always listen for the `stalled` event and log this to your error monitoring system". https://docs.bullmq.io/bull/important-notes
- BullMQ stalled-jobs page: the active state "prevents a worker that crashes or enters an endless loop from keeping a job in an active state forever"; when a worker cannot signal liveness "that job is moved back to the waiting list, or to the failed set"; the queue emits a `stalled` event. "There is not a 'stalled' state, only a 'stalled' event." https://docs.bullmq.io/guide/jobs/stalled
- Another BullMQ page: "A stalled job is moved back to the waiting status" and "will be processed again by another worker." https://docs.bullmq.io/guide/workers/stalled-jobs
- Defaults (WorkerOptions): `lockDuration` 30000 ms ("Duration of the lock for the job in milliseconds."); `stalledInterval` 30000 ("Number of milliseconds between stallness checks."); `maxStalledCount` 1 ("Amount of times a job can be recovered from a stalled state to the `wait` state."); `concurrency` 1 ("Amount of jobs that a single worker is allowed to work on in parallel."); `skipStalledCheck` false. `lockRenewTime` default: **Not established** (page lists none). https://docs.bullmq.io/api/interfaces/v5.WorkerOptions.html
- Over the limit: a job that stalls more than `maxStalledCount` "will be failed permanently" with "job stalled more than allowable limit"; default "1, as stalled jobs should be a rare occurrence." https://docs.bullmq.io/guide/jobs/stalled
- Stalls are caused by an overloaded Node event loop: "as long as you do not perform CPU operations exceeding that value you should not get stalled jobs"; remedy: sandboxed processors in separate Node processes. https://docs.bullmq.io/guide/jobs/stalled
- Killing a worker without waiting: "these jobs will be marked as stalled and processed automatically when new workers come online" (default wait about 30 s). https://docs.bullmq.io/guide/going-to-production
- Lock lost while the processor still runs: "A worker that loses a job's lock can't move it to `failed`. The job stays `active` briefly, and the stalled job checker moves it back to `waiting`." https://docs.bullmq.io/guide/workers/cancelling-jobs
- Go port: workers renew the lock at half of `LockDuration`; on failed renewal the worker "cancels the job's context with `bullmq.ErrLockLost`. This is a Go addition: Node.js keeps the processor running." https://github.com/taskforcesh/bullmq/blob/master/golang/README.md
- Idempotency: "your jobs should be designed with failure in mind"; success first time or after retry should leave the same final state ("Idempotence"). https://docs.bullmq.io/patterns/idempotent-jobs
- Exactly-once: **Not established** (no BullMQ page read states exactly-once; the README comparison table has no delivery-guarantee row). https://raw.githubusercontent.com/taskforcesh/bullmq/master/README.md

### A4. How long a job may run

- No maximum job duration was found. The page on concurrency "doesn't directly address long-running jobs." https://docs.bullmq.io/guide/workers/concurrency
- Mechanism: the lock must be renewed periodically: "the worker needs to periodically notify BullMQ that it is still working on the job." https://docs.bullmq.io/guide/workers/stalled-jobs
- Recommended `lockDuration` for long jobs: **Not established** from an official page (only a third-party mirror said so; not used).

### A5. Placement-relevant features

- Per-worker concurrency: `concurrency` option (above); "The other way to achieve concurrency is to provide multiple workers"; workers can run "on many machines". https://docs.bullmq.io/guide/workers/concurrency
- Rate limiting is global per queue: "The rate limiter is global, so if you have for example 10 workers for one queue ... still only 10 jobs will be processed by second." Jobs that are limited "stay in the waiting state." https://docs.bullmq.io/guide/rate-limiting
- Priorities: "A `priority` of `0` means no priority, so those jobs are processed before prioritized jobs." https://docs.bullmq.io/guide/jobs/prioritized
- Delayed jobs: "After the delay time has passed, the job is processed as a regular job." https://docs.bullmq.io/guide/jobs/delayed
- Flows: "parent - child relationships between jobs"; a parent is held back until its children finish successfully. https://docs.bullmq.io/guide/flows
- Routing to one machine: a queue is identified by name (`new Queue('Cars')`), any worker that opens that name takes jobs from it; whether a per-machine queue name is a documented routing pattern: **Not established**. https://docs.bullmq.io/guide/queues
- Cancelling an active job: the processor receives an optional `AbortSignal` as third parameter; `worker.cancelJob('job-id-123')` "returns true if job was active, false otherwise"; `worker.cancelAllJobs()`. Limits: "`worker.cancelJob()` only reaches the jobs run by that `Worker` instance."; for other processes the page points to a Relay example "Cancelling jobs on other workers". "Cancelling is a request, so the processor must check the signal and stop its own work." https://docs.bullmq.io/guide/workers/cancelling-jobs

### A6. Operations

- "Therefore is very important to configure the `maxmemory-policy` setting to `noeviction`." "We recommend enabling AOF (Append Only File)"; sync about once per second is usually enough; "persistence needs to be configured manually". https://docs.bullmq.io/guide/going-to-production
- Connection loss: "It is impossible to guarantee that a connection between BullMQ and Redis will always stay online"; ioredis retries "forever" by default; workers need `maxRetriesPerRequest: null`. https://docs.bullmq.io/guide/going-to-production
- Job data is stored in clear text; "The best is to avoid storing sensitive data in the job altogether." https://docs.bullmq.io/guide/going-to-production
- Finished jobs are kept by default: "By default, when your queue jobs are completed (or failed), they are stored in two special sets". https://docs.bullmq.io/guide/queues/auto-removal-of-jobs
- Workers and producers are clients that connect to the broker (Redis or Postgres); no inbound port on the worker is described. Behind-NAT behaviour: **Not established** (no page read states it; the client-dials-server model is only implied by the connection docs).
- Redis persistence, from Redis: RDB "you should be prepared to lose the latest minutes of data"; AOF with the default `fsync` every second "you can only lose one second worth of writes"; "The general indication you should use both persistence methods is if you want a degree of data safety comparable to what PostgreSQL can provide you." https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/
- Redis security, from Redis: "Redis is designed to be accessed by trusted clients inside trusted environments." Protected mode (since 3.2.0): with the default configuration (all interfaces, no password) Redis "only replies to queries from the loopback interfaces". ACLs "introduced in Redis 6" are the recommended authentication; TLS is "optional". `AUTH` "is sent unencrypted". https://redis.io/docs/latest/operate/oss_and_stack/management/security/

### A7. Measurements

- **Measured (project):** PostgreSQL backend throughput is "often around ~1.5-2x fewer jobs/s" than Redis "in similar setups, but this varies by workload". Not for this workload. https://docs.bullmq.io/guide/postgresql
- The repository `benchmark/` directory contains one file, `relay-benchmark.ts`, no README numbers. https://github.com/taskforcesh/bullmq/tree/master/benchmark
- Other BullMQ throughput numbers: **Not established**.

## B. River (riverqueue, Go + Postgres, SQLite experimental)

### B1. What must run, licences

- River "persists jobs to a Postgres database"; "River supports PostgreSQL and SQLite." PostgreSQL is "River's primary database target"; tested against "the three most recent major versions of PostgreSQL". Runtimes: Go, Java 21+, Ruby 3.2+, Rust, Node.js (TypeScript). https://riverqueue.com/docs
- Licence: "MPL-2.0 license". Tagline: "The polyglot queue: Fast and reliable background jobs in Go, Java, JS, Ruby, and Rust on Postgres/SQLite." https://github.com/riverqueue/river
- Nothing besides the database is needed: a River client "inserts jobs, runs workers, and participates in maintenance services" inside the application process. https://riverqueue.com/docs
- Postgres features used: a dedicated `LISTEN` connection "with polling as a fallback" (polling can be forced); the Postgres query `JobGetAvailable` contains `FOR UPDATE` and `SKIP LOCKED`. https://riverqueue.com/docs/database-drivers ; https://raw.githubusercontent.com/riverqueue/river/master/riverdriver/riverpgxv5/internal/dbsqlc/river_job.sql
- Go drivers: `riverpgxv5`, `riverdatabasesql`. https://riverqueue.com/docs/database-drivers

### B2. SQLite support and maturity

- "River 'mainly targets Postgres, but is equipped with experimental support for SQLite.'" "SQLite support was introduced in River 0.23.0." "SQLite support is broadly tested internally, but should still be considered experimental"; the schema "may still have a few tweaks made to its schema before being considering finalized." https://riverqueue.com/docs/sqlite
- Limits stated by River: "An inherent limitation of SQLite is that it can only be opened by one writer at a time."; "the SQLite driver isn't as fast as Postgres, benchmarking at about 1/4 the speed"; "every running River client needs read/write access."; batch operations "only work one row at a time". https://riverqueue.com/docs/sqlite
- Driver set-up: Go driver `riversqlite` over `modernc.org/sqlite` ("a modern, pure Go option"), pool limited with `SetMaxOpenConns(1)`. https://riverqueue.com/docs/sqlite
- Package doc: "This driver is currently in early testing." Version shown v0.51.2 (published Nov 9, 2026), a v0 module. https://pkg.go.dev/github.com/riverqueue/river/riverdriver/riversqlite
- LISTEN/NOTIFY equivalent on SQLite: **Not established** (the SQLite page does not mention it).

### B3. Delivery semantics and worker death

- "Once a job is inserted, it will be worked with _at least once_ semantics." "Jobs should be written so that they can still succeed even if run multiple times." https://riverqueue.com/docs/reliable-workers
- The rescuer, for jobs that look stuck "such as after a hardware crash, when a worker blocks indefinitely or ignores cancellation": re-enqueues them or discards them when out of attempts. Default threshold in Go: "Defaults to one hour, or `JobTimeout` plus one hour in case `JobTimeout` has been configured to be larger than one hour." "jobs that are still running after this duration will be rescheduled to run again." https://riverqueue.com/docs/maintenance-services
- "Jobs left in `running` state are later recovered by River's" rescuer. https://riverqueue.com/docs/stuck-jobs
- Retries: "The default limit is 25 total attempts, including the initial run."; "The last retry comes about three weeks after the first time a job is worked." https://riverqueue.com/docs/job-retries

### B4. Duration

- "The default job timeout is one minute." Workers must respect `ctx.Done()`; "Disabling timeouts also stops ordinary timeout-based rescue of abandoned jobs." https://riverqueue.com/docs/reliable-workers
- A job running past the rescue threshold is rescheduled, so River's own cap on one job is set by `JobTimeout` plus the rescuer threshold (above). https://riverqueue.com/docs/maintenance-services
- Heartbeat or lock renewal for long jobs: **Not established** (none described in the pages read).

### B5. Placement features

- Queues: "River can be configured with any number of queues"; all share one table; workers "only select jobs for queues they're configured to handle." "Configure each queue with its own concurrency limit" (`MaxWorkers`). https://riverqueue.com/docs/multiple-queues
- Priority is done with queues: "configure a high priority queue with its own set of workers." https://riverqueue.com/docs/multiple-queues
- Running different queues on different machines: **Not established** (page covers components sharing a database, not machines).
- Cancel: "Running jobs are marked for cancellation rather than immediately finalized." "The client executing the job then signals or interrupts its worker." In Go the worker's `context.Context` is cancelled. https://riverqueue.com/docs/cancelling-jobs
- Features list: scheduled jobs, snoozing, periodic and cron jobs, unique jobs, transactional job completion, web UI. https://github.com/riverqueue/river

### B6. Operations and measurements

- Workers connect to the database; every client needs read/write access to the SQLite file. https://riverqueue.com/docs/sqlite
- **Measured (project):** "River works about 46k jobs/sec" on "commodity laptop (8-core 2022 M2 MacBook Air) with 2,000 worker goroutines", Postgres. The authors: "We don't recommend interpreting these measurements as gospel, or using them for direct comparisons to other systems." Not for this workload. https://riverqueue.com/docs/benchmarks

## C. asynq (hibiken/asynq, Go + Redis)

- Description: "Simple, reliable, and efficient distributed task queue in Go". Redis "Version 4.0 or higher is required." Licence MIT. https://github.com/hibiken/asynq
- Status as written in the README: "The library is relatively stable and is currently undergoing moderate development" with "less frequent breaking API changes"; "Current major version is zero (v0.x.x)". No "maintenance mode" notice exists in the README. "Some of the lua scripts in this library may not be compatible with Redis Cluster." https://raw.githubusercontent.com/hibiken/asynq/master/README.md
- Maintenance as seen in the repository: issue "Need maintainers?" (#626, Mar 2023) exists; in issue #836 a maintainer wrote "The project is actively maintained, just relatively slower with the release process."; larger changes "need approval from the lead maintainer". Latest releases listed: v0.26.0 (03 Feb, year not shown), v0.25.1 (11 Dec), v0.25.0 (01 Nov). https://github.com/hibiken/asynq/issues?q=maintainer ; https://github.com/hibiken/asynq/issues/836 ; https://github.com/hibiken/asynq/releases
- Features: "Guaranteed at least one execution of a task", "Automatic recovery of tasks in the event of a worker crash", weighted and strict priority queues, "Allow timeout and deadline per task", unique tasks, periodic tasks, pausing queues, Redis Sentinel, web UI and CLI to "inspect and remote-control queues and tasks". https://raw.githubusercontent.com/hibiken/asynq/master/README.md
- Worker death (v0.22.0 notes): "This version improves worker crash recovery by introducing a concept of worker lease for an active task." "`Server` now recovers tasks with an expired lease. Recovered tasks are retried/archived with `ErrLeaseExpired` error." https://github.com/hibiken/asynq/releases
- Worker death (Redis's guide, run with Asynq 0.26 on Redis 7.2): "A server holds a lease on each active task and renews it while it works." After `kill -9` of a server running a 60 s task "the task stayed in the `active` state for about a minute, because its lease had to expire", moved to `retry` then `pending`, and "a new server ran it again about 73 seconds after the kill." "Asynq therefore re-runs a task whose server dies, but only after a delay and at least once. Write handlers so that running one twice is safe." The lost run counts as a failed attempt. **Measured** (one test, Redis's, not this workload). https://redis.io/docs/latest/integrate/asynq/
- Pollers: scheduled and retried tasks move on a poll "about every 5 seconds"; "Don't rely on `ProcessIn` or on the retry delay for sub-5-second accuracy." https://redis.io/docs/latest/integrate/asynq/
- Queues: tasks go to a named queue (`asynq.Queue` option; default "default"); weights give priority shares; `StrictPriority: true` processes higher queues first. https://github.com/hibiken/asynq/wiki/Queue-Priority
- Max duration of one task, lease length default, remote cancellation of an active task, per-machine routing: **Not established** (not found in pages read).
- Needs Redis; the Redis licence and security notes in A1 and A6 apply.

## D. NATS JetStream

### D1. What must run, licence, embedding

- Licence: "NATS server and its clients are open source under the Apache 2.0 license"; "NATS is a Cloud Native Computing Foundation Graduated Project." https://nats.io/about/ ; server repo: "distributed under the Apache Version 2.0 license". https://github.com/nats-io/nats-server
- One binary: `nats-server` is "The single binary that does all message routing"; clustering and JetStream are enabled "through configuration on the same `nats-server`". https://docs.nats.io/running-a-nats-service/clients
- JetStream is switched on with `nats-server -js` ("The `-js` flag turns on JetStream."). Streams default to file storage; memory storage is "lost on restart". https://docs.nats.io/learn/jetstream/your-first-stream.md ; https://docs.nats.io/nats-concepts/jetstream/streams
- Embedding in Go, from source comments in `nats-server`: `NewServer` "will setup a new server struct after parsing the options"; `Start` "this will not block"; `ReadyForConnections` "returns `true` if the server is ready to accept clients"; `InProcessConn` "returns an in-process connection to the server, avoiding the need to use a TCP listener for local connectivity within the same process." https://raw.githubusercontent.com/nats-io/nats-server/main/server/server.go ; the `Options` struct has fields `JetStream bool`, `StoreDir string`, `DontListen bool`. https://raw.githubusercontent.com/nats-io/nats-server/main/server/opts.go
- From the NATS blog: "the server can be imported as a package" (a k3s backend project embeds it). That post does not say the embedded server runs JetStream. https://nats.io/blog/exploring-nats-as-a-backend-for-k3s/
- An official how-to page on embedding the server with JetStream: **Not established** (searched docs.nats.io and the repository README; the pkg.go.dev page was too long to read fully).

### D2. Delivery semantics and worker death

- JetStream "adds a persistence layer on top, giving you at-least-once delivery"; "if a message isn't acknowledged in time, the server redelivers it". https://docs.nats.io/nats-concepts/jetstream
- Ack responses: ack, nak, term, in-progress. `AckWait`: "Its length is AckWait, and it defaults to 30 seconds."; no answer means "the server treats it as a silent failure and redelivers"; in-progress "resets the AckWait timer so a long job doesn't trip redelivery." https://docs.nats.io/learn/jetstream/acknowledgment.md
- `MaxDeliver`: "The default is `-1`, which means no limit." "A message can be redelivered forever." When the limit is hit "the server removes it from the consumer's pending list and never delivers it again"; the message stays in the stream; "JetStream has no built-in dead-letter queue" (a max-deliveries advisory is published). https://docs.nats.io/learn/jetstream/acknowledgment.md
- Worker death, in NATS's words: "the server can't tell a crash from slow work; it only knows the ack hasn't come." The message waits until `AckWait` (30 s by default), then goes to a surviving worker, "so the order may be processed twice if the first worker had already finished it". The page recommends keying side effects by `order_id`. https://docs.nats.io/learn/jetstream/worker-pool.md
- Exactly-once: the delivery pages describe at-least-once only. Double ack: "The client sends the ack and waits for the server to confirm it before treating the message as done."; "Reach for it when reprocessing a message would be harmful and you can't make the handler idempotent"; "A plain ack is fire-and-forget." https://docs.nats.io/learn/jetstream/delivery-and-acknowledgment.md
- Publish-side dedup: tag publishes with `Nats-Msg-Id`; "The duplicate-tracking window is two minutes by default"; "The tracking window is a stream setting, not a header"; a network timeout "means no confirmation, not no write." The page does not use the term "exactly-once". https://docs.nats.io/learn/jetstream/publishing.md
- Storage guarantee: "one `PubAck` per message, at-least-once storage." https://docs.nats.io/learn/jetstream/advanced-publishing.md

### D3. Work queues, duration, placement

- `WorkQueue` retention: "WorkQueue keeps a message only until one consumer acks it." "The first ack removes the message for everyone." Two unfiltered or overlapping consumers fail with "multiple non-filtered consumers not allowed on workqueue stream" / "filtered consumer not unique on workqueue stream". "A worker pool sharing one consumer is the other valid setup". https://docs.nats.io/learn/jetstream/retention-policies.md
- Worker pool: "Point several processes at the same `shipping` consumer and they share the load." "Distribution follows demand: a faster worker pulls more often". https://docs.nats.io/learn/jetstream/worker-pool.md
- Per-machine routing is done by giving each consumer a subject filter ("Give each consumer a filter that splits the subjects between them"). https://docs.nats.io/learn/jetstream/retention-policies.md
- `MaxAckPending`: "the limit on un-acked messages the consumer hands out before it waits for acks"; default 1000. https://docs.nats.io/nats-concepts/jetstream/consumers
- Pull consumers: "The `shipping` consumer delivers the next message to a worker when the worker asks." (https://docs.nats.io/nats-concepts/jetstream/consumers); fetch and consume take a `batch` and `expires`; per the tool's summary of the pull-consumer page, a larger batch means more work is lost if a worker dies mid-batch. https://docs.nats.io/learn/jetstream/pull-consumers.md
- Long jobs: in-progress ack keeps resetting `AckWait` (above). Maximum job duration: **Not established**.
- Cancelling a running job remotely: **Not established** (not a JetStream concept in the pages read).

### D4. Leaf nodes, replication, measurements

- Leaf node: "a NATS server that opens an outbound connection to a remote NATS system". "The factory's server dials the `east` cluster; `east` never dials the factory." "no inbound firewall rule, no public address on the factory side." A leaf with its own JetStream needs a domain ("a name that isolates one JetStream system from another across a leaf link"). https://docs.nats.io/running-a-nats-service/configuration/leafnodes
- Replication: `R=3` "REQUIRES a clustered NATS deployment of at least three servers"; a group "keeps working as long as more than half its members are reachable". Single server: "Lose that one server and you lose the stream: every message and every consumer's position." https://docs.nats.io/learn/jetstream/surviving-node-loss.md
- Security of the NATS port and TLS/auth settings: **Not established** (not read).
- **Measured:** none found. The `nats bench` page has no JetStream throughput numbers. https://docs.nats.io/using-nats/nats-tools/nats_cli/natsbench

## E. Temporal

### E1. What must run, licence, languages

- The Temporal Service is "the group of services, known as the Temporal Server, combined with Persistence and Visibility stores". https://docs.temporal.io/temporal-service
- Server services: Frontend ("for rate limiting, routing, authorizing"), History, Matching ("hosts Task Queues for dispatching"), Worker Service. "For production, Temporal recommends running each service independently"; "starting with one History Service process for every 500 History Shards." https://docs.temporal.io/temporal-service/temporal-server
- Persistence: "Cassandra v3.11, v4.0, and 5.0.4 and later", "PostgreSQL 13.18, 14.15, 15.10 and 16.6", "MySQL v5.7 and v8.0". "Temporal also supports SQLite v3.x persistence, but this is meant only for development and testing, not production usage." https://docs.temporal.io/temporal-service/persistence
- Dev server: `temporal server start-dev`; "By default, Workflow Executions are lost when the server process dies."; `--db-filename` adds persistence; "The development server is not intended for production use." https://docs.temporal.io/cli/server ; https://github.com/temporalio/temporal
- Licence: MIT. https://github.com/temporalio/temporal
- SDKs: ".NET, Go, Java, PHP, Python, Ruby, Rust, and TypeScript." https://docs.temporal.io/evaluate/why-temporal
- Whether the Temporal server can be embedded in one Go binary for production: **Not established** (only the dev server was found).

### E2. Durable execution, worker death, retries

- "Durable Execution runs your code to completion across crashes and outages." A Workflow "may take seconds or months"; if a Worker crashes "another Worker replays the Event History and resumes where execution stopped". https://docs.temporal.io/evaluate/why-temporal
- Event History is "a complete and durable log of everything that has happened in the lifecycle of a Workflow Execution". https://docs.temporal.io/encyclopedia/event-history
- Long workflows use Continue-As-New: a long Event History "could even generate more Events than allowed by the Event History limits". https://docs.temporal.io/workflow-execution/continue-as-new
- Activity retry: "If an Activity attempt fails, it is automatically retried using its Retry Policy." Default policy: "exponential backoff with a 2.0 backoff coefficient, starting with a 1-second initial interval"; attempts "unlimited"; maximum interval "100 times the Initial Interval". "Unlike Activities, Workflow Executions do not retry by default." https://docs.temporal.io/activities ; https://docs.temporal.io/encyclopedia/retry-policies
- Worker death: "Since Temporal can't tell when a Worker crashes, Workflows rely on the start_to_close timeout" to decide an Activity is inactive; Temporal "guarantees that the Activity will be observed as completed exactly once," but "the Activity may be executed multiple times and may even partially complete more than once during this process." If a Worker finishes then crashes before reporting, "the Activity will be retried." https://docs.temporal.io/activity-definition
- "Temporal recommends that Activities be idempotent." https://docs.temporal.io/activity-definition

### E3. Duration, heartbeats, placement

- Timeouts: Schedule-To-Close default "infinity"; Start-To-Close "has no default value of its own" and "We strongly recommend setting a Start-To-Close Timeout."; Schedule-To-Start default "infinity". https://docs.temporal.io/encyclopedia/detecting-activity-failures
- Heartbeat: "a ping from the Worker that is executing the Activity to the Temporal Service"; Heartbeat Timeout is "the maximum time between Activity Heartbeats"; "If this timeout is reached, the Activity Task fails and a retry occurs if a Retry Policy dictates it."; heartbeat payload can "save Activity Execution progress"; "Activity Cancellations are delivered to Activities from the Temporal Service when they Heartbeat." https://docs.temporal.io/encyclopedia/detecting-activity-failures
- Long Activities, Temporal's own words: "For long-running Activities, we recommend using a relatively short Heartbeat Timeout and a frequent Heartbeat." https://docs.temporal.io/encyclopedia/detecting-activity-failures ; "For Activities, your operation should either complete within a few minutes or it should heartbeat." https://docs.temporal.io/activity-definition
- Retry resumes from the last checkpoint: "The last recorded Heartbeat details are made available to the Activity function on the next attempt." https://docs.temporal.io/activities
- Task Queue: "a lightweight, dynamically allocated queue that one or more Worker Entities poll for Tasks"; "Workers poll for Tasks in Task Queues via synchronous RPC."; "A Worker Process polls for a message only when it has spare capacity"; "Task Routing, which is the routing of specific Tasks to specific Worker Processes or even a specific process." https://docs.temporal.io/task-queue
- Standalone Activities: "Temporal's durable job queue", status Public Preview, Go APIs "marked Experimental". https://learn.temporal.io/tutorials/go/standalone-activities/
- Measured throughput: **Not established** (not looked up).

## F. SQLite-backed Go queues and the "jobs table in the app database" pattern

### F1. goqite (maragudk/goqite)

- "a persistent message queue Go library built on SQLite and inspired by AWS SQS" (README adds "but much simpler"); "Also supports Postgres!"; "No non-test dependencies. Bring your own SQL driver." MIT. https://github.com/maragudk/goqite
- Semantics: messages "are guaranteed to not be redelivered before a timeout occurs."; "Message timeouts can be extended, to support e.g. long-running tasks."; the example says to delete the message "so it doesn't get redelivered"; redelivery timeout and maximum receive count are configurable. Whether a message can be received twice is implied, not stated: "The page does not explicitly say a message can be received more than once" (tool's note). https://raw.githubusercontent.com/maragudk/goqite/main/README.md
- **Measured (project, "just for fun"):** about 12,500 messages/s with 16 parallel producers/consumers, about 18,500 with one; "SQLite only allows one writer at a time, so the parallelism just creates write contention." Not for this workload. https://raw.githubusercontent.com/maragudk/goqite/main/README.md
- Maturity statement: none found. https://github.com/maragudk/goqite
- Which Go SQLite driver the tests use: **Not established**.

### F2. backlite (mikestefanello/backlite)

- "Type-safe, persistent, embedded task queues and background job runner w/ SQLite. Web monitoring UI included." Works with any SQLite driver; only `modernc.org/sqlite` is bundled for tests. MIT. https://github.com/mikestefanello/backlite
- Semantics: no explicit at-most/at-least statement; `ReleaseAfter`: "tasks claimed and passed for execution should be added back to the queue if a response was never received"; retries with backoff. "The worker pool is the only way to process tasks; they cannot be pulled manually." https://github.com/mikestefanello/backlite
- Status: "This project is under active development."; "No significant API or schema changes are expected at this time, but it is certainly possible." https://github.com/mikestefanello/backlite

### F3. The jobs-table pattern, from the databases' own docs

- Postgres: "With `SKIP LOCKED`, any selected rows that cannot be immediately locked are skipped. Skipping locked rows provides an inconsistent view of the data, so this is not suitable for general purpose work, but can be used to avoid lock contention with multiple consumers accessing a queue-like table." https://www.postgresql.org/docs/current/sql-select.html
- SQLite has no `SKIP LOCKED` equivalent in the pages read: **Not established** (no page read states one either way).
- SQLite `RETURNING`: "one result row for each database row that is deleted, inserted, or updated"; supported "since version 3.35.0 (2021-03-12)". https://www.sqlite.org/lang_returning.html
- SQLite WAL: "since there is only one WAL file, there can only be one writer at a time."; "All processes using a database must be on the same host computer; WAL does not work over a network filesystem." https://www.sqlite.org/wal.html
- Worker-death handling for a hand-rolled table (lease, timeout, retry): the same pattern appears in each library above (lease/visibility timeout: goqite, backlite `ReleaseAfter`, River rescuer, asynq lease); a documented generic pattern page from SQLite or Postgres: **Not established**.

## G. Primary-source statements on queue versus placement of long-lived workloads

No page read states the distinction in those words; these are each system's own definitions.

- Kubernetes: "Jobs represent one-off tasks that run to completion and then stop." https://kubernetes.io/docs/concepts/workloads/controllers/job/ ; and "Job and CronJob provide different ways to define tasks that run to completion and then stop." while "Deployment is a good fit for managing a stateless application workload on your cluster, where any Pod in the Deployment is interchangeable and can be replaced if needed." https://kubernetes.io/docs/concepts/workloads/
- Kubernetes Job replacement: "The Job object will start a new Pod if the first Pod fails or is deleted (for example due to a node hardware failure or a node reboot)." https://kubernetes.io/docs/concepts/workloads/controllers/job/
- Kubernetes placement is a separate step: "A scheduler watches for newly created Pods that have no Node assigned." The filtering step "finds the set of Nodes where it's feasible to schedule the Pod"; scoring "ranks the remaining nodes"; "The scheduler then notifies the API server about this decision in a process called binding." https://kubernetes.io/docs/concepts/scheduling-eviction/kube-scheduler/
- Nomad: "The `service` scheduler is designed for scheduling long lived services that should never go down." "Service jobs are intended to run until explicitly stopped by an operator." "If a service task exits it is considered a failure and handled according to the job's restart and reschedule blocks." Batch jobs "are short lived, finishing in a few minutes to a few days." "Batch jobs are intended to run until they exit successfully." https://developer.hashicorp.com/nomad/docs/concepts/scheduling/schedulers
- Nomad placement: "In the first phase the scheduler finds nodes that are feasible"; "Scoring is primarily based on bin packing, which is used to optimize the resource utilization." https://developer.hashicorp.com/nomad/docs/concepts/scheduling/scheduling
- Temporal on very long activities: "For Activities, your operation should either complete within a few minutes or it should heartbeat." https://docs.temporal.io/activity-definition

## Comparison table

| System | Extra infrastructure | Language | Worker-death behaviour (project's wording) | Embeddable in one Go binary? | Works with SQLite? |
|---|---|---|---|---|---|
| BullMQ | Redis 6.2+ (or Valkey/Dragonfly) or PostgreSQL 13+ | Node/Bun, Python, Elixir, .NET, Rust, PHP, Go (official, v0) | Stalled job "moved back to the waiting status", "processed again by another worker"; after `maxStalledCount` (1) fails; "double processed" | Go port is a library; it needs Redis or Postgres running separately | No (backends are Redis and PostgreSQL) |
| River | PostgreSQL (primary) or SQLite (experimental) | Go, Java, Ruby, Rust, TypeScript | Rescuer: "jobs that are still running after this duration will be rescheduled to run again" (default 1 h); "at least once" | Yes, library in-process; with SQLite uses `modernc.org/sqlite` | Yes, experimental, "early testing", one writer, about 1/4 Postgres speed |
| asynq | Redis 4.0+ | Go | Lease; expired lease task retried/archived with `ErrLeaseExpired`; "re-runs a task whose server dies ... at least once" | Library; needs Redis | No |
| NATS JetStream | `nats-server` (embeddable per source comments) | Server in Go; many client languages | No ack within `AckWait` (30 s): "redelivers"; "the order may be processed twice" | Server can be imported as a Go package (source comments, k3s blog); official how-to not established | No (file or memory store of its own) |
| Temporal | Temporal Server (Frontend, History, Matching, Worker) plus Cassandra, PostgreSQL or MySQL; Visibility store | SDKs: .NET, Go, Java, PHP, Python, Ruby, Rust, TypeScript | Activity retried per Retry Policy after timeout or missed heartbeat; may "be executed multiple times"; heartbeat details resume | Not established for production (dev server only found) | Only for dev and test, "not production usage" |
| goqite | None beyond the app's database | Go | Message not redelivered before timeout; redelivered after it | Yes, library | Yes (base), also Postgres |
| backlite | None beyond the app's SQLite | Go | `ReleaseAfter` returns claimed tasks to the queue if no response | Yes, library | Yes |
| Plain jobs table | The app's own database | Any | Whatever lease/timeout the app writes | Yes | Yes; one writer at a time, same host only in WAL |

## Not established

- BullMQ Pro licence terms and price.
- BullMQ `lockRenewTime` default; an official recommended `lockDuration` for long jobs.
- Any BullMQ statement of exactly-once delivery (none found); BullMQ throughput numbers.
- Whether BullMQ documents per-machine queue names as a routing pattern; how workers behind NAT connect to Redis/Postgres (client-dials-broker is not stated in a page read).
- River: LISTEN/NOTIFY equivalent on SQLite; heartbeats or lock renewal for long jobs; running queues on separate machines.
- asynq: default lease length, maximum task duration, remote cancellation of an active task, per-machine routing.
- NATS: an official how-to for embedding the server with JetStream (only source comments and a blog line found); JetStream throughput numbers; security/TLS/auth settings; remote cancellation of a running job; maximum job duration.
- Temporal: embedding the server in one production binary; throughput numbers.
- SQLite: any `SKIP LOCKED` equivalent; a generic documented jobs-table pattern page from SQLite or Postgres.
- goqite: which SQLite driver it is tested with; any maturity statement.
- Kubernetes Job: statement that a Job pod may run more than once (page was truncated before that section); Nomad `batch` vs `service` exact scheduling difference beyond the scheduler page's text.
