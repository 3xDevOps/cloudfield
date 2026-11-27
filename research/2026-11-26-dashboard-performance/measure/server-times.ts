// Time per request through the local gateway for the calls that carry the
// most bytes, interleaved round-robin and repeated, so each row is comparable
// with the others from the same minute. Reports the median and the spread
// (min to max); read the ratios, not the milliseconds.
//
//   bun research/2026-11-26-dashboard-performance/measure/server-times.ts /tmp/perf-world.json [rounds=25]

import { readFileSync } from 'node:fs'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const rounds = Number(process.argv[3] ?? 25)

async function rpc(method: string, params: unknown) {
  const started = performance.now()
  const res = await fetch(`http://${world.addr}/api/v1/${method}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${world.token}` },
    body: JSON.stringify(params),
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`${method}: ${res.status}: ${text}`)
  return { body: JSON.parse(text), bytes: text.length, ms: performance.now() - started }
}

const busy = (await rpc('workspace.list', {})).body.workspaces.find((w: { name: string }) => w.name === 'a-busy').id
const lane = ['run.status', 'coord.message', 'coord.message.acked']
const cases: [string, string, unknown][] = [
  ['server.info (baseline: one small call)', 'server.info', {}],
  ['run.list (every session)', 'run.list', {}],
  ['workspace.timeline, lane types, 1000 newest', 'workspace.timeline', { workspace_id: busy, types: lane, newest: true, limit: 1000 }],
  ['workspace.timeline, lane types, 100 newest', 'workspace.timeline', { workspace_id: busy, types: lane, newest: true, limit: 100 }],
  ['workspace.timeline, no type filter, 100 newest (Activity)', 'workspace.timeline', { workspace_id: busy, newest: true, limit: 100 }],
  ['workspace.timeline, one session, run.diff, 50 newest (Changes)', 'workspace.timeline', { workspace_id: busy, run_id: 'run-seed00000', types: ['run.diff'], newest: true, limit: 50 }],
]
const times = cases.map(() => [] as number[])
const bytes = cases.map(() => 0)
for (let round = 0; round < rounds; round++) {
  for (const [i, [, method, params]] of cases.entries()) {
    const r = await rpc(method, params)
    times[i].push(r.ms)
    bytes[i] = r.bytes
  }
}
const median = (list: number[]) => [...list].sort((a, b) => a - b)[Math.floor(list.length / 2)]
const base = median(times[0])
console.log(`${rounds} rounds, interleaved`)
console.log('median ms   min-max ms    x baseline   bytes    call')
for (const [i, [name]] of cases.entries()) {
  const m = median(times[i])
  console.log(`${m.toFixed(1).padStart(9)}   ${`${Math.min(...times[i]).toFixed(1)}-${Math.max(...times[i]).toFixed(1)}`.padEnd(12)}  ${(m / base).toFixed(1).padStart(9)}   ${String(bytes[i]).padStart(7)}  ${name}`)
}
