// Replays the request chain Lanes makes for one workspace (readBack in
// web/src/routes/board/lanes/use-lane-data.ts): workspace.timeline, newest
// first, 1,000 events a page, until the window's floor or the 5,000-event
// budget. Prints, per page: bytes, events, rereads, server time, and how far
// back history now reaches.
//
//   bun research/2026-11-26-dashboard-performance/measure/lanes-chain.ts /tmp/perf-world.json [hours=24]

import { readFileSync } from 'node:fs'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const hours = Number(process.argv[3] ?? 24)
const types = ['run.status', 'coord.message', 'coord.message.acked']
const budget = 5000

async function rpc(method: string, params: unknown) {
  const started = performance.now()
  const res = await fetch(`http://${world.addr}/api/v1/${method}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${world.token}`, 'accept-encoding': 'gzip' },
    body: JSON.stringify(params),
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`${method}: ${res.status}: ${text}`)
  return { body: JSON.parse(text), bytes: text.length, encoding: res.headers.get('content-encoding') ?? 'none', ms: performance.now() - started }
}

const { body: list } = await rpc('workspace.list', {})
const busy = list.workspaces.find((w: { name: string }) => w.name === 'a-busy')
const now = Date.now()
const floor = now - hours * 3600_000
let before: number | undefined
let held = 0
let useful = 0
let bytes = 0
console.log(`window ${hours}h, workspace ${busy.name}`)
console.log('page  events  rereads  bytes   enc   ms    history reaches back (h)  window covered')
for (let page = 1; ; page++) {
  const r = await rpc('workspace.timeline', { workspace_id: busy.id, types, newest: true, before_seq: before, limit: 1000 })
  const events = r.body.events as { time: string; type: string; payload: { reread?: boolean } }[]
  const rereads = events.filter((e) => e.type === 'run.status' && e.payload.reread).length
  held += events.length
  useful += events.length - rereads
  bytes += r.bytes
  const oldest = events[0] ? Date.parse(events[0].time) : now
  const reach = (now - oldest) / 3600_000
  console.log(`${String(page).padStart(4)}  ${String(events.length).padStart(6)}  ${String(rereads).padStart(7)}  ${String(r.bytes).padStart(6)}  ${r.encoding.padEnd(4)}  ${r.ms.toFixed(0).padStart(4)}  ${reach.toFixed(2).padStart(10)}  ${(Math.min(1, reach / hours) * 100).toFixed(0).padStart(20)}%`)
  if (!r.body.more) { console.log('log exhausted'); break }
  if (oldest <= floor) { console.log('reached the window floor'); break }
  if (held >= budget) { console.log('stopped at the 5,000-event budget: older bars stay "History not loaded"'); break }
  before = r.body.older_seq
}
console.log(`total: ${held} events held, ${useful} of them drawn (${held - useful} rereads discarded), ${bytes} bytes`)
