# Moving off a retiring edge address

- An **edge** is the relay Cloudfield servers and clients dial out to
  ([edge.md](edge.md)); an **address pair** is one sign-in host and one
  relay host of it, and an edge can serve several.
- A **retiring** pair works in full and says where to go: its edge pages
  show `This address of the edge is retiring on <date>. Its new address is
  <address>`, every answer carries the header `aether-edge-retiring: <new
  relay origin>; date=<date>`, and on a server enrolled there `sudo
  cloudfield-server edge status` prints a `retiring` row with the command
  that moves it ([Moving a server](#moving-a-server)).
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

The server reads the same from the notice the edge signs with its pinned key
at each connection, and from then on says so itself:

- `edge status` prints `retiring     https://edge-old.example.com is retiring
  on <date>; move this server with: sudo cloudfield-server edge move
  https://edge-new.example.com`, without `on <date>` when the operator set
  none and with `--config <file>` when it read another options file. A
  notice that came with the connection that pinned the edge key ends with
  `(the edge sent this with the connection that pinned its key, so it is
  trusted on first use and shown only)`.
- The journal (`sudo journalctl -u aether-server`) gets a warning at each
  connection: `edge: https://edge-old.example.com is retiring on <date>;
  move this server with: sudo cloudfield-server edge move
  https://edge-new.example.com (with the service's --config <file>, if it
  has one) if edge-url there still names https://edge-old.example.com`.
- In the dashboard, super admins get a warning titled
  `https://edge-old.example.com is retiring on <date>` on **Settings →
  Remote access** and a card with the same title on **Needs you**, each
  with the move and restart commands of steps 2 and 3. Other members see
  an **Address** row in Settings, `https://edge-old.example.com is retiring
  on <date>; the edge recommends https://edge-new.example.com. A super
  admin moves the server.`, and no card.

A server build from before these notices shows none of the three; the
`curl` line works on every build.

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
   enrolled at https://edge-new.example.com, an address of the same edge (edge key SHA256:<fingerprint>, still pinned); the edge announces https://edge-old.example.com too, so links and connectors made there keep working until it is retired; it is retiring, so there is no moving back
   /etc/aether/server.conf: edge-url = https://edge-new.example.com (was https://edge-old.example.com)
   apply it with:
     sudo systemctl restart aether-server
   ```

   A build from before the notices ends the first line at `keep working`
   and adds `to move back:` with `sudo aether-server edge move
   https://edge-old.example.com`. With another options file, pass `--config
   <file>` after the address. A refusal prints one line, exits 1 and writes
   nothing; the refusals are listed in
   [edge.md](edge.md#moving-to-another-address-of-the-edge).

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

It still prints the `retiring` row from the notice the server stored before.
The journal (`sudo journalctl -u aether-server`) logs `edge: control
connection ended; reconnecting` with the same error, and retries. Nobody
reaches the server through the edge; member SSH keys and tailnet access are
not affected.

`sudo cloudfield-server edge move https://edge-new.example.com` still moves
it. When the old address refuses with `410`, the command enrolls at the new
address only, and moves only when that answer carries a notice, signed with
the pinned edge key for this enrollment, that the old address is retired in
favour of the new one; the new address must also present the pinned key,
speak edge protocol 6 or later and announce itself. A `410`, its text, or a
notice stored earlier moves nothing. It prints no way back:

```
enrolled at https://edge-new.example.com, an address of the same edge (edge key SHA256:<fingerprint>, still pinned); the edge says https://edge-old.example.com is retired
/etc/aether/server.conf: edge-url = https://edge-new.example.com (was https://edge-old.example.com)
apply it with:
  sudo systemctl restart aether-server
```

Restart as in step 3 and check as in step 4.

Without that notice it changes nothing and says what to do by hand:

```
aether-server: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com; the edge at https://edge-new.example.com sends no notice signed with the pinned key that https://edge-old.example.com is retired, and a refusal alone does not show it, so nothing changed. If https://edge-old.example.com no longer answers for this edge: confirm https://edge-new.example.com and the edge key's fingerprint with whoever runs the edge, then run sudo cloudfield-server config set edge-url https://edge-new.example.com and sudo systemctl restart aether-server
```

It ends with the same steps when no connection to the old address could be
made, and when no edge key is pinned, where `sudo cloudfield-server edge
trust (it shows the fingerprint to compare; type yes)` comes before the
restart. A build from before this check enrolls at the old address first
and fails:

```
aether-server: edgeagent: enroll at https://edge-old.example.com: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com
```

In either case, move it by hand. First check that the new address presents
the edge key the server pinned; both commands must print the same
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

While the address is retiring, a `cloudfield` command that the old address
answers prints, once, on stderr after its own output:

```
cloudfield: https://edge-old.example.com says it is retiring on <date> and that its new address is https://edge-new.example.com.
Sign in there with: cloudfield login --edge https://edge-new.example.com
The default link (server <server id>) still names https://edge-old.example.com.
The link "<name>" (server <server id>) still names https://edge-old.example.com.
The sync daemon's unit names https://edge-old.example.com.
Moving links and the sync daemon, and signing out of https://edge-old.example.com: https://github.com/3xDevOps/cloudfield/blob/main/docs/edge-retire.md#moving-a-device
```

Nothing signs that notice: it is the old address's own word. The `Sign in
there` line is printed only when this computer already knows the new address
(a token there, or a saved link that names it; the CLI's built-in default
edge counts only when the old address is one of the project's hosted ones)
and holds no token for it. For an address it does not know, the line is
`Confirm that address with whoever runs the edge before you sign in there.`
The link and daemon lines appear for what this computer holds. The CLI
prints no link, daemon or sign-out command and changes nothing; the steps
are these:

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
   cloudfield link <server id> --edge https://edge-new.example.com [--name <name>] [--addr <host[:port]>]
   ```

   ```
   linked to server <server id> through edge-new.example.com as <display name> (<role>)
   ```

   Every `link` makes its server the default link, the top-level fields of
   `config.json`, and `--name` also replaces the saved link of that name.
   So link the default link's server last, without `--name` if it had none.

3. Where the sync daemon's unit names the old address, write the unit again
   and restart it ([install.md](install.md#the-client-side-sync-daemon)):

   ```sh
   grep edge-old.example.com ~/.config/systemd/user/aether-daemon.service
   cloudfield daemon install --repo <path>
   systemctl --user daemon-reload && systemctl --user restart aether-daemon
   ```

   `daemon install` takes the server and its edge address from the default
   link. For another server, name both: `cloudfield daemon install --repo
   <path> --server-id <server id> --edge-url https://edge-new.example.com`.
   It writes the whole unit again, so repeat `--workspace`, `--base`,
   `--remote` and `--user` where the old `ExecStart` line has them, and
   with `--server-id` also `--server`, `--key` and `--known-hosts`. The
   unit path and the restart command are Linux's; install.md has the macOS
   and Windows ones.

4. Sign out at the old address while it still answers, so its token is
   revoked there:

   ```sh
   cloudfield logout --edge https://edge-old.example.com
   ```

   ```
   signed out of edge-old.example.com: the device token is revoked at the edge and deleted here; the device key and your links stay
   still signed in to edge-new.example.com; sign out there with: aether logout --edge https://edge-new.example.com
   ```

Check it: the `jq` command above prints only `https://edge-new.example.com`,
and `cloudfield servers --edge https://edge-new.example.com` lists your
servers.

The desktop app uses the same token file and links as the CLI on that
computer, so the steps above move it too. The app's sign-in step for the
Cloudfield relay shows the notice under the signed-in edge, as text:
`edge-old.example.com says it is retiring on <date> and that its new address
is https://edge-new.example.com.`, then `You are already signed in at
https://edge-new.example.com.` when this computer is, else `Confirm that
address with whoever runs the edge before you sign in there. Then run
cloudfield login --edge <new address>.` The app fills in no address and
starts no sign-in from a notice. How to point the Android app at another
address of the edge is not documented yet.

### If the address is retired before the device moved

When the operator retires the pair, the edge deletes the device tokens and
browser sessions issued there. Every command at the old address is refused,
for example:

```
aether: list servers: auth-old.example.com refused: https://auth-old.example.com is retired; this edge is at https://edge-new.example.com. Move a server with: cloudfield-server edge move https://edge-new.example.com. Sign a device in with: cloudfield login --edge https://edge-new.example.com (HTTP 410)
```

A connection to a server ends in `connect to server <server id>:
edge-old.example.com refused: https://edge-old.example.com is retired; this
edge is at https://edge-new.example.com (HTTP 410)`. The dashboard's
connection line reads `<edge> refused the connection to <server>: <the
edge's text>`, and a first load that fails shows a panel titled
`edge-old.example.com says it is retired`. Run steps 1 to 3.

Then `cloudfield logout --edge https://edge-old.example.com` deletes the
local token, though a retired address confirms no sign-out:

```
deleted the device token for edge-old.example.com here, but the edge did not confirm the sign-out: it answered that this address is retired and that its new address is https://edge-new.example.com.
Device <device id> may still be signed in; revoke it on https://auth-new.example.com/devices.
still signed in to edge-new.example.com; sign out there with: aether logout --edge https://edge-new.example.com
```

The edge deleted that token when it retired the pair. The second line names
the sign-in origin this computer recorded when it signed in at the new
address. Without that sign-in it ends `revoke it on the Devices page of the
sign-in origin of https://edge-new.example.com` when this computer knows the
new address as above, else `ask whoever runs the edge for the address of its
Devices page and revoke it there`. A `410` that names no replacement, or that comes
without the edge's protocol version header, keeps the token and ends `; the
token is still valid and kept in <config dir>/edge-tokens.json so you can
retry`, as every `410` does on a CLI from before this change.

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
