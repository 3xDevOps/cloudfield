// Cold open and reload, repeated: when the first session shows, when the store
// is hydrated (taken as the first presence.roster request, which the Team
// line sends only once hydrated), how many requests it took, and how often
// budget.get was called. Run against a world with PERF_LINK_DELAY_MS so the
// serial stages show.
//
//   bun research/2026-11-26-dashboard-performance/measure/cold-open.ts /tmp/perf-world.json [rounds=5]

import { readFileSync } from 'node:fs'
import { chromium } from '../../../web/node_modules/@playwright/test'

const world = JSON.parse(readFileSync(process.argv[2], 'utf8'))
const rounds = Number(process.argv[3] ?? 5)
const browser = await chromium.launch()
const median = (list: number[]) => [...list].sort((a, b) => a - b)[Math.floor(list.length / 2)]

async function visit(reload: boolean) {
  const context = await browser.newContext({ colorScheme: 'dark', viewport: { width: 1440, height: 900 } })
  const page = await context.newPage()
  let calls: string[] = []
  let started = Date.now()
  let hydratedAt = 0
  page.on('request', (request) => {
    const url = new URL(request.url())
    if (!url.pathname.startsWith('/api/v1/')) return
    const method = url.pathname.split('/').pop()!
    calls.push(method)
    if (method === 'presence.roster' && !hydratedAt) hydratedAt = Date.now() - started
  })
  await page.goto(world.url)
  await page.getByText(/^Seeded session \d+$/).first().waitFor()
  if (reload) {
    // The second load paints the saved snapshot first, then hydrates.
    await page.waitForLoadState('networkidle')
    calls = []
    hydratedAt = 0
    started = Date.now()
    await page.reload()
    await page.getByText(/^Seeded session \d+$/).first().waitFor()
  }
  const shown = Date.now() - started
  // Hydration, then the extras and the Team line's reads, then quiet: a socket that is still connecting sends no request.
  for (let waited = 0; !hydratedAt && waited < 15_000; waited += 50) await page.waitForTimeout(50)
  await page.waitForLoadState('networkidle')
  const quiet = Date.now() - started
  await context.close()
  return { shown, hydrated: hydratedAt, quiet, requests: calls.length, budgets: calls.filter((c) => c === 'budget.get').length }
}

for (const [name, reload] of [['cold open', false], ['reload (snapshot painted first)', true]] as const) {
  // The first visit also pays for the gateway's connection to the server.
  await visit(reload)
  const runs = []
  for (let i = 0; i < rounds; i++) runs.push(await visit(reload))
  const col = (key: 'shown' | 'hydrated' | 'quiet' | 'requests' | 'budgets') => runs.map((r) => r[key])
  console.log(`${name}: sessions shown median ${median(col('shown'))} ms (min ${Math.min(...col('shown'))}, max ${Math.max(...col('shown'))}); hydrated median ${median(col('hydrated'))} ms (min ${Math.min(...col('hydrated'))}, max ${Math.max(...col('hydrated'))}); quiet at median ${median(col('quiet'))} ms; requests ${median(col('requests'))}; budget.get ${median(col('budgets'))} (4 workspaces); n=${rounds}`)
}
await browser.close()
