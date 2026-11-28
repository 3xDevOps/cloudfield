import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MissingRun } from '@/components/missing-run'
import { WaitingLaunches } from '@/components/waiting-launch'
import { api } from '@/lib/api'
import type { LaunchWaiting } from '@/lib/types'
import { useStore } from '@/store'
import { serverInfo } from '@/test/fixtures'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

const refusal = 'scheduler: insufficient host memory headroom: 1 available bytes; reserve 2 bytes'
const waiting = (over: Partial<LaunchWaiting> = {}): LaunchWaiting => ({
  id: 'run-w', workspace_id: 'ws-1', harness: 'claude', task: 'add a health check', state: 'waiting', refusal,
  attempts: 3, passed: 1, created_at: new Date(Date.now() - 120_000).toISOString(), expires_at: '2026-11-28T22:00:00Z', ...over,
})

beforeEach(() => {
  vi.mocked(api.launchWaitingList).mockReset().mockResolvedValue([])
  vi.mocked(api.launchWaitingCancel).mockReset()
  useStore.setState({ hydrated: true, hydrationError: null, streamDead: false, info: { ...serverInfo, features: ['launch-wait'] } })
})

describe('a launch waiting for room', () => {
  it('shows the waiting launch on its session ID, with the server\u2019s refusal verbatim and Cancel', async () => {
    vi.mocked(api.launchWaitingList).mockResolvedValue([waiting()])
    vi.mocked(api.launchWaitingCancel).mockResolvedValue(waiting({ state: 'cancelled' }))
    render(<MissingRun runId="run-w" />)

    expect(await screen.findByRole('heading', { name: 'Waiting for room on the server' })).toBeDefined()
    expect(screen.getByText(refusal)).toBeDefined()
    expect(screen.getByText('add a health check')).toBeDefined()
    expect(screen.getByText(/No session exists yet\..*tried 3 more times; 1 other launch started first\./)).toBeDefined()
    expect(screen.queryByText('This session is not on the server. It may have been deleted.')).toBeNull()
    expect(api.launchWaitingList).toHaveBeenCalledWith('run-w', expect.anything())

    vi.mocked(api.launchWaitingList).mockResolvedValue([waiting({ state: 'cancelled', refusal: 'cancelled by mem-alice' })])
    fireEvent.click(screen.getByRole('button', { name: 'Cancel launch' }))
    await waitFor(() => expect(api.launchWaitingCancel).toHaveBeenCalledWith('run-w'))
    expect(await screen.findByRole('heading', { name: 'Launch cancelled' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Cancel launch' })).toBeNull()
  })

  it('says why a launch that gave up did not start', async () => {
    vi.mocked(api.launchWaitingList).mockResolvedValue([waiting({ state: 'expired', refusal: `no room within 24 hours; the last refusal: ${refusal}` })])
    render(<MissingRun runId="run-w" />)
    expect(await screen.findByRole('heading', { name: 'Launch gave up waiting' })).toBeDefined()
    expect(screen.getByRole('alert').textContent).toContain(refusal)
    expect(screen.getByText(/No session was started\./)).toBeDefined()
  })

  it('keeps "Session not found" for an ID that is no waiting launch, and on a server that keeps none', async () => {
    vi.mocked(api.launchWaitingList).mockRejectedValue(new Error('launch.waiting.list: no waiting launch run-x'))
    const view = render(<MissingRun runId="run-x" />)
    expect(await screen.findByText('This session is not on the server. It may have been deleted.')).toBeDefined()
    view.unmount()

    vi.mocked(api.launchWaitingList).mockClear()
    useStore.setState({ info: serverInfo })
    render(<MissingRun runId="run-x" />)
    expect(screen.getByText('This session is not on the server. It may have been deleted.')).toBeDefined()
    expect(api.launchWaitingList).not.toHaveBeenCalled()
  })

  it('lists waiting launches above the sessions, each with its refusal and Cancel, and opens one', async () => {
    vi.mocked(api.launchWaitingList).mockResolvedValue([waiting(), waiting({ id: 'run-r', task: 'refused one', state: 'refused', refusal: 'run.launch: role viewer may not launch' })])
    vi.mocked(api.launchWaitingCancel).mockResolvedValue(waiting({ state: 'cancelled' }))
    render(<WaitingLaunches />)

    const list = await screen.findByRole('list', { name: 'Launches waiting for room' })
    expect(list.textContent).toContain(refusal)
    expect(list.textContent).toContain('run.launch: role viewer may not launch')
    expect(screen.getAllByRole('button', { name: 'Cancel' })).toHaveLength(1)

    fireEvent.click(screen.getByText('add a health check'))
    expect(useStore.getState().route).toMatchObject({ name: 'run', params: { runId: 'run-w' } })
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(api.launchWaitingCancel).toHaveBeenCalledWith('run-w'))
  })

  it('draws nothing when nothing waits', async () => {
    const { container } = render(<WaitingLaunches />)
    await waitFor(() => expect(api.launchWaitingList).toHaveBeenCalled())
    expect(container.firstChild).toBeNull()
  })
})
