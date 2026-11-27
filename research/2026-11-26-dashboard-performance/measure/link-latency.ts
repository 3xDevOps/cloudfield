// What one call and one Lanes history chain cost on the world's link. Run it
// against a world started with PERF_LINK_DELAY_MS so a round trip to the
// server has a known price; the results are then multiples of that price.
//
//   bun research/2026-11-26-dashboard-performance/measure/link-latency.ts /tmp/perf-world.json <round trip ms>

import { readFileSync } from 'node:fs'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const trip = Number(process.argv[3] ?? 150)

async function rpc(method: string, params: unknown) {
  const started = performance.now()
  const res = await fetch(`http://${world.addr}/api/v1/${method}`, {
    method: 'POST',
    headers: { 'content-type': 'application/json', authorization: `Bearer ${world.token}` },
    body: JSON.stringify(params),
  })
  const text = await res.text()
  if (!res.ok) throw new Error(`${method}: ${res.status}: ${text}`)
  return { body: JSON.parse(text), ms: performance.now() - started }
}
const median = (list: number[]) => [...list].sort((a, b) => a - b)[Math.floor(list.length / 2)]
const line = (name: string, ms: number[]) =>
  console.log(`${name}: median ${median(ms).toFixed(0)} ms = ${(median(ms) / trip).toFixed(1)} round trips (min ${Math.min(...ms).toFixed(0)}, max ${Math.max(...ms).toFixed(0)}, n=${ms.length})`)

await rpc('server.info', {})
const one: number[] = []
for (let i = 0; i < 15; i++) one.push((await rpc('server.info', {})).ms)
line('one small call (server.info), one after another', one)

const burst: number[] = []
for (let i = 0; i < 5; i++) {
  const started = performance.now()
  await Promise.all(Array.from({ length: 6 }, () => rpc('server.info', {})))
  burst.push(performance.now() - started)
}
line('six calls at once', burst)

const busy = (await rpc('workspace.list', {})).body.workspaces.find((w: { name: string }) => w.name === 'a-busy').id
const chain: number[] = []
for (let i = 0; i < 3; i++) {
  const started = performance.now()
  let before: number | undefined
  for (let page = 0; page < 5; page++) {
    const r = await rpc('workspace.timeline', { workspace_id: busy, types: ['run.status', 'coord.message', 'coord.message.acked'], newest: true, before_seq: before, limit: 1000 })
    before = r.body.older_seq
  }
  chain.push(performance.now() - started)
}
line('Lanes history chain, 5 pages of 1,000 events in series', chain)
