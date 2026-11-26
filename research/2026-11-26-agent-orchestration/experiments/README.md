# Experiments run on 2026-11-26

All times are UTC from `date` inside each sandbox. Four sub-sessions were started, all as children of run-805s2y01bd, and all were cancelled after they reported. No other sessions were started for experiments.

## Review experiment

Task: review an already-merged pull request blind (no later commits, no GitHub, no team memory, no web) and list defects. The two pull requests each introduced a race that a flaky test exposed about three weeks later:

| Reviewed | Commit | Known defect | Fixed by |
|---|---|---|---|
| #547 | 175f1ad7c | budget status read outside `publishMu`, so a stale state is published last | #1028 |
| #716 | a3171a811 | `raise` re-reads a card in the gap before `decide` hands over its decision | #1038 |

Arms, one run each per pull request:

| Arm | How started | Session | Result file |
|---|---|---|---|
| Opus subagent | Agent call in the lead's session, model opus, effort high, isolated worktree, launched after 06:51:29 | none | `opus-subagent-547.md`, `opus-subagent-716.md` |
| Opus sub-session | `subsession start --harness claude --model opus --mode enhanced`, owner's account, 06:51:16-17 | run-y22nvj6436, run-mrnk3an8dh | `opus-ss-547.md`, `opus-ss-716.md` |
| omp gpt-6.2-astra | `subsession start --harness omp --model openai-codex/gpt-6.2-astra --mode enhanced --account <shared account>`, 06:51:17-18, then `/fast on` sent while it was provisioning | run-tzqst7xdjm, run-gqb76abhz3 | `omp-astra-547.md`, `omp-astra-716.md` |

Briefs: `brief-sub-session.md` and `brief-subagent.md` (identical except for delivery).

Timings and cost:

| Arm | PR | Started | First action | Finished work | Report received | `cost_usd` or tokens |
|---|---|---|---|---|---|---|
| Opus subagent | #547 | after 06:51:29 | 06:51:52 | 07:00:18 | n/a | 97,793 tokens |
| Opus subagent | #716 | after 06:51:29 | 06:51:39 | 07:00:52 | n/a | 145,625 tokens |
| Opus sub-session | #547 | 06:51:17 | 06:51:54 | 06:54:18 | 06:54:32 | 0.863 |
| Opus sub-session | #716 | 06:51:16 | 06:51:41 | 06:55:06 | 06:55:54 | 1.618 |
| omp astra | #547 | 06:51:18 | 06:51:52 | 07:01:00 | 07:02:48 | 4.096 |
| omp astra | #716 | 06:51:17 | 06:51:51 | 07:01:20 | 07:03:17 | 7.656 |

Conditions that bias the comparison:

- The two Opus subagents shared the lead's one vCPU with eight collector subagents; `/proc/pressure/cpu` read `some avg10=99.96 avg60=95.75` at 06:58:45. The sub-sessions each had their own VM.
- The subagent on #547 also wrote and ran a scratch test; the sub-session on #547 ran the package tests; the subagent on #716 ran selected package tests; both omp runs and the Opus sub-session on #716 ran nothing.
- The subagents ran at effort high. `subsession start` has no effort setting, so the sub-sessions ran at whatever their harness defaults to.
- The brief capped findings at 10. Both omp runs returned exactly 10; the Opus arms returned 5 to 7.
- Whether fast mode was active for omp is not established.
- Blinding rests on each reviewer's word. Every checkout contained the later fixes in its history.

## Judging

Two Opus subagents, one per pull request, were given the three result files with arm and model lines removed, labelled A, B, C, plus the known defect. Each built a table of distinct issues and traced every blocker and each reviewer's top three findings in the code at the reviewed commit. Verdicts: `judge-547.md`, `judge-716.md`.

Label key: #547: A = Opus sub-session, B = omp, C = Opus subagent. #716: A = omp, B = Opus subagent, C = Opus sub-session.

Limits: the judges are the same model family as two of the three arms; they rejected none of the 32 findings they checked; writing style may have revealed the arms.

## Orchestration timings of this run

| Step | Time |
|---|---|
| First command | 06:48:49 |
| Workflow with 8 collectors launched; ran 2 at a time; stopped | about 06:50 to 06:52 |
| Same 8 briefs started as subagents | 06:52:08 |
| Collectors returned after | 2.0, 2.3, 2.6, 5.1, 6.0, 6.5, 9.4 and 15.2 min |
| Judges started / last verdict | 07:08 / 07:14 |

Collector models: six Sonnet (1a, 1b, 1c, 3a, 3b, 3c), two Opus (2a, 2b). All ran at effort high.

## Attack on the report

Two fresh reviewers were given the pushed report and told to attack it, with one brief:

| Reviewer | How started | Time | Findings | File |
|---|---|---|---|---|
| Opus | subagent in the lead's session, effort high | 6.6 min | 15 (3 blocker, 9 should-fix, 3 minor) | `attack-opus.md` |
| omp `openai-codex/gpt-6.2-astra` | sub-session run-hgdk0zhp99 on the shared omp account, Enhanced, then `/fast on`; fast mode "not reported by this harness" | 10.2 min from launch to report | 15 (7 blocker, 8 should-fix) | `attack-omp.md` |

Eight findings overlap. Seven were raised by omp alone and four by Opus alone. Each file ends with what the lead changed. The omp reviewer read the commit from before the Opus fixes.

That makes five sub-sessions in this run: four for the review experiment and this reviewer. All were cancelled after reporting.
