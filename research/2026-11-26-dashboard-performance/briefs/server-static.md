# Brief: server-side cost behind the dashboard's requests (static analysis)

You are analysing the Go server of Cloudfield (checkout at /workspace) for a performance
investigation of its dashboard. Read code only. Do NOT run builds, tests, benchmarks,
servers or browsers (another agent owns the single CPU for measurements). Do not edit any
file except your one output file. Never make anything up: every claim names file:line; where
you cannot establish something, write "not established".

Known symptom: the Lanes view's bars take very long to be coloured in. Lanes calls the RPC
`workspace.timeline` (handler internal/sshd/timeline.go, reader internal/timeline/timeline.go
`Reader.Before`, which calls `events.EventLog.ReadBefore` with a workspace filter and
types [run.status, coord.message, coord.message.acked], limit 1000) repeatedly, backward.

Establish:

1. The full path of one `workspace.timeline` call: RPC dispatch (registerGuarded, permission
   check, resolveActor, eventScope), `LastSeq`, the SQL behind `ReadBefore`/`Read` (find the
   EventLog implementation in internal/store or internal/events), the table schema and its
   indexes (internal/store/migrate.go), whether the query with a workspace + types filter
   can use an index or scans, what `scanBudget`, `minBatch`, `scanWindow` do, payload
   decoding cost per event, `events.Scoped` / `Reachable` / `VisibleTo` per event (any store
   read per event = N+1?), and JSON encoding of the result. Say which steps are O(rows
   returned) and which are O(rows in the log).
2. The same for the RPCs the dashboard makes on a cold open and on the Board, Map, a session
   open (ACP history / transcript), changes and files views: find them from
   web/src/lib/api*.ts and web/src/store (hydration), then for each handler in internal/sshd
   (and internal/ops, internal/localgw if it serves any itself) list the store queries it
   runs, N+1 patterns (a query per run/session/workspace in a loop), work repeated per
   request that could be done once, and anything that shells out (git) or touches the
   filesystem/containers per request.
3. Locks: mutexes or single-writer SQLite connections held across slow work on these paths
   (SetMaxOpenConns, busy_timeout, WAL mode, transactions spanning I/O). What would a busy
   host (many agents writing events) do to a reader?
4. The live event stream (/ws/events or equivalent): what the server sends on connect
   (replay size, from which seq), per-subscriber filtering cost, and what a reconnect
   re-sends.
5. What measurement hooks exist: Go benchmarks (`grep -rn "func Benchmark"`), timing logs,
   metrics/pprof endpoints, slow-query logging. List them with file:line.

Output: write ONE markdown file,
/workspace/research/2026-11-26-dashboard-performance/evidence/server-static.md
with the five sections, file:line for every claim, the literal SQL where relevant, and a
final "Suspects" section ranked by expected cost on a store with weeks of events (hundreds
of thousands of rows), hundreds of sessions and several workspaces — with, for each, the
measurement that would confirm or refute it (e.g. EXPLAIN QUERY PLAN of a named query, a
count of queries per request). No private data. Your final message: 10 lines at most.
