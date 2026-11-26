# Several Claude Code Logins per Member

**Goal:** A member keeps several of their own Anthropic logins for Claude
Code. Cloudfield picks one at launch by the usage it can read, and moves a
running session to the next when the vendor reports the current one has run
out, in the open: every session shows which login it is on and every move is
recorded with the vendor's reason.

**Status:** proposed 2026-11-26. Claude Code only. Decisions marked
**Owner** wait for the owner; the build proceeds on the stated default.

A **login** is one sign-in of an agent CLI: for Claude Code, a claude.ai
subscription sign-in, a Console API key, or a keyless Console sign-in. An
**account** stays what it is today: a member, whose login a session spends
(`runs.account_member_id`). This plan makes a session's account a member
*and* one of that member's logins.

## What exists

Most of the machinery is built and stays:

- Launch-time choice and the move of a running session, both between
  *members'* accounts ([usage fallback](2026-10-11-usage-fallback.md),
  `internal/fallback`, `internal/scheduler/usage_fallback.go`). The move
  replaces the session's container, resumes the agent's own conversation
  (`claude --resume`, ACP `session/resume`) and sends one continuation
  prompt; it is crash-safe through the sidecar's `account_swap` marker.
- The run-out signals, each with a near-miss fixture: a rejected
  `rate_limit_event` plus an error `result`, the ACP `-32603` error with
  `errorKind == "rate_limit"`, and the `StopFailure` hook, all gated on
  Claude Code's exported usage-limit prefixes
  (`internal/agentstatus/agentstatus.go`). A transient 429 ("not your usage
  limit"), an overloaded model, a network error, a refused prompt and a
  signed-out login match none of them and never move a session.
- A session on another member's account mounts that member's login file
  over the launcher's and gets its own copy of `~/.claude.json` with the
  account keys removed, at `CLAUDE_CONFIG_DIR`
  (`internal/scheduler/environment.go` `loginMounts`, `accountConfigMounts`).

What is missing: a member has exactly one login per agent, the files in
their home. A second `claude auth login` overwrites the first.

## What Orca does

Read at `stablyai/orca` commit `055555dfa7d78ce12ac3244f8d95dbba21d8556d`
(MIT, Copyright (c) 2026 Lovecast Inc.).

- Each account is its own Claude Code configuration directory, signed in by
  running `claude auth login` with `CLAUDE_CONFIG_DIR` set to it. Orca never
  copies or refreshes a token. Transcript directories are shared, so a
  conversation resumes under another account.
- It detects a spent account from Claude Code's own stream (the same
  signals as above) and shows usage per account from the usage endpoint.
- **It does not switch automatically.** A person picks the account; Orca
  then restarts the session with `--resume` and never re-sends the prompt.
  Its docs: "Orca does not pool quota across accounts."

Taken from Orca: one isolated store per login, sign-in only through the
CLI's own flow, resume without replay, usage shown per login. The automatic
move is Cloudfield's existing fallback, extended. No Orca code is copied;
`docs/notices.md` gains an acknowledgement of the design source.

## What Anthropic's pages say

Fetched 2026-11-26. Quotes are literal.

- Several accounts: no rule against it. "To stay signed in to multiple
  accounts at once, such as work and personal accounts, give each account
  its own configuration directory."
  (code.claude.com/docs/en/authentication)
- At a subscription limit, Anthropic's own options include "Switch to a
  Claude Console account and purchase API usage credits for intensive
  coding sprints". (support.claude.com/en/articles/11145838)
- "You can be a member of multiple Team or Enterprise organizations at the
  same time." (support.claude.com/en/articles/13566435)
- No page says whether rotating between two personal subscriptions at a
  limit is allowed. The nearest rules are in the Usage Policy
  (anthropic.com/legal/aup, effective 2026-11-12), which forbids to
  "create multiple accounts, to avoid detection, circumvent product
  guardrails", and to "Intentionally bypass or interfere with
  capabilities, restrictions, or guardrails established within our
  products"; and the Claude Code legal page: "Advertised usage limits for
  Pro, Max, and Ultra plans assume ordinary, individual usage".
  (code.claude.com/docs/en/legal-and-compliance)
- "You may not share your Account login information, Anthropic API key, or
  Account credentials with anyone else." (anthropic.com/legal/consumer-terms)
- Sign-in "must complete through Anthropic's own flow"; a person may sign
  in "to the unmodified Claude Code binary with their own Claude
  subscription, including where a platform hosts Claude Code".
  (code.claude.com/docs/en/legal-and-compliance)

So: a subscription plus a Console login, and seats in different
organisations, are clearly allowed to hold; two personal subscriptions
moved between automatically is undecided by the text.

## Decisions

### 1. Logins

- A member's **default login** is what they have today: the files in their
  home. It is never moved, copied or renamed. A member who adds no login
  has no new row, file or code path.
- An **added login** has a name the member chooses (`[a-z0-9][a-z0-9-]*`,
  at most 32 characters, unique per member and agent; `default` is
  reserved), a kind read from its files (`subscription`, `api-key`,
  `console`), and, for display to its owner only, the plan and the
  signed-in email and organisation Claude Code cached.
- Logins are **ordered**. The first is preferred; the default login is
  first until the member reorders. Each login has an **automatic** switch:
  off means Cloudfield uses it only when the member picks it for a launch.
- **Owner:** a second personal subscription (plan `pro` or `max` while
  another of the member's logins is `pro` or `max`) is added with
  automatic **on**, after the CLI and dashboard show what Anthropic's
  pages do and do not say, with the links above. The alternative is
  automatic off for that case until the member turns it on. It is one
  default; the build makes it a single constant.

### 2. Storage

- An added login lives at
  `<data-dir>/logins/<member-id>/<agent>/<login-id>/`, outside every member
  home, mode `0700`, files `0600`, with the ownership a member home's login
  file has. It holds the three places Claude Code keeps a login on Linux:
  `credentials.json` (the `claudeAiOauth` sign-in), `account.json` (the
  account keys of `~/.claude.json`, which include a Console API key as
  `primaryApiKey`), and `anthropic/` (the keyless Console profile Claude
  Code keeps under `ANTHROPIC_CONFIG_DIR`).
- One `agent_logins` table: member, agent, id, name, position, automatic,
  created. Additive migration. Runs and account segments gain an
  `account_login` column, empty for the default login, so every existing
  row already means what it meant.
- A session on an added login gets what a session on a teammate's shared
  account gets today, from the login's directory instead of a teammate's
  home: `credentials.json` mounted over `~/.claude/.credentials.json`, a
  per-session copy of `~/.claude.json` carrying this login's account keys
  at `CLAUDE_CONFIG_DIR`, and `anthropic/` at `ANTHROPIC_CONFIG_DIR`. The
  default login's three stores are covered or masked in that container, so
  the agent sees exactly one login. Nothing else is mounted or copied.
- Two sessions on one login share its file, as two sessions on the default
  login do today (pinned mount, rewrite in place). Two logins never share a
  file, so a refresh of one cannot overwrite another.
- **Signing in** is Claude Code's own flow. `cloudfield agent login claude
  --as <name>` creates the empty login and opens a terminal in the
  member's environment with that login's stores mounted as above, running
  `claude auth login`. The server never sees a password, runs no OAuth of
  its own and never refreshes a token.
- **Removing** a login deletes its directory and row. It is refused while a
  live session is on it, naming the sessions. It does not sign the login
  out at Anthropic; the command says so.

### 3. When Cloudfield switches

**At launch.** The launch asks for a login (`--login <name>`, the launch
dialog) or, by default, the member's first automatic login. The existing
rule then applies with logins as candidates: the requested login is used
unless a successful usage read shows a window at 95% or more. Then, in
order:

1. the member's other automatic logins, in their order: the first whose
   usage reads below 95%, or which is an API-key or Console login
   (Anthropic exposes no usage for those, and the member placed it in the
   order);
2. accounts teammates shared, by most usage left, as today.

A login that is signed out, expired without a refresh token or unreadable
is never chosen. When nothing qualifies the launch stays on the requested
login, as today.

**In a running session.** The signal is the existing one and only that:
the vendor's usage-limit error ending the turn. Cloudfield then does what
it does today for a teammate's account, with the member's logins first in
the candidate order: stop the agent, replace the container with one on the
next login, resume the same conversation and send one continuation prompt
naming both logins.

- The turn has already ended with the vendor's error when the signal
  arrives, so no tool call is in flight. Finished tool calls are in the
  transcript, which lives in the member's home and is the same under every
  login. The prompt is not sent again; the continuation prompt is sent
  once, guarded by the `account_swap` marker across a server restart.
- Files the turn wrote are in the session's worktree, which the new
  container mounts.
- A login the session already used is not tried again in that session.
- A session whose home is not the login owner's own (after a handoff)
  never moves to an added login.

**When all are spent.** The session parks at `needs-attention` (headless:
fails) with the vendor's own message, the logins tried, and the earliest
reset time among them when a usage read gave one.

### 4. What the person sees and controls

- `cloudfield agent login claude --as <name>`, `cloudfield agent logins
  [claude]` (name, kind, plan, email, usage windows, automatic, order,
  sessions on it), `cloudfield agent logins rename|remove|order|auto`.
- Agents page: the Claude Code row lists its logins with usage, **Add
  login** and per-login **Remove** as visible buttons, drag to order, the
  automatic toggle.
- Session header, `cloudfield sessions` and the session snapshot: `On
  <member>'s account, login <name>`, and for an API-key or Console login
  `billed to Console credits`.
- Every move: a `run.account` event with both logins and the vendor's
  message, an item-log notice, a room note, and a new account segment.
  Cost stays with the segment that spent it.
- Off switches: per login (automatic), per launch (`--no-fallback`), and
  the saved launch default, all existing or above.

### 5. Sharing and precedence

- A share reaches the owner's **default login only**, exactly what it
  reaches today. An added login is used only by its owner's own sessions,
  in the owner's own home. No wire field lets a grantee name another
  member's login; the server ignores a login on an account that is not the
  caller's.
- A launch on a teammate's account that is spent moves as today; the
  launcher's own logins come before other teammates' accounts.
- A sub-session inherits its parent's requested login and is chosen for
  like any launch. A swarm worker is authorised against the swarm's
  accountable human, whose logins are the candidates.

### 6. Security

- An API key and a refresh token are bearer secrets. They exist only in
  the login's directory and, for the one login a session runs on, in that
  session's container, as today for the default login.
- The server reads a login only through the allowlisted reader
  (`memberhome.ReadCredential`'s rules: fixed names, opened through a
  root, size limit) to read usage and the login's kind. No RPC returns a
  credential. List responses carry name, kind, plan, email and
  organisation, to the login's owner only; another member sees none of it.
- Logs, events, reports, room notes and error text carry a login's name
  and id, never file content. The quota cache keeps its fingerprint, not
  the token.
- A session on login A cannot read login B: B is outside the home and not
  mounted. A sub-session and a handed-off session get no more than their
  own login. The environment terminal holds the default login only, except
  the sign-in terminal, which holds the one login being signed in.
- The run-out signal comes from inside the container and can be forged, as
  today. Forging it moves the session only to a login its owner marked
  automatic, and each move is recorded.

## Pull requests

| # | Scope | Proves |
| --- | --- | --- |
| 1 | Storage and naming: `agent_logins`, the login directory, list, add, rename, remove, order; usage read per login | Two logins never share a file; one login means no new row or file |
| 2 | Launch-time choice: `account_login` on runs and segments, mounts, candidates | A spent first login launches on the second; a grantee cannot name an owner's login |
| 3 | In-session move between a member's logins, reset time in the parked reason | Forced races of the signal against turn end and server restart: no replayed prompt, one continuation |
| 4 | CLI | Integration test of the commands |
| 5 | Dashboard | Dark-mode design review at desktop, 430 and 360 widths |
| 6 | Docs (`agents.md`, `harnesses.md`, `teams.md`, `security.md`, `privacy.md`, `notices.md`) and an end-to-end test with a stand-in agent that reports "run out" on cue | Add, launch, run out, move, all spent |

1 to 3 are one builder: storage and the switch are coupled.

## Not in this plan

- Other agents. The table and wire fields carry the agent, so Codex can
  follow.
- Sharing an added login with a teammate.
- A Console login that runs out of credit: Claude Code reports it with
  another error, which is shown as it is and moves nothing until its
  signal is read from the vendor's source and fixtured.
- Continuing on the same login after its reset.
