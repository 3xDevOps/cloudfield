# Brief: static request inventory of the dashboard, and what the relay path adds

You are collecting facts for a performance investigation of the Cloudfield dashboard
(repository checkout at /workspace; dashboard source in web/src, Go server in internal/).
Read code only. Do NOT run builds, tests, browsers, servers, or any network call except
`gh pr view <n> --repo 3xDevOps/cloudfield` for the pull requests named below. Do not edit
any file except your one output file. Never make anything up: every claim names
file:line; where you cannot establish something, write "not established".

Known symptom: the Lanes view opens fast but its bars take very long to be coloured in.
Lanes reads history in web/src/routes/board/lanes/use-lane-data.ts (`readBack`: serial
1,000-event pages of `api.workspaceTimeline`, per workspace).

Collect, in this order:

1. Transport. How does the browser reach the server? Read web/src/lib/api*.ts (and what it
   imports), internal/localgw, internal/edge (especially internal/edge/proto/limits.go and
   the relay's HTTP/WebSocket framing). For each of the three paths (direct, local gateway
   `cloudfield gui`, hosted Edge relay): is an RPC one HTTP request, or a message on a
   shared socket? Which limits apply (MaxHTTPConnsPerServer = 8, body sizes, per-request
   overhead, queueing when the limit is reached, timeouts)? Is response compression applied
   anywhere (gzip/br/zstd on HTTP, permessage-deflate on WebSockets)? Is HTTP/2 used on which
   hop (see PR #1102)?
2. Request inventory per journey, from code: for each of — cold open of the dashboard
   (hydration), Board, Map, Lanes, opening a session with a long transcript, the terminal,
   the changes view, the files view, switching workspace, a reconnect after the link drops —
   list every RPC method / HTTP fetch / WebSocket opened, in order, with: what triggers it
   (file:line), whether it is awaited serially before the next or issued in parallel
   (Promise.all, independent effects), page sizes and loop bounds, whether it re-fetches
   something the store already holds, and whether it repeats on a timer or per item (N+1
   from the client: one request per session/workspace/card).
3. Lanes specifically: how many `workspace.timeline` requests does opening Lanes make as a
   function of (events in window, workspaces in scope, historyBudget in web/src/store/lanes.ts)?
   What else does Lanes fetch (agents, mail, overlaps)? What happens on window change, pan,
   workspace switch, leaving and re-entering the view (is history dropped and re-read)?
4. Prior work, so it is not redone: read the descriptions of PRs #1095, #1097, #1098, #1100,
   #1102, #1103 and summarise in at most 4 lines each: what was measured, how, the result,
   and what they explicitly left undone.

Output: write ONE markdown file,
/workspace/research/2026-11-26-dashboard-performance/evidence/network-static.md
with the four sections above, tables where it helps, file:line for every claim, and a final
section "Suspects" ranking the serial chains / N+1s / refetches you found by how many round
trips they cost on a relay link. No private data (no tokens, host names, people's names).
Your final message: 10 lines at most, the top suspects with file:line.
