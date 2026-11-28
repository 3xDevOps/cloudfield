# Moving off a retiring edge address

- An **edge** is the relay Cloudfield servers and clients dial out to
  ([edge.md](edge.md)); an **address pair** is one sign-in host and one
  relay host of it, and an edge can serve several.
- A **retiring** pair works in full and says where to go: its edge pages
  show `This address of the edge is retiring on <date>. Its new address is
  <address>`, its answers carry the header `aether-edge-retiring: <new
  relay origin>; date=<date>` (read it from `GET /v1/edge`), and on a
  server enrolled there `sudo cloudfield-server edge status` prints a
  `retiring` row with the command that moves it
  ([Moving a server](#moving-a-server)).
- A **retired** pair answers `410 Gone` naming the new address: JSON to a
  server, a client or a connector, a page to a browser. It never redirects
  to the new pair, and its `/healthz` still answers `ok`.
- Nothing moves by itself. Before the operator retires the pair, a person
  moves each server, each device (a signed-in CLI or app) and each
  connector (Cloudfield added to ChatGPT, Claude or another MCP client at a
  URL on the edge).

The examples use `auth-old.example.com` + `edge-old.example.com` for the
retiring pair and `auth-new.example.com` + `edge-new.example.com` for its
replacement. Use the addresses your edge's operator gives you.

## Moving a server

This is for the administrator of a `cloudfield-server` host whose `edge-url`
is the old relay origin. Moving it changes the address it enrolls at. The
**pinned edge key** stays: the edge's public signing key, which the server
stored at its first connection and requires at every later one. So do the
owner, members and devices
([edge.md](edge.md#moving-to-another-address-of-the-edge)).

Check where the server reads its address from, where it is, which build it
runs, and what the edge says about the address:

```sh
systemctl cat aether-server | grep ExecStart
grep -e edge-url -e data-dir /etc/aether/server.conf
sudo cloudfield-server edge status
cloudfield-server version
curl -fsS -H 'aether-edge-version: 6' https://edge-old.example.com/v1/edge | jq -r '.retiring, .retiring_date'
```

Expect `ExecStart=/usr/local/bin/aether-server serve --config
/etc/aether/server.conf`. With `--edge-url` on that line the service takes
its address from the flag, and nothing below changes it. With `--data-dir`
there, pass the same `--data-dir <dir>` to `edge status` and `edge trust`
below, never to `edge move`: where the options file gives another
directory, `move` refuses with `<file> gives edge-url "<url>" and data-dir
"<dir>", but edge move runs with "<url>" and "<dir>" from its flags;
nothing changed, run it again without --edge-url or --data-dir`, and
without the flag it reads the file's directory, not the service's. `move`
works only once the options file's `data-dir` is the directory the
service uses. With another `--config <file>`, read that file instead and
pass `--config <file>` to every `cloudfield-server edge` and `config`
command below; `update` and `version` take neither.

The server is on the old pair when the second prints `edge-url =
https://edge-old.example.com` and the status exits 0 with `edge
https://edge-old.example.com`, no `running` row, and `connection
connected since <time>; the edge last answered <n>s ago`. A `running` row
names another address the running server is at or last dialed. Any other
`connection` row is no proof that the server is connected there now
([edge.md](edge.md#checking-the-connection)); `connected since <time>,
unchecked: ...` exits 0 too, and only says that a server from before
heartbeats once connected. The version line is
`aether-server <version> (<commit>)`. The last command prints the
replacement and the date, or `null` twice when the address is not
retiring.

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

Whether this build can move itself is in its usage, not in its version
number, since prereleases of one version differ:

```sh
cloudfield-server edge
```

It prints `aether-server: usage: cloudfield-server edge <status|claim-code
[--admin <member id>]|trust|move <address>|leave> [--config <file>]`. With
`move <address>` in it, do steps 2 to 4. Without, as in v0.8.7's
`aether-server: usage: aether-server edge <status|claim-code [--admin
<member id>]|trust|leave> [--config <file>]`, or where the command is
still only `aether-server`, do step 1 first.

A build from before edge protocol 6 also serves connectors only at the
address it is enrolled at. A connector added at the new address is then
refused with `invalid_target`, `resource must be
https://edge-old.example.com/mcp`, or `.../mcp/<server id>` for a server's
own URL, until the server is updated (step 1).

1. Update the server ([install.md](install.md#update-the-server)). The
   update restarts the service, which ends every relayed connection. It
   needs the root systemd unit `aether-server install` writes. On a host
   without it, it refuses and names the other way: `sudo aether update`,
   then a restart of the service by hand.

   ```sh
   sudo aether-server update
   ```

2. Move it. The server keeps running. Each of the command's two enrollments
   replaces the server's control connection at the edge, and the server
   dials again by itself. Until it has, a new relayed connection can fail
   with `server is not connected to the edge`.

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

3. Apply it. The restart ends every relayed connection; clients connect
   again once the server is back:

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
   ago`, and no `running` line. That is the server's control connection to
   the edge only, as the `measures` row says. For a relayed connection,
   link from a computer signed in at the new address
   ([Moving a device](#moving-a-device), step 2).

Then hand out new links ([Links](#links)), and tell members to move their
devices and connectors ([Moving a device](#moving-a-device),
[Moving a connector](#moving-a-connector)).

### If the address is retired before the server moved

`sudo cloudfield-server edge status` exits 3 with:

```
connection   disconnected since <time>: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com
```

It still prints the `retiring` row when the server stored a notice before:
a build with notices that connected while the address was retiring.
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
made. With no edge key pinned it also names `sudo cloudfield-server edge
trust`, to run before the restart. A build from before this check enrolls
at the old address first and fails:

```
aether-server: edgeagent: enroll at https://edge-old.example.com: dial https://edge-old.example.com/v1/server/control: 410 Gone: https://edge-old.example.com is retired; this edge is at https://edge-new.example.com
```

In either case the move is by hand, and on the operator's word alone.
`edge move` refused because nothing the server can verify says the new
address is the same edge. A `410`, its text, and what the new address says
of itself are no such proof. Get the new address and the edge key's
fingerprint from whoever runs the edge, through a channel that is not the
edge. Without that, stop. Never work around a signature, pin, protocol or
address that does not match: stop and ask the operator.

**With a pinned key** (the `edge key` row of `edge status` is a `SHA256:`
fingerprint):

```sh
sudo cloudfield-server edge status | grep 'edge key'
curl -fsS -H 'aether-edge-version: 6' https://edge-new.example.com/v1/edge | jq -r .fingerprint
```

Both must print the fingerprint the operator gave you, also when the
server pinned the key only a moment ago. This is a pre-check and proves
nothing by itself: the second line is the new address's own statement,
and an edge key is public. If the three do not agree, stop. If they do:

```sh
sudo cloudfield-server config set edge-url https://edge-new.example.com
sudo systemctl restart aether-server
```

```
/etc/aether/server.conf: edge-url = https://edge-new.example.com (was https://edge-old.example.com)
apply it with:
  sudo systemctl restart aether-server
```

`config set` leaves the pin in place, and the pin is what protects the
server from then on:

- At every connection the server compares the key the edge presents with
  the pinned one and refuses another: `edge status` and the journal show
  `edge key changed: pinned <fingerprint>, edge presents <fingerprint>;
  ...`. Do not run the `edge trust` that message names to get past it.
  This compares public keys; it does not prove that whoever answers holds
  the private key.
- The private key is needed after that: the server opens a relayed member
  or claim connection only for a grant signed with the pinned key, and
  keeps a notice only when its signature verifies against it.
- The TLS certificate of the new host name is what authenticates the
  address itself, as for the address the server first enrolled at.

Check it as in step 4. The server keeps its owner, which it holds per edge
key, not per address.

**Without a pinned key** (the row reads `not pinned yet; the server pins it
when it first connects`), the server has nothing to compare with: it pins
whatever key answers its first connection, and the running service keeps
dialing. So stop the service first, then pin the operator's key before
the new address is written anywhere. Stopping it ends every connection to
this server:

```sh
sudo systemctl stop aether-server
sudo cloudfield-server edge status | grep 'edge key'
```

The row must still read `not pinned yet`. If it shows a fingerprint, the
service pinned a key before it stopped: follow **With a pinned key**
above, whose three fingerprints must agree, and start the service with
its restart. Otherwise chain the three commands with `&&`, so that
nothing runs after a refusal. `edge trust` asks the address given as
`--edge-url`, uses the data directory of the options file as the service
does, and exits non-zero unless it pinned:

```sh
sudo cloudfield-server edge trust --edge-url https://edge-new.example.com &&
  sudo cloudfield-server config set edge-url https://edge-new.example.com &&
  sudo systemctl restart aether-server
```

With another options file or data directory, add `--config <file>` to the
first two and `--data-dir <dir>` to the first. `edge trust` asks:

```
pinned edge key:  none
https://edge-new.example.com presents: SHA256:<fingerprint>
Compare it with the fingerprint the edge's operator publishes. A key you did not expect means another party may be answering for the edge.
Type yes to pin the new key:
```

Type `yes` only when the `presents` line is the fingerprint the operator
gave you. It then prints `pinned SHA256:<fingerprint>; the server uses it
when it next reconnects` and a line on the owner, and the other two
commands run. Any other answer, or none, ends in `aether-server: not
pinned; the edge key is unchanged`, and an address that does not answer in
the error of the request. Nothing after it runs: the options file still
names the old address and the service stays stopped. Stop there and ask
the operator. Never run the other two commands by hand after a refusal;
`sudo systemctl start aether-server` brings the server back at the old
address. Check it as in step 4.

## Moving a device

This is for anyone who signed in to the edge at the old address: the
`cloudfield` CLI, the desktop app, or the Android app. A **device** is one
client install, with its own key and a device token from the edge. A device
token never expires. The client keeps one per relay origin and sends it
only to that origin and to the sign-in origin that issued it, so the new
address takes a sign-in of its own. The device key stays the same, and
servers know the device by it.

The CLI keeps `config.json` (the links), `edge-device-key` and
`edge-tokens.json` in one directory: `aether` under
`$CLOUDFIELD_CONFIG_DIR` (or `$AETHER_CONFIG_DIR`) when that is set, else
under the platform's config directory, which on Linux is
`$XDG_CONFIG_HOME` or `~/.config`; a `cloudfield` directory beside
`aether` is used instead when it exists. macOS and Windows are in
[install.md](install.md#client-files-and-variables). On Linux:

```sh
d=${CLOUDFIELD_CONFIG_DIR:-${AETHER_CONFIG_DIR:-${XDG_CONFIG_HOME:-$HOME/.config}}}
[ -d "$d/cloudfield" ] && d=$d/cloudfield || d=$d/aether
(umask 077; set -C; cat "$d/config.json" > "$d/config.json.before-move") &&
  cmp "$d/config.json" "$d/config.json.before-move" && echo "kept $d/config.json.before-move"
jq -r '.edges | to_entries[] | "\(.key)  \(.value.account.login // .value.account.email)"' "$d/edge-tokens.json"
jq '{default: {addr, key, edge_url, server_id}, links: [.links[]? | {name, addr, edge_url, server_id}]}' "$d/config.json"
cloudfield servers --edge https://edge-old.example.com
```

No command prints a token. The second line keeps the whole links file,
which the steps below rewrite, and must print `kept <file>`: go on only
then. `set -C` refuses to overwrite: an error that the file exists means
a copy from an earlier attempt is there. That copy is the original: keep
it, and do not make another. The first `jq` prints each relay origin this
computer holds a token for, as the token file spells it, and the account.
The second
prints the default link, which is the top-level fields of `config.json`,
and each saved link: its name, direct address, edge address and server
id. Note every entry that names the old relay host, and the whole default
link. `https://edge-old.example.com:443` is the same address as
`https://edge-old.example.com`, and the token file keeps the spelling it
was signed in with: note it as printed. The last command asks the edge: it lists
your servers when the token for the old address still works. An entry in
the file does not show that.

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

1. Sign in at the new address with the same GitHub account, and confirm
   the code in the browser. Skip this when `cloudfield servers --edge
   https://edge-new.example.com` already lists your servers with that
   account: a second sign-in there issues another token and replaces the
   local one without revoking it, so the first stays valid until you
   revoke it on `https://auth-new.example.com/devices`.

   ```sh
   cloudfield login --edge https://edge-new.example.com
   ```

   ```
   auth-new.example.com signs you in for the edge edge-new.example.com
   open https://auth-new.example.com/device and enter the code <code>
   waiting for you to confirm the code...
   signed in to edge-new.example.com as <login> (github); this device is "<label>"
   ```

   Five lines on how long the token lasts follow.

2. Link each server again that a link reaches through the old address,
   with the same name and direct address it had:

   ```sh
   cloudfield link <server id> --edge https://edge-new.example.com [--name <name>] [--addr <host[:port]>]
   ```

   ```
   linked to server <server id> through edge-new.example.com as <display name> (<role>)
   ```

   With `--addr` the line reads `linked to server <server id> at <addr>,
   then through edge-new.example.com as ...` whichever path answered, since
   the direct address is tried first. To see the relay work, link once
   without `--addr`, then again with it. A member who waits for approval
   gets `(<role>, pending); an admin runs: aether member approve <member
   id>`.

   Every `link` makes its server the default link, and `--name` also
   writes the saved link of that name. A link that replaces a default link
   with another server id first saves that default as a link named after
   its server id, with the edge address it had, unless a saved link already
   has that server id. So the order is:

   1. If the default link names the old address, link its server first,
      without `--name`. That changes its edge address in place and keeps
      its other fields.
   2. Link each saved link that names the old address, with `--name <name>`.
   3. Link the server of the original default link again, last, with the
      `--addr` it had and the edge address it now has, so it is the
      default again.
   4. Run the `jq` line for `config.json` again. Link any entry that still
      names the old address with `--name <its name>`, then repeat 3.
   5. A default link with no `server_id` was a direct link, which the
      links above replaced without saving. Never restore it with
      `cloudfield link <addr>`: that dials before any `known_hosts` of
      the old link is back, checks the host key against
      `~/.ssh/known_hosts`, and trusts and records a host it does not
      find there. Put the saved top-level fields back instead, keeping
      the links as they are now:

      ```sh
      jq -s '.[0] + (.[1] | with_entries(select(.key == "links")))' "$d/config.json.before-move" "$d/config.json" > "$d/config.json.new" &&
        cat "$d/config.json.new" > "$d/config.json" && rm "$d/config.json.new"
      ```

   What `link` does not carry over: except when it relinks the server the
   default link already names, it writes the link anew, without the
   `repo`, `known_hosts`, `user` and `key` the link had, and the default
   link without `gui_server`. Compare with the copy and put back by hand,
   in an editor, what differs beside `edge_url`:

   ```sh
   diff <(jq -S . "$d/config.json.before-move") <(jq -S . "$d/config.json")
   ```

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
   `--remote`, `--user`, `--key` and `--known-hosts` where the old
   `ExecStart` line has them, and with `--server-id` also `--server`. The
   unit path and the restart command are Linux's; install.md has the macOS
   and Windows ones.

4. Sign out at the old address while it still answers, so its token is
   revoked there. Give `--edge` exactly as the first `jq` line printed
   it, with `:443` if it has one: `logout` finds the token only under
   that spelling.

   ```sh
   cloudfield logout --edge https://edge-old.example.com
   ```

   ```
   signed out of edge-old.example.com: the device token is revoked at the edge and deleted here; the device key and your links stay
   still signed in to edge-new.example.com; sign out there with: aether logout --edge https://edge-new.example.com
   ```

Check it: the first `jq` line no longer lists the old relay host in any
spelling, the second shows no `edge_url` with it, and
`cloudfield servers --edge https://edge-new.example.com` prints your
servers. That is the edge's list, not a connection to a server; the `link`
lines of step 2 are. Then delete `config.json.before-move`.

The desktop app uses the same token file and links as the CLI on that
computer, so the steps above move it too. The app's sign-in step for the
Cloudfield relay shows the notice under the signed-in edge, as text:
`edge-old.example.com says it is retiring on <date> and that its new address
is https://edge-new.example.com.`, then `You are already signed in at
https://edge-new.example.com.` when this computer is, else `Confirm that
address with whoever runs the edge before you sign in there. Then run
cloudfield login --edge <new address>.` The app fills in no address and
starts no sign-in from a notice.

The Android app keeps its own device key and tokens in private storage and
has no terminal ([install.md](install.md#android-app)), so none of the
steps above runs on a phone, and its sign-in step has no field for an edge
address. A join link names its edge, so the phone moves with one:

1. Once the server has moved, an admin makes a join link for the GitHub
   account the phone is signed in with: `cloudfield invite --github
   <login> --workspace <workspace>` ([Links](#links)). Only a super admin
   may leave `--workspace` out; another admin is refused with `an admin's
   invitation names at least one workspace they own (--workspace
   <workspace>)`.
2. On the phone, open the link in the browser. Its page at the new
   address opens the app, by itself or with **Open in Cloudfield**. The
   app signs in at the edge the link names; use the same GitHub account,
   then select **Join**. The app's own fields for a link, `Have an
   invitation or claim link?` while signed out and `Claim or invite link`
   while signed in, are not there when its only sign-in is at a retired
   address.
3. The server finds the member by that GitHub account before it looks at
   the invitation, so the phone joins as the member it already is. If the
   app shows `Waiting for an admin of <server> to approve this device`, an
   admin approves it ([teams.md](teams.md#approve-devices)).
4. The invitation was not used and stays open for 7 days: the admin
   revokes it with `cloudfield invite revoke <invitation-id>`, which
   `cloudfield invite list` shows.

Never clear the app's storage to start over: that deletes the device key,
and the phone becomes a new device.

### If the address is retired before the device moved

When the operator retires the pair, the edge deletes the device tokens and
browser sessions it recorded at the old sign-in host, and those with no
recorded origin ([At the retired start](#at-the-retired-start)). Every
command at the old address is refused, whether its token was deleted or
not, for example:

```
aether: list servers: auth-old.example.com refused: https://auth-old.example.com is retired; this edge is at https://edge-new.example.com. Move a server with: cloudfield-server edge move https://edge-new.example.com. Sign a device in with: cloudfield login --edge https://edge-new.example.com (HTTP 410)
```

A connection to a server ends in `connect to server <server id>:
edge-old.example.com refused: https://edge-old.example.com is retired; this
edge is at https://edge-new.example.com (HTTP 410)`. The dashboard's
connection line reads `Authorization failed · edge-old.example.com refused
the connection to server <server id>: <the edge's text>`, and a first load
that fails with a refusal the app reads as a retired address's shows a
panel titled `edge-old.example.com says it is retired`. Run steps 1 to 3.

Then `cloudfield logout --edge https://edge-old.example.com` deletes the
local token, though a retired address confirms no sign-out:

```
deleted the device token for edge-old.example.com here, but the edge did not confirm the sign-out: it answered that this address is retired and that its new address is https://edge-new.example.com.
Device <device id> may still be signed in; revoke it on https://auth-new.example.com/devices.
still signed in to edge-new.example.com; sign out there with: aether logout --edge https://edge-new.example.com
```

A retired address answers `410` before it reads a token, so the refusal
does not say whether the edge still holds this one. Revoke the device on
the page the second line names, and never try the old token at another
address. That line names
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
dashboard, **Settings → ChatGPT and Claude** shows `Universal URL` for a
connector at `/mcp` and `This server’s own URL, <url>` for one at
`/mcp/<server id>`; `cloudfield mcp` prints `universal URL` and `at <url>`.

The server reads the state of an address from the notices its edge signs
([Moving a server](#moving-a-server)). From those it marks each of your
connectors at a retiring or retired address, in four places. Each is
detailed in
[connectors.md](connectors.md#adding-a-connector-again-at-a-new-address);
a server build from before these marks shows none, and a CLI from before
them prints no such line. Nothing moves or revokes a marked connector for
you. Each text below drops ` on <date>` when the operator set none.

- `cloudfield mcp` prints a marked connector with `at <url>`, at `/mcp`
  too, and one line under it. `aether mcp` in these lines is the same
  command under its old name:

  ```
  Connected:
    <id>  <client name> (<client host>)  scopes <scopes>  added <date>  never used  at https://edge-old.example.com/mcp
      retiring on <date>: add it again at https://edge-new.example.com/mcp, check it works, remove the old one in the app, then: aether mcp revoke <id>
  ```

  With no new URL named the line is `retiring on <date>: ask this server's
  admin where to add it again`. At a retired address it is `retired: add it
  again at https://edge-new.example.com/mcp if you still use it, remove the
  old one in the app, then: aether mcp revoke <id>`, or with no new URL
  `retired: remove it in the app, then: aether mcp revoke <id>`. When a
  named URL is a server's own and the URL `cloudfield mcp` offers takes the
  edge sign-in, it adds `Reconnecting one at the server's own URL asks for a
  code: aether mcp code`. When no code can be minted, it prints this list
  before the error.
- **Settings → ChatGPT and Claude** shows the connector as `Added at <url>`
  under the title `This address is retiring on <date>` or `This address is
  retired`. With a new URL named, the text is `Add it again in <client
  name> at this URL and check that it works, then remove the old connector
  in <client name> and select Revoke on this row.` (for a retired address,
  `This connector no longer works. Add it again in <client name> at this
  URL, then remove the old connector in <client name> and select Revoke on
  this row.`), then `Its page asks for a code; run aether mcp code to get
  one.` where that applies, `Its consent page is where you choose its
  scopes.` and the URL with a copy button. With none it is `It keeps working
  until the address is retired. Ask this server’s admin where to add it
  again.` or `This connector no longer works. Revoke it, and remove the old
  connector in <client name>.` The row has no **Reconnect** and no **Move to
  the universal URL**, and Settings never moves or revokes it: you select
  **Revoke**.
- The consent page at a retiring address, which the app opens when you
  connect there, says `This address is retiring on <date>. Add the connector
  at <url> instead. Connecting here still works until it is retired.` With
  no new URL its middle sentence is `Ask this server's admin where to add
  the connector instead.`
- A tool result carries a second text block, `Notice for the user: this
  connector was added at https://edge-old.example.com/mcp, an address that
  is retiring on <date>. It must be added again at
  https://edge-new.example.com/mcp.`, at most once a day per connector and
  again after a server restart. It comes only while the address is retiring
  and a new URL is named, never with a failed call, and never with `search`
  or `fetch`.

The new URL is the connector's path, `/mcp` or `/mcp/<server id>`, at the
new relay origin. The server names it only while all of these hold:

- the server is connected to its edge at edge protocol 6 or later, and the
  edge reports it claimed;
- the edge announces the new address on that connection;
- the notice did not come with the connection that pinned the edge key,
  which is trusted on first use;
- for `/mcp` in `cloudfield mcp`, Settings and the tool result, you have a
  GitHub account linked. The consent page at `/mcp` names it without this
  check.

When no URL is named, a connector at a retiring address keeps working
until the address is retired. For `/mcp`, link a GitHub account with
`cloudfield member link --github <login>`
([teams.md](teams.md#link-or-merge-sign-ins)). Otherwise ask the server's
admin, who checks the first three conditions and gives you the URL. Then
follow the steps below.

While the server's own address is retiring and the new URL is named,
`cloudfield mcp` and Settings also offer that URL for a new connector.

The server must speak edge protocol 6: one that refuses the new address
with `invalid_target` is updated first
([Moving a server](#moving-a-server)).

1. Note the old connector's id: `cloudfield mcp` lists it under
   `Connected:`. Do this before adding the new one, since a build without
   the marks above prints `universal URL` for both.
2. Add the connector again at the same path on the new host, such as
   `https://edge-new.example.com/mcp`, as in
   [Add it in ChatGPT](connectors.md#add-it-in-chatgpt) or
   [Add it in Claude](connectors.md#add-it-in-claude). For a server's own
   URL, `cloudfield mcp code` prints the code it asks for. In Claude Code,
   add it under a name of its own and leave the working entry in place,
   then run `/mcp` there and authenticate the new entry:

   ```sh
   claude mcp add --transport http cloudfield-new https://edge-new.example.com/mcp
   ```

3. Check that it works: call a tool through the new connector, and see a
   result. `cloudfield mcp` then lists a second id with `last used`.
4. Only then remove the old connector in the app (in Claude Code, `claude
   mcp remove <old name>`). Removing it there does not revoke it on the
   server.
5. Revoke the old one on the server, with the id noted in 1:

   ```sh
   cloudfield mcp revoke <old connector id>
   ```

   ```
   revoked connector <old connector id>, <client name> (<client host>); its tokens no longer work
   ```

### If the address is retired before the connector moved

A connector request to the old address is answered `410 Gone` with:

```json
{"error":"https://edge-old.example.com is retired; this edge is at https://edge-new.example.com. Move a server with: cloudfield-server edge move https://edge-new.example.com. Sign a device in with: cloudfield login --edge https://edge-new.example.com","retired":{"replacement":"https://edge-new.example.com"}}
```

The app shows its own error for a failed connection. The server no longer
accepts a token issued at the old address. Once the server is connected at
the new address, `cloudfield mcp` and Settings mark the connector `retired`
as above; a retired address gives no note in tool results. Add the
connector again and revoke the old one as above; the old one also stops
being listed once its refresh token is unused for 30 days.

## Links

Nothing made at the old address carries over once it is retired:

- **Join links** (invitations) name the address they were made at. Once
  the server has moved, issue new ones with `cloudfield invite --github
  <login>` or `--email <address>`, with the `--role` and `--workspace` the
  person is to get; each is valid 7 days
  ([teams.md](teams.md#through-an-edge)). Without `--github` or `--email`
  the command prints a one-time invite code for a direct link, valid 24
  hours, not a join link. At a retired address a join link opens a page
  that says `This link was made at the retired address and no longer
  works. Ask whoever sent it for a new one.`
- **Claim links** name it too. A moved server keeps its owner and needs
  none: `sudo cloudfield-server edge claim-code` answers `this server is
  already claimed by <account>`, with `--admin` too. A server with no
  owner and no members gets a new link from that command, valid 30
  minutes. One with no owner that has members is refused without `--admin
  <member id>` naming one of its admins; the refusal lists them.
- **Browser sessions** on the edge's pages are per host: sign in at the new
  sign-in host. At a retired host, a browser gets a page with status 410,
  `This address is retired`, naming the new address and two commands,
  `cloudfield-server edge move <new relay origin>` and `cloudfield login
  --edge <new relay origin>`, and the edge's cookies on that host are
  cleared.

## For edge operators

You run `aether-edge` and serve a second pair (`--origin-pairs`,
[edge.md](edge.md#more-than-one-address-pair)) that replaces the old one.
Set each option in the environment file, never as a flag: an older image
ignores an unknown variable and refuses an unknown flag. Every refusal below
is printed as `aether-edge: <text>`, and the edge does not start.

The two options name the same pair one after the other, never together.
These are lines of the systemd environment file, not shell; in Compose
they go under `environment:` as `NAME: value`.

```
# while the pair is retiring (steps 1 to 3 below)
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01

# once it is retired (step 4 on): the line above is removed
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
   date=2027-03-01`. From this start, and while this option or
   `--retired-pairs` stays set, it records the pair each device token and
   session is issued at, for one issued earlier the pair it is next used
   at, and the pair each server enrolls at. A device sign-in that was
   pending at that restart, started before the edge recorded, is deleted
   and its person runs `cloudfield login` again.
2. Wait while people move, at least 30 days. A device token never expires,
   and one issued before step 1 gets a recorded origin only when it is
   next used. Step 4 deletes every token and session with no recorded
   origin, on every pair, and keeps those recorded at a pair that stays,
   used since or not. Read who is left with `aether-edge retiring` at the
   start, tell the owners it lists, and read it again right before step 4.
   A run with neither option, or of an image from before them, stops the
   recording: what it issues has no origin. Step 4 deletes it too, unless
   it was used once at a pair that stays after recording began again: any
   request with the token records it there, a `cloudfield servers`
   included. A connector request records only its server and relay
   origin, and gives no token an origin.
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

The refusal checks only that the edge ever recorded, not for how long:
after one short run of step 1, undone or not, step 4 starts.

Other refusals at step 4:

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth-old.example.com is still served; remove its pair from --origin-pairs in the same edit` | Remove the pair from `--origin-pairs` |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove the `--retiring-pairs` entry once only one pair is served |
| `--retired-pairs: https://edge-new.example.com, the replacement of https://edge-old.example.com, is retiring; name a pair that stays` | Name a replacement that is not retiring |
| `--retired-pairs names <n> pairs; an edge keeps at most 16` | At most 16 entries |
| `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: retire this pair once fewer pairs are retiring or retired` | Fewer entries in both options together |

### Who is left

In a container, `docker exec <container> aether-edge retiring ...` instead:

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

Each server row is a claimed server whose last recorded enrollment or claim
was at this relay origin; its owner login is whom to tell, and is `-`
for a server with no owner. With none, the table is the line `no server
last enrolled or claimed here`. The counts are of the tokens and sessions
that exist now, by last use; the session rows count accounts, and pending
device sign-ins are counted nowhere. Step 4 deletes what the first three
rows count, however recently used. A token issued before recording began
and first used since at another pair is counted, and kept, there.
`unknown origin` counts what has no recorded origin: step 4 deletes those
device tokens and sessions on every pair, and those servers stay on
whatever address they dial. A section `recorded at an address this edge no
longer pairs: <origin>` counts what is recorded at a sign-in origin no
recorded pair names now. A retired pair keeps its section.

The first line does not filter the counts, and proves less than it seems.
`since` is when the last known recording period began: a later time than
your step 1 means a new period began, after an unclean stop or a start
more than 10 minutes after the last clean one. An unchanged `since` does
not prove the edge recorded all the time: a run without recording that
fits between a clean stop and a start within 10 minutes leaves no trace.
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

```
# before
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-old.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-old.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<old-client-id>
AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=<first-secret-file>
AETHER_EDGE_ORIGIN_PAIRS=https://auth-new.example.com https://edge-new.example.com <new-client-id> <second-secret-file>
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01

# after the swap (step 3)
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-new.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-new.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<new-client-id>
AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=<second-secret-file>
AETHER_EDGE_ORIGIN_PAIRS=https://auth-old.example.com https://edge-old.example.com <old-client-id> <first-secret-file>
AETHER_EDGE_RETIRING_PAIRS=https://edge-old.example.com https://edge-new.example.com 2027-03-01

# after retiring (step 4)
AETHER_EDGE_SIGNIN_ORIGIN=https://auth-new.example.com
AETHER_EDGE_RELAY_ORIGIN=https://edge-new.example.com
AETHER_EDGE_GITHUB_CLIENT_ID=<new-client-id>
AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE=<second-secret-file>
AETHER_EDGE_RETIRED_PAIRS=https://auth-old.example.com https://edge-old.example.com https://edge-new.example.com
```

Each client id keeps its secret: where the first pair's secret file is a
setting of yours, as in [`packaging/edge/compose.yaml`](../packaging/edge/compose.yaml),
the two paths swap and no file changes. The packaged systemd unit sets
`AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` itself, to the credential it loads
from `/etc/aether-edge/github-client-secret`, and the environment file
above overrides that only if it names the variable. Where it does not,
the path cannot swap: put the new app's secret in that file and the old
app's in the file the `--origin-pairs` entry names; the first file then
holds the live secret. Either way, sign in at both sign-in hosts after
the restart: a wrong id or secret shows only in a real sign-in, as
`GitHub sign-in: exchange code: <error>`. No pair is preferred, so the
swap changes only the order of the relay origins sent to servers.

### At the retired start

Once its listeners are bound and before it serves, the edge deletes in one
transaction each device token, browser session and pending device sign-in
whose recorded sign-in origin:

- has the host name of the retired sign-in origin, whatever the port; or
- has the host name of another sign-in origin the edge once recorded
  beside the retired relay origin, unless a served pair signs in at that
  host. The edge then keeps those rows and warns `edge: keeping the
  sign-ins of a served sign-in origin once recorded beside a retired relay
  origin`; or
- is missing, on every pair: the row was issued while the edge did not
  record and has not been used since while it did.

It keeps every row recorded at a sign-in origin that stays, issued there
or first seen there, used since or not: when a token was last used plays
no part. Then it logs:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth-old.example.com relay=https://edge-old.example.com replacement=https://edge-new.example.com
```

A deleted device signs in again; servers keep their record of it and its
approval. Deleted rows stay deleted if you remove the option. The deletion
commits before its log line is written and before the edge answers a
health check, so neither a failed health check nor a missing log line
shows that nothing was deleted. Only a start refused over its options or
its recorded pairs, with `aether-edge: <text>` as under [Order](#order)
and in [edge.md](edge.md#retired), deleted nothing; it serves no pair
until the options are put right.

A retired host answers `410` before it reads a token, so a `410` says
nothing about whether a token was deleted. A device that may still hold a
valid one is revoked on the Devices page (`/devices`) of the sign-in
origin that stays, never by sending its token to another host.

Check both old hosts from outside:

```sh
curl -sS -i https://edge-old.example.com/v1/edge
curl -sS -i https://auth-old.example.com/v1/edge
```

Each answers status `410`, `Cache-Control: no-store`, no `Location` header,
and the JSON error shown under [Moving a connector](#moving-a-connector)
with its own origin. `/healthz` still answers `ok`, so a probe of
`/healthz` does not show the retirement.

A `410` there says nothing about the pair that stays. Check it before and
after the retired start:

```sh
curl -fsS -H 'aether-edge-version: 6' https://edge-new.example.com/v1/edge | jq -r '.signin_origin, .fingerprint'
```

It prints `https://auth-new.example.com` and the edge key's fingerprint,
the same before and after. Then sign in at
`https://auth-new.example.com/signin` in a browser profile that has no
session there (one that has skips GitHub), run `cloudfield servers --edge
https://edge-new.example.com` on a device that signed in there earlier,
and see a server enrolled there reconnect ([Moving a server](#moving-a-server),
step 4).

### Rollback

From step 1 on, never restore the database as an undo, and roll back only
to an image that has both options (`aether-edge serve -h` lists
`-retiring-pairs` and `-retired-pairs`). That it can run the database
(`aether-edge schema --json`) is not that check.

- An image without the options ignores both variables. It records
  nothing and issues device tokens and sessions with no recorded origin.
  The next retired start deletes those on every pair, except one used in
  between at a pair that stays while an image with the options recorded.
  It sends servers no notice, so a server cannot move itself off a
  retired address, and it answers a retired host `421`, not `410`, except
  on `/healthz`.
- A backup from before step 1 has no recorded origin on any row and no
  record that the edge recorded, so an edge with `--retired-pairs` refuses
  to start on it while it holds a sign-in, with the refusal under
  [Order](#order).
- On a backup taken while the pair was retiring the edge starts and
  deletes the same sign-ins again. A restore brings back only the rows
  the backup holds: of the device tokens revoked since, those recorded at
  a pair that stays are valid again, and the start deletes the others
  with the retired pair's. It loses the accounts, sign-ins and claims
  made since ([edge.md](edge.md#backup-and-recovery)).
- An image without the options accepts every restored token at every
  served pair.

Take a backup right before step 4 and another once its checks pass.
`aether-edge backup` copies `edge.db` only; `--with-key` also copies the
edge key, a separate file the edge does not start without. Only the
private key restores an edge. Check the offline copy by the fingerprint
derived from it, not by its presence:

```sh
ssh-keygen -y -f <copy> | ssh-keygen -lf -
```

It prints the `.fingerprint` of `GET /v1/edge` and no key material, and
fails on a copy of the public key alone, for which `ssh-keygen -lf
<copy>` would print the same fingerprint.

With `aether-edge-update`, turn the updater off for the time of each
manual change, with the inverse of the command that
[edge.md](edge.md#installing-the-updater) turns it on with, and run no
`aether-edge-update` command by hand in that time:

```sh
systemctl is-active aether-edge-update.service aether-edge-update-recover.service
sudo systemctl disable --now aether-edge-update.timer aether-edge-update-recover.service
```

Run the first line again after the second. `activating` or
`deactivating` is a run in progress: let it end. Never stop
`aether-edge-update.service`, which rolls back a switch it interrupts.
The updater checks a release's promotion, signature and build order, not
which commits it has, so before it is on again (`sudo systemctl enable
--now` with the same two units), and before any `aether-edge-update
--rollback`, check that the release it would run has both options.
`--rollback` returns to the release in the `previous` file of the
updater's state directory. Set `AETHER_EDGE_UPDATE_RELAY_URL` to the new
relay origin before step 4, since the updater's checks get `410` at a
retired one ([edge.md](edge.md#automatic-updates)).

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
