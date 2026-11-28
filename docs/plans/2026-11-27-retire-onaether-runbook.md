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
- It keeps everything recorded at `auth.cloudfield.ai`, used since or not.
  The recorded origin decides, never the last use
  ([step 17](#17-retire-the-old-pair)).
- A server not moved is unreachable through the edge until its admin moves
  it: on a build with #1132, `sudo cloudfield-server edge move
  https://edge.cloudfield.ai` and a restart; on an older build by hand,
  against the fingerprint of step 3
  ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).

Step 15 is the go/no-go on those numbers.

## The maintenance window

This runbook is for one host: the edge as the Compose service
`aether-edge`, updated by `aether-edge-update`
([edge.md](../edge.md#automatic-updates) says the hosted edge follows
`stable`, which only the updater does, and the updater runs only on
Compose). [Check the host first](#check-the-host-first) confirms that.

The updater runs `docker compose up` on the same project, daily at 04:00
UTC with up to an hour's random delay, and a failed update rolls back by
itself. Every run takes an exclusive lock on
`/var/lib/aether-edge-update/lock` first and exits when another process
holds it. Every step that edits a file, starts the edge or takes a backup
holds that lock from before its first command until after its last check.
The steps call this a **window**.

**Open a window.** Start a root shell and take the lock; every command
until `exit` runs in this shell, without `sudo`:

```sh
sudo bash
```

```sh
exec 9>/var/lib/aether-edge-update/lock
flock -n 9 && echo locked
grep -E '^(state|kind|from_version|to_version|since)=' /var/lib/aether-edge-update/state
```

- `locked` must print. Without it an updater run holds the lock: follow it
  with `journalctl -u aether-edge-update.service -f`, and repeat the
  `flock` line when it has ended. Never `systemctl stop
  aether-edge-update.service`: that marks a switch in progress for
  rollback and rolls it back.
- Expect `state=idle`, `state=held`, or no state file (`grep` then says
  so). `held` changes nothing by itself.
- `state=switching` or `state=rolling-back` is a run that was
  interrupted. It must complete before anything else: `exit`, run `sudo
  systemctl start aether-edge-update-recover.service`, which finishes the
  switch or rolls it back with the unit's own settings and returns when it
  is done, read `journalctl -u aether-edge-update-recover.service -n 30
  --no-pager`, and open the window again.
- `state=broken` is in [edge.md](../edge.md#when-an-update-stops). No step
  of this runbook runs until it is fixed.

Then paste the helpers. They read the Compose project, directory, file and
env file from the running container's labels, so every `docker compose`
command below uses what the container was started with:

```sh
lbl() { docker inspect aether-edge --format "{{index .Config.Labels \"com.docker.compose.$1\"}}"; }
EDGE_PROJECT=$(lbl project) EDGE_DIR=$(lbl project.working_dir)
EDGE_FILE=$(lbl project.config_files) EDGE_ENV=$(lbl project.environment_file)
EDGE_ENV=${EDGE_ENV:-$EDGE_DIR/.env} EDGE_NEXT=$EDGE_DIR/compose.next.yaml
echo "$EDGE_PROJECT | $EDGE_DIR | $EDGE_FILE | $EDGE_ENV"

# dc <compose file> <command...>
dc() { (cd "$EDGE_DIR" && docker compose -p "$EDGE_PROJECT" --env-file "$EDGE_ENV" -f "$1" "${@:2}"); }

# Values of the listed settings; of every other edge variable, the name only.
edge_settings() {
  jq -r '
    (map(capture("^(?<key>[^=]+)=(?<value>.*)$"; "s")) | from_entries) as $e
    | ("SIGNIN_ORIGIN RELAY_ORIGIN GITHUB_CLIENT_ID GITHUB_CLIENT_SECRET_FILE ORIGIN_PAIRS RETIRING_PAIRS RETIRED_PAIRS DATA METRICS_LISTEN" / " ") as $allow
    | ($allow[] | . as $n | ($e["CLOUDFIELD_EDGE_" + $n] // "") as $new | ($e["AETHER_EDGE_" + $n] // "") as $old
       | "\($n) = \(if $new != "" then $new else $old end)"
         + (if $new != "" and $new != $old then "  (from CLOUDFIELD_EDGE_\($n); AETHER_EDGE_\($n) = \($old))" else "" end)),
      "other edge variables set, names only: \([$e | keys[] | select(test("^(CLOUDFIELD|AETHER)_EDGE_")) | select(sub("^(CLOUDFIELD|AETHER)_EDGE_"; "") as $n | $allow | index($n) | not)] | join(" "))"'
}

# What the running container was started with.
running() {
  docker inspect aether-edge --format 'image = {{.Config.Image}}'
  docker inspect aether-edge --format 'arguments = {{len .Args}}'
  docker inspect aether-edge --format '{{json .Config.Env}}' | edge_settings
}

# What "up" would start from the compose file $1.
proposed() {
  dc "$1" config --format json | jq -r '.services["aether-edge"]
    | "image = \(.image)", "arguments = \(.command // ["serve"] | length)"'
  dc "$1" config --format json | jq -c '.services["aether-edge"]
    | [.environment // {} | to_entries[] | "\(.key)=\(.value)"]' | edge_settings
}

# The claimed-server count, only when the edge is ready.
ready() {
  local out
  out=$(timeout 30 docker exec aether-edge aether-edge healthcheck --ready --json) &&
    printf '%s\n' "$out" | jq -er 'select(.status == "ready") | .servers.claimed | numbers'
}

# wait_ready <n>: up to 180 s for the edge to be ready with n claimed servers.
wait_ready() {
  local n i
  for i in $(seq 36); do
    n=$(ready 2>/dev/null) && [ "$n" -ge "$1" ] && { echo "ready, $n claimed servers"; return 0; }
    sleep 5
  done
  echo "after 180 s: $(ready 2>&1 || echo not ready), wanted $1 claimed servers"
  return 1
}
```

The `echo` prints `aether-edge | /opt/aether-edge |
/opt/aether-edge/compose.yaml | /opt/aether-edge/.env`. An empty
`environment_file` label means the last `up` named no `--env-file`, and
Compose read `.env` in the directory; the helper uses that.

The edge's variables can hold one secret inline,
`AETHER_EDGE_GITHUB_CLIENT_SECRET` (or its `CLOUDFIELD_EDGE_` name);
`edge_settings` prints the value of the nine settings it lists, none of
which is a secret, and only the name of any other. For each option the
edge uses the `CLOUDFIELD_EDGE_<NAME>` variable when it is not empty, else
`AETHER_EDGE_<NAME>`, without a warning when both are set
(`internal/envname`); the helper prints the value in effect and marks one
that comes from a `CLOUDFIELD_EDGE_` name. `healthcheck --ready --json`
prints its report also when the edge is not ready, so `ready` prints a
count only when the command exits 0 and the report says `ready`, as the
updater checks it.

**Change the settings.** Steps 5, 16 and 17 run this in an open window,
with `<n>` the step's number:

1. Note the count before: `before=$(ready); echo "$before"`.
2. Copy the file and edit the copy, never `compose.yaml` itself:
   `cp -a "$EDGE_FILE" "$EDGE_NEXT"`, then the step's lines in
   `$EDGE_NEXT`.
3. Compare what would start with what runs:

   ```sh
   diff <(running) <(proposed "$EDGE_NEXT")
   ```

   It must show the lines the step names and no other: no changed
   `image`, `arguments`, or setting the step did not edit. The values of
   variables outside the list are not compared.
4. Install the file and start it, keeping the old file as the undo:

   ```sh
   cp -a "$EDGE_FILE" "$EDGE_FILE.before-step-<n>"
   mv "$EDGE_NEXT" "$EDGE_FILE"
   dc "$EDGE_FILE" up -d --wait --wait-timeout 180 aether-edge; echo "up exited $?"
   docker inspect aether-edge --format '{{.Created}} {{.State.Status}} {{.RestartCount}}'
   ```

   Expect `up exited 0` and `<a time after the mv> running 0`. Compose made
   a new container for the changed settings, so `docker logs aether-edge`
   holds this start only, and `0` says it started once. `--wait` waits for
   the image's health check, which is liveness only.
5. Wait for readiness and the servers: `wait_ready "$before"`. If it ends
   `after 180 s`, read the line. A count below `$before` with the edge
   ready is a server that has not dialed again: wait and run `ready`
   again, and decide before going on. Anything else is the edge's own
   reason.
6. The step's own checks.
7. `diff <(running) <(proposed "$EDGE_FILE") && echo same` prints `same`.

The undo of a change is the old file: `cp -a
"$EDGE_FILE.before-step-<n>" "$EDGE_NEXT"`, then from 3.

**Close the window** with `exit`. The lock goes with the shell. While a
window is open, a run the timer starts logs `edge-update: another run
holds /var/lib/aether-edge-update/lock; exiting`, exits 0, and that day's
update waits for the next day's run.

**A reboot inside a window** releases the lock. Docker starts the
container again as it was last created, whatever `compose.yaml` holds;
`aether-edge-update-recover.service` runs; and the timer, being
`Persistent=true`, runs the updater for a 04:00 UTC window missed while
the host was off, after up to an hour's random delay. That run starts
whatever `compose.yaml` holds, but only if `stable` has a new release.
Because step 4 of the procedure installs the file with one `mv`, the file
is the old one or the new one, never half of an edit. After a reboot, open
a window at once and run `diff <(running) <(proposed "$EDGE_FILE")`: if it
is not empty, the new file was installed and not started; go on from the
`dc ... up` line, or restore the old file.

### No rollback to a release without the code

From step 5 until the checks of step 17 have passed, never run
`aether-edge-update --rollback`, with or without `--restore-backup`, and
never write another digest to `/opt/aether-edge/.env` by hand.
`/var/lib/aether-edge-update/previous` names the release the updater last
replaced, and the one before the release of step 1 has no retirement code.
Such an image ignores `AETHER_EDGE_RETIRING_PAIRS`, an unknown variable.
Only a binary that knows the option turns recording on
(`cmd/aether-edge/main.go`), so the edge stops recording and issues device
tokens and browser sessions with no recorded origin, at both pairs; step
17 then deletes them at both pairs. `--restore-backup` also puts the
pre-update database back, and one from before step 5 holds no recorded
origin at all.

After step 17, roll back only to a release that contains #1134
(`6b70f903c`). An older image ignores `AETHER_EDGE_RETIRED_PAIRS`: it
sends servers no signed `retired` notice, so `edge move` from the old
address stops working, it answers the old hosts `421`, and it issues
sign-ins with no recorded origin, which the next start of a release with
the entry deletes at the pair that stays. Before any `--rollback`:

```sh
sudo cut -d' ' -f2 /var/lib/aether-edge-update/previous
```

prints the release it would return to; in a clone, `git tag --contains
6b70f903c` must list it. Never add `--restore-backup` while the third
field of that file, the backup's path, carries a time before step 17.

The updater's own rollback after a failed update returns to the release
that ran before that update. It restores that update's backup, minutes
old, only when the older release cannot run the database.

## Check the host first

Nothing in the repository says how the hosted edge is deployed. Verify
each line on the host before step 1. Where one differs, stop: the steps do
not apply as written.

- [ ] **It runs as the Compose service.**

  ```sh
  sudo docker ps --filter name='^aether-edge$' --format '{{.ID}} {{.Image}} {{.Status}}'
  systemctl is-active aether-edge.service
  ```

  Expect one line, `<id> ghcr.io/3xdevops/aether-edge@sha256:<digest> Up
  <time> (healthy)`, then `inactive`. With no line, or with `active`, the
  edge does not run as this runbook assumes: stop.
- [ ] **The updater.**

  ```sh
  systemctl is-enabled aether-edge-update.timer aether-edge-update-recover.service
  systemctl is-active aether-edge-update.timer
  systemctl is-active aether-edge-update.service aether-edge-update-recover.service
  systemctl show aether-edge-update.service aether-edge-update-recover.service -p Environment -p EnvironmentFiles --value | grep -c .
  sudo env -i PATH="$PATH" sh -c '
    . /etc/aether-edge-update.env
    for n in CHANNEL RELAY_URL COMPOSE_DIR DIGEST_FILE STATE; do
      eval "v=\${CLOUDFIELD_EDGE_UPDATE_$n:-\${AETHER_EDGE_UPDATE_$n:-}}"
      echo "$n = ${v:-(default)}"
    done'
  ```

  Expect `enabled` twice and `active`. Each service prints `inactive` or
  `failed`. `failed` is the last run's exit status, not a run in progress:
  a held release exits 3 and every other stop, a completed automatic
  rollback included, exits 1; `journalctl -u aether-edge-update.service -n
  30 --no-pager` says which. The count is `0`: neither unit sets a
  variable, so both read `/etc/aether-edge-update.env`. The last command
  reads that file as the updater does, a shell file it sources, and prints
  the five settings this runbook relies on: `CHANNEL` is `stable` or
  `(default)`, `RELAY_URL` is `https://edge.onaether.dev`, and
  `COMPOSE_DIR`, `DIGEST_FILE` and `STATE` are `(default)`, which is
  `/opt/aether-edge`, `/opt/aether-edge/.env` and
  `/var/lib/aether-edge-update`.
- [ ] **The project, and the running settings.** [Open a
  window](#the-maintenance-window). The `echo` prints the four values shown
  there: one compose file, the one in the updater's directory, and the
  updater's digest file. Then:

  ```sh
  docker inspect aether-edge --format '{{.State.Status}} {{.State.Health.Status}} {{.RestartCount}} {{.Path}}'
  running
  ```

  Expect `running healthy 0 /usr/local/bin/aether-edge`, then:
  - `image` with the digest of the first item;
  - `arguments = 1`, the image's own `serve`: a flag on the command line
    would win over every variable below;
  - the old pair in `SIGNIN_ORIGIN`, `RELAY_ORIGIN` and `GITHUB_CLIENT_ID`,
    with `GITHUB_CLIENT_SECRET_FILE = /run/secrets/github-client-secret`;
  - the cloudfield.ai pair in `ORIGIN_PAIRS`, ending in
    `/run/secrets/github-client-secret-2`
    ([rename plan, C3](2026-11-15-cloudfield-rename.md#resubmitting-the-listings-owners-checklist));
  - `RETIRING_PAIRS`, `RETIRED_PAIRS`, `DATA` and `METRICS_LISTEN` empty
    (the defaults are `/var/lib/aether-edge` and `127.0.0.1:9464`);
  - no line marked `(from CLOUDFIELD_EDGE_...)`. If one is, every edit
    below goes to that name, and the `AETHER_EDGE_` name of the same
    option gets the same value in the same edit: only two such conflicts
    are refused, both at step 17;
  - no `GITHUB_CLIENT_SECRET` among the other names.
- [ ] **The file on disk is what runs.**

  ```sh
  diff <(running) <(proposed "$EDGE_FILE") && echo same
  ```

  Expect `same`. A difference is an edit or a digest that was never
  started: find out which before step 1, because the next `up` starts it.
- [ ] **Data and secrets.**

  ```sh
  docker inspect aether-edge --format '{{range .Mounts}}{{.Type}} {{.Source}} {{.Destination}}{{println}}{{end}}'
  ```

  Expect `bind /opt/aether-edge/data /var/lib/aether-edge`, and one `bind`
  line each to `/run/secrets/github-client-secret` and
  `/run/secrets/github-client-secret-2`. The second field of those two is
  the secret's file on the host: `ls -ln <file>` shows owner `65532` and
  mode `-r--------` for each.
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
  docker exec aether-edge aether-edge retiring | head -n 1
  ```

  Expect `no origin pairs recorded: the edge records them from its first
  start with an option that retires a pair`, or `nothing is recorded yet:
  ...` on a release without the code. Step 5 is then the first recording.

Close the window.

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
sudo aether-edge-update --check
```

Expect `2` from the second. In a clone, `git merge-base --is-ancestor
6b70f903c <commit> && echo yes` prints `yes` for the printed commit. The
third checks the running digest's promotion and signed release and prints
`edge-update: verified the running <version> (<digest>)`, then `up to
date: ...` or `would update ...`; it changes nothing. Run it outside a
window: inside one it exits with `another run holds ...`.

- Undo: nothing changed.
- Cost to users: none.

### 2. Back up edge.db

In a [window](#the-maintenance-window):

```sh
docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
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
sudo ssh-keygen -lf /opt/aether-edge/data/edge_key
```

The first two print the same `SHA256:` fingerprint, and the third prints it
in `256 SHA256:<fingerprint> aether edge (ED25519)`.

The edge does not start without `edge_key` beside a database that holds
accounts or servers. Where the offline copy that
[edge.md](../edge.md#backup-and-recovery) asks for is kept, `ssh-keygen
-lf <copy>` must print the same fingerprint; a copy that exists but prints
another is no copy. Without one, make it now with `docker exec aether-edge
aether-edge backup --with-key /var/lib/aether-edge/backups/` in a window,
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
about the pair that stays. The checks have two parts.

**Read-only checks.** They leave nothing behind:

```sh
curl -fsS -H 'Aether-Edge-Version: 6' https://edge.cloudfield.ai/v1/edge | jq -r '.signin_origin, .fingerprint, .retiring'
```

Expect `https://auth.cloudfield.ai`, the fingerprint of step 3 and `null`.
Then:

- **A token issued earlier.** On a computer that signed in at
  `https://edge.cloudfield.ai` before, `cloudfield servers --edge
  https://edge.cloudfield.ai` lists its servers. It shows that this one
  token is accepted, and nothing about any other.
- **A server connected.** On a server whose `edge-url` is
  `https://edge.cloudfield.ai`, `sudo cloudfield-server edge status` exits
  0 with `connection   connected since <time>; the edge last answered <n>s
  ago`. After a restart of the edge, `<time>` is later than the restart.
- **A connector.** A connector added at `https://edge.cloudfield.ai/mcp`
  returns a result for a tool call that only reads.

**Sign-in checks.** They sign in, so they run where the owner's own
sign-ins and links are not: a new browser profile, and a config directory
of their own on any computer with the CLI. A private window can share a
session with other private windows and then skips GitHub; a new profile
cannot.

- **A fresh browser sign-in.** In the new profile, open
  `https://auth.cloudfield.ai/signin` and sign in with GitHub. A wrong
  secret ends in `GitHub sign-in: exchange code: <error>`.
- **A fresh device sign-in and a relayed connection.** The CLI keeps its
  links, device key and tokens in one directory, which
  `CLOUDFIELD_CONFIG_DIR` replaces (`internal/cli/config.go`). In one
  terminal:

  ```sh
  export CLOUDFIELD_CONFIG_DIR="$(mktemp -d)"
  cloudfield login --edge https://edge.cloudfield.ai
  cloudfield link <server id> --edge https://edge.cloudfield.ai
  ```

  Confirm the code in the new profile. `login` prints:

  ```
  auth.cloudfield.ai signs you in for the edge edge.cloudfield.ai
  open https://auth.cloudfield.ai/device and enter the code <code>
  waiting for you to confirm the code...
  signed in to edge.cloudfield.ai as <login> (github); this device is "<label>"
  ```

  and then five lines on the token's lifetime. `link`, for a server
  enrolled at `https://edge.cloudfield.ai` that the account is a member
  of, prints `linked to server <server id> through edge.cloudfield.ai as
  <name> (<role>)`. This directory has a device key of its own, so the
  server sees a new device: under `approved-devices` the command first
  prints `waiting for an admin of <server> to approve this device (<label>,
  <fingerprint>)…` until it is approved from the owner's own computer
  ([teams.md](../teams.md#approve-devices)).

Keep the terminal's directory for steps 16 and 17: there, run `cloudfield
logout --edge https://edge.cloudfield.ai` and the two commands again, so
the sign-in is a fresh one.

- Undo, after step 17: in that terminal, `cloudfield logout --edge
  https://edge.cloudfield.ai` revokes the token at the edge; on the owner's
  own computer, `cloudfield device list` and `cloudfield device revoke
  <device-id>` remove the test device from the server; then `rm -r
  "$CLOUDFIELD_CONFIG_DIR"`. Delete the browser profile; the session it
  leaves at the edge ends after 30 days unused. The owner's own config
  directory, default link and sign-ins were never touched.
- Cost to users: none.

## Phase 1: mark the old pair retiring

### 5. Set AETHER_EDGE_RETIRING_PAIRS

The warning period starts here. The old pair is the first pair. Marking it
retiring needs no swap, because the option names a pair by its relay
origin. Retiring it later does need the swap (step 16).

In a window, [change the settings](#the-maintenance-window) with `<n>` 5.
The line to add to the service's `environment:`:

```yaml
      AETHER_EDGE_RETIRING_PAIRS: https://edge.onaether.dev https://edge.cloudfield.ai 2026-12-15
```

The `diff` shows one changed line, `RETIRING_PAIRS`, with that value. The
step's own checks:

```sh
docker logs aether-edge 2>&1 | grep -e 'aether-edge: retiring' -e '^aether-edge:'
```

Expect `aether-edge: retiring relay=https://edge.onaether.dev
replacement=https://edge.cloudfield.ai date=2026-12-15`. A date that is
not real is refused: `aether-edge: --retiring-pairs: the date "<date>" of
https://edge.onaether.dev is not a real date written YYYY-MM-DD, such as
2027-03-01`.

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
carries no such header. Run the read-only checks of step 4, note the time
of this start for step 15, and close the window.

On a server enrolled at `https://edge.onaether.dev` whose build has #1132,
once it has connected again, `sudo cloudfield-server edge status` prints
`retiring     https://edge.onaether.dev is retiring on 2026-12-15; move this
server with: sudo cloudfield-server edge move https://edge.cloudfield.ai`,
the journal logs the same as a warning at each connection, and super admins
see a warning with the move and restart commands on Settings, Remote access
and a card on Needs you
([edge-retire.md](../edge-retire.md#moving-a-server)).

From this start, and while this option or `AETHER_EDGE_RETIRED_PAIRS` is
set on a release that has the code, the edge records for every pair the
sign-in origin each device token, browser session and pending device
sign-in is issued at; for a token or session issued earlier, the sign-in
origin of the pair it is next used at; and each claimed server's relay
origin at each enrollment or claim
([edge.md](../edge.md#monitoring), [privacy.md](../privacy.md)).

- Undo: the old file, as in the procedure. Origins recorded meanwhile
  stay in `edge.db`. **An undo disarms step 17's safety check.** That
  check refuses only an edge that never recorded. After an undo, or with
  the option set for a day, step 17 starts, and deletes every sign-in
  that has no recorded origin by then. Without the option the edge also
  issues tokens and sessions with no recorded origin, which step 17
  deletes at both pairs. After an undo, do this step again and wait the
  whole period before step 17.
- Cost to users: the restart ends every relayed connection; servers dial
  again by themselves. A device sign-in that was pending at this restart
  is deleted, since it was started before the edge recorded origins; its
  person runs `cloudfield login` again. Removing the option does not bring
  it back.

## Phase 2: watch who is left

### 6. Read the report

```sh
sudo docker exec aether-edge aether-edge retiring --origin https://edge.onaether.dev
```

- The first line is the last known recording period: `since <time>`, when
  it began; `last started <time>`, the edge's last start with recording;
  and whether it stopped cleanly. A `since` later than step 5 says a new
  period began: after an unclean stop, or a start more than 10 minutes
  after the last clean one. A `since` equal to step 5 does not prove the
  edge recorded all the time: a run without recording that fits between a
  clean stop and a start within 10 minutes leaves no trace here. The line
  does not filter the counts.
- Each count is of rows that exist now, by last use within 1, 7 and 30
  days and in all. A revoked token has no row, an expired session is not
  counted, and a pending device sign-in is counted nowhere. The session
  rows count accounts, not sessions.
- The server table (`SERVER ID  NAME  OWNER LOGIN  BUILD  LAST ENROLLED OR
  CLAIMED`) lists the claimed servers whose last recorded enrollment or
  claim was at `https://edge.onaether.dev`, or `no server last enrolled or
  claimed here`. The owner login is empty for a server with no owner.
- `device tokens issued here`: tokens issued at `https://auth.onaether.dev`
  while the edge recorded. `device tokens issued earlier, first seen
  here`: tokens issued while it did not, whose first use since came in on
  a host of the old pair. Step 17 deletes both, however recently used.
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
- A section `recorded at an address this edge no longer pairs: <origin>`
  counts the tokens and sessions recorded at a sign-in origin no recorded
  pair names now. Step 17 deletes them when that origin has the host name
  `auth.onaether.dev`, or was once recorded beside `edge.onaether.dev` and
  no served pair signs in at it.
- `unknown origin`: the claimed servers that have not enrolled or been
  claimed since recording began, and the device tokens and sessions with
  no recorded origin. Step 17 deletes those tokens and sessions at both
  pairs; the servers stay at whatever address they dial, and
  `aether-edge servers list` names their owners.

Run it without `--origin` too: the section of `https://edge.cloudfield.ai`
counts what step 17 keeps. A token issued before step 5 and first used
since on a cloudfield.ai host is counted, and kept, there.

An empty list is not proof that nobody is left; the report says why in its
last lines. The old pair keeps its section in the report after step 17,
with or without servers.

- Undo: nothing changed (the command opens `edge.db` read-only).
- Cost to users: none.

### 7. Read the metrics

The metrics listener is on loopback inside the container, and the image
has no shell or `curl`. This runs the host's `curl` in the container's
network namespace and starts no other image:

```sh
sudo nsenter -t "$(sudo docker inspect -f '{{.State.Pid}}' aether-edge)" -n \
  curl -q -sS --noproxy '*' --max-time 10 http://127.0.0.1:9464/metrics |
  grep -e aether_edge_servers_by_origin -e aether_edge_device_logins_total -e aether_edge_connector_requests_total -e aether_edge_retired_requests_total
```

`nsenter` is from util-linux, as the `flock` the updater needs is.
`127.0.0.1:9464` is the default the host check found in effect.

`aether_edge_servers_by_origin{origin="https://edge.onaether.dev"}` counts
servers connected there now, unclaimed ones too, which the report misses.
`aether_edge_device_logins_total{origin}` and
`aether_edge_connector_requests_total{origin,protocol}` count device
sign-ins and connector requests per address since the edge last started.
`aether_edge_retired_requests_total` exists only from step 17 on.

- Undo: nothing changed.
- Cost to users: none.

### 8. Contact owners

After the first report, and again for each owner who appears in a later
one: a server shows in the table only once it has enrolled again after
step 5, which can be after the first report. Who can be named:

- each claimed server recorded on the old pair, by the owner login in the
  report's table;
- the claimed servers under `unknown origin`, by `sudo docker exec
  aether-edge aether-edge servers list`, which prints every server with
  its owner but not the address it dials.

Nobody else: a server with no owner has no login, an unclaimed server has
no row, and the people behind device tokens and connectors are only
counted. They see the warnings in the product, on builds that show them.
Writing to owners is outside the product, and whether to is the owner's
decision: the plan sends no mail
([plan, For the owner to decide](2026-11-26-retire-address-pair.md#for-the-owner-to-decide)),
and [privacy.md](../privacy.md) would have to allow it. Give the date, and
point them to [edge-retire.md](../edge-retire.md).

- Undo: none; a message cannot be recalled.
- Cost to users: none.

### 9. Wait

Recommendation: at least 30 days after step 5. A device token never
expires, and one issued before step 5 gets a recorded origin only when it
is next used; step 17 deletes those that have none. The edge's own refusal
names 30 days as the example, and checks only that recording ever ran,
not for how long.

The owner chose `2026-12-15` knowing it gives less than that, counted from
step 5 and not from the day it was chosen, at the costs under
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

A run of the updater that switches release fetches `<relay URL>/v1/edge`
before the switch and after it; a run that finds the edge up to date never
does. A retired relay answers `410` there. With the old URL after step 17,
an update waits `AETHER_EDGE_UPDATE_PUBLIC_WAIT` seconds (default 300) and
exits 1 with `GET https://edge.onaether.dev/v1/edge failed; the running
edge must answer before an update`, changing nothing. In a window, set in
`/etc/aether-edge-update.env`:

```sh
AETHER_EDGE_UPDATE_RELAY_URL=https://edge.cloudfield.ai
```

Then run the last command of the updater item of
[Check the host first](#check-the-host-first) again: `RELAY_URL =
https://edge.cloudfield.ai`, the rest as before. Close the window. The
updater's own request:

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
2. Read the report's first line as step 6 says. A `since` later than step
   5 means a new period began; origins recorded before it are kept, and
   whatever was issued while the edge did not record has no origin, which
   step 17 deletes.
3. Check that no image without the code ran since step 5:

   ```sh
   journalctl -u aether-edge-update.service -u aether-edge-update-recover.service --since '<time of step 5>' --no-pager | grep -e 'now running' -e 'rolling back' -e 'rolled back'
   sudo cat /var/lib/aether-edge-update/current
   sudo grep -c 'rolled back by an operator' /var/lib/aether-edge-update/failed
   sudo docker exec aether-edge aether-edge version --json | jq -r .version,.commit
   ```

   The first prints the updater's switches: `edge-update: now running
   <version> (<digest>)` for each update, and a `rolling back` and a
   `rolled back` line for each automatic rollback. The second prints the
   digest and release the updater last switched to. The third prints `0`,
   or that the file does not exist. Every release named, and the last
   command's, must be in `git tag --contains 6b70f903c`. These are the
   updater's own records. They do not show a digest written by hand, or a
   `--rollback` run from a shell that failed and returned: that is why
   [both are forbidden](#no-rollback-to-a-release-without-the-code). If an
   image without the code ran, no-go: what it issued has no origin.
4. Check the live warning, with the two `curl` lines of step 5.
5. For each server still in the table, each nonzero row of the old pair,
   of a `no longer pairs` section and of `unknown origin`, and each
   server the metric counts on the old relay origin, decide: retire
   anyway, or wait. Contact owners who are new in the table (step 8).
   Whoever is left pays the costs under [Notice](#notice). A server whose
   admin cannot be reached stays unreachable through the edge until
   someone with a shell on its host moves it; member SSH keys and tailnet
   access are not affected.
6. Check that steps 10 to 14 are settled, and run all of step 4.
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

In a window, [change the settings](#the-maintenance-window) with `<n>` 16:

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
docker logs aether-edge 2>&1 | grep -e 'aether-edge: serving' -e 'aether-edge: retiring' -e '^aether-edge:'
```

Expect `aether-edge: serving signin=https://auth.cloudfield.ai
relay=https://edge.cloudfield.ai metrics=<address>`, then `aether-edge:
serving signin=https://auth.onaether.dev relay=https://edge.onaether.dev`,
then the retiring line of step 5. Both `curl` lines of step 3 still print
the same fingerprint. Run all of step 4, and the fresh browser sign-in on
`https://auth.onaether.dev/signin` too: each sign-in host uses its own
OAuth app, and a wrong id or secret shows only in a real sign-in. Close
the window, unless step 17 follows at once.

- Undo: the old file, as in the procedure.
- Cost to users: the restart ends every relayed connection; servers dial
  again by themselves.

### 17. Retire the old pair

In a window. First a fresh backup, minutes before the edit, copied off
the machine as in step 2:

```sh
docker exec aether-edge aether-edge backup /var/lib/aether-edge/backups/
```

Note how many servers are connected at the pair that stays, to compare
like with like afterwards:

```sh
kept() { nsenter -t "$(docker inspect -f '{{.State.Pid}}' aether-edge)" -n curl -q -sS --noproxy '*' --max-time 10 http://127.0.0.1:9464/metrics | grep -F 'aether_edge_servers_by_origin{origin="https://edge.cloudfield.ai"}'; }
kept
```

Then [change the settings](#the-maintenance-window) with `<n>` 17. In one
edit: the old pair leaves `AETHER_EDGE_ORIGIN_PAIRS` entirely,
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

The `diff` shows three changed lines: `ORIGIN_PAIRS` and `RETIRING_PAIRS`
now empty, `RETIRED_PAIRS` the line above. In the procedure, wait with
`wait_ready 0`: the claimed count falls by the servers still at the old
pair. The step's own checks:

```sh
docker logs aether-edge 2>&1 | grep -e 'edge: deleted the sign-ins' -e 'edge: keeping the sign-ins' -e 'aether-edge: retired' -e '^aether-edge:'
kept
```

Expect no `edge: keeping the sign-ins` line, and in this order:

```
edge: deleted the sign-ins of retired pairs and those with no recorded origin devices=<n> sessions=<n> authorizations=<n>
aether-edge: retired signin=https://auth.onaether.dev relay=https://edge.onaether.dev replacement=https://edge.cloudfield.ai
```

Run `kept` again until it prints the number noted before, for at most 3
minutes. A lower number after that is a server at the cloudfield.ai pair
that has not dialed again: find out which before closing the window.

**What this start deletes**, in one transaction, once the listeners are
bound and before the edge serves: each device token, browser session and
pending device sign-in whose recorded sign-in origin

- has the host name `auth.onaether.dev`, whatever its port; or
- has the host name of another sign-in origin the edge once recorded
  beside `edge.onaether.dev`, unless a served pair signs in at that host
  (the edge then keeps those rows and logs `edge: keeping the sign-ins of
  a served sign-in origin once recorded beside a retired relay origin`);
  or
- is missing: the row was issued while the edge did not record and has
  not been used since at an edge that records. This applies at both
  pairs.

Every start that records also deletes the pending device sign-ins bound
to no relay origin.

**What it keeps:** every row recorded at `https://auth.cloudfield.ai`,
whether it was issued there or first seen there, used since or not. When a
token was last used plays no part.

The edge answers a retired host `410` before it reads any token, so a
`410` says nothing about whether a token still exists. A device that may
still hold a valid token is revoked on
`https://auth.cloudfield.ai/devices`, never by sending its token to
another host.

The deletion commits before its log line is written and before the edge
answers a health check. A failed or timed-out `up`, a missing `edge:
deleted the sign-ins` line, or an error after the start do not show that
nothing was deleted. Only a start refused with one of the lines below,
all checked before the transaction, deleted nothing.

If the edge does not start, the `docker logs` line above ends with
`aether-edge: <refusal>`:

| Refusal | Fix |
| --- | --- |
| `--retired-pairs: https://auth.onaether.dev is still served as --signin-origin or --relay-origin; first make its replacement the first pair (--signin-origin, --relay-origin, --github-client-id) and remove this pair` | Step 16 first |
| `--retired-pairs: https://auth.onaether.dev is still served; remove its pair from --origin-pairs in the same edit` | Remove `AETHER_EDGE_ORIGIN_PAIRS`, and `CLOUDFIELD_EDGE_ORIGIN_PAIRS` if set |
| `--retiring-pairs: this edge serves one address pair, which cannot retire; serve its replacement first with --origin-pairs (AETHER_EDGE_ORIGIN_PAIRS)` | Remove `AETHER_EDGE_RETIRING_PAIRS`, and `CLOUDFIELD_EDGE_RETIRING_PAIRS` if set |
| `AETHER_EDGE_SIGNIN_ORIGIN names https://auth.onaether.dev, which --retired-pairs retires: an edge image older than v0.8.7 reads only the AETHER_EDGE_ names and would serve it in full after a rollback. Remove AETHER_EDGE_SIGNIN_ORIGIN, or set it to the value of CLOUDFIELD_EDGE_SIGNIN_ORIGIN` (the same for `AETHER_EDGE_RELAY_ORIGIN` and `AETHER_EDGE_ORIGIN_PAIRS`) | Set that `AETHER_EDGE_` name to the value in effect. Never remove the `CLOUDFIELD_EDGE_` name instead: the edge would then serve the old pair |
| `this edge uses the data directory <dir> from CLOUDFIELD_EDGE_DATA, but an edge image older than v0.8.7 reads only AETHER_EDGE_DATA and would open /var/lib/aether-edge, whose database keeps the sign-ins --retired-pairs deletes, after a rollback. Set AETHER_EDGE_DATA to <dir>, or pass --data <dir> on the command line, which both images read` | Set `AETHER_EDGE_DATA` to `<dir>` |
| `edge: --retired-pairs would delete, on every pair, the sign-ins with no recorded origin (device tokens <n>, browser sessions <n>, pending device sign-ins <n>), since this edge has never recorded which pair issued them. ...` | This database never recorded: step 5 never ran on it, or it was restored from a backup older than step 5. Restore the step 16 file, then step 5 and the wait |
| `--retired-pairs names <n> pairs; an edge keeps at most 16`, or `--retiring-pairs and --retired-pairs name <n> pairs together, and a server is sent at most 16 notices: ...` | Fewer entries; cannot happen with one |

The other refusals are in [edge.md](../edge.md#retired). Between the two
prefixes the edge refuses only the two conflicts in the table, over the
origins and the data directory. A `CLOUDFIELD_EDGE_` value that overrides
the client id, the secret file, or the retiring or retired pairs is used
as it is, which is why the `diff` comes first.

A refused start serves nothing: both pairs are down, and the container
restarts into the same refusal, until the step 16 file is back and
started.

Against the live edge. First the old hosts, both:

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
relay host.

Then the pair that stays: all of step 4. Its token issued earlier still
lists its servers; that shows this one token was kept, not what else was.

The metric `aether_edge_retired_requests_total{origin,kind}` (step 7)
counts what still arrives at each old host, by `server`, `client`,
`connector` and `browser`. `curl -fsS https://edge.onaether.dev/healthz`
still prints `ok`.

Once the checks pass, take another backup with the command above and copy
it off the machine: it is the one to restore after a later failure. Then
close the window.

**What cannot be undone.** The device tokens, browser sessions and pending
sign-ins this start deleted stay deleted: those people sign in again at
`auth.cloudfield.ai`, and servers keep their record of each device and its
approval. A database restore is not an undo:

- The step 2 backup, or any from before step 5, has no recorded origin on
  any row and no record that recording ran. With
  `AETHER_EDGE_RETIRED_PAIRS` set the edge refuses to start on it while
  it holds a sign-in (the `never recorded` refusal above).
- On the backup from the top of this step the edge starts and deletes the
  same sign-ins again. That restore also brings back every device token
  revoked since the backup, and loses every account, device sign-in, claim
  and owner change made since.
- Either backup under an image older than #1134, or with the option
  removed, makes the deleted tokens valid again at every served pair.
- The backup taken after the checks does not hold the deleted rows.

From here the rule under
[No rollback to a release without the code](#no-rollback-to-a-release-without-the-code)
applies in its second form.

- Undo, partly: the step 16 file, as in the procedure. Both hosts are
  served again; the deleted sign-ins are not restored.
- Cost to users: the restart ends every relayed connection, at the
  cloudfield.ai pair too; servers there dial again by themselves. Every
  device token and browser session listed under **What this start
  deletes** is signed out.
  A server still at `edge.onaether.dev` is unreachable through the edge
  until its admin moves it; connectors added at `https://edge.onaether.dev/mcp`
  fail; join and claim links made there stop working.
  On a server whose build has #1132, `sudo cloudfield-server edge move
  https://edge.cloudfield.ai` still moves it, on the signed `retired` notice
  the new address sends, and takes effect at the restart; an older build
  is moved by hand
  ([edge-retire.md](../edge-retire.md#if-the-address-is-retired-before-the-server-moved)).
  With a CLI that has #1133, `cloudfield logout --edge
  https://edge.onaether.dev` deletes the local token and prints that the
  edge did not confirm the sign-out. On a server whose build has #1131,
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
the edge, both hosts routed to it by Traefik with their certificates,
and `AETHER_EDGE_RETIRED_PAIRS` for good. A backup taken between step 5
and step 17 and restored later holds the deleted tokens again, and the
start with the entry deletes them.

- Undo: n/a.
- Cost to users: none.

### 19. The old GitHub OAuth app stays

This runbook leaves in place the GitHub OAuth app whose callback is
`https://auth.onaether.dev/signin/github/callback`, its secret file, and
the `github-client-secret` entries in `compose.yaml`.

After step 17 the edge uses none of them: no setting names the app's
client id or its secret file, a retired pair needs no OAuth app, and the
callback answers `410` like every path on a retired host but `/healthz`.
What stays exposed is the secret itself: a valid client secret of that
app, in its file on the host and mounted in the container at
`/run/secrets/github-client-secret`. The undo of step 17 needs all three.

Removing them is a separate, later decision of the owner, not a step of
this runbook. Once the app or its secret is gone, serving the old pair
again needs a new one.

- Undo: nothing changed.
- Cost to users: none.

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
| 2. Back up edge.db | yes | Delete the file and its copy |
| 3. Record the fingerprint | partly | Reads change nothing; a published fingerprint is corrected, not recalled |
| 4. Check the new pair | yes | Sign the test device out, revoke it on the server, delete its config directory and the browser profile |
| 5. Mark retiring | yes, at a cost | Start the old file; recorded origins stay; an undo or a short run disarms step 17's refusal |
| 6. Report | yes | Read-only |
| 7. Metrics | yes | Read-only |
| 8. Contact owners | no | A message cannot be recalled |
| 9. Wait | yes | Change the date, which is a restart |
| 10. Listings | partly | Only in each portal |
| 11. Updater and probes | yes, until step 17 | Set the old URL back while the old pair is served |
| 12. OpenAI challenge | partly | The old token can be served again; a verification is OpenAI's |
| 13. Mail | partly | At the mail provider; mail refused in between is lost |
| 14. Website redirect | yes | Read-only |
| 15. Go or no-go | yes | The checks change nothing; a no-go is a restart and a message |
| 16. Swap | yes | Start the old file |
| 17. Retire | partly | Start the step 16 file; deleted sign-ins stay deleted; roll back only to a release with #1134; a database restore is not an undo |
| 18. Keep DNS and entry | yes | Nothing to undo |
| 19. Old OAuth app | yes | Left in place; removing it is a separate decision |
| 20. Keep the domain | no | A lapsed domain may not come back |
