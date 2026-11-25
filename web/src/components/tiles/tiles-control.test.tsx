import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { changeTiles } from '@/components/tiles/area'
import { TileArea } from '@/components/tiles/tile-area'
import type * as apiModule from '@/lib/api'
import * as fileDrag from '@/lib/file-drag'
import * as motion from '@/lib/motion'
import { RunFrame } from '@/routes/run'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { dropTab, emptyTiles, openTab, pickTab, splitTile, tileList } from '@/store/tiles'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

const mine = 'tidy the readme'
const theirs = 'speed up search'
const seeds = [run({ task: mine }), run({ id: 'run_2', task: theirs, member_id: bob.id })]

function Page() {
  const route = useStore((s) => s.route)
  return <main id="main"><TileArea params={route.params} Frame={RunFrame} /></main>
}

function show(runId: string, view?: RunView) {
  act(() => useStore.getState().navigate('run', view ? { runId, view } : { runId }))
  return render(<Page />)
}

const beside = (runId: string, view?: RunView) =>
  act(() => changeTiles((layout, ctx) => {
    const split = splitTile(layout, layout.focused, 'right')
    return pickTab(split, split.focused, runId, ctx, view)
  }, true))

const attaches = (runId: string) => StubSocket.opened.filter((socket) => socket.url.includes(runId))

function attached(runId: string, control: boolean) {
  const socket = attaches(runId).at(-1)!
  act(() => {
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, cols: 132, rows: 40, has_control: control, control_generation: control ? 1 : 0 }) })
  })
  return socket
}

const tile = (name: string) => screen.getByRole('region', { name: new RegExp(`^${name}, Terminal, tile`) })
const grid = (name: string) => tile(name).querySelector<HTMLElement>('[id^=run-agent-panel] .overflow-x-auto')!

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
    runs: Object.fromEntries(seeds.map((seed) => [seed.id, toRecord(seed)])),
    runViewMemory: {},
    tiles: emptyTiles(),
    savedTiles: {},
    roomStatus: {},
    roomStatusControl: {},
    browserRequests: {},
    agentList: null,
    terminalCacheEpoch: 0,
    route: { name: 'board', params: {} },
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('terminals in tiles', () => {
  it('sends nothing and keeps its attach when a click focuses its tile', () => {
    show('run_1')
    beside('run_2')
    const own = attached('run_1', true)
    const watched = attached('run_2', false)
    const sent = [own.sent.length, watched.sent.length]
    fireEvent.pointerDown(grid(mine))
    expect(useStore.getState().route.params.runId).toBe('run_1')
    fireEvent.pointerDown(grid(theirs))
    expect(useStore.getState().route.params.runId).toBe('run_2')
    expect([own.sent.length, watched.sent.length]).toEqual(sent)
    expect(StubSocket.opened).toHaveLength(2)
    expect(own.closed || watched.closed).toBe(false)
  })

  it('draws a terminal it does not control scaled, with its control line under it, in a tile that is not focused', () => {
    show('run_2')
    beside('run_1')
    attached('run_1', true)
    attached('run_2', false)
    expect(grid(theirs).style.overflowY).toBe('auto')
    const line = within(tile(theirs)).getByRole('group', { name: 'Session presence' })
    expect(within(tile(theirs)).getByRole('toolbar', { name: 'Terminal toolbar' }).contains(line)).toBe(false)
    expect(grid(mine).style.overflowY).toBe('hidden')
    expect(within(tile(mine)).getByRole('toolbar', { name: 'Terminal toolbar' }).contains(within(tile(mine)).getByRole('group', { name: 'Session presence' }))).toBe(true)
  })

  it('takes control with one click in a tile that is not focused', async () => {
    show('run_2')
    beside('run_1')
    attached('run_1', true)
    const socket = attached('run_2', false)
    await userEvent.click(within(tile(theirs)).getByRole('button', { name: 'Take control' }))
    expect(useStore.getState().route.params.runId).toBe('run_2')
    expect(socket.frames().at(-1)).toMatchObject({ type: 'control', write: true })
    expect(attaches('run_2')).toHaveLength(1)
  })
})

describe('a press that focuses a tile', () => {
  const enhanced = { mode: 'acp', acp: true } as const
  const third = 'cache the catalog'

  /** Tile 1, not focused, holds two sessions with the second on show; tile 2 has the focus. */
  async function twoTabsAtRest() {
    act(() => useStore.setState({
      runs: Object.fromEntries([run({ ...enhanced, task: mine }), run({ ...enhanced, id: 'run_2', task: theirs }), run({ ...enhanced, id: 'run_3', task: third })].map((seed) => [seed.id, toRecord(seed)])),
    }))
    show('run_1')
    act(() => changeTiles((layout, ctx) => openTab(layout, layout.focused, 'run_3', ctx), true))
    beside('run_2')
    await act(async () => {})
    for (const socket of StubSocket.opened.filter((opened) => opened.url.includes('/ws/acp/'))) new ScriptedSession(socket).open()
    return within(screen.getByRole('region', { name: /, tile 1 of 2$/ }))
  }

  it('moves no control out from under its click', async () => {
    const rest = await twoTabsAtRest()
    expect(rest.queryByRole('button', { name: 'Split right', hidden: true })).toBeNull()
    expect(rest.getByRole('group', { name: 'Session controller' })).toBeDefined()

    const field = await rest.findByRole<HTMLTextAreaElement>('combobox', { name: 'Message the agent' })
    fireEvent.pointerDown(field)
    expect(useStore.getState().route.params.runId).toBe('run_3')
    expect(field.rows).toBe(1)
    expect(rest.queryByRole('button', { name: 'Split right' })).toBeNull()
    fireEvent.pointerUp(field)
    await waitFor(() => expect(field.rows).toBe(2))
    expect(rest.getByRole('button', { name: 'Split right' })).toBeDefined()
    const footer = rest.getByRole('button', { name: 'Attach files' }).closest('.wrap-from-end')!
    expect(footer.className).toContain('pointer-events-none')
    fireEvent.pointerMove(window)
    expect(footer.className).not.toContain('pointer-events-none')
  })

  it('opens the picker on a first click on "+"', async () => {
    const rest = await twoTabsAtRest()
    await userEvent.click(rest.getByRole('button', { name: 'Add a tab' }))
    expect(useStore.getState().route.params.runId).toBe('run_3')
    expect(within(screen.getByRole('dialog')).getByPlaceholderText('Find a session or view')).toBeDefined()
  })

  it('activates a background tab on a first click on it', async () => {
    const rest = await twoTabsAtRest()
    await userEvent.click(rest.getByRole('tab', { name: new RegExp(mine) }))
    expect(rest.getByRole('tab', { name: new RegExp(mine) }).getAttribute('aria-selected')).toBe('true')
    expect(useStore.getState().route.params.runId).toBe('run_1')
  })

  it('closes a tab on a first click on its close button', async () => {
    const rest = await twoTabsAtRest()
    await userEvent.click(rest.getByRole('button', { name: `Close ${mine}` }))
    expect(rest.queryByRole('tab', { name: new RegExp(mine) })).toBeNull()
    expect(rest.getAllByRole('tab', { name: new RegExp(third) })).toHaveLength(1)
  })

  it('renders no other session\'s Chat when a session changes', async () => {
    await twoTabsAtRest()
    // Only a session's Chat calls it here.
    const chats = vi.spyOn(fileDrag, 'useRefuseStrayFileDrops')
    // A write outside a batch notifies what the streams' replies still hold back, which would render every Chat.
    act(() => useStore.setState({}))
    chats.mockClear()
    act(() => useStore.setState((s) => ({ runs: { ...s.runs, run_2: { ...s.runs.run_2, task: 'renamed' } } })))
    expect(chats).toHaveBeenCalledTimes(1)
  })
})

describe('the session menu', () => {
  it('opens on a right-click on Chat only in the tile that carries the session\'s controls', async () => {
    const enhanced = { mode: 'acp', acp: true } as const
    act(() => useStore.setState({ runs: { run_1: toRecord(run(enhanced)) } }))
    show('run_1')
    beside('run_1', 'changes')
    await act(async () => {})
    const chat = document.querySelector('[data-tile] [role=tabpanel] [role=tabpanel]')!
    expect(fireEvent.contextMenu(chat)).toBe(true)
    fireEvent.pointerDown(chat)
    expect(fireEvent.contextMenu(chat)).toBe(false)
    expect(await screen.findByRole('menuitem', { name: 'Leave a comment…' })).toBeDefined()
  })
})

describe('a glide', () => {
  /** Stands in for every glide, and lands them when called. */
  function glides() {
    vi.spyOn(motion, 'time').mockReturnValue(300)
    const frames: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (frame: FrameRequestCallback) => frames.push(frame))
    let land = () => {}
    const glide = { finished: new Promise<void>((done) => { land = done }), pause: vi.fn(), play: vi.fn(), cancel() {} }
    vi.spyOn(Element.prototype, 'animate').mockReturnValue(glide as unknown as Animation)
    return { frames, glide, land: () => act(async () => land()) }
  }

  it('holds its clock for two frames, and draws a tile that grows in as an empty pane until it has landed', async () => {
    const { frames, glide, land } = glides()
    show('run_1')
    beside('run_2')
    await act(async () => {})
    expect(glide.pause).toHaveBeenCalledTimes(2)
    frames.splice(0).forEach((frame) => frame(0))
    expect(glide.play).not.toHaveBeenCalled()
    frames.splice(0).forEach((frame) => frame(0))
    expect(glide.play).toHaveBeenCalledTimes(2)

    const grown = screen.getByRole('region', { name: /, tile 2 of 2$/ })
    expect(grown.querySelector('[data-slot=pane-header]')).toBeNull()
    expect(grown.firstElementChild!.className).toContain('*:invisible')
    await land()
    expect(grown.querySelector('[data-slot=pane-header]')).not.toBeNull()
    expect(grown.firstElementChild!.className).not.toContain('*:invisible')
  })

  it('keeps showing a session that was on screen when its tab is dropped into a new tile', async () => {
    show('run_1')
    act(() => changeTiles((layout, ctx) => openTab(layout, layout.focused, 'run_2', ctx), true))
    await act(async () => {})
    const { land } = glides()
    act(() => changeTiles((layout, ctx) => {
      const from = tileList(layout.root)[0]
      return dropTab(layout, { runId: 'run_2', tabId: from.active }, from.id, 'right', ctx)
    }))
    await act(async () => {})
    const grown = screen.getByRole('region', { name: /, tile 2 of 2$/ })
    expect(grown.getAttribute('aria-label')).toContain(theirs)
    expect(grown.querySelector('[data-slot=pane-header]')).not.toBeNull()
    await land()
  })
})
