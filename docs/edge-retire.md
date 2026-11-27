# Moving off a retiring edge address

- An **edge** is the relay Cloudfield servers and clients dial out to
  ([edge.md](edge.md)); an **address pair** is one sign-in host and one
  relay host of it, and an edge can serve several.
- A **retiring** pair works in full and says where to go: its edge pages
  show `This address of the edge is retiring on <date>. Its new address is
  <address>`, every answer carries the header `aether-edge-retiring: <new
  relay origin>; date=<date>`, and TODO(after #1132): `sudo
  cloudfield-server edge status` shows a `retiring` row.
- A **retired** pair answers every request but `/healthz` with `410 Gone`
  naming the new address.
- Nothing moves by itself: a person moves each server, device and connector
  before the operator retires the pair.

The examples use `auth-old.example.com` + `edge-old.example.com` for the
retiring pair and `auth-new.example.com` + `edge-new.example.com` for its
replacement. Use the addresses your edge's operator gives you.

## Moving a server

This is for the administrator of a `cloudfield-server` host whose `edge-url`
is the old relay origin. Moving it changes the address it enrolls at; the
pinned edge key, the owner, members and devices stay
([edge.md](edge.md#moving-to-another-address-of-the-edge)).

Check where the server is, which build it runs, and what the edge says about
the address:

```sh
grep edge-url /etc/aether/server.conf
sudo cloudfield-server edge status
cloudfield-server version
curl -fsS -H 'aether-edge-version: 6' https://edge-old.example.com/v1/edge | jq -r '.retiring, .retiring_date'
```

The server is on the old pair when the first prints `edge-url =
https://edge-old.example.com` and the status prints `edge
https://edge-old.example.com`. The version line is `aether-server <version>`.
The last command prints the replacement and the date, or `null` twice when
the address is not retiring.

What to do depends on the version:

| Version | Do this |
| --- | --- |
| v0.9.0 or later | Steps 2 to 4 |
| v0.8.7 or later, below v0.9.0 | Step 1, then 2 to 4. `edge move` does not exist yet: v0.8.7 answers `aether-server: usage: aether-server edge <status\|claim-code [--admin <member id>]\|trust\|leave> [--config <file>]` |
| Below v0.8.7 | Step 1, then 2 to 4. Until it is updated the server serves connectors only at the address it is enrolled at, so a connector added at the new address is refused with `invalid_target`, `resource must be https://edge-old.example.com/mcp` |

Below v0.9.0 the command is `aether-server`: run `aether-server version`
where `cloudfield-server` does not exist yet.

1. Update the server ([install.md](install.md#update-the-server)):

   ```sh
   sudo aether-server update
   ```

2. Move it. The server keeps running:

   ```sh
   sudo cloudfield-server edge move https://edge-new.example.com
   ```

   ```
   enrolled at https://edge-new.example.com, an address of the same edge (edge key SHA256:<fingerprint>, still pinned); the edge announces https://edge-old.example.com too, so links and connectors made there keep working
   /etc/aether/server.conf: edge-url = https://edge-new.example.com (was https://edge-old.example.com)
   apply it with:
     sudo systemctl restart aether-server
   to move back:
     sudo aether-server edge move https://edge-old.example.com
   ```

   Links and connectors made at the old address keep working only until the
   pair is retired. A refusal prints one line, exits 1 and writes nothing;
   the refusals are listed in [edge.md](edge.md#moving-to-another-address-of-the-edge).

3. Apply it. The restart ends every relayed connection until the server is
   back:

   ```sh
   sudo systemctl restart aether-server
   ```

4. Check it. `edge status` exits 0 once the server is connected:

   ```sh
   timeout 300 sh -c 'until sudo cloudfield-server edge status >/dev/null; do sleep 5; done'
   sudo cloudfield-server edge status
   ```

   Expect `edge         https://edge-new.example.com`, connector URLs under
   `https://edge-new.example.com/mcp`, the same `edge key` and `owner` as
   before, `connection   connected since <time>; the edge last answered <n>s
   ago`, and no `running` line.

Then hand out new links ([Links](#links)), and tell members to move their
devices and connectors ([Moving a device](#moving-a-device),
[Moving a connector](#moving-a-connector)).

### If the address is retired before the server moved

`sudo cloudfield-server edge status` exits 3 with:

```
connection   disconnected since <time>: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com
```

The journal (`sudo journalctl -u aether-server`) logs `edge: control
connection ended; reconnecting` with the same error, and retries. Nobody
reaches the server through the edge; member SSH keys and tailnet access are
not affected.

`edge move` from a retired address fails, because it enrolls at the old
address first:

```
aether-server: edgeagent: enroll at https://edge-old.example.com: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com
```

TODO(after #1132): `edge move` accepts a retired address when the new
address's own answer carries the edge's signed notice that the old one is
retired in its favour.

Move it by hand instead. First check that the new address presents the
edge key the server pinned; both commands must print the same
`SHA256:` fingerprint:

```sh
sudo cloudfield-server edge status | grep 'edge key'
curl -fsS -H 'aether-edge-version: 6' https://edge-new.example.com/v1/edge | jq -r .fingerprint
```

If they differ, another edge answers there: stop and ask the edge's
operator. If they match:

```sh
sudo cloudfield-server config set edge-url https://edge-new.example.com
sudo systemctl restart aether-server
```

```
/etc/aether/server.conf: edge-url = https://edge-new.example.com (was https://edge-old.example.com)
apply it with:
  sudo systemctl restart aether-server
```

Check it as in step 4. The server keeps its pin and its owner, which it holds
per edge key, not per address.

## Moving a device

This is for anyone who signed in to the edge at the old address: the
`cloudfield` CLI, the desktop app, or the Android app. A **device** is one
client install, with its own key and a device token from the edge. A device
token never expires and is sent only to the address that issued it.

Check where this computer is signed in and linked (Linux paths; macOS and
Windows are in [install.md](install.md#client-files-and-variables)):

```sh
jq -r '.edges | keys[]' ~/.config/aether/edge-tokens.json
grep -n -e edge_url -e server_id ~/.config/aether/config.json
```

The first prints each relay origin this computer holds a token for; the
second each saved link through an edge and its server id. Look for
`https://edge-old.example.com`.

TODO(after #1133): the CLI prints, once per run on stderr, that the address
is retiring and the commands below for what this computer holds.

1. Sign in at the new address, and confirm the code in the browser:

   ```sh
   cloudfield login --edge https://edge-new.example.com
   ```

   ```
   auth-new.example.com signs you in for the edge edge-new.example.com
   open https://auth-new.example.com/device and enter the code <code>
   waiting for you to confirm the code...
   signed in to edge-new.example.com as <login>; this device is "<label>"
   ```

2. Link each server again that a saved link reaches through the old address,
   with the same name and direct address it had:

   ```sh
   cloudfield link --edge https://edge-new.example.com [--name <name>] [--addr <host:port>] <server id>
   ```

3. Where the sync daemon was installed from such a link, install it again
   with the same flags, then restart it
   ([install.md](install.md#the-client-side-sync-daemon)):

   ```sh
   grep edge-old.example.com ~/.config/systemd/user/aether-daemon.service
   cloudfield daemon install --repo <path>
   systemctl --user daemon-reload && systemctl --user restart aether-daemon
   ```

4. Sign out at the old address while it still answers, so its token is
   revoked there:

   ```sh
   cloudfield logout --edge https://edge-old.example.com
   ```

   ```
   signed out of edge-old.example.com: the device token is revoked at the edge and deleted here; the device key and your links stay
   ```

Check it: the `jq` command above prints only `https://edge-new.example.com`,
and `cloudfield servers --edge https://edge-new.example.com` lists your
servers.

The desktop app uses the same token file and links as the CLI on that
computer, so the steps above move it too. TODO(after #1133): the desktop and
Android apps show the notice beside the signed-in edge, with the new address
filled into the sign-in field for you to confirm. How to point the Android
app at another address of the edge is not documented yet.

### If the address is retired before the device moved

When the operator retires the pair, the edge deletes the device tokens and
browser sessions issued there. Every command at the old address is refused,
for example:

```
aether: list servers: auth-old.example.com refused: https://auth-old.example.com is retired; this edge is at https://edge-new.example.com. Move a server with: cloudfield-server edge move https://edge-new.example.com. Sign a device in with: cloudfield login --edge https://edge-new.example.com (HTTP 410)
```

A connection to a server ends in `connect to server <server id>:
edge-old.example.com refused: https://edge-old.example.com is retired; this
edge is at https://edge-new.example.com (HTTP 410)`. The dashboard shows
`<edge> refused the connection to <server>`. Run steps 1 to 3.

`cloudfield logout --edge https://edge-old.example.com` is refused the same
way and keeps the local entry, ending `; the token is still valid and kept in
<config dir>/edge-tokens.json so you can retry`. The edge deleted that token
when it retired the pair. TODO(after #1133): on a `410` that names the
replacement, `logout` removes the local token and says the edge did not
confirm it, with the device id and the Devices page at the new address where
to revoke it.

## Moving a connector

This is for a member who added Cloudfield to ChatGPT, Claude, Claude Code or
another MCP client at `https://edge-old.example.com/mcp` or
`https://edge-old.example.com/mcp/<server id>` ([connectors.md](connectors.md)).
A connector's grant and tokens are bound to the URL it was added at, so it
cannot be moved: you add it again at the new address and revoke the old one.

Check: the app's connector settings show the URL you added. In the
dashboard, **Settings → ChatGPT and Claude** lists each of your connectors
with the URL it was added at. `cloudfield mcp` prints `at <url>` for a
connector at a server's own URL and `universal URL` for one at `/mcp`,
without the address. TODO(after #1131): `cloudfield mcp` marks each connector
at a retiring or retired address and prints the URL to add instead, with the
same path; the consent page at the old address shows a warning; a tool
result carries `Notice for the user: this connector was added at <old>/mcp,
an address that is retiring[ on <date>]. It must be added again at
<new>/mcp.`, at most once a day per connector.

The server must be v0.8.7 or later ([Moving a server](#moving-a-server)).

1. Add the connector again at the same path on the new host, such as
   `https://edge-new.example.com/mcp`, as in
   [connectors.md](connectors.md#add-it-in-chatgpt). For a server's own URL,
   `cloudfield mcp code` prints the code it asks for. In Claude Code:

   ```sh
   claude mcp add --transport http aether https://edge-new.example.com/mcp
   ```

   (Remove the old entry first with `claude mcp remove aether` if it has the
   same name.)

2. Check that it works: `cloudfield mcp` lists it under `Connected:` with
   `last used` after you call a tool from the app.
3. Remove the old connector in the app. Removing it there does not revoke it
   on the server.
4. Revoke the old one on the server, with the id `cloudfield mcp` lists:

   ```sh
   cloudfield mcp revoke <old connector id>
   ```

   ```
   revoked connector <old connector id>, <client name> (<client host>); its tokens no longer work
   ```

### If the address is retired before the connector moved

Every connector path at the old address answers `410 Gone` with:

```json
{"error":"https://edge-old.example.com is retired; this edge is at https://edge-new.example.com. Move a server with: cloudfield-server edge move https://edge-new.example.com. Sign a device in with: cloudfield login --edge https://edge-new.example.com","retired":{"replacement":"https://edge-new.example.com"}}
```

The app shows its own error for a failed connection. The server no longer
accepts a token issued at the old address. Add the connector again and
revoke the old one as above; the old one also stops being listed once its
refresh token is unused for 30 days.

## Links

Nothing made at the old address carries over once it is retired:

- **Join links** (invitations) and **claim links** name the address they were
  made at. Once the server has moved, issue new ones there:
  `sudo cloudfield-server edge claim-code` for a claim link, valid 30
  minutes, and `cloudfield invite` for an invitation, valid 7 days
  ([teams.md](teams.md#through-an-edge)). At a retired address a join link
  opens a page that says `This link was made at the retired address and no
  longer works. Ask whoever sent it for a new one.`
- **Browser sessions** on the edge's pages are per host: sign in at the new
  sign-in host. At a retired host, a browser gets a page with status 410,
  `This address is retired`, naming the new address and the two commands
  above, and the edge's cookies on that host are cleared.

## For edge operators

You run `aether-edge` and serve a second pair (`--origin-pairs`,
[edge.md](edge.md#more-than-one-address-pair)) that replaces the old one.
Set both options in the environment file, never as flags: an older image
ignores an unknown variable and refuses an unknown flag. Every refusal below
is printed as `aether-edge: <text>`, and the edge does not start.

```sh
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01
AETHER_EDGE_RETIRED_PAIRS=https://auth-old.example.com https://edge-old.example.com https://edge-new.example.com
```

`--retiring-pairs` is `<relay origin> <replacement relay origin>
[<YYYY-MM-DD>]`, both served relay origins; the date is only shown.
`--retired-pairs` is `<sign-in origin> <relay origin> <replacement relay
origin>`, at most 16 entries; the first two must no longer be served.
Entries are comma-separated. Every check is in
[edge.md](edge.md#retiring-an-address-pair).

### Order

1. Mark the pair retiring and restart. The edge logs `aether-edge: retiring
   relay=https://edge-old.example.com replacement=https://edge-new.example.com
   date=2027-03-01`. From this start it records which pair each server,
   device token and session uses. A device sign-in pending at that restart
   is deleted and its person runs `cloudfield login` again.
2. Wait while people move, at least 30 days: browser sessions end after 30
   days unused, and device tokens never expire, so the retired start below
   signs out every device not used during the wait. Read who is left with
   `aether-edge retiring`.
3. If the old pair is the first pair (`--signin-origin`, `--relay-origin`,
   `--github-client-id`), swap: make the new pair the first, move the old
   one into `--origin-pairs`, keep `--retiring-pairs`, restart. The first
   pair cannot be taken out; retiring it without the swap is refused with
   `--retired-pairs: https://auth-old.example.com is still served as
   --signin-origin or --relay-origin; first make its replacement the first
   pair (--signin-origin, --relay-origin, --github-client-id) and remove this
   pair`.
4. Retire it in one edit: remove the pair from `--origin-pairs`, add its
   `--retired-pairs` entry, remove its `--retiring-pairs` entry, restart.
5. Keep the `--retired-pairs` entry for good, after the DNS records are gone
   too.

Skip step 1 and step 4 refuses, when the database holds any sign-in with no
recorded origin:

```
aether-edge: edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens <n>, browser sessions <n>, pending device sign-ins <n>), since this edge has never recorded which pair issued them. Serve the pair again (in --origin-pairs, with --retired-pairs unset) and run it with --retiring-pairs naming the pair, for as long as you give people to move, such as 30 days: each token or session used in that time is recorded and kept. A shorter run lets this start pass but signs out everyone who did not use the edge during it
```

Other refusals at step 4:

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth-old.example.com is still served; remove its pair from --origin-pairs in the same edit` | Remove the pair from `--origin-pairs` |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove the `--retiring-pairs` entry once only one pair is served |
| `--retired-pairs: https://edge-new.example.com, the replacement of https://edge-old.example.com, is retiring; name a pair that stays` | Name a replacement that is not retiring |
| `--retired-pairs names <n> pairs; an edge keeps at most 16` | At most 16 entries |
| `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: retire this pair once fewer pairs are retiring or retired` | Fewer entries in both options together |

### Who is left

```sh
sudo -u aether-edge aether-edge retiring --origin https://edge-old.example.com
```

```
last known recording period: since <time>, last started <time>; the edge is recording now, or stopped uncleanly

relay origin https://edge-old.example.com, sign-in origin https://auth-old.example.com

SERVER ID  NAME  OWNER LOGIN  BUILD  LAST ENROLLED OR CLAIMED
<id>       <name>  <login>    <build>  <time>

LAST USED WITHIN                                                      1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens issued here                                             <n>    <n>     <n>      <n>
device tokens issued earlier, first seen here                         <n>    <n>     <n>      <n>
accounts with a session here                                          <n>    <n>     <n>      <n>
accounts with device tokens here and none recorded at another origin  <n>    <n>     <n>      <n>
servers with connector requests through here                          <n>    <n>     <n>      <n>

unknown origin: <n> claimed servers not enrolled or claimed since recording began

LAST USED WITHIN         1 DAY  7 DAYS  30 DAYS  TOTAL
device tokens            <n>    <n>     <n>      <n>
accounts with a session  <n>    <n>     <n>      <n>
```

Each server row is a claimed server whose last enrollment or claim was at
this relay origin; its owner login is whom to tell. `unknown origin` counts
what has not been used since recording began: step 4 deletes those device
tokens and sessions, and those servers stay on whatever address they dial.
Every report ends with:

```
An empty list is not proof that nobody is left: the edge records only while an
option that retires a pair is set; a server that has not enrolled or been
claimed since, and a device token or session not used since, have no origin;
an older image run in between records nothing; and an unclaimed server has no
row at all (aether_edge_servers_by_origin counts those connected now).
Connector counts are requests the edge sent on to a connected, claimed server,
which the server may still have refused; requests the edge refused are not.
```

`--json` prints the same as JSON ([edge.md](edge.md#monitoring)).

### Metrics

On `/metrics` (`--metrics-listen`, loopback), counted since the edge last
started:

- `aether_edge_servers_by_origin{origin}`: servers connected now, by the relay
  origin they enrolled at, unclaimed ones included.
- `aether_edge_device_logins_total{origin}`: device sign-ins by sign-in origin.
- `aether_edge_connector_requests_total{origin,protocol}`: connector requests
  by relay origin and the server's edge protocol, `0` when it was not
  connected.
- `aether_edge_retired_requests_total{origin,kind}`: requests to a retired
  host, by `kind` `server`, `client`, `connector` or `browser`; exported
  while `--retired-pairs` is set.

### The swap for the first pair

```sh
# before
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-old.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-old.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<old-client-id>
AETHER_EDGE_ORIGIN_PAIRS=https://auth-new.example.com https://edge-new.example.com <new-client-id> <new-secret-file>
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01

# after the swap (step 3)
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-new.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-new.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<new-client-id>
AETHER_EDGE_ORIGIN_PAIRS=https://auth-old.example.com https://edge-old.example.com <old-client-id> <old-secret-file>
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01

# after retiring (step 4)
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-new.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-new.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<new-client-id>
AETHER_EDGE_RETIRED_PAIRS=https://auth-old.example.com https://edge-old.example.com https://edge-new.example.com
```

The first pair's secret moves with it: under the packaged unit it is the
credential `github-client-secret` (`/etc/aether-edge/github-client-secret`),
so put the new app's secret there and the old app's in the file the
`--origin-pairs` entry names. No pair is preferred, so the swap changes only
the order of the relay origins sent to servers.

### At the retired start

Once its listeners are bound and before it serves, the edge deletes in one
transaction the device tokens, browser sessions and pending device sign-ins
issued at the retired sign-in origin, and those with no recorded origin on
every pair, then logs:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth-old.example.com relay=https://edge-old.example.com replacement=https://edge-new.example.com
```

A deleted device signs in again; servers keep their record of it and its
approval. Deleted rows stay deleted if you remove the option. Check from
outside:

```sh
curl -sS -i https://edge-old.example.com/v1/edge
```

It answers status `410`, `Cache-Control: no-store` and the JSON error shown
under [Moving a connector](#moving-a-connector). `/healthz` still answers
`ok`, so a probe of `/healthz` does not show the retirement.

### Rollback

Across a retirement, roll back the image only, never the database. A
database restored from a backup taken before the retirement holds the
deleted tokens again: an image with `--retired-pairs` deletes them again at
start, and an older image accepts them at every served pair. With
`aether-edge-update`, that rules out `--rollback --restore-backup` to a
release from before the retirement ([edge.md](edge.md#automatic-updates)).

An image older than v0.8.7 reads only the `AETHER_EDGE_` names, so the edge
refuses a retired host that only a `CLOUDFIELD_EDGE_` name hides:

```
aether-edge: AETHER_EDGE_SIGNIN_ORIGIN names https://auth-old.example.com, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN
```

The same is refused for `AETHER_EDGE_RELAY_ORIGIN` and
`AETHER_EDGE_ORIGIN_PAIRS`, and for a data directory set only by
`CLOUDFIELD_EDGE_DATA` (here with `AETHER_EDGE_DATA` unset):

```
aether-edge: this edge uses the data directory <dir> from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to <dir>, or pass --data <dir> on the command line, which both images read
```

An older image ignores `--retired-pairs` and does not serve the pair either:
behind a proxy it answers `421 Misdirected Request` on those hosts.

### Certificates and host names

Behind a reverse proxy, keep both retired hosts' `server` blocks (or router
rules) and certificates until the pair is removed. With its own
certificates the edge answers a retired host only with an unexpired
certificate already in its ACME cache and never renews it. Once it expires
the TLS handshake fails, with the error `<host> is the host of a retired
pair and the ACME cache holds no unexpired certificate for it that the
client supports; the edge orders none`.

Keep both host names under your control for good, pointing at the edge. A
server or client that never moved still dials them, and a server checks only
that whoever answers presents the edge's public key, so whoever held the
names would receive device tokens, connector tokens and server connections.

The project's own edge follows
[plans/2026-11-27-retire-onaether-runbook.md](plans/2026-11-27-retire-onaether-runbook.md).
