import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import type { Run } from '@/lib/types'
import { lookupRoute } from '@/routes/registry'
import { RunFrame, type FrameTile } from '@/routes/run'
import { RunSession } from '@/routes/run/session-host'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { ScriptedSession } from '@/test/acp-stream'
import { alice, bob, run, serverInfo, workspace } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

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
