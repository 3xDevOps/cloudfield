# Handover: dashboard performance (2026-11-27)

For whoever continues this work. The ranked causes, their outcomes and what was rejected are in [README.md](README.md).

## State (2026-11-27, 20:30Z)

- Merged: #1178 (gateway reuses control channels for reads), #1180 (Lanes reads history without rereads; `no_rereads`, `older_time`), #1189 (failed agent-mail and subsessions reads show Retry), #1196 (Lanes budget follows the window; Load more), #1211 (lane history re-read after an event could not be applied), #1212 (lane history stuck at loading after a member change), #1216 (origin note without a credential).
- Closed without merging, reasons in README "Tried and rejected": #1193 (cold open), #1208 (keep Lanes history).
- Waiting on the owner:
  - #1214, compression of `workspace.timeline` pages (draft). Decision: accept the residual side channel or not. If yes: one fresh review of its last push (it changed how the timeline's reader filter caches sessions) and CI before merge; measure the gain on a real link first if a recording exists.
  - The duplicate budget read on reload (`useTeamRefresh`, `web/src/routes/team/sync.ts`): six requests saved per reload, no time anyone feels.
  - Paging `run.list` (351 KB for 600 sessions on every cold open; changes what lists show).
  - The session-view polls (`dev.terminal.list`, `run.room.status`).
  - Whether to hide a workspace origin's credential from `workspace.list`, the dashboard and `cloudfield link`, or refuse such an origin (#1216's description lists every place it is still shown).
  - Anything a real recording changes: the HAR of Lanes filling in, asked for in README, has not arrived, so the reread share, the link's round-trip time and its throughput on the owner's server are still not established.
- Known and not fixed (found by reviewers, present on main): a late `coord.messages.list` answer is applied without checking the signed-in member or a newer read (#1189's description); a reconnect replay during a repair can reset lane history (#1211's first review).

## Running the measurements

Needs Docker (the seeded server needs it to restart), `bun`, and Chromium's libraries (`cd web && bunx playwright install-deps chromium`).

```sh
make build                                   # dist/aether, dist/aether-server, with the dashboard embedded
cd web
bun ../research/2026-11-26-dashboard-performance/measure/world.ts /tmp/world.json
# stays up; in another shell, from the repository root:
bun research/2026-11-26-dashboard-performance/measure/lanes-browser.ts /tmp/world.json 24h 0
bun research/2026-11-26-dashboard-performance/measure/journeys.ts /tmp/world.json 0
bun research/2026-11-26-dashboard-performance/measure/cold-open.ts /tmp/world.json 7
bun research/2026-11-26-dashboard-performance/measure/link-latency.ts /tmp/world.json 150
```

- `PERF_LINK_DELAY_MS=75` before `world.ts` puts a relay between gateway and server that holds every chunk 75 ms each way: one round trip costs 150 ms. Every "150 ms link" number was taken this way, with the browser-side delay argument at 0.
- `PERF_CLI=<path>` and `PERF_SERVER=<path>` copy another `aether` or `aether-server` over `dist/` before starting, to compare two builds on the same seed. Build the "before" binaries from the commit you compare against and keep them outside `dist/`.
- Arguments after the state file go to the seed tool (`seed/main.go`, built with `-tags perfseed`): `--sessions 3000` makes the larger store used for memory numbers.
- Each script's header says what it measures. One CPU: run one measurement at a time; compare counts, bytes and ratios, not single wall-clock times; repeat and show the spread.
- Stop a world by killing its `bun` process, then any `aether` and `aether-server` left; do not use `pkill -f` with a pattern that matches your own shell.

## Things that cost time

- A click on the Lanes tab before the workspace has settled can be undone: wait for network idle first (the scripts do).
- Lanes on the phone layout is under Sessions, then the **Lanes** pill; use `hasTouch: true, isMobile: true` so 44px controls apply.
- The dashboard is embedded in `dist/aether`: a gateway built from another commit also serves that commit's dashboard.
- The full `bun run test` suite times out in unrelated files on a one-CPU sandbox; run the files you touched and rely on CI's `dashboard` job for the rest.
