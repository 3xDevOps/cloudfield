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
part of [edge-retire.md](../edge-retire.md#for-edge-operators).

## Notice

`2026-12-15` is 18 days after the day it was chosen, less than the 30 days
step 9 recommends, and the warning period is shorter still: it starts only
when step 5 is done on a release that has the code (step 1), which no beta
or stable release has on 2026-11-28. What the shorter period costs, at
step 17:

- Every device token and browser session the edge recorded at
  `auth.onaether.dev` is deleted, whatever the notice. Those people sign in
  again at `auth.cloudfield.ai`.
- Every device token and browser session that was not used between step 5
  and step 17 is deleted too, on both pairs. The shorter the period, the
  more of these. Those people sign in again.
- A server not moved is unreachable through the edge until its admin runs
  `sudo cloudfield-server edge move https://edge.cloudfield.ai` and
  restarts it.

Step 15 is the go/no-go on those numbers.

## Check the host first

Nothing in the repository says how the hosted edge is deployed. The steps
are written for Docker Compose from
[`packaging/edge/compose.yaml`](../../packaging/edge/compose.yaml) in
`/opt/aether-edge`, behind Traefik, updated by `aether-edge-update`
([edge.md](../edge.md#automatic-updates) says the hosted edge follows
`stable`, and the updater runs only on Compose). Verify each line on the
host before step 1; where one differs, use the value found wherever a step
names the default.

- [ ] **How it runs.** One of these prints something:

  ```sh
  sudo docker inspect aether-edge --format '{{index .Config.Labels "com.docker.compose.project.working_dir"}} {{index .Config.Labels "com.docker.compose.project.config_files"}}'
  systemctl cat aether-edge
  ```

  The first prints the Compose directory and file of the running
  container, expected `/opt/aether-edge /opt/aether-edge/compose.yaml`. The
  second prints the unit and its drop-ins on a systemd install; use the
  [systemd column](#on-a-systemd-install) then.
- [ ] **Command line, data, secrets, proxy.** No value of a secret is
  printed:

  ```sh
  cd /opt/aether-edge && sudo docker compose config --format json | jq '
    (.services["aether-edge"] | {command, volumes, secrets,
      rules: (.labels // {} | with_entries(select(.key | endswith(".rule"))))}),
    .secrets'
  ```

  Expect `command` `null` (a flag on the command line wins over every
  variable below); one bind of a host directory to `/var/lib/aether-edge`,
  expected `/opt/aether-edge/data`; the secrets `github-client-secret` and
  `github-client-secret-2`, each with its `file`; and a Traefik rule that
  names all four hosts. A secret named `<name>` is the file
  `/run/secrets/<name>` in the container. Each secret file is readable by
  uid 65532 only: `sudo ls -ln` shows owner `65532` and mode `-r--------`.
- [ ] **Settings in effect.** For each option the edge uses the
  `CLOUDFIELD_EDGE_<NAME>` variable when it is not empty, else
  `AETHER_EDGE_<NAME>`, without a warning when both are set
  (`internal/envname`). This prints the value in effect of every option
  this runbook relies on, and the names of all edge variables that are
  set:

  ```sh
  cd /opt/aether-edge && sudo docker compose config --format json | jq -r '
    (.services["aether-edge"].environment // {}) as $e
    | (("SIGNIN_ORIGIN RELAY_ORIGIN GITHUB_CLIENT_ID GITHUB_CLIENT_SECRET_FILE ORIGIN_PAIRS RETIRING_PAIRS RETIRED_PAIRS DATA" | split(" "))[]
       | "\(.) = \(($e["CLOUDFIELD_EDGE_" + .] // "") as $new
           | if $new != "" then $new + "  (from CLOUDFIELD_EDGE_" + . + ")" else ($e["AETHER_EDGE_" + .] // "") end)"),
      "set: \([$e | keys[] | select(test("^(CLOUDFIELD|AETHER)_EDGE_"))] | join(" "))"'
  ```

  Never print the whole `environment:`: `AETHER_EDGE_GITHUB_CLIENT_SECRET`
  (or its `CLOUDFIELD_EDGE_` name) holds the client secret itself. Expect
  the old pair in `SIGNIN_ORIGIN`, `RELAY_ORIGIN` and `GITHUB_CLIENT_ID`
  with `GITHUB_CLIENT_SECRET_FILE = /run/secrets/github-client-secret`, the
  cloudfield.ai pair in `ORIGIN_PAIRS` with
  `/run/secrets/github-client-secret-2`
  ([rename plan, C3](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist)),
  `DATA` empty (the default, `/var/lib/aether-edge`), and no
  `GITHUB_CLIENT_SECRET` name in the `set:` line. If a value comes from a
  `CLOUDFIELD_EDGE_` name, every edit below goes to that name, and the
  `AETHER_EDGE_` name of the same option gets the same value in the same
  edit: only two such conflicts are refused, both at step 17. The steps
  call this command **print the settings**.
- [ ] **The updater.**

  ```sh
  systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
  systemctl show aether-edge-update.service -p Environment -p EnvironmentFiles
  sudo grep -E '^(CLOUDFIELD|AETHER)_EDGE_UPDATE_' /etc/aether-edge-update.env
  ```

  Expect `enabled` twice, both properties empty, and
  `AETHER_EDGE_UPDATE_RELAY_URL=https://edge.onaether.dev`. The updater
  reads `/etc/aether-edge-update.env` unless
  `CLOUDFIELD_EDGE_UPDATE_CONFIG` or `AETHER_EDGE_UPDATE_CONFIG` in the
  unit's environment names another file; a line in the file overrides the
  same name in the environment; and for each setting a `CLOUDFIELD_EDGE_`
  value that is not empty wins over the `AETHER_EDGE_` one. Other
  `_UPDATE_COMPOSE_DIR`, `_UPDATE_DIGEST_FILE` or `_UPDATE_STATE` values
  replace `/opt/aether-edge`, `/opt/aether-edge/.env` and
  `/var/lib/aether-edge-update` below.

### On a systemd install

The variables are in `/etc/aether-edge/aether-edge.env` as `NAME=value`,
with the same precedence. There is no updater: skip
[Pausing the updater](#pausing-the-updater) and the updater part of step
11.

| Compose | systemd |
| --- | --- |
| `cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge` | `sudo systemctl restart aether-edge`, then `sudo -u aether-edge aether-edge healthcheck --ready`, which exits 0 once the edge is ready |
| `sudo docker logs aether-edge` | `journalctl -u aether-edge` |
| `sudo docker exec aether-edge aether-edge <command>` | `sudo -u aether-edge aether-edge <command>`. It does not read the unit's environment file: add `--data <dir>` when the data directory is not `/var/lib/aether-edge` |
| `/opt/aether-edge/data` | `/var/lib/aether-edge` |
| print the settings | `sudo grep -E '^(CLOUDFIELD\|AETHER)_EDGE_(SIGNIN_ORIGIN\|RELAY_ORIGIN\|GITHUB_CLIENT_ID\|GITHUB_CLIENT_SECRET_FILE\|ORIGIN_PAIRS\|RETIRING_PAIRS\|RETIRED_PAIRS\|DATA)=' /etc/aether-edge/aether-edge.env`; `sudo grep -c -E '^(CLOUDFIELD\|AETHER)_EDGE_GITHUB_CLIENT_SECRET=' /etc/aether-edge/aether-edge.env` prints `0`; `systemctl cat aether-edge \| grep -E '^(ExecStart\|EnvironmentFile\|LoadCredential)='` shows `ExecStart=/usr/local/bin/aether-edge serve` with no flag |
| first pair's secret, the file of the secret `github-client-secret` | `/etc/aether-edge/github-client-secret`, always: the unit loads it as the credential `github-client-secret` and sets `AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE` to it |
| second pair's secret, `/run/secrets/github-client-secret-2` | `/run/credentials/aether-edge.service/github-client-secret-2`, loaded by a drop-in's `LoadCredential=github-client-secret-2:/etc/aether-edge/github-client-secret-2` |
| metrics (step 7) | `curl -s http://127.0.0.1:9464/metrics` on the host |

### Pausing the updater

The updater runs `docker compose up` in the same directory, daily at 04:00
UTC with up to an hour's delay, and at boot after a missed window. A run
during a manual switch could start a half-edited `compose.yaml`, and a
failed update rolls back by itself, restoring its pre-update database
backup when the older release cannot run the database. Before every manual
`docker compose up` (steps 5, 16, 17 and 19, and a changed date or token):

```sh
sudo systemctl stop aether-edge-update.timer
systemctl is-active aether-edge-update.service aether-edge-update-recover.service
sudo flock -n /var/lib/aether-edge-update/lock true && echo free
sudo grep '^state=' /var/lib/aether-edge-update/state
```

Expect `inactive` twice, `free`, and `state=idle` or no state file. If a
run is active or the lock is held, wait for it to end; never `systemctl
stop aether-edge-update.service`, which marks a switch in progress for
rollback and rolls it back. `state=switching` or `state=rolling-back`
means a run was interrupted: `sudo aether-edge-update --recover` finishes
or rolls it back, and the edge must pass its checks before any edit.
`state=held` changes nothing by itself. `state=broken` is in
[edge.md](../edge.md#when-an-update-stops).

Once the step's checks pass:

```sh
sudo systemctl start aether-edge-update.timer
```

The timer is `Persistent=true`: started after a 04:00 UTC window passed
while it was stopped, it runs the updater at once.

## Phase 0: before anything

### 1. The release with the retirement code runs on the edge

Pull requests #1129 (records, `aether-edge retiring`), #1128
(`--retiring-pairs`, notices, banners) and #1134 (`--retired-pairs`, the
`410` answers, the start-up deletion) must be in the running image. The
edge follows `stable`, so this waits for a stable release that contains
#1134 (`6b70f903c`) and its promotion
([edge.md](../edge.md#promoting-an-edge-release-to-stable)). In a clone,
`git tag --contains 6b70f903c` lists the releases with it. On 2026-11-28 those
are `v0.10.0-nightly.6` and `v0.10.0-nightly.7`; `v0.10.0-beta.1` does not
contain it, so the beta and the stable to wait for are later ones. The
warning period cannot start before that release runs here.

```sh
sudo docker exec aether-edge aether-edge version --json | jq -r .version,.commit
sudo docker exec aether-edge aether-edge serve -h 2>&1 | grep -c -e -retiring-pairs -e -retired-pairs
```

Expect `2` from the second. In a clone, `git merge-base --is-ancestor
6b70f903c <commit> && echo yes` prints `yes` for the printed commit.

- Undo: nothing changed.
- Cost to users: none.

### 2. Back up edge.db and check the edge key's copy

```sh
sudo docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
```

Expect `backed up /var/lib/aether-edge/edge.db (schema <n>) to
/var/lib/aether-edge/backups/edge-v<n>-<time>.db`. Copy it off the machine.

What it covers: `edge.db` only, as it was at that moment. The edge key
`edge_key` is a separate file in the data directory, and the edge does not
start without it beside a database that holds accounts or servers. Check
that the offline copy [edge.md](../edge.md#backup-and-recovery) asks for
exists; if it does not, add `--with-key`, which also writes
`<backup>.key`, and keep that file encrypted: whoever holds it can act as
the edge. A backup holds neither `compose.yaml`, `.env`, the secret files
nor the updater's state.

This backup is for a failure before step 17. It is not a way back from
step 17 ([What cannot be undone](#17-retire-the-old-pair)): it predates
step 5, so no row in it has a recorded origin and nothing in it says
recording ever ran, and a start with `AETHER_EDGE_RETIRED_PAIRS` on it is
refused with the `never recorded` refusal of step 17. Restored after step
5, it drops every origin recorded since, and the warning period starts
over.

- Undo: delete the file.
- Cost to users: none.

### 3. Record the edge key fingerprint

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.onaether.dev/v1/edge | jq -r .fingerprint
sudo ssh-keygen -lf /opt/aether-edge/data/edge_key
```

The first two print the same `SHA256:` fingerprint, and the third prints it
in `256 SHA256:<fingerprint> aether edge (ED25519)`. Publish it where
server admins can read it over a channel other than the edge: a server
moved by hand is checked against it
([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).

- Undo: nothing changed.
- Cost to users: none.

### 4. Check that the cloudfield.ai pair works

Steps 16 and 17 run these again; a `410` on the old hosts says nothing
about the pair that stays.

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r '.signin_origin, .fingerprint, .retiring'
sudo docker exec aether-edge aether-edge healthcheck --ready --json | jq .servers.claimed
```

Expect `https://auth.cloudfield.ai`, the fingerprint of step 3 and `null`;
note the count of claimed servers. Then, by hand:

- **A fresh browser sign-in.** In a private browser window, open
  `https://auth.cloudfield.ai/signin` and sign in with GitHub. A window
  that already has a session there is sent on without going to GitHub and
  tests nothing. A wrong secret ends in `GitHub sign-in: exchange code:
  <error>`.
- **A fresh device sign-in.** On a computer not signed in there,
  `cloudfield login --edge https://edge.cloudfield.ai` ends with `signed in
  to edge.cloudfield.ai as <login>; this device is "<label>"`.
- **A kept token and a relayed connection.** On a computer that signed in
  at `https://edge.cloudfield.ai` earlier, `cloudfield servers --edge
  https://edge.cloudfield.ai` lists its servers, and `cloudfield link
  <server id> --edge https://edge.cloudfield.ai`, for a server enrolled
  there, prints `linked to server <server id> through edge.cloudfield.ai as
  <name> (<role>)`.
- **A server connected.** On a server whose `edge-url` is
  `https://edge.cloudfield.ai`, `sudo cloudfield-server edge status` exits
  0 with `connection   connected since <time>; the edge last answered <n>s
  ago`. After a restart of the edge, `<time>` is later than the restart.
- **A connector.** A connector added at `https://edge.cloudfield.ai/mcp`
  answers a tool call.

- Undo: nothing changed, but the device sign-in leaves a device token:
  `cloudfield logout --edge https://edge.cloudfield.ai` on a test computer.
- Cost to users: none.

## Phase 1: mark the old pair retiring

### 5. Set AETHER_EDGE_RETIRING_PAIRS

The warning period starts here. The old pair is the first pair. Marking it
retiring needs no swap, because the option names a pair by its relay
origin. Retiring it later does need the swap (step 16).

[Pause the updater](#pausing-the-updater). Add to the service's
`environment:`:

```yaml
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15
```

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep 'aether-edge: retiring'
```

Expect `aether-edge: retiring relay=https://edge.onaether.dev
replacement=https://edge.cloudfield.ai date=2026-12-15`, and that value as
`RETIRING_PAIRS` when you print the settings. A date that is not real is
refused: `aether-edge: --retiring-pairs: the date "<date>" of
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
carries no such header. Run the checks of step 4, then start the updater's
timer again.

On a server enrolled at `https://edge.onaether.dev` whose build has #1132,
once it has connected again, `sudo cloudfield-server edge status` prints
`retiring     https://edge.onaether.dev is retiring on 2026-12-15; move this
server with: sudo cloudfield-server edge move https://edge.cloudfield.ai`,
the journal logs the same as a warning at each connection, and super admins
see a warning with the move and restart commands on Settings, Remote access
and a card on Needs you
([edge-retire.md](../edge-retire.md#moving-a-server)).

From this start, and only while the option stays set, the edge records for
every pair the sign-in origin each device token, browser session and
pending device sign-in is issued at; for a token or session issued
earlier, the origin of the pair it is next used at; and each claimed
server's relay origin at each enrollment or claim
([edge.md](../edge.md#monitoring), [privacy.md](../privacy.md)).

- Undo: remove the line and run the same `docker compose up`. Origins
  recorded meanwhile stay in `edge.db`. **An undo disarms step 17's
  safety check.** That check refuses only an edge that never recorded, not
  one that recorded for too short a time, so after an undo, or with the
  option set for a day, step 17 starts and deletes every sign-in not used
  while the edge recorded. Without the option the edge also issues tokens
  and sessions with no recorded origin, which step 17 deletes on both
  pairs. After an undo, do this step again and wait the whole period
  before step 17.
- Cost to users: the restart drops relayed connections for a few seconds.
  A device sign-in that was pending at this restart is deleted, since it
  was started before the edge recorded origins; its person runs
  `cloudfield login` again. Removing the option does not bring it back.

## Phase 2: watch who is left

### 6. Read the report

```sh
sudo docker exec aether-edge aether-edge retiring --origin https://edge.onaether.dev
```

- The first line is the last known recording period: when it began, when
  the edge last started recording, and whether it stopped cleanly. It does
  not filter the counts. Each count is of rows that exist now, by last use
  within 1, 7 and 30 days and in all; a revoked token has no row, and an
  expired session is not counted.
- The server table (`SERVER ID  NAME  OWNER LOGIN  BUILD  LAST ENROLLED OR
  CLAIMED`) lists the claimed servers whose last recorded enrollment or
  claim was at `https://edge.onaether.dev`, or `no server last enrolled or
  claimed here`. A build below v0.9.0 must update before `edge move`; below
  v0.8.7 its connectors work at one address only.
- `device tokens issued here`: tokens issued at `https://auth.onaether.dev`
  while the edge recorded. `device tokens issued earlier, first seen
  here`: tokens issued before, whose first use since came in on a host of
  the old pair. Step 17 deletes both, however recently they were used.
- `accounts with a session here`: accounts with an unexpired browser
  session recorded at `https://auth.onaether.dev`. Step 17 deletes those
  sessions.
- `accounts with device tokens here and none recorded at another origin`:
  accounts all of whose recorded device tokens are at
  `https://auth.onaether.dev`. A token with no recorded origin does not
  count as another, so such an account may still hold one.
- `servers with connector requests through here`: claimed servers for
  which a connector request came in on `https://edge.onaether.dev` and the
  edge sent it on, recorded at most once an hour. The server may have
  refused the request.
- `unknown origin`: the claimed servers that have not enrolled or been
  claimed since recording began, and the device tokens and sessions not
  used since. Step 17 deletes those tokens and sessions on both pairs; the
  servers stay at whatever address they dial.

A token issued at the old address before step 5 and first used since on a
cloudfield.ai host is recorded there and kept. A token recorded at
`https://auth.cloudfield.ai` is kept however long it goes unused.

An empty list is not proof that nobody is left; the report says why in its
last lines. The old pair keeps its section in the report after step 17,
with or without servers.

- Undo: nothing changed (the command opens `edge.db` read-only).
- Cost to users: none.

### 7. Read the metrics

The metrics listener is on loopback inside the container, the image has
no shell or `curl`, and edge.md gives no command for that. This runs the
host's `curl` in the container's network namespace and starts no other
image:

```sh
sudo nsenter -t "$(sudo docker inspect -f '{{.State.Pid}}' aether-edge)" -n \
  curl -s http://127.0.0.1:9464/metrics |
  grep -e aether_edge_servers_by_origin -e aether_edge_device_logins_total -e aether_edge_connector_requests_total -e aether_edge_retired_requests_total
```

`nsenter` is from util-linux, as the `flock` the updater needs is. To use
an image instead, pin it by digest: pull it once, read the digest with
`sudo docker image inspect --format '{{index .RepoDigests 0}}' <image>`,
check it against its publisher's, and run `sudo docker run --rm --network
container:aether-edge <image>@sha256:<digest> ...`. Never run a tag there:
the container shares the edge's network namespace.

`aether_edge_servers_by_origin{origin="https://edge.onaether.dev"}` counts
servers connected there now, unclaimed ones too, which the report misses.
`aether_edge_device_logins_total{origin}` and
`aether_edge_connector_requests_total{origin,protocol}` count device
sign-ins and connector requests per address since the edge last started.
`aether_edge_retired_requests_total` exists only from step 17 on.

- Undo: nothing changed.
- Cost to users: none.

### 8. Contact owners

Right after the first report, not at the end of the wait. The report lists
the owner login of each claimed server recorded on the old pair. Nobody
else can be named: an unclaimed server, a server that has not enrolled
since step 5, and the people behind device tokens and connectors have no
row, and see only the warnings in the product, on builds that show them.
Writing to owners is outside the product, and whether to is the owner's
decision: the plan sends no mail
([plan, For the owner to decide](2026-11-26-retire-address-pair.md#for-the-owner-to-decide)),
and [privacy.md](../privacy.md) would have to allow it. Give the date, and
point them to [edge-retire.md](../edge-retire.md).

- Undo: none; a message cannot be recalled.
- Cost to users: none.

### 9. Wait

Recommendation: at least 30 days after step 5. A browser session ends
after 30 days unused, and a device token never expires, so every token not
used during the wait is deleted in step 17 and its person signs in again.
The edge's own refusal names 30 days as the example, and checks only that
recording ever ran, not for how long.

The owner chose `2026-12-15` knowing it gives less than that, counted from
step 5 and not from the day it was chosen, at the costs under
[Notice](#notice). The date is only text. To change it, edit the line of
step 5 and start the edge as there; the restart drops relayed connections
for a few seconds.

- Undo: n/a.
- Cost to users: none.

## Phase 3: outside the product, before retiring

In this order.

### 10. Listings and their reviewer sign-in

On `main` both listing packages name the new address:

```sh
git show origin/main:packaging/claude-connector/listing.json | jq -r .server_url
git show origin/main:packaging/chatgpt-plugin/mcp.json | jq -r .mcpServers.aether.url
```

Both print `https://edge.cloudfield.ai/mcp`. That is the package in the
repository. Whether anything is submitted or published, at which URL, and
in what review state is only in the two portals: the owner checks there
([rename plan, C2](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist)).

- Nothing at `https://edge.onaether.dev/mcp`: any submission follows
  [claude-connector.md](../claude-connector.md#submit-in-the-portal) and
  [chatgpt-plugin.md](../chatgpt-plugin.md#submit-in-the-portal) at the new
  URL; the reviewer chooses **Sign in with the reviewer account**, signs
  in on `auth.cloudfield.ai` and consents on `edge.cloudfield.ai`
  ([rename plan, C8](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist)).
- A listing, a submission or an open support case at
  `https://edge.onaether.dev/mcp`: it must move before step 17, and no new
  submission goes in until that is settled. For Claude, write to
  `mcp-review@anthropic.com`; for ChatGPT, OpenAI says "To change an
  existing MCP server's URL, contact support". Step 17 waits for their
  answer.

- Undo: as each portal allows; not assured.
- Cost to users: none from this step. People who added a listed connector
  at the old URL add it again at the new one
  ([edge-retire.md](../edge-retire.md#moving-a-connector)).

### 11. The updater's relay URL and monitoring probes

A run of the updater that switches release fetches `<relay URL>/v1/edge`
before the switch and after it; a run that finds the edge up to date never
does. A retired relay answers `410` there. With the old URL after step 17,
an update waits `AETHER_EDGE_UPDATE_PUBLIC_WAIT` seconds (default 300) and
exits 1 with `GET https://edge.onaether.dev/v1/edge failed; the running
edge must answer before an update`, changing nothing. A `--rollback` goes
on with the fingerprint the last update recorded, fails its check after
the switch with `GET https://edge.onaether.dev/v1/edge failed for 300s`,
fails the same check on the way back, and leaves the state `broken`. In
`/etc/aether-edge-update.env`:

```sh
AETHER_EDGE_UPDATE_RELAY_URL=https://edge.cloudfield.ai
```

```sh
sudo grep -E '^(CLOUDFIELD|AETHER)_EDGE_UPDATE_(RELAY_URL|CONFIG)=' /etc/aether-edge-update.env
systemctl show aether-edge-update.service -p Environment -p EnvironmentFiles
curl -fsS --max-time 10 -H 'Aether-Edge-Version: 1' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
```

The first prints that line only, or a `CLOUDFIELD_EDGE_UPDATE_RELAY_URL`
line with the same value, which wins; the second prints both properties
empty, as in [Check the host first](#check-the-host-first); the third, the
updater's own request, prints the fingerprint from step 3.

Move every probe that reads a page or `/v1/` on `auth.onaether.dev` or
`edge.onaether.dev` to the cloudfield.ai hosts. `/healthz` keeps answering
`ok` on a retired host, so it shows only that the proxy still routes the
host; keep it and the certificate expiry probes on all four host names.
Which probes exist is not in the repository: the owner lists them.

- Undo: set the old values back, only while the old pair is served. After
  step 17 the old URL breaks updates and rollbacks as above.
- Cost to users: none.

### 12. The OpenAI domain challenge

The edge holds one challenge token and serves it on every relay host. A
retired host answers `410` there too. Whether a ChatGPT submission exists
and which host it verified is in the OpenAI portal only (step 10). If one
verified `edge.onaether.dev`, verify `edge.cloudfield.ai` in the portal
and see it verified there before step 17
([chatgpt-plugin.md](../chatgpt-plugin.md#submit-in-the-portal), step 4).
While a review or support case is open, do not change the token.

```sh
curl -sS -i https://edge.cloudfield.ai/.well-known/openai-apps-challenge
```

`200` with a `text/plain` body: that body is the token served; compare it
with the one the portal shows. `404` with the body `404 page not found`:
the edge serves none. Neither shows whether a review relies on a
verification made earlier. Any other answer: stop and find out.

- Undo: set the previous `AETHER_EDGE_OPENAI_CHALLENGE` and restart.
- Cost to users: none unless the token changes; that takes a restart,
  which drops relayed connections for a few seconds.

### 13. Mail

Owner check; how mail for `onaether.dev` is delivered is not in the
repository. `team@onaether.dev` must keep receiving
([rename plan](2026-11-15-cloudfield-rename.md#how-the-old-hostnames-keep-working)),
for example by a forward to `team@cloudfield.ai` at the mail provider.
Send a test message to the old address and check that it arrives.

- Undo: remove the forward.
- Cost to users: none.

### 14. The website redirect

Owner check; the redirect is configured outside this repository.
`onaether.dev` is meant to redirect to the same path on `cloudfield.ai`:

```sh
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://onaether.dev/docs/privacy
```

Expect a `3xx` status and `https://cloudfield.ai/docs/privacy`. This is
the website host only: `auth.onaether.dev` and `edge.onaether.dev` never
redirect, since their requests carry codes and tokens.

- Undo: n/a; nothing changes here.
- Cost to users: none.

## Phase 4: retire

### 15. Go or no-go

Immediately before step 16, on the day:

1. Run the report of step 6 and the metrics of step 7 again.
2. Check the report's first line: `since <time>` is the time of step 5. A
   later time means a new period began, after an unclean stop or a start
   more than 10 minutes after the last clean one. Origins recorded before
   it are kept. If the option was unset in between, what was issued then
   has no origin, and step 17 deletes it.
3. For each server still in the table, each nonzero row of the old pair
   and of `unknown origin`, and each server the metric counts on the old
   relay origin, decide: retire anyway, or wait. Whoever is left pays the
   costs under [Notice](#notice).
   A server whose admin cannot be reached stays unreachable through the
   edge until someone with a shell on its host moves it; member SSH keys
   and tailnet access are not affected.
4. Check that steps 10 to 14 are settled, and that step 4 passes.
5. Record the time, the report, the metric lines, the decision and who
   made it, where the owner keeps deployment records, outside this
   repository. The report holds owner logins.

No-go: set a later date in the line of step 5, start the edge as there,
and tell the owners of step 8.

- Undo: nothing changed.
- Cost to users: none.

### 16. Swap the pairs

`--retired-pairs` refuses a host of the first pair, so the cloudfield.ai pair
becomes the first and the old pair an `AETHER_EDGE_ORIGIN_PAIRS` entry, with
its own app and secret. No pair is preferred: servers see only the order of
`relay_origins` change.

[Pause the updater](#pausing-the-updater), then edit:

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

On Compose the paths swap and no file changes: `github-client-secret`
still holds the onaether.dev app's secret, `github-client-secret-2` the
cloudfield.ai app's.

On systemd the paths cannot swap, because the unit always loads
`/etc/aether-edge/github-client-secret` for the first pair. Swap the two
files' contents instead, and keep the old pair's entry at
`/run/credentials/aether-edge.service/github-client-secret-2`. From then
on `/etc/aether-edge/github-client-secret` holds the cloudfield.ai app's
secret, the live one, and `/etc/aether-edge/github-client-secret-2` the
onaether.dev app's.

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep -e 'aether-edge: serving' -e 'aether-edge: retiring' | tail -3
```

Expect `aether-edge: serving signin=https://auth.cloudfield.ai
relay=https://edge.cloudfield.ai metrics=<address>`, then `aether-edge:
serving signin=https://auth.onaether.dev relay=https://edge.onaether.dev`,
then the retiring line of step 5. Print the settings: they show the after
lines. Both `curl` lines of step 3 still print the same fingerprint. Run
the checks of step 4, and the fresh browser sign-in on
`https://auth.onaether.dev/signin` too: each sign-in host uses its own
OAuth app, and a wrong id or secret shows only in a real sign-in. Then
start the updater's timer again, unless step 17 follows at once.

- Undo: put the before lines back and start the edge the same way. On
  systemd, swap the two files' contents back in the same edit.
- Cost to users: the restart drops relayed connections for a few seconds.

### 17. Retire the old pair

First a fresh backup, minutes before the edit, copied off the machine:

```sh
sudo docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
```

The updater stays [paused](#pausing-the-updater); run its checks again.

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

Print the settings before starting: `ORIGIN_PAIRS` and `RETIRING_PAIRS`
are empty, `RETIRED_PAIRS` is the line above, and no value comes from a
name you did not edit.

```sh
cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge
sudo docker logs aether-edge 2>&1 | grep -e 'edge: deleted the sign-ins' -e 'aether-edge: retired'
```

Expect, in this order:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth.onaether.dev relay=https://edge.onaether.dev replacement=https://edge.cloudfield.ai
```

**What this start deletes**, in one transaction, once the listeners are
bound and before the edge serves:

- the device tokens, browser sessions and pending device sign-ins whose
  recorded sign-in origin has the host name `auth.onaether.dev`, whatever
  its port, or the host name of any other sign-in origin the edge recorded
  beside `edge.onaether.dev` that no served pair signs in at;
- those with no recorded origin, on both pairs: the ones not used since
  step 5.

It keeps everything recorded at `https://auth.cloudfield.ai`, used
recently or not.

The deletion commits before the edge answers its health check. A `docker
compose up --wait` that fails or times out is therefore no proof that
nothing was deleted: the `edge: deleted the sign-ins` line is.

If the edge does not start, `sudo docker logs aether-edge` ends with
`aether-edge: <refusal>`:

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth.onaether.dev is still served as --signin-origin or --relay-origin; first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair` | Step 16 first |
| `--retired-pairs: https://auth.onaether.dev is still served; remove its pair from --origin-pairs in the same edit` | Remove `AETHER_EDGE_ORIGIN_PAIRS`, and `CLOUDFIELD_EDGE_ORIGIN_PAIRS` if set |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove `AETHER_EDGE_RETIRING_PAIRS`, and `CLOUDFIELD_EDGE_RETIRING_PAIRS` if set |
| `AETHER_EDGE_SIGNIN_ORIGIN names https://auth.onaether.dev, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN` (the same for `AETHER_EDGE_RELAY_ORIGIN` and `AETHER_EDGE_ORIGIN_PAIRS`) | Set that `AETHER_EDGE_` name to the value in effect, or remove it. Never remove the `CLOUDFIELD_EDGE_` name instead: the edge would then serve the old pair |
| `this edge uses the data directory <dir> from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to <dir>, or pass --data <dir> on the command line, which both images read` | Set `AETHER_EDGE_DATA` to `<dir>` |
| `edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens <n>, browser sessions <n>, pending device sign-ins <n>), since this edge has never recorded which pair issued them. ...` | This database never recorded: step 5 never ran on it, or it was restored from a backup older than step 5. Restore the step 16 lines, then step 5 and the wait |
| `--retired-pairs names <n> pairs; an edge keeps at most 16`, or `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: ...` | Fewer entries; cannot happen with one |

The other refusals are in [edge.md](../edge.md#retired). Between the two
prefixes the edge refuses only the two conflicts in the table, over the
origins and the data directory. A `CLOUDFIELD_EDGE_` value that overrides
the client id, the secret file, or the retiring or retired pairs is used
as it is, which is why the settings are printed first.

A start refused with one of these lines deleted nothing. It also serves
nothing: both pairs are down, and the container restarts into the same
refusal, until the step 16 lines are back and the edge starts.

The owner's checks against the live edge. First the old hosts, both:

```sh
for h in auth.onaether.dev edge.onaether.dev; do
  curl -sS -o /dev/null -w "$h %{http_code} [%{redirect_url}]\n" "https://$h/v1/edge"
  curl -sS "https://$h/v1/edge"
  curl -s -H 'Accept: text/html' "https://$h/" | grep '<p><code>'
done
```

Each host prints `<host> 410 []`, with nothing between the brackets since
a retired host never redirects, then its own origin in:

```json
{"error":"https://edge.onaether.dev is retired; this edge is at https://edge.cloudfield.ai. Move a server with: cloudfield-server edge move https://edge.cloudfield.ai. Sign a device in with: cloudfield login --edge https://edge.cloudfield.ai","retired":{"replacement":"https://edge.cloudfield.ai"}}
```

and then `<p><code>https://auth.onaether.dev</code> is retired. This edge
is now at <code>https://auth.cloudfield.ai</code>: sign in there.</p>` on
the sign-in host, `<p><code>https://edge.onaether.dev</code> is retired.
This edge is now at <code>https://edge.cloudfield.ai</code>.</p>` on the
relay host.

Then the pair that stays: every check of step 4. The claimed-server count
is lower by the servers that were still on the old pair. The kept token
must still list its servers: it shows that the deletion took only what is
listed above.

The metric `aether_edge_retired_requests_total{origin,kind}` (step 7)
counts what still arrives at each old host, by `server`, `client`,
`connector` and `browser`. `curl -fsS https://edge.onaether.dev/healthz`
still prints `ok`.

Once the checks pass, take another backup with the command above and copy
it off the machine: it is the one to restore after a later failure. Then
start the updater's timer again.

**What cannot be undone.** The device tokens, browser sessions and pending
sign-ins this start deleted stay deleted: those people sign in again at
`auth.cloudfield.ai`, and servers keep their record of each device and its
approval. A database restore is not an undo:

- The step 2 backup, or any from before step 5, has no recorded origin on
  any row and no record that recording ran. With
  `AETHER_EDGE_RETIRED_PAIRS` set the edge refuses to start on it (the
  `never recorded` refusal above).
- On the backup from the top of this step the edge starts and deletes the
  same sign-ins again. That restore also brings back every device token
  revoked since the backup, and loses every account, device sign-in, claim
  and owner change made since.
- Either backup under an image older than #1134, or with the option
  removed, makes the deleted tokens valid again at every served pair.

So from here a rollback is of the image only. With the updater, that rules
out `aether-edge-update --rollback --restore-backup` while
`/var/lib/aether-edge-update/previous` names a backup taken before this
step; its third field is the backup's path, which carries its time.

- Undo, partly: restore the step 16 lines and start the edge. Both hosts
  are served again; the deleted sign-ins are not restored. Once step 19 is
  done this needs more
  ([step 19](#19-the-old-github-oauth-app)).
- Cost to users: the restart drops relayed connections on the
  cloudfield.ai pair too, for a few seconds. Every device token and
  browser session listed under **What this start deletes** is signed out.
  A server still at `edge.onaether.dev` is unreachable through the edge
  until its admin moves it; connectors added at `https://edge.onaether.dev/mcp`
  fail; join and claim links made there stop working.
  On a server whose build has #1132, `sudo cloudfield-server edge move
  https://edge.cloudfield.ai` still moves it, on the signed `retired` notice
  the new address sends, and takes effect at the restart
  ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).
  With a CLI that has #1133, `cloudfield logout --edge
  https://edge.onaether.dev` deletes the dead local token and prints that
  the edge did not confirm the sign-out. On a server whose build has #1131,
  once it is connected at `https://edge.cloudfield.ai`, a `cloudfield mcp`
  with #1131 prints under each connector added at
  `https://edge.onaether.dev` `retired: add it again at <new URL> if you
  still use it, remove the old one in the app, then: aether mcp revoke
  <id>`, or `retired: remove it in the app, then: aether mcp revoke <id>`
  while the server names no new URL
  ([edge-retire.md](../edge-retire.md#moving-a-connector)).

## Phase 5: after

### 18. Keep DNS, routing and the entry

Keep the A records of `auth.onaether.dev` and `edge.onaether.dev` pointing at
the edge, both hosts in the Traefik router rule with their certificates,
and `AETHER_EDGE_RETIRED_PAIRS` for good. A backup taken after step 5 and
restored later holds the deleted tokens again, and the start with the
entry deletes them.

- Undo: n/a.
- Cost to users: none.

### 19. The old GitHub OAuth app

The app whose callback is `https://auth.onaether.dev/signin/github/callback`
is no longer used. Its client id was `AETHER_EDGE_GITHUB_CLIENT_ID` until
step 16 and the third field of `AETHER_EDGE_ORIGIN_PAIRS` until step 17.

First find the live secret, which must stay. Print the settings:
`GITHUB_CLIENT_SECRET_FILE` names it. After step 16 as written that is the
Compose secret `github-client-secret-2`, or on systemd
`/etc/aether-edge/github-client-secret`. The other file holds the old
app's secret.

1. In GitHub, **Settings → Developer settings → OAuth Apps**, open the app
   with the old callback, and delete it or generate a new secret and
   delete the old one.
2. Compose: delete the old app's secret file, expected
   `/opt/aether-edge/github-client-secret`, and in the same edit the
   `github-client-secret` entry under the service's `secrets:` and under
   the top-level `secrets:`. [Pause the updater](#pausing-the-updater),
   run `docker compose up -d --wait aether-edge`, run the checks of step
   4, start the timer.
3. systemd: remove the drop-in line
   `LoadCredential=github-client-secret-2:/etc/aether-edge/github-client-secret-2`
   (`sudo systemctl edit aether-edge`), restart, run the checks of step 4,
   then delete `/etc/aether-edge/github-client-secret-2`. Never delete
   `/etc/aether-edge/github-client-secret`: since the swap it holds the
   live secret.

- Undo: none for the deleted app or secret; GitHub does not give either
  back. Serving the old pair again (the undo of step 17) then needs a
  secret GitHub issues anew, and after a deleted app a new OAuth app with
  that callback and its new client id; the secret in a file readable by
  uid 65532 only, both `secrets:` entries again (on systemd the file and
  the drop-in line), and an `AETHER_EDGE_ORIGIN_PAIRS` entry with the
  matching client id. Accounts are keyed by GitHub user id, so a new app
  signs people in to the same accounts.
- Cost to users: the restart drops relayed connections for a few seconds.

### 20. Keep onaether.dev registered

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
| 4. Check the new pair | yes | Sign the test device out |
| 5. Mark retiring | yes, at a cost | Remove `AETHER_EDGE_RETIRING_PAIRS`, restart; recorded origins stay; an undo or a short run disarms step 17's refusal |
| 6. Report | yes | Read-only |
| 7. Metrics | yes | Read-only |
| 8. Contact owners | no | A message cannot be recalled |
| 9. Wait | yes | Change the date, restart |
| 10. Listings | partly | As each portal allows |
| 11. Updater and probes | yes, until step 17 | Set the old URL back while the old pair is served |
| 12. OpenAI challenge | partly | Set the old token back and restart, while no review relies on the new one |
| 13. Mail | yes | Remove the forward |
| 14. Website redirect | yes | Unchanged |
| 15. Go or no-go | yes | Changes nothing |
| 16. Swap | yes | Restore the before lines, restart; on systemd swap the secret files' contents back too |
| 17. Retire | partly | Restore the step 16 lines, restart; deleted sign-ins stay deleted; roll back the image only; a database restore is not an undo |
| 18. Keep DNS and entry | yes | Nothing to undo |
| 19. Old OAuth app | no | A deleted app or secret does not come back; serving the old pair again needs a new one |
| 20. Keep the domain | no | A lapsed domain may not come back |
