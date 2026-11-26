# Review experiment: PR #547 (175f1ad7c) — warn admins at budget warning/cap

- arm: Opus sub-session
- model: claude-opus-5-5 (Opus 5.5, as the harness reports it)
- first action time: 2026-11-26T06:51:54Z
- finish time: 2026-11-26T06:54:18Z
- blinding rules: kept every one (no commits after 175f1ad7c read, no gh/GitHub, no team-memory search, no web)
- ran: `go test ./internal/push/ ./internal/cost/` at 175f1ad7c (both pass); otherwise code reading of the diff and surrounding code

## Findings (most severe first)

### 1. Budget status is read outside publishMu, so a stale state can be published last and desync `s.state`, which then suppresses later notices
- where: internal/cost/service.go:467-473 (publish), callers at :370 (SetBudget), :387/:395 (Admit), :453/:464 (announce)
- `publishMu` only serialises the map update and the bus publish; each caller computes `status()` before taking it. Two callers can read in one order and publish in the other, so the last event published (and `s.state`) holds the older state.
- scenario A: workspace is `exceeded`. A launch calls Admit, which reads `exceeded`. An admin raises the cap with SetBudget, which reads `ok` and publishes ok (From=exceeded). Admit then publishes the refusal with State=exceeded, From=ok. Push sees a rise and queues "Budget reached". sendBudget re-reads and sees ok, so that notice is dropped. But `s.state` is now `exceeded` while the truth is `ok`, and the timeline's last budget event says exceeded. Later, spend under the new cap crosses the warning: announce publishes warn with From=exceeded, which is a fall, so there is no warning notice. If spend instead jumps straight past the new cap, announce sees exceeded == exceeded and publishes nothing: no cap notice and no timeline entry.
- scenario B: announce (consumer goroutine) reads `warn` while SetBudget raising the cap reads `ok` and publishes first. The stale warn is published last and `s.state`=warn. When spend reaches the new warning, `changed` is false and nothing is sent.
- fix: compute status under publishMu (or re-read status inside publish).
- severity: should-fix (the feature's purpose is to never miss this notice). confidence: high on the logic; the window is narrow (a concurrent admin edit or refused launch).

### 2. Cost service Start now fails the whole server when any one workspace's budget status can't be read
- where: internal/cost/service.go:78-95; internal/server/services.go:101-106
- Start now lists every workspace and runs SummarizeRunCosts on each budgeted one. Any error (a transient SQLite busy error, a row that fails to scan, ctx deadline on a large install) returns an error from Start. startServices then aborts server startup. Before this change, cost Start could only fail on subscribe.
- Startup also does O(workspaces) full cost summaries serially, inside the setup ctx.
- severity: should-fix (a seeding nicety for de-duping notices becomes a hard boot dependency). confidence: medium-high.

### 3. A dropped budget event loses its notice permanently
- where: internal/push/service.go:230-245 (consume), internal/push/budget.go:39-42
- The push consumer never checks `sub.Dropped()` and never replays. The comment's premise is that "a dropped event costs at most one announcement" because the next run event re-reads the run. That does not hold for budgets. If the subscriber overflows and drops the ok->warn or warn->exceeded event, the cost service's `s.state` already records the new state and publishes nothing more until the state changes again. No admin is ever told, which defeats the "overnight swarm" goal from the PR body.
- severity: should-fix. confidence: medium (needs a slow or overflowed push subscriber).

### 4. A late, out-of-order handoff after the watcher exits can send a stale warning after the cap notice
- where: internal/push/budget.go:39-62, 78-96, 100-128
- Each budget event spawns its own ListMembers goroutine, and those goroutines retry independently (`retry`). The seq dedupe lives only in a live watcher (`due.seq`) or in the 1-slot channel. Sequence: warn (seq 10) has a failing ListMembers and retries. exceeded (seq 11) is delivered and sent, and its watcher exits and deletes itself from `notices`. An admin then raises the cap, so the state is warn again; a fall sends nothing. The retried seq-10 goroutine finally runs due(): there is no watcher, so a new one starts with due.seq=0 and accepts seq 10. sendBudget sees state==warn and sends "Budget warning". That repeats a threshold the admin was already told about (and has just acted on), out of order.
- severity: minor. confidence: medium.

### 5. Role is checked only when the event arrives, not at send time
- where: internal/push/budget.go:49 vs sendBudget :143-180
- The admin filter runs once, in budget(). The watcher can wait the quiet period or retry for a long time. sendBudget does not re-check that the member is still an admin and not removed. A member demoted meanwhile still receives workspace spend and limit on their devices and Slack DM.
- severity: minor (information to a just-demoted member). confidence: high on the code path.

### 6. Slack Notice retries can post duplicate DMs
- where: internal/slack/notify.go:157; internal/push/budget.go:177 and the retry loop at :106-109
- Notice always posts a new message. watchBudget retries until Notice returns nil. If chat.postMessage succeeds at Slack but the response errors (a timeout or a 5xx after accept), every retry posts another DM. Need edits one message per run, but Notice has no stored message to dedupe against.
- severity: minor. confidence: medium.

### 7. Server wiring depends on file-name init order
- where: internal/server/svc_push.go:18,35
- `Budgets: d.SSH.Services.Costs` works only because svc_cost.go's builder runs before svc_push.go's, which the comment attributes to Go init order. That order depends on file names as presented to the compiler. If either file is renamed, or the cost builder is skipped, push.New gets a nil Budgets and returns "Budgets ... required", which fails server construction. That is fragile but fails loudly, not silently.
- severity: minor. confidence: high that it is order-dependent; low impact today.
