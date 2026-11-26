# 1c: Non-Anthropic measurements on multi-agent orchestration, parallel coding agents, cross-model review

Collected 2026-11-26 with WebFetch/WebSearch. Caveat on quotes: WebFetch runs the page through a small model, so quoted strings are what that extraction returned, not a byte-checked copy. Numbers marked "table" were read from a fetched table. Abstract-only pages (no full text read) are flagged. Tags: MEASURED / ADVICE / OBSERVED-ONCE / DESIGN-FACT.

## A. When multi-agent helps or hurts

| Claim | Number / quote | Measured on | Source | Tag |
|---|---|---|---|---|
| Multi-agent vs single agent is task-dependent, range +80.8% to -70.0% | "+80.8% on decomposable financial reasoning to -70.0% on sequential planning" | 260 configs, 6 benchmarks (BrowseComp-Plus, Finance-Agent, PlanCraft, WorkBench, SWE-bench Verified, Terminal-Bench), 5 architectures, 3 model families (GPT-5 family, Gemini 2.0/2.5, Claude Sonnet 3.7/4/4.5) | https://arxiv.org/abs/2512.08296 (Google/MIT et al., "Towards a Science of Scaling Agent Systems") | MEASURED |
| Averaged over all benchmarks, no multi-agent architecture beats single agent by much, and costs far more | Table 5: SAS success 0.466, 1.0x error amp, 7.2 turns; Independent 0.370, 17.2x, 58% overhead; Decentralized 0.477, 7.8x, 263%; Centralized 0.463, 4.4x, 285%; Hybrid 0.452, 5.1x, 515%, 44.3 turns | same | https://arxiv.org/html/2512.08296 (Table 5) | MEASURED |
| Coding benchmark: every multi-agent variant is worse than single agent | SWE-bench Verified: SAS 0.522, Hybrid 0.511, Centralized 0.506, Decentralized 0.494, Independent 0.444 (Independent -14.9%, Centralized -3.1%). Terminal-Bench: SAS 0.344, Centralized -19.2%, Independent +1.7% | same; these are single-task-at-a-time agent harnesses, not parallel independent tickets | same HTML page | MEASURED |
| Strong single agent => coordination stops paying | Gains turn negative when single-agent accuracy exceeds about 45% ("capability ceiling") | cross-validated predictive model, R2 = 0.373; picks best architecture for 87% of held-out configs | same | MEASURED (model fit, not a law) |
| Centralized verification limits error propagation | Trace-level error amplification: Independent 17.2x vs Centralized 4.4x | same | same | MEASURED |
| Thesis | "architecture-task alignment, not number of agents, determines collaborative success" | | same | author conclusion |
| Page read only to char 100000 of 190814; later sections not read | | | | gap |

## B. Why multi-agent systems fail (taxonomy)

| Claim | Number / quote | Measured on | Source | Tag |
|---|---|---|---|---|
| 14 failure modes, 3 categories, dataset of 1,600+ annotated traces, 7 frameworks | abstract: 150 traces to build taxonomy, kappa = 0.88 | MetaGPT, ChatDev, HyperAgent, AppWorld, AG2, Magentic-One, OpenManus; coding, math, general agent tasks; GPT-4, Claude 3, Qwen2.5, CodeLlama era | https://arxiv.org/abs/2503.13657 | MEASURED |
| Category shares (derived by the fetch tool by summing mode percentages, not stated by the paper): system design ~44.2%, inter-agent misalignment ~32.4%, task verification ~23.5% | top modes: step repetition 15.7%, reasoning-action mismatch 13.2%, unaware of termination 12.4%, disobey task spec 11.8%, incorrect verification 9.1% | same | https://arxiv.org/html/2503.13657 (first 100k of 160k chars read) | MEASURED (derived) |
| Prompt/structure fixes helped but did not close the gap | ChatDev: clearer roles +9.4%, CEO final say +9.4%, added task-objective verification +15.6% on ProgramDev; "task completion rates remained low" | ChatDev, GPT-4o | same | MEASURED, one framework |
| Verifiers are often shallow | "many existing verifiers perform only superficial checks, such as confirming code compiles" | same | same | OBSERVED |

## C. Cognition (Devin)

| Claim | Quote | Source | Tag |
|---|---|---|---|
| 2025 post has no measurements | Principles: "Share context, and share full agent traces, not just individual messages"; "Actions carry implicit decisions, and conflicting decisions carry bad results". Fetch tool: no benchmarks, metrics or percentages; illustrative examples only. Dated 2025.06.12 | https://cognition.com/blog/dont-build-multi-agents (cognition.ai 301-redirects here) | ADVICE |
| April 2026 follow-up narrows the position | "multi-agent systems work best today when writes stay single-threaded" | https://cognition.com/blog/multi-agents-working | ADVICE |
| Writer + clean-context reviewer loop | "Devin Review catches an average of 2 bugs per PR"; ~58% severe (logic errors, missing edge cases, security). No sample size or method given | same | OBSERVED-ONCE (vendor figure, unspecified method) |
| Reviewer works better without the writer's context | "we found this technique to work best when the coding and review agents do not share any context beforehand" | same | ADVICE (no figures) |
| Cross-model "smart friend" | Small primary (SWE-1.5, 950 tokens/sec) calling larger Sonnet 4.5: "SWE 1.5 was not good enough at being the primary model for this setup to really work."; "The cost and speed wins were real, but the quality ceiling was set by the primary". Claude+GPT frontier pairings gave "real gains" in tricky cases, no figures | same | OBSERVED-ONCE |
| Manager Devin spawns child Devins via internal MCP | live, no metrics reported | same | DESIGN-FACT |
| Post reports "no sample sizes, error bars, or controlled benchmark comparisons" (fetch tool's summary) | | same | |

## D. Cross-model / ensemble review

| Claim | Number | Measured on | Source | Tag |
|---|---|---|---|---|
| Different model families find different defects | No single model above ~64% recall; 87 of 154 confirmed defects (56.5%) found by exactly one model; median cross-family Jaccard ~0.37; a second provider raised coverage 33.6% -> 57.1%, diminishing after | 8 model versions, 5 providers, 18 code/mixed artifacts from one live team, human-reconciled key; author calls it "directional" | https://zenodo.org/records/20519585 (Zenodo preprint, not peer reviewed, single author) | MEASURED, small, single-org |
| Four specialised agents beat single agents | 76.1% bugs caught; accuracy 32.8% -> 72.4%; per added agent +14.9, +13.5, +11.2 points; agent correlation rho 0.05-0.25 | 99 labelled samples; LLM behind agents not named on page; abstract only | https://arxiv.org/abs/2511.16708 | MEASURED, small; different prompts, not different models |
| Ensembles of diverse models: ceiling vs realised | Ceiling "can be 83% above the best single model"; diversity-based selection reaches up to 95% of that ceiling and works with two-model ensembles; consensus voting falls in a "popularity trap" | 10 LLMs, 5 families, 3 code-generation/repair benchmarks (not review); abstract only | https://arxiv.org/abs/2510.21513 | MEASURED, generation not review |
| Counter-evidence: second model adds noise | Haiku 4.5 beat Sonnet 4.6 on F1 (0.365 vs 0.343) at 3.2x lower cost; best F1 on real PRs 0.066 vs 0.847 on synthetic; F1 0.657 on diffs <10 lines vs 0.043 on diffs >150 lines; near-zero recall on performance bugs | 5 models; 100 synthetic mutations + 50 real bug-fix PRs; abstract/results only. The "union of two models adds false positives" claim came from a search summary and was NOT confirmed on the fetched page | https://arxiv.org/abs/2606.15689 | MEASURED, small real-PR set |
| Self-preference: judges favour own output | GPT-4 73.5% accurate at telling own text from two LLMs and humans; Kendall tau between recognising and preferring own summary 0.41/0.37 (GPT-3.5, no fine-tune, XSUM/CNN); Llama 2 near chance at self-recognition (0.514, 0.505) | summarisation (XSUM, CNN/DM), 1,000 articles each; GPT-3.5, GPT-4, Llama 2; not code | https://arxiv.org/abs/2404.13076 and https://arxiv.org/html/2404.13076 | MEASURED, non-coding |
| Debate between copies rarely beats cheap baselines; heterogeneity helps | 5 MAD methods, 9 benchmarks, 4 models: "often fail to outperform simple single-agent baselines"; model heterogeneity called a "universal antidote" | reasoning benchmarks, not coding | https://arxiv.org/abs/2502.08788 | MEASURED, non-coding; abstract only |
| Teams underperform their best member | "LLM teams consistently fail to match their expert agent's performance, even when explicitly told who the expert is"; losses up to 41.1% on ML benchmarks | self-organising teams; models and benchmarks not on fetched page | https://arxiv.org/abs/2602.01011 (ICML 2026) | MEASURED, abstract only. (A search summary said 37.6%; the abstract says 41.1%; use 41.1%) |

## E. Parallel coding agents and merge cost

| Claim | Number | Measured on | Source | Tag |
|---|---|---|---|---|
| Orchestrated parallel agents in isolated workspaces beat one agent on long tasks | CAID: +25.6% PaperBench, +14.7% Commit0 absolute accuracy; manager makes dependency-aware plan, git worktree + merge, test-based verification | models not named on abstract page | https://arxiv.org/abs/2603.21489 (Geng and Neubig) | MEASURED, abstract only |
| Concurrent agent PRs collide | Textual conflicts 41.7% cross-agent pairs vs 19.8% same-agent pairs (non-overlapping 95% CIs); 84.4% of conflicted files are source; ~42% structural | 747 co-active PR pairs, AIDev-pop (33,596 PRs, 2,807 repos); only textual conflicts; cross-agent subset is 122 repos | https://arxiv.org/abs/2607.04697 | MEASURED, observational, timing-based |

## F. OpenAI Codex docs (no measurements)

- "use parallel agents for read-heavy tasks such as exploration, tests, triage, and summarization"; "subagent workflows consume more tokens than comparable single-agent runs"; "agents editing code at once can create conflicts and increase coordination overhead". Example prompt: "Spawn one subagent for security risks, one for test gaps, and one for maintainability." Source: https://learn.chatgpt.com/docs/agent-configuration/subagents.md (redirect from developers.openai.com/codex/subagents.md). ADVICE. Page has no performance figures; the 8 and 20 seconds are example config values. Codex spawns subagents only on explicit request (search summary of the same docs; not re-verified on the fetched page).

## G. Task sizing (METR)

- 50% time horizon, v1 numbers: "about 50 minutes" for Claude 3.7 Sonnet; doubling roughly every seven months since 2019, "may have accelerated in 2024". Tasks: RE-Bench, HCAST, 66 shorter tasks; software, ML, cybersecurity. https://arxiv.org/abs/2503.14499 (page shows v4 revised 10 Jul 2026). MEASURED. The authors say external validity is uncertain.
- METR page last updated 2026-09-08 and "no longer actively updated": GPT-5 about 2h17m 50% horizon; "Measurements above 16 hrs are unreliable with our current task suite"; horizon "measures task difficulty, not how long an agent runs"; human baselines likely overestimate expert time. https://metr.org/time-horizons/. MEASURED. Newer models' numbers are in a linked YAML not read.
- Task suites are self-contained, auto-scorable tasks; do not transfer to messy repo work.

## Could not establish

- A controlled study of a lead agent plus in-session subagents vs a single agent on real repository tickets (speed and quality). Not found.
- A controlled, peer-reviewed study of cross-model code review on a shared benchmark with real PRs. The best evidence is a single-org Zenodo preprint and small arXiv studies.
- Cognition's method and sample for "2 bugs per PR, 58% severe". The page gives neither. A ZenML summary of it was seen only in search results.
- Full text of 2602.01011, 2511.16708, 2510.21513, 2603.21489, 2502.08788: only abstract pages read. Google paper: first 100k of 190k chars read. MAST: first 100k of 160k.
- OpenAI code-review measurements (e.g. Codex review precision). Not searched beyond the subagents doc.
- Newer METR horizons (YAML not read).

## Surprises

- On SWE-bench Verified in the Google study, every multi-agent architecture was slightly worse than a single agent (SAS 0.522). The large gains were on a decomposable finance task. Centralized beat Independent on error amplification by 4x.
- A smaller Haiku 4.5 out-reviewed Sonnet 4.6 on F1 at 3.2x lower cost, and real-PR review F1 was 0.066, far below synthetic.
- Cognition's own 2026 reversal is qualitative, with no controlled numbers.
- Self-preference evidence is on summarisation, so applying it to code review (same-model reviewer bias) is inference, not measurement.
- Search-engine summaries and fetched pages disagreed on a few numbers (37.6% vs 41.1%; METR 50 min vs 110 min per version). Fetched primary pages were used above.
