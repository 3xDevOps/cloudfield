// Opens the seeded world's dashboard in headless Chromium, opens Lanes on one
// time window and records how the bars fill in.
//
//   bun research/2026-11-26-dashboard-performance/measure/lanes-browser.ts /tmp/perf-world.json <window: 1h|6h|24h|7d> <delay ms per request> [screenshot.png]
//
// The delay is added in front of every /api/v1 request, to stand in for a
// relay link. Output: requests by method while Lanes fills, bytes, the share
// of bar width still drawn "History not loaded" over time, and the renderer's
// own CPU time (CDP Performance.getMetrics), which does not include the wait.

import { readFileSync } from 'node:fs'
import { chromium } from '../../../web/node_modules/@playwright/test'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const windowName = process.argv[3] ?? '24h'
const delay = Number(process.argv[4] ?? 0)
const shot = process.argv[5]

const browser = await chromium.launch()
const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } })
const page = await context.newPage()
const cdp = await context.newCDPSession(page)
await cdp.send('Performance.enable')
const cpu = async () => {
  const { metrics } = await cdp.send('Performance.getMetrics')
  const of = (name: string) => metrics.find((m) => m.name === name)?.value ?? 0
  return { script: of('ScriptDuration'), layout: of('LayoutDuration'), style: of('RecalcStyleDuration'), task: of('TaskDuration') }
}

let phase = 'cold open'
const seen: Record<string, Record<string, { n: number; bytes: number }>> = {}
await page.route('**/api/v1/**', async (route) => {
  if (delay) await new Promise((r) => setTimeout(r, delay))
  const response = await route.fetch()
  const body = await response.body()
  const method = new URL(route.request().url()).pathname.split('/').pop()!
  const entry = ((seen[phase] ??= {})[method] ??= { n: 0, bytes: 0 })
  entry.n++
  entry.bytes += body.length
  await route.fulfill({ response, body })
})

const report = (name: string) => {
  const rows = Object.entries(seen[name] ?? {}).sort((a, b) => b[1].bytes - a[1].bytes)
  const total = rows.reduce((sum, [, v]) => ({ n: sum.n + v.n, bytes: sum.bytes + v.bytes }), { n: 0, bytes: 0 })
  console.log(`\n${name}: ${total.n} requests, ${total.bytes} bytes`)
  for (const [method, v] of rows) console.log(`  ${String(v.n).padStart(4)}  ${String(v.bytes).padStart(9)}  ${method}`)
}

await page.goto(world.url)
await page.getByRole('tab', { name: 'Lanes' }).waitFor()
await page.waitForLoadState('networkidle')
report('cold open')

// The 24 seeded sessions the restart interrupted sit in Needs you; the rest are Finished.
phase = 'lanes'
const before = await cpu()
await page.getByRole('tab', { name: 'Lanes' }).click()
await page.getByRole('radio', { name: windowName, exact: true }).click()
// Every workspace, and the Finished section unfolded, so each session draws its own bar.
await page.getByRole('combobox', { name: 'Lanes workspace scope' }).click()
await page.getByRole('option', { name: 'All workspaces' }).click()
const started = Date.now()
const tree = page.getByRole('tree', { name: 'Session lanes' })
await tree.getByRole('treeitem', { name: /^Finished, / }).first().click()

const unloadedShare = () => page.evaluate(() => {
  let dotted = 0
  let filled = 0
  for (const track of document.querySelectorAll('[data-lane-track]')) {
    for (const bar of track.querySelectorAll<HTMLElement>('.border-dotted')) dotted += bar.getBoundingClientRect().width
    for (const bar of track.querySelectorAll<HTMLElement>('[class*="bg-current"]')) filled += bar.getBoundingClientRect().width
  }
  return { dotted: Math.round(dotted), filled: Math.round(filled) }
})

console.log(`\nwindow ${windowName}, ${delay} ms added per request`)
console.log('   t(ms)  loading  unloaded px  drawn px  unloaded share')
let last = ''
for (;;) {
  const loading = await page.getByText('Loading history…').count()
  const { dotted, filled } = await unloadedShare()
  const line = `${loading ? 'yes' : 'no '}      ${String(dotted).padStart(8)}  ${String(filled).padStart(8)}  ${dotted + filled ? ((dotted / (dotted + filled)) * 100).toFixed(0) : '-'}%`
  if (line !== last) console.log(`${String(Date.now() - started).padStart(8)}  ${line}`)
  last = line
  if (!loading && Date.now() - started > 1500) break
  if (Date.now() - started > 120_000) { console.log('gave up after 120 s'); break }
  await page.waitForTimeout(100)
}
const after = await cpu()
console.log(`settled after ${Date.now() - started} ms; capped: ${await page.getByText('Older history not loaded').count() ? 'yes' : 'no'}`)
console.log(`renderer CPU while filling: script ${(after.script - before.script).toFixed(2)} s, layout ${(after.layout - before.layout).toFixed(2)} s, style ${(after.style - before.style).toFixed(2)} s, all tasks ${(after.task - before.task).toFixed(2)} s`)
report('lanes')
if (shot) await page.screenshot({ path: shot })
await browser.close()
