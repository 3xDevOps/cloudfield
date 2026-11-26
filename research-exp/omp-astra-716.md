- Arm: omp gpt-6.2-astra sub-session
- Model: `openai-codex/gpt-6.2-astra`
- First action (UTC): `2026-11-26T06:51:51Z`
- Review finished (UTC): `2026-11-26T07:01:20Z`
- Blinding: kept every rule. No later commits, GitHub, team-memory searches or web access.
- What I ran: detached checkout and parent-to-commit diff of `a3171a811e29f26ff1639423f618f94ea8519301`; static source review. No builds or tests. The scenarios below are derived from the code, not observed executions.

### 1. A parked Stop can destroy a finished session’s retained container

**Blocker · high confidence** — `internal/assistant/card.go:541-545`. Raise Stop for a running session, end the assistant turn, let the target finish with its container retained, then approve. The rebuilt `run.kill` summary names the ID, label and owner, not the run state (`internal/assistant/tools/tools.go:409-414`), so it matches. Production `kill` on a terminal, retained entry calls `expireRetainedLocked`, which destroys the container, then dismisses the finish (`internal/scheduler/steer.go:54-63`; `internal/scheduler/supervise.go:507`). The member approved stopping live work, not disposing of a finished environment. The error returned by the fake kill in the restart test does not protect this path.

### 2. Forgetting a preference leaves its plaintext in the live database

**Blocker · high confidence** — `internal/assistant/card.go:243-253`. Request `assistant.forget`, approve, then start a new conversation. The forget summary contains the entire preference (`internal/assistant/tools/act.go:318`); the card insert saves that summary and `finishCard` saves it again in the result. Neither secure deletion of the preference nor conversation reset deletes these copies. The 30-day prune only runs when the *same member adds another card* (`internal/store/assistant_cards.go:86-88`). If they never do, the text remains indefinitely in `aether.db`, not just in a backup or one of the transcript copies listed in `docs/privacy.md:506-528`.

### 3. Asking to save the environment makes all approval cards unreadable

**Blocker · high confidence** — `internal/assistant/card.go:264`. The normal `aether_save_environment` tool sends nil params (`internal/ops/assistant.go:107-110`); the socket client omits the field. `cardRow` stores `string(nil)` as `""`. `cardOf` replaces the correctly decoded JSON `null` with `json.RawMessage("")`: a non-nil, zero-byte slice, not a JSON value. With manual approval enabled, marshaling `assistant.status` now fails for the entire member and the stream exits at `internal/sshd/assistant.go:453-461`. The member cannot see the save card or any unrelated card in that status until the invalid card leaves the open set.

### 4. A parked launch can charge a different teammate than the card names

**Should-fix · high confidence** — `internal/assistant/card.go:541-545`. Raise an automatic-fallback launch with Alice as the only shared account, then replace Alice’s share with Bob’s before approving. Both summaries say “may fall back to accounts shared with you”; the account names are in `OtherOwner`, *outside* the summary (`internal/assistant/tools/tools.go:609-617`). The dashboard explicitly shows Alice as the payer (`web/src/components/assistant/cards.tsx:142-146`). The fresh card would show Bob, but the comparison passes and the launch can use Bob’s quota. A generic fallback clause is not equality of the card the member saw.

### 5. A tool call reported as not approved can execute on the next restart

**Should-fix · high confidence** — `internal/assistant/confirm.go:110-124`. The decision commits `approved` and hands it to a live waiter. If the subsequent `approved → running` write fails (for example, a competing SQLite writer exceeds the busy timeout), the waiter returns the claim error. The socket tells the model the method “was not approved”; nothing ran. Unlike `runParked`’s claim-error path, this path neither fails the row nor schedules another worker. It stays approved and cannot be declined. On restart `Resume` executes it (`internal/assistant/card.go:576-577`), potentially long after the member dealt with the reported failure another way.

### 6. Reset can separate a parked mutation from its outcome record

**Should-fix · high confidence** — `internal/assistant/card.go:559-560`. A parked `run.relaunch` claims the row and records approval. Before `srv.Call` takes the held action, New conversation cancels the old conversation. `heldActions.take` drops that action; `actionCaller.Call` forwards a non-reversible method with no held action straight to the underlying caller, bypassing `record` (`internal/assistant/confirm.go:403-430`). The parked call uses the service context, so the relaunch can succeed. No one finishes the card: it stays `running`, and restart records a failure even though the new run was created.

### 7. An old card’s private contents can reappear after New conversation

**Should-fix · high confidence** — `internal/assistant/card.go:382-391`. Decline a card. The handler moves it out of pending and reads that its generation is current. Now let another device finish a reset before `m.conversation()` is evaluated on line 391. The reset doesn’t withdraw the already-declined row; `noteOutcome` is passed the **new** conversation context, so its guard passes and it appends the discarded card’s summary to the new transcript. A declined forget card, for example, puts the old preference text back in view. Checking the generation and selecting the destination must refer to the same conversation.

### 8. Approval immediately after restart races session recovery

**Should-fix · high confidence** — `internal/assistant/card.go:375-378`. Restart with a *pending* mode-switch card for a live session. SSH accepts requests concurrently with scheduler recovery (`internal/server/server.go:734-746`). If the member approves before that session is reattached, `decide` starts `runParked` with a nil readiness channel. `SwitchMode` sees no entry in `s.runs` and returns “the session has no live container” (`internal/scheduler/mode_switch.go:93-98`); the card is terminally failed. The same card already *approved* at startup waits for `RecoveryReady()` at line 577. The validity of the action should not depend on which side of startup the click lands.

### 9. Late success can be attributed to a different, still-pending tool call

**Should-fix · high confidence** — `web/src/lib/assistant-actions.ts:377-390`. In two successive turns, ask to add two different memory notes to the same workspace. End each turn with its card still open. Now approve only the **first** card. Its approval record arrives after both calls; neither has a saved action ID yet. `ownerOf` matches method and workspace, not note text or original tool-call ID, and chooses the newest matching block. The success then follows that same draft by action ID. With the turn-ended result, lines 272-275 show the **second** note as done, even though it is still awaiting approval. Server execution is correct; the transcript tells the member the wrong note was added.

### 10. An uncertain PR creation becomes a definite failure with a Retry button

**Should-fix · high confidence** — `internal/assistant/confirm.go:460-461,487-493`. If the creation response is lost and reconciliation cannot find the PR, the server returns `creation_uncertain: true` with an error. The tool deliberately returns `write: "pending"` and tells the caller to check PR status first (`internal/ops/pr.go:77-83`). The new record instead says `failed`. The dashboard honors that record before its `done → unconfirmed` check (`web/src/lib/assistant-actions.ts:270-275,532-534`), so the member gets **Retry** instead of “Not confirmed: check status before retrying.” The UI test for this case omits the server record and misses the regression.
