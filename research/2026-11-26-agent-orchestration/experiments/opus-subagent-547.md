# Adversarial review: PR #547 (175f1ad7c) "warn admins when a workspace reaches its budget warning"

- arm: Opus in-session subagent
- model: claude-opus-5-5 (Opus 5.5, as the harness reports it)
- first action time: 2026-11-26T06:51:52Z
- finish time: 2026-11-26T07:00:18Z
- blinding rules: all kept. I read only 175f1ad7c and its parent. No later commits, no git log/show/blame on later revisions, no gh/GitHub, no team memory, no web.
- what I ran:
  - `go test -count=1 ./internal/push/ ./internal/cost/ ./internal/slack/` at 175f1ad7c: all pass.
  - A throwaway scratch test in `internal/cost`, deleted afterwards (worktree clean), that forces an Admit/SetBudget interleaving. It reproduced finding 1; its output is below.

## 1. Budget state is read outside `publishMu`, so stale states are still published out of order. This corrupts `From` and suppresses a later warning notice (should-fix, high confidence, reproduced)

`internal/cost/service.go:386-395` (Admit), `:369-373` (SetBudget), `:453-464` (announce) and `:467-480` (publish).

`publishMu` only orders the `bus.Publish` calls. Each caller computes `st` with `s.status()` before it takes the lock. A caller holding an older status can therefore publish after a newer one. That event's `From` is the newer state and its `State` is the stale one, and `s.state` is overwritten with the stale value. The commit message says the cost service "publishes budget events in the order their states change"; the code does not do that.

Reproduced interleaving (scratch test, real sqlite store and in-proc bus):
1. Spend is $12 and the cap is $10, so the state is `exceeded`. This is the normal overnight-swarm case where launches keep being refused.
2. `Admit` reads the budget row (cap $10) and is preempted before or inside `SummarizeRunCosts`.
3. An admin runs `SetBudget(limit 100, warn 50)`. It publishes `exceeded -> ok`, which is correct.
4. `Admit` resumes. It computes `exceeded` from the stale $10 row, refuses the launch and publishes `ok -> exceeded`. `s.state` is now `exceeded`, but the true state is `ok`.
5. The next run cost takes spend to $60, which is `warn` under the new budget. `announce` publishes `exceeded -> warn`. In `push.budget` (`internal/push/budget.go:40`), `level(warn) <= level(exceeded)`, so **no admin is told about the warning**.

Observed events:
```
seq=2 from="exceeded" state="ok"       limit=100  reason="budget set to $100.00 ..."
seq=3 from="ok"       state="exceeded" limit=10   reason="new run refused ..."
seq=4 from="exceeded" state="warn"     spend=60 limit=100
```

Effects:
- The reverse ordering (a stale lower state published after a higher one) makes the next announce publish a second `X -> exceeded` rise, and push then sends a duplicate "Budget reached".
- The timeline also shows a bogus `exceeded` after the admin raised the cap.

Fix: compute the status inside `publishMu` (re-read under the lock in `publish`, or have every publisher take the lock before `status()`), so the published state is the latest one.

## 2. A permanent Slack failure is retried every minute for as long as the budget stays exceeded, which can be forever (should-fix, high confidence)

`internal/slack/notify.go:157` returns every `chat.postMessage` error. `internal/push/budget.go:177` treats any sink error as "not done", and `:110` retries after `retry` (1 minute).

Spend is lifetime and never resets (`cost.Evaluate`), so `exceeded` can stand indefinitely. Errors that retrying cannot fix include `channel_not_found`, `is_archived`, `not_in_channel`, `account_inactive`, `invalid_auth` and `token_revoked`. Notice's own doc comment promises it returns an error "only when trying again could deliver n", but none of these errors is classified as permanent.

Result: one goroutine per affected admin loops forever. Every minute it runs a full `SummarizeRunCosts`, `GetWorkspace`, a connector `local()` grant, a `server.info` call and a Slack API call, plus a warning log line. The same mistake in `Need` was bounded by the run's need ending; a budget has no such end. Slack rate-limiting the bot can then hurt the run-need messages that share the token.

Fix: classify permanent Slack errors as done (log them), or cap the retries.

## 3. A pending notice is lost for good on restart, or when push is not subscribed or drops the event, because cost now seeds its state at Start (should-fix, medium confidence)

`internal/cost/service.go:78-95`, `internal/push/budget.go:96-128` and `internal/push/service.go:227-248`.

Push keeps due notices only in memory, waiting out the quiet period while an admin uses a dashboard. Since this change, cost reads the current state at `Start`, so after a restart the rise is never published again. Three scenarios lose a notification:
- **Restart during the quiet period.** `warn` is published at 23:00 while the admin has a dashboard open. The server restarts before the quiet minute ends, for example through the server self-update path. Cost seeds `warn`, the notice is gone, and the admin hears nothing until the cap.
- **Event published before push subscribes.** Services start in registration order. Cost starts and its consumer runs before roughly ten other services (development ... overlap) start, and push subscribes after them. A `run.cost` or terminal-status event handled in that window can cross a threshold. Push is not subscribed yet, and cost already records the new state, so the event is never re-announced.
- **Event dropped by push.** Push deliberately does not replay a dropped event ("costs at most one announcement"). For budgets that one announcement is the whole feature, because cost never re-emits it.

The commit says "server restarts send nothing" as if this were only about duplicates, but the same mechanism drops notices that were never delivered.

## 4. Cost `Start` now fails the whole server start on any per-workspace store error (minor, medium confidence)

`internal/cost/service.go:83-91`.

Before this change, `Start` only subscribed. Now any non-`ErrNotFound` error from `GetWorkspaceBudget`, or a `SummarizeRunCosts` error for any one budgeted workspace, makes `Start` return an error. `startServices` (`internal/server/services.go:101-107`) aborts server startup on that error. A single bad row or a transient lock error at boot therefore takes down the whole server, all for a cache that only decides whether to notify.

The startup work also runs serially (N workspaces x a full cost summary each). The cost subscription is already open during that time, so a long boot widens the window in which its buffer can overflow and trigger the replay-from-`lastSeq`=0 path in `consume`.

Seeding the state lazily, or logging and leaving the state unset, would avoid both problems.

## 5. The admin role is checked when the event arrives, not when the notice is sent (minor, medium confidence)

`internal/push/budget.go:49` and `:142-180`.

The admin list is taken once per event. `sendBudget` re-reads the budget but not the member. An admin demoted to collaborator or viewer during the quiet period, or during the minute-long retry loops in findings 2 and 3, still gets the "Budget reached" push and Slack DM. A deleted member is mostly safe, because their subscriptions and Slack link are gone. The run-need path, by contrast, decides from the run as it stands when it sends.
