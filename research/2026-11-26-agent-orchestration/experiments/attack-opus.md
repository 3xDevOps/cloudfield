# Adversarial review of research/2026-11-26-agent-orchestration (PR #1130)

Reviewer: Opus, fresh context. Read: REPORT.md, PROPOSALS.md, all of evidence/ and experiments/, a sample of the cited code, and `gh pr view` for the omp-reviewed PRs. Nothing changed except this file.

Severity count: 3 blocker, 9 should-fix, 3 minor.

## Findings (most severe first)

### 1. BLOCKER: "Skip the second model for documentation-only changes" goes against the report's own record

Attacked: REPORT rec 3, "**Skip the second model** for documentation-only and mechanical changes: both reviewers were clean on #1026 and #1127 (3a 2), and published real-PR review precision is low (1c D), so a second reviewer on a trivial change mostly adds noise." The same advice appears in the PROPOSALS review note ("Docs-only and mechanical PRs need only (1)") and in its change table.

What is wrong:
- #1026 is a generated module-path refactor and #1127 is a CI change. Neither is documentation-only.
- The documentation-only PRs in 3a's table are where omp did best. `gh pr view` file lists:
  - #1122 is `docs(plans)` (one file, docs/plans/...). omp raised 5 blockers after 3 Opus passes.
  - #1123 is text in the connector listing JSON. omp's one blocker was a factual error that Opus missed ("no tool merges PRs", but `aether_pr_merge` exists).
  - #1121 is `docs(legal)`. omp found a false claim, rated "partly".
  - So 2 of the 3 PRs where "omp reported a blocker or factual error the Opus reviews had not" (3a 2) were documentation-only, and a third docs-only PR was a partial catch.
- 1c D does not support "mostly adds noise":
  - That study measured F1, not precision.
  - Its models were Haiku 4.5 and Sonnet 4.6, not omp/gpt-6.2-astra.
  - It found F1 *higher* on small diffs (0.657 for <10 lines against 0.043 for >150 lines).
  - Its "union of two models adds false positives" claim was "NOT confirmed on the fetched page" (1c D, row 4).

Correction: skip the second model only for mechanical or generated changes (#1026, #1127). Keep it for docs, plans and listings that make factual claims about the product, which is where the record shows omp catching errors.

### 2. BLOCKER: Private data in a public repository

`gh repo view` reports the repository as PUBLIC, and the pull request branch is on a public ref.

(Detail shortened by the lead so this file does not repeat it.) evidence/3c named a teammate together with a sign-in problem, linked a first name to a GitHub login, named a host and described an incident on it, and quoted a remark about the owner's budget. PROPOSALS named the account used for the omp reviewer while saying that account was left out on purpose. The file quotes team-memory notes, which CLAUDE.md says the repository does not hold.

Correction: strip names, logins tied to accounts, host names and incident detail; move the account line to team memory. Because the branch was already public, deleting it may not be enough.

### 3. BLOCKER: "The same Opus review took 8.4-9.2 min in-session against 2.4-3.4 min in its own VM" compares different work

Attacked: decision-rule row "Subagents" ("...CPU pressure read 96-99% and the same Opus review took..."), "The difference that matters is the machine", the staffing note's "parallel builds or tests crawl", and PROPOSALS product change #1 (ranked first).

What is wrong:
- **Effort differed.** The subagents ran at "effort high" (README line 18). A sub-session cannot be given effort: `ChildStartParams` has no effort field (internal/protocol/children.go:96-104). The launch passes only Model, Permission and extras (internal/children/dispatch.go:137-139). REPORT line 100 itself says a child "inherits neither the parent's effort setting". 1b 4 gives Opus 5.5's default as `medium`.
- **Work differed.** Both subagents compiled and ran Go tests on the loaded single vCPU:
  - #547: package tests plus a scratch test.
  - #716: `go test -p 2 ./internal/assistant/ ./internal/store/ ...` (header of opus-subagent-716.md).
  - README line 38 leaves out the #716 subagent's test run.
  - The #716 sub-session ran nothing (3.4 min).
  - Compiling Go on one CPU at 99% pressure is CPU-bound work. It is not the "model-bound" kind the row recommends subagents for.
- **The report contradicts itself.** It recommends subagents for "work that waits on the model, not the CPU. Up to about 10", then uses a 3x slowdown of review subagents as proof of CPU starvation. Either CPU contention slows model-bound subagents, and "about 10" is wrong, or the slowdown has another cause (effort, builds).

Correction: report the timings as confounded by effort and by in-session builds. Say the CPU share is not isolated. Rerun with equal effort and no builds before ranking the vCPU change first.

### 4. SHOULD-FIX: "The second model family added real coverage" attributes the gain to family when the design cannot separate it

Attacked: REPORT Review bullet 1, and PROPOSALS "Why: in a blind test omp raised 8 and 6 issues no Opus run did".

The omp arm differs from the Opus arms in more than model family:
- Harness: omp against Claude Code.
- Work time: about 9.1-9.5 min against 2.4-3.4 min for the Opus sub-session.
- Effort: the Opus sub-session's effort was unset (finding 3).
- Count: omp hit the brief's cap of 10 both times. The Opus arms stopped at 5-7, which fits the brief's "fewer strong ones beat many weak ones".

Only 3 of omp's 8 #716 uniques were checked (judge-716 §4). REPORT mentions the time confound only under "Not established". The Limits bullet (line 47) also leaves out the README's "Blinding rests on each reviewer's word". The checkouts contained the later fixes: a worktree branches from the default branch (1b 3), and the brief names the PR number.

Correction: "the omp arm raised more distinct, judged-real issues. Model family, harness, time spent and how each arm read the count instruction are not separable at n=2."

### 5. SHOULD-FIX: "Review did not find the hard race" leaves out that blind review found the other race

Attacked:
- REPORT bullet "Review did not find the hard race...".
- The failure-mode guard "Review and reproduction are separate steps".
- The CLAUDE.md draft line "A fix for a race or a retry path needs a reproduction... Review does not replace it."

What is left out: on #547, all three blind arms found the #1028 race (judge-547 §1, A/B/C all HIT), and one reproduced it. That race had escaped for about three weeks. So blind review found 1 of the 2 known races, in 3 of 3 arms.

The 3a 3 record (#1028, #1032, #1038) is about reviewers of the *fix* PRs. The author had already found the bug, so those reviewers could not have "found" it, and they say nothing about whether review finds races.

Correction: "Blind review found the #547 race in all three arms and missed the #716 race in all three. Reproduction caught both."

### 6. SHOULD-FIX: "73 of 73 origin queue failures in queue-only jobs had a green pull request run" proves nothing about string changes

Attacked: REPORT rec 1 ("...and 73 of 73... (3b 3; partly by construction...)"), and PROPOSALS product change #5.

The queue accepts only PRs whose CI gate is green, so 73/73 holds by construction, not "partly". 3b 3 also says "Against the claim: not every queue-only failure is text-related; most e2e/kvm/windows ones are timing or platform failures". 3c h: "A text-changing PR breaking only in the queue: not found". The support for the string-change advice is one case, #1025.

Correction: cite #1025 alone (n=1). Drop the 73/73 from this argument, or state it as "15.7% of non-cancelled queue runs fail in suites PR CI skips".

### 7. SHOULD-FIX: The merged-wait guard "none needed beyond that" leaves out a gap the evidence documents

Attacked: failure-mode row "A lead sleeps on a `merged` wait... | Use `--until pr:...:merged`; none needed beyond that".

2b 3 gap 2: "A removal before the wait was registered is ignored" (internal/watch/wait_pr.go:95-96, checked). A lead that runs `gh pr merge` and reports waiting only after the queue has already dropped the PR sleeps until merge, close or the 7-day expiry. That is the liaison's scenario. Gap 1 (a failing queue read sleeps up to 7 days) is in the same row, yet PROPOSALS #7 asks for a product fix for it.

Correction:
- Guard: report the wait immediately after queueing.
- On wake or after one queue cycle (about 25 min), check `gh pr view --json mergeStateStatus` or the queue.
- Note both gaps.

### 8. SHOULD-FIX: Stacking delays are overstated, and "one queued PR per lead" has an unstated cost

Attacked: REPORT failure-mode row "Delays 1.4 to 5.2 h (3b 4)", rec 6 "Queue one pull request at a time per lead", "A pull request with one failed queue run took a median 1.40 h", and PROPOSALS "One queued pull request per lead | ... delays of 1.4 to 5.2 h".

What is wrong:
- The 1.4-5.2 h is first enqueue to merge, and it includes the PRs' own failures. Per 3b 4, #1029 then failed on its own flake (`TestHarnessUpdateTimeoutStillLaunches`). #1031 then failed on its own integration test and an e2e test. The share caused by stacking is smaller.
- The 1.40 h median is for PRs with "at least one" failed run (3b 2), including PRs that had to fix their own defects.
- The rule rests on one incident (11-17). There the failing entry (#1025) belonged to a different lead than #1028/#1029/#1031 (3c 3: the flaky-test lead's 11 PRs). Per-lead serialization would not have kept #1028 off #1025.
- The cost is unstated. At a median 23.5 min per run, a lead with 11 PRs needs at least 4.3 h of strictly serial queue time.

Correction: present the stacking delay as an upper bound, n=1. State the throughput cost. Keep "do not queue behind a red entry".

### 9. SHOULD-FIX: "The merge queue is the narrowest point" is not supported

Attacked: REPORT line 83.

The evidence shows the opposite on average:
- A PR's median life from open to merge is 2.66 h (3a 1). Without a failure, first queue run to merge is a median 0.41 h (3b 2).
- 2 builds at once at about 23.5 min each is roughly 5 merges per hour, or about 120 a day. The peak was 48 merges in a day (3a 1).
- Review and session durations are "not established" (3c 5), so no comparison was made.

Correction: "A queue failure is the costliest single event: 1.40 h against 0.41 h median". Drop "narrowest".

### 10. SHOULD-FIX: The public CLAUDE.md draft turns one session's CPU count into a rule

Attacked: PROPOSALS §2 draft ("A session has one CPU... a workflow runs two agents at a time on one CPU"), and the staffing note "a session has 1 vCPU".

The code sets CPUs to `min(8, NumCPU)` unless an admin or the run sets a limit (internal/scheduler/capacity.go:70-73, checked). REPORT itself lists "Why sessions have one vCPU; the host's size" as not established. For a self-hosted public product, the sentence is false on any host with more than one CPU and no low limit.

Correction: "Check `nproc`. On a one-CPU session, run one build or test at a time and start subagents directly; a workflow caps its concurrency by CPU count."

### 11. SHOULD-FIX: "Reports are cut at about 300 characters: Wrong" leaves out a recorded truncation

Attacked: the failure-mode row verdict, and "the task's premise that reports are cut at 300 characters was wrong" (This run).

The code reading for reports is sound (2b 2). But 3c a records "Long aether-internal send bodies (~2-4 KB) to Enhanced (acp) workers arrived cut off after ~500-650 chars, and the inbox item was acked anyway" (2026-10-27). Several notes show leads asking reviewers to resend parts. The report drops all of it.

Correction: "Wrong for reports in current code. A recorded case of sends cut at ~500-650 chars exists (2026-10-27), cause not traced."

### 12. SHOULD-FIX: "Dynamic workflows, heavy is slower than plain subagents here" was not measured

Attacked: REPORT "Two things..." item 2, and PROPOSALS product change #1 (raise `CLAUDE_CODE_WORKFLOW_MAX_CONCURRENT_AGENTS`).

The workflow was stopped after about 2 min (README "about 06:50 to 06:52"). No completion time was taken. With CPU pressure at 96-99% for 10 concurrent agents, which the report blames for a 3x slowdown, what the 2-agent cap costs in wall-clock time is not established. Raising workflow concurrency on one vCPU would add the contention the report warns about.

Correction: label it as an inference from the cap, and say that raising the cap on 1 vCPU trades against CPU contention.

### 13. MINOR: Published numbers moved to a different setting

- REPORT line 22: "Anthropic's 90.2% gain was on research queries... and 'token usage by itself explains 80% of the variance'". Per 1a 1, the 80% was measured on BrowseComp, not on the internal research eval behind the 90.2%.
- Decision row "Lead alone" tags Anthropic S4 as "published, on benchmarks". S4 is advice (1a 2).
- The Google SWE-bench result used GPT-5, Gemini 2.x and Sonnet 3.7-4.5 harnesses, each solving one task (1c A). It was not a lead plus subagents doing reading or review.
- "Evaluators 'skew positive'" (1a 8) is about agents grading *their own* work.

Correction: name each setting inline.

### 14. MINOR: PROPOSALS §4 says "The three rated blocker, all in judge-716.md"

judge-547 cluster 8 (B#1) was also rated blocker by its reviewer and judged REAL in mechanism and NOT-FIXED. judge-716 rows 6, 8, 11, 12 and 17 are REAL and NOT-FIXED, yet §4 lists none of them. The count of "eleven more" for #547 is right (clusters 2-4, 6-12, 14).

Correction: four rated blocker (three in #716, one in #547), plus five more real, unfixed #716 rows.

### 15. MINOR: "omp reported a blocker... in 3 of 7 pull requests" ignores a conflict between collectors

3a counts #1125 as "no omp review stated". 3c 6 cites `fzaewasm0g`: omp found "five owner-checklist blockers in #1125". If 3c is right, the count is 4 of 8, or 3 of 8 with one disputed. 3a also says the findings are author-written and unverified. All of these PRs were unmerged, with no ground truth (3c: "No note scores either reviewer against later truth").

Correction: reconcile #1125 and state that the record is unscored.

## Claims checked and found sound

1. **Overlap counts.** Recomputed from the judge cluster tables and label keys:
   - #716: Opus union {6,8,11-18} = 10, omp {1-10} = 10, common {6,8} = 2. The Opus arms share {6,14,16} = 3.
   - #547: Opus union = 8, omp = 10, common {1,3,4,5} = 4.
   - Every "Issues no other arm raised (real/checked)" cell matches.
   - Per arm, adding omp to one Opus arm adds 6-9 issues; adding the other Opus arm adds 1-4. This supports "fewer" for a second Opus run.
2. **32 findings checked with none rejected** (21 in #547 plus 11 in #716).
3. **Timings and cost from README.**
   - Work 8.4, 9.2, 2.4, 3.4, 9.1 and 9.5 min.
   - Launch to first action 37, 25, 34 and 34 s.
   - omp $11.75 (the report's $11.76 is a rounding of rounded values) against $2.48, or 4.7x.
   - Launch to report 11.5-12.0 min against 3.3-4.6 min.
   - The run took 25 min (06:48:49 to 07:14).
   - Caveat: the subagent "10-23 s" figures are upper bounds.
4. **Code citations sampled, all correct.**
   - `vm runtime: %s VM %s: it is not running` at internal/runtime/vm/lifecycle.go:59.
   - `requestBurst = 30`, `requestRefill = time.Second`.
   - `min(8, NumCPU)`.
   - One-level depth in policy.go.
   - The `!entry.waits() || entry.answered()` gate.
   - `idleNoticeAfter = time.Hour`, `maxUntrusted = 300`, `maxNoticeSummary = 3000`.
   - The docs/harnesses.md:1000-1002 "refuses them Standard" against `WakeRefusal` on the parent only.
   - The ci.yml PR job comment.
   - Only drift: 2a cites dispatch.go:131-134 for LaunchWithOptions, which is now at 137-139.
5. **Merge-queue arithmetic and collector length.**
   - 149/465 = 32.0% failed.
   - Medians: 1404 s for failed runs, 1421 s for green runs.
   - 1.40 h against 0.41 h.
   - Collector files run 1,866 to 3,096 words (`wc -w`).

## What the lead changed in response

| Finding | Action |
|---|---|
| 1 | Accepted. The report now keeps the second reviewer for documents and narrows only for generated or mechanical changes, as the owner's call |
| 2 | Accepted. Evidence file 3c scrubbed, the account name removed from PROPOSALS, the branch history rewritten. The earlier commits may stay reachable by hash on GitHub until it collects them |
| 3 | Accepted. The timing gap is reported as unexplained; CPU share, effort and work done are not separated |
| 4 | Accepted. The gain is attributed to the omp arm, not to the model family |
| 5 | Accepted. "Blind review found one of the two known races" |
| 6 | Accepted. The 73 of 73 figure is dropped; #1025 is cited as one case |
| 7 | Accepted. Both wait gaps and a guard are in the failure-mode table |
| 8 | Accepted. Delays are stated as including the pull requests' own failures; "one queued pull request per lead" is replaced by "do not queue behind a red entry" |
| 9 | Accepted. "Narrowest point" removed |
| 10 | Accepted. The CLAUDE.md draft says to check `nproc` |
| 11 | Accepted. The 2026-10-27 truncated-send note is in the table |
| 12 | Accepted. The workflow's cost in time is labelled as inferred |
| 13 | Accepted. Settings named inline |
| 14 | Accepted. PROPOSALS section 4 lists four blockers and the other real, unfixed rows |
| 15 | Accepted. #1125 is noted and the record is called unscored |
