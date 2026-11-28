import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import type * as React from 'react'
import { api } from '@/lib/api'
import type * as apiModule from '@/lib/api'
import type { GatewayCapabilities } from '@/lib/types'
import { Composer } from '@/routes/run/composer'
import { useStore } from '@/store'
import { toRecord } from '@/store/runs'
import { alice, run, serverInfo, workspace } from '@/test/fixtures'

vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const every: GatewayCapabilities = { gateway: 'remote', methods: ['*'], ws: [] }
const closed = { id: 'run_1', status: 'merged', reason: 'closed; retained container', finished_at: '2026-08-14T10:30:00Z', container_retained_until: '2026-08-21T10:30:00Z' } as const

function mount(over: Partial<ReturnType<typeof run>>, methods = every, props?: Pick<Parameters<typeof Composer>[0], 'docked' | 'collapsed' | 'onDropStatus'>) {
  const record = toRecord(run({ ...over }))
  useStore.setState({
    workspaces: { [workspace.id]: workspace },
    activeWorkspace: workspace.id,
    members: { [alice.id]: alice },
    runs: { [record.id]: record },
    capabilities: methods,
    info: { ...serverInfo, member: alice },
    hydrated: true,
  })
  const Live = () => {
    const current = useStore((s) => s.runs[record.id]!)
    return <Composer run={current} agent={{} as never} room={{} as never} textarea={{ current: null } as React.RefObject<HTMLTextAreaElement | null>} onFocusChange={() => {}} onEscape={() => {}} {...props} />
  }
  return render(<Live />)
}

beforeEach(() => vi.clearAllMocks())

describe('session drop target status', () => {
  it('attaches a file through the collapsed Enhanced docked composer', async () => {
    mount({ id: 'run_docked_drop', mode: 'acp', acp: true }, every, { docked: true, collapsed: true })
    act(() => useStore.getState().acpStream('run_docked_drop', 'live'))
    const field = screen.getByRole('combobox', { name: 'Message the agent' })
    const file = new File(['notes'], 'notes.txt', { type: 'text/plain' })
    const dataTransfer = { types: ['Files'], items: [{ kind: 'file' }], files: [file] }

    fireEvent.dragEnter(field, { dataTransfer })
    expect(screen.getByText('Drop to attach 1 file')).toBeDefined()
    fireEvent.drop(field, { dataTransfer })
    expect(screen.queryByText('Drop to attach 1 file')).toBeNull()
    await waitFor(() => expect(api.uploadTerminalFile).toHaveBeenCalledWith(file, 'run_docked_drop'))
  })

  it('reports the available attachment room to the session pane', async () => {
    const onDropStatus = vi.fn()
    mount({ mode: 'tui', acp: false }, every, { onDropStatus })

    await waitFor(() => expect(onDropStatus).toHaveBeenLastCalledWith(expect.objectContaining({ limit: 8, upload: expect.any(Function) })))
  })

  it('blocks pane drops when file upload is unavailable', async () => {
    const onDropStatus = vi.fn()
    mount({ mode: 'tui', acp: false }, { gateway: 'remote', methods: ['run.get'], ws: [] }, { onDropStatus })

    await waitFor(() => expect(onDropStatus).toHaveBeenLastCalledWith(expect.objectContaining({ limit: 0, blocked: 'Files cannot be attached now', upload: expect.any(Function) })))
  })
})

describe.each([{ mode: 'tui', acp: false }, { mode: 'acp', acp: true }] as const)('finished $mode session', (kind) => {
  it('offers Relaunch inside the retention grace and returns to a live run', async () => {
    vi.mocked(api.runRelaunch).mockResolvedValue(run({ ...kind, id: 'run_1', status: 'running', reason: undefined, finished_at: undefined, status_seq: 99 }))
    mount({ ...kind, ...closed })
    expect(screen.getByText(/Relaunch it before .+ to continue the chat and files\./).role).toBe('status')

    fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))

    await waitFor(() => expect(useStore.getState().runs.run_1!.status).toBe('running'))
    expect(api.runRelaunch).toHaveBeenCalledWith('run_1')
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
    expect(useStore.getState().runs.run_1!.container_retained_until).toBeUndefined()
  })

  it('shows the server refusal and keeps the button', async () => {
    vi.mocked(api.runRelaunch).mockRejectedValue(new Error('run.relaunch: retained container unavailable'))
    mount({ ...kind, ...closed })

    fireEvent.click(screen.getByRole('button', { name: 'Relaunch' }))

    expect((await screen.findByRole('alert')).textContent).toBe('run.relaunch: retained container unavailable')
    expect(screen.getByRole('button', { name: 'Relaunch' })).toHaveProperty('disabled', false)
  })

  it('offers nothing once the container is released', () => {
    mount({ ...kind, ...closed, reason: 'closed', container_retained_until: undefined })
    expect(screen.getByText('This session has finished.').role).toBe('status')
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  })

  it('offers nothing when the gateway lacks run.relaunch', () => {
    mount({ ...kind, ...closed }, { gateway: 'remote', methods: ['run.get'], ws: [] })
    expect(screen.queryByRole('button', { name: 'Relaunch' })).toBeNull()
  })
})

describe('a launched Enhanced session whose agent is still starting', () => {
  const ack = { ok: true as const, seq: 0, replay: 0, epoch: 0, has_control: false }

  it('takes a message instead of refusing it, and keeps the box and an unsent draft when the start fails', async () => {
    mount({ mode: 'acp', acp: true })
    act(() => {
      useStore.getState().acpStream('run_1', 'live')
      useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true })
    })
    fireEvent.change(screen.getByPlaceholderText('Message the agent while it starts'), { target: { value: 'also add tests' } })
    fireEvent.click(screen.getByRole('button', { name: /Send/ }))
    await waitFor(() => expect(api.runInject).toHaveBeenCalledWith('run_1', 'also add tests', expect.any(String), expect.anything()))

    const box = screen.getByPlaceholderText('Message the agent while it starts')
    fireEvent.change(box, { target: { value: 'and update the docs' } })
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false }))
    expect(box).toHaveProperty('value', 'and update the docs')
    expect(box.isConnected).toBe(true)
  })
})
