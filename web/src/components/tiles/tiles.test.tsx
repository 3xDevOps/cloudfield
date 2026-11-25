import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useContext } from 'react'
import userEvent from '@testing-library/user-event'
import type * as React from 'react'
import { CenterView } from '@/components/shell/center-view'
import { SidebarRuns } from '@/components/shell/sidebar-runs'
import { changeTiles, openToSide } from '@/components/tiles/area'
import { TileArea } from '@/components/tiles/tile-area'
import type * as xtermHostModule from '@/components/xterm-host'
import type * as apiModule from '@/lib/api'
import { bindRouteToUrl } from '@/lib/url-state'
import { lookupRoute } from '@/routes/registry'
import { RunFrame, type FrameTile } from '@/routes/run'
import { RunAgentContext } from '@/routes/run/agent-context'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { closeTile, emptyTiles, openTab, pickTab, splitTile, tileList, type TileLayout, type ViewContext } from '@/store/tiles'
import { ScriptedSession } from '@/test/acp-stream'
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
let frameRenders = 0

function Recorded(props: React.ComponentProps<typeof RunFrame>) {
  // Read as the real frame reads it, so a session rendered again counts here.
  useContext(RunAgentContext)
  frameRenders += 1
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
    acpSessions: {},
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

  it('leaves for the board from the X on the only tab, in place of the history entry, and keeps the layout', () => {
    const stop = bindRouteToUrl(useStore)
    show()
    const before = state().tiles
    const entries = window.history.length
    fireEvent.click(screen.getByRole('button', { name: `Close ${first}` }))
    stop()
    expect(state().route.name).toBe('board')
    expect(window.history.length).toBe(entries)
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

  it('puts the Needs you badge on a background tab and the lock on a protected session, and names a parked tile by the tab that needs you', async () => {
    act(() => state().upsertRun({ ...seeds[1], protected: true }))
    show('run_3')
    expect(strip(1).queryByText('Needs you', { selector: '[data-slot=badge]' })).toBeNull()
    arrange((layout, ctx) => openTab(layout, layout.focused, 'run_2', ctx))
    expect(within(strip(1).getByRole('tab', { name: /answer the schema question/ })).getByText('Needs you', { selector: '[data-slot=badge]' })).toBeDefined()
    expect(within(strip(1).getByRole('tab', { name: new RegExp(second) })).getByRole('img', { name: /^Protected/ })).toBeDefined()
    beside('run_1')
    await settled()
    fireEvent.click(tile(2).getByRole('button', { name: 'Maximize' }))
    fireEvent.click(tile(1).getByRole('button', { name: /answer the schema question/ }))
    expect(state().route.params.runId).toBe('run_3')
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

    fireEvent.click(changes)
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    expect(tabs()).toEqual([['run_1'], ['run_1']])
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Changes, tile 2 of 2`])
    const chat = within(tile(2).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name: 'Chat' })
    expect(await hintOn(chat)).toBe('On screen in the tile at left')
  })

  it('draws the lines under the header in each of its tiles, so focus moves no pane', () => {
    act(() => state().upsertRun(run({ ...enhanced, id: 'run_child', task: 'review the diff', parent_run_id: 'run_1' })))
    show('run_child')
    beside('run_child', 'changes')
    const lineage = () => tiles().map((node) => within(node).queryAllByRole('navigation', { name: 'Session lineage' }).length)
    expect(lineage()).toEqual([1, 1])
    fireEvent.pointerDown(tiles()[0])
    expect(lineage()).toEqual([1, 1])
  })

  it('closes the tile of a view the session stops offering as soon as it does', async () => {
    show()
    beside('run_1', 'browser')
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${first}, Browser, tile 2 of 2`])
    act(() => state().upsertRun({ ...state().runs.run_1, status: 'merged' }))
    await settled()
    expect(names()).toEqual([`${first}, Chat, tile 1 of 1`])
  })

  it('renders no frame again for an event from a session in no tile, and only its own for one in a tile or a background tab', async () => {
    show()
    beside('run_2')
    arrange((layout, ctx) => openTab(layout, layout.focused, 'run_4', ctx))
    fireEvent.pointerDown(tiles()[0])
    await settled()
    const before = frameRenders
    const heard = vi.fn()
    const stop = useStore.subscribe(heard)
    act(() => state().upsertRun({ ...state().runs.run_3, reason: 'needs input: Which table?' }))
    act(() => state().upsertRun({ ...state().runs.run_2, task: 'speed up the search' }))
    stop()
    expect(frameRenders).toBe(before)
    expect(heard).toHaveBeenCalledTimes(2)
    frames.clear()
    act(() => state().upsertRun({ ...state().runs.run_1, task: 'rewrite checkout' }))
    expect([...frames.keys()]).toEqual(['run_1:session'])
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

describe('the header row', () => {
  const header = (n: number) => tiles()[n - 1].querySelector('header')!
  const gold = () => [...document.querySelectorAll('header [data-slot=button]')].filter((button) => button.className.includes('bg-action'))
  const asking = { ...enhanced, status: 'needs-attention', state: 'needs-you' } as const

  it('keeps the strip and the view row where they are when focus moves between tiles', async () => {
    show()
    beside('run_1', 'changes')
    beside('run_2')
    await settled()
    const rows = () => tiles().flatMap((node) => {
      const [strip, body] = node.children
      const row = body.querySelector(':scope > div > div > header:first-child')!
      expect(strip.className).toContain('h-8')
      expect(row.className).toContain('h-8')
      return [strip, row]
    })
    const before = rows()
    for (const n of [0, 1, 2]) {
      fireEvent.pointerDown(tiles()[n])
      expect(state().tiles.focused).toBe(tiles()[n].dataset.tile)
      rows().forEach((row, index) => expect(row).toBe(before[index]))
    }
  })

  it('shows a session its controls once, its other tile the state alone, and one gold action on screen', async () => {
    act(() => state().upsertRun(run({ ...asking, id: 'run_5', task: 'pick a port', reason: 'needs input: Which port?' })))
    show('run_3')
    beside('run_3', 'changes')
    beside('run_5')
    await settled()
    const toolbars = () => tiles().map((node) => within(node).queryAllByRole('toolbar', { name: 'Session actions' }).length)
    expect(toolbars()).toEqual([1, 0, 1])
    expect(within(header(2)).queryAllByRole('button')).toEqual([])
    expect(within(header(2)).getByRole('status').textContent).toBe('Needs you')
    expect(within(header(1)).getByRole('button', { name: 'Reply' }).className).not.toContain('bg-action')
    expect(gold()).toEqual([within(header(3)).getByRole('button', { name: 'Reply' })])

    fireEvent.click(tile(3).getByRole('button', { name: 'Maximize' }))
    expect(frames.get('run_3:session')).toMatchObject({ controls: true, arrows: { changes: { where: 'On screen in the tile at right' } } })
    expect(frames.get('run_3:changes')).toMatchObject({ controls: false, chatOnScreen: true })
    fireEvent.click(screen.getByRole('button', { name: 'Restore tiles' }))
    fireEvent.pointerDown(tiles()[1])
    expect(toolbars()).toEqual([0, 1, 1])
    expect(gold()).toEqual([within(header(2)).getByRole('button', { name: 'Reply' })])
  })

  it('sizes the row from its own width: icons for the view names under 800px, and under 560px no reason and Session tree under More', async () => {
    const chat = () => within(tile(1).getByRole('tablist', { name: 'Session views' })).getByRole('tab', { name: 'Chat' })
    show('run_3')
    expect(chat().textContent).toBe('Chat')
    expect(header(1).textContent).toContain('Which schema?')
    expect(within(header(1)).getByRole('button', { name: /^Session tree: / })).toBeDefined()
    cleanup()

    sized(700, 800)
    show('run_3')
    await settled()
    expect(chat().textContent).toBe('')
    expect(await hintOn(chat())).toBe('Chat')
    expect(header(1).textContent).toContain('Which schema?')
    expect(within(header(1)).getByRole('button', { name: /^Session tree: / })).toBeDefined()
    cleanup()

    sized(500, 800)
    show('run_3')
    await settled()
    expect(chat().textContent).toBe('')
    expect(within(header(1)).getByRole('status').textContent).toBe('Needs you')
    expect(header(1).textContent).not.toContain('Which schema?')
    expect(within(header(1)).queryByRole('button', { name: /^Session tree: / })).toBeNull()
    await userEvent.click(within(header(1)).getByRole('button', { name: 'More' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Session tree' }))
    expect(await screen.findByRole('dialog', { name: 'Session tree' })).toBeDefined()
  })

  it('docks Details beside a lone or maximized tile and opens it as a sheet between tiles', async () => {
    sized(1600, 900)
    useStore.setState({ detailsOpen: true })
    show()
    expect(screen.getByRole('complementary', { name: 'Session details' })).toBeDefined()
    beside('run_2')
    await settled()
    expect(screen.queryByRole('complementary', { name: 'Session details' })).toBeNull()
    await userEvent.click(tile(2).getByRole('button', { name: 'Show details' }))
    expect(await screen.findByRole('dialog', { name: 'Session details' })).toBeDefined()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Session details' })).toBeNull())
    fireEvent.click(tile(2).getByRole('button', { name: 'Maximize' }))
    expect(screen.getByRole('complementary', { name: 'Session details' })).toBeDefined()
    expect(document.querySelectorAll('#run-details')).toHaveLength(1)
    expect(frames.get('run_1:session')).toMatchObject({ alone: false })
  })

  it('offers Open sidebar on the first strip while the sidebar is hidden', () => {
    useStore.setState({ sidebarCollapsed: true })
    show()
    beside('run_2')
    expect(tile(2).queryByRole('button', { name: 'Open sidebar' })).toBeNull()
    fireEvent.click(tile(1).getByRole('button', { name: 'Open sidebar' }))
    expect(state().sidebarCollapsed).toBe(false)
    expect(screen.queryByRole('button', { name: 'Open sidebar' })).toBeNull()
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
    expect(screen.getByRole('button', { name: `Copy branch ${state().runs.run_2.branch}` })).toBeDefined()
  })

  it('moves the keyboard to the heading of a session opened from another page', async () => {
    render(<><button>outside</button><main id="main" tabIndex={-1}><CenterView /></main></>)
    screen.getByRole('button', { name: 'outside' }).focus()
    act(() => state().navigate('run', { runId: 'run_1' }))
    await settled()
    expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1, name: first }))
  })

  it('adds a history entry only for a change that opens a tab', () => {
    const stop = bindRouteToUrl(useStore)
    show()
    beside('run_2')
    const entries = window.history.length
    arrange((layout, ctx) => pickTab(layout, layout.focused, 'run_1', ctx, 'session'))
    expect(state().route.params.runId).toBe('run_1')
    expect(window.history.length).toBe(entries)
    stop()
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

describe('the composer', () => {
  const fields = (n: number) => tile(n).queryAllByRole<HTMLTextAreaElement>('combobox', { name: 'Message the agent' })
  /** Opens the session's stream, which is what lets its composer take a message. */
  async function live(runId: string, n: number) {
    new ScriptedSession(StubSocket.opened.find((socket) => socket.url.includes(`/ws/acp/${runId}`))!).open()
    await waitFor(() => expect(fields(n)).toHaveLength(1))
  }

  it('is whole in the focused Chat tile and one line in the other, and keeps the draft between the two', async () => {
    show()
    beside('run_2')
    await settled()
    await live('run_1', 1)
    await live('run_2', 2)
    const [field] = fields(2)
    expect(field.rows).toBe(2)
    expect(tile(2).getByRole('button', { name: 'Attach files' })).toBeDefined()
    expect(fields(1)[0].rows).toBe(1)
    expect(tile(1).queryByRole('button', { name: 'Attach files' })).toBeNull()
    expect(tile(1).getByRole('button', { name: 'Send' })).toBeDefined()

    fireEvent.change(field, { target: { value: 'ship it\nthen rest' } })
    fireEvent.pointerDown(tiles()[0])
    expect(fields(2)).toEqual([field])
    expect(field.rows).toBe(1)
    expect(tile(2).getByText('+1 line')).toBeDefined()
    expect(field.value).toBe('ship it\nthen rest')
    expect(fields(1)[0].rows).toBe(2)
    fireEvent.pointerDown(tiles()[1])
    expect(field.rows).toBe(2)
    expect(field.value).toBe('ship it\nthen rest')
    fireEvent.change(field, { target: { value: '' } })
  })

  it('docks one line under another view that c opens, and leaves the session with one field once its Chat is on screen', async () => {
    show('run_1', 'changes')
    await settled()
    expect(tile(1).queryByText('Connecting to the agent…')).toBeNull()
    await live('run_1', 1)
    const [docked] = fields(1)
    expect(docked.rows).toBe(1)
    press('c')
    expect(document.activeElement).toBe(docked)
    expect(docked.rows).toBe(2)
    expect(state().route.params.view).toBe('changes')

    beside('run_2')
    await settled()
    expect(docked.rows).toBe(1)
    fireEvent.pointerDown(tiles()[0])
    expect(fields(1)).toEqual([docked])
    expect(docked.rows).toBe(1)
    arrange((layout) => closeTile(layout, tiles()[1].dataset.tile!))
    await settled()

    beside('run_1', 'session')
    await settled()
    expect(fields(1)).toEqual([])
    expect(fields(2)).toHaveLength(1)
    fireEvent.pointerDown(tiles()[0])
    expect(fields(2)[0].rows).toBe(1)
    press('c')
    expect(state().route.params).toMatchObject({ runId: 'run_1', view: 'session' })
    await waitFor(() => expect(document.activeElement).toBe(fields(2)[0]))
    expect(fields(2)[0].rows).toBe(2)
  })

  it('opens Chat for the header Reply even where a docked field could take the answer', async () => {
    show('run_3', 'changes')
    await settled()
    await live('run_3', 1)
    fireEvent.click(tile(1).getByRole('button', { name: 'Reply' }))
    expect(state().route.params).toEqual({ runId: 'run_3', view: 'session' })
  })

  it('focuses the composer for Reply when the session\'s Chat is in a parked tile', async () => {
    show('run_3', 'changes')
    beside('run_3', 'session')
    await settled()
    await live('run_3', 2)
    fireEvent.click(tile(1).getByRole('button', { name: 'Maximize' }))
    fireEvent.click(tile(1).getByRole('button', { name: 'Reply' }))
    expect(names()).toEqual(['answer the schema question, Chat, tile 1 of 1'])
    await waitFor(() => expect(document.activeElement).toBe(fields(1)[0]))
  })

  it('docks no field under a Browser or a Standard session', async () => {
    show('run_2', 'browser')
    await settled()
    new ScriptedSession(StubSocket.last()).open()
    expect(fields(1)).toEqual([])
    act(() => state().upsertRun(run({ id: 'run_5', task: 'bump the lockfile' })))
    act(() => state().navigate('run', { runId: 'run_5', view: 'changes' }))
    await settled()
    expect(names()).toEqual(['bump the lockfile, Changes, tile 1 of 1'])
    expect(screen.queryByRole('textbox', { name: 'Message the agent' })).toBeNull()
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

describe('motion', () => {
  it('glides the tile that stays from its old rectangle with its body already at the final width, the new one in from the edge, and never on a seam move', async () => {
    sized(1600, 900)
    show()
    const animate = vi.spyOn(Element.prototype, 'animate')
    const stays = tiles()[0]
    const body = stays.lastElementChild as HTMLElement
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    expect(animate.mock.contexts).toEqual([stays, tiles()[1]])
    expect(animate.mock.calls[0][0]).toEqual([{ left: '0%', top: '0%', width: '100%', height: '100%', zIndex: 1 }, { left: '0%', top: '0%', width: '50%', height: '100%', zIndex: 1 }])
    expect(animate.mock.calls[1][0]).toEqual([{ left: '100%', top: '0%', width: '0%', height: '100%', zIndex: 1 }, { left: '50%', top: '0%', width: '50%', height: '100%', zIndex: 1 }])
    expect([body.style.width, body.style.height, stays.style.overflow]).toEqual(['1600px', '', 'clip'])
    await settled()
    expect([body.style.width, body.style.height, stays.style.overflow]).toEqual(['', '', ''])

    animate.mockClear()
    fireEvent.keyDown(screen.getByRole('separator', { name: 'Resize the tiles' }), { key: 'ArrowLeft' })
    expect(animate).not.toHaveBeenCalled()
  })

  it('restarts a glide cut short toward an explicit end rectangle', () => {
    sized(1600, 900)
    show()
    const animate = vi.spyOn(tiles()[0], 'animate')
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    fireEvent.click(tile(2).getByRole('button', { name: 'Close tile' }))
    expect((animate.mock.lastCall![0] as Keyframe[])[1]).toEqual({ left: '0%', top: '0%', width: '100%', height: '100%', zIndex: 1 })
  })

  it('moves nothing under reduced motion', () => {
    sized(1600, 900)
    vi.spyOn(window, 'matchMedia').mockImplementation((query) => ({ matches: query.includes('reduced-motion'), media: query, addEventListener() {}, removeEventListener() {} }) as unknown as MediaQueryList)
    show()
    const animate = vi.spyOn(Element.prototype, 'animate')
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    expect(tiles()).toHaveLength(2)
    expect(animate).not.toHaveBeenCalled()
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

describe('closing', () => {
  it('opens the board in place of the entry when the last tab closes beside an empty tile', () => {
    const stop = bindRouteToUrl(useStore)
    show()
    fireEvent.click(screen.getByRole('button', { name: 'Split right' }))
    const entries = window.history.length
    fireEvent.click(tile(1).getByRole('button', { name: `Close ${first}` }))
    expect(state().route.name).toBe('board')
    expect(window.history.length).toBe(entries)
    stop()
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

  it('opens the focused row to the side on Mod+Enter', () => {
    act(() => state().navigate('run', { runId: 'run_1' }))
    render(<><SidebarRuns /><Page /></>)
    fireEvent.keyDown(within(row(second)).getByRole('button', { name: new RegExp(` · ${second} · `) }), { key: 'Enter', ctrlKey: true })
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, `${second}, Chat, tile 2 of 2`])
  })

  it('offers no Open to the side on a touch screen', () => {
    atViewport(1024, { pointer: 'coarse' })
    render(<SidebarRuns />)
    expect(screen.queryByRole('button', { name: 'Open to the side' })).toBeNull()
    const before = state().tiles
    fireEvent.keyDown(screen.getByRole('button', { name: new RegExp(` · ${second} · `) }), { key: 'Enter', ctrlKey: true })
    expect(state().tiles).toBe(before)
  })
})

describe('keys', () => {
  const chord = (code: string, init: KeyboardEventInit = {}, target: Element | Window = window) =>
    fireEvent.keyDown(target, { key: code.replace('Digit', ''), code, ctrlKey: true, ...init })
  const said = (words: string | RegExp) => screen.queryByText(words, { selector: '[role=status]' })
  /** Something that owns the keys pressed inside it: a field, or a terminal by its class. */
  function holder(tag: string, className = '') {
    const node = document.createElement(tag)
    node.className = className
    document.body.append(node)
    onTestFinished(() => node.remove())
    return node
  }

  it('splits right and down on Mod+\\ from a field, and leaves the chord to a terminal', () => {
    show()
    chord('Backslash', {}, holder('div', 'xterm'))
    expect(tiles()).toHaveLength(1)
    chord('Backslash', {}, holder('textarea'))
    expect(names()).toEqual([`${first}, Chat, tile 1 of 2`, 'New tile, tile 2 of 2'])
    expect(said('Split right')).not.toBeNull()
    fireEvent.click(tile(2).getByRole('button', { name: 'Close tile' }))
    chord('Backslash', { shiftKey: true })
    expect(state().tiles.root).toMatchObject({ dir: 'column' })
  })

  it('opens the picker and says so when Mod+\\ finds no room', async () => {
    sized(800, 700)
    show()
    chord('Backslash')
    expect(await screen.findByRole('dialog')).toBeDefined()
    expect(said('No room to split: adds a tab')).not.toBeNull()
    expect(tiles()).toHaveLength(1)
  })

  it('focuses a tile by its place on Mod+digit, from a terminal too, and the next one on g t, with two tiles or more', async () => {
    show()
    chord('Digit1')
    press('g')
    press('t')
    expect(said(/focused$/)).toBeNull()

    beside('run_2')
    await settled()
    chord('Digit1', {}, holder('div', 'xterm'))
    expect(state().tiles.focused).toBe(tiles()[0].dataset.tile)
    expect(state().route.params.runId).toBe('run_1')
    expect(said(`${first} focused`)).not.toBeNull()
    press('g')
    press('t')
    expect(state().tiles.focused).toBe(tiles()[1].dataset.tile)
    press('g')
    press('t')
    expect(state().tiles.focused).toBe(tiles()[0].dataset.tile)
  })

  it('maximizes on x, closes the tab on w and opens the picker on s, never from a field or with nothing to act on', async () => {
    show()
    press('x')
    press('w')
    expect(state().tiles.maximized).toBe(false)
    expect(state().route.name).toBe('run')

    beside('run_2')
    await settled()
    fireEvent.keyDown(holder('textarea'), { key: 'x' })
    fireEvent.keyDown(holder('div', 'xterm'), { key: 'w' })
    expect(state().tiles.maximized).toBe(false)
    expect(tabs()).toEqual([['run_1'], ['run_2']])
    press('x')
    expect(state().tiles.maximized).toBe(true)
    expect(said('Tile maximized')).not.toBeNull()
    press('x')
    expect(state().tiles.maximized).toBe(false)
    expect(said('Tiles restored')).not.toBeNull()

    press('w')
    expect(tabs()).toEqual([['run_1']])
    expect(said('Tab closed')).not.toBeNull()
    press('s')
    expect(within(await screen.findByRole('dialog')).getByRole('heading', { name: 'Open in this tile' })).toBeDefined()
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
