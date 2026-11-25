import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { CenterView } from '@/components/shell/center-view'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import { RunFrame, type FrameTile } from '@/routes/run'
import { RunSession } from '@/routes/run/session-host'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { emptyTiles, routeTiles, splitTile, viewContext } from '@/store/tiles'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'
import { atViewport } from '@/test/viewport'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi() }
})

const focusedTile: FrameTile = { focused: true, controls: true, alone: false, terminalHost: true, chatOnScreen: false, elsewhere: [], arrows: {}, onArrow: () => {} }
const otherTile: FrameTile = { focused: false, controls: false, alone: false, terminalHost: false, chatOnScreen: false, elsewhere: [], arrows: {}, onArrow: () => {} }

function record(over: Partial<Run> = {}) {
  useStore.getState().upsertRun(run(over))
  return useStore.getState().runs.run_1!
}

function frame(view: RunView, tile: FrameTile) {
  return <RunFrame run={useStore.getState().runs.run_1!} params={{ runId: 'run_1', view }} tile={tile} />
}

function attached() {
  act(() => {
    const socket = StubSocket.last()
    socket.onopen?.()
    socket.onmessage?.({ data: JSON.stringify({ ok: true, cols: 80, rows: 24, has_control: false, control_generation: 0 }) })
  })
}

beforeEach(() => {
  StubSocket.install()
  vi.clearAllMocks()
  useStore.setState({
    hydrated: true,
    runs: {},
    info: serverInfo,
    members: { [alice.id]: alice, [bob.id]: bob },
    workspaces: { [workspace.id]: workspace },
    route: { name: 'run', params: { runId: 'run_1' } },
    roomStatus: {},
    roomStatusControl: {},
    runViewMemory: {},
    tiles: emptyTiles(),
    shownRunView: null,
    browserRequests: {},
    agentList: null,
    terminalCacheEpoch: 0,
    capabilities: { gateway: 'remote', methods: ['*'], ws: ['events', 'attach'] },
  })
})

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('session host', () => {
  it('attaches and polls once for two frames of one session', async () => {
    const session = record()
    render(
      <RunSession run={session} views={['terminal', 'changes']}>
        {frame('terminal', { ...focusedTile, elsewhere: ['changes'] })}
        {frame('changes', { ...otherTile, elsewhere: ['terminal'] })}
      </RunSession>,
    )
    expect(screen.getAllByRole('tablist', { name: 'Session views' })).toHaveLength(2)
    expect(StubSocket.opened).toHaveLength(1)
    expect(document.querySelectorAll('[id="run-agent-panel-run_1"]')).toHaveLength(1)
    await waitFor(() => expect(api.runRoomStatus).toHaveBeenCalledTimes(1))
    expect(api.runRoomList).toHaveBeenCalledTimes(1)
    expect(api.devTerminalList).toHaveBeenCalledTimes(1)
  })

  it('asks for write control only once a frame has shown the default view, and keeps asking after', () => {
    const session = record()
    const host = (view: RunView) => (
      <RunSession run={session} views={[view]}>{frame(view, focusedTile)}</RunSession>
    )
    const view = render(host('changes'))
    attached()
    const socket = StubSocket.last()
    expect(socket.frames()).toHaveLength(1)
    expect(socket.frames()[0]).not.toMatchObject({ write: true })
    view.rerender(host('terminal'))
    expect(socket.frames()).toHaveLength(2)
    expect(socket.frames()[1]).toMatchObject({ type: 'control', write: true })
    view.rerender(host('changes'))
    expect(socket.frames()).toHaveLength(2)
    expect(StubSocket.opened).toHaveLength(1)
  })

  it('remounts with a new attach when the terminal cache epoch moves', () => {
    const View = lookupRoute('run')!
    record()
    render(<View params={{ runId: 'run_1' }} />)
    attached()
    const before = screen.getByRole('tablist', { name: 'Session views' })
    act(() => useStore.setState({ terminalCacheEpoch: 1 }))
    expect(StubSocket.opened).toHaveLength(2)
    expect(StubSocket.opened[0]!.closed).toBe(true)
    expect(screen.getByRole('tablist', { name: 'Session views' })).not.toBe(before)
  })
})

describe('a phone', () => {
  it('closes a session and attaches the next one, with no tile around it', () => {
    atViewport(390, { pointer: 'coarse' })
    record()
    useStore.getState().upsertRun(run({ id: 'run_2', task: 'speed up search' }))
    render(<main id="main"><CenterView /></main>)
    expect(StubSocket.opened.map((socket) => socket.url)).toEqual([expect.stringContaining('/ws/attach/run_1')])
    act(() => useStore.getState().navigate('run', { runId: 'run_2' }))
    expect(StubSocket.opened.map((socket) => socket.closed)).toEqual([true, false])
    expect(StubSocket.last().url).toContain('/ws/attach/run_2')
    expect(screen.getByRole('heading', { level: 1, name: 'speed up search' })).toBeDefined()
    expect(document.querySelector('[data-tile]')).toBeNull()
    expect(screen.queryByRole('tablist', { name: 'Sessions in this tile' })).toBeNull()
  })

  it('puts no tab panel or tab stop of a tile around the session', () => {
    atViewport(390, { pointer: 'coarse' })
    record()
    render(<main id="main"><CenterView /></main>)
    const around: Element[] = []
    for (let node = screen.getByRole('heading', { level: 1 }).parentElement; node && node.id !== 'main'; node = node.parentElement) around.push(node)
    expect(around.filter((node) => node.hasAttribute('role') || node.getAttribute('tabindex') === '0' || node.hasAttribute('aria-labelledby'))).toEqual([])
    expect(screen.getAllByRole('tabpanel', { hidden: true }).filter((panel) => !document.getElementById(panel.getAttribute('aria-labelledby') ?? ''))).toEqual([])
  })
})

describe('a phone whose stored focus is an empty tile', () => {
  function split(view?: RunView) {
    atViewport(390, { pointer: 'coarse' })
    record()
    const ctx = viewContext(useStore.getState())
    const one = routeTiles(emptyTiles(), { runId: 'run_1' }, ctx)
    useStore.setState({ tiles: splitTile(one, one.focused, 'right') })
    useStore.setState({ route: { name: 'run', params: view ? { runId: 'run_1', view } : { runId: 'run_1' } } })
    render(<main id="main"><CenterView /></main>)
    return within(screen.getByRole('tablist', { name: 'Session views' }))
  }

  it('switches views from its tabs', () => {
    const views = split()
    expect(views.getByRole('tab', { name: 'Terminal' }).getAttribute('aria-selected')).toBe('true')
    fireEvent.mouseDown(views.getByRole('tab', { name: 'Changes' }))
    expect(views.getByRole('tab', { name: 'Changes' }).getAttribute('aria-selected')).toBe('true')
    expect(useStore.getState().route.params).toEqual({ runId: 'run_1', view: 'changes' })
    expect(StubSocket.opened).toHaveLength(1)
  })

  it('opens a link on the view it names', () => {
    const views = split('changes')
    expect(views.getByRole('tab', { name: 'Changes' }).getAttribute('aria-selected')).toBe('true')
    expect(useStore.getState().route.params).toEqual({ runId: 'run_1', view: 'changes' })
  })
})

describe('a window resized across the phone breakpoint', () => {
  it.each([[700, 960], [960, 700]])('keeps the session, its open Captures and a control request from %i to %i wide', async (from, to) => {
    const resize = atViewport(from)
    record({ mode: 'acp', acp: true })
    useStore.setState({ route: { name: 'run', params: { runId: 'run_1', view: 'session' } } })
    render(<main id="main"><CenterView /></main>)
    const socket = StubSocket.opened.find((opened) => opened.url.includes('/ws/acp/run_1'))!
    const session = new ScriptedSession(socket).open({ has_control: true, control_generation: 4 })
    const composer = await screen.findByRole('combobox', { name: 'Message the agent' })
    await userEvent.type(composer, 'half a thought')
    await userEvent.click(screen.getByRole('button', { name: 'More' }))
    await userEvent.click(await screen.findByRole('menuitem', { name: 'Captures…' }))
    const captures = await screen.findByRole('dialog', { name: 'Captures' })
    session.send({
      type: 'takeover',
      ok: true,
      takeover: {
        id: 'takeover-request',
        requester_member_id: bob.id,
        requester_session_id: 'other-session',
        holder_session_id: String(session.header().control_session_id),
        holder_generation: 4,
        phase: 'review',
        hold_started_at: '2026-10-08T12:00:00Z',
        hold_deadline: '2026-10-08T12:00:03Z',
        decision_deadline: '2026-10-08T12:00:10Z',
        server_now: '2026-10-08T12:00:03Z',
      },
    })
    const deny = within(await screen.findByRole('alertdialog', { name: 'Session control requested' })).getByRole('button', { name: 'Deny' })
    expect(document.activeElement).toBe(deny)

    resize(to)

    expect(within(screen.getByRole('alertdialog', { name: 'Session control requested' })).getByRole('button', { name: 'Deny' })).toBe(deny)
    expect(document.activeElement).toBe(deny)
    expect(captures.isConnected).toBe(true)
    expect(composer.isConnected).toBe(true)
    expect(composer).toHaveProperty('value', 'half a thought')
    expect(Boolean(document.querySelector('[data-tile] [role=tablist][aria-label="Sessions in this tile"]'))).toBe(to >= 768)
    expect(socket.closed).toBe(false)
    expect(StubSocket.opened.filter((opened) => opened.url.includes('/ws/acp/run_1'))).toHaveLength(1)
  })
})

describe('unfocused frame', () => {
  function addressKey() {
    const press = new KeyboardEvent('keydown', { key: 'l', ctrlKey: true, bubbles: true, cancelable: true })
    act(() => { document.body.dispatchEvent(press) })
    return press.defaultPrevented
  }

  it('answers no session or Browser key and is not the shown view', () => {
    const session = record({ mode: 'acp' })
    const host = (tile: FrameTile) => (
      <RunSession run={session} views={['session']}>{frame('session', tile)}</RunSession>
    )
    const view = render(host(otherTile))
    fireEvent.keyDown(window, { key: ']' })
    expect(addressKey()).toBe(false)
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(useStore.getState().route).toEqual({ name: 'run', params: { runId: 'run_1' } })
    expect(useStore.getState().browserRequests).toEqual({})
    expect(useStore.getState().shownRunView).toBeNull()

    view.rerender(host(focusedTile))
    expect(useStore.getState().shownRunView).toEqual({ runId: 'run_1', view: 'session' })
    fireEvent.keyDown(window, { key: ']' })
    expect(useStore.getState().route.params.view).toBe('terminal')
    expect(addressKey()).toBe(true)
    expect(useStore.getState().browserRequests).toEqual({ run_1: '' })
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(useStore.getState().route.name).toBe('run')
    view.unmount()
  })

  it('does not move the keyboard to the composer the route asks for', async () => {
    const session = record({ mode: 'acp', acp: true })
    const params = { runId: 'run_1', view: 'session', focus: 'composer' }
    const host = (tile: FrameTile) => (
      <RunSession run={session} views={['session']}><RunFrame run={session} params={params} tile={tile} /></RunSession>
    )
    const view = render(host(otherTile))
    new ScriptedSession(StubSocket.opened.find((socket) => socket.url.includes('/ws/acp/run_1'))!).open()
    const composer = await screen.findByRole('combobox', { name: 'Message the agent' })
    expect(document.activeElement).toBe(document.body)

    view.rerender(host(focusedTile))
    await waitFor(() => expect(document.activeElement).toBe(composer))
    view.unmount()
  })

  it('leaves the keyboard where it is when its Browser has no page', async () => {
    vi.mocked(api.devBrowserStatus).mockResolvedValue({ available: true, running: false, state: 'not_started' })
    vi.mocked(api.devBrowserPages).mockResolvedValue({ pages: [] })
    const session = record({ mode: 'acp' })
    const host = (tile: FrameTile) => (
      <RunSession key={String(tile.focused)} run={session} views={['browser']}>{frame('browser', tile)}</RunSession>
    )
    const view = render(host(otherTile))
    expect(await screen.findByRole('heading', { name: 'Open a page' })).toBeDefined()
    expect(document.activeElement).toBe(document.body)

    view.rerender(host(focusedTile))
    expect(await screen.findByRole('heading', { name: 'Open a page' })).toBeDefined()
    await waitFor(() => expect(document.activeElement).toBe(screen.getByRole('combobox', { name: 'Address' })))
    view.unmount()
  })
})
