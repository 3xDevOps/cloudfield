# Retiring an Address Pair of an Edge

**Goal:** an edge that serves more than one
[address pair](../edge.md#more-than-one-address-pair) can switch one off
without stranding anyone silently: whoever still uses the pair is warned
with the exact command, the edge's operator can count who is left, and the
pair ends with an error that names its replacement.

**Status:** draft, 2026-11-26. The owner decided the same day to retire the
hosted edge's first pair (`auth.onaether.dev` + `edge.onaether.dev`) on a
date he names later, which reverses
[decision 7](2026-11-15-cloudfield-rename.md#7-repository-names-and-the-old-domain)
of the rename plan for those two host names. Everything marked
*Assumption* is this plan's choice and stands unless the owner changes it.

## Rules

- Nothing changes for any edge until its operator sets the new setting. No
  date, host name or state is built in. An edge with one pair cannot use
  the setting and is not affected.
- A date is text shown to people. The edge never acts on it: only the
  operator's own edit moves a pair to the next state.
- No request is redirected to another origin, on any path, in any state.
  Sign-in, token, callback, join and connector paths carry codes or tokens
  in their URLs or bodies, and the edge client refuses redirects
  (`internal/edge/client/edgeclient.go`), so a retired pair answers with an
  error that names the replacement and the person or program starts again
  there.
- Key pinning, grant issuer and audience checks, connector token binding,
  origin checks, CSRF and `__Host-` cookie scope stay as they are. Nothing
  issued at one pair is accepted at another to make a move easier.
- A notice names the replacement as text. No client changes its
  configuration, signs in or sends a credential because of a notice.

## States

| State | Set by | What the pair does |
| --- | --- | --- |
| active | default | As today |
| retiring | the setting, with an optional date | Everything works. The edge tells servers, clients and browsers that the pair is retiring and names the replacement, and records who still uses it |
| retired | the setting | Every request on either host gets an error naming the replacement. Nothing is relayed, signed in or issued. A device token or browser session presented there is ended |
| removed | the operator deletes the pair from the configuration and DNS | The host names answer nothing. No product state |

retiring to active and retired to retiring are undone by editing the
setting and restarting the edge. What retired ended (device tokens and
browser sessions presented there) stays ended.

## The setting

One new option, in its own variable so that an older edge image ignores it
after a rollback, as it ignores `AETHER_EDGE_ORIGIN_PAIRS`:

```sh
AETHER_EDGE_RETIRE_PAIRS=https://<relay-host-1> retiring https://<relay-host-2> 2027-03-01
AETHER_EDGE_RETIRE_PAIRS=https://<relay-host-1> retired https://<relay-host-2>
```

`--retire-pairs` / `AETHER_EDGE_RETIRE_PAIRS`: comma-separated entries of
`<relay origin> <retiring|retired> <replacement relay origin> [<date>]`.
A pair is named by its relay origin; its sign-in origin follows. The edge
refuses to start, naming the problem, when an origin is not a relay origin
it serves, the replacement is the same pair or is itself retiring or
retired, a pair appears twice, the state is another word, the date is not
`YYYY-MM-DD`, a date is given with `retired`, or the text a retired pair
must send a server would not fit a WebSocket close reason (123 bytes).
The first pair (`--signin-origin`, `--relay-origin`) can be named like any
other. The edge reads the setting at start; changing it needs a restart,
like every other option.

## Who is on the pair, and what each sees

"Old" is the retiring pair and "new" its replacement. Version facts: a
server serves connectors at more than one address from v0.8.7 (edge
protocol 6); `edge move` exists from v0.9.0.

| # | Who | Learns it while retiring | Moves by | After retired, if not moved | Operator sees |
| --- | --- | --- | --- | --- | --- |
| 1 | Server enrolled at the old relay origin, on a build with this plan | `ready` carries the notice: a warning in the server log at each connect, a `retiring` row in `cloudfield-server edge status`, and for super admins a warning on Settings, Remote access and in Needs you, each with the command | `sudo cloudfield-server edge move <new>` then `sudo systemctl restart aether-server`. `edge status` can exit 3 for about a second after the restart | The control connection is closed with the retired text; `edge status` exits 3 and prints it; nobody reaches the server through the edge. `edge move <new>` still works: it skips the probe of the retired address and keeps the key check | Server id, name, owner, build, last enrolled |
| 2 | The same on an older build (v0.9.0 or later) | Nothing in the product: it ignores the notice. The guide, the release notes and the operator | The same command | The same close text in `edge status` and the log. `edge move` fails at the retired address; the guide gives `cloudfield-server config set edge-url <new>` and a restart | The same, with the build |
| 3 | Server below v0.9.0 (no `edge move`) or below v0.8.7 (connectors at one address only) | Nothing in the product | Update the server, then as 1 | As 2 | The same, with the build |
| 4 | Device signed in at the old pair: CLI | Every answer from the old pair carries `Aether-Edge-Retiring`; the CLI prints one line on stderr per run with both commands | `cloudfield login --edge <new>`, then `cloudfield link --edge <new> <server id>` for each saved link the notice lists | `<old> refused: ... (HTTP 410)` with the same commands. The token presented is ended | Count of devices, by last use |
| 5 | The same in the desktop or Android app, which has no terminal | The local gateway passes the notice on `edge.status`; the dashboard shows it where the signed-in edge is shown, with a "Sign in at `<new host>`" action | That action, then link again | The 410 text in the same place | Counted with 4 |
| 6 | Connector added at `<old>/mcp...` (Claude, ChatGPT, Claude Code and others) | The consent page at the old address; `cloudfield mcp` marks each such connector and the URL to add instead; one extra text block on a tool result, at most once a day per connector | Add the connector again at `<new>/mcp...`, then `cloudfield mcp revoke <id>` for the old one. The grant is bound to the address and is not moved | `410` with the retired text on every connector path; the client shows a failed connection | Count of servers that got connector requests through the old relay origin, by last use. The server's admin sees each connector in `cloudfield mcp` |
| 7 | Browser signed in at the old sign-in origin | A banner on every edge page there, and on the device page a CLI opens | Sign in at the new sign-in origin. Cookies are per host | A page that names the new address as text, with status 410. The session cookie presented is cleared and its session ended | Count of accounts that signed in there, by last use |
| 8 | Links made at the old pair: join links, claim links, QR codes, saved or printed | The join page shows the banner | A new link from the server once it has moved. A claim code lasts minutes and a join link until its invitation expires | The 410 page. An unexpired invitation still works from a link made after the move | Not counted: the edge stores no link |
| 9 | A server enrolled at the new pair that still has connectors at the old one | As 6: its `ready` lists the retiring origin | As 6 | As 6 | As 6 |

Not in the product, in the runbook: DNS, the first GitHub OAuth app, mail,
the website redirect, directory listings and their reviewer sign-in,
monitoring probes of the old host names.

## Mechanism

### Edge to server

`ready` gains one optional field. A server that does not know it ignores
it, and no protocol version changes:

```json
"retiring": [{"origin": "https://<relay-host-1>", "replacement": "https://<relay-host-2>", "date": "2027-03-01"}]
```

It lists every retiring relay origin, so a server knows both that its own
address is retiring and which connector addresses are. `Ready.state` keeps
its two values: an older server refuses any other. A retired origin is no
longer listed in `relay_origins`.

A server enrolling at a retired relay origin has its WebSocket accepted
and closed with a reason that fits 123 bytes:
`this address is retired; the edge is at <new>: see docs/edge-retire.md`.
An HTTP refusal would reach `edge status` on older builds only as a status
code. *Assumption:* the builder confirms this against the previous
server build in the upgrade tests and uses whichever form older builds
print.

The server refuses `edge move` to a retiring or retired address.

### Edge to client

Every API answer and WebSocket handshake answer of a retiring pair carries
`Aether-Edge-Retiring: <replacement relay origin>` and, with a date,
`; date=YYYY-MM-DD`. `GET /v1/edge` carries the same in two optional
fields. The client checks that the value is a valid origin and printable
before printing it, prints it once per run, and changes nothing.

A retired pair answers every path except `/healthz` on both hosts with
`410 Gone`: JSON `{"error": "<old> is retired; this edge is at <new>"}`
for API paths, a static page with the same text for a browser. No
`Location`, no `WWW-Authenticate`, no `Set-Cookie` other than clearing the
edge's own cookies on that host.

### Ending what a retired pair is shown

A client sends a device token only to the pair that issued it, so a token
arriving at a retired host belongs to a device that has not moved. If the
host name is ever lost, its holder would receive those tokens, and a
device token is valid at every pair. So a retired pair ends each device
token and browser session presented to it, as signing out there would,
before answering 410. The server's record of the device and its approval
is untouched; signing in at the new address gives a new token.

Tokens never presented during the retired period stay valid. The runbook
therefore keeps the pair retired, answering, for a stated period before
DNS is removed, and keeps the domain registered afterwards.

### Counting

The edge holds no address in its database today; the three per-address
metrics are in memory and count live connections and requests since
start. New, and only for a pair marked retiring or retired: one table of
last use, `(kind, id, origin, last_used_at)`, written at most once an hour
per row:

| kind | id | Written when |
| --- | --- | --- |
| `server` | server id, with the build it reported | it enrolls at the relay origin |
| `device` | device id | its token is used on either host of the pair |
| `connector` | server id | a connector request for it arrives at the relay origin |
| `browser` | account id | the account signs in at the sign-in origin |

Rows go when their server, device or account is removed, and when the
operator runs the report with `--forget <origin>`. Nothing is recorded for
an active pair, so an edge that never sets the option holds nothing new.
`docs/privacy.md` gains the row. The migration is additive and keeps the
store's compatibility floor.

`aether-edge retiring [--data <dir>] [--origin <relay origin>] [--json]`
prints, per retiring or retired relay origin, the counts of each kind seen
in the last 1, 7 and 30 days and in total, then the servers (id, name,
owner login, build, last enrolled), which `servers list` already shows
apart from the build and time. Devices, accounts and connectors are
counts only.

### Server, CLI and dashboard

- `status.json` records the notice; `edge status` prints
  `retiring     <old> is retiring[ on <date>]; move this server with: sudo cloudfield-server edge move <new>`
  and keeps its exit code.
- `server.edge` returns the notice; the dashboard shows it to super admins.
- The CLI's line: `cloudfield: <old> is retiring[ on <date>]; sign in at its
  new address with: cloudfield login --edge <new>` and, per saved link at
  the old address, `cloudfield link --edge <new> <server id>`.
- The connector's tool-result notice is a second text block after the
  existing one, so a client that reads the first block as JSON is
  unaffected. The `instructions` string is at 491 of its 512 characters
  and is not used.

## Decisions

1. No redirect anywhere in the product. The only safe redirect, the
   website's, is outside it.
2. A retired pair answers 410 with the replacement named; it does not go
   silent until the operator removes it.
3. The date never triggers anything.
4. Counting starts when a pair is marked retiring, not before.
5. A retired pair ends the device tokens and browser sessions presented
   to it.
6. *Assumption:* no in-product mail to server owners. The report gives
   the operator the owner logins the edge already shows.

## For the owner to decide

- Whether `onaether.dev` stays registered after its host names stop
  answering. This plan recommends keeping it: an install that never
  updated still dials it and would hand a later holder a device token.
- Whether the operator may write to owners of servers that have not moved,
  using the addresses the edge holds. The privacy page would have to
  allow it.

## Pull requests

| # | Change | Depends on |
| --- | --- | --- |
| 1 | Edge: the setting, its checks, the `ready` field and its reader in the server's edge agent, the client header and `GET /v1/edge` fields, page banners | none |
| 2 | Edge: retired behaviour, ending presented tokens and sessions, the end-to-end test with two pairs | 1 |
| 3 | Edge: the last-use table, `aether-edge retiring`, `docs/privacy.md` | 1 |
| 4 | Server: log, `edge status`, `server.edge`, dashboard warning, `edge move` to and from a retiring or retired address | 1 |
| 5 | Client: the CLI line, the gateway's `edge.status`, the dashboard notice | 1 |
| 6 | Connector: consent page, `cloudfield mcp`, tool-result notice | 1 |
| 7 | `docs/edge-retire.md`: the guide for people and for edge operators | 1 to 6 |
| 8 | The hosted edge's runbook | 2, 3 |
