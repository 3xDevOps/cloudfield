# Runbook: Retiring auth.onaether.dev and edge.onaether.dev

**Goal:** the hosted edge stops serving its first address pair,
`https://auth.onaether.dev` + `https://edge.onaether.dev`, in favour of
`https://auth.cloudfield.ai` + `https://edge.cloudfield.ai`, with everyone
still on the old pair warned first, counted, and then answered with a `410`
that names the new address.

**Status:** written 2026-11-27, not run. Every step is the owner's, or an
agent's he runs on the hosted edge's host. The retirement date is
`2026-12-15`, which the owner chose on 2026-11-27; the edge only shows
it. It follows
[the retirement plan](2026-11-26-retire-address-pair.md) and the operator
part of [edge-retire.md](../edge-retire.md#for-edge-operators). Sentences
marked `TODO(after #1131)` describe a pull request that is not merged yet.

## Assumptions

- The hosted edge runs in Docker Compose from
  [`packaging/edge/compose.yaml`](../../packaging/edge/compose.yaml) in
  `/opt/aether-edge`, behind Traefik, under `aether-edge-update`: edge.md
  says the hosted edge follows `stable`, and the updater runs only on
  Compose. Check it first; `systemctl cat aether-edge` prints a unit on a
  systemd install instead:

  ```sh
  sudo docker ps --filter name=aether-edge
  ```

- Its settings are the `environment:` of the `aether-edge` service in
  `/opt/aether-edge/compose.yaml`, with the first pair in
  `AETHER_EDGE_SIGNIN_ORIGIN`, `AETHER_EDGE_RELAY_ORIGIN`,
  `AETHER_EDGE_GITHUB_CLIENT_ID` and
  `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE: /run/secrets/github-client-secret`,
  and the cloudfield.ai pair in `AETHER_EDGE_ORIGIN_PAIRS` with
  `/run/secrets/github-client-secret-2`
  ([rename plan, C3](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist)).
  Print them before changing anything; the output holds client ids, not
  secrets:

  ```sh
  cd /opt/aether-edge && sudo docker compose config --format json | jq '.services["aether-edge"].environment'
  ```

  If any name starts with `CLOUDFIELD_EDGE_`, the refusals in step 15 apply.

On a systemd install the same variables go in
`/etc/aether-edge/aether-edge.env` as `NAME=value`, and:

| Compose | systemd |
| --- | --- |
| `cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge` | `sudo systemctl restart aether-edge` |
| `sudo docker logs aether-edge` | `journalctl -u aether-edge` |
| `sudo docker exec aether-edge aether-edge <command>` | `sudo -u aether-edge aether-edge <command>` |
| `/opt/aether-edge/data` | `/var/lib/aether-edge` |
| first pair's secret `/opt/aether-edge/github-client-secret` | `/etc/aether-edge/github-client-secret`, the unit's credential `github-client-secret` |

## Phase 0: before anything

### 1. The release with the retirement code runs on the edge

Pull requests #1129 (records, `aether-edge retiring`), #1128
(`--retiring-pairs`, notices, banners) and #1134 (`--retired-pairs`, the
`410` answers, the start-up deletion) must be in the running image. The
edge follows `stable`, so this waits for a stable release that contains
#1134 (`6b70f903c`) and its promotion
([edge.md](../edge.md#promoting-an-edge-release-to-stable)). In a clone,
`git tag --contains 6b70f903c` lists the releases with it.

```sh
sudo docker exec aether-edge aether-edge version --json | jq -r .version,.commit
sudo docker exec aether-edge aether-edge serve -h 2>&1 | grep -c -e -retiring-pairs -e -retired-pairs
```

Expect `2` from the second. In a clone, `git merge-base --is-ancestor
6b70f903c <commit> && echo yes` prints `yes` for the printed commit.

- Undo: nothing changed.
- Cost to users: none.

### 2. Back up edge.db

```sh
sudo docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
```

Expect `backed up /var/lib/aether-edge/edge.db (schema <n>) to
/var/lib/aether-edge/backups/edge-v<n>-<time>.db`. Copy it off the machine.
It holds the sign-ins step 15 deletes: never restore it after step 15 except
under an image with `AETHER_EDGE_RETIRED_PAIRS` set, which deletes them
again at start.

- Undo: delete the file.
- Cost to users: none.

### 3. Record the edge key fingerprint

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.onaether.dev/v1/edge | jq -r .fingerprint
sudo ssh-keygen -lf /opt/aether-edge/data/edge_key
```

All three print the same `SHA256:` fingerprint. Servers moving by hand
compare against it ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).

- Undo: nothing changed.
- Cost to users: none.

## Phase 1: mark the old pair retiring

### 4. Set AETHER_EDGE_RETIRING_PAIRS

The old pair is the first pair. Marking it retiring needs no swap,
because the option names a pair by its relay origin. Retiring it later
does need the swap (step 14). Add to the service's `environment:`:

```yaml
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15
```

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep 'aether-edge: retiring'
```

Expect `aether-edge: retiring relay=https://edge.onaether.dev
replacement=https://edge.cloudfield.ai date=2026-12-15`. A date that is not
real is refused: `aether-edge: --retiring-pairs: the date "<date>" of
https://edge.onaether.dev is not a real date written YYYY-MM-DD, such as
2027-03-01`.

The owner's checks against the live edge:

```sh
curl -sS -D - -o /dev/null -H 'Aether-Edge-Version: 6' https://edge.onaether.dev/v1/edge | grep -i '^aether-edge-retiring'
curl -s -H 'Accept: text/html' https://auth.onaether.dev/signin | grep 'is retiring'
```

Expect `Aether-Edge-Retiring: https://edge.cloudfield.ai; date=2026-12-15`, and
`<p class="warning" role="status">This address of the edge is retiring on
2026-12-15. Its new address is <code>https://auth.cloudfield.ai</code>: sign in
there.</p>`. The same request to `https://edge.cloudfield.ai/v1/edge`
carries no such header.

On a server enrolled at `https://edge.onaether.dev` whose build has #1132,
once it has connected again, `sudo cloudfield-server edge status` prints
`retiring     https://edge.onaether.dev is retiring on 2026-12-15; move this
server with: sudo cloudfield-server edge move https://edge.cloudfield.ai`,
the journal logs the same as a warning at each connection, and super admins
see a warning with the move and restart commands on Settings, Remote access
and a card on Needs you
([edge-retire.md](../edge-retire.md#moving-a-server)).

- Undo: remove the line and run the same `docker compose up`. Origins
  recorded meanwhile stay in `edge.db` ([privacy.md](../privacy.md)).
- Cost to users: the restart drops relayed connections for a few seconds.
  A device sign-in pending at the restart is deleted, and its person runs
  `cloudfield login` again. From now the edge records which pair each
  server, device token and session uses.

## Phase 2: watch who is left

### 5. Read the report

```sh
sudo docker exec aether-edge aether-edge retiring --origin https://edge.onaether.dev
```

- The first line is the recording period. Counts cover only that period.
- The server table (`SERVER ID  NAME  OWNER LOGIN  BUILD  LAST ENROLLED OR
  CLAIMED`) lists claimed servers whose last enrollment or claim was at
  `https://edge.onaether.dev`. A build below v0.9.0 must update before
  `edge move`; below v0.8.7 its connectors work at one address only.
- `device tokens issued here`: tokens issued at `https://auth.onaether.dev`
  since recording began, by last use; `device tokens issued earlier, first
  seen here`: older tokens next used here. Both are deleted in step 15.
- `accounts with a session here`: browser sign-ins there, deleted in step 15.
- `accounts with device tokens here and none recorded at another origin`:
  people who have not signed in at the new address at all.
- `servers with connector requests through here`: servers whose connectors
  still use `https://edge.onaether.dev/mcp`.
- `unknown origin`: servers, device tokens and sessions not used since
  recording began. Step 15 deletes those tokens and sessions on both pairs.

An empty list is not proof that nobody is left; the report says why in its
last lines.

- Undo: nothing changed (the command opens `edge.db` read-only).
- Cost to users: none.

### 6. Read the metrics

edge.md says to scrape from a container sharing the edge's network and
gives no command; one way:

```sh
sudo docker run --rm --network container:aether-edge curlimages/curl -s http://127.0.0.1:9464/metrics |
  grep -e aether_edge_servers_by_origin -e aether_edge_device_logins_total -e aether_edge_connector_requests_total
```

`aether_edge_servers_by_origin{origin="https://edge.onaether.dev"}` counts
servers connected there now, unclaimed ones too, which the report misses.
The other two count sign-ins and connector requests per address since the
edge last started.

- Undo: nothing changed.
- Cost to users: none.

### 7. Wait

Recommendation, the owner decides: at least 30 days after step 4. A
browser session ends after 30 days unused, and a device token never
expires, so every token not used during the wait is deleted in step 15
and its person signs in again. The edge's own refusal names 30 days as the
example. The date is only text: to change it, edit the line and restart.

- Undo: n/a.
- Cost to users: none.

### 8. Contact owners

The report lists the owner login of each server still on the old pair.
Writing to them is outside the product, and whether to is the owner's
decision: the plan sends no mail
([plan, For the owner to decide](2026-11-26-retire-address-pair.md#for-the-owner-to-decide)),
and [privacy.md](../privacy.md) would have to allow it. Point them to
[edge-retire.md](../edge-retire.md).

- Undo: n/a.
- Cost to users: none.

## Phase 3: outside the product, before retiring

In this order. None of it changes the edge.

### 9. Listings and their reviewer sign-in

On `main` both listing packages already name the new address:

```sh
git show origin/main:packaging/claude-connector/listing.json | jq -r .server_url
git show origin/main:packaging/chatgpt-plugin/mcp.json | jq -r .mcpServers.aether.url
```

Both print `https://edge.cloudfield.ai/mcp`. Any submission follows
[claude-connector.md](../claude-connector.md#submit-in-the-portal) and
[chatgpt-plugin.md](../chatgpt-plugin.md#submit-in-the-portal) at that URL;
the reviewer chooses **Sign in with the reviewer account**, signs in on
`auth.cloudfield.ai` and consents on `edge.cloudfield.ai`
([rename plan, C8](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist)).
A listing already published at `https://edge.onaether.dev/mcp` must move
first: for Claude, write to `mcp-review@anthropic.com`; for ChatGPT, OpenAI
says "To change an existing MCP server's URL, contact support".

- Undo: as each portal allows.
- Cost to users: none.

### 10. The updater and monitoring probes

The updater fetches `<relay>/v1/edge` before each update and after it. At
a retired relay that answers `410`: every run waits 300 seconds and stops
with `GET https://edge.onaether.dev/v1/edge failed; the running edge must
answer before an update`, and a rollback cannot pass its check. In
`/etc/aether-edge-update.env`:

```sh
AETHER_EDGE_UPDATE_RELAY_URL=https://edge.cloudfield.ai
```

```sh
grep UPDATE_RELAY_URL /etc/aether-edge-update.env
curl -fsS -H 'Aether-Edge-Version: 1' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
```

The first prints only that line (a `CLOUDFIELD_EDGE_UPDATE_RELAY_URL`
would win over it); the second, the updater's own request, prints the
fingerprint from step 3. Move every probe that reads a page or `/v1/` on
`auth.onaether.dev` or `edge.onaether.dev` to the cloudfield.ai hosts.
`/healthz` keeps answering `ok` on a retired host; keep the certificate
expiry probes on all four host names.

- Undo: set the old value back.
- Cost to users: none.

### 11. The OpenAI domain challenge

The edge serves one challenge token on every relay host, and a retired host
answers `410` there too. If a ChatGPT submission relies on
`edge.onaether.dev`, verify `edge.cloudfield.ai` in the portal before step
15 ([chatgpt-plugin.md](../chatgpt-plugin.md#submit-in-the-portal), step 4).
While a review or support case is open, do not change the token.

```sh
curl -sS -o /dev/null -w '%{http_code}\n' https://edge.cloudfield.ai/.well-known/openai-apps-challenge
```

`200`: a token is served. `404`: none is set, and nothing relies on one.

- Undo: set the previous `AETHER_EDGE_OPENAI_CHALLENGE` and restart.
- Cost to users: none.

### 12. Mail

`team@onaether.dev` keeps receiving: forward it to `team@cloudfield.ai` at
the mail provider. Send a test message to the old address and check that it
arrives.

- Undo: remove the forward.
- Cost to users: none.

### 13. The website redirect

`onaether.dev` keeps redirecting to the same path on `cloudfield.ai`:

```sh
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://onaether.dev/docs/privacy
```

Expect a `3xx` status and `https://cloudfield.ai/docs/privacy`.

- Undo: n/a; nothing changes here.
- Cost to users: none.

## Phase 4: retire

### 14. Swap the pairs

`--retired-pairs` refuses a host of the first pair, so the cloudfield.ai pair
becomes the first and the old pair an `AETHER_EDGE_ORIGIN_PAIRS` entry, with
its own app and secret. No pair is preferred: servers see only the order of
`relay_origins` change.

```yaml
      # before
      AETHER_EDGE_SIGNIN_ORIGIN: https://auth.onaether.dev
      AETHER_EDGE_RELAY_ORIGIN: https://edge.onaether.dev
      AETHER_EDGE_GITHUB_CLIENT_ID: <onaether-client-id>
      AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE: /run/secrets/github-client-secret
      AETHER_EDGE_ORIGIN_PAIRS: https://auth.cloudfield.ai https://edge.cloudfield.ai <cloudfield-client-id> /run/secrets/github-client-secret-2
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15

      # after
      AETHER_EDGE_SIGNIN_ORIGIN: https://auth.cloudfield.ai
      AETHER_EDGE_RELAY_ORIGIN: https://edge.cloudfield.ai
      AETHER_EDGE_GITHUB_CLIENT_ID: <cloudfield-client-id>
      AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE: /run/secrets/github-client-secret-2
      AETHER_EDGE_ORIGIN_PAIRS: https://auth.onaether.dev https://edge.onaether.dev <onaether-client-id> /run/secrets/github-client-secret
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15
```

On systemd, swap the two secret files' contents instead of the paths: the
unit always loads `/etc/aether-edge/github-client-secret` for the first
pair.

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep -e 'aether-edge: serving' -e 'aether-edge: retiring' | tail -3
```

Expect `aether-edge: serving signin=https://auth.cloudfield.ai
relay=https://edge.cloudfield.ai metrics=<address>`, then `aether-edge:
serving signin=https://auth.onaether.dev relay=https://edge.onaether.dev`,
then the retiring line of step 4. Both `curl` lines of step 3 still print
the same fingerprint. Sign in once on each sign-in host with GitHub: each
uses its own OAuth app.

- Undo: put the before lines back and restart.
- Cost to users: the restart drops relayed connections for a few seconds.

### 15. Retire the old pair

In one edit: the old pair leaves `AETHER_EDGE_ORIGIN_PAIRS` entirely,
`AETHER_EDGE_RETIRED_PAIRS` names it, and its `AETHER_EDGE_RETIRING_PAIRS`
entry goes (with one served pair left, the option is refused). Keep
`auth.onaether.dev` and `edge.onaether.dev` in the Traefik router rule.

```yaml
      AETHER_EDGE_SIGNIN_ORIGIN: https://auth.cloudfield.ai
      AETHER_EDGE_RELAY_ORIGIN: https://edge.cloudfield.ai
      AETHER_EDGE_GITHUB_CLIENT_ID: <cloudfield-client-id>
      AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE: /run/secrets/github-client-secret-2
      AETHER_EDGE_RETIRED_PAIRS: https://auth.onaether.dev https://edge.onaether.dev https://edge.cloudfield.ai
```

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep -e 'edge: deleted the sign-ins' -e 'aether-edge: retired'
```

Expect, in this order:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth.onaether.dev relay=https://edge.onaether.dev replacement=https://edge.cloudfield.ai
```

If the edge does not start, `sudo docker logs aether-edge` ends with
`aether-edge: <refusal>`:

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth.onaether.dev is still served as --signin-origin or --relay-origin; first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair` | Step 14 first |
| `--retired-pairs: https://auth.onaether.dev is still served; remove its pair from --origin-pairs in the same edit` | Remove `AETHER_EDGE_ORIGIN_PAIRS` |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove `AETHER_EDGE_RETIRING_PAIRS` |
| `AETHER_EDGE_SIGNIN_ORIGIN names https://auth.onaether.dev, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN` (the same for `AETHER_EDGE_RELAY_ORIGIN` and `AETHER_EDGE_ORIGIN_PAIRS`) | Set the `AETHER_EDGE_` names to the new values, or drop the `CLOUDFIELD_EDGE_` ones |
| `this edge uses the data directory <dir> from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to <dir>, or pass --data <dir> on the command line, which both images read` | Set `AETHER_EDGE_DATA` |
| `edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens <n>, browser sessions <n>, pending device sign-ins <n>), since this edge has never recorded which pair issued them. ...` | Step 4 never ran on this database: go back to step 4 and wait |
| `--retired-pairs names <n> pairs; an edge keeps at most 16`, or `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: ...` | Fewer entries; cannot happen with one |

A refused start deletes nothing. Restore the step 14 lines to serve again.

The owner's checks against the live edge:

```sh
curl -sS -i https://edge.onaether.dev/v1/edge
curl -s -H 'Accept: text/html' https://auth.onaether.dev/ | grep 'is retired'
curl -fsS https://edge.onaether.dev/healthz
```

The first answers status `410`, `Cache-Control: no-store`, and:

```json
{"error":"https://edge.onaether.dev is retired; this edge is at https://edge.cloudfield.ai. Move a server with: cloudfield-server edge move https://edge.cloudfield.ai. Sign a device in with: cloudfield login --edge https://edge.cloudfield.ai","retired":{"replacement":"https://edge.cloudfield.ai"}}
```

The second prints `<p><code>https://auth.onaether.dev</code> is retired.
This edge is now at <code>https://auth.cloudfield.ai</code>: sign in
there.</p>`, the third `ok`. Then
`sudo docker exec aether-edge aether-edge retiring` still lists the old
pair: a server there has not moved and can no longer enroll there. The
metric `aether_edge_retired_requests_total{origin,kind}` (scraped as in step
6) counts what still arrives at each old host, by `server`, `client`,
`connector` and `browser`.

**What cannot be undone.** The device tokens, browser sessions and pending
sign-ins this start deleted stay deleted: those people sign in again at
`auth.cloudfield.ai`, and servers keep their record of each device and its
approval. From here a rollback is of the image only, never of the database:
a backup from before this step holds the deleted tokens, and an image
older than #1134 accepts them at every served pair. With the updater, that
rules out `aether-edge-update --rollback --restore-backup` to a release from
before this step.

- Undo, partly: restore the step 14 lines and restart. Both hosts are served
  again; the deleted sign-ins are not.
- Cost to users: every device token and browser session issued at
  `auth.onaether.dev`, and every one with no recorded origin, is signed out.
  A server still at `edge.onaether.dev` is unreachable through the edge
  until its admin moves it; connectors added at `https://edge.onaether.dev/mcp`
  fail; join and claim links made there stop working.
  On a server whose build has #1132, `sudo cloudfield-server edge move
  https://edge.cloudfield.ai` still moves it, on the signed `retired` notice
  the new address sends, and takes effect at the restart
  ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).
  With a CLI that has #1133, `cloudfield logout --edge
  https://edge.onaether.dev` deletes the dead local token and prints that
  the edge did not confirm the sign-out. TODO(after #1131):
  `cloudfield mcp` marks connectors at the retired address.

## Phase 5: after

### 16. Keep DNS, routing and the entry

Keep the A records of `auth.onaether.dev` and `edge.onaether.dev` pointing at
the edge, both hosts in the Traefik router rule with their certificates,
and `AETHER_EDGE_RETIRED_PAIRS` for good. A backup restored later holds the
deleted tokens again, and the start with the entry deletes them.

- Undo: n/a.
- Cost to users: none.

### 17. The old GitHub OAuth app

The app whose callback is `https://auth.onaether.dev/signin/github/callback`
is no longer used. Its client id was `AETHER_EDGE_GITHUB_CLIENT_ID` until
step 14 and the third field of `AETHER_EDGE_ORIGIN_PAIRS` until step 15; its
secret is `/opt/aether-edge/github-client-secret`. In GitHub, **Settings →
Developer settings → OAuth Apps**, delete the app or generate a new secret
and drop the old one. Then delete the secret file and its `secrets:` entries
from `compose.yaml`.

- Undo: rotating, yes, with the new secret; deleting, no: undoing step 15
  would then need a new app and its client id.
- Cost to users: none.

### 18. Keep onaether.dev registered

Do not let the `onaether.dev` registration lapse. A server or client that
never moved still dials these host names, and a printed join link still
opens them. A server checks only that the edge presents the public key it
pinned, not that it holds the private key, so whoever held the name would
receive server control connections, device tokens and connector tokens.

"Removed", in the plan's terms, means deleting the two DNS records while
keeping the `AETHER_EDGE_RETIRED_PAIRS` entry: the names then answer
nothing. The plan does not make that safe, nor giving up the domain, and
counts cannot show that nobody is left.

## Summary

| Step | Reversible? | How |
| --- | --- | --- |
| 1. Release check | yes | Changes nothing |
| 2. Back up edge.db | yes | Delete the copy |
| 3. Record the fingerprint | yes | Changes nothing |
| 4. Mark retiring | yes | Remove `AETHER_EDGE_RETIRING_PAIRS`, restart; recorded origins stay |
| 5. Report | yes | Read-only |
| 6. Metrics | yes | Read-only |
| 7. Wait | yes | Change the date, restart |
| 8. Contact owners | no | Mail cannot be recalled |
| 9. Listings | partly | As each directory allows |
| 10. Updater and probes | yes | Set the old URL back |
| 11. OpenAI challenge | partly | Set the old token back while no review relies on the new one |
| 12. Mail forward | yes | Remove the forward |
| 13. Website redirect | yes | Unchanged |
| 14. Swap | yes | Restore the before lines, restart |
| 15. Retire | partly | Restore the step 14 lines, restart; deleted sign-ins stay deleted; roll back the image only, never the database |
| 16. Keep DNS and entry | yes | Nothing to undo |
| 17. Old OAuth app | partly | Rotating, yes; deleting, no |
| 18. Keep the domain | no | A lapsed domain may not come back |
