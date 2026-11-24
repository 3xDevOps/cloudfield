import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type * as React from 'react'
import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { changeTiles, openToSide } from '@/components/tiles/area'
import { TileArea } from '@/components/tiles/tile-area'
import type * as xtermHostModule from '@/components/xterm-host'
import type * as apiModule from '@/lib/api'
import { bindRouteToUrl } from '@/lib/url-state'
import { lookupRoute } from '@/routes/registry'
import { RunFrame, type FrameTile } from '@/routes/run'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyTiles, openTab, pickTab, splitTile, tileList, type TileLayout, type ViewContext } from '@/store/tiles'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { hintOn } from '@/test/tooltip'
import { atViewport } from '@/test/viewport'

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
const seeds = [
  run(enhanced),
  run({ ...enhanced, id: 'run_2', task: second }),
  run({ ...enhanced, id: 'run_3', task: 'answer the schema question', status: 'needs-attention', state: 'needs-you', reason: 'needs input: Which schema?' }),
  run({ id: 'run_4', task: 'tidy the readme', status: 'merged' }),
]

/** What the area last told each frame about its tile, by session and view. */
const frames = new Map<string, FrameTile & { params: Record<string, string> }>()

function Recorded(props: React.ComponentProps<typeof RunFrame>) {
  frames.set(`${props.run.id}:${props.params.view}`, { ...props.tile!, params: props.params })
  return <RunFrame {...props} />
}

function Page() {
  const route = useStore((s) => s.route)
  return route.name === 'run' ? <main id="main"><TileArea params={route.params} Frame={Recorded} /></main> : <p>the board</p>
}

function show(runId = 'run_1', view?: RunView) {
  act(() => useStore.getState().navigate('run', view ? { runId, view } : { runId }))
  return render(<Page />)
}

/** Lays every element out at this size, which jsdom never does. */
function sized(width: number, height: number) {
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockReturnValue({ width, height, left: 0, top: 0, right: width, bottom: height, x: 0, y: 0, toJSON: () => ({}) })
}

const arrange = (change: (layout: TileLayout, ctx: ViewContext) => TileLayout) => act(() => changeTiles(change, true))
const beside = (runId: string, view?: RunView) =>
  arrange((layout, ctx) => {
    const split = splitTile(layout, layout.focused, 'right')
    return pickTab(split, split.focused, runId, ctx, view)
  })
const state = () => useStore.getState()
const tiles = () => screen.getAllByRole('region', { name: /, tile \d+ of \d+$/ })
const tile = (n: number) => within(tiles()[n - 1])
const names = () => tiles().map((node) => node.getAttribute('aria-label'))
const strip = (n: number) => within(tile(n).getByRole('tablist', { name: 'Sessions in this tile' }))
const tabs = () => tileList(state().tiles.root).map((node) => node.tabs.map((tab) => tab.runId))
const press = (key: string, init: KeyboardEventInit = {}) => fireEvent.keyDown(window, { key, ...init })
/** A session that mounts inside an event renders a microtask later under jsdom. */
const settled = () => act(async () => {})

beforeEach(() => {
  frames.clear()
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
    savedTiles: {},
    roomMessages: {},
    roomStatus: {},
    browserRequests: {},
    agentList: null,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('one session', () => {
  it('shows one tile with no outline that reads as a single session', () => {
    show()
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])
    expect(tiles()[0].className).toContain('after:border-transparent')
    expect(screen.getByRole('heading', { level: 1, name: first })).toBeDefined()
    expect(screen.getByRole('tablist', { name: 'Session views' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Maximize' })).toBeNull()
    expect(screen.queryByRole('separator')).toBeNull()
    expect(frames.get('run_1:session')).toMatchObject({ focused: true, controls: true, alone: true, terminalHost: true })
  })

  it('shows a phone the single session with no tile control', () => {
    atViewport(390, { pointer: 'coarse' })
    const View = lookupRoute('run')!
    act(() => state().navigate('run', { runId: 'run_1' }))
    render(<View params={state().route.params} />)
    expect(screen.getByRole('button', { name: 'Back' })).toBeDefined()
    expect(document.querySelector('[data-tile]')).toBeNull()
    expect(screen.queryByRole('button', { name: /Split|Add a tab|Maximize/ })).toBeNull()
  })

  it('leaves for the board from the X on the only tab and keeps the layout', () => {
    show()
    const before = state().tiles
    fireEvent.click(screen.getByRole('button', { name: `Close ${first}` }))
    expect(state().route.name).toBe('board')
    expect(state().tiles).toBe(before)
    expect(state().runs.run_1).toBeDefined()
  })
})

describe('splitting', () => {
  it('opens a focused empty tile with the picker on Split right and Split down', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, 'New tile, tile 2 of 2'])
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    expect(tiles()[1].className).toContain('after:border-accent')
    expect(tiles()[0].className).toContain('after:border-transparent')
    expect(document.activeElement).toBe(tile(2).getByRole('combobox', { name: 'Find a session or view' }))
    expect(state().route.params.runId).toBe('run_1')

    fireEvent.click(tile(2).getByRole('button', { name: 'Close tile' }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])
    fireEvent.click(screen.getByRole('button', { name: 'Split down' }))
    expect(state().tiles.root).toMatchObject({ dir: 'column' })
    expect(tile(2).getByRole('heading', { name: 'Open in this tile' })).toBeDefined()
  })

  it('adds a tab instead when the 420 by 400 floor leaves no room', async () => {
    sized(800, 700)
    show()
    const button = screen.getByRole('button', { name: 'Split right' })
    expect(await hintOn(button)).toBe('No room to split: adds a tab')
    fireEvent.click(button)
    expect(tiles()).toHaveLength(1)
    fireEvent.click(within(await screen.findByRole('dialog')).getByRole('option', { name: new RegExp(second) }))
    expect(tabs()).toEqual([['run_1', 'run_2']])
    expect(state().route.params.runId).toBe('run_2')
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('shows a layout that does not fit as the focused tile, with the rest in the pill and nothing to restore', () => {
    sized(800, 700)
    show()
    beside('run_2')
    expect(names()).toEqual([`${second}, Chat, tile 1 of 1`])
    expect(tile(1).getByRole('button', { name: new RegExp(first) })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Restore tiles' })).toBeNull()
    expect(state().tiles.maximized).toBe(false)
  })
})

describe('the picker', () => {
  it('leads with the other views, then sessions by state, then the ones on screen, then New session', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    expect([...tiles()[1].querySelectorAll('[cmdk-group-heading]')].map((node) => node.textContent)).toEqual([
      `Other views of ${first}`,
      'Needs you',
      'Working',
      'Finished',
      'Already on screen',
    ])
    expect(tile(2).getAllByRole('option').map((node) => node.textContent?.replace(/\d+\w+$/, ''))).toEqual([
      'Trace', 'Terminal', 'Changes', 'Files', 'Browser', 'answer the schema question', second, 'tidy the readme', first,
    ])
    expect(tile(2).getByRole('button', { name: 'New session' })).toBeDefined()
  })

  it('fills the empty tile with the picked session and pushes one history entry', () => {
    const stop = bindRouteToUrl(useStore)
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    const entries = window.history.length
    fireEvent.click(tile(2).getByRole('option', { name: new RegExp(second) }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${second}, Chat, tile 2 of 2`])
    expect(state().route.params.runId).toBe('run_2')
    expect(window.history.length).toBe(entries + 1)
    stop()
  })

  it('opens another view of the same session, and both tabs then name their view', () => {
    show()
    expect(strip(1).getByRole('tab').textContent).not.toContain('Chat')
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    fireEvent.click(tile(2).getByRole('option', { name: 'Changes' }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Changes, tile 2 of 2`])
    expect(state().route.params).toMatchObject({ runId: 'run_1', view: 'changes' })
    expect(strip(1).getByRole('tab').textContent).toContain('Chat')
    expect(strip(2).getByRole('tab').textContent).toContain('Changes')
    expect(frames.get('run_1:session')).toMatchObject({ focused: false, alone: false, elsewhere: ['changes'], params: { runId: 'run_1', view: 'session' } })
  })

  it('goes to the tile that already shows an arrowed view and closes the empty tile', () => {
    show()
    beside('run_1', 'changes')
    fireEvent.click(tile(2).getByRole('button', { name: 'Split down' }))
    expect(tiles()).toHaveLength(3)
    const chat = tile(3).getByRole('option', { name: 'Chat' })
    expect(chat.querySelector('svg.lucide-arrow-up-right')).not.toBeNull()
    fireEvent.pointerMove(chat)
    expect(tiles()[0].className).toContain('after:border-dashed')
    fireEvent.click(chat)
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Changes, tile 2 of 2`])
    expect(state().tiles.focused).toBe(tiles()[0].dataset.tile)
    expect(tiles()[0].className).not.toContain('after:border-dashed')
  })

  it('adds a session that is already on screen on its first free view', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    fireEvent.click(tile(2).getByRole('option', { name: new RegExp(first) }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Trace, tile 2 of 2`])
  })
})

describe('tabs', () => {
  it('activates a background tab and closes one without ending its session', async () => {
    show()
    arrange((layout, ctx) => openTab(layout, layout.focused, 'run_2', ctx))
    expect(strip(1).getAllByRole('tab').map((tab) => tab.getAttribute('aria-selected'))).toEqual(['false', 'true'])
    fireEvent.mouseDown(strip(1).getByRole('tab', { name: new RegExp(first) }))
    await settled()
    expect(state().route.params.runId).toBe('run_1')
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])

    fireEvent.click(tile(1).getByRole('button', { name: `Close ${second}` }))
    expect(tabs()).toEqual([['run_1']])
    expect(state().runs.run_2).toBeDefined()
    expect(state().route.name).toBe('run')
  })

  it('closes the active tab on Delete and hands the next one the route', async () => {
    show()
    arrange((layout, ctx) => openTab(layout, layout.focused, 'run_2', ctx))
    await settled()
    fireEvent.keyDown(strip(1).getByRole('tab', { name: new RegExp(second) }), { key: 'Delete' })
    expect(tabs()).toEqual([['run_1']])
    expect(state().route.params.runId).toBe('run_1')
  })

  it('removes a tile with its last tab, and its sibling takes the focus', () => {
    show()
    beside('run_2')
    fireEvent.click(tile(2).getByRole('button', { name: `Close ${second}` }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])
    expect(state().route.params.runId).toBe('run_1')
  })

  it('moves keyboard focus to the neighbour when Delete closes a tile, and keeps the open subagent on Maximize', async () => {
    show()
    beside('run_2')
    fireEvent.keyDown(strip(2).getByRole('tab'), { key: 'Delete' })
    await waitFor(() => expect(document.activeElement).toBe(strip(1).getByRole('tab')))

    beside('run_2')
    act(() => state().navigate('run', { runId: 'run_2', agent: 'agent_1' }))
    fireEvent.click(tile(2).getByRole('button', { name: 'Maximize' }))
    expect(state().route.params).toEqual({ runId: 'run_2', agent: 'agent_1' })
  })

  it('puts the Needs you badge on a background tab and the lock on a protected session', () => {
    act(() => state().upsertRun({ ...seeds[1], protected: true }))
    show('run_3')
    expect(strip(1).queryByText('Needs you', { selector: '[data-slot=badge]' })).toBeNull()
    arrange((layout, ctx) => openTab(layout, layout.focused, 'run_2', ctx))
    expect(within(strip(1).getByRole('tab', { name: /answer the schema question/ })).getByText('Needs you', { selector: '[data-slot=badge]' })).toBeDefined()
    expect(within(strip(1).getByRole('tab', { name: new RegExp(second) })).getByRole('img', { name: /^Protected/ })).toBeDefined()
  })
})

describe('a session in two tiles', () => {
  function chatAndChanges() {
    show()
    beside('run_1', 'changes')
    fireEvent.pointerDown(tiles()[0])
  }

  it('marks a view shown in another tile with an arrow that says where, and jumps there', async () => {
    chatAndChanges()
    const changes = within(tile(1).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name: 'Changes' })
    expect(changes.querySelector('svg.lucide-arrow-up-right')).not.toBeNull()
    expect(await hintOn(changes)).toBe('On screen in the tile at right')
    expect(tiles()[1].className).toContain('after:border-dashed')
    fireEvent.blur(changes)
    expect(tiles()[1].className).not.toContain('after:border-dashed')

    fireEvent.mouseDown(changes)
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    expect(tabs()).toEqual([['run_1'], ['run_1']])
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Changes, tile 2 of 2`])
    const chat = within(tile(2).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name: 'Chat' })
    expect(await hintOn(chat)).toBe('On screen in the tile at left')
  })

  it('stays in the other tile when the click on an arrowed view then focuses the tab it pressed', async () => {
    chatAndChanges()
    await userEvent.click(within(tile(1).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name: 'Changes' }))
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    expect(state().route.params).toMatchObject({ runId: 'run_1', view: 'changes' })
    expect(document.activeElement).toBe(tile(2).getByRole('heading', { level: 1, name: first }))
  })

  it('stays in the other tile when the picker that jumped there closes and hands focus back', async () => {
    chatAndChanges()
    await userEvent.click(tile(1).getByRole('button', { name: 'Add a tab' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('option', { name: 'Changes' }))
    await waitFor(() => expect(document.activeElement).toBe(tile(2).getByRole('heading', { level: 1, name: first })))
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    expect(state().route.params).toMatchObject({ runId: 'run_1', view: 'changes' })
    expect(tabs()).toEqual([['run_1'], ['run_1']])
  })

  it('follows the Tab key into another tile', async () => {
    chatAndChanges()
    tile(1).getAllByRole('button').at(-1)!.focus()
    while (!tiles()[1].contains(document.activeElement)) await userEvent.tab()
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
  })

  it('skips a view another tile holds on ] and [', () => {
    chatAndChanges()
    act(() => state().navigate('run', { runId: 'run_1', view: 'terminal' }))
    press(']')
    expect(state().route.params.view).toBe('files')
    press('[')
    expect(state().route.params.view).toBe('terminal')
  })

  it('gives the controls to the focused tile, then to the sibling when that tile closes', () => {
    chatAndChanges()
    expect(frames.get('run_1:session')).toMatchObject({ focused: true, controls: true, terminalHost: true })
    expect(frames.get('run_1:changes')).toMatchObject({ focused: false, controls: false, terminalHost: false })
    fireEvent.pointerDown(tiles()[1])
    expect(frames.get('run_1:session')).toMatchObject({ focused: false, controls: false, terminalHost: true })
    expect(frames.get('run_1:changes')).toMatchObject({ focused: true, controls: true })
    fireEvent.click(tile(2).getByRole('button', { name: `Close ${first}` }))
    expect(frames.get('run_1:session')).toMatchObject({ focused: true, controls: true, alone: true, elsewhere: [] })
  })

  it('keeps a hidden view mounted in its tile until another tile shows it', () => {
    show('run_1', 'changes')
    act(() => state().navigate('run', { runId: 'run_1', view: 'session' }))
    expect(document.querySelectorAll('[role=group][aria-label=Changes]')).toHaveLength(1)
    beside('run_1', 'changes')
    expect(document.querySelectorAll('[role=group][aria-label=Changes]')).toHaveLength(1)
    expect(tiles()[1].querySelector('[role=group][aria-label=Changes]')).not.toBeNull()
  })
})

describe('focus', () => {
  it('lets only the focused tile answer ] [ c and Mod+.', async () => {
    show()
    beside('run_2')
    await settled()
    press(']')
    expect(state().route.params).toMatchObject({ runId: 'run_2', view: 'trace' })
    expect(names()[0]).toBe(`${first}, Chat, tile 1 of 2`)
    press('[')
    expect(state().route.params.view).toBe('session')

    press(']')
    await settled()
    press('c')
    expect(state().route.params).toMatchObject({ runId: 'run_2', view: 'session' })
    expect(names()[0]).toBe(`${first}, Chat, tile 1 of 2`)

    await settled()
    press('.', { ctrlKey: true })
    expect(await screen.findAllByRole('dialog', { name: 'Session details' })).toHaveLength(1)
  })

  it('follows a click into another tile, replacing the history entry', () => {
    const stop = bindRouteToUrl(useStore)
    show()
    beside('run_2')
    const entries = window.history.length
    fireEvent.pointerDown(tiles()[0])
    expect(state().tiles.focused).toBe(tiles()[0].dataset.tile)
    expect(state().route.params.runId).toBe('run_1')
    expect(window.location.search).toContain('run=run_1')
    expect(window.history.length).toBe(entries)
    stop()
  })

  it('shows a deep link in the focused tile, and Back refocuses without rebuilding a tile', async () => {
    const stop = bindRouteToUrl(useStore)
    show()
    act(() => openToSide('run_2'))
    expect(tabs()).toEqual([['run_1'], ['run_2']])
    fireEvent.pointerDown(tiles()[0])
    fireEvent.pointerDown(tiles()[1])
    act(() => window.history.back())
    await waitFor(() => expect(state().route.params.runId).toBe('run_1'))
    expect(state().tiles.focused).toBe(tiles()[0].dataset.tile)
    expect(tabs()).toEqual([['run_1'], ['run_2']])

    act(() => useStore.setState({ route: { name: 'run', params: { runId: 'run_4', view: 'changes' } } }))
    expect(names()).toEqual(['tidy the readme, Changes, tile 1 of 2', `${second}, Chat, tile 2 of 2`])
    stop()
  })
})

describe('seams', () => {
  it('is a separator that resizes from the keyboard and evens on a double click', () => {
    sized(1600, 900)
    show()
    beside('run_2')
    const seam = screen.getByRole('separator', { name: 'Resize the tiles' })
    expect(seam.getAttribute('aria-orientation')).toBe('vertical')
    expect([seam.getAttribute('aria-valuemin'), seam.getAttribute('aria-valuenow'), seam.getAttribute('aria-valuemax')]).toEqual(['420', '800', '1180'])
    fireEvent.keyDown(seam, { key: 'ArrowRight' })
    expect(state().tiles.root).toMatchObject({ ratio: 0.51 })
    fireEvent.keyDown(seam, { key: 'Home' })
    expect(screen.getByRole('separator').getAttribute('aria-valuenow')).toBe('420')
    fireEvent.pointerDown(seam, { clientX: 420 })
    fireEvent.pointerMove(window, { clientX: 620 })
    fireEvent.pointerUp(window)
    expect(screen.getByRole('separator').getAttribute('aria-valuenow')).toBe('620')
    fireEvent.doubleClick(seam)
    expect(state().tiles.root).toMatchObject({ ratio: 0.5 })
    expect(tiles()[0].style.width).toBe('50%')
  })
})

describe('maximize', () => {
  it('fills the area, parks the rest in the pill, swaps on a chip and restores', async () => {
    show()
    beside('run_2')
    fireEvent.click(tile(2).getByRole('button', { name: 'Maximize' }))
    expect(names()).toEqual([`${second}, Chat, tile 1 of 1`])
    expect(tiles()[0].className).toContain('after:border-transparent')
    expect(tiles()[0].style.width).toBe('100%')
    expect(frames.get('run_2:session')).toMatchObject({ alone: true })

    fireEvent.click(tile(1).getByRole('button', { name: new RegExp(first) }))
    await settled()
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])
    expect(state().route.params.runId).toBe('run_1')
    expect(tile(1).getByRole('button', { name: new RegExp(second) })).toBeDefined()

    fireEvent.click(screen.getByRole('button', { name: 'Restore tiles' }))
    expect(tiles()).toHaveLength(2)
  })

  it('spends Escape on the subagent, then on restoring, then on leaving for the board', async () => {
    show()
    beside('run_2')
    fireEvent.doubleClick(strip(2).getByRole('tab'))
    expect(state().tiles.maximized).toBe(true)
    act(() => state().navigate('run', { runId: 'run_2', view: 'session', agent: 'agent_1' }))
    await settled()
    press('Escape')
    expect(state().route.params).toEqual({ runId: 'run_2', view: 'session' })
    expect(state().tiles.maximized).toBe(true)
    press('Escape')
    expect(state().tiles.maximized).toBe(false)
    expect(state().route.name).toBe('run')
    press('Escape')
    expect(state().route.name).toBe('board')
  })
})

describe('the layout', () => {
  it('comes back after a reload for the member who made it', async () => {
    act(() => state().setIdentityKey('alice-key'))
    show()
    beside('run_2')
    const saved = state().tiles
    const stored = window.localStorage.getItem('aether.ui')!
    act(() => useStore.setState({ identityKey: null, tiles: emptyTiles(), savedTiles: {} }))
    window.localStorage.setItem('aether.ui', stored)
    await act(() => useStore.persist.rehydrate())
    act(() => state().setIdentityKey('alice-key'))
    expect(state().tiles).toMatchObject({ focused: saved.focused, root: { id: saved.root.id, ratio: 0.5 } })
    expect(tabs()).toEqual([['run_1'], ['run_2']])
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${second}, Chat, tile 2 of 2`])
  })
})

describe('the sidebar', () => {
  const row = (title: string) => screen.getByRole('button', { name: new RegExp(` · ${title} · `) }).closest<HTMLElement>('[data-slot=list-row]')!
  const marked = () => [...document.querySelectorAll('[data-slot=list-row]')].filter((node) => node.className.includes('before:bg-accent'))

  it('opens a session to the side and marks each session on screen once', () => {
    act(() => state().navigate('run', { runId: 'run_1' }))
    render(<><SidebarRuns /><Page /></>)
    expect(marked()).toEqual([row(first)])
    fireEvent.click(within(row(second)).getByRole('button', { name: 'Open to the side' }))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${second}, Chat, tile 2 of 2`])
    expect(state().route.params.runId).toBe('run_2')
    expect(row(second).getAttribute('data-selected')).toBe('true')

    fireEvent.click(within(row(first)).getByRole('button', { name: 'Open to the side' }))
    expect(tiles()).toHaveLength(3)
    expect(marked()).toEqual([row(first), row(second)])
  })

  it('marks only the session it shows when the layout does not fit', () => {
    sized(800, 700)
    act(() => state().navigate('run', { runId: 'run_1' }))
    render(<><SidebarRuns /><Page /></>)
    beside('run_2')
    expect(marked()).toEqual([row(second)])
  })

  it('offers no Open to the side on a touch screen', () => {
    atViewport(1024, { pointer: 'coarse' })
    render(<SidebarRuns />)
    expect(screen.queryByRole('button', { name: 'Open to the side' })).toBeNull()
  })
})

describe('accessibility', () => {
  it('makes the strip a tab list whose selected tab controls the tile body, labels each tile and keeps one h1', () => {
    show()
    beside('run_2')
    for (const node of tiles()) {
      const selected = within(within(node).getByRole('tablist', { name: 'Sessions in this tile' })).getByRole('tab', { selected: true })
      const panel = document.getElementById(selected.getAttribute('aria-controls')!)!
      expect(panel.getAttribute('role')).toBe('tabpanel')
      expect(panel.parentElement).toBe(node)
      expect(within(panel).getByRole('tablist', { name: 'Session views' })).toBeDefined()
    }
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${second}, Chat, tile 2 of 2`])
    const headings = screen.getAllByRole('heading', { level: 1 })
    expect(headings).toHaveLength(1)
    expect(tiles()[1].contains(headings[0])).toBe(true)
    expect(tile(1).getByRole('heading', { level: 2, name: first })).toBeDefined()
  })

  it('says what a tile control did', () => {
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    expect(screen.getByText('Split right', { selector: '[role=status]' })).toBeDefined()
    fireEvent.click(tile(2).getByRole('button', { name: 'Close tile' }))
    expect(screen.getByText('Tile closed', { selector: '[role=status]' })).toBeDefined()
  })
})
