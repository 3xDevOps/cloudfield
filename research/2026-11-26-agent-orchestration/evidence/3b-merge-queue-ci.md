# 3b: merge queue and CI record, 3xDevOps/cloudfield, 2026-11-05 to 2026-11-26

Method: `gh run list --event merge_group --created ">=2026-11-05" --limit 1000` returned 543 runs (all workflow "CI"; 543 < limit, so complete for the window). Per-run jobs via `gh run view ID --json jobs`, failing-log greps via `--log-failed`, per-PR queue history via GraphQL `timelineItems` (AddedToMergeQueueEvent / RemovedFromMergeQueueEvent / PullRequestCommit). Duration = `startedAt` to `updatedAt` of the run (wall clock of one run, excludes time waiting behind other entries). Nothing was rerun or changed.

## 1. Which jobs run where (claim check)

| Claim | Evidence | Source | Tag |
| --- | --- | --- | --- |
| PR CI runs only changes, audit, lint, build-and-test, store-tests, dashboard | Comment: "A pull request runs changes, audit, lint, build-and-test, store-tests and dashboard only. The merge queue and the nightly run on main run every job" | `.github/workflows/ci.yml:4-9` | DESIGN-FACT |
| Integration/e2e suites are queue-only | `suite` job `if: ... github.event_name != 'pull_request' ...`; docs: "CI runs the Docker suites in the merge queue and on `main` only, with `-race`" | `ci.yml:330-332`; `docs/testing.md:100-101` | DESIGN-FACT |
| Observed: PR run for #1025 | PR run 44146568325 jobs: changes, audit, dashboard, store-tests, build-and-test, lint all success; `suite:skipped`; CI gate success. Queue run 44149116775 for the same PR had 21 jobs. | `gh run view 44146568325 --json jobs` | MEASURED |
| `-race` is queue/main only | `TEST_RACE: ${{ github.event_name != 'pull_request' && '-race' || '' }}`; docs: "CI runs pull requests without `-race`, except `internal/store`" | `ci.yml:253`; `docs/testing.md:43-44` | DESIGN-FACT |
| Queue also runs windows, windows-install, kvm, edge-image, release-build, arm64 | jobs of `ci-suite.yml` (kvm/edge-image only when the diff can affect them) | `ci-suite.yml`, `ci.yml:12` | DESIGN-FACT |

Current queue ruleset (ruleset 21073688, read today; may not equal the window's config): `max_entries_to_build: 2`, `max_entries_to_merge: 5`, `min_entries_to_merge: 1`, `min_entries_to_merge_wait_minutes: 5`, `grouping_strategy: HEADGREEN`, `merge_method: SQUASH`, `check_response_timeout_minutes: 90`; required check is only "CI gate" (`gh api repos/3xDevOps/cloudfield/rulesets/21073688`). DESIGN-FACT.

## 2. Queue volume, outcomes, duration (543 runs, 351 distinct PRs)

| Metric | Value | Tag |
| --- | --- | --- |
| Outcomes | success 316 (58.2%), failure 149 (27.4%), cancelled 77 (14.2%), startup_failure 1 | MEASURED |
| Failure share of runs that finished pass/fail | 149 / 465 = 32.0% | MEASURED |
| Duration, all pass+fail runs | n=465, median 1412 s (23.5 min), p90 1882 s (31.4 min), max 3437 s | MEASURED |
| Duration, success only | n=316, median 1421 s, p90 1890 s | MEASURED |
| Duration, failure only | n=149, median 1404 s, p90 1835 s (failures do not fail fast; the whole suite runs) | MEASURED |
| Per day (created date: success / failure / cancelled) | 11-05 34/21/11; 11-06 2/1/0; 11-07 11/2/2; 11-08 5/4/2; 11-09 7/5/1; 11-10 26/5/8; 11-11 14/10/9; 11-12 14/6/4; 11-13 21/10/10; 11-14 18/9/2; 11-15 14/6/2; 11-16 46/13/12; 11-17 27/28/11; 11-18 35/8/1; 11-19 10/5/2; 11-20 5/3/0; 11-21..23 none; 11-24 14/6/0; 11-25 12/7/0; 11-26 1/0/0 (11-05 also 1 startup_failure) | MEASURED |
| Worst days by failure share | 11-17: 28 of 66 runs failed (42%); 11-05: 21 of 67 (31%) | MEASURED |
| PR outcome of first queue run | success 208, failure 92, cancelled 50, startup_failure 1 (of 351 PRs) | MEASURED |
| PR time, first queue run to `mergedAt`, PRs with no failed queue run | n=248, median 0.41 h, p90 0.66 h, max 2.60 h | MEASURED |
| Same, PRs with at least one failed queue run | n=102, median 1.40 h, p90 3.67 h, max 32.30 h | MEASURED |

Only one PR (#1126) of the 351 had not merged by the time of the query.

## 3. What failed, and was PR CI blind to it?

Failing leaf jobs per failed run (149 runs; a run can list several jobs):

| Group | Runs | Note |
| --- | --- | --- |
| Failures with no PR-level job failing (only queue-only jobs red) | 93 of 149 (62%) | dashboard-e2e only 43; integration only 21; windows/windows-install/edge-image/release-build/desktop 16; kvm only 7; combos 6 |
| Failures with a PR-level job (build-and-test, lint, dashboard) red | 56 of 149 (38%) | build-and-test alone 45; those run on PRs but without `-race` and not on the queue's combined tree |

Of the 113 "origin" failures (below), 73 had only queue-only jobs red, and for all 73 the last pull_request CI run on the PR branch before the queue run was `success` (73 of 73, looked up per PR with `gh run list -b BRANCH --event pull_request`). MEASURED. Caveat: the queue only accepts PRs whose "CI gate" is green, so this is partly by construction; the finding is how often the extra suites then fail (73 of 466 non-cancelled queue runs, 15.7%).

Concrete instance, text-changing PR passing PR CI and failing only in the queue (MEASURED):
- #1025 "docs: name the cloudfield commands and CLOUDFIELD_ variables": the PR touches 100+ files (docs plus Go usage strings in `cmd/aether/*.go`; branch commits include "feat(cli): name cloudfield and cloudfield-server in help and usage text"). PR CI run 44146568325 green at 18:19Z with `suite` skipped. Queue run 44149116775 (18:33Z) failed `suite / integration (server-rest, browser)` with `--- FAIL: TestIntegrationTerminalExec` (`terminal_exec_integration_test.go:64`, an assertion on the CLI usage line). The PR's last commit is "test(server): expect the cloudfield usage line from terminal exec", pushed 19:47Z after the failure (timeline commit event).
- Other queue-only failures where the PR had to push commits afterwards, with PR CI run ids: #1002 run 43992858795 (integration + e2e, `TestRunDeletionPrunesEvidenceOnceInTheBackground`); #971 run 43790220755 (`TestIntegrationAssistantContainerIsolationDocker`); #958 run 43767195614 (edge-image, `TestEdgeUpgrade`); #798 run 42311193659 (e2e sticky-notes.spec.ts); #860 run 42984690948 (e2e account-sharing.spec.ts); #1016 run 44111829800 (windows: `TestInstallAllReplacesNothingUnlessEveryFileMatches`, `TestCheckKVMShowsTheRealError` fail on a Windows runner). 36 origin queue-only failures were followed by new commits on the PR.
- Cross-PR semantic conflict caught only on the stacked tree: #1008 "feat(dashboard): match the dark theme to the website's teal and gold" failed build-and-test with `TestAssetCopiesMatchTheirSources` ("templates/images/cloudfield-favicon.svg differs from ../../../web/public/cloudfield-favicon.svg; copy it again"), runs 44166907677, 44174426744, 44184463595. DESIGN-FACT on PR CI: build-and-test does run on PRs, so this is a unit test red only on the combination or on re-merged base.
- Against the claim: not every queue-only failure is text-related; most e2e/kvm/windows ones are timing or platform failures (see flakes).

## 4. 2026-11-17 cluster (28 of 66 runs failed)

| PR (title) | Queue runs (order) | First enqueue to `mergedAt` | Cause | Tag |
| --- | --- | --- | --- | --- |
| #1025 docs/CLI rename | f f f f f f s | 18:33Z 11-17 to 01:38Z 11-18 = 7 h 05 min | Own defect first (TerminalExec, run 44149116775); then collateral from #1008's asset test (runs 44170210770, 44178794414, 44184465284); then kvm flake (44192611083, `TestKVMBrowserInTheSessionVM`, passed later with no new commit). Removed from queue "manual" 19:43 and 23:14, "failed_checks" 01:10 | MEASURED |
| #1028 fix(cost) | f s | 18:46Z to 20:08Z = 1 h 22 min | Collateral: failed run 44151661959 on the same TerminalExec failure while stacked on #1025; re-queued on a base without #1025 (run 44161649665 base 25978398a) and passed | MEASURED |
| #1029 test(dashboard) | f f s | 18:47Z to 23:59Z = 5 h 12 min | Collateral (44153755304, TerminalExec); then own-run flake `TestHarnessUpdateTimeoutStillLaunches` (44161653454, build-and-test, no new commit before the passing run); removed "failed_checks" 20:55 | MEASURED |
| #1031 test(dashboard) | f f f c s | 18:55Z to 23:47Z = 4 h 52 min | Collateral (44156389116), then `TestRunDeletionPrunesEvidenceOnceInTheBackground` (44166066652, integration), then e2e (44174424581) | MEASURED |
| #1008 dark theme | c f f f s | 19:13Z to 02:00Z 11-18 = 6 h 47 min | Real defect: `TestAssetCopiesMatchTheirSources` x3; PR pushed a fix 22:51Z; removed "failed_checks" 22:42 | MEASURED |
| #1033 | f f c s | 21:22Z to 23:37Z = 2 h 15 min | Collateral: `TestAssetCopiesMatchTheirSources` (44179677875, 44188165725) stacked on #1025/#1008 | MEASURED |

Stacking evidence: four consecutive entries (#1025 -> #1028 -> #1029 -> #1031; each run's base is the previous entry's head SHA, e.g. 44149116775 head aa08404c2 is base of 44151661959) all failed on the one `TestIntegrationTerminalExec` assertion, one run per 13-15 min. So the user-reported claim (#1025 and #1008 failed and held back what was behind them) is confirmed; the held-back PRs were #1028, #1029, #1031, #1033, each delayed 1.4 to 5.2 h.

## 5. Stacking and the cost of one bad entry

- Each merge group is one PR: all 543 branches are `gh-readonly-queue/main/pr-N-<base sha>`, and 539 of 543 have a base SHA equal to another run's head SHA (entries are built on top of each other). Merged groups' SHAs are squash commits on main with one PR each. MEASURED (`git log` of run SHAs, branch names).
- Concurrency (overlap of run intervals): max 6 at 2026-11-13 21:08Z; time-weighted 1 run 46%, 2 runs 50%, 3+ runs 4%. MEASURED. Matches `max_entries_to_build: 2`; peaks above 2 are rebuilds overlapping cancelled runs.
- Cost of one failure: of 149 failed runs, 113 are "origin" (base entry passed or base is main), 25 are collateral (base entry failed on the same job), 10 stacked on a failure with a different job, 1 on a cancelled base. 35 of 149 (23.5%) therefore sat on a failed base. MEASURED.
- An origin failure leaves on average 0.96 stacked descendant runs (median 1, max 5) that end cancelled (50), failed (35) or passed (24) across 113 failures; 67 of 113 had at least one. MEASURED. 77 cancelled runs total: 35 had a failed base, 23 a cancelled base, 19 a passed base.
- Time cost: PRs with a failed queue run took median 1.40 h versus 0.41 h without (3.4x), p90 3.67 h versus 0.66 h. MEASURED. 242 PR-hours spent between first queue run and merge by the 102 PRs with a failure (sum of their delays).

## 6. Flake rate (failed, then passed on re-queue, no new commits)

- Definition: origin failure (not collateral), the same PR later has a successful queue run, and no PR commit or force-push dated after the failed run's creation (GraphQL `PullRequestCommit.committedDate`, `HeadRefForcePushedEvent`; committedDate can predate a push, so this is a proxy).
- Result: 41 of 113 origin failures (36%) across 33 distinct PRs. By job: build-and-test 19, windows/windows-install/edge-image/release-build 11, dashboard-e2e 8, integration 8, kvm 2 (counts are failing jobs, a run can have several). Of 351 PRs that queued, 33 (9.4%) hit at least one such failure. 42 other origin failures were followed by new commits (real fixes or rebases). 27 + 3 origin failures had no later successful run in the data (the PR merged on a later run or run list ended).
- Repeating tests across different PRs (MEASURED, grep of `--- FAIL`): `TestAPromptDropsAWakeNotYetWritten` in 6 runs on 6 PRs (#993 run 43879972191, #1014 44019965002, #1081 44457436053, #1099 45371107427, #687 45590606145, #1109 45557994530; message "wait_test.go:842: 1 wakes after the prompt"); `TestSaveAfterRestartRecordsTheTerminalsFallbackImage` 5 runs (#1074 44429661068, #1090 44695060943, #1093 45354162292, #1099 45368066556, #688 45587596132); `TestTerminalExecScriptMarksOnlyACommandStoppedAtTheLimit` 4 runs, 4 PRs (#913, #904, #941, #969); `TestKVMNetwork` 4 runs; 54 of 149 failed runs involve a dashboard-e2e job.
- Retry example: #1109 failed build-and-test twice (45557994530, 45560651971) with no commit between, then merged. #1056 and #1094 and #1090 (`integration` flakes) similar.

## Could not establish

- Whether each "no new commit then passed" failure was a flake or a fix landing on main between runs (a PR rebased by the queue picks up main); no per-failure root cause for the 41.
- The queue settings during the window (only today's ruleset read); rulesets history not queried.
- Time each PR waited in the queue before its first run starts (only run start and PR timeline for failed-or-cancelled PRs were fetched; no-failure PRs measured from first run).
- Whether PR #1025's usage-string test mismatch was the test expecting the old name or the new one at that commit (log shows only the assertion line; fix commit is named "expect the cloudfield usage line").
- Dollar or runner-minute cost of queue reruns (no billing data read).

## Surprises

- Queue entries are single-PR and stacked, so one bad entry fails the ones behind it with the same error; #1028 recovered by re-queueing without #1025.
- Failed runs take as long as green ones (median 23.4 vs 23.7 min): no fail-fast, a bad entry blocks its followers for a full cycle.
- 62% of failed runs had no PR-level job red, yet the suite that failed is skipped on PRs by design; at the same time 36% of origin failures cleared on retry with no new commit.
