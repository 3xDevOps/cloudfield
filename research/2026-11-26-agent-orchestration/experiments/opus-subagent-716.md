# Review of PR #716 (a3171a811) - durable approval cards

- arm: Opus in-session subagent
- model: claude-opus-5-5 (Opus 5.5, as the harness reports it)
- first action time: 2026-11-26T06:51:39Z
- finish time: 2026-11-26T07:00:52Z
- blinding rules: all kept. Read only a3171a811 and its parent (`git diff HEAD^ HEAD`, files at HEAD). No later commits, no `gh` or GitHub, no team memory, no web.
- what I ran: `go test -p 2 ./internal/assistant/ ./internal/store/ -run 'Card|Confirm|Park|Resume' -count=1`, which passed. No scratch tests were written. Every finding below comes from reading the code.

## Findings (most severe first)

### 1. A late approval refuses any card whose summary has live data in it: run.close, run.pr.merge, watch.delete, and any session whose title changes
- Where: internal/assistant/card.go:541-545 (`runParked` re-summarizes and requires `fresh.Summary == c.Summary`). The changing data comes from:
  - internal/sshd/close_check.go:161: the close warning "...when Aether last checked <timestamp>" goes into the run.close summary (internal/assistant/tools/act.go:98-110).
  - internal/assistant/tools/act.go:189-197: the run.pr.merge summary includes "N failing checks, N pending checks, N reviewers requesting changes".
  - internal/assistant/tools/act.go:285-290: the watch.delete summary includes "its N notices".
  - internal/assistant/tools/tools.go:961-966: `runLabel` uses `run.Title`. Every run-targeting summary embeds it.
- Failing scenario:
  1. The assistant asks to merge a PR while CI runs. The card reads "...1 pending checks...".
  2. The turn ends, or the member presses Stop, or the server restarts.
  3. The member approves 5 minutes later from their phone. By then CI has finished.
  4. `runParked` claims the card, re-summarizes and gets "0 pending checks". It records "The card changed after you approved it, so nothing was run" and fails the card.
  - run.close fails the same way when the PR poller has written a newer "last checked" time.
  - watch.delete fails once a new notice arrives.
  - Any run card fails once the session's title is set or changes.
- Consequence: the PR's headline promise, approving after the turn ended, from another device or after a restart, fails for these methods. For run.close and title changes the refusal is spurious, because nothing the member decided on has changed.
- Secondary effect: `raise` dedupes on the same summary string (card.go:195-196). A retry of such a call after the data moved opens a second card for the identical call instead of reusing the first.
- Severity: should-fix. Confidence: high on the mechanism, medium on how often it fires (it depends on the poll cadence).

### 2. A failed reset withdraws the cards of a conversation that survives
- Where: internal/assistant/assistant.go:704-712. `withdrawAll` runs before `DiscardAssistantState` and `ResetAssistantSession`.
- Failing scenario:
  1. The member has pending or approved cards and presses New conversation.
  2. `withdrawAll` moves every pending and approved card to withdrawn. Their calls get `errSessionEnded`: "a reset discarded the conversation".
  3. `DiscardAssistantState` or `ResetAssistantSession` then fails, for example a host or store error.
  4. The reset returns an error. The generation is unchanged and the conversation lives on, but its approvals are gone for good. An approved card that had not started is also withdrawn and never runs.
- Severity: minor. Confidence: medium.

### 3. A confirmed call can run with no record during a reset, leaving its card stuck in "running"
- Where:
  - internal/assistant/card.go:548-560 (`runParked`: noteAction, then hold, then `srv.Call`)
  - internal/assistant/confirm.go:117-126 (the live path does the same)
  - internal/assistant/confirm.go:403-416 (`take` drops held actions of a cancelled conv)
  - internal/assistant/confirm.go:428-430 (`!ok && !Reversible` passes straight to `Caller.Call` with no record)
- Failing interleaving:
  1. A late approval's `runParked` claims the card (approved to running) and records "runs now" in conv.
  2. A reset now runs `endConv()`. `withdrawAll` leaves running cards alone.
  3. `srv.Call` reaches `actionCaller.Call`. `take` finds the held action's conv cancelled and drops it, so `ok` is false.
  4. The call still runs through the raw caller, after the member reset the conversation. No outcome is recorded and `finishCard` never runs.
  5. The card stays `running` until the next server start, when Resume fails it with "the server restarted while it ran" even though the call may have succeeded.
- The window is narrow. The pass-through existed before this PR, but this PR adds the stuck durable claim.
- Severity: minor. Confidence: medium.

### 4. A call told "expired" can still run later on the server
- Where: internal/assistant/confirm.go:91-103 and card.go:403-416.
- Failing scenario:
  1. The store refuses the expiry write. `expireAt` then retries every minute.
  2. The waiting call hits its `ExpiresAt + 1m` deadline and `stopWaiting(errExpired)` returns "the member did not answer before the card expired" to the model.
  3. The card is still pending. `Confirm`'s expiry guard (card.go:329) is the only thing between a later approval and the call.
  4. Once the expiry write keeps failing, any approval in that gap goes through `runParked` and runs a call the model was told had expired.
  5. If the model acted on "expired", for example by doing the work another way, the action now happens twice.
- Severity: minor. Confidence: medium.

### 5. The panel hides approved and running cards unless a pending card is also open
- Where: web/src/components/assistant/panel.tsx:177-185. The dock renders only when `waiting`, and `waiting` counts only pending cards (panel.tsx:65). The card list outside the dock appears only in setup and failed states (panel.tsx:118-141).
- Failing scenario: the member approves a card from another device and the call runs on the server for a while. In the ready or running state, the "Approved, running..." card and the `cardsHeading` "N running" text never show. That contradicts "The panel lists open cards with their state after a reload".
- Severity: minor. Confidence: high.

### 6. Origin is the member's last prompt, not the turn's
- Where: internal/assistant/assistant.go:877-889 (`send` sets `m.origin` before `PromptIdle`) and assistant.go `turnOrigin`, used by confirm.go:68.
- Failing scenarios:
  - A prompt refused because a turn is running (`PromptIdle` returns false) still sets `m.origin` for that window. A card the running turn raises in that window takes the refused prompt's origin and so its park/wait behaviour and TTL.
  - Autonomous turns inherit whatever origin the last prompt had.
  - With connectors wired in (phase 3), a dashboard turn's card could park for 24h, or a connector turn's call could block for 10 minutes.
- At this commit only `Door(dashboard)` reaches `Prompt` (internal/sshd/assistant.go:126), so nothing goes wrong today.
- Severity: minor. Confidence: low (latent).

### 7. One undecodable card row breaks the whole status call
- Where: internal/assistant/card.go:287-290. `openCards` returns an error and `Status` fails (assistant.go:307-310).
- Failing scenario: a card row whose `card` or `origin` JSON does not decode, for example a future field change or a hand-edited database, makes `assistant.status` fail. The panel cannot load for that member until the row leaves pending, approved or running, which can take up to 24h for a parked card. The row is only skipped in `raise`.
- Severity: minor. Confidence: low.

## Checked and found sound
- Claim atomicity, approved to running via `MoveAssistantCard`: two `runParked`, or the live path plus `runParked`, cannot both run a card.
- The `confirmations` channel protocol: answer, release and supersede each send under the lock before they delete, so `leave`'s blocking receive cannot deadlock.
- The pending re-check in `raise` after registering a wait.
- Resume runs before SSH serves, so its "running" means cut off by the restart.
- `Confirm` and `Card` are scoped by member.
- `RecoveryReady` is nil-safe.
