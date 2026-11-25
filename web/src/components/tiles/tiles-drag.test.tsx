import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { changeTiles } from '@/components/tiles/area'
import { TileArea } from '@/components/tiles/tile-area'
import type * as xtermHostModule from '@/components/xterm-host'
import type * as apiModule from '@/lib/api'
import { RunFrame } from '@/routes/run'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyTiles, openTab, pickTab, splitTile, tileList, type TileNode } from '@/store/tiles'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/components/xterm-host', async (importOriginal) => {
  const actual = await importOriginal<typeof xtermHostModule>()
  return { ...actual, useXterm: (options: Parameters<typeof actual.useXterm>[0] = {}) => actual.useXterm({ ...options, enabled: false }) }
})

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

const enhanced = { mode: 'acp', acp: true } as const
const first = 'rewrite the checkout flow'
const second = 'speed up search'
const third = 'tidy the readme'
const seeds = [run(enhanced), run({ ...enhanced, id: 'run_2', task: second }), run({ ...enhanced, id: 'run_3', task: third })]

function Page() {
  const route = useStore((s) => s.route)
  return (
    <>
      <SidebarRuns />
      {route.name === 'run' ? <main id="main"><TileArea params={route.params} Frame={RunFrame} /></main> : <p>the board</p>}
    </>
  )
}

/**
 * Lays the page out as a browser would, which jsdom never does: the area fills
 * the window, each tile takes its percentages of it, a strip is 32px tall and
 * its tabs are 100px wide. A sidebar row and a view segment are 10px squares.
 */
function show(width = 1600, height = 900) {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(function (this: HTMLElement) {
    const box = (left: number, top: number, w: number, h: number) => ({ left, top, width: w, height: h, right: left + w, bottom: top + h, x: left, y: top, toJSON: () => ({}) })
    const node = this.closest<HTMLElement>('[data-tile]')
    if (!node) return this.matches('[data-run-row]') ? box(0, 0, 10, 10) : box(0, 0, width, height)
    const share = (value: string, of: number) => (parseFloat(value) / 100) * of
    const tile = box(share(node.style.left, width), share(node.style.top, height), share(node.style.width, width), share(node.style.height, height))
    const tab = this.closest<HTMLElement>('[data-tab]')
    if (tab) return box(tile.left + [...node.querySelectorAll('[data-tab]')].indexOf(tab) * 100, tile.top, 100, 32)
    if (this.role === 'tab') return box(tile.left, tile.top + 32, 10, 10)
    return this === node.firstElementChild ? box(tile.left, tile.top, tile.width, 32) : tile
  })
  act(() => useStore.getState().navigate('run', { runId: 'run_1' }))
  return render(<Page />)
}

const state = () => useStore.getState()
const beside = (runId: string, view?: RunView) =>
  act(async () => changeTiles((layout, ctx) => {
    const split = splitTile(layout, layout.focused, 'right')
    return pickTab(split, split.focused, runId, ctx, view)
  }, true))
/** A session that mounts inside an event renders a microtask later under jsdom. */
const tab = (runId: string) => act(async () => changeTiles((layout, ctx) => openTab(layout, layout.focused, runId, ctx), true))
const tiles = () => screen.getAllByRole('region', { name: /, tile \d+ of \d+$/ })
const names = () => tiles().map((node) => node.getAttribute('aria-label')!.replace(/, tile.*/, ''))
const row = (title: string) => screen.getByRole('button', { name: new RegExp(` · ${title} · `) })
const sessionTab = (n: number, title: string) => within(within(tiles()[n - 1]).getByRole('tablist', { name: 'Sessions in this tile' })).getByRole('tab', { name: new RegExp(title) })
const segment = (n: number, name: string) => within(within(tiles()[n - 1]).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name })
const chip = () => document.querySelector('[data-slot=drag-chip]')?.textContent
const preview = () => document.querySelector<HTMLElement>('[data-slot=drop-preview]')
const said = (words: string) => screen.getByText(words, { selector: '[role=status]' })
/** The tree as nested session ids: a row split is `['row', a, b]`, a tile the list of its tabs. */
const tree = (node: TileNode = state().tiles.root): unknown => ('tabs' in node ? node.tabs.map((held) => held.runId) : [node.dir, tree(node.a), tree(node.b)])
const tabIds = () => tileList(state().tiles.root).flatMap((node) => node.tabs.map((held) => held.id))

function lift(source: Element, pointerType = 'mouse') {
  fireEvent.pointerDown(source, { button: 0, pointerType, clientX: 5, clientY: 5 })
  fireEvent.pointerMove(window, { buttons: 1, clientX: 8, clientY: 5 })
  fireEvent.pointerMove(window, { buttons: 1, clientX: 12, clientY: 12 })
}
const over = (clientX: number, clientY: number) => fireEvent.pointerMove(window, { buttons: 1, clientX, clientY })
const release = (clientX: number, clientY: number) => fireEvent.pointerUp(window, { clientX, clientY })

beforeEach(() => {
  StubSocket.install()
  useStore.setState({
    hydrated: true,
    staleSince: null,
    info: serverInfo,
    identityKey: null,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    mineOnly: false,
    runs: Object.fromEntries(seeds.map((seed) => [seed.id, toRecord(seed)])),
    runViewMemory: {},
    tiles: emptyTiles(),
    tileDrag: null,
    savedTiles: {},
    roomMessages: {},
    roomStatus: {},
    acpSessions: {},
    browserRequests: {},
    agentList: null,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('dragging a sidebar row', () => {
  it.each([
    ['left', 100, 450, 'Split left', ['row', ['run_2'], ['run_1']]],
    ['right', 1500, 450, 'Split right', ['row', ['run_1'], ['run_2']]],
    ['up', 800, 100, 'Split up', ['column', ['run_2'], ['run_1']]],
    ['down', 800, 850, 'Split down', ['column', ['run_1'], ['run_2']]],
    ['centre', 800, 450, 'Add as tab', ['run_1', 'run_2']],
    ['strip', 800, 16, 'Add as tab', ['run_1', 'run_2']],
  ])('opens the session on the %s of the tile it is dropped on', (_, x, y, action, expected) => {
    show()
    lift(row(second))
    over(x, y)
    expect(chip()).toBe(`${second}${action}`)
    expect(preview()).toBeDefined()
    const added = action === 'Add as tab'
    expect(document.querySelector(added ? '[data-slot=drop-tab]' : '[data-slot=drop-stays]')!.textContent).toBe(added ? second : first)
    release(x, y)
    expect(tree()).toEqual(expected)
    expect(state().route.params.runId).toBe('run_2')
    expect(chip()).toBeUndefined()
    expect(said(`${second}: ${action}`)).toBeDefined()
  })

  it('keeps the chip clear of the plate that names what lands', () => {
    vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(400)
    vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(28)
    show()
    lift(row(second))
    over(1500, 440)
    const top = () => document.querySelector<HTMLElement>('[data-slot=drag-chip]')!.style.top
    expect(top()).toBe('472px')
    over(1500, 300)
    expect(top()).toBe('316px')
  })

  it('covers the room the new tile will take, its floor included, and drops the focus outline while it does', async () => {
    show()
    await beside('run_3')
    lift(row(second))
    over(1500, 450)
    expect(preview()!.style).toMatchObject({ left: '1180px', top: '0px', width: '420px', height: '900px' })
    expect(tiles().some((node) => node.className.includes('after:border-accent'))).toBe(false)
    over(1500, 880)
    expect(preview()!.style).toMatchObject({ left: '800px', top: '450px', width: '800px', height: '450px' })
    expect(preview()!.className).not.toContain('transition')
    expect(preview()!.closest('[aria-hidden=true]')).not.toBeNull()
  })

  it('adds a tab where the 420 by 400 floor leaves no room for a tile', () => {
    show(800, 700)
    lift(row(second))
    over(760, 350)
    expect(chip()).toBe(`${second}Add as tab`)
    expect(preview()!.style.width).toBe('800px')
    release(760, 350)
    expect(tree()).toEqual(['run_1', 'run_2'])
  })

  it('changes nothing on Escape, keeps the session open and swallows the click of the release', async () => {
    show()
    const before = state().tiles
    lift(row(second))
    over(1500, 450)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(chip()).toBeUndefined()
    expect(said('Drag cancelled')).toBeDefined()
    release(1500, 450)
    fireEvent.click(row(second))
    expect(state().tiles).toBe(before)
    expect(state().route).toMatchObject({ name: 'run', params: { runId: 'run_1' } })

    await act(() => new Promise((resolve) => setTimeout(resolve)))
    fireEvent.click(row(second))
    expect(state().route.params.runId).toBe('run_2')
  })

  it('stays a click under 4px, and never starts from a finger', () => {
    show()
    fireEvent.pointerDown(row(second), { button: 0, pointerType: 'mouse', clientX: 5, clientY: 5 })
    fireEvent.pointerMove(window, { buttons: 1, clientX: 7, clientY: 7 })
    expect(state().tileDrag).toBeNull()
    release(7, 7)
    lift(row(second), 'touch')
    expect(state().tileDrag).toBeNull()
  })

  it('swallows no later click when the release after Escape never arrived', () => {
    show()
    lift(row(second))
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.pointerDown(row(second), { button: 0, pointerType: 'mouse', clientX: 5, clientY: 5 })
    release(5, 5)
    fireEvent.click(row(second))
    expect(state().route.params.runId).toBe('run_2')
  })

  it('opens the session when a shaky click travels 4px and ends on the row', () => {
    show()
    fireEvent.pointerDown(row(second), { button: 0, pointerType: 'mouse', clientX: 5, clientY: 5 })
    over(9, 9)
    expect(chip()).toBe(second)
    expect(preview()).toBeNull()
    expect(document.querySelector('[data-slot=drag-chip]')!.parentElement!.className).toContain('pointer-events-none')
    release(9, 9)
    fireEvent.click(row(second))
    expect(tree()).toEqual(['run_2'])
    expect(state().route.params.runId).toBe('run_2')
  })

  it('only adds a tab to a maximized tile', async () => {
    show()
    await beside('run_3')
    await act(async () => changeTiles((layout) => ({ ...layout, maximized: true })))
    lift(row(second))
    over(1500, 450)
    expect(chip()).toBe(`${second}Add as tab`)
    expect(preview()!.style.width).toBe('1600px')
    release(1500, 450)
    expect(tree()).toEqual(['row', ['run_1'], ['run_3', 'run_2']])
    expect(state().tiles.maximized).toBe(true)
  })

  it('answers no shortcut while a drag is in flight', async () => {
    show()
    await tab('run_2')
    const before = state().tiles
    lift(sessionTab(1, second))
    fireEvent.keyDown(document.body, { key: 'w', code: 'KeyW' })
    fireEvent.keyDown(document.body, { key: '\\', code: 'Backslash', ctrlKey: true })
    expect(state().tiles).toBe(before)
    expect(state().tileDrag).not.toBeNull()
  })

  it('ends when the button was let go outside the window', () => {
    show()
    lift(row(second))
    fireEvent.pointerMove(window, { buttons: 0, clientX: 800, clientY: 450 })
    expect(state().tileDrag).toBeNull()
    expect(tree()).toEqual(['run_1'])
  })
})

describe('dragging a tab', () => {
  it('moves it to the edge of another tile, previewed as it lands once its emptied tile closes', async () => {
    show()
    await beside('run_2')
    const before = tabIds()
    lift(sessionTab(2, second))
    over(400, 850)
    expect(chip()).toContain('Split down')
    expect(preview()!.style).toMatchObject({ left: '0px', top: '450px', width: '1600px', height: '450px' })
    expect(document.querySelector<HTMLElement>('[data-slot=drop-stays]')!.parentElement!.style).toMatchObject({ left: '0px', top: '0px', width: '1600px', height: '450px' })
    release(400, 850)
    expect(tree()).toEqual(['column', ['run_1'], ['run_2']])
    expect(tabIds()).toEqual(before)
    expect(said(`${second} · Chat: Split down`)).toBeDefined()
  })

  it('moves it into another strip where the line shows', async () => {
    show()
    await tab('run_3')
    await beside('run_2')
    lift(sessionTab(2, second))
    over(120, 16)
    expect(chip()).toContain('Move tab')
    expect(preview()!.style).toMatchObject({ left: '99px', width: '2px', height: '32px' })
    release(120, 16)
    expect(tree()).toEqual(['run_1', 'run_2', 'run_3'])
  })

  it('reorders inside its own strip', async () => {
    show()
    await tab('run_2')
    await tab('run_3')
    lift(sessionTab(1, third))
    over(10, 16)
    release(10, 16)
    expect(tree()).toEqual(['run_3', 'run_1', 'run_2'])
    expect(state().route.params.runId).toBe('run_3')
  })

  it('offers nothing over the middle of its own tile, or anywhere on a tile it is alone in', async () => {
    show()
    lift(sessionTab(1, first))
    over(1500, 450)
    expect(preview()).toBeNull()
    expect(chip()).toBe(`${first}·Chat`)
    release(1500, 450)
    expect(tree()).toEqual(['run_1'])

    await tab('run_2')
    lift(sessionTab(1, second))
    over(800, 450)
    expect(preview()).toBeNull()
    over(1500, 450)
    release(1500, 450)
    expect(tree()).toEqual(['row', ['run_1'], ['run_2']])
  })
})

describe('dragging a view', () => {
  it('tears a view off to the side, leaving a dashed slot while it is lifted', () => {
    show()
    lift(segment(1, 'Changes'))
    expect(segment(1, 'Changes').className).toContain('outline-dashed')
    over(1500, 450)
    expect(chip()).toBe(`${first}·ChangesSplit right`)
    expect(document.querySelector('[data-slot=drop-stays]')!.textContent).toBe(first)
    release(1500, 450)
    expect(names()).toEqual([`${first}, Chat`, `${first}, Changes`])
    expect(segment(1, 'Changes').className).not.toContain('outline-dashed')
  })

  it('opens the view when a shaky click travels 4px and ends on the segment', () => {
    show()
    fireEvent.pointerDown(segment(1, 'Changes'), { button: 0, pointerType: 'mouse', clientX: 5, clientY: 37 })
    over(9, 41)
    expect(state().tileDrag).not.toBeNull()
    expect(preview()).toBeNull()
    release(9, 41)
    fireEvent.click(segment(1, 'Changes'))
    expect(names()).toEqual([`${first}, Changes`])
  })

  it('offers nothing on the tile whose tab already holds the view', async () => {
    show()
    await beside('run_1', 'changes')
    await tab('run_2')
    const before = tabIds()
    lift(segment(1, 'Changes'))
    over(1200, 450)
    expect(preview()).toBeNull()
    release(1200, 450)
    expect(tree()).toEqual(['row', ['run_1'], ['run_1', 'run_2']])
    expect(tabIds()).toEqual(before)
  })

  it('opens a view on the click, not the press, and does not drag the view it shows', () => {
    show()
    fireEvent.mouseDown(segment(1, 'Changes'))
    expect(names()).toEqual([`${first}, Chat`])
    lift(segment(1, 'Chat'))
    expect(state().tileDrag).toBeNull()
    fireEvent.click(segment(1, 'Changes'))
    expect(names()).toEqual([`${first}, Changes`])
  })

  it('moves the tab that already holds the view and never makes a second one', async () => {
    show()
    await beside('run_1', 'changes')
    act(() => changeTiles((layout) => ({ ...layout, focused: tileList(layout.root)[0].id })))
    const before = tabIds()
    lift(segment(1, 'Changes'))
    over(400, 850)
    release(400, 850)
    expect(names()).toEqual([`${first}, Chat`, `${first}, Changes`])
    expect(state().tiles.root).toMatchObject({ dir: 'column' })
    expect(tabIds()).toEqual(before)
  })

  it('adds the view as a tab on the middle of another tile', async () => {
    show()
    await beside('run_2')
    lift(segment(1, 'Files'))
    over(1200, 450)
    release(1200, 450)
    expect(tree()).toEqual(['row', ['run_1'], ['run_2', 'run_1']])
    expect(names()).toEqual([`${first}, Chat`, `${first}, Files`])
  })
})

describe('a strip with more tabs than room', () => {
  it('folds the tabs that do not fit into a menu, the last first, and a moved tab still lands where it was dropped', async () => {
    show(360)
    await tab('run_2')
    await tab('run_3')
    const strip = within(within(tiles()[0]).getByRole('tablist', { name: 'Sessions in this tile' }))
    expect(strip.getAllByRole('tab').map((shown) => shown.textContent)).toEqual([expect.stringContaining(first), expect.stringContaining(third)])

    lift(sessionTab(1, first))
    over(180, 10)
    release(180, 10)
    expect(tree()).toEqual(['run_2', 'run_3', 'run_1'])

    await userEvent.click(screen.getByRole('button', { name: '1 more' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: new RegExp(third) }))
    expect(state().route.params.runId).toBe('run_3')
    expect(strip.getByRole('tab', { name: new RegExp(third) }).getAttribute('aria-selected')).toBe('true')
    expect(strip.queryByRole('tab', { name: new RegExp(first) })).toBeNull()
  })

  it('counts 128px for a background tab that shows its view\'s icon', async () => {
    show(400)
    await tab('run_1')
    await tab('run_3')
    expect(screen.getByRole('button', { name: '1 more' })).toBeDefined()
  })

  it('folds nothing while every tab fits at its floor', async () => {
    show(400)
    await tab('run_2')
    await tab('run_3')
    expect(within(within(tiles()[0]).getByRole('tablist', { name: 'Sessions in this tile' })).getAllByRole('tab')).toHaveLength(3)
  })
})
