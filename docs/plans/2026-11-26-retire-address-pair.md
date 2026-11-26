# Retiring an Address Pair of an Edge

**Goal:** an edge that serves more than one
[address pair](../edge.md#more-than-one-address-pair) can switch one off
without stranding anyone silently: whoever still uses the pair is warned
with the exact command, the edge's operator can count who is left, and the
pair ends with an error that names its replacement.

**Status:** reviewed draft, 2026-11-26. The owner decided the same day to
retire the hosted edge's first pair (`auth.onaether.dev` +
`edge.onaether.dev`) on a date he names later, which reverses
[decision 7](2026-11-15-cloudfield-rename.md#7-repository-names-and-the-old-domain)
of the rename plan for those two host names. A security review and three
adversarial passes on the first draft are folded in
([Review](#review-2026-11-26)). Everything marked *Assumption* is this
plan's choice and stands unless the owner changes it.

## Rules

- Nothing changes for any edge until its operator sets a new option. No
  date, host name or state is built in. An edge with one pair cannot use
  the options, and records nothing new.
- A date is text shown to people. The edge never acts on it: only the
  operator's own edit moves a pair to the next state.
- No request is redirected to another origin, on any path, in any state.
  Sign-in, token, callback, join and connector paths carry codes or tokens,
  and the edge client refuses redirects
  (`internal/edge/client/edgeclient.go`). A retired pair answers with an
  error that names the replacement, and the person or program starts again
  there.
- Key pinning, grant issuer and audience checks, connector token binding,
  origin checks, CSRF and `__Host-` cookie scope stay as they are. Nothing
  issued at one pair is accepted at another to make a move easier.
- A server acts on a notice only when the edge key it pinned signed it. A
  client pins no edge key, so a client shows a notice as text and never
  signs in, links or changes its configuration because of one.
- An older edge image must fail closed: after a rollback a retired pair is
  not served, and never comes back into service.

## States

| State | Set by | What the pair does |
| --- | --- | --- |
| active | default | As today |
| retiring | `--retiring-pairs`, with an optional date | Everything works. The edge tells servers, clients and browsers that the pair is retiring and names the replacement |
| retired | the operator takes the pair out of the served pairs and names it in `--retired-pairs` | Every request on either host gets `410` naming the replacement. Nothing is relayed, signed in or issued. Device tokens and browser sessions that were issued there are deleted |
| removed | the operator deletes the entry and the DNS records | The host names answer nothing. No product state |

retiring to active and retired to retiring are undone by editing the
options and restarting the edge. Device tokens and browser sessions
deleted at retired stay deleted.

## The options

```sh
AETHER_EDGE_RETIRING_PAIRS=https://<relay-host-1> https://<relay-host-2> 2027-03-01
AETHER_EDGE_RETIRED_PAIRS=https://<signin-host-1> https://<relay-host-1> https://<relay-host-2>
```

`--retiring-pairs` / `AETHER_EDGE_RETIRING_PAIRS`: comma-separated entries
of `<relay origin> <replacement relay origin> [<YYYY-MM-DD>]`. Both must
be relay origins of pairs the edge serves. It is a notice only, so an
older image that ignores it loses the notice and nothing else.

`--retired-pairs` / `AETHER_EDGE_RETIRED_PAIRS`: comma-separated entries
of `<sign-in origin> <relay origin> <replacement relay origin>`. The first
two must not be origins of a served pair: the operator removes the pair
from `--origin-pairs` in the same edit, so an older image, which ignores
this option, answers `421` on those hosts. An image with this plan keeps
certificates for retired hosts and answers them before it looks for a
served pair; behind a proxy their `server` blocks and certificates stay
until the pair is removed. A retired pair needs no GitHub OAuth app. At
most 16 retired pairs.

The edge refuses to start, naming the entry and the fix, when an origin
does not parse, a retiring origin or a replacement is not a served relay
origin, a replacement is the entry's own pair or is itself retiring, a
retired origin is still served, an origin appears twice, or a date is not
a real `YYYY-MM-DD` date. Origins are compared in canonical form.

The first pair (`--signin-origin`, `--relay-origin`,
`--github-client-id`) is required, so it cannot be taken out of the
served pairs. To retire it the operator first swaps: the replacement
becomes the first pair and the old one an `--origin-pairs` entry. No pair
is preferred, so the swap changes only the order of `relay_origins`. The
edge reads options at start only.

## Who is on the pair, and what each sees

"Old" is the retiring pair and "new" its replacement. A server serves
connectors at more than one address from v0.8.7 (edge protocol 6);
`edge move` exists from v0.9.0.

| # | Who | Learns it while retiring | Moves by | After retired, if not moved | Operator sees |
| --- | --- | --- | --- | --- | --- |
| 1 | Server enrolled at the old relay origin, on a build with this plan | A signed notice in `ready`: a warning in the server log at each connect, a `retiring` row in `cloudfield-server edge status`, and for super admins a warning on Settings, Remote access and in Needs you, each with the command | On the server's host: `sudo cloudfield-server edge move <new>`, then `sudo systemctl restart aether-server`. `edge status` can exit 3 for about a second after the restart | `edge status` exits 3 with `dial <old>/v1/server/control: 410 Gone: <text>` and the stored notice's command. Nobody reaches the server through the edge. `edge move <new>` works | Server id, name, owner, build, last enrolled |
| 2 | The same on an older build with `edge move` (v0.9.0 or later) | Nothing in the product: it ignores the notice. The guide, the release notes, the operator | The same command | The same `410 Gone: <text>` in `edge status` and the log. `edge move` is refused at the retired address; the guide gives `sudo cloudfield-server config set edge-url <new>` and a restart, which keeps the pinned key | The same, with the build |
| 3 | Server below v0.9.0 (no `edge move`; the binary is `aether-server`) or below v0.8.7 (connectors at one address only) | Nothing in the product | Update the server, then as 1 | As 2 | The same, with the build |
| 4 | Device signed in at the old pair: CLI | Every answer from the old pair carries `Aether-Edge-Retiring`; the CLI prints the notice on stderr once per run, with the commands | `cloudfield login --edge <new>`; `cloudfield link --edge <new> [--name <name>] [--addr <addr>] <server id>` for each saved link at the old address; `cloudfield daemon install` again where the sync daemon was installed from such a link; then `cloudfield logout --edge <old>` | `<old> refused: <text> (HTTP 410)`. `logout --edge <old>` then removes the local token | Count of devices issued at the old sign-in origin, by last use |
| 5 | The same on an older CLI or app | Nothing | As 4 | The same 410 text on the CLI; the older dashboard shows only `<edge> refused the connection to <server>` | Counted with 4 |
| 6 | The same in the desktop or Android app, on a build with this plan | The local gateway asks `GET /v1/edge` for each signed-in edge and reports the notice on `edge.status`; the dashboard shows it beside the signed-in edge, as text, with the new address filled into the existing sign-in field for the person to confirm | Sign in at the new address there, then link again | The 410 text in the same place | Counted with 4 |
| 7 | Connector added at `<old>/mcp...` (Claude, ChatGPT, Claude Code and others) | The consent page at the old address; `cloudfield mcp` marks each of the member's connectors at the old address and prints the URL to add instead; one extra text block on a tool result, at most once a day per connector | Add the connector again at `<new>/mcp...`, then `cloudfield mcp revoke <id>`. The grant is bound to the address and is not moved | `410` with the retired text on every connector path; the client shows a failed connection. `cloudfield mcp` marks the connector retired | Count of servers that got connector requests through the old relay origin, by last use |
| 8 | Browser signed in at the old sign-in origin | A banner on every edge page there, the device page included | Sign in at the new sign-in origin. Cookies are per host | A page with status 410 that names the new address as text and clears the edge's cookies on that host | Count of accounts with a session issued there, by last use |
| 9 | Links made at the old pair: join links, claim links, saved or printed | The join page shows the banner | A link the server makes after it has moved. A claim code lasts 30 minutes and an invitation 7 days | The 410 page: open the same link with the new host name in place of the old one, or ask for a new invitation | Not counted: the edge stores no link |
| 10 | A server enrolled at the new pair with connectors at the old one | As 7: its `ready` carries the notice for the old origin | As 7 | As 7 | As 7 |

Outside the product, in the runbook and in order there: the updater's
`UPDATE_RELAY_URL` and the monitoring probes, the directory listings and
their reviewer sign-in (the sister change moves `packaging/`), the OpenAI
domain challenge, DNS, the first GitHub OAuth app, mail, the website
redirect.

## Mechanism

### Notices to servers

`ready` gains one optional field, which an older server ignores. No
protocol version changes and `Ready.state` keeps its two values:

```json
"notices": [{"state": "retiring", "origin": "https://<relay-host-1>", "replacement": "https://<relay-host-2>", "date": "2027-03-01", "signature": "<base64>"}]
```

It lists every retiring and retired relay origin, so a server knows that
its own address is retiring and which connector addresses are retiring or
retired. The edge signs each entry with its key over a domain string, the
server id, the handshake nonce, and the entry's fields, under a signing
context of its own so that a notice never verifies as a grant or an
assertion. The server keeps an entry only when the signature verifies
against the pinned key and drops any other without refusing the `ready`.
It stores the kept entries per pinned key, and each `ready` replaces the
whole set, an empty one included, so a notice the edge stops sending is
gone at the next connect and none survives `edge trust`. What the server
shows comes from that store. A first `ready` pins the key it arrives
with, so its notices are trusted as the pin is: on first use.

Someone with DNS and a certificate for a relay host and the edge's public
key can complete a handshake and send no notices, which hides a warning
until the next real connect. It cannot add or change one.

A retired relay origin is no longer in `relay_origins`, so a server
refuses connector requests said to come from it.

A retired host answers the control path like every other path: `410` with
the JSON error, before any handshake and without touching the server's
registration. Every edge-capable build prints a refused dial's error text
in `edge status` and retries with a backoff that reaches a minute.

`edge move`, on a build with this plan:

- refuses a target that a verified notice names as retiring or retired;
- when the address it is leaving answers `410`, skips that probe, and at
  the target requires the pinned key, edge protocol 6, the target among
  the announced origins, and in that probe's own `ready`, never from the
  store, a verified `retired` notice whose origin is the address it is
  leaving and whose replacement is the target. With no key pinned it
  prints `cloudfield-server config set edge-url <new>` and the restart
  instead;
- prints no `to move back` line and no "links and connectors made there
  keep working" for a retired address.

`edge status` prints the `retiring` row only when the running server is at
the address `edge-url` names; otherwise the existing `running` row is the
one to follow. It cleans control characters from the stored error text.

### Notices to clients

Every API answer and WebSocket handshake answer of a retiring pair carries
`Aether-Edge-Retiring: <replacement relay origin>` and, with a date,
`; date=YYYY-MM-DD`; `GET /v1/edge` carries the same in optional fields.
The client checks that the value is a canonical origin and a real date,
prints it as text, and does nothing else.

A retired pair answers every path except `/healthz`, on both hosts, with
`410 Gone` and no `Location` or `WWW-Authenticate`. The only cookies it
sets are the edge's own `__Host-` cookies with `Max-Age=0`, on the
browser page:

```json
{"error": "<old> is retired; this edge is at <new>. Move a server with: cloudfield-server edge move <new>. Sign a device in with: cloudfield login --edge <new>", "retired": {"replacement": "<new>"}}
```

A browser gets a static page with the same text. On a 410 that carries
`retired`, `logout --edge <old>` removes the local token and prints that
the edge did not confirm it, with the device id and where to revoke it:
the Devices page at the new address. A 410 without the field keeps the
token, as any other refusal does today.

A client cannot authenticate a notice, and after a pair is removed the
host name may answer for someone else. So the client labels it as the old
address's own statement and offers a command or a filled-in field only
when it already has reason to know the replacement: it holds a token
there, a saved link names it, it is the client's built-in default edge,
or, in the dashboard, the linked server's verified notice names the same
address. Otherwise it shows the text and says to confirm the address with
whoever runs the edge.

### Device tokens and browser sessions

A device token has no expiry and is valid at every pair, and the edge
records no address for it today. A client sends its token only to the pair
that issued it, and every sign-in makes a new device row. So:

- `devices` and `web_sessions` each gain a nullable column for the sign-in
  origin they were issued at, written at issue on an edge that serves
  more than one pair or lists a retired one. A row without one is given
  the origin of the pair it is next used at, on the API and on the relay
  path alike.
- At start, the edge deletes the device tokens and sessions whose origin
  is a retired sign-in origin, as signing out there would. The server's
  record of the device and its approval is untouched.
- `aether-edge devices end --no-address` ends the tokens that name no
  address: those not used since the columns arrived. A device it ends
  signs in again. An edge that starts with a retired pair and such rows
  logs their count and this command at each start.

After that, nothing a later holder of the old host names could receive
from a device or browser is valid at the edge. Connector tokens are held
by servers, bound to the old URL, and reach a server only through an edge
that serves the old origin, which no image does once the pair is out of
the served pairs.

A join link's secret is in the URL fragment, which the join page's script
reads. The runbook keeps the retired pair answering for at least the
invitation lifetime of 7 days after the last server left it, and
recommends keeping the domain registered.

### Counting

The edge holds no address in its database today; the three per-address
metrics are in memory and restart at zero. New, in one additive migration
that keeps the store's compatibility floor (an older binary can ignore
every addition, and rows go with their parent by foreign key):

| What | Where | Written |
| --- | --- | --- |
| The relay origin a server last enrolled at, when, and the build it reported | columns on `servers` | at enrollment, which already writes the row |
| The sign-in origin a device token or session was issued at | columns above | at issue, or at next use |
| The last connector request for a server through a relay origin | a table keyed by server and origin | at most once an hour per row |

These are written only on an edge that serves more than one pair or
lists a retired one, and then for every pair, so the counts exist before
a pair is marked retiring. `docs/privacy.md` gains the rows, and says
that a backup holds them. An unclaimed server has no row and is counted
from the live relay registration only (`aether_edge_servers_by_origin`).

`aether-edge retiring [--data <dir>] [--origin <relay origin>] [--json]`
prints for an origin: servers last enrolled there (id, name, owner login
or none, build, when), and counts by last use in 1, 7 and 30 days and in
total of device tokens issued there, accounts with a session issued
there, accounts with a device there and none at another origin, and
servers with connector requests through it. A server that moved shows at
its new origin only. `aether_edge_retired_requests_total{origin,kind}`
counts what still arrives at a retired host, by server, client, connector
and browser paths, with no ids.

### CLI, gateway, dashboard, connector

- CLI, once per run on stderr:
  `cloudfield: <old> says it is retiring[ on <date>] and that its new address is <new>.`
  then, when the replacement is known as above, `Sign in there with:
  cloudfield login --edge <new>` and the link, daemon and logout commands
  of row 4 for what this machine holds; otherwise `Confirm that address
  with whoever runs the edge before you sign in there.`
- The gateway caches each signed-in edge's `GET /v1/edge` notice for
  `edge.status`. The dashboard never starts a sign-in from a notice.
- The connector reads only the server's verified notices. The consent
  page shows a static warning. `connector.list` returns the retiring and
  retired origins so `cloudfield mcp` can mark connectors. The tool-result
  notice is added in `internal/connector` as a second text block after the
  unchanged first one, in a fixed template addressed to the user:
  `Notice for the user: this connector was added at <old>/mcp, an address
  that is retiring[ on <date>]. It must be added again at <new>/mcp.`
  The `instructions` string is at 493 of its 512 characters and is not
  used.

## Decisions

1. No redirect anywhere in the product.
2. A retired pair answers 410 naming the replacement until the operator
   removes it.
3. The date never triggers anything.
4. Retired is a pair taken out of service and listed apart, so a rollback
   cannot bring it back.
5. Servers act only on notices signed by the pinned edge key. Clients
   show notices as text.
6. Device tokens and sessions carry the address that issued them and are
   deleted when it is retired.
7. Counting is on for an edge with more than one pair and holds no more
   than an address, a time and a build per row the edge already has.
8. *Assumption:* no in-product mail to server owners. The report gives the
   operator the owner logins the edge already shows.

## For the owner to decide

- Whether `onaether.dev` stays registered after its host names stop
  answering. This plan recommends keeping it: the steps above remove what
  a later holder could use, but an install that never updated still dials
  it, and a printed join link still opens it.
- Whether the operator may write to owners of servers that have not moved,
  using the addresses the edge holds. The privacy page would have to
  allow it.

## Pull requests

Each carries its tests and the documentation of what it changes.

| # | Change | Depends on |
| --- | --- | --- |
| 1 | Edge and protocol: `--retiring-pairs`, signed notices in `ready` and their verified store in the server's edge agent, the client header and `GET /v1/edge` fields and their parser in the edge client, page banners | none |
| 2 | Edge store: the migration, its writers, `aether-edge retiring`, `devices end`, `docs/privacy.md` | none |
| 3 | Edge: `--retired-pairs`, the 410 answers, `retired` notices, deleting tokens and sessions of a retired origin, the metric, the end-to-end test with two pairs that retires one | 1, 2 |
| 4 | Server: log, `edge status`, `server.edge`, the dashboard warning, `edge move` to and from a retiring or retired address | 1 |
| 5 | Client: the CLI lines, `logout` at a retired pair, the gateway's `edge.status`, the dashboard notice | 1 |
| 6 | Connector: consent page, `connector.list` and `cloudfield mcp`, the tool-result notice | 1 |
| 7 | `docs/edge-retire.md`: the guide for people and for edge operators | 1 to 6 |
| 8 | The hosted edge's runbook | 2, 3 |

## Review (2026-11-26)

What the reviews changed in this plan:

- One option that an older image ignores would have let a rollback serve
  a retired pair in full again. Retired is now a pair removed from the
  served pairs.
- Burning tokens as they are presented left every token that was never
  presented valid for good. Tokens and sessions now carry their address.
- A notice named an address nobody had authenticated. Server-side notices
  are signed; the desktop app no longer signs in from one.
- `edge move` from a retired address also failed at the target, which no
  longer announces the address left. The signed `retired` notice replaces
  that check.
- A refused HTTP upgrade does reach `edge status` on every build, so the
  control path answers 410 like the rest.
- The last-use table could not be cleaned by an older binary. Columns and
  a table with foreign keys replace it.
- The gateway's `edge.status` makes no request today, so the notice had
  no source there.
- Added: older CLIs and apps, named links and `--addr`, the sync daemon,
  `logout`, the updater's `UPDATE_RELAY_URL`, the first pair.

A second security review of the revised plan added: certificates and
routing for retired hosts, per-key replacement of the server's notice
store, `edge move` reading the notice from its own probe, no command or
filled-in field from an unauthenticated client notice unless the address
is already known, `logout` wording, and no new records on an edge with
one pair.

Not changed: `FormerURL` stays in the client's choice of a hosted edge
until the hosted pair is removed, since it only orders addresses a
machine already uses. Connector grants bound to a retired address are
left for the member to revoke, because deleting them on a notice could
not be undone after an operator's mistake.
