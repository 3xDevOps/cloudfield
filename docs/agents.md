# Set up and drive Cloudfield from a coding agent

This guide is for a coding agent, such as Claude Code, Codex or opencode,
working in a shell on a developer's computer. With it the agent installs
Cloudfield, sets up the server over ssh, and launches and follows sessions through the
`cloudfield` CLI. The human keeps every sign-in, approval and secret.

Cloudfield's terms: the **server** is the Linux machine where agents run
(`cloudfield-server`); this computer is a **client** (`cloudfield`). A **workspace** is
one repository on the server. A **session** is one agent working on one task in its
own sandbox (a container or virtual machine), on its own git branch. The **edge** is a relay at
`https://edge.cloudfield.ai` that the server and clients dial out to, so neither
needs an open port. The [glossary](README.md#concepts) has the rest.

`cloudfield <command> --help` prints any command's usage, flags and an example.
`cloudfield help agents` prints this guide.

## The prompt

The human pastes this into their coding agent:

```text
Set up Cloudfield on my server and this computer, or do this Cloudfield task: <task>.
Read the guide first: run `cloudfield help agents` (use `aether` wherever
`cloudfield` is not found), or if neither is installed, fetch
https://raw.githubusercontent.com/3xDevOps/cloudfield/main/docs/agents.md

Rules:
1. Run `cloudfield status` first, if Cloudfield is installed, and do only what
   is missing.
2. Before you change anything, ask me every question you need in one message:
   - the server as user@host, and whether that user is root or has
     passwordless sudo
   - edge or Tailscale
   - with the edge: is a GitHub sign-in enough (account), or must I approve
     each new device (approved-devices)
   - which coding agent, and whether it signs in with a subscription or an
     API key
   - which repository: a path on this computer or a GitHub URL
   - whether sessions may use Docker inside them, which installs Sysbox and
     changes /etc/docker/daemon.json on the server
   - CLI only, or the desktop app too
3. Never ask me for a password, sudo password, token or key in chat.
4. Hand every sign-in to me as "open this URL and enter this code", then wait
   for it to finish.
5. Never approve devices, members, invites or session permission requests
   yourself. Tell me the command and let me run it.
6. Ask before anything that costs money or publishes: firewall changes,
   deploy keys, pushes to GitHub, pull requests.
7. If a command fails, stop and show me the real error and the command it
   names. Do not work around a check.
```

## Before you start

- **Human** marks a step only the human does, in their own terminal or
  browser.
- `cloudfield login --json`, `cloudfield github connect` and `cloudfield agent login codex`
  print a URL and a code, then wait until the human finishes or the code
  expires after 10 to 15 minutes. Run them in the background with output to a
  file, give the human the URL and code, and wait for the command to exit 0.
- `cloudfield wait` runs until the session needs something or its `--timeout`. Run it
  in the background too, or give it a `--timeout` shorter than your shell
  tool's limit and repeat it.
- `cloudfield status` prints what this computer is linked to and a `next` line with
  the command for the first missing step. `an admin runs:` or
  `a human runs in a terminal:` before it means it is not yours to run.
  `cloudfield status --json` prints the same as one object.

## Set up

### 1. Install the CLI on this computer

CLI only, into `~/.local/bin`, without `sudo`:

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --client --role none --bin-dir "$HOME/.local/bin"
```

With the desktop app too (it downloads Electron and takes a few minutes):

```sh
curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --role client
```

Check with `cloudfield version`. On Windows, follow
[install.md](install.md#windows-install-script).

### 2. Install the server over ssh

The server needs Linux, Docker Engine and git, and at least 2.5 GiB of
available memory ([install.md](install.md#server-prerequisites)). These need a
user that is root or has passwordless `sudo`. If `sudo` asks for a password,
**Human:** run each command below in their own terminal as
`ssh -t <user>@<host> '...'`.

The first `ssh` to a new server fails without a terminal to confirm its host
key. **Human:** ssh to it once and check the key. Never pass
`StrictHostKeyChecking=no` or `accept-new`.

Add `--sysbox=false` to the `cloudfield-server install` commands below unless the
human allowed Docker inside sessions.

```sh
ssh <user>@<host> 'curl -fsSL https://raw.githubusercontent.com/3xDevOps/cloudfield/main/scripts/install.sh | sh -s -- --role none'
```

With the edge, pass the access policy the human chose:

```sh
ssh <user>@<host> 'sudo cloudfield-server install --edge-url https://edge.cloudfield.ai --edge-access <account|approved-devices>'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
ssh <user>@<host> "timeout 300 sh -c 'until sudo cloudfield-server edge status >/dev/null; do sleep 5; done'"
ssh <user>@<host> 'sudo cloudfield-server edge claim-code'
```

`edge status` exits 0 when connected and 3 otherwise, while connecting or
when it cannot connect. If the wait times out, run
`sudo cloudfield-server edge status` once more and show the human its
`connection` line. `claim-code` prints the claim code, valid for 30 minutes.

With Tailscale on the server and this computer, leave the edge off:

```sh
ssh <user>@<host> 'sudo cloudfield-server install'
ssh <user>@<host> 'sudo systemctl daemon-reload && sudo systemctl enable --now aether-server'
```

If `install` prints a `warning:` line or `Docker does not answer`, show it to
the human before you go on.

### 3. Sign in and claim the server

With the edge:

```sh
cloudfield login --json
```

**Human:** open `verification_uri` from the first JSON line, sign in with
GitHub and enter `user_code`. The second line reads `"state":"signed_in"`.
Then claim the server, which makes the human its administrator:

```sh
cloudfield link --claim <claim code>
```

It ends with `linked to server <server id> through edge.cloudfield.ai as <name>
(admin)`. If the code expired, `sudo cloudfield-server edge claim-code` on the
server prints a new one.

With Tailscale, the first link becomes admin: `cloudfield link <host>:2222`. Run
it right after the server starts: until then any SSH key that reaches port 2222
becomes admin, so that port must stay closed to the internet
([networking.md](networking.md#the-first-admin)). Ask the human before you
change a firewall.

### 4. Add the repository

```sh
cloudfield workspace import ~/src/app
cloudfield workspace import https://github.com/acme/app
```

A path imports the clone and pushes its checked-out branch to the server. A
GitHub URL needs GitHub connected first ([Publish](#bring-results-back-and-publish)
shows `cloudfield github connect`). It prints `fetched <branch> at <commit>` and an
`aether workspace mirror adopt ... --yes` command: show the human the commit and
run the command once they confirm it.

### 5. Install and sign in the agent

```sh
cloudfield agent add claude
```

The agent signs in inside the human's environment on the server:

- Codex: `cloudfield agent login codex` prints a URL and a code. **Human:** open it
  and enter the code.
- Claude Code and the others need typed input. **Human:** run
  `cloudfield agent login <name>` in their own terminal.
- With an API key instead, **Human:** run
  `cloudfield workspace env set --secret ANTHROPIC_API_KEY` for Claude Code, which
  reads the key with input hidden. Other agents' keys are in
  [harnesses.md](harnesses.md#use-an-api-key).

Check with `cloudfield agent list --json` (`login_found`) and
`cloudfield account usage`, which asks the vendor whether the login works. Both
look only for a login in your home. A key set with
`cloudfield workspace env set --secret` writes none, so `login_found` stays
false, `account usage` reports the agent unauthenticated, and `cloudfield status`
keeps naming `aether agent login`; go on to step 6. A Claude Code sign-in
through the Anthropic Console is a login: `login_found` is true, and
`account usage` reports `unsupported`, because the API bills it and no usage
window measures it ([harnesses.md](harnesses.md#claude-code)).

### 6. Launch the first session

```sh
cloudfield session "add a health check endpoint" --workspace app --agent claude
cloudfield wait run-d6ay38tj8j --timeout 30m
```

`cloudfield session` prints `session <session-id> running`. If it fails or is interrupted, it
prints the retry with `--idempotency-key`; that retry returns the same session
instead of starting another. `cloudfield session - < task.md` reads the task from
stdin.

`--linear-issue <url>` links the session to a Linear issue, for example
`--linear-issue https://linear.app/acme/issue/ENG-123`. Without it, the first
Linear issue link in the task is used. The link is fixed at launch. A
malformed URL fails with
`linear issue URL "<value>" must have the form https://linear.app/<workspace>/issue/<TEAM>-<number>[/<slug>]`.

Sessions of harnesses that carry Cloudfield's instructions, such as Claude Code,
Codex and opencode, are told the linked issue and to put `Fixes ENG-123` in
the description of any pull request they open. Gemini, Cursor, Antigravity and
custom harnesses are not told. `cloudfield
publish` and the Changes view's **Publish…** append that line when the body
does not already name the identifier, before any trailing `Co-authored-by`
trailers, so Linear's GitHub integration links the pull request and closes the
issue when it merges. A pull request the agent opens itself with `gh pr
create` gets no appended line.

## Add a repository

```sh
cloudfield workspace import <path|url>
```

Only an admin can add one. Re-running it for a clone already imported links the
clone to its workspace.

## Work across repos

```sh
cloudfield session "rename the /users endpoint in the API and the web client" --workspace web --repo api --agent claude
```

Each `--repo` workspace is checked out at `/repos/<name>` on its own branch.
`cloudfield pull <session-id>` fetches every repo's branch into the clone linked for
its workspace.

## Add another laptop

Install the CLI there (step 1), then:

```sh
cloudfield login --json
cloudfield servers
cloudfield link <server id>
```

Under `approved-devices`, `cloudfield link <server id>` exits 1 and prints the
`cloudfield device approve` command for this device. **Human:** approve it from the
dashboard's **Needs you** card, or run that command on an approved device; it
asks `y/N`. Then run `cloudfield link <server id>` again.

With Tailscale: `cloudfield link <host>:2222`.

## Add a teammate

Ask the human for the teammate's GitHub login and role, then:

```sh
cloudfield invite --github <login> --role collaborator
```

It prints an invite link once. The teammate runs `cloudfield login`, then
`cloudfield link '<invite link>'` (`aether link` on Windows). Under `approved-devices`, **Human:** approve
their device as above.

With Tailscale, the teammate runs `cloudfield link <host>:2222`; **Human:** run the
`cloudfield member approve <member-id>` it prints. Roles and invite codes are in
[teams.md](teams.md).

## Add a coding agent

```sh
cloudfield agent add codex
cloudfield agent login codex
cloudfield agent list --json
```

Sign-in works as in step 5. Agent names: `claude`, `codex`, `opencode`, `pi`,
`omp`, `copilot`, `gemini`, `cursor`, `antigravity`
([harnesses.md](harnesses.md)).

## Several logins

A **login** is one sign-in of an agent. Your **default login** is the one in
your home, which `cloudfield agent login claude` signs in. Claude Code can
also keep **added logins**, stored on the server outside your home, so that a
session can start or continue on another of your subscriptions when one runs
out:

```sh
cloudfield agent login claude --as work     # add a login named work
cloudfield agent logins                     # list your logins in the order Cloudfield tries them
cloudfield session "fix the flaky test" --agent claude --login work
```

Notes, reasons and prompts name a login as its member's name with an added
login's name after it, `Ada (work)`; a default login is the member's name
alone. The dashboard's **Agents** page lists the same logins with the same
controls, and the launch dialog's **Login** field names one. The CLI prints a
server's refusal as `rpc error <code>: <message>`; the quotes below are the
message.

### Add a login

Adding a login needs a role that may launch sessions, collaborator or above
([teams.md](teams.md#roles)); a viewer is refused with `agent.login.add:
permission denied: launching sessions requires the collaborator role`. It
also needs Claude Code installed in your environment, or the CLI stops with
`agent claude is not installed in your environment; run aether agent add
claude first`, and a server that advertises the `logins` feature; against an
older one it stops with `--as: server <version> lacks the "logins" feature, so
it keeps only the login in your home; update the server with cloudfield
server update`, which a super admin runs.

A new login starts with **Automatic** and **Use for shares** on, at once:
Cloudfield may start or move your sessions onto it, and the sessions of
teammates you share your account with can run on it
([teams.md](teams.md#shares-and-several-logins)). On a Console login each of
those sessions is charged per use to your Console organisation, and Cloudfield
does not tell you when a teammate's session lands there. There is no interval in which the login
is yours alone; to keep it out of reach, run the two commands the sign-in
prints last as soon as it ends.

`--as <name>` runs `claude auth login` in a container of its own, with your
home mounted and the new login's directory mounted over the files that hold
your default login, so Claude Code saves the sign-in into the new login. The
`claude` it runs is the one in your home's `~/.local/bin`: code planted in your
home runs during the sign-in and can capture the new login
([security.md](security.md#several-logins)). It prints:

```text
adding login work as lgn-…: claude auth login runs in a container of its own; follow its prompts
if this terminal goes before the sign-in ends, return to it with: cloudfield agent login claude --attach lgn-…
```

**Human:** run it in your own terminal and follow the prompts; without a
terminal it prints the command for one and reserves nothing. It ends with:

```text
added login work: subscription, plan max, <email>, organisation <name>
Automatic is on: Cloudfield may start or move your sessions onto it; turn it off with: cloudfield agent logins auto work off
Use for shares is on: teammates you share your account with can be served by it; turn it off with: cloudfield agent logins share work off
```

The first line leaves out what Claude Code did not record and ends with
`billed per use` for a Console login, whose two switch lines add `, and those
sessions are charged per use to it` before the `;`.

A name has 1 to 32 lowercase letters, digits and `-`, starts with a letter or
digit, and is unique among your logins. `default` and names starting with
`lgn-`, the form of a login's id, are refused.

You can add a Claude subscription (claude.ai) sign-in, or an Anthropic Console
sign-in that creates no API key. Nothing is added when the sign-in:

- saved a Console API key: `the sign-in saved an Anthropic Console API key,
  which Cloudfield does not keep as an added login: Claude Code copies it into
  backups in your home, where it would outlive the login. Keep the key as your
  default login or as a workspace secret (docs/harnesses.md#use-an-api-key);
  nothing was added` ([harnesses.md](harnesses.md#use-an-api-key)). Cloudfield
  does not revoke the key;
- saved nothing: `the sign-in ended before Claude Code saved a login; nothing
  was added`;
- recorded the same email and organisation as a login you have: `this sign-in
  is the account and organisation your login "work" is signed in to, so it was
  not added; sign in to another account to add a login`. When a login you have
  cannot be read for this check: `your login "work" could not be read to check
  this sign-in is another account, so it was not added: <error>`.

You run one sign-in at a time: a second `--as` fails with `store: conflict: the
sign-in of login "work" (lgn-…) is still running; finish it first`, followed by
`that sign-in ends by itself about 15 minutes after its terminal opened, or 10
minutes after it was started if none did; to finish it sooner, return to it
with:` and the `--attach` command. A sign-in whose terminal is not opened
within 10 minutes, or whose container does not start within 5, ends with
nothing added; one still running 15 minutes after its container started,
which can be minutes after its terminal opened, is stopped. Closing
your terminal does not end it: `cloudfield agent login claude --attach
<login-id>` returns to it, or, for 10 minutes after it ended, prints how it
ended. The server keeps a running sign-in and its outcome in memory: after a
server restart, run `cloudfield agent logins` and, if the login is not listed,
add it again.

### Manage logins

`cloudfield agent logins` prints one row per login:

| Column | Meaning |
| --- | --- |
| `#` | Position in your order |
| `LOGIN` | Name; `default` is the login in your home |
| `KIND` | `subscription`, `console` (a Console sign-in without a key), or `api_key` (a default login only) |
| `PLAN`, `EMAIL`, `ORGANISATION` | What Claude Code recorded at sign-in |
| `USAGE` | Each usage window's share used; otherwise the read's status, such as `unsupported` for a Console login, which has no windows, or `stale`; `not read` when the request failed; `-` when nothing was read. Errors are printed under the table |
| `AUTOMATIC`, `SHARES` | The two switches below |
| `BILLED` | `per use` when the recorded kind is `console` or `api_key`. It labels the sign-in Cloudfield reads, not who pays: [What does not work](#what-does-not-work-with-added-logins) and [security.md](security.md#several-logins) list what can pay instead |
| `YOUR SESSIONS` | How many of your sessions that have not ended run on it; `-` when they could not be read, with the error on stderr. Closed sessions' kept containers and teammates' sessions, which also block **Remove**, are not counted |

`--json` prints the same, with the ids of those sessions (`null` when they
could not be read). The other commands take a login's name or id:

With a second added login, `spare`:

```sh
cloudfield agent logins order work default spare   # every login once, default included
cloudfield agent logins auto spare off
cloudfield agent logins share work off
cloudfield agent logins rename spare personal
cloudfield agent logins remove personal         # asks first; --yes without a terminal
```

- **Order** is the order Cloudfield tries your logins in, for your sessions
  and your teammates'. A new login of kind `subscription` goes just before
  the first login in your order whose recorded kind is `console` or
  `api_key`, or last when there is none; a new `console` login goes last. The
  dashboard marks the first login with Automatic on **tried first**: a launch
  that names no login starts there unless the rules in
  [Which login a session starts on](#which-login-a-session-starts-on) pick
  another.
- **Rename** changes the name; the id stays.
- **Remove** deletes the login's files and its row from the server.
  Cloudfield does not sign the login out at Anthropic. Adding it again gives
  a new id, the place a new login gets, and both switches on.

Each switch is on when the login is added:

| Off | Does | Does not |
| --- | --- | --- |
| **Automatic** | Cloudfield never starts a session on the login unless you name it with `--login`, and never moves a session onto it. A teammate's session no longer starts there, and reopening a teammate's kept session on it is refused. | Move or stop a session already on it, yours or a teammate's, which still wakes there from sleep, or a move already under way to it. |
| **Use for shares** | No new teammate's session starts or moves there. Teammates' running sessions on it are moved to another login or account they may use, or stopped, shortly after ([teams.md](teams.md#shares-and-several-logins)). | Touch your own sessions, or recall a token a session already copied. |

**Remove** is refused while anything may still mount the login: a session on
it that has not ended, a closed session's kept container
([failure-handling.md](failure-handling.md#relaunching-a-closed-session)), a
move taking a session to it, a [subsession](teams.md#subsessions) queued or
launching on it, or, just
after a server start, a kept container whose record the server has not read
yet (`the server is still recovering its sessions; try again in a moment`).
The refusal names your own sessions and counts your teammates':

```text
login "work" is in use by your sessions run-… (end them, or free the sandbox of one that has ended) and 1 of your teammates' sessions, which hold it until they end and their containers go (revoking the share you gave a teammate, with cloudfield account revoke <member-id>, moves their running sessions off it)
```

A closed session has ended but keeps its container, and so the login, for
`--run-container-ttl`: 30 days by default, 7 for a
[Standard](harnesses.md#modes-and-what-the-session-container-does) session's
container on a Docker host. **Free
sandbox…** on the session in the dashboard removes it sooner;
`cloudfield delete <session-id>` does too, with the session's checkout and
transcript. For teammates' sessions, `cloudfield agent logins share <name>
off` moves their running ones off the login; their kept containers wait for
someone who may stop those sessions to free them
([who can act](security.md#who-can-see-and-act-on-a-session)), or for the
deadline.

On a terminal, `cloudfield agent logins remove <login>` describes the login
and asks before it removes anything:

```text
login personal: subscription, plan max, <email>, organisation <name>
Removing it deletes its files from Cloudfield and does not sign it out at Anthropic; Cloudfield refuses while a session is on it. Remove? [y/N]:
```

Only `y` or `yes`, in any case, removes it; any other answer prints
`cloudfield agent logins remove: nothing was removed`. Without a terminal it
refuses unless you add `--yes`, which skips the question: `--yes is required
because cloudfield agent logins remove deletes the login's files from
Cloudfield: cloudfield agent logins remove personal --yes`.

The default login is not renamed or removed here. Order and both switches
cover it once you have added a login; before that they fail with `you have
only the default login; add a login first`. A switch you turned off on the
default login stays off after you remove your last added login, though the
dashboard then shows no switches: `cloudfield agent logins auto default on`
and `cloudfield agent logins share default on` turn them on again.

### Which login a session starts on

`--login <name>` takes one of your own logins by name or id; `--login default`
is the login in your home. Only a login's owner names it.

Without `--login`, the session starts on the first login in your order that is
automatic and can serve it: it holds a sign-in, the session can mount it, and
nothing in [What does not work](#what-does-not-work-with-added-logins) rules
it out. A default login whose files the server cannot read counts as able to
serve. When a workspace or server variable or `~/.claude/settings.json` signs
Claude Code in, the session starts on the default login, since that setting
pays whatever the login. When none of your logins can serve, the launch fails
with:

```text
none of your logins starts a session unless you name it: <login>: <why>; name one with cloudfield session --agent claude --login <name>, or let Cloudfield choose it with cloudfield agent logins auto <name> on
```

`<login>: <why>` repeats, separated by `; `, once for each login, and reads
`<login> is automatic off` for a login with Automatic off. The full command is
`cloudfield session "<task>" --agent claude --login <name>`.

Named or not, when a usage read shows the login's five-hour or seven-day
window at 95% or more, the session starts on the first other candidate whose
usage reads below that: your other automatic logins in your order, then the
accounts shared with you, as [usage fallback](teams.md#usage-fallback) ranks
them. When
none does, it takes the first whose usage cannot be read, and otherwise
starts where it was going to. A launch that names no login and would start
on a login [billed per use](#when-a-login-runs-out) starts on the first
candidate with usage known to be left instead, and otherwise stays. The
dashboard's **At its limit** mark counts any current window at 95%, a
model's weekly one included, so a launch can still start on a login marked
so. The CLI says where the session started when that is not the login you
named, and when the login is billed per use:

```text
session run-4zjmsfgn3s running
started on your login console, not work; billed per use
```

`--no-fallback` turns every automatic change off, between your own logins
too: the session starts on the login you named or, unnamed, on the first
automatic login that can serve it, whatever its usage or billing, and never
moves.

### When a login runs out

A session moves only when a turn ends on one of Claude Code's usage-limit
messages, those starting with a prefix in the list in
`internal/agentstatus/agentstatus.go`, such as `You've hit your` or `You're
out of usage credits` ([harnesses.md](harnesses.md#usage-limit-signals)). Any
other error, such as a signed-out login or a transient 429, moves nothing.
Cloudfield stops the agent, starts a new container on the next login, resumes
the same conversation there, and sends the agent one prompt:

```text
Your previous turn stopped because Ada's usage ran out. The session continues on Ada (work)'s account. Continue the task where you stopped.
```

The candidates are those of a launch, and a login the session already ran on
is not tried again (a teammate's login only once Cloudfield confirmed its
mount in the container). Only a spent balance moves a session between the
logins of one account; a seat, allocation, group or model message restricts
the whole account, so the session skips its other logins and moves to another
account or stops. When the session is not its login owner's own, Cloudfield
first reads the login's usage again and moves it only when a current window,
of any kind, reads 95% or more ([security.md](security.md#several-logins)). A
teammate's session on a Console login, which has no windows, therefore stops
on such a message instead of moving.

The session's notes, which its **Chat** view in the dashboard shows, get
`Continued on Ada (work)'s account because Ada's usage ran out.` An Enhanced
session ([enhanced-sessions.md](enhanced-sessions.md)) gets the continuation prompt at most once, and not at all when a person's
message reaches the agent first; a Standard or
[Background](enhanced-sessions.md#background-sessions) session gets it as
the prompt of the command that resumes the conversation. A message sent
while a move is in flight is refused with `ptyhost: no terminal for session:
scheduler: the session is moving to another account or login; send the
message when it finishes`.

A login **billed per use** has no usage windows: the API bills each request.
That is a Console login and, once you have added a login, a default login
kept as an API key or a Console profile, or signed in by an `apiKeyHelper` or
a key in `~/.claude/settings.json`. Unless you name it or launch with
`--no-fallback`, a session reaches one, at launch or by a move, only when no
login it may use has usage known to be left, and a subsession does not
inherit its parent's login when that is billed per use. Landing on it adds
`Ada (console)'s account is billed per use.` to the session's log and notes,
and `cloudfield sessions` shows `billed per use`.

Cloudfield enforces no spending cap on a login billed per use;
[workspace budgets](teams.md#budgets) refuse new sessions at the cap and do
not stop a running one. A session moves off such a login for usage only on
one of Claude Code's usage-limit messages, and no test here shows which
message, if any,
Anthropic gives a Console account out of credit: if it matches none of the
prefixes, the session stops with Claude Code's error instead of moving. After
such a message from your own session on a login whose usage cannot be read,
Cloudfield counts that login as spent for 30 minutes, except as the start of
a launch that names no login. The server keeps this in memory: a restart, or
removing one of your logins, forgets it.

When no login or account the session may use has usage left, an interactive
session parks as **Needs you** and a Background session fails. Once the
session has run on two logins or more, the reason lists those it left
because they ran out, such as `Usage limit reached on Ada (work)'s account;
no fallback has usage left; logins tried: Ada, Ada (work); the first is
usable again at 2026-11-27T18:00:00Z`
([failure-handling.md](failure-handling.md#several-logins)).

### What does not work with added logins

| Setup | Refusal | Why |
| --- | --- | --- |
| A workspace or server variable that signs Claude Code in, such as `ANTHROPIC_API_KEY` | `scheduler: a sign-in outside the login pays here: a workspace or server variable signs the agent in, so login lgn-… would not pay; launch on the default login` | Claude Code would pay with the variable. A launch that names no login stays on the default login. |
| `apiKeyHelper`, or a variable that signs Claude Code in under `env`, in `~/.claude/settings.json` | The first row's refusal, with `apiKeyHelper in ~/.claude/settings.json signs the agent in`, or `ANTHROPIC_API_KEY under env in ~/.claude/settings.json signs the agent in`, in place of `a workspace or server variable signs the agent in` | Claude Code prefers those settings to a login file. |
| A `~/.claude/settings.json` the server cannot read: not a regular file, another hard link, not JSON, or over 1 MiB | The first row's refusal, with `~/.claude/settings.json may sign the agent in and cannot be read: <error>` in place of `a workspace or server variable signs the agent in` | It may hold either of the above. Fix the file the error names, or launch on the default login. |
| An agent definition (`--agent-definitions`) that gives `claude` its own executable | `scheduler: only Claude Code keeps added logins: this server runs claude from its --agent-definitions entry, whose executable leaves no place for an added login; use the default login, or the server's operator removes executable from that entry, or the entry, to use added logins` | The definition has no sign-in command and no account configuration to mount a login into. |
| An [Isolated](security.md#permission-tiers) session on an added Console login | `scheduler: login lgn-…: an Isolated session cannot use a Console login: it is a whole directory the session could change for later sessions; launch it at Default, or on another login` | The session could change the login's directory for later sessions. A Console default login runs Isolated as before. |
| Session VMs (`session-runtime vm`) | `scheduler: session VMs cannot verify an added login's mounts yet, so they do not run on one; launch on the default login` | Cloudfield cannot yet check a VM's mounts. |

Anthropic's pages show how to stay [signed in to several accounts, each in
its own configuration directory](https://code.claude.com/docs/en/authentication),
offer [a Console account at a subscription
limit](https://support.claude.com/en/articles/11145838), and say [you can belong
to several Team or Enterprise
organizations](https://support.claude.com/en/articles/13566435), while the
[Usage Policy](https://www.anthropic.com/legal/aup) rules out creating multiple
accounts to "circumvent product guardrails", [Claude Code's legal
page](https://code.claude.com/docs/en/legal-and-compliance) says "Advertised
usage limits for Pro, Max, and Ultra plans assume ordinary, individual usage",
and, on sharing a login with teammates, the [consumer
terms](https://www.anthropic.com/legal/consumer-terms) say "You may not share
your Account login information, Anthropic API key, or Account credentials with
anyone else"; none of them says whether moving
between two of your own subscriptions at a usage limit is allowed or
forbidden.

## Follow and answer sessions

```sh
cloudfield sessions --needs-you
cloudfield sessions <session-id> --json
cloudfield wait <session-id> [<session-id>...] --timeout 30m
cloudfield logs <session-id>
cloudfield messages <session-id> --json
cloudfield message <session-id> "<reply>"
```

Given several session ids, `cloudfield wait` returns when the first of them
would end a wait on it alone, prints that session and exits with its code;
when more than one would, the first in the order given. A session already in
such a state (finished, failed, needs you, parked or a broken wait) ends the
wait at once every time, so leave it out to wait on the rest. It reads the
sessions in the order given and stops at the one that ends the wait, so an
unknown id after that one is not reported. On timeout it prints every session,
and with `--json` nothing.

`cloudfield wait` exits 0 when the session finished, 1 when it failed, 3 when it needs
you, 4 when it is parked with nothing pending (its turn ended without a
report, or it went silent) and a message continues it, 5 when its wait broke
(a condition resolved and the wake never reached the agent, or its source
went unread for an hour), and 124 on
timeout. `cloudfield sessions --needs-you` prints on stdout a table of the
sessions waiting on a member, then on stderr what each needs from you with the
command that answers it, or that it waits on another member. `cloudfield
sessions <session-id>` prints that for one session on stdout. With `--json` a
session carries its `agent`, its `mode`
(`standard`, `enhanced` or `background`, as `--mode` takes it),
`unacked_messages` (agent mail it has not acknowledged) and, when set,
`parent_run_id` (on a subsession, with `detached` once it runs on its own),
`wait_conditions` and `controller_member_id`. `cloudfield messages <session-id>` prints
the messages the session sent to or received from other agents, bodies
included; a line from the session to itself is one the server wrote to wake
it. What you send with `cloudfield message` reaches the agent as a prompt and
is not listed.

`cloudfield message --attach <path> <session-id> "<text>"` sends a file from
this computer with the message, and `--steer` adds the message to an Enhanced
agent's running turn instead of queueing it. Both flags go before the session
id: every word after it is the message, and either flag there is refused.

A permission request is the human's: show it with its options, and run the
`cloudfield answer <session-id> <request-id> <option-id>` they pick. It allows or
rejects once, never always. Relay a session's question to the human unless the
task they gave you already answers it.

## Bring results back and publish

```sh
cloudfield github connect
cloudfield publish <session-id>
cloudfield close <session-id> --outcome merged
cloudfield pull <session-id>
```

Publish before you close: a finished session needs `cloudfield reopen <session-id>` first.
Publish pushes commits only; if it reports uncommitted changes, ask the agent
to commit with `cloudfield message <session-id> "commit your changes"`, then publish
again.

`cloudfield pull` fetches the session's branch into the linked clone without merging.
The branch gets the agent's work when the session closes, so pull after close.
Ask the human before `cloudfield github connect`: it grants `repo`, `read:org`,
`gist` and `admin:ssh_signing_key`. **Human:** open the URL and enter the code.
Ask before `cloudfield publish`: it pushes the branch and opens a pull request.
`--outcome abandoned` closes a session without its result.

## Update

```sh
cloudfield server update --status
cloudfield update --check --json
```

Update the server before the clients. Ask the human first: `cloudfield server
update` (admin only) restarts the server, and `cloudfield update` replaces this
CLI. Sessions keep going through a server update.

## Remember Cloudfield

With the human's consent, add this line to their global agent instructions,
for example `~/.claude/CLAUDE.md` or `~/.codex/AGENTS.md`:

```text
This machine uses Cloudfield; for Cloudfield tasks run `cloudfield help agents` (`aether help agents` if `cloudfield` is not found).
```
