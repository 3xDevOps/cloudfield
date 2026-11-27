# Several Claude Code Logins per Member

**Goal:** A member keeps several of their own Anthropic logins for Claude
Code. Cloudfield picks one at launch by the usage it can read, and moves a
running session to the next when the vendor reports the current one has run
out, in the open: every session shows which login it is on and every move is
recorded with the vendor's reason.

**Status:** proposed 2026-11-26. Claude Code only. The owner decided the
defaults on 2026-11-26: "allow ppl to bring multiple personal
subscriptions and sharing shares them all and for ppl their auto fallback
is to their subscriptons and then teammates. maybe we allow for a order
ofr priority thingy or smth as well". He was told Anthropic's pages do
not settle the case of two personal subscriptions and took that decision
as the owner.
The [Review](#review) section lists what review changed.

A **login** is one sign-in of an agent CLI. Claude Code keeps one in any of
three places on Linux: a claude.ai subscription sign-in
(`~/.claude/.credentials.json`), a Console API key (`primaryApiKey` in
`~/.claude.json`), and a keyless Console sign-in (an Anthropic profile under
`ANTHROPIC_CONFIG_DIR`, by default `~/.config/anthropic`). An
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
  (`internal/agentstatus/agentstatus.go`). The list is the vendor's: it
  holds the subscription limits and also "You're out of usage credits",
  the organisation, seat and group messages, each meaning this login
  cannot pay for the turn. A transient 429 ("not your usage limit"), an
  overloaded model, a network error, a refused prompt and a signed-out
  login match none of them and never move a session.
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
moved between automatically is undecided by the text, and sharing a login
with another person is against the consumer terms' sentence above, as the
existing account sharing already is. `docs/agents.md` carries one
sentence saying what the pages do and do not say, with the links, and
calls it neither allowed nor forbidden.

## Decisions

### 1. Logins

- A member's **default login** is what they have today: the files in their
  home, of any of the three kinds. It is never moved, copied or renamed. A
  member who adds no login has no new row, file or code path, and their
  launches and moves choose exactly as today, by today's code paths.
- An **added login** is a subscription sign-in or a keyless Console
  sign-in. A Console API key is not accepted as an added login: Claude Code
  keeps it in `~/.claude.json`, and a copy of that file reaches the
  member's home (its `backups/`), where it would outlive the login. A
  member who pays by API key keeps it as their default login or as a
  workspace secret, as today.
- An added login has an id (`lgn-…`, what rows, markers and the wire
  carry), a name the member chooses (`[a-z0-9][a-z0-9-]*`, at most 32
  characters, unique per member and agent; `default` is reserved), a kind
  read from its files, and the plan, email and organisation Claude Code
  reported at sign-in. Its owner sees all of it. A login's name is shown
  wherever the account it belongs to is shown today, because which login
  pays must be in the open; the email and organisation are shown to its
  owner only.
- A sign-in that reports the email and organisation of a login the member
  already has is refused, naming that login.
- Logins are **ordered**, the default login included: adding the first
  login also writes the default login's row (id `default`, no directory),
  so the order and the switches have one home. The order is the member's
  priority: the first login is preferred, for their own sessions and for
  teammates they share with. A new subscription login goes last among the
  subscription logins; a login billed per use (a Console login) goes
  last of all. The member reorders freely.
- Each login has two switches, both **on** when it is added:
  - **Automatic**: off means Cloudfield uses the login only when its
    owner picks it for a launch, and never chooses it or moves a session
    onto it by itself.
  - **Use for shares**: off keeps the login out of every share; only its
    owner's sessions run on it.
- Automatic use is on for every combination of a member's own logins,
  two personal subscriptions included (the owner's decision above).

### 2. Storage

- An added login lives at
  `<data-dir>/logins/<member-id>/<agent>/<login-id>/`, outside every member
  home, mode `0700`, files `0600`, owned and reserved the way a member
  home is. It holds `credentials.json` (the `claudeAiOauth` sign-in),
  `anthropic/` (the keyless Console profile) and `identity.json` (plan,
  email, organisation; no secret).
- One `agent_logins` table: member, agent, id, name, position, automatic,
  use for shares, created. Additive migration. Runs and account segments gain an
  `account_login` column holding the login id, empty for the default
  login, so every existing row already means what it meant.
- **One type names an account everywhere it is a key:** member plus login
  id. The usage cache, `fallback.Measured`, the remembered login refusals
  and verified logins, the `account_swap` marker, recovery's comparison of
  a run's account with the marker, the "is this a move" test, the revoke
  sweep, and the "account changed" checks of the run repository and of
  terminals in a session all key on it. Today they key on the member and could not tell two logins of one
  member apart.
- A session on an added login gets, from the login's directory:
  `credentials.json` mounted over `~/.claude/.credentials.json`;
  `anthropic/` mounted at `ANTHROPIC_CONFIG_DIR`; and the per-session copy
  of `~/.claude.json` a shared-account session gets today, with the
  account keys removed and no key added, at `CLAUDE_CONFIG_DIR`, with the
  legacy `~/.claude/.config.json` masked. The container's agent then finds
  one login. The inode of each mounted login file is checked before the
  agent starts, as for a shared login today, so a dropped mount fails the
  launch instead of running on the default login under another name. The
  check today covers a login mounted as a subpath of a volume; every
  mount an added login adds is made that way or checked likewise. A
  runtime that cannot make or verify these mounts (the plan's builder
  checks the microVM guest) refuses a launch on an added login with the
  reason.
- **Which store Claude Code prefers when it finds more than one is not
  known from its documentation.** Before pull request 2 merges, a test
  runs the real, unmodified Claude Code against a local stand-in for
  Anthropic's API, with made-up values and no real login, in each layout
  a session can have (subscription login added over each kind of default;
  Console login added over each kind of default) and shows the request
  carries the added login's credential. A layout that cannot be shown is
  refused at launch, and if no Console layout can be, Console logins
  leave this plan.
- A keyless Console login is a whole directory, so an Isolated session
  refuses it with the reason, as it refuses a teammate's whole-directory
  login today.
- When another credential wins over the login file (`ANTHROPIC_API_KEY`
  from a workspace secret or the server, or whatever else
  `internal/scheduler/agent_login.go` already treats as signing the agent
  in), Claude Code uses it whatever the login. Such a launch chooses and
  moves between no logins, and the session header says that credential
  pays.
- Two sessions on one login share its file, as two sessions on the default
  login do today (pinned mount, rewrite in place). Two logins never share a
  file, so a refresh of one cannot overwrite another.
- **Signing in** is Claude Code's own flow. `cloudfield agent login claude
  --as <name>` creates the login and runs `claude auth login` in a
  terminal in its own short-lived container, built by the function that
  builds a session's mounts for that login, with a scratch `.claude.json`
  under the login's directory. The member's environment terminal and the
  default login's three stores are not in it or are masked, so a sign-in
  cannot overwrite or sign out another login. When the command ends the
  server reads the kind and `oauthAccount` from the scratch file, writes
  `identity.json`, and removes the container. A login that never signed in
  is removed. The server never sees a password, runs no OAuth of its own
  and never refreshes a token.
- **Removing** a login deletes its directory and row, under the launch
  authorisation lock. It is refused while any session that is starting,
  running, asleep or still has a container is on it, a queued launch
  asks for it, a sign-in is open on
  it, or an `account_swap` marker names it, listing the sessions. The
  marker, written when a move chooses its target, is the reservation. A
  rename changes the name only; ids and paths never change, and a removed
  login's id is not reused. It does not sign the login out at Anthropic; the command says
  so.
- `member remove` deletes the member's logins. A member merge moves the
  merged member's added logins to the surviving member, renaming on a
  clash, in the step that moves the home, with use for shares **off**:
  the merge drops the shares the merged member granted, and the
  survivor's teammates were never given these logins.

### 3. When Cloudfield switches

**Who may use a login.** Two members matter for a session: its owner, and
the member whose home its container mounts (they differ after a handoff).
A login is **eligible** for a session when all of these hold:

- each of those two members owns it, or holds a share of its account
  while the login is on for shares;
- it is automatic, or its owner named it for this launch;
- it can sign in (read from its file, apart from whether its usage can be
  read): not signed out, not expired without a refresh token, its sign-in
  finished;
- the session can mount it: an Isolated session cannot mount a Console
  login's directory, a runtime may not support the mounts, and a
  teammate's session can be given only a subscription default login, as
  today, since a default login kept as an API key or a Console profile
  has no file a share mounts.

An account none of whose logins is eligible for a launcher is treated as
not shared with them: the launch is refused with that reason, never
served by a login that failed a test.

**At launch.** A launch names an account, as today, and its owner may
also name one of their logins (`--login <name>`, the launch dialog; the
default login is named `default`). A launch that names no login, from any
client, starts from the account's first eligible login in its owner's
order. A stored run with an empty `account_login` is on the default login
and is rebuilt on it; the wire's "none named" and the row's "default" are
never confused, because the server resolves the first into the second
before it stores the run.

That login is used unless a successful usage read shows a window at 95%
or more, or the vendor reported it run out as below. Then the candidates
are every other eligible login of:

1. the same account, in its owner's order;
2. the launcher's own account, when the first was a teammate's and the
   launcher has added logins, in their order;
3. the other accounts shared with the launcher, ranked as today by most
   usage left, each account scored by its first eligible login whose
   usage reads below 95%.

The first candidate whose usage reads below 95% is taken. **A login whose
usage cannot be read is taken only when no candidate with readable usage
is left**, as an unreadable account is today: among those, in the same 1,
2, 3 sequence. That covers a login billed per use (a Console login, for
which Anthropic exposes no usage) and a subscription login whose access
token expired while idle and which holds a refresh token. So a session
uses up every readable subscription it may use, its own before another
teammate's, before it touches anything billed per use. When nothing
qualifies the launch stays where it started, as today.

A launcher with no added login keeps today's ranking exactly: their own
account is ranked with the teammates' by usage, as it is now.

The per-launch order (`--fallback <member-id,...>`) keeps naming members
and stays the whole list of the *other* accounts that may be used, each
standing for its eligible logins. The other logins of the account a
session is on are always candidates; `--no-fallback` turns every
automatic change off, between logins of one account too.

**A login billed per use never runs out the way a window does.** What
holds it back: it is taken only when no readable subscription is left
(above); its automatic and use-for-shares switches; and a sub-session of
a session on one does not inherit it but is chosen for anew. The one hard
stop is the vendor's: the credit its owner bought in the Console, whose
exhaustion is the "out of usage credits" message and moves the session on
like any run-out. Cloudfield's workspace budgets refuse new sessions at
the cap and do not stop a running one, and a session that has reached a
per-use login stays on it until it ends. A session that lands on such a
login, at launch or by a move, says so in its header (`billed per use`),
in its log and in the room note, and the login's owner gets the
notification a move sends today.

A login whose usage cannot be read and which the vendor reported run out
is passed over for 30 minutes, so a Console login out of credit is not
chosen again by every launch. Only a report from a session its owner
owns sets that mark: another member's session cannot push the owner's
launches off a login.

**In a running session.** The signal is the existing one and only that:
the vendor's message, from its exported list, ending the turn. To
another login of the same account only the depletion messages move a
session ("You've
hit your", "You've reached your", "You're out of usage credits", "You're
out of extra usage", "Your org is out of usage"). The seat, allocation,
group and model messages are an administrator's or a plan's restriction,
not a spent balance: on those the account's other logins are skipped,
and the session moves to another account or parks exactly as it does
today. Either way a login whose usage cannot be read is
passed over for the 30 minutes above. Cloudfield
then does what it does today for a teammate's account, with the
candidates in the order above: stop the agent, replace the container
with one on the next login, resume the same conversation and send one
continuation prompt naming both logins.

- The turn has already ended with the vendor's error when the signal
  arrives, so no tool call is in flight. Finished tool calls are in the
  transcript, which lives in the member's home and is the same under every
  login. The prompt is not sent again. The continuation prompt is sent
  **at most once**: the marker records that it is being sent before it is
  sent. An error that proves nothing was sent leaves it owed, and a
  usage-limit error from the next login starts the next move, both as
  today. After a restart that finds it marked as being sent, Cloudfield
  does not send again and does not stop the agent, which may be working
  on it: it clears the marker and writes "the session moved to <login>;
  the continuation prompt may not have been delivered" to the session's
  log. A headless session's continuation is its resume command, so it is
  delivered by starting the container the marker names. Today the marker is cleared after the send
  (`internal/scheduler/acp_driver.go` `sendContinuation`), so a restart
  between the two sends it twice; this plan changes that for every move.
- A signal is acted on only for the container and agent process that
  emitted it. A late or repeated report from the container a move
  replaced is dropped.
- Files the turn wrote are in the session's worktree, which the new
  container mounts. Processes the agent left running in the old container
  and its terminals end with it, as in every move today.
- A login the session already used is not tried again in that session.
- A move chooses its target under the launch authorisation lock, where
  the login must be eligible, and writes the marker there. From then the move is committed to that login:
  the marker blocks its removal, and reordering or turning automatic off
  afterwards governs the next choice, not the move in flight. Losing the
  right to the login is different: the check of the share that exists
  today when the move is recorded stays and covers use for shares, and
  its failure ends as a revoke in that gap ends today.
  The old container is gone by the time the move is recorded, so there is
  no later point at which another candidate could still be tried.
- **A teammate's signal is checked.** The signal comes from inside the
  container and can be forged. When the session's owner is not the
  login's owner, Cloudfield reads the login's usage afresh, every window
  including the per-model ones, before it acts: it moves the session only
  when the read shows a window at 95% or more, and otherwise parks it with
  "the agent reported a usage limit that <owner>'s usage does not show".
  A teammate's session is therefore never moved off a login billed per
  use on its own word, nor walked through the owner's logins by a forged
  signal. The owner's own sessions are moved on the signal alone, as
  today.
- A handoff works as today, with one refusal: a session on a login that
  is off for shares is not handed to anyone but that login's owner.

**When all are spent.** The session parks at `needs-attention` (headless:
fails) with the reason Cloudfield composes today, the logins tried, and
when the first of them is usable again: for each login the latest reset
among its windows at 95% or more, and the earliest of those; "unknown"
when no usage read gave one.

### 4. What the person sees and controls

- `cloudfield agent login claude --as <name>`, `cloudfield agent logins
  [claude]` (name, kind, plan, email, usage windows, order, automatic,
  use for shares, billed per use, sessions on it), `cloudfield agent
  logins rename|remove|order|auto|share`.
- Agents page: the Claude Code row lists its logins in priority order with
  usage, **Add login**, per-login **Remove**, move up and down, and the
  two switches, all visible beside the login. Each login says whether a
  share reaches it and whether it is billed per use, and the page names
  the teammates the account is shared with.
- Session header, `cloudfield sessions` and the session snapshot: `On
  <member>'s account, login <name>`, and for a Console login `billed per
  use`.
- Every move: a `run.account` event with both login ids and the kind of
  credential that now pays, an item-log notice, a room note, and a new
  account segment. Cost stays with the segment that spent it. The
  vendor's message comes from inside the container, so only its first
  line, cut to 200 characters, is kept, and only in the session's own
  item log, which already holds the agent's output. Events, room notes
  and reasons carry text Cloudfield composes.
- Switches: per login (automatic, use for shares), per launch
  (`--no-fallback`), and the saved launch default.

### 5. Sharing and precedence

- **A share shares them all.** `cloudfield account share <member>` lets
  that member's sessions run on any of the owner's logins that is
  eligible for them (section 3), which is every automatic login unless
  the owner turned use for shares off on it. Which one serves a teammate
  is the owner's order and Cloudfield's choice, never the teammate's:
  only a login's owner may name it for a launch, and the server refuses a
  login named by anyone else. A login travels by id on the wire, so a
  name two members both use cannot resolve against the wrong member.
- A teammate's session on an added login gets the mounts of section 2 from
  the owner's login directory, exactly as it gets the owner's default
  login file from the owner's home today.
- Turning **use for shares** off for a login, like revoking a share, moves
  teammates' sessions off it by the existing revoke path, or stops them
  when nothing else can run them. The sweep is keyed by login, and it
  covers sessions whose move marker names the login as their target and
  sessions that are asleep. The owner's own sessions stay.
- The sweep and a relaunch check **both** the session's owner and the
  member whose home it mounts. Today they check one, so a session handed
  to a teammate stays on the first owner's login after that teammate's
  share is revoked (`internal/scheduler/account_revoke.go`,
  `internal/sshd/handlers.go` relaunch); this plan fixes that for every
  login.
- Waking a sleeping session restores a container that holds its login.
  The restore itself, whoever or whatever asked for it, checks that the
  login is still eligible for the session and hands the session to the
  revoke path when it is not (`internal/scheduler/sleep.go`).
- Every check that asks today "does this member hold a share of the run's
  account" (terminals in the session, relaunch, run repository commit and
  push) also asks that the run's login is on for shares, unless the
  member owns it.
- A sub-session is a new admission. It starts on its parent's current
  account and login when that login is eligible for it and is not billed
  per use; otherwise it is chosen for as a launch that names none. The
  check runs again when a queued sub-session is dispatched, and a
  reopened session whose login is gone or no longer eligible is chosen
  for the same way, with a line in its log.
- **Steering.** A member who may steer a session can make its agent act:
  send it prompts, answer its questions, write files into its worktree
  (`internal/sshd/guard.go` `checkSteer`). An agent told to print its
  login file will, so whoever may steer a session can reach the login it
  is on. That is so for the default login today and is so for every
  login now; `security.md` says it, and the two switches are the owner's
  control over where a login can be taken.

### 6. Security

- A refresh token is a bearer secret. An added login's exists only in the
  login's directory and in the containers of sessions running on it.
- The server reads a login only through the allowlisted reader
  (`memberhome.ReadCredential`'s rules: fixed names, opened through a
  root, size limit) to read usage and the login's kind. No RPC returns a
  credential.
- Logs, events, reports, room notes and error text carry a login's id
  and name, never file content. The quota cache
  keeps its fingerprint, not the token.
- **What a share can reach, and what the switches do not do.** A
  teammate's session holds the refresh token of the one login it runs on,
  as it holds the owner's default login today, and by the owner's
  decision a share now reaches every login left on for shares.
  - The server lists the email and organisation of a login to its owner
    only, and a teammate cannot name a login or make the server choose a
    particular one. A teammate's session can still learn who the login
    it runs on belongs to: Claude Code fetches the profile and keeps it
    in its own files in the teammate's home.
  - **Use for shares off** means Cloudfield never mounts that login for a
    session a teammate owns or whose home is a teammate's. It does not
    wall the owner's own sessions: whoever may steer one of those can
    have its agent print the login it is on, and a session handed to a
    teammate still runs in the owner's home.
  - Revoking a share, turning the switch off or removing a login stops
    Cloudfield mounting it. It does not recall a token a teammate's
    session already copied, which stays valid until Anthropic expires or
    the owner revokes it, as `security.md` says of a share today.
  - A teammate's session cannot be moved through the owner's logins on a
    forged signal (section 3).
- **A member's own sessions are not walled from each other.** They share
  the home, so code a session plants there runs in the next container,
  and a session with sudo can unmount what covers the default login. A
  compromised or prompt-injected session can therefore reach, in time,
  every login Cloudfield will put that member's sessions on. That is
  today's exposure of the default login, multiplied by the logins marked
  automatic. Isolated sessions do not write to the home and narrow this.
- A move carries the conversation with it: the next login's organisation
  receives the whole transcript on resume. `security.md` says so.
- **Operators.** The server's operator, and code a workspace admin makes
  every session run (setup scripts), can read any login a session is on,
  as today. Backups of the data directory hold the added logins, as they
  hold the member homes with the default ones; `privacy.md` says so.
- **Refresh.** Claude Code serialises token refresh with a lock in the
  home's `~/.claude/`. Sessions in one home refresh one at a time. A
  teammate's session and an Isolated session have another home and so
  another lock; that gap exists for a shared default login today
  ([shared-account credentials](2026-11-03-shared-account-credentials.md))
  and now covers every shared login.

## Pull requests

| # | Scope | Proves |
| --- | --- | --- |
| 1 | Storage and naming: `agent_logins`, the login directory, the account key type, list, add (sign-in container), rename, remove, order; usage read per login; member remove and merge | Two logins never share a file; one login means no new row or file; a sign-in cannot touch another login |
| 2 | Launch-time choice: `account_login` on runs and segments, mounts and the inode check, candidates, what a share reaches | A spent first login launches on the second; own logins before a teammate's; a grantee is served by the owner's order, cannot name a login and never reaches one that is off for shares |
| 3 | In-session move between an account's logins, use-for-shares off moving teammates off, reset time in the parked reason, handoff refusal | Forced races of the signal against turn end, removal, automatic off and server restart: no replayed prompt, one continuation, recovery tells two logins of one member apart |
| 4 | CLI | Integration test of the commands |
| 5 | Dashboard | Dark-mode design review at desktop, 430 and 360 widths |
| 6 | Docs (`agents.md`, `harnesses.md`, `teams.md`, `security.md`, `privacy.md`, `notices.md`) and an end-to-end test with a stand-in agent that reports "run out" on cue | Add, launch, run out, move, all spent |

1 to 3 are one builder: storage and the switch are coupled.

## Not in this plan

- Other agents. The table and wire fields carry the agent, so Codex can
  follow.
- A Console API key as an added login.
- Sharing one login with one teammate and not another, and limiting a
  login to named workspaces.
- Continuing on the same login after its reset.

## Review

A security review by Claude Opus (`claude-opus-5-5`, fresh context)
changed the first draft:

- A Console API key would have been copied into the member's home through
  Claude Code's backups of `~/.claude.json` and outlived its login. API
  keys are no longer accepted as added logins.
- "A session on login A cannot read login B" was not a boundary inside one
  member's sessions. Section 6 now says what the boundary is.
- Collaborators and handoff recipients could reach an added login. Handoff
  of a session on an added login is refused; the collaborator exposure is
  stated and has the automatic switch as its control.
- Move, recovery and the caches keyed on the member. They key on member
  and login.
- The fallback order, sub-session inheritance and login names could
  resolve against another member. Logins travel by id and a mismatch is
  refused.
- An idle login's expired access token made it unchoosable. It now counts
  as usage unknown when it holds a refresh token.
- The vendor's list holds more than subscription limits. The plan says so,
  and remembers a vendor-reported run-out for logins with no readable
  usage.
- Removal and the automatic switch raced a move. Both are checked under
  the lock that records the move.
- The sign-in terminal, the Isolated rule for a directory login, member
  removal and merge, the inode check, `ANTHROPIC_API_KEY` in the
  environment, and the default login's row were undecided. They are
  decided above.

A second review ran on oh-my-pi, asked for `openai-codex/gpt-6.2-astra`;
it reported its model as `cursor/claude-opus-5-high`, so it was not the
second model family intended. Its findings against the first draft
changed these further points:

- A member with a share of the default login and steer rights could open
  a terminal in the owner's session on an added login. Reaching such a
  session is now the owner's alone.
- The continuation prompt was "once" only while nothing failed between
  sending it and clearing the marker. It is now at most once, and parks
  when delivery is unknown.
- The vendor's message is untrusted text from the container. It stays in
  the session's own log, cut short.
- Administrator and plan restrictions in the vendor's list no longer move
  a session between a member's own logins.
- A late signal from a replaced container, the reservation of a move's
  target against removal, the per-launch order as the whole allowed list,
  the default for clients that send no login, sign-in health apart from
  usage, and the reset time reported were underspecified.

Answered, not changed: the default login's files stay readable inside the
member's own sessions on another login (the boundary is the member); an
older server binary refuses a newer database, as after every migration.

A fresh Opus review of the revised plan (`claude-opus-5-5`) changed:

- Judged hop by hop, a Console login between two personal subscriptions
  made the unsettled pair reachable. Candidates are judged against every
  login a session ran on.
- Steer alone lets a collaborator have the agent print its login, so
  gating terminals was not enough. A session on an added login is steered
  by its owner alone.
- The second check "under the lock that records the move" had nowhere to
  fall back to. The choice is the commitment.
- Parking a session whose continuation may have been delivered would stop
  an agent at work. It is logged instead.
- An organisation out of usage is depletion, not a restriction.
- Which store Claude Code prefers is unverified; a test with the real
  binary now gates pull request 2.
- "No login named" and "the default login" were confused.

Recorded, not changed: the rule allows a move to a seat in another
organisation, which carries the transcript there. That is the liaison's
decision; the setting's text says what moves.

On 2026-11-26 the owner's decision (Status, above) replaced three things
the reviews had shaped: the rule that switched off automatic use between
pairs of logins Anthropic's pages do not settle (and with it the
judging of a login against every login a session ran on); "a share
reaches the default login only"; and "a session on an added login is
steered by its owner alone". Sections 1, 3, 5 and 6 are rewritten for it
and were reviewed again before those parts were built.

The rewritten sections were reviewed again by Claude Opus
(`claude-opus-5-5`) and by oh-my-pi on `openai-codex/gpt-6.2-astra`
(its harness's model field). What that changed:

- "The first login when none is automatic" let a teammate land on a login
  that was off for shares. Eligibility is now one definition, and an
  account with no eligible login is not shared.
- A teammate's per-use login was taken while the launcher's own
  subscription had usage. Unreadable usage is again held back until
  nothing readable is left, as today's code does.
- A teammate could forge the run-out signal and be walked through the
  owner's logins, onto the per-use one. A teammate's signal is now
  checked against a fresh usage read.
- The switches were described as walls they are not. Section 6 says what
  they stop and what they do not.
- Revoke after a handoff checked one member where two matter; waking a
  session restored a login without asking; a move's target was not swept
  when its sharing was withdrawn. All three are fenced.
- The handoff rule would have broken today's handoffs; it now refuses one
  case only.
- A saved fallback order skipped the member's own second login; the
  restriction messages contradicted "as today"; a merge handed logins to
  the survivor's teammates; budgets were described wrongly; removal
  missed a session still starting.

## As built

Built in #1142, #1148, #1149, #1144 and #1143. Where the build differs from
the plan above:

- A merge moves the absorbed member's login directories after its
  transaction commits, not in the step that moves the home; a crash between
  the two, or a move or a member removal that fails to move or delete the
  directories, is logged and settled by the next start's `ReconcileLogins`,
  and the merge or removal still succeeds. Moved logins keep their Automatic
  switch and go after the survivor's own, off for shares; a clashing name is
  cut to fit 32 characters before its suffix.
- A sign-in needs the launch permission and is admitted like a launch. Its
  terminal must open within 10 minutes and its container start within 5; it
  is stopped 15 minutes after its container started. Pending sign-ins and
  their outcomes are kept in the server's memory and do not survive a
  restart.
- Besides a workspace or server variable, an `apiKeyHelper` or a sign-in
  variable under `env` in the home's `~/.claude/settings.json` refuses a named
  added login, and automatic choices skip added logins there. So does an
  agent definition that gives `claude` its own executable. A launch naming
  no login on the member's own account, where no login serves without being
  named, is refused, naming each login and why.
- In a session on an added login, each variable that signs Claude Code in,
  and `ANTHROPIC_BASE_URL`, that no workspace or server variable sets is set
  empty; a set value that does not sign Claude Code in stays.
  `ANTHROPIC_PROFILE` is always set empty. Only a change of a login's kind is
  refused; a session that can write a login's directory can put another
  account's sign-in of the same kind there. A setting written into the home
  after the launch checks, a repository's `.claude/settings.json` or
  `.claude/settings.local.json`, and managed settings in an image can still
  sign Claude Code in.
- The mask over the legacy `~/.claude/.config.json` is a plain bind and is
  not inode-checked; the inode check of the other mounts runs once, when the
  container starts. Session VMs refuse every added login.
- An Enhanced session's owed continuation prompt is dropped, with a notice
  in the item log, when a person's prompt reaches the conversation first.
  Only a session the server cannot reach, a prompt the agent host reports
  unsent, or a failure to record durably that it is being sent leaves it
  owed; any other error clears it. A Standard or Background session gets the
  continuation as the prompt of the command that resumes the conversation.
- A Background session that a restart finds sending its continuation is
  settled from its log: ended with "the continuation prompt may not have
  been delivered" when the log does not show the prompt, resumed when the
  turn it started was cut short, and otherwise ended as that turn ended.
- A teammate's run-out signal counts a report the vendor sent in the last 10
  seconds as a fresh read.
- A reopened session whose login is gone, off for shares or automatic off
  for a member who does not own it is refused, not chosen for anew.
- The owner of a login billed per use is not notified when a teammate's
  session lands on it; the session's log, the room note and `per_use` on
  `run.account` say so.
- Three checks apply only to accounts whose owner keeps login rows, so a
  member who keeps none sees no change; removing the last added login keeps
  the default login's row when one of its switches is off: the fresh read of a teammate's
  signal, the owner check in the revoke sweep, and the login check on wake.
- The 30-minute pass-over of a login reported run out is kept in memory; a
  restart forgets it.
- The run repository's "account changed" check still compares members; a
  move between one member's logins replaces the container, which the
  execution check refuses.
