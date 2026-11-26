# How to staff agent work on Cloudfield: lead, subagents, workflows, sub-sessions, second-model reviewers

2026-11-26. Lead: Claude Fable 5.2 (`claude-fable-5-2`), session run-805s2y01bd. Research only; no product code changed.

Evidence is in `evidence/` (eight collector files) and `experiments/` (six blind reviews, two judge verdicts, the briefs). Each claim names its file. Strength tags: **measured here** (a command or experiment in this run), **record** (GitHub or team memory, written by the sessions themselves and not scored), **published** (someone else's measurement, on the task named), **design** (read from this repo's code or docs), **advice** (opinion).

## The decision rule

| Use | When | Evidence | Strength |
|---|---|---|---|
| **Lead alone** | A small or urgent fix; tightly coupled code; anything sequential | Claude Code's guidance: a subagent given the same task "doesn't make the answer any better" and can be slower (1a S4). Google, on SWE-bench Verified with single-task harnesses: every multi-agent architecture averaged below a single agent, 0.444-0.511 against 0.522 (1c A); these are means over models, and the omp reviewer reports single configurations that went the other way. #1127: the lead wrote alone and used two reviewer sub-sessions (3c i) | advice; published, on a benchmark that is not a lead with subagents; one local case |
| **Subagents** (direct Agent calls) | Reading, searching, collecting, reviewing by reading. Ten ran at once here | All ten finished, in 2 to 15 min. They share the lead's sandbox, which has 1 vCPU here; CPU pressure read 96-99% over the minute when four were still running, two of them Go tests; it read about 50% earlier with all eight collectors running (2a 1) | measured here, one run |
| **Workflow** | When the script's fixed shape matters (find, then verify each finding) | On this 1-vCPU session a workflow started 2 of 8 agents and held the rest; Claude Code documents "fewer when Claude Code has fewer CPUs" (1b 2). I stopped it after two minutes and started the same 8 briefs as subagents, so its finish time was not measured | measured here: the cap. Its cost in time: inferred |
| **Sub-session** | It builds or runs tests beside other work; needs another agent or account; needs its own branch or pull request; must outlive the turn | First action 25-37 s after launch, four launched together on a quiet server (experiments). Own VM, own checkout, reports `cost_usd`. One level deep, 8 live per parent (2a 3) | measured here, n=4; design |
| **Another model** | As a second reviewer | Below | measured here, n=2 PRs; record, 7 PRs, unscored |

Where today's rule is off:

1. **"Subagents start in seconds" is true but is not the difference.** Subagents took their first action at most 10-23 s after launch, sub-sessions 25-37 s. What a sub-session buys is its own machine and checkout, not a faster start.
2. **"Dynamic workflows, heavy" is unproven here and capped.** Two agents at a time on one vCPU. Nothing I found measures a quality gain from workflows over subagents (1b "Could not establish"). Raising the cap on one vCPU would add CPU contention.
3. **The same Opus review took 8.4-9.2 min as a subagent and 2.4-3.4 min as a sub-session, but I cannot say why.** The subagents ran at effort high in a session at 96-99% CPU pressure and both ran Go tests; a sub-session takes no effort setting (2a 1) and one of the two ran nothing. CPU share, effort and work done are not separated.

Fan-out is not free where it was measured. Anthropic's 90.2% gain was on its internal research eval, at about 15x the tokens of chat; on BrowseComp, "token usage by itself explains 80% of the variance" (1a 1). Google measured +80.8% on a decomposable finance task and -70.0% on a sequential planning one (1c A). Anthropic and Cognition both advise keeping writes to coupled code in one agent (1a 2, 1c C). In the other direction, a manager with workers in isolated worktrees gained 14.7 and 25.6 points over a single agent on Commit0 and PaperBench (1c E, abstract only). None of these is a lead with subagents on repository tickets. The local record: one coordinator started 49 sub-sessions, 33 of them reviews, and the project changed its default (#941, 3a 5); test sweeps in sub-sessions drove host load to 308 and launches were refused (3c 4).

## Review

### What the experiment showed

Three reviewers got the same brief, blind, on two merged pull requests whose code held a race that a flaky test later exposed: #547 (merged 2026-10-25; race fixed by #1028 on 11-17) and #716 (merged 11-05; fixed by #1038 on 11-18). For #547 the unlocked read predates the change; #547 added the lock that failed to cover it. Both were picked because they escaped, and both are large (687 and 2,707 changed lines against a merged median of 153), so nothing here is an escape rate. Two blinded Opus judges then traced findings in the code.

| Arm | PR | Launch to first action | Work time | Cost | Findings | Known race | Issues no other arm raised (judged real / checked) |
|---|---|---|---|---|---|---|---|
| Opus subagent | #547 | at most 23 s | 8.4 min | 97,793 tokens | 5 | found, reproduced | 1 (1/1) |
| Opus sub-session | #547 | 37 s | 2.4 min | $0.86 | 7 | found | 2 (2/2) |
| omp gpt-6.2-astra | #547 | 34 s | 9.1 min | $4.10 | 10 | found | 6 (5/5) |
| Opus subagent | #716 | at most 10 s | 9.2 min | 145,625 tokens | 7 | missed | 4 (2/2) |
| Opus sub-session | #716 | 25 s | 3.4 min | $1.62 | 6 | missed (same code path, other failure) | 2 (1/1) |
| omp gpt-6.2-astra | #716 | 34 s | 9.5 min | $7.66 | 10 | missed | 8 (3/3) |

Costs are the `cost_usd` the sub-session list reports; subagents report tokens only.

- **The omp arm raised more distinct issues that the judges traced as real.** On #716 the two Opus arms together raised 10 issues and omp 10, with 2 in common. On #547 it was 8 and 10, with 4 in common. Adding omp to one Opus arm added 6-9 issues; adding the other Opus arm added 1-4.
- **I cannot attribute that to the model family.** The omp arm also differs in harness, in time spent (about three times the Opus sub-session's), in effort setting, and in how it read the brief's cap: it returned exactly 10 both times while the Opus arms stopped at 5-7. Only 8 of its 14 unique issues were checked, and "unique" depends on the judge's grouping: on #547 an Opus arm mentioned the mechanism of one issue credited to omp alone.
- **It cost about 4.7x an Opus sub-session** ($11.75 against $2.48 for the pair) and took about 12 min from launch to report against 3-5 min.
- **Blind review found one of the two known races.** All three arms found the #547 race, which had escaped for three weeks; none found the #716 race. Forcing the interleaving found both (#1028, #1038; 3a 3).
- Four findings were rated blocker by a reviewer, traced as real by a judge, and are not fixed on `main` (PROPOSALS section 4). I did not verify them.
- Limits: two pull requests, one run per arm; judges from the Opus family, who rejected none of the 32 findings they checked (21 of the 32 distinct issues; blockers and each arm's top three were chosen for checking, and "real" means the judge traced the mechanism, not that it was reproduced); blinding rests on each reviewer's word, and the later fixes were present in every checkout. Whether fast mode was on for omp is not established: `/fast on` was sent, nothing confirms it, and this repo has no such mechanism (2a 3).

The record points the same way and is unscored. On 2026-11-26, by the pull request descriptions, omp reported a blocker or factual error the Opus reviews had not in 3 of 7 pull requests (#1120, #1122, #1123) and agreed with Opus on 2 (3a 2); a team-memory note adds omp blockers on #1125 (3c 6). Two of those three were documentation: a plan and listing text. The #1120 case is confounded: Opus was asked to review one fix commit, omp the whole diff (3c 6). Outside evidence is one single-author preprint. Its first version reported coverage rising from 33.6% to 57.1% with a second, different-provider model (1c D). Its second version, which the omp reviewer found and I then read, recomputes that to "about 47% with one reviewer to about 72% with a second" and concludes that the data supports "adding a second independent reviewer, not a specific second provider" (zenodo.org/records/21328807).

### Recommended setup

1. **Author first.** Run the checks for what changed. For a race or retry path, force the interleaving and run under load. For a change to command names or usage text, run the integration tests that read that output (`make test-integration` takes `INTEGRATION_PKGS` and `INTEGRATION_RUN`): pull request CI skips them, and #1025 passed pull request CI and then failed an integration test on its usage line in the queue (3b 3). That is one case; 15.7% of non-cancelled queue runs failed only in suites pull request CI skips, most of them not about text.
2. **Two fresh reviewers in parallel, same head, whole diff, same brief**: Opus, and omp `openai-codex/gpt-6.2-astra`. Today's rule stands, including for documents that make claims about the product.
3. **The one narrowing with any support**: generated or mechanical changes, where both reviewers were clean (#1026, #1127). Two cases; the owner's call.
4. **Triage, do not obey.** Fix what comes with a traced failing scenario. "A reviewer that's asked to find problems will always find something" (1a 3, advice).
5. **Re-review behavior-changing fixes** with one fresh Opus reviewer. A fix for one review finding caused the next blocker on #1120 (3a 3).

## Briefs

What worked in this run, and matches Anthropic's list of objective, output format, tools and boundaries (1a 3, advice):

| Worker | Brief must contain |
|---|---|
| Subagent, collector | Standalone goal; the one file it may write; what it must not touch; a source and a tag per claim; "not established" instead of a guess; a short final reply. Put the brief in a file and pass the path |
| Reviewer | The diff, not the author's reasoning; "find defects, skip style"; file:line and a failing scenario per finding; a time box. A cap on findings is read differently by different models |
| Sub-session | The above, plus: commit the result file, because the parent fetches only committed work (2b 4), and push it if anything else must read it; a report under 250 characters, because the status line keeps 256 (2b 2); repository-relative paths; the effort wanted, in words |
| omp reviewer | `--model openai-codex/gpt-6.2-astra` on the start command; without a model the first #1026 run used a provider that refused (3a 2) |

All eight collectors overshot the 1,500-word limit (1,866 to 3,096 words), and web quotes came through a summarizing fetch, so they are near-verbatim. One collector put a teammate's name and a host name in its file despite the brief; the Opus attack reviewer caught it after the push.

## How many at once

| Resource | Limit | Source |
|---|---|---|
| Lead's sandbox | 1 vCPU and 8 GiB here; the code's default is up to 8 CPUs, so check `nproc`. Ten model-bound subagents ran; run one build or test job at a time on one vCPU | measured here; 2a 1 |
| Coordination calls per session | 30, then 1 per second, shared by all subagents. Two collectors hit it | 2a 4, 3c |
| Sub-sessions per parent | 8 live, more queue; launches of one member run one at a time | 2a 1, 2a 3 |
| Host | A launch is refused at 10% memory pressure; a refused queued sub-session is not retried | 2a 4 |
| Merge queue | 2 entries build at once; median run 23.5 min; failed runs take as long as green ones; 32% of finished runs failed; 36% of origin failures passed on retry with no new commit | 3b 2, 3b 6 |

A queue failure is the costliest single event in the record: a pull request with at least one failed queue run took a median 1.40 h from first queue run to merge, against 0.41 h without (3b 2). That includes pull requests fixing their own defects.

## Failure modes and guards

| Observation from the liaison | Verdict | Guard |
|---|---|---|
| Reports are cut at about 300 characters | **Wrong for reports in current code**: the parent gets the whole summary up to 4 KiB, with newlines flattened; the status line keeps 256 characters (2b 2). A 2026-10-27 note records long sends arriving cut at about 500-650 characters, cause not traced (3c a) | Lead with the result; put detail in a pushed file |
| A lead sleeps on a `merged` wait and misses a queue failure | Since #874 the wait resolves on removal from the queue after 1 min. Two gaps remain: a removal before the wait was registered is ignored, and a failing queue read sleeps on (2b 3). The hour-long miss itself is not in the record | Report the wait right after queueing; check the queue state on waking |
| A lead's sandbox died and unpushed work was lost | Not found in the record. By design the server tries to boot a dead VM to commit and publish its work, and only if the VM is gone are changes since the last published commit lost (2b 4) | Push every commit |
| A wake failed with "vm runtime ... it is not running" | The string exists (`internal/runtime/vm/lifecycle.go:59`); no recorded case | Not established |
| Launches fail with "coordination is not reachable" | A client-side socket failure; a call that hits its own 30 s timeout is not retried (2a 4). No recorded case | Retry the start with the same idempotency key |
| One bad pull request fails everything queued behind it | **Confirmed, one incident.** On 2026-11-17 #1025's failure repeated in the three entries stacked on it, and #1008's in two. Those pull requests merged 1.4 to 5.2 h after first queueing, some of that from their own later failures (3b 4) | Do not queue behind a red entry; re-queue on a clean base |
| A fresh reviewer catches blockers the author missed | Confirmed for #1120 to #1125 and for large changes (#1091: 4 blockers in 6 reviews). The three flaky-test bugs were found by the author's reproduction, before any reviewer saw the fix (3a 2, 3a 3) | Review and reproduction are separate steps |
| Text changes break only in the queue | One case: #1025 (3b 3) | Run the integration suite locally for usage-text changes |
| A small urgent fix needs no fan-out | One case, #1127. The sentence was dropped from the current note (3c i) | Put it back |
| Sonnet for sweeps, Opus for judgement | No outcome recorded either way (3c j). Here six Sonnet collectors and two Opus ones all returned usable files | Unproven; a cost choice |

Also found: a sub-session's `waiting` report does not reach its parent until an hour-long quiet notice (2b 2).

## Not established

- Any measured speed or quality gain from a lead with subagents over a single agent on real repository work. No local comparison, and no published study that I found.
- Whether a second reviewer from another family beats a second run of the same one at equal cost.
- How much of the subagent slowdown is CPU share, and how much time the workflow cap costs.
- Whether omp's extra findings come from the model, the harness, the time spent or the reading of the brief.
- Whether fast mode was on for any omp review.
- Either reviewer's false-positive rate.
- Why this sandbox has one vCPU; the host's size; how many sessions it carries before it degrades.
- Sub-session start time under load.
- Any outcome difference between Sonnet and Opus workers.
- The dollar cost of an in-session subagent.

## This run, as data

25 minutes from the first command to the last judge verdict: 8 collectors, 6 review arms, 2 judges, 4 sub-sessions. What went wrong: the workflow cap cost two minutes and a restart; two collectors hit the coordination rate limit; every collector overshot its length limit; my notes carried an "8 peers" cap that #664 removed on 2026-10-30, and a collector then wrote that the code never had one (2b 1); the task's premise about 300-character reports was wrong for reports; and the first push contained a teammate's name and a host name, since rewritten. Two reviewers then attacked this report. Opus found 15 issues (3 blockers) in 6.6 minutes; omp `gpt-6.2-astra` found 15 (7 blockers) in about 10. Seven of omp's were not in the Opus list, among them a superseded preprint figure, the wrong history of the peer cap and the wrong provenance of the #547 race, each of which I checked and found correct. Opus alone raised four. Findings and responses: `experiments/attack-opus.md`, `experiments/attack-omp.md`.
