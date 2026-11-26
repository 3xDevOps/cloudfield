# 1a: Anthropic's own writing on multi-agent systems and agent design

Collected 2026-11-26 via WebFetch/WebSearch in this run. Quotes were returned by WebFetch's summarizing model, so treat them as near-verbatim, not byte-exact. Tags: MEASURED (source reports a number and how), ADVICE, OBSERVED-ONCE (one anecdote or run), DESIGN-FACT.

Sources (S1-S9):
- S1 https://www.anthropic.com/engineering/multi-agent-research-system
- S2 https://www.anthropic.com/engineering/building-effective-agents
- S3 https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- S4 https://code.claude.com/docs/en/best-practices (the old URL anthropic.com/engineering/claude-code-best-practices now 308-redirects here; this is the current docs page, not the 2025 post; local copy line numbers cited below)
- S5 https://www.anthropic.com/engineering/harness-design-long-running-apps
- S6 https://www.anthropic.com/engineering/effective-harnesses-for-long-running-agents
- S7 https://www.anthropic.com/engineering/building-c-compiler
- S8 https://claude.com/blog/multi-agent-coordination-patterns (Cara Phillips et al., 2026-04-10)
- S9 https://claude.com/blog/subagents-in-claude-code (2026-04-07)

## 1. Measured gains and costs of parallel subagents

| Claim | Quote / number | Source | Tag |
|---|---|---|---|
| Multi-agent beat single agent on research | Opus 4 lead + Sonnet 4 subagents "outperformed single-agent Claude Opus 4 by 90.2% on our internal research eval." Setting: Anthropic internal research eval, breadth-first research queries, not coding, not BrowseComp. | S1 | MEASURED |
| Token multipliers | "agents typically use about 4x more tokens than chat interactions"; "multi-agent systems use about 15x more tokens than chats" | S1 | MEASURED |
| What explains variance | "three factors explained 95% of the performance variance in the BrowseComp evaluation"; "token usage by itself explains 80% of the variance". Other two factors: tool calls and model choice (no individual shares given). Measured on BrowseComp (web browsing), so the gain from subagents is largely "more tokens spent in parallel". | S1 | MEASURED |
| Parallel tool calling speedup | "cut research time by up to 90% for complex queries"; lead spins up 3-5 subagents in parallel; each subagent uses 3+ tools in parallel. Research-agent queries only. | S1 | MEASURED |
| Economic condition | "For economic viability, multi-agent systems require tasks where the value of the task is high enough" | S1 | ADVICE |
| Tool-description self-repair | A tool-testing agent that rewrites flawed MCP tool descriptions gave "a 40% decrease in task completion time for future agents" (research system, tool descriptions) | S1 | MEASURED |
| Harness cost, one task | Retro game maker (Opus 4.5): solo 20 min / $9; full planner-generator-evaluator harness 6 hr / $200, "over 20x more expensive"; in the solo run the central feature "simply didn't work". One task, one run each. | S5 | OBSERVED-ONCE |
| Updated harness cost | DAW run: 3 hr 50 min, $124.70 total (planner $0.46; build rounds $71.08, $36.89, $5.88; QA rounds $3.24, $3.09, $4.06). QA was about 6% of spend. | S5 | OBSERVED-ONCE |
| 16 parallel agents, C compiler | 16 agents, "nearly 2,000 Claude Code sessions", just under $20,000, 2 billion input / 140 million output tokens over two weeks (Opus 4.6); 100,000-line Rust compiler that builds Linux 6.9; "99% pass rate on most compiler test suites". | S7 | OBSERVED-ONCE |
| Subagent speed (qualitative) | "Three subagents working simultaneously generally finish the task in less time." No number. | S9 | ADVICE |

## 2. When multi-agent does NOT fit

- S1: domains needing shared context or many inter-agent dependencies "are not a good fit for multi-agent systems today"; "most coding tasks involve fewer truly parallelizable tasks than research"; "LLM agents are not yet great at coordinating and delegating to other agents in real time". ADVICE (the authors' judgment, 2025 models).
- S2: "agents are the better option when flexibility and model-driven decision-making are needed at scale"; "Agentic systems often trade latency and cost for better task performance"; "higher costs, and the potential for compounding errors"; "find the simplest solution possible, and only increasing complexity when needed". ADVICE.
- S9: sequential dependent work stays in one session ("subagents can't easily pass state along"); "Two subagents editing the same file in parallel is a recipe for conflict"; small tasks: delegation overhead can outweigh the benefit; rule of thumb "ten or more files, or ... three or more independent pieces of work". ADVICE.
- S7: parallel agents worked for independent failing tests, but on the Linux kernel "Every agent would hit the same bug, fix that bug, and then overwrite each other's changes." Fix: use GCC as an oracle so agents get different files. OBSERVED-ONCE.
- S4: "Handing the same task to a subagent doesn't make the answer any better." A subagent is "the same model (or a smaller one) on the same task. You get parallelism or a fresh pair of eyes out of it, but not a smarter answer." Subagents "can end up *slower* than doing the task where you are" because a fresh one must regather context. S4 also says subagents to protect context is a stale tip because "running out of context is rarely the problem anymore". ADVICE/DESIGN-FACT.
- S8: sequential subagents "incur multi-agent token costs without the speed benefit". ADVICE.

## 3. Writing a subagent brief

- S1: "Each subagent needs an objective, an output format, guidance on the tools and sources to use, and clear task boundaries." "Without detailed task descriptions, agents duplicate work, leave gaps, or fail to find necessary information." ADVICE (from observed failures).
- S1 failure example: a short lead prompt ("research the semiconductor shortage") was misread; one subagent explored the 2021 automotive chip crisis "while 2 others duplicated work investigating current 2025 supply chains". OBSERVED-ONCE.
- S9: "Scope tasks clearly"; say which tasks run in parallel; name the wanted output (summaries, specific findings). ADVICE.
- S4: review brief example `use a subagent to review the rate limiter diff against SPEC.md, check every requirement is implemented and the listed edge cases have tests, report gaps that affect correctness and skip style`; "Don't drop the `skip style` part ... A reviewer that's asked to find problems will always find *something*." ADVICE.
- S4: a reviewer can be given `tools: Read, Grep, Glob` so it cannot edit, "enforced by Claude Code rather than requested in a prompt". DESIGN-FACT.

## 4. Scaling effort to task size

- S1: scaling rules are embedded in the lead's prompt. "Simple fact-finding requires just 1 agent with 3-10 tool calls"; complex research "might use more than 10 subagents with clearly divided responsibilities". ADVICE.
- S1: "Search strategy should mirror expert human research: explore the landscape before drilling into specifics." ADVICE.
- S4: Haiku for simple subagent jobs; `/usage` shows per-subagent-type cost share. ADVICE.
- S5: with Opus 4.6 the sprint construct was removed; evaluator moved to a single end pass; evaluator is "worth the cost when the task sits beyond what the current model does reliably solo" and overhead otherwise. "Every component in a harness encodes an assumption about what the model can't do on its own." ADVICE with one-run evidence.

## 5. Results as files/artifacts vs relayed through the lead

- S1: "Subagents call tools to store their work in external systems, then pass lightweight references back to the coordinator." Benefits stated: less information loss, lower token overhead. DESIGN-FACT/ADVICE.
- S3: subagents may explore with "tens of thousands of tokens or more" yet "return only a condensed, distilled summary of its work (often 1,000-2,000 tokens)"; "the detailed search context remains isolated within sub-agents". DESIGN-FACT.
- S5: agents communicated through files: "one agent would write a file, another agent would read it". Sprint contracts agreed before code (sprint 3 had 27 criteria). OBSERVED-ONCE.
- S6: state carried by `claude-progress.txt` plus git history plus a JSON feature list edited only via a `passes` field; "the model is less likely to inappropriately change or overwrite JSON files compared to Markdown files." ADVICE.
- S7: lock files in `current_tasks/` and git sync for claiming tasks; "I don't use an orchestration agent."; "print a few lines of output and log all important information to a file." OBSERVED-ONCE.
- S8: orchestrator systems can lose findings across handoffs; shared state lets agents "see what others have discovered". ADVICE.

## 6. Synchronous vs asynchronous orchestration

- S1: synchronous: "the entire system can be blocked while waiting for a single subagent to finish searching"; asynchronous "would enable additional parallelism" but adds "result coordination, state consistency, and error propagation". The research system itself was synchronous. DESIGN-FACT/ADVICE.
- S8: agent teams: "one teammate finishes in two minutes and another takes twenty" (no numbers beyond that illustration).
- S4: subagents the main session starts "run in the background by default, so your main conversation keeps moving"; background work (loops, subagents) "keep sending requests while you aren't typing"; subagent prompt cache lasts 5 minutes unless changed. DESIGN-FACT of Claude Code, as of the fetched page.
- S1: rainbow deployments "to avoid disrupting running agents"; resume "from where the agent was when the errors occurred". DESIGN-FACT.

## 7. Evaluation and review practice

- S1: "We started with a set of about 20 queries representing real usage patterns."; one LLM call with one prompt scoring 0.0-1.0 was most consistent with humans; "Even in a world of automated evaluations, manual testing remains essential."; judge end state: "evaluate whether it achieved the correct final state." ADVICE.
- S2: coding is a good agent fit because "Code solutions are verifiable through automated tests"; "human review remains crucial". Evaluator-optimizer fits "when we have clear evaluation criteria". ADVICE.
- S7: "it's important that the task verifier is nearly perfect, otherwise Claude will solve the wrong problem"; added CI because "new features and bugfixes frequently broke existing functionality"; test runner defaulted to a 1% or 10% random sample. OBSERVED-ONCE.

## 8. Reviewers, verifiers, fresh context (explicit statements)

- S5: "agents reliably skew positive when grading their own work"; "tuning a standalone evaluator to be skeptical turns out to be far more tractable" than making a generator self-critical; still "inclined to be generous towards LLM-generated outputs"; "Out of the box, Claude is a poor QA agent" (it found real issues then talked itself into approving); fixed over "several rounds" of reading its logs against the author's judgment; evaluator drove the app with Playwright MCP. OBSERVED-ONCE (Opus 4.5, game maker / frontend).
- S4: `/code-review` "reviews the diff in a fresh subagent that never saw your conversation. It isn't swayed by the reasoning that produced the code."; "A second session is also the easiest independent reviewer you can get."; "A fresh reviewer does help, because it hasn't seen your reasoning, which is a different thing from delegating."; ask for evidence ("the command it ran and its output, or the screenshot"); `/verify` builds and runs the app and "catches the kind of problem that tests and typechecks miss"; start the build in a fresh session from SPEC.md for "a clean context and a written definition of done". ADVICE.
- S8: generator-verifier "when output quality is critical and evaluation criteria can be made explicit"; "The verifier is only as good as its criteria"; "Teams most often fail by implementing the loop without defining what verification means"; a vague verifier "will rubber-stamp"; cap iterations with an escalation fallback. ADVICE.
- S6: failure modes "declares victory on the entire project too early", "marks features as done prematurely", tries to "one-shot the app"; fix: one feature at a time, browser-automation testing "as a human user would" which "dramatically improved performance". Specialized testing/QA/cleanup agents are floated only as unproven ("still unclear whether a single, general-purpose coding agent performs best across contexts"). ADVICE.
- S1: no general reviewer in the runtime; only a CitationAgent. Fresh context: "agents can spawn fresh subagents with clean contexts while maintaining continuity through careful handoffs."
- S9: "For unbiased review, ask for a subagent that doesn't see the earlier discussion." Subagents are "worth that cost when context isolation, parallelism, or a fresh perspective actually helps."

## 9. Other design statements

- S2 orchestrator-workers: "a central LLM dynamically breaks down tasks, delegates them to worker LLMs, and synthesizes their results"; parallelization is sectioning (independent subtasks) or voting (same task repeatedly). DESIGN-FACT.
- S8: "For most use cases, we recommend starting with orchestrator-subagent." Agent teams fit "subtasks are independent and benefit from sustained, multi-step work". No benchmarks in that post. ADVICE.
- S5: compaction "doesn't give the agent a clean slate"; context resets with structured handoff were essential on Sonnet 4.5 ("context anxiety") but "Opus 4.5 largely removed that behavior"; resets add "orchestration complexity, token overhead, and latency". OBSERVED-ONCE.
- S3: compaction for back-and-forth, note-taking for "iterative development with clear milestones", multi-agent for "complex research and analysis where parallel exploration pays dividends". ADVICE.
- S4: worktrees give "each its own checkout so the edits can't collide"; `/batch` splits mechanical work into "5 to 30 units", each in its own worktree opening a PR. DESIGN-FACT.

## Could not establish

- Any Anthropic measurement of multi-agent gains on coding tasks other than the C compiler anecdote (no baseline there). S1 gives only the qualitative "fewer truly parallelizable tasks".
- Individual variance shares of tool calls and model choice in S1 (tool did not return them).
- Whether a cross-model reviewer (reviewer on a different vendor's model) is endorsed: no Anthropic page fetched discusses reviewers on other model families; S4 says only that default subagents use the same or smaller model. Not searched beyond these pages.
- The S1 quotes were not cross-checked against a second fetch of the full page text (WebFetch summarizes). Anthropic's "Managed Agents" and Claude Code agent-teams docs were found in search but not read.
- Exact publication dates for S1-S3, S5-S7 were not captured.

## Surprises

- S4 now says the "subagents protect your context" tip is stale and that delegation "doesn't make the answer any better"; the value is parallelism, noisy-output isolation and an independent reviewer.
- S1: token spend alone explains 80% of BrowseComp variance, so much of the multi-agent gain may be more compute rather than structure.
- S5: QA was only about 6% of spend in the DAW run but the author still had to tune it for several rounds, and a reviewer told to find problems "will always find *something*" (S4), so reviewer briefs need scope limits.
- S7: Anthropic's largest parallel-agent run used no orchestrator at all, only git locks and a near-perfect test verifier.
- S5: with a newer model the author removed harness parts, so numbers from one model generation may not transfer.
