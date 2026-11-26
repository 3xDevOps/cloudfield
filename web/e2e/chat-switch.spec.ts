// Returning to a session's Chat: the list is remounted by the switch, and it
// has to paint its first frame where it was left, not from guessed row
// heights that it corrects a frame or two later.
//
// The sessions and their room messages are answered in the browser, as in
// session-tiles.spec.ts: what is under test is the list, not how a session
// starts.

import type { Page } from '@playwright/test'
import { expect, test } from './fixtures'
import { memberID, seedWorkspace } from './harness/setup'

interface Frame {
  list: number
  top: number
  height: number
}

/** Records the Chat list's scroll offset and content height on every frame; each mounted list gets its own number. */
const record = (page: Page) => page.evaluate(() => {
  const w = window as unknown as { chatFrames: Frame[]; chatLists: WeakMap<Element, number> }
  w.chatLists ??= new WeakMap()
  w.chatFrames = []
  let next = Date.now()
  const tick = () => {
    const log = document.querySelector('[role="log"][aria-label="Chat"]')
    if (log) {
      if (!w.chatLists.has(log)) w.chatLists.set(log, next++)
      w.chatFrames.push({ list: w.chatLists.get(log)!, top: Math.round(log.scrollTop), height: log.scrollHeight })
    }
    requestAnimationFrame(tick)
  }
  requestAnimationFrame(tick)
})

const frames = (page: Page) => page.evaluate(() => (window as unknown as { chatFrames: Frame[] }).chatFrames)

test('a Chat returned to paints its first frame at the scroll position it settles on', async ({ page, aether }) => {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'), 'shop')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const member = await memberID(alice)
  const started = new Date(Date.now() - 20 * 60_000).toISOString()
  const titles = ['Lead the checkout rewrite', 'Review the checkout rewrite']
  const runs = titles.map((task, n) => ({
    id: `run-chat-${n}`, workspace_id: workspaces[0].id, member_id: member, account_member_id: member, task, harness: 'claude', mode: 'tui',
    status: 'running', state: 'working', status_seq: 0, branch: `aether/run-chat-${n}`, created_at: started, started_at: started, finished_at: null,
  }))
  await page.route('**/api/v1/run.list', (route) => route.fulfill({ json: { runs } }))
  // Rows of uneven height, which a list guessing one height for every row gets wrong.
  await page.route('**/api/v1/run.room.list', (route) => {
    const runID = (route.request().postDataJSON() as { run_id: string }).run_id
    const messages = Array.from({ length: 40 }, (_, n) => {
      const at = new Date(Date.parse(started) + n * 1000).toISOString()
      return {
        id: `${runID}-ask-${n}`, workspace_id: workspaces[0].id, run_id: runID, actor_id: member, kind: 'steer_request', state: 'sent', agent_delivery: 'delivered',
        body: Array.from({ length: 1 + (n % 5) }, (_, line) => `${runID} ask ${n} line ${line}`).join('\n\n'), created_at: at, updated_at: at,
      }
    })
    return route.fulfill({ json: { messages } })
  })
  await page.setViewportSize({ width: 1440, height: 900 })
  await page.goto(alice.url)

  const log = page.getByRole('log', { name: 'Chat' })
  const rows = titles.map((title) => page.getByRole('navigation', { name: 'Cloudfield' }).getByRole('region', { name: 'Sessions' }).getByRole('button', { name: new RegExp(title) }))
  for (const [n, row] of rows.entries()) {
    await row.click()
    await page.getByRole('tab', { name: 'Chat' }).click()
    await expect(log.getByText(`run-chat-${n} ask 39 line 0`)).toBeVisible()
  }

  for (const n of [0, 1]) {
    await record(page)
    await rows[n].click()
    await expect(log.getByText(`run-chat-${n} ask 39 line 0`)).toBeVisible()
    // Long enough for a list that started from guessed heights to measure its rows and move.
    await page.waitForTimeout(1000)
    const all = await frames(page)
    const shown = all.filter((frame) => frame.list !== all[0].list)
    expect(shown.length, 'the switch mounts a new list').toBeGreaterThan(0)
    const settled = shown.at(-1)!
    expect(settled.top, 'the list is scrolled, so it has an offset to get wrong').toBeGreaterThan(0)
    expect([...new Set(shown.map((frame) => `${frame.top}/${frame.height}`))], 'every frame of the returned Chat is its settled one').toEqual([`${settled.top}/${settled.height}`])
  }
})
