# 3a: GitHub record, 3xDevOps/cloudfield, 2026-11-05 to 2026-11-26

Source for everything below: `gh pr list/view -R 3xDevOps/cloudfield --json ...` run 2026-11-26, plus PR bodies and comments fetched in this run. Review facts are what the PR body or comments SAY (written by the authoring session); I did not verify them against the code. Tags: MEASURED, ADVICE, OBSERVED-ONCE, DESIGN-FACT.

## 1. Throughput (MEASURED, `gh pr list --search "created:>=2026-11-05" --limit 600`)

- 355 PRs created in the window: 327 merged, 11 closed unmerged, 17 open (13 drafts; 11 created 2026-11-26).
- Opened per day (11-05..11-26): 21, 8, 11, 2, 6, 34, 22, 8, 37, 14, 25, 40, 35, 36, 13, 5, 0, 0, 1, 17, 9, 11 (11-26 partial). Mean 16.1 opened/day over 22 days.
- Merged per day (of those 327): 11-05 18, 06 2, 07 12, 08 6, 09 6, 10 28, 11 17, 12 18, 13 23, 14 19, 15 15, 16 48, 17 28, 18 42, 19 13, 20 5, 24 14, 25 13 (none 11-21..23). By merge date there are 353 merges in the window, because 26 older PRs (#687-#774) also merged (`--search "merged:>=2026-11-05"`). About 14.9 merges/day by created-in-window; peak 48 on 11-16.
- Closed unmerged by day: 11-05 1, 11-07 1, 11-08 1, 11-13 1, 11-15 3, 11-18 1, 11-19 3. Sizes +3/-1 to +275/-7; examples: #1063 "Migrate workflows to Blacksmith runners" (+125/-139), #1075 queue stop at first failed job.
- Open-to-merge time (created to mergedAt, so it includes draft time), n=327: p10 0.86 h, p25 1.33 h, median 2.66 h, p75 5.06 h, p90 16.96 h, max 204 h.
- Size (additions+deletions), n=327: median 153 lines and 6 files; p25 45, p75 462, p90 1215; max 17,120 (#837). Buckets: <10: 32; 10-49: 56; 50-199: 100; 200-499: 60; 500-999: 35; >=1000: 44.
- Type mix of merged titles: fix 144, feat 91, test 36, ci 19, perf 18, docs 16, build 2, chore 1.
- Authors (all 355): not-varram 167, ilovecrayons 133, jaydenhchen 32, AviationNerd 18, artremizov 4, app/blacksmith-sh 1.

## 2. Review record

Body-only scan of the 253 merged feat/fix/perf PRs: 137 contain review wording (adversarial, fresh-context, "## Review", "Merge gate"); 84 name Opus; 71 name Greptile; 80 have NO review wording in the body (e.g. #1118, #1113, #1109, #1101, #1049, #1015, #1013). I read comments on 16 of those 80: none held review text, so "not stated" most likely means "not recorded", but I cannot prove no review happened. Of 167 merged PRs (all types) with review wording, 38 say subagent or `claude -p`, 23 say sub-session, the rest do not say.

Reviewer kinds seen: fresh-context Opus subagent (`claude -p --model opus`, Claude Code subagent), fresh-context Opus Cloudfield sub-session (run id given), Greptile bot and Greptile CLI (`greptile review --apex --agent`), parent/lead session, human reviewers (ilovecrayons, AviationNerd), design review by Opus on dark screenshots, and, only from 2026-11-26, omp (oh-my-pi) on `openai-codex/gpt-6.2-astra`.

| PR | Size (+/- , files) | Reviewer kind(s) | Findings / blockers as stated | Notable catch |
|---|---|---|---|---|
| #1028 fix(cost) | +23/-20, 1 | 1 Opus subagent; Greptile P2 | approve, 0 blocking; 3 non-blocking kept; Greptile asked for deterministic test, declined | author found the bug (below); reviewer confirmed race and no deadlock |
| #1032 fix(edge) | +9/-2, 1 | 1 Opus subagent | approve, 0 blocking; 2 non-blocking applied | none beyond author's |
| #1038 fix(assistant) | +6/-1, 2 | 1 Opus subagent | approve, 0 blocking; 3 non-blocking applied, 1 kept with reason (withdraw deadlock risk) | lock-order check |
| #1008 feat(dashboard) | +148/-120, 37 | author only | body: "my own review, as the implementer, and not a cold one: no independent reviewer has looked at this version"; merged 11-18 | none recorded; no later review in body or comments |
| #1025 docs rename | +1672/-1639, 133 | 1 Opus fresh reviewer; Greptile P1 | 0 blockers, 5 should-fix, 6 nit; Greptile P1 valid (Windows `&` breaks `cloudfield.cmd`) | reviewer ran `go test ./cmd/... ./internal/cli/...`; the merge queue then failed an integration test PR CI does not run (`terminal_exec_integration_test.go:63`, usage string renamed); 4 stacked queue groups failed behind it |
| #1003 feat(edge) | +1398/-117, 45 | parent session x2, Opus security pass, Greptile | 2 majors + minors fixed; security: 1 Medium (`edge move` does not prove the target holds the pinned key), wording fixed, proof not built; Greptile 1 P1 + 2 P2 + P1 (group), all fixed | Greptile P1: moves can break old connectors |
| #909 feat(server) | +991/-41, 26 | "the adversarial review", Greptile, human | counts not stated | review led to fixes of the client-path update (kernel installed before new server serves; release without kernel) |
| #837 feat(server)! | +15525/-1595, 401 | Opus adversarial rounds, Opus design, Greptile bot + CLI 16 passes | R1: 1 high, 7 med, 8 low; R2 (4 parallel parts): 2 blocking; delta reviews: 2 more blocking (git push outliving revoke; watch receipt naming a session); Greptile CLI findings to pass 15 | high: multi-repo session checked only primary workspace (read/write of extra repo) |
| #798 feat(dashboard) | +1503/-56, 39 | Greptile x3, human x4 | no adversarial review recorded; e2e not executed, design review "outstanding" | flake fixed later in #821 |
| #959 feat(server) | +3648/-205 | 6 fresh Opus passes, Greptile 15 threads | blockers: pass 4 (cancel during download reported done but update installed), pass 6 (decision lock taken twice); majors 1+4+3+2 | 2 blockers in 6 passes; all fixed |
| #964 ci promote | +1704/-155 | 2 fresh-context reviews, Greptile 4 | 0 blockers; 6 findings fixed, 3 minors fixed | later fixed by #976 (below) |
| #1091 feat(vm) | +1079/-48 | 6 fresh-context reviews | blocking in review 1 (symlink bind to `/etc/shadow`), review 3 (2: FIFO freeze, huge sparse copy), review 4 (1: nested-dir fd growth); reviews 2, 5, 6 none | 4 blockers in 6 reviews |
| #1090 fix(acphost) | +189/-11 | 4 fresh Opus reviews | review 1: 1 blocker (adopted session lost every thought); 2-4 none | |
| #1086 fix(guest) | +105/-10 | 5 fresh reviewers | 4th: 1 blocking (KVM test could pass on broken build); 5th none | |
| #1085 fix(scheduler) | +103/-16 | author `claude -p opus`, adversarial at hand-off, 2 more | 4 findings; hand-off review: 1 major ("the PR does not fix the dying wakes"; reworded, no Fixes line) + 3 minor; later 2 + 0 blockers | |
| #1069 fix(scheduler) | +1507/-95 | adversarial at d4d959b + 2 Opus | 6 findings fixed (e.g. restart thawed every frozen VM); 2 follow-up reviews found more, fixed | |
| #1052 fix(vm) | +292/-112 | 2 fresh Opus | R1: 1 major (deny-list opened routed container networks), 1 minor; R2: 6 points | major proven by KVM run 44303300640 |
| #1039 fix(vm) | +36/-26 | 2 fresh Opus | 0 blockers; ~7 doc/comment findings | |
| #1068 fix(vm) | +80/-23 | 2 Opus | 0 blockers; 1 nit, 1 minor (stale comment) | |
| #860 feat | +1712/-96 | Opus x2 passes, design Opus x2, Greptile bot 11 threads + CLI 7 | P1: 1 blocking, 5 should-fix, 9 nit; P2: 0; design: 4 blocking | |
| #843 fix | +264/-26 | Opus 4 rounds, copy review | "no blocking findings left" (counts not stated) | |
| #838 fix(dashboard) | +336/-26 | Opus 3 rounds, design 2 rounds, Greptile CLI | counts not stated; CLI 2 real (P1 drag leaves card open) | |
| #818 fix | +256/0 | Opus adversarial | 0 blockers; fixes applied | two Greptile findings arrived after auto-merge (#819) |
| #1105 fix(dashboard) | +377/-54 | 4 rounds fresh Opus with screenshots | blocking findings "checked by a second agent" (counts not stated) | |
| #1110 feat(dashboard) | +265/-57 | 2 Opus code + 1 design + 2 more | rounds 3-4 no blocking; earlier counts not stated | |
| #1002 feat(server) | +733/-105 | parent adversarial, Greptile | no blocker or major; 5 numbered items | |

Open PRs with a second model family (all created 2026-11-26; none merged at snapshot):

| PR | Opus reviews | omp on gpt-6.2-astra | Did omp find what Opus did not? |
|---|---|---|---|
| #1120 desktop installer launch (+435/-17) | 4 fresh sub-sessions: blocking only in review 3 (a regression from the first fix, red CI job); review 4 at fa21b36cf "no findings" | Review 5 at the same fa21b36cf: request changes, 3 blocking (CI job with PTY+Xvfb passed every gate; `whoami.exe` found via mutable `SystemRoot`; root check depends on `PATH`'s `id`) plus should-fix | Yes: 2 fixed, 1 declined with reason, 5 should-fix (3 fixed) |
| #1122 plan retiring an address pair (+417/0) | 3 Opus passes: 1 blocker + 8 should-fix + 7 notes; 6 should-fix + 7 notes on the revised plan; 3-lens workflow | 5 blockers + should-fixes, "folded in except the three below" | Yes (5 blockers after 3 Opus passes), though overlap with Opus findings is not stated |
| #1123 connector listings (+11/-11) | M-items only, no blocking | 1 blocking: descriptions said no tool merges PRs, but `aether_pr_merge` exists (`internal/ops/pr.go:35`) | Yes, a factual error, fixed |
| #1121 legal pages (+33/-20) | 1 blocking (merge order), 2 minors | same blocking, plus a false "No connector tool merges pull requests" term (answered: pre-existing), wording fix | Partly: one wording fix, one pre-existing error |
| #1124 connector docs (+88/-30) | 2 Opus reviews: B1 blocking each time (fixed), several M | body lists run-c2ftbmv3qy as a reviewer but gives no omp findings | not stated |
| #1127 CI runners (+43/-85) | nit only | nit only (labels wording) | No |
| #1026 module path (+4274/-4274) | no findings | no findings after comparing all 1,530 files and reproducing six protobuf outputs | No |
| #1125 plan (+274/-18) | 1 Opus: BLOCKING 1, partly rebutted | no omp review stated | n/a |

Count: 7 PRs with an omp review stated (#1120, #1121, #1122, #1123, #1127, #1026, plus #1124 unstated findings). omp added a blocker or factual error Opus had missed in 3 (#1120, #1122, #1123), 1 partly (#1121), 0 in 2 (#1127, #1026). Zero merged PRs in the window had a second-family reviewer; the rule dates from 2026-11-26 06:36 UTC (#1026 working notes: "the owner's new rule: a second adversarial reviewer on omp"). OBSERVED-ONCE, single day, small n.

Setup problems recorded: #1026 first omp run, started with no model given, "ran through the Cursor provider": `Cursor RATE_LIMITED_CHANGEABLE: Model unavailable on Free` and stopped without reviewing. Fast mode was not confirmed in #1120, #1026 and #1127 (`/fast on` sent, no answer or tier `openai=none`).

## 3. Flaky-test PRs and fresh-context reviewers

Product bugs (MEASURED by forced-delay reproduction on a one-CPU sandbox; each body says "the test is right, the product is wrong"):
- #1028 (found in merge-queue run 44112389958): `cost.Service.announce` read budget status, compared with last published state, and published under different locks; a stale `Warn` read published after a raise had published `OK`, so a real warning was never sent. Found by the authoring session (run-cqnmfrcqjb) from the CI log, proven with 500 ms + 200 ms scratch delays: main 0/30, fix 30/30. Reviewer: approve, confirmed `Admit`'s refusal had the same race.
- #1032 (run 44111769058, also a 2026-11-10 flake): on server stop, the relay dialed a new connection and sent `Open` before the agent's control loop saw cancellation, so the agent answered `403 connection limit reached`. Found by the author (run-qsynna70xc) with temporary logging in a loaded run (failed on run 39 of 60). The first fix checked the derived session context and still failed about 3 of 36 loaded runs; the second checked Run's own context (50/50 under load). That correction came from the author's load test, not the reviewer; the reviewer approved the final design and asked for two non-blocking changes.
- #1038 (run 44210612996): `decide` approved a card then handed the result to the waiting call; a re-ask's second read landed in the gap and returned "was decided while this call asked again". Found by the author (run-cqnmfrcqjb); main failed 30/200 under `stress --cpu 4`, fix 200/200. Reviewer: approve, checked lock order; kept `withdraw` outside the lock with a reason.
- Reviewers in all three: 1 Opus subagent, 0 blocking, no bug found. The bugs came from author-side reproduction, not review. Greptile: P2 on #1028 (declined), none on #1032 (5/5), none on #1038.

#1120, #1122, #1123-#1125 fresh-context blockers (OBSERVED-ONCE, 2026-11-26): see the second table. In short, #1120 review 3 found that the fix for review 1's finding broke the build (tee wait race, 2 of 6 parallel runs); #1124 B1 (note told readers to add connectors at a URL the CLI does not print), second B1 (steps still said the old URL), #1125 BLOCKING 1 ("plan publishes an end for the old addresses", partly rebutted) and 9 further fixes; #1123 and #1121 per the table.

## 4. Defects that escaped review

- Reverts in the window: 0. `gh pr list --search "revert in:title"` returns only #635 and #241 (created 2026-10-27 and 2026-09-26); `git log --since=2026-11-05 --grep='^revert'` in the local clone is empty.
- Follow-up fixes whose body names an earlier in-window PR as the cause (not exhaustive; I scanned 65 fix-to-earlier-PR reference pairs within 48 h and read the ones below):

| Fix | Escaped from | Gap | What escaped | Review of the original |
|---|---|---|---|---|
| #821 | #798 | 32 h | flake in the sticky-note rail on a main push run | no adversarial review recorded; e2e not run; design review outstanding |
| #1083 | #1049 | 26 h | on-demand wake restore could lose every process of a saved VM ("Since #1049") | body has no review wording; #1083 itself then took 5 fresh reviews |
| #907 | #891 | 2 h | helper evicted process records still being read; "Greptile found it on #896" | Greptile 2 passes; a section titled adversarial review |
| #976 | #964 | 1 h | promotion could proceed with push CI not green; found by Greptile on #963; workflow "has never been run" | 2 fresh-context reviews, 0 blockers, 9 findings fixed |
| #819 | #818 | 2 h | home repair after a stale reservation; two Greptile findings posted after auto-merge was on | Opus adversarial: 0 blockers |
| #969 | #959 | 3 h | 3 review-bot findings | 6 Opus passes (2 found blockers) + 15 Greptile threads |
| #1039 | #1013 | 21 h | VMs booted before the relay kept the host's DNS | not stated |
| #1052 | #1015 | 31 h | split default route case ("Not covered" in #1015) | known gap |
| #899 | #892 | 14 h | permission tiers never checked in a real VM | not stated |

Caught before main (merge queue, not review): #1025's integration test failure above; #1003 dropped from queue run 44013488763 by a harness port race (`bind: address already in use`). Queue failures are mentioned in 21 of 253 merged code PR bodies.

## 5. Fan-out, sub-sessions, workers, integrator, split

- Merged bodies (n=327) mentioning: sub-session 35, worker 17, "stacked on/pull" 18, "rebased onto" 13, fan-out 2 (both code, not orchestration), integrator 0, "merge conflict" 0.
- DESIGN-FACT/ADVICE, #941 (merged 2026-11-15): "One coordinator session started 49 subsessions, 33 of them reviews." Fix: default to subagents; a subsession is "reserved for a large, independent stream of work that needs its own branch or pull request, runs for hours, or needs another agent, model or harness"; a fresh subagent that did not write the change counts as an independent reviewer. #1041 (2026-11-18) removed Greptile from the merge gate text.
- Forced to subagents: #829 and #837 ("the server refuses sub-sessions on the requested account", `-32001`), #1022 ("sub-session containers had DNS failures today"), #979 ("host was short of memory"), #887 ("Claude subsessions are disabled").
- Builder split: #1023 "Builder: sub-session run-ar4q624fx7 (Opus)", with two Fable 5.2 advisor reads and two Opus design rounds; round 1 had 1 blocker. #1022: "builders as Opus sub-sessions, one at a time" (rename plan, items #1016, #1024, #1025, #1026 each separate). Rework counts for the split are not stated.
- Conflicts explicit: #991 ("The rebase over #982 conflicted on the installers' repository default", fixed); #1118 ("original branch conflicted with the dashboard rework", ported); #982 rebased onto #977, #981, #980 with no conflicts; #1008 rebased with no conflict. #1026 trial-merged every open PR with `git merge-tree`: none gains a conflict, one needs a new `upgrade.json` hash (#1119). #1026 was regenerated by script rather than rebased on 2026-11-26.
- Stacking cost: in the #1025 queue incident, groups #1028, #1029, #1031 and #1008 failed behind it (5 runs), and #1008's failure (`TestAssetCopiesMatchTheirSources`) stalled #1025 too.
- #1046 and #1054 have identical titles ("save idle VMs before the host runs out of memory"), #1054 was created 33 s after #1046 merged on 11-18 (09:39:46 vs 09:40:19), 965 lines against 1,180; reason not stated.

## Could not establish

- Whether the 80 code PRs without review wording were reviewed (16 sampled, none showed review text in comments).
- Per-finding counts for #909, #843, #838, #1105, #1110 (stated only as "rounds").
- Whether omp's #1122 blockers overlapped Opus's; omp findings on #1124.
- Review of #1049, #1013, #899 (no wording). Full list of escaped defects: the scan keys on PR-number references, so unreferenced fixes are missed.
- PR descriptions are author-written; no independent check of finding counts. Wall-clock cost of reviews and tokens: not in the record.
- Outcomes of the open 11-26 PRs (not merged at snapshot).

## Surprises

- The three flaky-test PRs: every product bug came from author-side forced-interleaving reproduction; the Opus reviewer found 0 blockers in all three, and in #1032 the first fix was wrong until the author's load test showed otherwise.
- #1120: Opus review 4 and omp review 5 ran on the same SHA fa21b36cf; Opus said "no findings", omp said "request changes" with 3 blockers.
- Opus findings concentrate in large or concurrency-heavy PRs (#1091 4 blockers across 6 reviews; #959 2 across 6; #837 about 5 across rounds); small PRs rarely get a blocker.
- A reviewer's own fixes created a blocker (#1120 review 3).
- #1008 merged with a stated self-review only.
- 49 sub-sessions from one coordinator, 33 of them reviews, led to a rule steering reviews to subagents.
