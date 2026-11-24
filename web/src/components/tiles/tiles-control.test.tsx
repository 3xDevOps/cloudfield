import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { changeTiles } from '@/components/tiles/area'
import { TileArea } from '@/components/tiles/tile-area'
import type * as apiModule from '@/lib/api'
import { RunFrame } from '@/routes/run'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { emptyTiles, pickTab, splitTile } from '@/store/tiles'
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
