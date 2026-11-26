# Adversarial review: PR #716 (a3171a811e29f26ff1639423f618f94ea8519301)

- arm: Opus sub-session
- model: claude-opus-5-5 (Opus 5.5, as the harness reports it)
- first action time: 2026-11-26T06:51:41Z
- finish time: 2026-11-26T06:55:06Z
- blinding rules: kept all of them. I read only the commit and its parent
  (`git show`/`git diff` of a3171a811 and file reads at that checkout). No
  later commits, no GitHub, no team memory search, no web.
- what I ran: nothing was built or tested. This was a static read of
  `internal/assistant/{card,confirm,assistant,rules,preference}.go`,
  `internal/assistant/tools/tools.go`, `internal/store/assistant_cards.go`,
  migration v104, `internal/server/server.go` (Run ordering),
  `internal/acphost` turn numbering, and the web panel/action diffs.

Paths and lines are at a3171a811. Findings are ranked most severe first.

## 1. A crash between the Done record and the card update makes a successful call show as Failed

- Where: `internal/assistant/confirm.go:369-375` (`noteOutcome`: the
  transcript record is written first, then `finishCard`) together with
  `internal/assistant/card.go:586-606` (`Resume`, the running branch).
- Scenario: an approved call (for example `run.launch` or `run.pr.merge`)
  returns success. `actionCaller.record` → `noteOutcome` writes
  `Done: …` into the conversation. The server stops (crash, OOM or deploy)
  before `finishCard` moves the card from `running` to `succeeded`. On the
  next start `Resume` finds the card still `running` and writes a second
  record for the same action id (`a.ID = row.ActionID`):
  `Failed: the server restarted while it ran; check before retrying`.
  `latestRecords` in `web/src/lib/assistant-actions.ts` keeps the latest
  record per action id, so the panel shows Failed for a call that
  succeeded. The member, told to "check before retrying", is likely to ask
  again, which launches a second session or tries a second merge.
- Fix direction: before failing a running card in `Resume`, check the
  transcript for an outcome record with that action id. Or write the card's
  terminal state before, or together with, the transcript record.
- Severity: should-fix. Confidence: medium. The window is narrow, but the
  result is a wrong outcome that invites a duplicate action.

## 2. A card approved right after a restart runs before run recovery finishes

- Where: `internal/assistant/card.go:375-378` (`decide` →
  `s.spawn(func() { s.runParked(m, id, nil) })`) against
  `card.go:576-577` (`Resume` passes `s.cfg.Runs.RecoveryReady()`).
- Scenario: the server restarts with a pending card (state survives the
  restart). `Server.Run` calls `Resume`, then starts `sched.Start` and
  `ssh.Serve` in parallel (`internal/server/server.go:700-744`). The member
  approves the card from the phone during the first seconds of the restart.
  `decide` finds no waiter and starts `runParked` with `ready == nil`, so
  `run.kill`, `run.inject` or `run.launch` runs against runs the scheduler
  has not reconciled yet. For a card that was already approved, `Resume`
  waits for `RecoveryReady()` precisely to avoid this. The live approve path
  does not wait, so whether the wait happens depends on whether the card was
  approved before or after the restart.
- Fix direction: have `runParked` always wait on
  `s.cfg.Runs.RecoveryReady()`. The channel is already closed in steady
  state, so this costs nothing.
- Severity: should-fix. Confidence: medium. I did not check how each
  scheduler method behaves before recovery.

## 3. A server-run card can run with no record and stay "running" until the next restart

- Where: `internal/assistant/card.go:548-560` (`runParked`: `noteAction`,
  then `m.actions.hold(conv, …)`, then `srv.Call`) and
  `internal/assistant/confirm.go:403-416, 428-431` (`heldActions.take`
  drops entries of a discarded conversation; `actionCaller.Call` then
  passes a non-reversible method straight through with no record).
- Scenario: `runParked` claims the card (`approved` → `running`) and writes
  the Approved record. The member then presses New conversation, and
  `resetLocked` cancels `conv`. `withdrawAll` skips `running` cards. Next,
  `srv.Call` → `take` drops the held action because `conv.Err() != nil`.
  `actionCaller.Call` makes the real call anyway (`!ok &&
  !Reversible(method)`) and records nothing. `finishCard` is never called,
  so the card stays `running`, and it is hidden because `openCards` filters
  by generation. On the next restart `Resume` fails it with "restarted while
  it ran", which is false: the call ran, after the reset, with no outcome
  anywhere. The docs say a new conversation withdraws every open card.
- The same keying by method and params also lets identical concurrent calls
  swap their held actions, for example a dashboard card and a connector card
  with equal params; `raise` deduplicates only within one door. Each outcome
  then lands on the other card.
- Severity: should-fix. Confidence: medium.

## 4. A call that shares a near-expiry card gets almost no wait

- Where: `internal/assistant/card.go:190-215` (`raise` reuses any pending
  card with the same call and never looks at `ExpiresAt`) and
  `internal/assistant/confirm.go:91` (the deadline is
  `time.Until(c.ExpiresAt) + time.Minute`).
- Scenario: a card is raised at T. The turn ends, and the card stays pending
  until T+10m. At T+9m50s the member asks again and the assistant repeats the
  same call. `raise` attaches the new call to the old card, which expires
  10 s later. The member sees the card, reaches for Approve, and it expires.
  The docs promise "The call waits for the card for 10 minutes". A pending
  card already past `ExpiresAt` is reused too, when its expiry write failed
  and is waiting for the one-minute retry in `expireAt`, and `Confirm` then
  refuses it as expired.
- Fix direction: skip reuse when the remaining time is short or already
  past.
- Severity: minor. Confidence: high.

## 5. Approved or running cards disappear from the normal panel, and their updates stop streaming

- Where: `web/src/components/assistant/panel.tsx`. `waiting` is
  `confirmations.some(cardPending) || …`. In the ready and running states,
  cards render only inside `dock={waiting && (…)}`. `streamable` is also
  gated on `waiting` when the panel is closed.
- Scenario: the member approves a parked card while the assistant is idle.
  The card moves to `approved`, then `running`, and the panel stops showing
  it, because the dock renders only when something is pending. The docs say
  "An approved card reads Approved, running… until its outcome is in the
  conversation". That only holds in the setup and failed states, which
  render `cards` unconditionally. With the panel closed and no pending card,
  the stream drops, so the outcome does not arrive until the next refresh.
- Severity: minor. Confidence: medium.

## 6. One undecodable card row breaks `assistant.status` for the member

- Where: `internal/assistant/card.go:286-289` (`openCards` returns the first
  `cardOf` error). `Status` (`assistant.go:307-310`) returns that error.
- Scenario: one open row of the current generation has `card` or `origin`
  JSON that does not decode, for example after a field type change in
  `AssistantConfirmation` or a hand-edited database. Every
  `assistant.status` call fails, so the whole panel is broken, not just that
  card. The only way out is New conversation, which bumps the generation.
  `Resume` already logs and skips bad rows; `openCards` should do the same,
  or fail only that card.
- Severity: minor. Confidence: high on the behavior, low on how likely it is.

## Considered and not reported

- Races among `wait`, `answer`, `leave` and `release`: I traced
  approve-vs-hangup, supersede-vs-hangup and approve-during-reuse. Each path
  either hands the approval to exactly one runner or ends with
  `runParked`'s claim (`approved` → `running` as one conditional UPDATE),
  so a card runs at most once.
- Reset racing a claim: `claim` checks the generation, then does a
  conditional move. `withdrawAll` moves only `pending` and `approved`.
  Either the claim wins and the call runs, which is finding 3's tail, or the
  withdraw wins and nothing runs.
- `Confirm` authorization: cards are looked up by `(member_id, id)` from the
  authenticated member. The connector's `aether_decide_approval` is for
  run-input permissions, not `assistant.confirm`.
