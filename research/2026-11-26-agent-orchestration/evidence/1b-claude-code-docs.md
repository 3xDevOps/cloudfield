# 1b: Claude Code official docs (evidence)

Collected 2026-11-26 with WebFetch from code.claude.com/docs/en/*.md (index: https://code.claude.com/docs/llms.txt). Pages were fetched in this run. Method caveat: WebFetch passes each page through a small summarizer. Pages that came back as raw page text (workflows, agent-teams, costs, agents, worktrees, code-review-local, ultrareview, agent-sdk/subagents, advisor, common-workflows, prompt-caching) are verbatim. Quotes from sub-agents, model-config and best-practices are the summarizer's extracts of the page; the wording was quoted, but check the page before reusing them as citations. best-practices.md was read through a saved copy of the fetch output.

Tags: MEASURED = source reports a measurement; ADVICE = recommendation; DESIGN-FACT = documented behavior or limit; OBSERVED-ONCE = single anecdote.

## 1. What a subagent shares with its parent

| Claim | Exact quote | Source | Tag |
|---|---|---|---|
| Context is fresh, not inherited | "Each subagent starts with a fresh, isolated context window. It doesn't see your conversation history, the skills you've already invoked, or the files Claude has already read." Exception: a fork "inherits the parent conversation instead of starting fresh." | /sub-agents.md | DESIGN-FACT |
| Only channel parent to child is the prompt string | "The only content you pass from parent to subagent is the Agent tool's prompt string, so include any file paths, error messages, or decisions the subagent needs directly in that prompt." | /agent-sdk/subagents.md | DESIGN-FACT |
| cwd | "A subagent starts in the main conversation's current working directory. Within a subagent, `cd` commands don't persist between Bash or PowerShell tool calls and don't affect the main conversation's working directory." | /sub-agents.md | DESIGN-FACT |
| Tools and permissions | "Subagents inherit the built-in tools and MCP tools available in the main conversation, narrowed by two filters." Unset permission mode: "the subagent inherits the main conversation's permission mode." Background subagents "surface every permission prompt in your main session." | /sub-agents.md | DESIGN-FACT |
| Model | "By default, a subagent you define runs on the same model as the conversation that starts it." | /sub-agents.md | DESIGN-FACT |
| Project instructions / not inherited | Receives project CLAUDE.md (`omitClaudeMd` can drop it). Does not receive "The parent's conversation history or tool results" or "The parent's system prompt". Subagents get a system prompt "not the Claude Code system prompt"; auto memory is not loaded. | /agent-sdk/subagents.md, /sub-agents.md | DESIGN-FACT |
| Thinking | "Subagents inherit your main conversation's extended thinking configuration. There is no per-subagent thinking setting." | /sub-agents.md | DESIGN-FACT |
| What it returns | "Intermediate tool calls and results stay inside the subagent; only its final message returns to the parent." Parent "may summarize it in its own response." Docs say to instruct the parent to keep it verbatim. | /agent-sdk/subagents.md | DESIGN-FACT |
| Output scan | "Claude Code scans each subagent's final report before Claude reads it"; "it never removes or rewords the subagent's text." | /sub-agents.md, /agent-sdk/subagents.md | DESIGN-FACT |
| Early death | "A subagent that produced nothing, or whose only output was tool calls, fails with `Agent terminated early due to an API error`." | /sub-agents.md | DESIGN-FACT |

## 2. Nesting, concurrency, background

- Nesting is on by default: "By default, a subagent can spawn subagents of its own, up to three layers below the main conversation." Turn off with `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH=1`. At the limit "Claude Code withholds the `Agent` tool from every subagent except a fork." A fork "can't spawn further forks." (/sub-agents.md; DESIGN-FACT)
- Concurrency: "when 20 subagents are running in a session, spawning another with the Agent tool fails with `Concurrent subagent limit reached`, and the error tells Claude not to retry." Override `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS`. "There's no limit on the total number of subagents Claude can spawn over a session." Ultracode sessions are exempt. (/sub-agents.md; DESIGN-FACT)
- SDK caps: depth default `3`, concurrency `20`, plus `maxBudgetUsd` which "refuses to spawn more subagents, returning `Budget limit reached`, stops background subagents that are still running". (/agent-sdk/subagents.md; DESIGN-FACT)
- Agent teams: "No nested teams: teammates cannot spawn their own teammates." (/agent-teams.md; DESIGN-FACT)
- Background default: "a subagent other than `web-fetch` that Claude spawns from your main conversation starts in the background"; results arrive "as a completion notification in a later turn." Background subagents run "with a smaller built-in tool set" than foreground ones. (/sub-agents.md; DESIGN-FACT)
- Workflows: "Up to 16 concurrent agents by default, fewer when Claude Code has fewer CPUs available, including inside a CPU-limited container" (env `CLAUDE_CODE_WORKFLOW_MAX_CONCURRENT_AGENTS`, 1 to 256); "Up to 4,096 items in a single `parallel()` or `pipeline()` call"; "1,000 agents total per run". (/workflows.md; DESIGN-FACT)
- `/batch`: "has Claude split one large change into 5 to 30 worktree-isolated subagents." (/agents.md; DESIGN-FACT)

## 3. Worktree isolation

- Subagent frontmatter `isolation: worktree`; "By default, the copy branches from your default branch rather than the parent session's `HEAD`." `worktree.baseRef: "head"` branches from current HEAD, and "Use this when isolating subagents that need to operate on in-progress work." (/sub-agents.md, /worktrees.md; DESIGN-FACT)
- Kept or removed: "Claude Code keeps the worktree if git shows changed files, untracked files, or new commits there." Sweep removes agent worktrees older than `cleanupPeriodDays`. (/worktrees.md; DESIGN-FACT)
- Worktree subagents load instruction files from the main conversation: "the subagent also doesn't load the `CLAUDE.md` file or `.claude/rules/` directory at the worktree's root as it reads files there, even if those differ on the worktree's branch." (/worktrees.md; DESIGN-FACT)
- A worktree is a fresh checkout: gitignored files and installs are absent; `.worktreeinclude` copies gitignored files. (/worktrees.md; DESIGN-FACT)
- Isolation limit: the checks "read the path an edit targets, the directory a command runs in, and the text of the command... a command that writes into the main checkout without running git there, such as `cp` or a shell redirect, isn't refused by them." (/worktrees.md; DESIGN-FACT)
- Agent teams do not isolate: "Agent teams don't isolate teammates in worktrees, so partition the work so each teammate owns a different set of files." "Two teammates editing the same file leads to overwrites." (/agents.md, /agent-teams.md; DESIGN-FACT)
- Permission approvals are shared across worktrees: choosing "Yes, and don't ask again" in a worktree session saves the rule to the main checkout's `.claude/settings.local.json`. (/worktrees.md; DESIGN-FACT)

## 4. Model and effort per subagent

- Model resolution (first match wins): per-invocation `model` parameter; definition `model` field (incl. `inherit`); `CLAUDE_CODE_SUBAGENT_MODEL`; the main conversation's model. `CLAUDE_CODE_SUBAGENT_MODEL_FORCE=1` makes it ignore the definition field. (/sub-agents.md; DESIGN-FACT)
- Workflows use the same order: "A model the script names for a stage counts as the per-invocation model in that order." (/workflows.md; DESIGN-FACT)
- Effort per subagent: "set the `effort` field in its definition"; it "Overrides the session effort level, but not the `CLAUDE_CODE_EFFORT_LEVEL` environment variable." Fallbacks: `CLAUDE_CODE_EFFORT_LEVEL`, then spawning subagent's effort, then `CLAUDE_CODE_SUBAGENT_EFFORT`. "On another model, that level doesn't apply." (/sub-agents.md, /model-config.md; DESIGN-FACT)
- SDK `AgentDefinition.effort`: `'low' | 'medium' | 'high' | 'xhigh' | 'max' | number`; `maxTurns` returns partial output marked as such. (/agent-sdk/subagents.md; DESIGN-FACT)
- Teammates: model picked from spawn prompt, then definition, then env, then lead's model. "A teammate's model and fast mode are fixed when it spawns." Per-teammate permission modes cannot be set at spawn. (/agent-teams.md; DESIGN-FACT)
- Effort levels: Opus 5.5, Sonnet 5.5, Haiku 5.5 have low/medium/high/xhigh/max and default to `medium`. `max`: "may show diminishing returns and is prone to overthinking, so test before adopting it broadly". (/model-config.md; DESIGN-FACT/ADVICE)
- Effort comparison: "In Anthropic's testing, Opus 5.5 at `medium` matches or exceeds Opus 5 at `high` on coding and knowledge-work evaluations." No task set, metric or number given on the page. (/model-config.md; MEASURED, unspecified)
- Ultracode: `/effort ultracode` or the word `ultracode` in a prompt makes Claude write a workflow; "each request uses more tokens and takes longer than the same request without a workflow." Setting it via `--effort ultracode` also sets `xhigh`. (/workflows.md; DESIGN-FACT)
- Cache: changing effort mid-session invalidates the cache on most models but not on Opus 5.5, Sonnet 5.5, Haiku 5.5, Fable 5.1+ with API key or subscription. Each model has its own cache. (/prompt-caching.md; DESIGN-FACT)

## 5. When to use what (docs' own guidance)

- Subagents vs main: use subagents when "The task produces verbose output you don't need in your main context", needs tool restrictions, or "is self-contained and can return a summary". Stay in main when it "needs frequent back-and-forth", "Multiple phases share significant context", it's a "quick, targeted change" or "Latency matters". (/sub-agents.md; ADVICE)
- Best-practices "Know what subagents cost": "Sometimes it's time as well: a fresh subagent first has to gather context your conversation already had, so it can end up *slower* than doing the task where you are." "And by default it's the same model (or a smaller one) on the same task. You get parallelism or a fresh pair of eyes out of it, but not a smarter answer." (/best-practices.md; ADVICE)
- "What you can stop doing" table: "Delegate to a subagent for a better answer: Handing the same task to a subagent doesn't make the answer any better. A fresh reviewer does help, because it hasn't seen your reasoning." Also "Running out of context is rarely the problem anymore" (most current models have a 1M window). (/best-practices.md; ADVICE)
- Four-way compare (/workflows.md table): subagents "A few delegated tasks per turn"; agent teams "A handful of long-running peers"; workflows "Dozens to hundreds of agents per run"; "Who decides what runs next": Claude turn by turn vs "The script". "Moving the plan into code also lets a workflow apply a repeatable quality pattern... independent agents adversarially review each other's findings before they're reported". (DESIGN-FACT/ADVICE)
- Agent teams vs subagents: "Use subagents when you need quick, focused workers that report back. Use agent teams when teammates need to share findings, challenge each other, and coordinate on their own." "For sequential tasks, same-file edits, or work with many dependencies, a single session or subagents are more effective." (/agent-teams.md; ADVICE)
- Separate sessions: "For work that needs to keep running in parallel or won't fit in one context window, run it in separate sessions". Cross-session messaging lets sessions "pass findings and status between themselves"; teammates "message each other directly". (/sub-agents.md, /agents.md; ADVICE)
- Team size: "Start with 3-5 teammates for most workflows"; "Having 5-6 tasks per teammate keeps everyone productive"; "Three focused teammates often outperform five scattered ones." Start team use with "research, review", not parallel implementation. (/agent-teams.md; ADVICE, no measurement given)
- Competing-hypothesis debugging: "Sequential investigation suffers from anchoring"; independent adversarial investigators make the survivor "much more likely to be the actual root cause." (/agent-teams.md; ADVICE, no measurement)

## 6. Cost multipliers

| Claim | Quote | Source | Tag |
|---|---|---|---|
| Agent teams ~7x | "Agent teams use approximately 7x more tokens than standard sessions when teammates run in plan mode, because each teammate maintains its own context window and runs as a separate Claude instance." No task, model or method given. | /costs.md | MEASURED? (no basis stated; do not transfer) |
| Teams scale linear | "token usage is roughly proportional to team size"; "Use Sonnet for teammates." | /costs.md | ADVICE |
| Subagent cost | "every subagent sends its own requests and they count against the same usage as your main conversation." No multiplier given. | /best-practices.md, /costs.md | DESIGN-FACT |
| Workflows | "a single run can use meaningfully more tokens than working through the same task in conversation." `Large workflow` warning at "more than 25 agents, or its projected token total passes 1.5 million" (advisory only). Default size guideline `medium` (<10 agents), `small` on Pro. | /workflows.md | DESIGN-FACT |
| Baseline spend | "average cost is around 13 US dollars per developer per active day and 150-250 US dollars per developer per month, with costs remaining below 30 US dollars [dollar signs rewritten for the repository audit] per active day for 90% of users." Enterprise deployments; not about agents. | /costs.md | MEASURED |
| Cache for subagents | Subagent first request "doesn't read the parent's cache"; subagents get a 5-minute TTL "even on a subscription". A fork "inherits the parent's system prompt, tools, and conversation history exactly, so its first request reads the parent's cache" and "costs less than a fresh subagent for tasks that need the same context." Workflow fan-outs hold all but the first matching agent up to 5 s (`CLAUDE_CODE_WORKFLOW_PREFIX_STAGGER_MS`) to share cache. | /prompt-caching.md, /sub-agents.md, /workflows.md | DESIGN-FACT |
| Cost levers | `model: haiku` for simple subagents; "Ask for short summaries back"; `/usage` shows the subagent share; "Shut down teammates when their work is done." | /best-practices.md, /costs.md | ADVICE |
| Opus 5 delegates more | "Claude Opus 5 delegates to subagents more readily than earlier models"; with the `claude_code` preset on Opus 5 the system prompt tells Claude not to call Agent unless asked. | /agent-sdk/subagents.md | DESIGN-FACT |

## 7. Review: fresh context, other models

- "A second session is also the easiest independent reviewer you can get... another reviews `@src/middleware/rateLimiter.ts` without having watched it being written." (/best-practices.md; ADVICE)
- `/code-review`: "reviews the diff in a fresh subagent that never saw your conversation. It isn't swayed by the reasoning that produced the code." Runs as a background subagent with own context; effort arg: "At `low`, the review reports the findings it's most confident in... From `medium` through `max`, the review broadens coverage." Default findings limit 32. (/best-practices.md, /code-review-local.md; DESIGN-FACT/ADVICE)
- Review prompt warning: "A reviewer that's asked to find problems will always find *something*. If you fix every finding, you end up with extra abstraction layers and tests for cases that can't happen." Keep `skip style`. (/best-practices.md; ADVICE)
- Ultrareview (research preview): "every reported finding is independently reproduced and verified"; "roughly 5 to 15 minutes"; "typically 5 to 25 US dollars in usage credits [dollar signs rewritten for the repository audit]"; 3 free runs on Pro/Max/Ultra; diff limit "500 changed files and 8,000 changed lines by default". Cloud only, claude.ai auth; not on Bedrock/Vertex/Foundry. (/ultrareview.md; DESIGN-FACT; cost range is the docs' estimate, no benchmark)
- Advisor (experimental): second, stronger model that "receives the full conversation, including every tool call and result"; "pairing a faster main model with a stronger advisor typically costs less than running the stronger model throughout." Advisor must rank at or above the main model, so it cannot be a weaker or non-Anthropic reviewer. Anthropic API only. (/advisor.md; ADVICE/DESIGN-FACT)
- Other models: "In every approach the workers are Claude sessions. To involve a different tool, expose it to Claude as an MCP server." (/agents.md; DESIGN-FACT)

## 8. Workflows: failure modes and limits

- "No mid-run user input"; scripts have "No direct filesystem or shell access"; agent output with `schema` is validated, "fails after five attempts". (/workflows.md; DESIGN-FACT)
- Stall: an agent with silent output "starts over from the same prompt"; max five restarts; "Files the stalled attempt already changed stay changed". (/workflows.md; DESIGN-FACT)
- Resume: a Failed agent mid-fan-out reruns "work that already finished" for every later agent. Usage-limit pause only in interactive terminal/Code-tab sessions, not in `claude -p`, SDK, background sessions or teammates. (/workflows.md; DESIGN-FACT)
- Verifier agents that cannot check a claim (rate limit, API error) leave it "unverified instead of counting it as refuted". (/workflows.md; DESIGN-FACT)
- Keyword `ultracode` does nothing in `-p` prompts, unstamped SDK prompts, scheduled tasks or relayed webhooks/PR comments. (/workflows.md; DESIGN-FACT)

## 9. Agent teams: failure modes

All from /agent-teams.md (DESIGN-FACT): "experimental and disabled by default" (`CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1`); no in-process resume; "Task status can lag: teammates sometimes fail to mark tasks as completed"; "Teammates may stop after encountering errors instead of recovering"; "The lead can stop early too"; "Sometimes the lead starts implementing tasks itself instead of waiting for teammates"; one team per session; lead fixed; teammate permission prompts bubble to lead; spawning is interactive only (`-p` runs plain subagents). Enabling teams also turns named subagents into teammates.

## Could not establish

- Any measured speed-up or quality gain for subagents, teams or workflows: the docs give none. The only quantified multiplier is the unexplained "7x" in costs.md (no task, model or method). Effort claims ("Opus 5.5 at medium matches or exceeds Opus 5 at high") lack numbers on the page; it links a blog post, not fetched.
- Per-subagent token multiplier: not stated anywhere I read.
- sub-agents.md is about 114,800 characters; the first 100,000 and the tail were both fetched through the summarizer, which can drop detail. The last 34,000 characters of model-config.md (it is about 134,700 long) were not read. The "Effort level of subagents and teammates" section was read.
- Managed Code Review service (code-review.md and code-review-costs.md), agent-view.md, fast-mode.md, and cross-session-messaging.md were listed in the index but not read in this run.
- platform.claude.com agent-sdk mirror: not tried; the SDK subagents page on code.claude.com was reachable and used.
- Claude Code docs are version-dependent (many "requires v2.1.x" notes); I did not check the installed CLI version here.

## Surprises

- Docs themselves say delegating the same task to a subagent does "not" improve the answer; only a fresh-context reviewer helps, and a subagent can be slower than staying in the main session.
- Nesting is on by default (3 layers), concurrency cap is 20, yet a subagent depth/budget cap exists only as env/SDK options.
- Enabling agent teams silently converts named subagents into teammates (no confirmation).
- Subagents get a 5-minute prompt cache TTL even on subscriptions; forks share the parent cache and are the cheaper way to hand over context.
- Worktree subagents branch from the default branch, not the parent's HEAD, unless `worktree.baseRef` is `head`; and they read CLAUDE.md from the main conversation, not their worktree.
- Docs state reviewers asked to find problems "will always find something"; the recommended fix is prompt scope ("skip style"), not more reviewers.
- Reviewer/advisor models are Anthropic-only; other tools can join only as MCP servers.
