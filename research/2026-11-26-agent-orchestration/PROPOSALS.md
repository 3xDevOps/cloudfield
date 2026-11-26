# Proposed changes (drafts; nothing here is applied)

Evidence references point into `REPORT.md`, `evidence/` and `experiments/`.

## 1. Pinned team-memory notes

Team-memory note bodies are capped at 1,024 bytes (2b 5). Both drafts fit. `<owner>` and `<omp account>` stand for the names the current notes already carry; they are left out of this public file.

### Staffing (would replace note-sjzbg5fh9q)

```
Staffing, <owner>'s rule, revised 2026-11-26 after research/2026-11-26-agent-orchestration. A Fable 5.2 lead plans, splits, verifies, integrates. Small or urgent fix, or tightly coupled code: the lead does it, no fan-out. Reading, search, collection, review: subagents in the lead's session (opus for judgement and reviews, sonnet for collection, effort high), started as direct Agent calls. Check nproc: on 1 vCPU a workflow runs 2 agents at a time, and only one build or test job should run in the session. A sub-session (first action in about 30 s) when the work builds or tests beside other work, needs another agent or account, needs its own branch or PR, or must outlive the turn. Briefs stand alone, in a file; results come back as pushed files; check a claim before repeating it; reviewers never see the author's reasoning. Push every commit. Never queue a PR behind a red merge-queue entry. On <owner>'s own account.
```

### Review (would replace note-zsvnh0gxyq)

```
Adversarial review, <owner>'s rule, revised 2026-11-26. Before review the author runs the checks for the change: force the interleaving for race or retry fixes; run the integration tests that read command names or usage text when those change (PR CI skips them). Then two fresh reviewers in parallel, same head, whole diff, same brief (find defects, skip style, file:line and a failing scenario each): (1) Claude Opus, subagent or sub-session; (2) omp sub-session: agent omp, account <omp account>, Enhanced, --model openai-codex/gpt-6.2-astra, then a message that is only /fast on. Fix what has a traced scenario, answer the rest in the PR. Re-review behavior-changing fixes with one fresh Opus. Name reviewers and models in the PR. Why: in a blind test on 2 PRs the omp run raised 8 and 6 issues no Opus run did, at about 5x the cost, cause not isolated; review found 1 of 2 known races. Gotchas: bare astra is refused; fast mode is unconfirmed.
```

Where these differ from today's notes, and why:

| Change | Evidence |
|---|---|
| Drop "workflows, heavy" as the default; say direct subagents and check `nproc` | A workflow started 2 of 8 agents on this 1-vCPU session (measured). Its cost in time is inferred, not measured |
| Replace "start in seconds and add no load" | Subagent at most 10-23 s, sub-session 25-37 s to first action (n=4, quiet server). Subagents share the lead's CPU |
| Put back "small or urgent: no fan-out" | It was in the superseded note and dropped (3c i) |
| Add author-side reproduction and the local integration run | Blind review found one of two known races; #1025 broke only in the queue (experiments, 3b 3) |
| Reviewers in parallel, same head, whole diff, same brief | The #1120 comparison was confounded by different scopes (3c 6) |
| "Never queue behind a red entry" | 2026-11-17 stacking (3b 4), one incident |
| Not changed: every pull request gets both reviewers | Two of the three pull requests where omp caught what Opus missed were documents (3a 2). The only cases where both were clean are a generated rename and a CI change (#1026, #1127); exempting those is the owner's call |

## 2. Replacement for "Working in parallel as a Cloudfield agent" (CLAUDE.md and AGENTS.md)

The evidence supports additions, not a rewrite. Draft of the full section:

```markdown
## Working in parallel as a Cloudfield agent

Many sessions share the same CI runners and one merge queue.

Splitting work:

- A small or urgent fix, or tightly coupled code, stays with one agent.
- Use subagents in your own session for reading, searching and review.
  They share your session's CPUs: check `nproc`. On one CPU, run one
  build or test job at a time and start subagents directly; a workflow
  limits how many agents run at once by CPU count.
- Start a sub-session for work that builds or tests beside yours, needs
  another agent or account, needs its own branch or pull request, or must
  outlive your turn. Have it commit and push its result; you fetch only
  committed work.
- Push every commit. A sandbox that is lost before the server captures
  it loses what was not published.

Pull requests:

- Keep a pull request as a draft while you iterate. Pull request CI runs
  only `changes`, `audit`, `lint`, `build-and-test`, `store-tests` and
  `dashboard`; the merge queue runs the full suite.
- Push milestones freely. Do not wait on CI or rerun it between pushes.
- Run the checks for what you changed locally first; see
  [docs/testing.md](docs/testing.md). A change to command names or usage
  text needs the integration tests that read that output before it
  queues (`make test-integration` with `INTEGRATION_PKGS` and
  `INTEGRATION_RUN`): pull request CI does not run them.
- A fix for a race or a retry path needs a reproduction that forces the
  interleaving, before and after. Review does not replace it.
- Mark the pull request ready (`gh pr ready <n>`) only when it is done;
  then the merge gate applies.
- The merge gate, with no review bot to wait for:
  - An adversarial review by an Opus reviewer with a fresh context that
    did not write the change (a subagent is enough; give it the pull
    request, not the author's reasoning). Tell it to find defects and
    skip style, and to give a file, a line and a failing scenario for
    each finding. Fix each real finding and answer the rest with a
    reason in the pull request description.
  - When those fixes change behavior, another such reviewer checks them;
    repeat until no finding blocks. A rebase or a wording-only fix needs
    no new review.
  - `CI gate` green on the current head.

  Then `gh pr merge <n> --squash` puts it in the merge queue, which runs
  the full suite once per batch before `main` moves. Never pass `--admin`.
- Do not queue behind an entry that has failed: entries build on the one
  ahead, so its failure becomes yours. Report your wait right after
  queueing.
- A UI change also gets a design review loop: (unchanged)
```

The second-model reviewer is left out of this public file on purpose: it depends on a teammate's account, which belongs in team memory.

## 3. Product changes to Cloudfield

Not ranked. Each is a list item, not a design.

| # | Change | Evidence |
|---|---|---|
| 1 | Wake the parent when a sub-session reports `waiting`, or tell it within minutes | Nothing reaches the parent until the 1-hour quiet notice (2b 2); a lead recorded "sub-session waiting reports not waking the parent" (3c 4) |
| 2 | Keep newlines in report summaries, and state the real limits in `aether-internal skill` and `report --help`: 4 KiB whole, 256 characters on the status line, 512 in the dashboard activity line | Summaries are flattened to one line (2b 2); the brief for this run said reports are cut at about 300 characters |
| 3 | Show a session's CPU count where the agent sees it, and decide whether one vCPU is intended | `nproc` = 1 here while the code's default ceiling is `min(8, NumCPU)` (2a 1); a workflow started 2 of 8 agents. How much time this costs was not measured |
| 4 | Add `--effort` to `subsession start`, or let a child inherit the parent's effort | No effort field exists (2a 1); it is one of the confounds in the experiment |
| 5 | Show which model and speed tier a sub-session is running, and make "ready" in `subsession options` mean the default model works | `/fast on` has no confirmation and no code in this repo (2a 3); omp's default model was refused while options said ready (3c 4) |
| 6 | Make the merge queue stop a run at its first failed job | Failed runs take a median 23.4 min, the same as green ones (3b 2); #1075, titled as doing this, was closed unmerged (3a 1) |
| 7 | Quarantine or fix the repeat flakes | `TestAPromptDropsAWakeNotYetWritten` failed 6 queue runs on 6 pull requests; 36% of origin failures passed on retry with no new commit (3b 6) |
| 8 | Offer a way to run the integration suite on a pull request that changes command usage text | #1025 (3b 3), one case; 15.7% of non-cancelled queue runs failed only in suites pull request CI skips |
| 9 | Close the two `merged` wait gaps: a removal before the wait was registered, and a merge-queue read that keeps failing | 2b 3; the second sleeps to the 7-day expiry with no broken-wait alarm |
| 10 | For a queued sub-session refused for host memory or pressure only, offer a bounded retry and show the child as waiting; report every other refusal at once | A refused queued child is final (2a 4); the same path carries permanent refusals such as budget and authorization, and blind retries add load to a pressed host |
| 11 | Give a rate-limit refusal its own error code, and consider a separate budget for read-only calls such as `memory search` | Rate limit and idempotency conflict share -32003; subagents share the session's 30-call budget and two hit it here (2a 4) |
| 12 | Report the cost of in-session subagents the way a sub-session's `cost_usd` is reported | Sub-sessions showed $0.86 to $7.66; subagents showed tokens only (experiments) |
| 13 | Fix two documentation mismatches | `docs/harnesses.md` says a sub-session refuses Standard for Copilot, Gemini and Cursor; the code checks only the parent (2a Surprises). `docs/coordination.md` omits that an `answered` child report is forwarded to the parent (2b 2) |

## 4. By-product for triage

The blind reviews of #716 and #547 produced findings that a judge traced as real and found not fixed on `main`. The lead did not verify them.

Rated blocker by the reviewer:

1. A parked Stop card approved after its session finished destroys the retained container (`experiments/judge-716.md` row 1).
2. Forgetting a preference leaves its text in `assistant_cards` (judge-716 row 2).
3. An `env.save` card with no parameters makes `assistant.status` fail to marshal (judge-716 row 3).
4. A queue overflow during the cost service's start scan replays the event log from zero and counts spend twice (`experiments/judge-547.md` cluster 8; the judge traced the mechanism, not the trigger, and notes the hazard predates #547).

Also judged real and not fixed: judge-716 rows 6, 8, 11, 12 and 17, and judge-547 clusters 2-4, 6, 7, 9-12 and 14.
