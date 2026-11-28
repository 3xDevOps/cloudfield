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
or stable release has on 2026-11-28. What step 17 does, whatever the
notice:

- It deletes every device token and browser session the edge recorded at
  `auth.onaether.dev`. Those people sign in again at `auth.cloudfield.ai`.
- It deletes every device token and browser session with no recorded
  origin, on both pairs: one issued while the edge did not record (before
  step 5) and not used since at an edge that records. The shorter the
  period, the more of these. Those people sign in again.
- It keeps everything recorded at `auth.cloudfield.ai`, issued there or
  first used there since step 5, used again or not. The recorded origin
  decides, never the last use ([step 17](#17-retire-the-old-pair)).
- A server not moved is unreachable through the edge until its admin moves
  it: on a build with #1132, `sudo cloudfield-server edge move
  https://edge.cloudfield.ai` and a restart; on an older build by hand,
  against the fingerprint of step 3
  ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).

Step 15 is the go/no-go on those numbers.

## The updater is off from step 5 to step 18

This runbook is for one host: the edge as the Compose service
`aether-edge` in `/opt/aether-edge`, updated by `aether-edge-update`
([edge.md](../edge.md#automatic-updates)).
[Check the host first](#check-the-host-first) confirms that.

The updater's timer runs it daily at 04:00 UTC with up to an hour's random
delay. A run can start another image with `docker compose up`, and roll
one back. It checks a release's promotion, signature and build order,
never which commits it contains (`scripts/edge-update.sh`). So step 5
turns it off, with the inverse of the command that
[edge.md](../edge.md#installing-the-updater) turns it on with, and step 18
turns it on again.

From step 5 until step 18, run no `aether-edge-update` command by hand
either:

- `--rollback` returns to the release in
  `/var/lib/aether-edge-update/previous`, which can be one from before
  the retirement code. An image from before #1128 ignores
  `AETHER_EDGE_RETIRING_PAIRS` and `AETHER_EDGE_RETIRED_PAIRS`, unknown
  variables to it (one between #1128 and #1134 ignores only the second).
  It records nothing, so every device token and browser
  session it issues, at either pair, has no recorded origin, and the
  start of step 17 deletes each of them that was not used again while the
  edge recorded. After step 17 it would also send servers no `retired`
  notice and answer the old hosts `421`, except to `GET /healthz` and
  `OPTIONS *`.
- `--check` is not read-only: it pulls the channel's image and the running
  one, and writes verified manifests and `release-check.err` under
  `/var/lib/aether-edge-update`. Nothing in these steps needs it.

Do not write another digest to `/opt/aether-edge/.env` by hand either. If
a release must be installed in that time, do it as
[edge.md](../edge.md#first-upgrade) says, and only a release for which, in
a clone, `git merge-base --is-ancestor 6b70f903c <tag> && echo yes` prints
`yes` (#1134, which holds the code of all three pull requests of step 1).

**Cost:** no automatic edge update from step 5 to step 18, which is at
least the wait of step 9.

## Changing the edge's settings

Steps 5, 16 and 17 change lines under `environment:` in
`/opt/aether-edge/compose.yaml` and start the edge again, the way
[edge.md](../edge.md#installing-the-updater) starts a Compose install:
edit the file, then `docker compose up -d --wait aether-edge` in its
directory. Each step runs this:

1. Note the claimed-server count. It prints a number only when the edge is
   ready, as the updater checks it; with anything else, stop and find out
   why before changing anything:

   ```sh
   out=$(sudo timeout 30 docker exec aether-edge aether-edge healthcheck --ready --json) &&
     printf '%s\n' "$out" | jq -er 'select(.status == "ready") | .servers.claimed | numbers'
   ```

2. Keep a copy of the file under a new name. The copy is the undo:

   ```sh
   step=5    # this step's number: 5, 16 or 17
   keep=/opt/aether-edge/compose.yaml.before-step-$step.$(date -u +%Y%m%dT%H%M%SZ)
   sudo sh -c 'set -C && cat /opt/aether-edge/compose.yaml > "$1" && cmp /opt/aether-edge/compose.yaml "$1" && echo "kept $1"' sh "$keep"
   ```

   It must print `kept <file>`: write that name down. `set -C` refuses a
   name that exists, so no copy is ever overwritten. Take the copy before
   the edit, once per attempt. Where `compose.yaml` already holds an edit
   of this step, take no new copy: the undo is the first one.
3. Edit `/opt/aether-edge/compose.yaml`: the step's lines, nothing else.
4. Compare. The output must be exactly the lines the step names:

   ```sh
   sudo diff <kept file> /opt/aether-edge/compose.yaml
   ```

5. Start it, with the command the host check settled on:

   ```sh
   (cd /opt/aether-edge && sudo docker compose up -d --wait aether-edge); echo "up exited $?"
   sudo docker inspect aether-edge --format '{{.State.StartedAt}} {{.State.Status}}'
   ```

   Expect `up exited 0` and `<time> running`. Note `<time>`: it is this
   start. `--wait` waits for the image's health check, which is liveness
   only. If `up` exits otherwise, `sudo docker logs --tail 20 aether-edge`
   ends with the edge's reason, `aether-edge: <text>`.
6. Run the command of 1 again until it prints a number. If it has printed
   none 3 minutes after `up` returned, undo.

   The number counts the claimed servers connected now. The restart ends
   every server's connection and each dials again by itself. A number
   below the one noted says that many have not returned yet. The number
   noted, or more, does not prove every server returned: one that
   connected for the first time or was claimed since counts too. The
   report names them: in `sudo docker exec aether-edge aether-edge
   retiring`, a server whose `LAST ENROLLED OR CLAIMED` is later than
   `<time>` has connected since this start. Decide on a lower number
   before going on.
7. The step's own checks. They read the log of this start only:
   `sudo docker logs --since <time> aether-edge 2>&1`.

**Undo** of a change: `sudo cp <kept file> /opt/aether-edge/compose.yaml`,
`sudo cmp <kept file> /opt/aether-edge/compose.yaml`, then 5 and 6.

A reboot between the edit and the `up` leaves the old container running
with the new file on disk: Docker starts a container again as it was
created, whatever `compose.yaml` holds. The next `up` applies the file.
After a reboot, run the `diff` of 4 to see which it is.

`docker logs --since`, `docker inspect` and the `--wait` of `up` are
written from Docker's documentation and were not run for this runbook.

## Check the host first

Nothing in the repository says how the hosted edge is deployed. Verify
each line on the host before step 1. Where one differs, stop: the steps do
not apply as written.

- [ ] **It runs as the Compose service.**

  ```sh
  sudo docker ps --filter name='^aether-edge$' --format '{{.ID}} {{.Image}} {{.Status}}'
  systemctl is-active aether-edge.service
  sudo docker inspect aether-edge --format '{{json .Config.Labels}}' |
    jq -r 'to_entries[] | select(.key | test("^com\\.docker\\.compose\\.(service|project|project\\.(working_dir|config_files|environment_file))$")) | "\(.key) = \(.value)"'
  ```

  Expect one line, `<id> ghcr.io/3xdevops/aether-edge@sha256:<digest> Up
  <time> (healthy)`, then `inactive`, then what Compose started the
  container with:

  ```
  com.docker.compose.project = aether-edge
  com.docker.compose.project.config_files = /opt/aether-edge/compose.yaml
  com.docker.compose.project.environment_file = /opt/aether-edge/.env
  com.docker.compose.project.working_dir = /opt/aether-edge
  com.docker.compose.service = aether-edge
  ```

  Write the five values down. With them, `(cd /opt/aether-edge && sudo
  docker compose up -d --wait aether-edge)` names the same project, file
  and env file, since Compose takes all three from the directory. A
  missing `environment_file` line is the same: the last `up` named no
  `--env-file`. With any other value, stop: the `up` of every step below
  would need it as `-p <project>`, `-f <file>` or `--env-file <file>`,
  and the updater's settings below would not be the defaults.
- [ ] **The updater.**

  ```sh
  systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
  systemctl is-active aether-edge-update.timer
  systemctl show aether-edge-update.service aether-edge-update-recover.service -p Environment -p EnvironmentFiles --value | grep -c .
  sudo env -i PATH="$PATH" sh -c '
    . /etc/aether-edge-update.env
    for n in CHANNEL RELAY_URL COMPOSE_DIR DIGEST_FILE STATE; do
      eval "new=\${CLOUDFIELD_EDGE_UPDATE_$n:-} old=\${AETHER_EDGE_UPDATE_$n:-}"
      echo "$n = ${new:-${old:-(default)}}${new:+  (from CLOUDFIELD_EDGE_UPDATE_$n)}"
    done'
  ```

  Expect `enabled` twice and `active`. The count is `0`: neither unit sets
  a variable, so both read `/etc/aether-edge-update.env`. The last command
  reads that file as the updater does, a shell file it sources, and
  prints the settings this runbook relies on: `CHANNEL` is `stable` or
  `(default)`, `RELAY_URL` is `https://edge.onaether.dev`, and
  `COMPOSE_DIR`, `DIGEST_FILE` and `STATE` are `(default)`, which is
  `/opt/aether-edge`, `/opt/aether-edge/.env` and
  `/var/lib/aether-edge-update`. The updater uses
  `CLOUDFIELD_EDGE_UPDATE_<NAME>` when it is not empty, else
  `AETHER_EDGE_UPDATE_<NAME>`, and the command marks each value that
  comes from the first. With a line marked `(from
  CLOUDFIELD_EDGE_UPDATE_...)`, stop: step 11 edits the `AETHER_` name.
- [ ] **The running settings.** Paste the function, then run the two
  commands:

  ```sh
  # Values of the listed settings; of every other edge variable, the name only.
  edge_settings() {
    jq -r '
      (map(capture("^(?<key>[^=]+)=(?<value>.*)$"; "p")) | from_entries) as $e
      | ("SIGNIN_ORIGIN RELAY_ORIGIN GITHUB_CLIENT_ID GITHUB_CLIENT_SECRET_FILE ORIGIN_PAIRS RETIRING_PAIRS RETIRED_PAIRS DATA METRICS_LISTEN" / " ") as $allow
      | ($allow[] | . as $n | ($e["CLOUDFIELD_EDGE_" + $n] // "") as $new | ($e["AETHER_EDGE_" + $n] // "") as $old
         | "\($n) = \(if $new != "" then $new else $old end)"
           + (if $new != "" then "  (from CLOUDFIELD_EDGE_\($n); AETHER_EDGE_\($n) = \($old))" else "" end)),
        "other edge variables set, names only: \([$e | keys[] | select(test("^(CLOUDFIELD|AETHER)_EDGE_")) | select(sub("^(CLOUDFIELD|AETHER)_EDGE_"; "") as $n | $allow | index($n) | not)] | join(" "))"'
  }
  sudo docker inspect aether-edge --format '{{.Config.Image}} {{.Path}} {{json .Args}}'
  sudo docker inspect aether-edge --format '{{json .Config.Env}}' | edge_settings
  ```

  The edge's variables can hold one secret inline,
  `AETHER_EDGE_GITHUB_CLIENT_SECRET` (or its `CLOUDFIELD_EDGE_` name). The
  function prints the value of the nine settings it lists, none of which
  is a secret, and only the name of any other. For each option the edge
  uses `CLOUDFIELD_EDGE_<NAME>` when it is not empty, else
  `AETHER_EDGE_<NAME>`, without a warning when both are set
  (`internal/envname`); the function prints the value in effect and marks
  every one that comes from a `CLOUDFIELD_EDGE_` name. Expect:
  - `<image of the first item> /usr/local/bin/aether-edge ["serve"]`, the
    image's own command: a flag there would win over every variable;
  - the old pair in `SIGNIN_ORIGIN`, `RELAY_ORIGIN` and `GITHUB_CLIENT_ID`,
    with `GITHUB_CLIENT_SECRET_FILE = /run/secrets/github-client-secret`;
  - the cloudfield.ai pair in `ORIGIN_PAIRS`, ending in
    `/run/secrets/github-client-secret-2`
    ([rename plan, C3](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist));
  - `RETIRING_PAIRS`, `RETIRED_PAIRS`, `DATA` and `METRICS_LISTEN` empty
    (the defaults are `/var/lib/aether-edge` and `127.0.0.1:9464`);
  - no line marked `(from CLOUDFIELD_EDGE_...)`, and no
    `GITHUB_CLIENT_SECRET` among the other names. With either, stop: the
    edits below name the `AETHER_EDGE_` lines only.

  Then read `/opt/aether-edge/compose.yaml` beside the printout, once: the
  service's `environment:` holds the same values and the same other
  names, and the `AETHER_EDGE_DIGEST` of `/opt/aether-edge/.env` is the
  image's digest. If they disagree, stop: the file holds an edit or a
  digest that was never started, and the next `up` starts it.
- [ ] **Data and secrets.**

  ```sh
  sudo docker inspect aether-edge --format '{{range .Mounts}}{{.Type}} {{.Source}} {{.Destination}}{{println}}{{end}}'
  ```

  Expect `bind /opt/aether-edge/data /var/lib/aether-edge`, and one `bind`
  line each to `/run/secrets/github-client-secret` and
  `/run/secrets/github-client-secret-2`. The second field of those two is
  the secret's file on the host: `sudo ls -ln <file>` shows owner `65532`
  and mode `-r--------` for each.
- [ ] **All four hosts reach the edge.**

  ```sh
  for h in auth.onaether.dev edge.onaether.dev auth.cloudfield.ai edge.cloudfield.ai; do
    printf '%s ' "$h"; curl -fsS --max-time 10 "https://$h/healthz"
  done
  ```

  Expect `ok` after each name: the proxy routes it to the edge with a
  valid certificate.
- [ ] **The edge has never recorded origins.**

  ```sh
  sudo docker exec aether-edge aether-edge retiring | head -n 1
  ```

  Expect `no origin pairs recorded: the edge records them from its first
  start with an option that retires a pair`. Step 5 is then the first
  recording. Two other answers mean step 1 is not done, or was done
  without a start since:
  - `nothing is recorded yet: ...` comes from a binary that has the
    report on a database whose schema is older than the one that holds
    origins;
  - `aether-edge: unknown command "retiring"` comes from a binary from
    before the report.

## Phase 0: before anything

### 1. The release with the retirement code runs on the edge

Pull requests #1129 (records, `aether-edge retiring`), #1128
(`--retiring-pairs`, notices, banners) and #1134 (`--retired-pairs`, the
`410` answers, the start-up deletion) must be in the running image. The
edge follows `stable`, so this waits for a stable release that contains
#1134 (`6b70f903c`) and its promotion
([edge.md](../edge.md#promoting-an-edge-release-to-stable)). In a clone
with the tags fetched, `git tag --contains 6b70f903c` lists the releases
with it. On 2026-11-28 those are `v0.10.0-nightly.6`, `v0.10.0-nightly.7`
and `v0.10.0-nightly.8`; `v0.10.0-beta.1` does not contain it, so the
beta and the stable to wait for are later ones. All this runbook needs of
them is `6b70f903c` in the edge's image. The warning period cannot start
before that release runs here.

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
/var/lib/aether-edge/backups/edge-v<n>-<time>.db`. The file is in
`/opt/aether-edge/data/backups/` on the host. Copy it off the machine, to
a place only the owner reads: it holds every account, device and owner.

What it covers: `edge.db` only, as it was at that moment. It holds
neither the edge key, `compose.yaml`, `.env`, the secret files nor the
updater's state.

This backup is for a failure before step 17. It is not a way back from
step 17 ([What cannot be undone](#17-retire-the-old-pair)). It predates
step 5: no row in it has a recorded origin and nothing in it says
recording ever ran. A start with `AETHER_EDGE_RETIRED_PAIRS` on it is
therefore refused, with the `never recorded` refusal of step 17, as long
as it holds any sign-in. Restored after step 5, it drops every origin
recorded since, and the warning period starts over.

- Undo: delete the file and its copy.
- Cost to users: none.

### 3. Record the edge key fingerprint

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.onaether.dev/v1/edge | jq -r .fingerprint
sudo ssh-keygen -y -f /opt/aether-edge/data/edge_key | ssh-keygen -lf -
```

The first two print the same `SHA256:` fingerprint, and the third prints it
in `256 SHA256:<fingerprint> aether edge (ED25519)`. The third reads the
private key and prints only the fingerprint of the public key it derives.

The edge does not start without `edge_key` beside a database that holds
accounts or servers, and only the private key restores it: `edge_key` is
an OpenSSH private key without a passphrase, which is all the edge parses
(`internal/edge/service/key.go`). Where the offline copy that
[edge.md](../edge.md#backup-and-recovery) asks for is kept, decrypt it to
a file only you read and run the third command on it: it must print the
same fingerprint. `ssh-keygen -lf <copy>` is not that check, since it
prints the same for a copy of the public key alone, which restores
nothing. Without a good copy, make one now with `sudo docker exec
aether-edge aether-edge backup --with-key /var/lib/aether-edge/backups/`,
which also writes `<backup>.key`, and keep that file encrypted and
offline: whoever holds it can act as the edge.

Publish the fingerprint where server admins can read it over a channel
other than the edge: a server moved by hand is checked against it
([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).
Record where it was published, when, and that it came from the third
command.

- Undo: none for the publication. A wrong fingerprint is corrected by
  publishing the right one in the same place and telling whoever may have
  read it.
- Cost to users: none.

### 4. Check that the cloudfield.ai pair works

Steps 5, 16 and 17 run these again; a `410` on the old hosts says nothing
about the pair that stays. Both checks only read:

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r '.signin_origin, .fingerprint, .retiring'
```

Expect `https://auth.cloudfield.ai`, the fingerprint of step 3 and `null`.

Then open `https://auth.cloudfield.ai/signin` in a browser profile that
has never signed in to the edge. Expect the page `Sign in to see your
Cloudfield servers or to approve a device.` with the button **Sign in
with GitHub**, and no retiring warning. Do not select the button: the
check is that the page loads, and a sign-in would leave a session.

Neither check signs in, uses a token or reaches a server. The pair has
served sign-ins since it was added; who uses it is in the report of step
6, in the section of `https://edge.cloudfield.ai`.

- Undo: nothing changed.
- Cost to users: none.

## Phase 1: mark the old pair retiring

### 5. Set AETHER_EDGE_RETIRING_PAIRS

The warning period starts here.

**First turn the updater off.** Check that no run or recovery is in
flight:

```sh
systemctl is-active aether-edge-update.service aether-edge-update-recover.service
sudo grep -E '^(state|kind|to_version|since)=' /var/lib/aether-edge-update/state
```

- Each service prints `inactive` or `failed`. `failed` is the last run's
  exit status, not a run in progress. `activating` or `deactivating` is a
  run: follow it with `journalctl -u aether-edge-update.service -f` and
  repeat when it has ended. Never `systemctl stop
  aether-edge-update.service`: the unit rolls back a switch it stops.
- Expect `state=idle`, `state=held`, or no state file (`grep` then says
  so). `held` is a release waiting for a manual step and changes nothing
  by itself.
- `state=switching` or `state=rolling-back` with both services inactive
  is a run that was interrupted. It must complete first: `sudo systemctl
  start aether-edge-update-recover.service` finishes the switch or rolls
  it back and returns when it is done. Read `journalctl -u
  aether-edge-update-recover.service -n 30 --no-pager`, then start this
  step again, at step 1 if the release changed.
- `state=broken` is in [edge.md](../edge.md#when-an-update-stops). No step
  of this runbook runs until it is fixed.

Then:

```sh
sudo systemctl disable --now aether-edge-update.timer aether-edge-update-recover.service
systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
systemctl is-active aether-edge-update.timer aether-edge-update.service aether-edge-update-recover.service
sudo sed -n 's/^AETHER_EDGE_DIGEST=//p' /opt/aether-edge/.env
```

Expect `disabled` twice and no `active`, `activating` or `deactivating`:
the timer could have started a run between the two checks, and it is then
followed as above. Note the digest the last line prints, for step 15.
`disable --now` is systemd's inverse of the documented `enable --now` and
was not run for this runbook.

**Then mark the pair.** The old pair is the first pair. Marking it
retiring needs no swap, because the option names a pair by its relay
origin. Retiring it later does need the swap (step 16).

[Change the settings](#changing-the-edges-settings) with `step=5`. The
line to add to the service's `environment:`:

```yaml
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15
```

The `diff` shows that one added line. The step's own checks:

```sh
sudo docker logs --since <time> aether-edge 2>&1 | grep -e 'aether-edge: retiring' -e '^aether-edge:'
```

Expect `aether-edge: retiring relay=https://edge.onaether.dev
replacement=https://edge.cloudfield.ai date=2026-12-15`, and no other
`aether-edge:` line: one is a refusal, listed in
[edge.md](../edge.md#retiring-an-address-pair).

Against the live edge:

```sh
curl -sS -D - -o /dev/null -H 'Aether-Edge-Version: 6' https://edge.onaether.dev/v1/edge | grep -i '^aether-edge-retiring'
curl -s -H 'Accept: text/html' https://auth.onaether.dev/signin | grep 'is retiring'
```

Expect `Aether-Edge-Retiring: https://edge.cloudfield.ai; date=2026-12-15`
(in lowercase over HTTP/2), and
`<p class="warning" role="status">This address of the edge is retiring on
2026-12-15. Its new address is <code>https://auth.cloudfield.ai</code>: sign in
there.</p>`. The same request to `https://edge.cloudfield.ai/v1/edge`
carries no such header. Run the checks of step 4, and note `<time>`, this
start, for step 15. Note too the `since` in the first line of `sudo docker
exec aether-edge aether-edge retiring`, for steps 6 and 15: those steps compare
the `since` they print with this one.

A server enrolled at `https://edge.onaether.dev` whose build has #1132
shows the warning and the move command once it has connected again
([edge-retire.md](../edge-retire.md#moving-a-server)).

From this start, and while this option or `AETHER_EDGE_RETIRED_PAIRS` is
set on a release that has the code, the edge records for every pair the
sign-in origin each device token, browser session and pending device
sign-in is issued at; for a token or session issued earlier, the sign-in
origin of the pair it is next used at; and each claimed server's relay
origin at each enrollment or claim
([edge.md](../edge.md#monitoring), [privacy.md](../privacy.md)). Any
request the edge checks a token on is such a use, a `cloudfield servers`
included; `GET /v1/edge` checks none. A connector request records only
its server and relay origin,
at most once an hour, and gives no token an origin.

- Undo: the kept file, as in the procedure; the updater stays off until
  step 18 either way. Origins recorded meanwhile stay in `edge.db`. **An
  undo disarms step 17's safety check.** That check refuses only an edge
  that never recorded. After an undo, or with the option set for a day,
  step 17 starts, and deletes every sign-in that has no recorded origin
  by then. Without the option the edge also issues tokens and sessions
  with no recorded origin, and step 17 deletes each one not used again
  while the edge recorded. After an undo, do this step again and wait the
  whole period before step 17.
- Cost to users: no automatic edge update until step 18. The restart ends
  every relayed connection; servers dial again by themselves. A device
  sign-in that was pending at this restart is deleted, since it was
  started before the edge recorded origins; its person runs `cloudfield
  login` again. Removing the option does not bring it back.

## Phase 2: watch who is left

### 6. Read the report

```sh
sudo docker exec aether-edge aether-edge retiring --origin https://edge.onaether.dev
sudo docker exec aether-edge aether-edge retiring
```

[edge-retire.md](../edge-retire.md#who-is-left) shows the output and what
each row counts. What matters here:

- The first line is the last known recording period. A `since` later than
  the one noted in step 5 says a new period began: after an unclean stop,
  or a start more than 10 minutes after the last clean one. A `since`
  equal to it does not prove the edge recorded all the time. The line
  does not filter the counts.
- The server table lists the claimed servers whose last recorded
  enrollment or claim was at `https://edge.onaether.dev`, with the owner
  login, `-` for a server with no owner.
- Step 17 deletes, however recently used: `device tokens issued here`,
  `device tokens issued earlier, first seen here` and the sessions behind
  `accounts with a session here` in the old pair's section; the same
  rows of a section `recorded at an address this edge no longer pairs:
  <origin>` whose origin has the host name `auth.onaether.dev`, or was
  once recorded beside `edge.onaether.dev` and is signed in at by no
  served pair; and the device tokens and sessions under `unknown origin`,
  at both pairs.
- Step 17 keeps what the section of `https://edge.cloudfield.ai` counts.
  A token issued before step 5 and first used since on a cloudfield.ai
  host is counted, and kept, there.
- The servers under `unknown origin` have not enrolled or been claimed
  since recording began. They stay at whatever address they dial.

An empty list is not proof that nobody is left; the report says why in its
last lines. The old pair keeps its section after step 17.

- Undo: nothing changed (the command opens `edge.db` read-only).
- Cost to users: none.

### 7. Read the metrics

The metrics listener is on loopback inside the container, and the image
has no shell or `curl`. This runs the host's `curl` in the container's
network namespace:

```sh
sudo nsenter -t "$(sudo docker inspect -f '{{.State.Pid}}' aether-edge)" -n \
  curl -q -sS --noproxy '*' --max-time 10 http://127.0.0.1:9464/metrics |
  grep -e aether_edge_servers_by_origin -e aether_edge_device_logins_total -e aether_edge_connector_requests_total -e aether_edge_retired_requests_total -e aether_edge_github_errors_total
```

`127.0.0.1:9464` is the default the host check found in effect.
`aether_edge_servers_by_origin{origin="https://edge.onaether.dev"}` counts
servers connected there now, unclaimed ones too, which the report misses.
`aether_edge_device_logins_total{origin}` and
`aether_edge_connector_requests_total{origin,protocol}` count device
sign-ins and connector requests per address, and
`aether_edge_github_errors_total` the calls to GitHub that failed, all
since the edge last started. `aether_edge_retired_requests_total` exists
only from step 17 on.

- Undo: nothing changed.
- Cost to users: none.

### 8. Contact owners

After the first report, and again for each owner who appears in a later
one: a server shows in the table only once it has enrolled again after
step 5. Who can be named:

- each claimed server recorded on the old pair, by the owner login in the
  report's table;
- the claimed servers under `unknown origin`, by `sudo docker exec
  aether-edge aether-edge servers list`, which prints every server with
  its owner but not the address it dials.

Nobody else: a server with no owner has no login, an unclaimed server has
no row, and the people behind device tokens and connectors are only
counted. Writing to owners is outside the product, and whether to is the
owner's decision: the plan sends no mail
([plan, For the owner to decide](2026-11-26-retire-address-pair.md#for-the-owner-to-decide)),
and [privacy.md](../privacy.md) would have to allow it. Give the date, and
point them to [edge-retire.md](../edge-retire.md).

- Undo: none; a message cannot be recalled.
- Cost to users: none.

### 9. Wait

Recommendation: at least 30 days after step 5. A device token never
expires, and one issued before step 5 gets a recorded origin only when it
is next used; step 17 deletes those that have none. The edge's own refusal
checks only that recording ever ran, not for how long.

The owner chose `2026-12-15` knowing it gives less, at the costs under
[Notice](#notice). The date is only text. To change it, change the line of
step 5 as in that step.

- Undo: n/a.
- Cost to users: none. A changed date is a restart, which ends every
  relayed connection.

## Phase 3: outside the product, before retiring

In this order. Each is the owner's check of something the repository does
not hold; none is undone by reverting a file.

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
  existing MCP server's URL, contact support". An answer from support is
  not the move. Step 17 waits until the portal shows the new URL and the
  reviewer sign-in above has been done at it.

- Undo: only in each portal, by its own rules; not assured.
- Cost to users: none from this step. People who added a listed connector
  at the old URL add it again at the new one
  ([edge-retire.md](../edge-retire.md#moving-a-connector)).

### 11. The updater's relay URL and monitoring probes

The updater is off, and this prepares step 18. A run that switches
release fetches `<relay URL>/v1/edge` before the switch and after it; a
run that finds the edge up to date never does. A retired relay answers
`410` there. With the old URL after step 17, an update waits
`AETHER_EDGE_UPDATE_PUBLIC_WAIT` seconds (default 300) and exits 1 with
`GET https://edge.onaether.dev/v1/edge failed; the running edge must
answer before an update`, before it switches. In
`/etc/aether-edge-update.env`, change the value of the assignment the
file has, so that it reads:

```sh
AETHER_EDGE_UPDATE_RELAY_URL=https://edge.cloudfield.ai
```

A `CLOUDFIELD_EDGE_UPDATE_RELAY_URL` that is not empty wins over it; the
host check found none. Then run the last command of the updater item of
[Check the host first](#check-the-host-first) again: `RELAY_URL =
https://edge.cloudfield.ai`, the rest as before. The updater's own
request:

```sh
curl -fsS --max-time 10 -H 'Aether-Edge-Version: 1' https://edge.cloudfield.ai/v1/edge | jq -r .fingerprint
```

prints the fingerprint of step 3.

Move every probe that reads a page or `/v1/` on `auth.onaether.dev` or
`edge.onaether.dev` to the cloudfield.ai hosts. `/healthz` keeps answering
`ok` on a retired host, so it shows only that the proxy still routes the
host; keep it and the certificate expiry probes on all four host names.
Which probes exist is not in the repository: the owner lists them.

- Undo: set the old value back, only while the old pair is served. After
  step 17 the old URL stops every update as above. A probe is moved back
  where it is configured.
- Cost to users: none.

### 12. The OpenAI domain challenge

The edge holds one challenge token and serves it on every relay host. A
retired host answers `410` there too. Whether a ChatGPT submission exists
and which host it verified is in the OpenAI portal only (step 10). If one
verified `edge.onaether.dev`, verify `edge.cloudfield.ai` in the portal
and see it verified there before step 17
([chatgpt-plugin.md](../chatgpt-plugin.md#submit-in-the-portal), step 4).
While a review or support case is open, change the token only as support
says.

```sh
curl -sS -i https://edge.cloudfield.ai/.well-known/openai-apps-challenge
```

`200` with a `text/plain` body: that body is the token served; compare it
with the one the portal shows. `404` with the body `404 page not found`:
the edge serves none. Neither shows whether a review relies on a
verification made earlier. Any other answer: stop and find out.

- Undo: none for a verification. Setting the previous
  `AETHER_EDGE_OPENAI_CHALLENGE` back restores what the edge serves, not
  what OpenAI has verified or dropped.
- Cost to users: none unless the token changes; that takes a restart,
  which ends every relayed connection.

### 13. Mail

How mail for `onaether.dev` is delivered is not in the repository.
`team@onaether.dev` must keep receiving
([rename plan](2026-11-15-cloudfield-rename.md#how-the-old-hostnames-keep-working)),
for example by a forward to `team@cloudfield.ai` at the mail provider.
Send a test message to the old address from outside both domains and
check that it arrives.

- Undo: only at the mail provider: put the previous delivery back and
  send the test again. Mail refused in between is not recovered.
- Cost to users: none.

### 14. The website redirect

The redirect is configured outside this repository. `onaether.dev` is
meant to redirect to the same path on `cloudfield.ai`:

```sh
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://onaether.dev/docs/privacy
```

Expect a `3xx` status and `https://cloudfield.ai/docs/privacy`. This is
the website host only: the edge never redirects a request from
`auth.onaether.dev` or `edge.onaether.dev` to the cloudfield.ai pair,
since their requests carry codes and tokens.

- Undo: n/a; this step only reads. A wrong redirect is fixed where the
  website is configured.
- Cost to users: none.

## Phase 4: retire

### 15. Go or no-go

Immediately before step 16, on the day:

1. Run the report of step 6, with and without `--origin`, and the metrics
   of step 7.
2. Check that the image of step 5 still runs and the updater is still
   off:

   ```sh
   systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
   systemctl is-active aether-edge-update.timer
   sudo docker inspect aether-edge --format '{{.Config.Image}} {{.State.StartedAt}}'
   ```

   Expect `disabled` twice, `inactive`, and `ghcr.io/3xdevops/aether-edge@`
   with the digest noted in step 5. The time is that of step 5, or of a
   later start the owner made or a reboot.
3. Read the report's first line as step 6 says. A `since` later than the
   one noted in step 5 means a new period began; origins recorded before
   it are kept, and
   whatever was issued while the edge did not record has no origin, which
   step 17 deletes unless it was used again while the edge recorded. With
   another digest in 2, or the updater on, an image without the code may
   have run: no-go until it is known which.
4. Check the live warning, with the two `curl` lines of step 5.
5. For each server still in the table, each nonzero row of the old pair,
   of a `no longer pairs` section and of `unknown origin`, and each
   server the metric counts on the old relay origin, decide: retire
   anyway, or wait. Contact owners who are new in the table since step
   8. Whoever is left pays the costs under [Notice](#notice). A server
   whose admin cannot be reached stays unreachable through the edge until
   someone with a shell on its host moves it; member SSH keys and tailnet
   access are not affected.
6. Check that steps 10 to 14 are settled, and run the checks of step 4.
7. Record the time, the report, the metric lines, the decision and who
   made it, where the owner keeps deployment records, outside this
   repository. The report holds owner logins.

No-go: set a later date in the line of step 5, as in that step, and tell
the owners of step 8.

- Undo: the checks change nothing. A no-go is not undone: the new date is
  a restart, and its messages cannot be recalled.
- Cost to users: none from the checks. A no-go's restart ends every
  relayed connection.

### 16. Swap the pairs

`--retired-pairs` refuses a host of the first pair, so the cloudfield.ai pair
becomes the first and the old pair an `AETHER_EDGE_ORIGIN_PAIRS` entry, with
its own app and secret. No pair is preferred: servers see only the order of
`relay_origins` change.

[Change the settings](#changing-the-edges-settings) with `step=16`:

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

Only the paths swap. No secret file and no `secrets:` entry changes:
`github-client-secret` still holds the onaether.dev app's secret,
`github-client-secret-2` the cloudfield.ai app's. The `diff` shows five
changed lines, `SIGNIN_ORIGIN` to `ORIGIN_PAIRS`. The step's own checks:

```sh
sudo docker logs --since <time> aether-edge 2>&1 | grep -e 'aether-edge: serving' -e 'aether-edge: retiring' -e '^aether-edge:'
```

Expect `aether-edge: serving signin=https://auth.cloudfield.ai
relay=https://edge.cloudfield.ai metrics=<address>`, then `aether-edge:
serving signin=https://auth.onaether.dev relay=https://edge.onaether.dev`,
then the retiring line of step 5. Both `curl` lines of step 3 still print
the same fingerprint. Run the checks of step 4, and load
`https://auth.onaether.dev/signin` the same way: it shows the warning of
step 5.

Those checks do not test either OAuth app: a client id paired with the
wrong secret shows only when someone signs in, as `GitHub sign-in:
exchange code: <error>` on the page and a rise of
`aether_edge_github_errors_total` (step 7). After the swap, read that
metric and `aether_edge_device_logins_total` of both sign-in origins
until people have signed in. The only direct test is a sign-in at each
host in the profile of step 4; it leaves a browser session at the edge,
which ends when that browser signs out or after 30 days unused.

- Undo: the kept file, as in the procedure.
- Cost to users: the restart ends every relayed connection; servers dial
  again by themselves.

### 17. Retire the old pair

First a fresh backup, minutes before the edit, copied off the machine as
in step 2:

```sh
sudo docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
```

Note how many servers are connected at the pair that stays: the line
`aether_edge_servers_by_origin{origin="https://edge.cloudfield.ai"} <n>`
of step 7's command.

Then [change the settings](#changing-the-edges-settings) with `step=17`.
In one edit: the old pair leaves `AETHER_EDGE_ORIGIN_PAIRS` entirely,
`AETHER_EDGE_RETIRED_PAIRS` names it, and its `AETHER_EDGE_RETIRING_PAIRS`
entry goes (with one served pair left, the option is refused). The file's
`secrets:` entries and the Traefik labels stay as they are.

```yaml
      AETHER_EDGE_SIGNIN_ORIGIN: https://auth.cloudfield.ai
      AETHER_EDGE_RELAY_ORIGIN: https://edge.cloudfield.ai
      AETHER_EDGE_GITHUB_CLIENT_ID: <cloudfield-client-id>
      AETHER_EDGE_GITHUB_CLIENT_SECRET_FILE: /run/secrets/github-client-secret-2
      AETHER_EDGE_RETIRED_PAIRS: https://auth.onaether.dev https://edge.onaether.dev https://edge.cloudfield.ai
```

The `diff` shows the `ORIGIN_PAIRS` and `RETIRING_PAIRS` lines removed and
the `RETIRED_PAIRS` line added. The claimed count of the procedure falls
by the claimed servers still at the old pair, so compare the metric line
noted above instead, for at most 5 minutes. It counts unclaimed servers
too, and the edge closes an unclaimed server's connection after 30
minutes, so it can differ for other reasons than this restart; the
report's table of `https://edge.cloudfield.ai` lists the claimed servers
last enrolled there, offline ones too, and those with a `LAST ENROLLED OR
CLAIMED` later than `<time>` enrolled or were claimed since this start,
which does not show that they are connected now. The step's own checks:

```sh
sudo docker logs --since <time> aether-edge 2>&1 | grep -e 'edge: deleted the sign-ins' -e 'edge: keeping the sign-ins' -e 'aether-edge: retired' -e '^aether-edge:'
```

Expect no `edge: keeping the sign-ins` line, and in this order:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth.onaether.dev relay=https://edge.onaether.dev replacement=https://edge.cloudfield.ai
```

**What this start deletes**, in one transaction, once the listeners are
bound and before the edge serves: each device token, browser session and
pending device sign-in whose recorded sign-in origin

- has the host name `auth.onaether.dev`, whatever its port; or
- has the host name of another sign-in origin the edge once recorded
  beside `edge.onaether.dev`, unless a served pair signs in at that host
  (the edge then keeps those rows and logs the `keeping` line); or
- is missing, at both pairs: the row was issued while the edge did not
  record and has not been used since at an edge that records.

**What it keeps:** every device token and session recorded at
`https://auth.cloudfield.ai`, issued there or first seen there, used
since or not. A token or session
issued while the edge did not record, at either pair, and used once on a
cloudfield.ai host while it records, is recorded there and kept.

The edge answers a retired host `410` before it reads any token, so a
`410` proves nothing about a token; never send one to another host to
find out. `https://auth.cloudfield.ai/devices` lists an account's
remaining tokens by label, key fingerprint, sign-in time and last use,
without device ids: a person who signed in again there has a row for the
new token, and nothing to revoke for one this start deleted.

The deletion commits before its log line is written and before the edge
answers a health check. A failed or timed-out `up`, or a missing `edge:
deleted the sign-ins` line, does not show that nothing was deleted. Only
a start refused with one of the lines below, all checked before the
transaction, deleted nothing. A refused start serves nothing: both pairs
are down, and the container restarts into the same refusal, until the
step 16 file is back and started. `sudo docker logs --tail 20 aether-edge`
then ends with `aether-edge: <refusal>`; the others are in
[edge.md](../edge.md#retired):

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth.onaether.dev is still served as --signin-origin or --relay-origin; first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair` | Step 16 first |
| `--retired-pairs: https://auth.onaether.dev is still served; remove its pair from --origin-pairs in the same edit` | Remove `AETHER_EDGE_ORIGIN_PAIRS`, and `CLOUDFIELD_EDGE_ORIGIN_PAIRS` if set |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove `AETHER_EDGE_RETIRING_PAIRS`, and `CLOUDFIELD_EDGE_RETIRING_PAIRS` if set |
| `AETHER_EDGE_SIGNIN_ORIGIN names https://auth.onaether.dev, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN` (the same for `AETHER_EDGE_RELAY_ORIGIN` and `AETHER_EDGE_ORIGIN_PAIRS`) | Set that `AETHER_EDGE_` name to the value in effect. Never remove the `CLOUDFIELD_EDGE_` name instead: the edge would then serve the old pair |
| `this edge uses the data directory <dir> from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to <dir>, or pass --data <dir> on the command line, which both images read` | Set `AETHER_EDGE_DATA` to `<dir>` |
| `edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens <n>, browser sessions <n>, pending device sign-ins <n>), since this edge has never recorded which pair issued them. ...` | This database never recorded: step 5 never ran on it, or it was restored from a backup older than step 5. Restore the step 16 file, then step 5 and the wait |
| `--retired-pairs names <n> pairs; an edge keeps at most 16`, or `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: ...` | Fewer entries; cannot happen with one |

Against the live edge, both old hosts:

```sh
for h in auth.onaether.dev edge.onaether.dev; do
  curl -sS -o /dev/null -D - "https://$h/v1/edge" | grep -i -e '^HTTP' -e '^location'
  curl -sS "https://$h/v1/edge"
  curl -s -H 'Accept: text/html' "https://$h/" | grep '<p><code>'
done
```

Each host prints a status line with `410` and no `location` line, then
its own origin in:

```json
{"error":"https://edge.onaether.dev is retired; this edge is at https://edge.cloudfield.ai. Move a server with: cloudfield-server edge move https://edge.cloudfield.ai. Sign a device in with: cloudfield login --edge https://edge.cloudfield.ai","retired":{"replacement":"https://edge.cloudfield.ai"}}
```

and then `<p><code>https://auth.onaether.dev</code> is retired. This edge
is now at <code>https://auth.cloudfield.ai</code>: sign in there.</p>` on
the sign-in host, `<p><code>https://edge.onaether.dev</code> is retired.
This edge is now at <code>https://edge.cloudfield.ai</code>.</p>` on the
relay host. `curl -fsS https://edge.onaether.dev/healthz` still prints
`ok`.

Then the pair that stays: the checks of step 4, and the two metrics of
step 16. `aether_edge_retired_requests_total{origin,kind}` (step 7)
counts what still arrives at each old host.

Once the checks pass, take another backup with the command above and copy
it off the machine: it is the one to restore after a later failure.

**What cannot be undone.** What this start deleted stays deleted: those
people sign in again at `auth.cloudfield.ai`, and servers keep their
record of each device and its approval. A database restore is not an
undo:

- A backup from before step 5 has no recorded origin on any row and no
  record that recording ran. With `AETHER_EDGE_RETIRED_PAIRS` set the
  edge refuses to start on it while it holds a sign-in (the `never
  recorded` refusal above).
- On the backup from the top of this step the edge starts and deletes the
  same sign-ins again. A restore brings back only the rows the backup
  holds: of the device tokens revoked since, those recorded at
  `https://auth.cloudfield.ai` are valid again, and the start deletes
  those of the old pair and those with no origin. It loses every
  account, device sign-in, claim and owner change made since.
- Either backup under an image older than #1134, or with the option
  removed, has none of its tokens deleted, at either pair.
- The backup taken after the checks does not hold the deleted rows.

- Undo, partly: the step 16 file, as in the procedure. Both hosts are
  served again; the deleted sign-ins are not restored.
- Cost to users: the restart ends every relayed connection, at the
  cloudfield.ai pair too; servers there dial again by themselves.
  Everyone under **What this start deletes** is signed out. A server
  still at `edge.onaether.dev` is unreachable through the edge until its
  admin moves it; connectors added at `https://edge.onaether.dev/mcp`
  fail; join and claim links made there stop working. What each of them
  sees and does is in
  [edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved),
  [its device part](../edge-retire.md#if-the-address-is-retired-before-the-device-moved)
  and
  [its connector part](../edge-retire.md#if-the-address-is-retired-before-the-connector-moved).

## Phase 5: after

### 18. Turn the updater back on

`AETHER_EDGE_RETIRED_PAIRS` stays for good (step 19), and for as long as
it is set the edge must run a release that contains `6b70f903c`. An older
image ignores the variable: it sends servers no signed `retired` notice,
so `edge move` from the old address stops working, it answers the old
hosts `421` except to `GET /healthz` and `OPTIONS *`, and it issues
sign-ins with no recorded
origin, which the next start of a release with the entry deletes, at the
pair that stays too.

The updater does not check that. See what it would install:

```sh
sudo aether-edge-update --check
```

It prints `edge-update: verified the running <version> (<digest>)`, then
one of `up to date: stable is <digest>`, `would update <version>
(<digest>) to <version> (<digest>)`, `held: <version> (<digest>):
<reasons>` with exit status 3, or `skipping <digest>, which failed
before: ...`. For the release a `would update` or `held` line names, in a
clone with the tags fetched:

```sh
git merge-base --is-ancestor 6b70f903c <tag> && echo yes
```

Only with `yes`, or `up to date`:

```sh
sudo systemctl enable --now aether-edge-update.timer aether-edge-update-recover.service
systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
```

Expect `enabled` twice. The timer is `Persistent=true`, which runs a
missed window at boot; whether systemd also runs one when the timer is
enabled again was not tested, so expect the first run at any time from
here.

That check covers the release on `stable` today. A later one with a
higher build can still lack the commit, for example a hotfix cut from an
older release line. The owner promotes to `stable`
([edge.md](../edge.md#promoting-an-edge-release-to-stable)): run the same
`git merge-base` line on every tag before promoting it. Run it too before
any update by hand, and before `sudo aether-edge-update --rollback` on the
release `sudo cut -d' ' -f2 /var/lib/aether-edge-update/previous` prints.
Never add `--restore-backup` to a rollback across step 17: the third field
of that file is the backup it restores when its check of the previous
release against the current database fails, and one from before step 17
holds the deleted sign-ins.

- Undo: `sudo systemctl disable --now aether-edge-update.timer
  aether-edge-update-recover.service`, after the checks at the top of
  step 5.
- Cost to users: none from this step. An update the timer then installs
  is a restart, which ends every relayed connection.

### 19. Keep DNS, routing and the entry

Keep the A records of `auth.onaether.dev` and `edge.onaether.dev` pointing at
the edge, both hosts routed to it by Traefik with their certificates,
and `AETHER_EDGE_RETIRED_PAIRS` for good. A backup taken between step 5
and step 17 and restored later holds the deleted tokens again, and the
start with the entry deletes them.

- Undo: n/a.
- Cost to users: none.

### 20. The old GitHub OAuth app stays

This runbook leaves in place the GitHub OAuth app whose callback is
`https://auth.onaether.dev/signin/github/callback`, its secret file, and
the `github-client-secret` entries in `compose.yaml`.

After step 17 the edge uses none of them: a retired pair needs no OAuth
app, and the callback answers `410`. What stays exposed is a valid client
secret of that app, in its file on the host and mounted in the container
at `/run/secrets/github-client-secret`. The undo of step 17 needs all
three, so removing them is a separate, later decision of the owner.

- Undo: nothing changed.
- Cost to users: none.

### 21. Keep onaether.dev registered

Do not let the `onaether.dev` registration lapse. A server or client that
never moved still dials these host names, and a printed join link still
opens them. A server checks only that the edge presents the public key it
pinned, not that it holds the private key, so whoever held the name would
receive server control connections, device tokens and connector tokens.

"Removed", in the plan's terms, means deleting the two DNS records while
keeping the `AETHER_EDGE_RETIRED_PAIRS` entry: the names then answer
nothing. The plan does not make that safe, nor giving up the domain, and
counts cannot show that nobody is left.

- Undo: none; a lapsed registration may not come back.
- Cost to users: none.

## Summary

| Step | Reversible? | How |
| --- | --- | --- |
| 1. Release check | yes | Changes nothing |
| 2. Back up edge.db | yes | Delete the file and its copy |
| 3. Record the fingerprint | partly | Reads change nothing; a published fingerprint is corrected, not recalled |
| 4. Check the new pair | yes | Read-only |
| 5. Updater off, mark retiring | yes, at a cost | Start the kept file; recorded origins stay; an undo or a short run disarms step 17's refusal; the updater stays off until step 18 |
| 6. Report | yes | Read-only |
| 7. Metrics | yes | Read-only |
| 8. Contact owners | no | A message cannot be recalled |
| 9. Wait | yes | Change the date, which is a restart |
| 10. Listings | partly | Only in each portal |
| 11. Updater URL and probes | yes, until step 17 | Set the old URL back while the old pair is served |
| 12. OpenAI challenge | partly | The old token can be served again; a verification is OpenAI's |
| 13. Mail | partly | At the mail provider; mail refused in between is lost |
| 14. Website redirect | yes | Read-only |
| 15. Go or no-go | yes | The checks change nothing; a no-go is a restart and a message |
| 16. Swap | yes | Start the kept file |
| 17. Retire | partly | Start the step 16 file; deleted sign-ins stay deleted; a database restore is not an undo |
| 18. Updater back on | yes | Disable the timer again; only releases with #1134 from here on |
| 19. Keep DNS and entry | yes | Nothing to undo |
| 20. Old OAuth app | yes | Left in place; removing it is a separate decision |
| 21. Keep the domain | no | A lapsed domain may not come back |
