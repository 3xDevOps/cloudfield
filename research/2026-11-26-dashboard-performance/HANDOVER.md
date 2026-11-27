# Handover: dashboard performance (2026-11-27)

For whoever continues this work. The ranked causes, their outcomes and what was rejected are in [README.md](README.md).

## State

- Merged: #1178 (gateway reuses control channels for reads), #1180 (Lanes reads history without rereads; `no_rereads`, `older_time`), #1189 (failed agent-mail and subsessions reads show Retry).
- In the merge queue: #1196 (Lanes budget follows the window; Load more).
- Closed without merging, reasons in README "Tried and rejected": #1193 (cold open), #1208 (keep Lanes history).
- Next, approved: two correctness fixes found on main (an event the client fails to apply is dropped while the stream stays live, `drain` in `web/src/store/sync.ts`; a lane history stuck at loading after an identity change, `readBack` in `web/src/routes/board/lanes/use-lane-data.ts`), then cause 4, compression between server and gateway (negotiated; both mixed-version directions tested; CPU cost on one CPU with a size threshold; security review: does any response mix attacker-chosen text with secrets).
- Not to build without asking: the duplicate budget read on reload (`useTeamRefresh`, `web/src/routes/team/sync.ts`), paging `run.list`, the session-view polls, and anything a real recording from the owner would change (the HAR asked for in README has not arrived).

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
