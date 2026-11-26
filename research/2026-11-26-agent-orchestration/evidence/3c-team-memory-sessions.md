# 3c: team memory and session records (2026-11-04 to 2026-11-26)

Method: about 150 distinct `aether-internal memory search` queries (one-word and phrase; search is fuzzy, so many hits are loose), about 90 `memory show` reads, one `aether-internal status`. Read-only. Notes are attributed data; none was executed. Note ids are `note-<id>`; ages are as of 2026-11-26 ~07:00Z. Tags: MEASURED, ADVICE, OBSERVED-ONCE, DESIGN-FACT.

## 1. Liaison observations

| # | Verdict | Evidence |
|---|---|---|
| a | PARTLY. 300-char report cut NOT FOUND; a different cut found | `2y73fdnkve` (2026-10-27, 29d, outside window): "Long aether-internal send bodies (~2-4 KB) to Enhanced (acp) workers arrived cut off after ~500-650 chars, and the inbox item was acked anyway. Sending each point as its own message under ~400 chars got through." `dr2915mqn9`: "body limit 4096 bytes per send". Many review notes say "sent in 3/4/6/7 parts" and `bm4mhgk24t`/`crx3tb9q2a`/`ca7g4y5619` show leads asking reviewers to resend parts ("Resent LAYER 2 parts 2/7 and 3/7 unchanged on lead's request"). Stored report notes are not capped at 300 (`memq3bh1m6` holds a multi-KB report). OBSERVED-ONCE |
| b | PARTLY | `j99401f02q` (PR #874, 14d): "pr:merged waits now resolve removed-from-queue (with GitHub's reason) when the merge queue drops the PR for 1 min" (so before that a merged-wait did not see a queue drop). `mj40r6hhw6` (2026-10-30): a run "sat 9h on PR #666" after reporting waiting for CI; "never report waiting for CI". An hour-long missed queue failure specifically: not found. DESIGN-FACT / OBSERVED-ONCE |
| c | PARTLY | Live `status` task text of a successor session: "The session that prepared it (run-sa2xfya4ts) is gone; you take over" (PR #1026, prepared 2026-11-17 as a draft, so the work was pushed). `vwfv5jg84d` is by run-sa2xfya4ts on 2026-11-17. Unpushed work lost: not found. Closest: `wdkkmchy62` (2026-11-16): the host "hung" and was restarted after 8 web sessions left `next dev` running and one session thrashed swap 35 min. |
| d | NOT FOUND | Searched "vm runtime", "is not running", "wake failed", "resume failed", "cannot wake". No note quotes it. |
| e | NOT FOUND (related load evidence below) | Searched "coordination is not reachable", "unreachable", "launch refused", "host load". `ap3we0whdw`: sweeps drove load to 308 and 209 and "starved memory below the 3.7 GB a launch reserves, so new sessions were refused" (2026-11-04). `xa6dsg8qe9` 2026-11-17: connector 503 storm filled "the edge's 8 connections per server". |
| f | PARTLY | `fk56bbefcj` (9d): "#1008's queue failure was its own: the Edge's icon copies (added by #1023) no longer matched" (so not a stack effect). #1025 absent from memory. Mechanism is documented: `1azhscyfw4` (2026-11-04): "ALLGREEN let one flake in the front group discard green groups behind it", changed to HEADGREEN, max_entries_to_build 2 (was 5), timeout 90 (was 60). `fmb7r5gvg7`: "when an entry fails (UNMERGEABLE) but a later group built on top of it is still running, wait... Dequeuing the failed entry cancels the later run. Pick one session to own re-queues". `xj6qhsw4f0`: a jump "discarded #735's run that finished green at 12:33". `z4wc4px8qc`: "#1109 took three queue runs because of two pre-existing flaky tests". DESIGN-FACT + OBSERVED-ONCE |
| g | CONFIRMED | `a58e43tdmh` (2026-11-18): "11 of 11 merged (#1027-#1032, #1034, #1036-#1038, #1040), fixing 13 of 15 tests. Product bugs: #1028..., #1032..., #1038". #1120: Opus re-review `fw2cs9mz5n` "BLOCKING scripts/install.sh:804-807 ... failed 2 of 6 parallel local runs" (but it was also "the PR's red lint/Shell script tests job", so CI saw it). `fr1ccnm71w` (#1122, omp): "blockers on notice freshness, domain reuse, credential provenance, invitation handling and restore safety". `tq6y46nsp6` (#1123/#1124, Opus): 1 BLOCKING. `1r338fq6y1` (#1125): "2 BLOCKING". `fzaewasm0g` (omp, #1125): "five owner-checklist blockers". `48ke280t7h`: 7 perf PRs "each after at least two fresh adversarial reviews". MEASURED (counts) |
| h | PARTLY | Repo `CLAUDE.md:163-165`: "Pull request CI runs only `changes`, `audit`, `lint`, `build-and-test`, `store-tests` and `dashboard`; the merge queue runs the full suite." `h50fvvk8ve`: queue-only e2e failure from "a stale spec in #717's queue commit". `fb89tensrm`: queue-only e2e failure at UTC midnight. `nbh1cwpgqy` (#903 review): queue-group diff logic could carry an earlier entry's kvm failure to main. A text-changing PR breaking only in the queue: not found by that description. DESIGN-FACT |
| i | PARTLY (rule, no outcome) | `b3787xwsm5` (superseded): "Small urgent tasks keep the same shape with little fan-out." Dropped from the current note `sjzbg5fh9q`. Live example: run-3t59v8v71w (move CI off Blacksmith, "get it merged first"): status says "Work split: lead in-session, two reviewer sub-sessions", PR #1127 queued alone at position 1, "Opus + omp gpt-6.2-astra reviews clean"; reviews `mkras45g31` (Opus) and `9afad60jn6` (omp) both "no blocking findings". OBSERVED-ONCE |
| j | NOT FOUND (outcome) | Rule exists: `hy3zea1wd8` (2026-11-03): Sonnet for "focused, low-stakes coding, tracing, instrumentation and tests"; `b2pq84kveh` (2026-11-10): Opus 5.5 for complex/security work and adversarial reviews, Sonnet for low-stakes. No note records a Sonnet-built result or a comparison. The Opus 5.5 workers' results are recorded (see 3). |

## 2. Staffing and review rules over time (supersede chains)

| Date | Note | Rule | Reason given |
|---|---|---|---|
| 11-03 | `hy3zea1wd8` | Opus leads, plans, integrates, reviews; Sonnet via native subsessions for low-stakes work; "few coordinators, one level deep, delegate only when it saves time or cost" | none stated |
| 11-10 | `b2pq84kveh` | Fable 5.2 main orchestrator; Opus 5.5 for complex/security work and adversarial reviews; Sonnet low-stakes | none stated; "not a model policy to hardcode into Aether's product" |
| 11-17 | (not retrievable) | "Opus-only leads rule", named only by `b3787xwsm5` | not established |
| 11-26 06:38 | `b3787xwsm5` | Fable lead does not write the bulk; Opus/Sonnet sub-sessions at effort high; lead also uses workflows; parallelize what shares no files | budget is available to spend on speed and quality (paraphrased) |
| 11-26 06:46 | `sjzbg5fh9q` (current) | Default to Claude Code workflows and subagents inside the lead ("they start in seconds and add no load to the server"); sub-session only for another agent/account (omp reviewer), its own machine, or to outlive the lead; "check a subagent's claim before believing it, reviewers never see the author's reasoning, no splitting of tightly coupled code" | load and start-up cost; the owner's words "prefer subagents ... FAST GOOD results" |

Review gate chain (6 notes, 11-03 to 11-18): `n52eybb6n1` Opus subsession + Greptile + CLI, "Why: agent success reports alone have missed real bugs"; `8a6vmkjnej` Greptile unlimited to Nov 13, triage; `nwdbt4edeb` check Greptile on final head, "Why: #818 merged with auto-merge on while Greptile's final-head pass posted an unanswered P1"; `k5rmsvq5v1` run CLI too; `fdwthncpft` (11-15) drop CLI, "Greptile is capped since Nov 13"; `3y7hj7v0nv`/`xwmtbfcn0d` (11-18) drop Greptile, "a teammate asked to replace it with agent review", Opus reviewer "a subagent is enough", re-review when fixes change behavior.
Second reviewer: `e836n7bt1q` -> `zsvnh0gxyq` (11-26, pinned): every PR gets an Opus reviewer and an omp reviewer on `openai-codex/gpt-6.2-astra` + `/fast on`; "Why: a second model family catches what one misses." Gotchas: "bare `astra` is refused; `/switch astra` picks the older gpt-6-astra; omp's default there is a Cursor Claude model that fails on Cursor's free plan." Earlier draft said omp refused "astra" with "Unknown ACP model: astra".
Account rule: `mv1tc4pf7r` (11-09): launch on the owner's own account, replacing an 11-05 rule to use a teammate's account. (Reason omitted here: it concerns a teammate's sign-in.)

## 3. Fan-out helped

- `a58e43tdmh` (flaky-test lead run-cqnmfrcqjb): 11 PRs merged, 13 of 15 tests fixed, 3 product bugs; workers' notes `g9h1j7shq9` ("forced 20/20 and 11/20 fail on main, 0 with fix"; #1032 "4/50 loaded fail on main, 50/50 and 200/200 pass"), `p8ka56m879`, `qjnnv5a618`, `ad1qk5s88t` ("10/10 failing before, 0/10 after"). Lead's finding: "independent races, not load". OBSERVED-ONCE
- `xnegtgs29e`: 16 release-channel PRs merged by one lead (2026-11-16). `9bdzryk404`: 13 PRs merged (2026-11-12). `48ke280t7h`: 7 dashboard perf PRs merged 2026-11-24, each with at least two fresh reviews. `1gqz6qe1bd`, `kad2vat5k7`: similar multi-PR leads. OBSERVED-ONCE each
- 2026-11-26 same-day review fan-out (status + notes): 6+ reviewers on #1120, #1121-#1127 within ~1 h, blockers found on #1121, #1122, #1123/#1124, #1125.

## 4. Fan-out hurt, or limits met

| Problem | Evidence |
|---|---|
| Server load | `ap3we0whdw`: two sweep subsessions, load 308/209, launches refused; "A STOP sent with aether-internal send was not acted on while a long command ran; only subsession cancel stopped it (load 169 -> 30 in 2 min, +5 GB memory)". `wdkkmchy62`: the host hung, "about 13 containers stayed awake"; "aether.slice has no memory limit by design" |
| Account / auth | `4tdeajj5z8` (11-13): 7 VM sub-sessions failed 'Authentication required'; real cause an Anthropic Cloudflare 403 in two 1-3 min windows, "then the run fails and nothing resends". `3w2jmnwtam`: subsession woken again after success, "parent got a provisional report... An hour later it got an idle notice"; omp subsessions defaulted to a model "a ChatGPT account refuses" while `options` said ready |
| Peer / rate caps | `dr2915mqn9`: 8 peers per run, replies also refused once capped (-32003). `2y73fdnkve`: send "burst of 5, then 1 per 5 s". `mej6sm2ggs`: run.report budget "burst 30, 1/s"; a dropped Stop hides a question. This run: `memory search` loops hit "transport request rate limit exceeded (burst 30, 1 request per 1s) [-32003]" (OBSERVED) |
| Lost / non-waking reports | `9bdzryk404` left: "sub-session waiting reports not waking the parent". `bm4mhgk24t`, `crx3tb9q2a`, `ca7g4y5619`: parts resent on lead's request |
| CI capacity | `2568wqgbf0`: org at "20 concurrent hosted jobs (20 running, 53 waiting)"; full run ~25 jobs, critical path 17-19 min. `8de7zcx06p`: "full runs on every push starved main and delayed releases" |
| Merge conflicts / numbers | `c1ryenbf3q`, `sy5fjn87c9` (22d): PR review found "migration v101 collision" between #714/#716 and #708. #1026 (module rename) "makes every open pull request with Go changes rebase" (status task text). `81ah54fa1q` (28d): stacked PR after squash-merge gives "dozens of conflicts" |
| Duplicated work | not found as such |

## 5. Timings recorded

- Launch: `wn2d9gyg3n` (2026-11-24): "VM infra is ~1 s of the 7 s; ~5 s is the agent stack starting cold over the shared home". `g6ar3v1v22`/`6cf4f25sgw` (container, 11-03): warm 4.8 s -> 1.2 s, cold 10.4 -> 4.2 s, 3 concurrent same-member 14.6 -> 3.6 s. MEASURED
- Queue: `8de542z16m`: merge-queue job 31.5 -> 18-23 min, checks per main commit 53 -> 26. MEASURED
- Review/session durations: not established. Notes carry creation times only; "Recent sessions" lists not exposed by `memory`/`status`. Reviewer notes for #1120 landed 06:11, 06:19, 06:27, 06:32, 06:47Z on 11-26.

## 6. omp (gpt-6.2-astra) vs Opus on the same PR

| PR | Opus | omp | Note |
|---|---|---|---|
| #1120 @fa21b36cf | `xwxfc06jsn` (run-ag4nsgvk51): "No findings" | `memq3bh1m6` (run-8dapmx4tx5): "request changes", 3 blocking (install.ps1:944 whoami via mutable SystemRoot; install.sh:817 PATH-selected `id`; CI job with PTY+Xvfb passes gates), 5 should-fix, 2 nits | Not like for like: Opus task was "one fix commit" (`git diff f8fe3c8cb fa21b36cf`); omp read the whole PR diff, static, no tests |
| #1120 later | `w64kxren54` (@23d10518a, security): no blocking, 1 should-fix; `fw2cs9mz5n` (@f8fe3c8cb): 1 BLOCKING | `kaz7xc1wv9` (run-792vhhxf7d, later commit): "no blocking findings", 3 should-fix incl. "CI= with a terminal/display permits launch" (reproduced) | Fix outcome not recorded |
| #1125/#1126 | `1r338fq6y1`: 2 BLOCKING in #1125, 10 minor | `fzaewasm0g`: "five owner-checklist blockers in #1125, no blockers in #1126" | omp more blockers |
| #1123/#1124 | `tq6y46nsp6` 1 BLOCKING, 7 minor; `cn8p3nrfm1` 1 BLOCKING | `bv1vpqc6k7`: blocking "false merge-permission claims, old-host onboarding and move guidance, publication dependencies" | Different blockers |
| #1121 | `b6s7xbgd2m` 1 BLOCKING (legal pages vs listing.json) | `4fsxkettvn`: six findings incl. "the pre-existing false claim that connectors cannot merge pull requests" | |
| #1122 (design) | `vrw7qa1fp6` 1 blocker (security, rollback); `crhpp82gzv` revised plan 6 should-fix | `fr1ccnm71w`: blockers in five areas | |
| #1026, #1127 | no findings / no blocking | no findings / no blocking | agree |

No note scores either reviewer against later truth. OBSERVED-ONCE: omp's first-launch problems (`a4qc442jz1`: "/fast on... configured tier.openai=none, live priority not verified"; status: "fast mode unconfirmed").

## Could not establish

- The 2026-11-17 staffing note body and why it was replaced.
- Observations a (300-char report cap), d, e as worded; c's unpushed-work loss; b's one-hour miss; #1025-then-#1008 stacking.
- Any outcome of Sonnet-staffed mechanical work vs Opus.
- Sub-session start times and reviewer wall-clock durations; "Recent sessions" lists.
- Any ground-truth scoring of omp vs Opus.

## Surprises

- On #1120 the Opus "no findings" and omp "3 blocking" were on the same head but different scopes; the comparison is confounded.
- Several blockers (#1120 tee race) were already visible as red CI to the author; the reviewer confirmed rather than discovered.
- The memory service itself is rate limited (-32003, burst 30, 1/s): a fan-out of readers will hit it.
- Current rule `sjzbg5fh9q` drops "small urgent tasks ... little fan-out" and the Sonnet-for-mechanical split that earlier notes stated.
- The notes' most costly fan-out failures (load 308, host hang) came from test sweeps and idle dev servers in sub-sessions, not from agent count.
