// Counts what each repeated journey asks of the server, in headless Chromium
// against the seeded world, with a fixed delay in front of every /api/v1
// request. For each journey: requests by method, bytes, the number of
// "waves" (groups of requests that started only after an earlier one ended:
// the serial depth a slow link multiplies), and the time until it was quiet.
//
//   bun research/2026-11-26-dashboard-performance/measure/journeys.ts /tmp/perf-world.json <delay ms>

import { readFileSync } from 'node:fs'
import { chromium, type Page } from '../../../web/node_modules/@playwright/test'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const delay = Number(process.argv[3] ?? 0)

const browser = await chromium.launch()
const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } })
const page = await context.newPage()

interface Call { method: string; start: number; end: number; bytes: number }
let calls: Call[] = []
let sockets: string[] = []
page.on('websocket', (ws) => sockets.push(new URL(ws.url()).pathname))
await page.route('**/api/v1/**', async (route) => {
  const call: Call = { method: new URL(route.request().url()).pathname.split('/').pop()!, start: Date.now(), end: 0, bytes: 0 }
  calls.push(call)
  if (delay) await new Promise((r) => setTimeout(r, delay))
  try {
    const response = await route.fetch()
    const body = await response.body()
    call.bytes = body.length
    call.end = Date.now()
    await route.fulfill({ response, body })
  } catch {
    call.end = Date.now()
  }
})

async function quiet(page: Page) {
  // Quiet: nothing in flight and nothing new for 700 ms past the delay.
  for (let idle = 0; idle < 7; ) {
    await page.waitForTimeout(100 + delay / 7)
    idle = calls.every((c) => c.end) ? idle + 1 : 0
  }
}

async function journey(name: string, act: () => Promise<void>, ready?: () => Promise<void>) {
  calls = []
  sockets = []
  const started = Date.now()
  await act()
  let readyAt = 0
  if (ready) { await ready(); readyAt = Date.now() - started }
  await quiet(page)
  const done = calls.filter((c) => c.end)
  // A wave starts when a request begins after some earlier request has ended.
  let waves = 0
  let frontier = -Infinity
  for (const c of [...done].sort((a, b) => a.start - b.start)) {
    if (c.start >= frontier) { waves++; frontier = Math.min(...done.filter((d) => d.start >= c.start).map((d) => d.end)) }
  }
  const by = new Map<string, { n: number; bytes: number }>()
  for (const c of done) { const e = by.get(c.method) ?? { n: 0, bytes: 0 }; e.n++; e.bytes += c.bytes; by.set(c.method, e) }
  const last = Math.max(started, ...done.map((c) => c.end)) - started
  console.log(`\n## ${name}`)
  console.log(`requests ${done.length}, bytes ${done.reduce((s, c) => s + c.bytes, 0)}, waves ${waves}, sockets opened ${sockets.length ? sockets.map((s) => s.replace(/run-seed\d+/, '<run>')).join(' ') : 'none'}`)
  console.log(`${ready ? `shown after ${readyAt} ms, ` : ''}last response at ${last} ms (delay ${delay} ms per request)`)
  console.log([...by].sort((a, b) => b[1].n - a[1].n).map(([m, v]) => `${m}×${v.n} (${v.bytes} B)`).join(', '))
}

const lanesTab = page.getByRole('tab', { name: 'Lanes' })
await journey('cold open (Board)', () => page.goto(world.url).then(() => {}), () => page.getByText(/^Seeded session \d+$/).first().waitFor())
await journey('Board to Map', () => page.getByRole('tab', { name: 'Map' }).click())
await journey('Map to List', () => page.getByRole('tab', { name: 'List' }).click())
await journey('List to Lanes (default window)', () => lanesTab.click())
await journey('Lanes: 24h window', () => page.getByRole('radio', { name: '24h', exact: true }).click())
await journey('Lanes: leave to Board and come back', async () => {
  await page.getByRole('tab', { name: 'Board', exact: true }).click()
  await lanesTab.click()
})
await journey('open a finished session', () => page.getByText(/^Seeded session \d+$/).first().click())
await journey('session: Changes tab', () => page.getByRole('tab', { name: /Changes/ }).first().click())
await journey('reload on a session (deep link)', () => page.reload().then(() => {}))
await journey('idle for 30 s on a session', () => page.waitForTimeout(30_000))
await browser.close()
