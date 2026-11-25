// Session tiles under a real pointer, against a real server and gateway. The
// sessions are answered in the browser, as in lanes.spec.ts: what is under
// test is how the layout follows the pointer, the address and a reload, not
// how sessions start. The last case attaches real terminals and needs Docker.

import type { Locator, Page } from '@playwright/test'
import { type Aether, expect, test } from './fixtures'
import { dockerReachable } from './harness/server'
import { memberID, seedWorkspace } from './harness/setup'

const lead = 'Lead the checkout rewrite'
const reviewer = 'Review the checkout rewrite'
const tester = 'Test the checkout rewrite'

/** Alice's workspace with three working Standard sessions, on a window that fits four tiles. */
async function setUp(page: Page, aether: Aether) {
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('shop'), 'shop')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  const member = await memberID(alice)
  const started = new Date(Date.now() - 20 * 60_000).toISOString()
  const run = (id: string, task: string) => ({
    id, workspace_id: workspaces[0].id, member_id: member, account_member_id: member, task, harness: 'claude', mode: 'tui',
    status: 'running', state: 'working', status_seq: 0, branch: `aether/${id}`, created_at: started, started_at: started, finished_at: null,
  })
  const runs = [run('run-tiles-lead', lead), run('run-tiles-reviewer', reviewer), run('run-tiles-tester', tester)]
  await page.route('**/api/v1/run.list', (route) => route.fulfill({ json: { runs } }))
  await page.setViewportSize({ width: 1920, height: 1200 })
  await page.goto(alice.url)
}

const row = (page: Page, title: string) =>
  page.getByRole('navigation', { name: 'Cloudfield' }).getByRole('region', { name: 'Sessions' }).getByRole('button', { name: new RegExp(title) })

/** The tile at `place` of `count`, in reading order. */
const tile = (page: Page, place: number, count: number) => page.getByRole('region', { name: new RegExp(`, tile ${place} of ${count}$`) })

const sessionTab = (within: Locator, title: string) => within.getByRole('tablist', { name: 'Sessions in this tile' }).getByRole('tab', { name: new RegExp(title) })

/** Whether the tile draws the focus outline, which is its `::after` border. */
const outlined = (target: Locator) => target.evaluate((node) => getComputedStyle(node, '::after').borderTopColor !== 'rgba(0, 0, 0, 0)')

const middle = async (target: Locator) => {
  const box = (await target.boundingBox())!
  return { x: box.x + box.width / 2, y: box.y + box.height / 2 }
}

/** Waits out the glide that follows a split, a close or a move: the tile has reached `width`. */
const landed = (target: Locator, width: number) =>
  expect.poll(async () => Math.round((await target.boundingBox())!.width)).toBe(Math.round(width))

/** Presses on `from` and lets go at a point, moving in steps as a hand does. */
async function drag(page: Page, from: Locator, to: { x: number; y: number }) {
  const start = await middle(from)
  await page.mouse.move(start.x, start.y)
  await page.mouse.down()
  await page.mouse.move(to.x, to.y, { steps: 12 })
  await page.mouse.up()
}

async function openBeside(page: Page) {
  await row(page, lead).click()
  await expect(tile(page, 1, 1)).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  await row(page, reviewer).hover()
  await row(page, reviewer).locator('..').getByRole('button', { name: 'Open to the side' }).click()
  await expect(tile(page, 2, 2)).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
}

test('Split right opens a tile to pick a session into, and a click moves the focus', async ({ page, aether }) => {
  await setUp(page, aether)
  await row(page, lead).click()
  const only = tile(page, 1, 1)
  await expect(sessionTab(only, lead)).toHaveAttribute('aria-selected', 'true')
  expect(await outlined(only)).toBe(false)
  const area = (await only.boundingBox())!
  await expect(only.getByRole('button', { name: 'Maximize' })).toHaveCount(0)

  await only.getByRole('button', { name: 'Split right' }).click()
  const picker = tile(page, 2, 2).getByRole('region', { name: 'Open in this tile' })
  await expect(picker.getByPlaceholder('Find a session or view')).toBeFocused()
  await picker.getByRole('option', { name: new RegExp(reviewer) }).click()

  const left = tile(page, 1, 2)
  const right = tile(page, 2, 2)
  await expect(right).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(reviewer)
  expect(await outlined(right)).toBe(true)
  expect(await outlined(left)).toBe(false)
  expect(new URL(page.url()).searchParams.get('run')).toBe('run-tiles-reviewer')

  // The strip and the view row keep their place when the focus moves.
  await landed(left, area.width / 2)
  const rows = () => Promise.all([left, right].flatMap((each) => [
    each.getByRole('tablist', { name: 'Sessions in this tile' }).boundingBox(),
    each.locator('header[data-slot="pane-header"]').boundingBox(),
  ]))
  const before = await rows()
  await sessionTab(left, lead).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(lead)
  expect(await outlined(left)).toBe(true)
  expect(await outlined(right)).toBe(false)
  expect(await rows()).toEqual(before)
  await expect(row(page, lead)).toHaveAttribute('aria-current', 'page')
  expect(new URL(page.url()).searchParams.get('run')).toBe('run-tiles-lead')

  await left.getByRole('button', { name: 'Maximize' }).click()
  const whole = tile(page, 1, 1)
  await expect(whole).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  await expect(whole.getByRole('button', { name: new RegExp(reviewer) })).toBeVisible()
  await whole.getByRole('button', { name: 'Restore tiles' }).click()
  await expect(tile(page, 2, 2)).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
})

test('a layout opened from the sidebar survives a reload, a deep link and Back', async ({ page, aether }) => {
  await setUp(page, aether)
  await openBeside(page)
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(reviewer)

  // Focus clicks replace the history entry, so one Back leaves both of them behind.
  const entries = await page.evaluate(() => history.length)
  await sessionTab(tile(page, 1, 2), lead).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(lead)
  await sessionTab(tile(page, 2, 2), reviewer).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(reviewer)
  expect(await page.evaluate(() => history.length)).toBe(entries)
  await page.goBack()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(lead)
  expect(new URL(page.url()).searchParams.get('run')).toBe('run-tiles-lead')
  await expect(tile(page, 2, 2)).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))

  await page.reload()
  await expect(tile(page, 1, 2)).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  await expect(tile(page, 2, 2)).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(lead)

  // A link to a session no tile holds replaces the focused tile's tab and leaves the other tile alone.
  const address = new URL(page.url())
  address.search = '?run=run-tiles-tester&view=changes'
  await page.goto(address.href)
  await expect(tile(page, 1, 2)).toHaveAccessibleName(new RegExp(`^${tester}, Changes`))
  await expect(tile(page, 2, 2)).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
  expect(await outlined(tile(page, 1, 2))).toBe(true)
})

test('a sidebar row dropped on an edge splits, and a tab dropped on a tile joins it', async ({ page, aether }) => {
  await setUp(page, aether)
  await row(page, lead).click()
  const only = tile(page, 1, 1)
  await expect(only).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  const area = (await only.boundingBox())!

  const from = await middle(row(page, reviewer))
  await page.mouse.move(from.x, from.y)
  await page.mouse.down()
  await page.mouse.move(area.x + area.width * 0.05, area.y + area.height / 2, { steps: 12 })
  await expect(page.locator('[data-slot="drag-chip"]')).toContainText('Split left')
  const preview = (await page.locator('[data-slot="drop-preview"]').boundingBox())!
  expect(Math.round(preview.width)).toBe(Math.round(area.width / 2))
  await page.mouse.up()

  const left = tile(page, 1, 2)
  const right = tile(page, 2, 2)
  await expect(left).toHaveAccessibleName(new RegExp(`^${reviewer}, Terminal`))
  await expect(right).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(reviewer)

  await drag(page, sessionTab(right, lead), await middle(left))
  const joined = tile(page, 1, 1)
  await expect(joined.getByRole('tablist', { name: 'Sessions in this tile' }).getByRole('tab')).toHaveCount(2)
  await expect(sessionTab(joined, lead)).toHaveAttribute('aria-selected', 'true')

  // One click on a control of a tile that is not focused acts: its strip keeps the layout it had until the press ends.
  await page.setViewportSize({ width: 1300, height: 900 })
  await row(page, tester).hover()
  await row(page, tester).locator('..').getByRole('button', { name: 'Open to the side' }).click()
  const rest = tile(page, 1, 2)
  const focused = tile(page, 2, 2)
  await expect(focused).toHaveAccessibleName(new RegExp(`^${tester}, Terminal`))
  await rest.getByRole('button', { name: 'Add a tab' }).click()
  await expect(page.getByRole('dialog').getByPlaceholder('Find a session or view')).toBeFocused()
  await page.keyboard.press('Escape')

  await sessionTab(focused, tester).click()
  expect(await outlined(rest)).toBe(false)
  await sessionTab(rest, reviewer).click()
  await expect(sessionTab(rest, reviewer)).toHaveAttribute('aria-selected', 'true')

  await sessionTab(focused, tester).click()
  expect(await outlined(rest)).toBe(false)
  await rest.getByRole('button', { name: `Close ${lead}` }).click()
  await expect(rest.getByRole('tablist', { name: 'Sessions in this tile' }).getByRole('tab')).toHaveCount(1)
})

test('a view torn off opens beside its session, and the seam between them drags', async ({ page, aether }) => {
  await setUp(page, aether)
  await row(page, lead).click()
  const only = tile(page, 1, 1)
  const area = (await only.boundingBox())!
  await drag(page, only.getByRole('tablist', { name: 'Session views' }).getByRole('tab', { name: 'Changes' }), { x: area.x + area.width * 0.95, y: area.y + area.height / 2 })

  const left = tile(page, 1, 2)
  const right = tile(page, 2, 2)
  await expect(left).toHaveAccessibleName(new RegExp(`^${lead}, Terminal`))
  await expect(right).toHaveAccessibleName(new RegExp(`^${lead}, Changes`))
  await expect(sessionTab(left, lead)).toContainText('Terminal')
  await expect(sessionTab(right, lead)).toContainText('Changes')
  // The session's controls sit in one tile only: the focused one.
  await expect(page.getByRole('toolbar', { name: 'Session actions' })).toHaveCount(1)
  await expect(right.getByRole('toolbar', { name: 'Session actions' })).toBeVisible()

  await landed(left, area.width / 2)
  const at = await middle(page.getByRole('separator', { name: 'Resize the tiles' }))
  await page.mouse.move(at.x, at.y)
  await page.mouse.down()
  await page.mouse.move(at.x - 120, at.y, { steps: 6 })
  await page.mouse.up()
  await landed(left, area.width / 2 - 120)
})

test('a focus click on a terminal tile neither reattaches nor resizes its terminal', async ({ page, aether }) => {
  test.skip(!dockerReachable(), 'a session needs a reachable Docker daemon')
  const alice = await aether.member('alice')
  await seedWorkspace(alice, aether.server.addr, await aether.seedRepo('project'))
  aether.installAgent(await memberID(alice), 'claude', 'while :; do sleep 1; done')
  const { workspaces } = await alice.api.rpc<{ workspaces: { id: string }[] }>('workspace.list')
  for (const task of [lead, reviewer]) {
    await alice.api.rpc('run.launch', { workspace_id: workspaces[0].id, harness: 'claude', task })
  }
  let attaches = 0
  const sent: string[] = []
  page.on('websocket', (socket) => {
    if (!socket.url().includes('/ws/attach/')) return
    attaches++
    socket.on('framesent', (frame) => {
      if (typeof frame.payload === 'string') sent.push(frame.payload)
    })
  })
  await page.setViewportSize({ width: 1920, height: 1200 })
  await page.goto(alice.url)
  await openBeside(page)
  const left = tile(page, 1, 2)
  const right = tile(page, 2, 2)
  const screen = (within: Locator) => within.locator('.xterm-screen:not([data-aether-frozen-view] *)')
  for (const each of [left, right]) await expect(screen(each)).toBeVisible()
  // The split's own resize has settled once two reads a second apart agree.
  let seen = -1
  await expect.poll(() => {
    const same = sent.length === seen
    seen = sent.length
    return same
  }, { intervals: [1000] }).toBe(true)
  const sockets = attaches
  const frames = sent.length

  await screen(left).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(lead)
  await screen(right).click()
  await expect(page.getByRole('heading', { level: 1 })).toHaveText(reviewer)
  await page.waitForTimeout(1000)
  expect(attaches).toBe(sockets)
  expect(sent.slice(frames).filter((frame) => frame.includes('"resize"'))).toEqual([])
})
