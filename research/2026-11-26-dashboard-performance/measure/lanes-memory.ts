// What holding more lane history costs the browser. Needs a dashboard built
// with a raised `historyBudget` (web/src/store/lanes.ts) so the whole window
// loads, and a large seeded store (world.ts ... --sessions 3000).
// For each time window: lane events held (counted from the responses), the
// JS heap after a forced collection against the heap on the Board, the
// renderer's CPU while the window filled, and requests.
//
//   bun research/2026-11-26-dashboard-performance/measure/lanes-memory.ts /tmp/world-big.json

import { readFileSync } from 'node:fs'
import { chromium } from '../../../web/node_modules/@playwright/test'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const browser = await chromium.launch()
console.log('window  events held  requests  MB fetched  heap MB over Board  script s  all tasks s  settled s')
for (const windowName of ['1h', '6h', '24h', '7d']) {
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  const cdp = await context.newCDPSession(page)
  await cdp.send('Performance.enable')
  await cdp.send('HeapProfiler.enable')
  const metrics = async () => {
    await cdp.send('HeapProfiler.collectGarbage')
    const { metrics } = await cdp.send('Performance.getMetrics')
    const of = (name: string) => metrics.find((m) => m.name === name)?.value ?? 0
    return { heap: of('JSHeapUsedSize'), script: of('ScriptDuration'), task: of('TaskDuration') }
  }
  let events = 0
  let requests = 0
  let bytes = 0
  page.on('response', async (response) => {
    if (!response.url().endsWith('/api/v1/workspace.timeline')) return
    const body = await response.text().catch(() => '')
    requests++
    bytes += body.length
    try { events += JSON.parse(body).events.length } catch { /* a failed read counts nothing */ }
  })
  await page.goto(world.url)
  await page.getByRole('tab', { name: 'Lanes' }).waitFor()
  await page.waitForLoadState('networkidle')
  const before = await metrics()
  const started = Date.now()
  await page.getByRole('tab', { name: 'Lanes' }).click()
  await page.getByRole('radio', { name: windowName, exact: true }).click()
  await page.getByRole('combobox', { name: 'Lanes workspace scope' }).click()
  await page.getByRole('option', { name: 'All workspaces' }).click()
  await page.waitForTimeout(500)
  while (await page.getByText('Loading history…').count()) await page.waitForTimeout(200)
  const settled = (Date.now() - started) / 1000
  await page.waitForTimeout(500)
  const after = await metrics()
  console.log(`${windowName.padEnd(6)}  ${String(events).padStart(11)}  ${String(requests).padStart(8)}  ${(bytes / 1e6).toFixed(1).padStart(10)}  ${((after.heap - before.heap) / 1e6).toFixed(1).padStart(18)}  ${(after.script - before.script).toFixed(1).padStart(8)}  ${(after.task - before.task).toFixed(1).padStart(11)}  ${settled.toFixed(1).padStart(9)}`)
  await context.close()
}
await browser.close()
