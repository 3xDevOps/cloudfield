# Dashboard network inventory (static, from code)

Scope: read-only analysis of the checkout at `/workspace`. Nothing was run. Every claim cites
`file:line`; "not established" marks what the code does not settle. `r` below is one
end-to-end round trip between the machine running `cloudfield gui` and the server, through
the relay where there is one.

## 1. Transport

### 1.1 The three paths

| Path | Browser to gateway | Gateway to server | One RPC is | Cited at |
| --- | --- | --- | --- | --- |
| Tailnet dashboard (`cloudfield-server --web-port`) | HTTPS, HTTP/2 for requests, HTTP/1.1 for each WebSocket | none: in-process call | one HTTP/2 stream (`POST /api/v1/<method>`) | `docs/local-gateway.md:30`, `internal/servergw/backend.go:18-20`, `internal/webgate/gateway.go:166-171`, `web/src/lib/api.ts:380-394` |
| `cloudfield gui`, address link (no edge) | plain HTTP/1.1 on loopback | one lazily dialed SSH connection (TCP) | one HTTP request, then a new SSH session channel on the shared connection | `docs/local-gateway.md:30`, `internal/localgw/backend.go:21-28, 322-333`, `internal/cli/dial.go:73-118` |
| `cloudfield gui`, hosted Edge relay | plain HTTP/1.1 on loopback | the same single SSH connection, carried as a WebSocket the relay splices to the server | same as above; every channel rides one TCP/TLS stream to the relay | `internal/cli/edge.go:107-129`, `internal/edge/client/dial.go:64-95`, `internal/edge/relay/connect.go:153-154, 202`, `internal/edge/relay/splice.go:23-70` |

Every RPC is a separate `fetch` POST: `web/src/lib/api.ts:380-394` (`call`). Nothing in the
browser multiplexes RPCs on a socket. Sockets are used only for streams: `/ws/events`
(`api.ts:1164`), `/ws/attach/<run>` (`api.ts:1165`), `/ws/acp/<run>` (`api.ts:1167`),
`/ws/assistant`, `/ws/terminal`, `/ws/dev/browser/<run>`.

Cost of an RPC through the local gateway, from the code:

- `sshBackend.callOnce` takes the shared connection, calls `conn.Control()` and then
  `roundTrip` (`internal/localgw/backend.go:322-333, 289-318`). `Control()` opens a new
  `session` channel and sends a `subsystem` request that waits for a reply
  (`internal/cli/channel.go:73-93, 159-165`). x/crypto's `openChannel` and a `SendRequest` with
  `wantReply` each wait for the peer's answer (`golang.org/x/crypto@v0.57.0/ssh/mux.go:346-368`,
  `ssh/channel.go:651-676`). The call itself then writes one JSON line and reads one line
  (`internal/protocol/client.go:30-58`).
  So an RPC is 3 serial round trips (channel open, subsystem accept, request/response), plus a
  channel close that waits for nothing. On the relay link that is `3r` per RPC, before server work.
- A stream (WebSocket) costs one more: channel open, (pty-req for attach), subsystem,
  header write, ack read (`internal/cli/channel.go:73-93`, `internal/webgate/acp.go:36-52`):
  about `4r` before the first byte.
- On the server, each control channel is served by its own goroutine and loop
  (`internal/sshd/session.go:104-136`, `internal/sshd/control.go:42-57`). I found no per-member
  or per-server cap on control channels (the 10-session/16-conn caps at
  `internal/sshd/run_ssh.go:36-41` are for run SSH only). Not exhaustively established.
- The gateway holds one SSH connection per link (`internal/localgw/backend.go:25-28`), so all
  concurrent RPCs, events and terminals share one TCP stream to the relay. No per-channel
  priority exists; a 1000-event page delays every other channel behind it on that stream. SSH
  channel window is 2 MiB, max packet 32 KiB (`ssh/channel.go:22-24` in x/crypto v0.57.0).

### 1.2 Limits

| Limit | Value | Applies to | Cited at |
| --- | --- | --- | --- |
| `MaxHTTPConnsPerServer` | 8, one request per connection, 15 s wait then 503 | only `KindHTTP`: the MCP connector path `/mcp/{server_id}`. **Not the dashboard**: the dashboard never uses `KindHTTP` | `internal/edge/proto/limits.go:12-16, 32-36`; `internal/edge/relay/relay.go:329`; `internal/edge/relay/http.go:119-134`; `docs/edge.md:714-716` |
| `MaxHTTPWaitersPerServer` | 16 | same (connector only) | `internal/edge/proto/limits.go:17-20` |
| `MaxSSHConnsPerServer` | 48 SSH connections per server (all clients) | the dashboard's one SSH connection counts as 1 | `internal/edge/proto/limits.go:10-12` |
| `MaxConnsPerDevice` | 16 | per device | `internal/edge/proto/limits.go:25` |
| Egress budget throttle | once the operator's monthly byte budget is spent, all splices together get 256 KiB/s, read in 4 KiB chunks | every splice, dashboard included; default budget is 0 (none) | `internal/edge/relay/relay.go:102-104, 123-129`; `internal/edge/relay/splice.go:45-56, 93-106`; `docs/edge.md:987`. The hosted edge's setting: not established |
| Splice copy | 32 KiB per read, one WebSocket binary message per write | every relayed byte | `internal/edge/relay/splice.go:13-17, 45-69`; `websocket.NetConn` read limit off (`coder/websocket@v1.8.14/netconn.go:49`) |
| Browser connections per host | 6 for HTTP/1.1: this is the loopback gateway's case, per the code's own comments; a browser rule, not set in the repo | `cloudfield gui` | `web/src/routes/team/sync.ts:17-18`; PR #1102 text. Whether WebSockets count against the six: not established |
| Request body | 1 MiB default; 12 MiB terminal image; 96 MiB import/upload/terminal file; ~385 MiB editor write; 6 MiB dictation | `POST /api/v1/<method>` | `internal/webgate/api.go:19-48` |
| Response line | 96 MiB per NDJSON line; ~385 MiB for patch, file and config reads | each control call | `internal/protocol/protocol.go:31-33`; `internal/protocol/client.go:80-93` |
| `workspace.timeline` page | default 100, max 1000 events | `workspace.timeline` | `internal/timeline/timeline.go:17-21, 194-203` |
| Call timeout | 60 s (15 min for transfers); the watchdog closes only that channel | each gateway call | `internal/localgw/backend.go:258-284, 289-318` |
| Dial timeout | 90 s; concurrent callers share one dial | SSH dial, edge included | `internal/localgw/backend.go:50-53, 127-160` |
| Dead-link detection | keepalive after 30 s of silence, drop after 5 min (10 intervals); through the edge a dead network takes up to 6 minutes; the edge marks a server offline after 60 s without control traffic | gateway SSH connection | `internal/localgw/backend.go:55-70, 192-214`; `docs/local-gateway.md:225-245`; `internal/edge/proto/limits.go:54-55` |
| Gateway HTTP server | `ReadHeaderTimeout` 10 s only; no read, write or idle timeout | every gateway | `internal/webgate/gateway.go:36-38, 133` |
| Browser call timeout | none in `call` unless the caller passes a signal | RPCs | `web/src/lib/api.ts:380-394` |

Queueing when a limit is reached: the 8-connection limit queues connector requests in the
relay (`internal/edge/relay/http.go:357-412`). It does not touch the dashboard. For the
dashboard the queues are the browser's 6-connection pool on the loopback gateway, and the
single SSH/TCP stream behind it.

### 1.3 Compression and HTTP/2

| Layer | Compressed? | Cited at |
| --- | --- | --- |
| Static dashboard files (`index.html`, chunks) | gzip, only when the request is **not** from loopback; so never on `cloudfield gui`, yes on the tailnet dashboard | `internal/webgate/static.go:83-95` |
| API JSON responses (`writeResult`, `handleCapabilities`) | no `Content-Encoding` anywhere | `internal/webgate/api.go:261-270`, `internal/webgate/api.go:238-259` |
| Browser to gateway WebSockets | permessage-deflate off: `websocket.Accept(w, r, nil)`; the library's zero mode is `CompressionDisabled` | `internal/webgate/socket.go:64`; `coder/websocket@v1.8.14/compress.go:20-23` |
| Edge WebSockets (client dial and relay accept) | off, deliberately (CRIME) | `internal/edge/client/dial.go:64-67` (zero options); `internal/edge/relay/relay.go:334-336` |
| SSH | `none` is the only algorithm x/crypto offers | `golang.org/x/crypto@v0.57.0/ssh/common.go:319` |
| Relay forwarding of HTTP (connector only) | `Accept-Encoding` stripped | `internal/edge/relay/http.go:42-52` |

So a `workspace.timeline` page crosses every hop as uncompressed JSON text, and base64/escape
expansion applies to some methods. Typical page byte size: not established.

HTTP/2 (PR #1102): only the tailnet dashboard (`servergw.Start` sets the TLS config; net/http
offers `h2`), `internal/webgate/gateway.go:166-179`, `internal/servergw/listen.go:54`;
`docs/local-gateway.md:30`. WebSockets stay on their own HTTP/1.1 connection. The loopback
gateway is plain HTTP/1.1, so it is not helped, and the Edge path is the loopback gateway.
Between gateway and server nothing is HTTP: it is SSH, with 3 serial round trips per RPC (1.1).

## 2. Request inventory per journey

Legend: RPC = `POST /api/v1/<method>`; local = `POST /local/v1/<verb>` served by the gateway
itself (no relay cost); WS = a WebSocket; serial = `await`ed before the next; parallel =
issued together. `r` = one relay round trip; RPC `3r`, WS open `4r`.

### 2.1 Cold open of the dashboard (hydration)

On the Edge path there is no saved snapshot to paint: `saveSnapshot` clears it whenever the
link has an `edge_url` (`web/src/lib/snapshot-cache.ts:52-55`), so `paintSnapshot`
(`snapshot-cache.ts:91`) has nothing on the next open. On other paths a snapshot paints first.

| # | Request | Trigger | Order | Notes |
| --- | --- | --- | --- | --- |
| 0 | static: `index.html` + first-page chunks (14 files in PR #1103's table), then 4 late chunks after hydration | page load; `preloadChunks()` at `web/src/App.tsx:95-97` | parallel | loopback, no gzip (`static.go:83-95`), hashed assets immutable (`static.go:78-82`); never crosses the relay |
| 1 | `GET /api/v1/capabilities` | `connect()` starts `probeGateway`: `web/src/store/sync.ts:1036, 837` | serial, first | gateway calls `server.info` to fill it (`internal/webgate/api.go:247`): `3r`; also triggers the SSH dial if cold |
| 2 | local `link.status` | `sync.ts:848` | serial after 1 | local only; reads config, no server call (`internal/localgw/local.go:250-265`) |
| 3 | WS `/ws/events` | `startStream` `sync.ts:953`, `web/src/lib/stream.ts:127-150` | serial after 2 | `4r`; `ok` ack means live |
| 4 | `server.info`, `workspace.list`, `member.list`, `run.list` (all runs, no `active_only`), `run.overlaps`, and `GET /capabilities` again (a second `server.info` inside) | `hydrate` on `live`: `sync.ts:182-202`, `api.ts:536, 896, 1073` | parallel, 6 requests in 7 slots (`member.list` once); equals the loopback browser cap of 6 | `3r` wave. Begins only after 3 |
| 5 | local `workspace.selection` | `sync.ts:206-220` (first load only) | serial after 4 | local only |
| 6 | local `link.status` again | `sync.ts:235-244` | serial after 5 | local only; already read at step 2 |
| 7 | `member.device.list`, `watch.list`, `member.merge.pending`, `server.edge` | `sync.ts:263-268`; each gated on a capability (`store/devices.ts:22-23`, `store/watches.ts:73`, `store/edge.ts:32`) | parallel (one `Promise.all` since #1100) | `3r` wave. `hydrated` is set only after this (`sync.ts:272`) |
| 8 | `linear.status` | `sync.ts:270` | background (`void`) | |
| 9 | local `update.check` (the gateway then calls `server.info` live, `internal/localgw/update.go:121, 157-166`, plus `server.update_status` when the rule holds, `update.go:140-155`) | `UpdateChecks` mounts with `App`: `web/src/App.tsx:139`, `web/src/components/update-checks.tsx:52-82`, `store/updates.ts:116` | parallel to hydration | |
| 10 | `server.update_status` | once `info` and `live`: `update-checks.tsx:91-108`, `updates.ts:154` | after `live`; repeats on every reconnect | |
| 11 | `assistant.status` | `web/src/components/assistant/panel.tsx:413-415` (`assistantStatus === null`) | after identity known | the assistant WS opens too if the assistant is working (`panel.tsx:416`) |
| 12 | `server.sysbox.status` | `SysboxNudge` in the Board's list/columns layouts, super admin only: `web/src/routes/board/index.tsx:175`, `web/src/lib/use-sysbox.ts:23-47` | on every mount of that layout; not cached | not on Map or Lanes (`board/index.tsx:90-103` returns first) |

Serial chain on the Edge path before `hydrated`: 1 (3r + dial) then 3 (4r) then 4 (3r) then 7 (3r)
= at least `13r` plus the SSH dial, with three local stages in between. `server.info` runs on the
server 4 times per cold open (hydrate, two `capabilities`, `update.check`); `link.status` is read
twice and `capabilities` twice.

Repeating work after open: `push.active` at most every 20 s while the pointer or keyboard is
active (`web/src/lib/push.ts:125-126, 167-171`); `update.check`/`server.update_status` every
30 min, ±10 % (`update-checks.tsx:18, 29-31, 73-77`) and on focus at most every 60 s
(`update-checks.tsx:20, 68-70`); `member.merge.pending` every 5 s while a merge code or proposal
is open (`sync.ts:1029-1033`); `workspace.budget` read 1.5 s after each `run.cost` event, per
workspace, coalesced (`web/src/store/cost.ts:68-91`, `sync.ts:660-663`).

### 2.2 Board (list and columns), and Map

Neither fetches a thing of its own beyond item 12 above (Board only). Cards, columns, the map
and its overlap chips derive from the store that hydration filled and events keep current
(`web/src/routes/board/selectors.ts:102-127`; `routes/board/board-map.tsx`, `run-map.tsx` hold no
`api` call; `rg` of `routes/board/*.tsx` finds only action handlers). Opening Board or Map is
zero RPCs. Switching tab remounts the pane (`TabsContent key={tab}`, `board/index.tsx:165-167`).

### 2.3 Lanes

See section 3. It fetches only `workspace.timeline`, plus `run.acp.history` for a sub-lane the
user opens (`web/src/routes/board/lanes/use-lane-data.ts:103-114`), and
`coord.messages.list` when a mail mark's popover opens (`lane-popover.tsx:16`).

### 2.4 Opening a session with a long transcript

Mounted once per distinct run in the tile area (`web/src/components/tiles/tile-area.tsx:142`).
Enhanced (ACP) session:

| Request | Trigger | Order | Notes |
| --- | --- | --- | --- |
| WS `/ws/acp/<run>` | `subscribeSession`: `web/src/routes/run/agent-terminal.ts:119`, `web/src/lib/acp-stream.ts:77-95` | parallel | `4r`; replays at most the newest 200 items (`internal/acphost/session.go:550-556`); the view paints ack and replay together (`acp-stream.ts:104-115`) |
| `run.room.list` (limit 100) | `useRunRoom`: `web/src/routes/run/room.ts:59-90`, `session-host.tsx:30` | parallel | |
| `run.room.status` | `room.ts:133-155` | immediately, then every 10 s while the session is mounted | per mounted session |
| `coord.messages.list` | `useMessageList`: `session-view.tsx:398`, `store/messages.ts:126-168` | parallel | list persists after the viewer leaves (`messages.ts:147-149`) |
| `workspace.timeline` (`run.diff`, limit 50, newest) | `ChangesBar`: `session-view.tsx:550`, `routes/run/changes-bar.tsx:23-29`, `store/diff.ts:293-318` | parallel; loops serially while the page holds only repo diffs | when the store has no snapshot yet |
| `agent.list` | `useAgentList`: `session-host.tsx:26`, `routes/agents/use-agents.ts:21-24` | once per identity, cached in the store | |
| `dev.terminal.list` | `useRunShells`: `session-host.tsx:29`, `routes/run/shells.tsx:60-84` | immediately, then every 2 s with the Terminal view shown, every 10 s otherwise | polls for every mounted session, visible tab only |
| `run.acp.history` (limit 1000), older pages | `useLiveLaunches` while a live subagent's launch is unloaded, at most 5 serial pages (`routes/run/subagent-cards.tsx:37-51`, `session-view.tsx:281`); then on scroll while within 3 viewports of the top (`session-view.tsx:61, 408-420`); the Trace view adds up to 5 more auto pages (`routes/run/trace-data.ts:18, 70-76`) | serial per run | `store/session-stream.ts:26, 205-221` |
| `workspace.timeline` (`run.status`, `run.cost`, 500 per page, cap 2000) | Trace view only: `trace-data.ts:65`, `store/trace-log.ts:7-10, 78` | serial pages | |

TUI (non-Enhanced) session instead: WS `/ws/attach/<run>` (`routes/terminal/session.ts:354`,
`internal/webgate/attach.go:23-85`) and `readSessionLog` = `workspace.timeline` for the run,
types `run.agent`, `workspace.timeline`, `run.status`, `run.review`, 1000 per page, serially
until 2000 events are read (`session-view.tsx:395-397`, `store/sessions.ts:32-35, 337-368`).
It runs again on each `live` flip, paging forward from its cursor until `more` is false.
Terminal scrollback beyond the replay uses `terminal.history` pages of 200
(`routes/terminal/history-cache.ts:66, 503-540`), only when the history view is used.

Rows and chat paint from the replay; there is no per-item N+1 on open. The N+1-shaped fan-out is
per mounted session: 2 pollers (`run.room.status`, `dev.terminal.list`) each.

### 2.5 The terminal

One WS `/ws/attach/<run>` (agent TUI) or `/ws/attach/<run>?shell=<tab>` (a dev shell) or
`/ws/terminal` (the environment terminal): `api.ts:1161-1170`. In the gateway each is one SSH
channel with a `pty-req` and the `attach` subsystem (`internal/cli/channel.go:73-93, 181-193`),
so about `4r` before the replay (a finished session's screen replay is capped at 1 MiB,
`internal/sshd/attach.go:989`; the live replay size: not established). Reconnect sends a resume
position when it has one, so an ordinary drop avoids a full replay
(`routes/terminal/attach.ts:925-940`). History is IndexedDB first; the first `terminal.history`
RPC happens on scroll into history, not on open. Resizes, input and shell control calls are
RPCs (`routes/run/shell-terminal.tsx:166-178`).

### 2.6 Changes view

| Request | Trigger | Order | Notes |
| --- | --- | --- | --- |
| `GET /api/v1/run/<run>/patch` (the whole patch) | `usePatch` effect: `web/src/routes/diff/index.tsx:289-297`, `loadPatch` 261-282; `api.ts:1046` | on open | not paged |
| `workspace.timeline` (`run.diff`, limit 50, newest) | `diff/index.tsx:126-129`, `store/diff.ts:334-353` | parallel to the patch | older pages on strip click (`routes/diff/strip.tsx:259-270`) |
| the patch again | on each `run.diff` event the store counts a new revision (`sync.ts:649-654`, `store/diff.ts:242-260`); the effect refetches unless the stored patch is "large" (`diff/index.tsx:289-298`) | each snapshot | refetches content the store already holds, uncompressed |
| `run.git.status`, `run.git.diff` ×2, `files.read` per changed file, `workspace.mirror.status` | publish dialog only: `routes/diff/publish-state.ts:116-153`, `publish-pr.ts:50-52` | on dialog open | one `files.read` per changed path (N+1 on the dialog) |

### 2.7 Files view

| Request | Trigger | Order | Notes |
| --- | --- | --- | --- |
| `files.tree` for each workspace's base root, plus `home.tree` | every `TreeDirectory` opens its root: `routes/files/tree.tsx:77-84, 223-243`, `useListing` 138-165 | parallel, one per workspace (N+1 by workspace count) | session checkouts start closed (`tree.tsx:96`) and read on expand |
| `files.tree` per expanded directory | `useListing` effect `tree.tsx:150-170` | on expand | |
| `files.read` | `routes/files/editor.tsx:77-115` | on select | |
| `files.diff` | `routes/files/document.tsx:224-239` | on opening diff mode | |
| every expanded directory and the open document of a run again | each `run.diff` event calls `invalidateRun`, dropping that run's trees, documents and diffs and bumping `filesEpoch` (`store/diff.ts:242-243`, `store/files.ts:342-348`); `useListing`/editor effects refetch | per snapshot event | re-fetches what the store held |
| up to 2 `files.tree` per terminal path that looks like a file | terminal link detection: `routes/files/open.ts:39-84` | per hovered line | cached after the first |

### 2.8 Switching workspace

`setActiveWorkspace` only changes store state (`web/src/store/ui.ts:347-353`). The one request
is the gateway-local `workspace.selection` write, serialized through `selectionWrite`
(`sync.ts:877-891`); no relay traffic. Board, Map and Lanes re-filter the held store. In Lanes,
new workspaces in scope get a `readBack`; history already held is kept (section 3).

### 2.9 Reconnect after the link drops

1. The SSH connection ends or is declared silent (30 s ping, 5 min drop;
   `internal/localgw/backend.go:55-70, 192-214`). `/ws/events` ends with 1012 and the browser
   retries at `backoff(0)` = 250-500 ms (`web/src/lib/stream.ts:36-40, 191-205`).
2. The next call or stream redials once: through the edge that is a WebSocket to the relay, an
   `open` to the server, a data socket, and an SSH handshake (`internal/cli/edge.go:107-129`,
   `internal/edge/relay/connect.go:153-154`, `AttachDeadline` 10 s `limits.go:29-31`); callers
   share one dial, up to 90 s (`backend.go:127-160`). The exact round-trip count of the handshake:
   not established.
3. `/ws/events` resubscribes with `after_seq` and replays; a local-gateway client does **not**
   rehydrate (`sync.ts:979`: `stale` is false when a cursor exists and the gateway is `local`).
   The tailnet gateway does rehydrate on every reconnect (`capabilities.gateway !== 'local'`):
   the full wave of 2.1 items 4 and 7.
4. Each open stream reconnects by itself: every `/ws/acp`, `/ws/attach` and assistant socket
   opens a new WS and SSH channel (`acp-stream.ts:186-194`, `attach.ts:925-940`), in parallel,
   `4r` each. `online` drops and reopens all sockets even if they look healthy
   (`stream.ts:217-237`).
5. Reads keyed on `live` repeat: `server.update_status` (`update-checks.tsx:91-108`);
   `run.room.list` re-walk per mounted session until it overlaps the cache (`room.ts:91-112`);
   `readSessionLog` forward paging for each TUI session (`session-view.tsx:395-397`); the changes
   history (`changes-bar.tsx:23-29`, `diff/index.tsx:126-129`).
6. Replayed events apply one at a time; any that fetch hold the queue (see 2.10).

### 2.10 Always on: the event queue is serial and awaits RPCs

Events apply in order through one chain (`sync.ts:898-911`); each event may `await` an RPC
before the next is applied. On the relay each costs `3r` of head-of-line delay for everything
behind it, including live `run.status` events that Lanes and the Board show:

- `run.mode`, `run.wait.changed`, `run.children` always call `run.get` (`sync.ts:609-623`).
- `run.status`, `run.retention`, `run.input`, `run.title`, `run.model`, `run.effort`,
  `run.protected`, `run.archived`, `git.branch`, `workspace.timeline`: `run.get` only when the run
  is unknown (e.g. `sync.ts:488-503`).
- `coord.message`: re-reads the newest page of every held message list the message belongs to
  (`sync.ts:776-783`).
- `workspace.room_message`: `run.get` for a question or reply, then a page walk of the room
  until it overlaps the cache (`sync.ts:732-770`, `reconcileRoomHistory` 319-356).
- `workspace.evidence_packet`: page walk of loaded evidence lists (`sync.ts:794-806`).
- an event for an unknown workspace, or from an unknown actor: `workspace.list` / `member.list`
  first (`sync.ts:421-440`).

## 3. Lanes specifically

Data path: `BoardLanes` calls `useLaneHistory(workspaces, floor)` with `workspaces` = the distinct
workspace ids among the displayed cards (all cards in "All workspaces" scope, including
finished ones; archived ones when shown) and `floor` = the window start rounded down to a minute
(`web/src/routes/board/lanes/board-lanes.tsx:128-134`, `use-lane-data.ts:66-73`). One effect
starts `readBack` for **every** workspace at once (`use-lane-data.ts:71-73`); each runs its own
serial loop (`use-lane-data.ts:20-63`). Parameters: `types = ['run.status', 'coord.message',
'coord.message.acked']` (`web/src/store/lanes.ts:12`), `newest: true`, `limit: 1000`
(`use-lane-data.ts:12, 39-45`), `historyBudget = 5000` events per workspace (`lanes.ts:15`).

Requests per workspace `i`: each page is the next newest 1000 matching events. The loop stops
when the oldest event read is at or before the window start (`use-lane-data.ts:34`), when the
log is exhausted (`more` false sets floor to -Infinity, line 53), or when 5000 events are held
(`use-lane-data.ts:35-37`; the check runs before each fetch). With `E_i` matching events inside
the window:

`requests_i = min( floor(E_i / 1000) + 1, 5 )`, or fewer if the whole log holds fewer.

| Events in window `E_i` | Pages | Chain at `3r` per page |
| --- | --- | --- |
| 0-999 | 1 | `3r` |
| 1000-1999 | 2 | `6r` |
| 2000-2999 | 3 | `9r` |
| 3000-3999 | 4 | `12r` |
| 4000 and more | 5, then `capped`: bars before the oldest held event stay "History not loaded" | `15r` |

Total requests = the sum over the `W` workspaces in scope. The chains run in parallel, so the
loopback gateway's 6-connection browser pool (`team/sync.ts:17-18`) turns `W` chains into
`ceil(requests / 6)` waves once `W` > 6, and every page crosses the one SSH/TCP stream to the
relay uncompressed (1.3). A bar is "unloaded" until its workspace's `floor` passes its start
(`web/src/routes/board/lanes/segments.ts:66-97`), so colouring finishes only when the last page
of the last workspace has landed. Typical `E_i` and `W`: not established.

Events in the window are inflated by rereads: `run.status` is requested by type, and a same-status
reread event is stored for every changed wait read
(`internal/scheduler/state.go:360-362`, `internal/events/event.go:146-148`). The server drops
rereads only when `Types` is empty or `NoRereads` is set (`internal/timeline/timeline.go:231`);
`workspace.timeline` has no param for it (`internal/protocol/timeline.go:18-27`,
`internal/sshd/timeline.go:43-50` never sets it), while Lanes discards them on arrival
(`segments.ts:34`, `web/src/lib/events.ts:59`). The changes API sets `NoRereads`
(`internal/sshd/changes.go:150`); Lanes cannot. `coord.message.acked` is also fetched and not
drawn by the bar code (it is requested at `lanes.ts:12`).

What else Lanes fetches: nothing for agents, mail or overlaps on open. Mail comes from the same
history events (`board-lanes.tsx` `mailOf(history.events)`); overlaps come from the store
(`run.overlaps` in hydration, `sync.ts:189-191`, kept by events). On demand: `run.acp.history`
(limit 1000) per opened sub-lane (`use-lane-data.ts:103-114`), `coord.messages.list` per opened
mail popover (`lane-popover.tsx:16`).

Window change, pan, workspace switch:

- Wider window or pan left: `floor` drops; `readBack` records the new floor in `wanted`; a
  read in flight follows it (`use-lane-data.ts:21-23, 34`), an idle one continues from `olderSeq`.
  Held pages are not re-read.
- Narrower window, pan right, and the per-minute `now` tick: `current.floor <= floor`, so
  nothing is fetched (`use-lane-data.ts:23`).
- Scope switch (workspace or all, or showing archived): `workspaces` changes, only workspaces
  with no held history get a `readBack`; held history stays.
- Live events (`/ws/events`) append without fetching (`sync.ts:811-812`,
  `lanes.ts:83-97`); past 5000 held events the oldest are dropped and `floor` moves up
  (`lanes.ts:90-93`).

Leaving and re-entering: the last Lanes view's unmount sets `laneHistory = {}`
(`lanes.ts:79-81`; the hold is taken in `use-lane-data.ts:67`). Switching Board tabs remounts the
pane (`board/index.tsx:165-167`), so Lanes, Map, Lanes re-reads everything from page 1. History
is also dropped on an identity change (`sync.ts:230`), a server restart that rewinds the log
(`store/server.ts:293-300`) and a reset. There is no cache across visits.

Client work per page (not network): `setLaneHistory` runs `merged()`, which builds a `Set` of
held seqs and re-sorts all held events (`lanes.ts:64-67, 99-106`), and the model rebuilds from
`statusChanges(history.events)` over every workspace's events on every page
(`board-lanes.tsx:138`). That is `O(held events)` per page, for up to `W x 5000` events.

## 4. Prior work (PRs #1095, #1097, #1098, #1100, #1102, #1103; all merged 2026-11-24)

- **#1095** (VM page cache trim). Measured on real VMs, `make test-kvm`: 1280 MiB read once in a
  4 GiB guest returned 16% to the host after 3 min without the trim, 75% after 76 s with it; a
  third of a filled guest returned in 20 s vs 3 min 55 s. Left: no build wall-time comparison,
  idle program memory unchanged. Not dashboard or network work.
- **#1097** (streaming code block). Measured one render of an open `ts` block in Node, no DOM:
  7.8 ms at 100 lines, 23.8 ms at 300, 73.7 ms at 1000, two thirds highlighting; fix renders the
  last block plain until complete, checked by highlighter call-count tests and dark Chromium
  screenshots at 1440 and 390 (not retaken after review). Left: `useDeferredValue` (no profile),
  selection cleared per chunk, a stale subagent streaming flag.
- **#1098** (background re-renders). Found by audit; verified with render-count tests, not a
  browser: 2 to 1 sidebar row renders, 1 to 0 app commits per editor keystroke, 50 to 1
  `localStorage` writes per 50 store writes. Left: the sidebar's roving-focus effect and the
  uncapped Finished group; no real-browser render cost, no screenshots.
- **#1100** (first load). Playwright Chromium, dark, 1440 and 390, scripts delayed 1.5 s and the app
  chunk held 3.5 s: placeholder mark is the same DOM node from 100 ms to 5.5 s. Fonts made
  hashed and immutable; the four optional hydration reads moved into one `Promise.all` (unit test).
  Left: time to first paint on a real slow link, no test pins `data-app`, no font subsetting. It
  left the serial `workspace.selection`/`link.status` awaits and the duplicated reads
  (`sync.ts:206-244`) as they were.
- **#1102** (HTTP/2). Integration test `TestIntegrationServerGatewayHTTPSConfigImport` asserts HTTP/2,
  no extended CONNECT, a working `/ws/events`, and the `GODEBUG=http2server=0` fallback; it states
  the motivation as a seven-call startup burst on six HTTP/1.1 connections. No latency was measured.
  Left: a real browser, `/ws/attach` over TLS, anything on the loopback gateway or the Edge path.
- **#1103** (bundle split). `bun run build` sizes on `origin/main` vs head, gzip -6: first-page app
  script 3,780,415 to 3,308,362 raw (1,139,398 to 997,618 gzip); recharts and the editor load
  after first hydration; jsdom render 304-332 ms to 3-72 ms with the code preloaded. Left:
  CodeMirror language packs (805,881 raw) stay on the first page, no browser trace or real-browser
  timing, xterm and `marked` untouched.

None of the six touched the Lanes read, the event queue, the relay path or any RPC count other
than #1100's parallelization of four optional reads.

## Suspects, ranked by relay round trips

`r` is one client-to-server round trip through the relay; an RPC is `3r`, a stream open `4r`.

1. **Lanes `readBack`**: `use-lane-data.ts:31-56`. Up to `min(floor(E/1000)+1, 5)` serial pages per
   workspace, each `3r` plus a 1000-event uncompressed JSON page, `W` workspaces racing for 6
   browser connections and one SSH/TCP stream; everything is dropped and re-read on every re-entry
   (`lanes.ts:79-81`). The fetch includes rereads the client throws away (`timeline.go:231`,
   `segments.ts:34`) and acks it never draws. Worst case `15r` per workspace, `ceil(5W/6)` waves.
2. **Cold open on Edge**: `sync.ts:182-272`, `1036`. At least `13r` plus the SSH dial in four serial
   relay stages before `hydrated`; no saved snapshot on Edge (`snapshot-cache.ts:52-55`); `server.info`
   runs 4 times and `capabilities`/`link.status` twice (`sync.ts:196, 237, 837, 848`; `update.go:121`).
   Two of the four stages could be parallel (`workspace.selection`, second `link.status` are local;
   `server.edge` etc. wait on the first wave).
3. **Serial event queue**: `sync.ts:898-911, 609-623, 732-783`. Every awaited fetch costs `3r` of
   delay to all later events, including the live `run.status` Lanes and Board draw; a replay after
   reconnect multiplies it by the number of such events.
4. **Per-session pollers**: `dev.terminal.list` every 2 s or 10 s (`shells.tsx:21, 60-84`) and
   `run.room.status` every 10 s (`room.ts:11, 133-155`), for every mounted session. Each poll is
   `3r` on the shared stream and a connection of the browser's 6; at 2 s it is 1.5 RPC/s per session.
5. **Opening a session**: `session-host.tsx:25-30`, `session-view.tsx:281, 396-398, 550`. WS `4r`
   plus six RPCs in parallel (`3r` wave), then up to 5 serial `run.acp.history` pages
   (`subagent-cards.tsx:37-51`) and, for TUI, 2 serial 1000-event timeline pages
   (`store/sessions.ts:347-354`) re-run on every reconnect.
6. **Refetch on `run.diff`**: `diff/index.tsx:289-298` refetches the whole patch per snapshot;
   `store/diff.ts:242-243` / `files.ts:342-348` drop and refetch every expanded directory and the
   open document of that run per snapshot. Bytes rather than round trips: unpaged, uncompressed.
7. **Reconnect**: one dial (WS to relay, `open`, data socket, SSH handshake; count not established)
   plus `4r` per open stream in parallel, then the `live`-keyed rereads of 2.9; dead-link
   detection itself takes up to 5 minutes (6 through the edge), `docs/local-gateway.md:243-245`.

Not suspects: workspace switching (local write only, 2.8), Board and Map (no requests, 2.2), the
8-connection edge limit (connector path only, 1.2), static assets (loopback, never relayed).

## Not established

- Real values of `W`, `E_i`, the byte size of a timeline page, and `r` on the affected link.
- The exact round-trip count of the edge dial and SSH handshake.
- Server-side time per `workspace.timeline` page (the handler re-decodes and re-encodes every
  payload, `internal/sshd/timeline.go:60-81`, `internal/events/sqlitelog.go:156-194`; cost not measured).
- Whether the browser counts WebSockets against the loopback gateway's 6-connection pool.
- The hosted edge's egress budget setting (`relay.go:102-104`).
- Whether any other always-mounted component issues requests that these greps missed;
  `rg 'api\.|client\.'` over `web/src/components`, `routes/board`, `routes/run`, `routes/files`,
  `routes/diff` and `store/` was the method.
