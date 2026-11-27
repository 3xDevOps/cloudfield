import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api } from '@/lib/api'
import { SleepCallout } from '@/routes/run/sleep-callout'
import { useStore } from '@/store'
import { run, runRecords } from '@/test/fixtures'

beforeEach(() => {
  useStore.setState({ info: { member: { id: 'mem_admin', role: 'admin' } } } as never)
})

afterEach(() => {
  vi.restoreAllMocks()
})

const since = '2026-11-11T09:00:00Z'

function record(fields: Parameters<typeof run>[0]) {
  return runRecords(run({ id: 'run_1', status: 'needs-attention', acp: true, ...fields })).run_1!
}

describe('SleepCallout', () => {
  it('shows nothing for an awake session', () => {
    const { container } = render(<SleepCallout run={record({})} />)
    expect(container.textContent).toBe('')
  })

  it('wakes on Wake and shows the real error of a failed wake', async () => {
    const wake = vi.spyOn(api, 'runWake').mockRejectedValue(new Error('scheduler: insufficient host memory headroom: 1.2 GiB available'))
    render(<SleepCallout run={record({ asleep_since: since })} />)
    expect(screen.getByText(/Asleep since/)).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Wake' }))
    await waitFor(() => expect(wake).toHaveBeenCalledWith('run_1'))
    expect(await screen.findByText('Wake failed')).toBeDefined()
    expect(screen.getByRole('button', { name: 'Wake' })).toBeDefined()
    expect(screen.getByText(/insufficient host memory headroom: 1.2 GiB available/)).toBeDefined()
  })

  it('offers no Wake to a member who cannot steer, nor for a closed session', () => {
    useStore.setState({ info: { member: { id: 'mem_viewer', role: 'viewer' } } } as never)
    render(<SleepCallout run={record({ asleep_since: since })} />)
    expect(screen.getByText(/Asleep since/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Wake' })).toBeNull()
    useStore.setState({ info: { member: { id: 'mem_admin', role: 'admin' } } } as never)
    const { container } = render(<SleepCallout run={record({ asleep_since: since, status: 'merged' })} />)
    expect(container.textContent).toBe('')
  })

  it('keeps the explanation folded behind the Asleep since row', () => {
    render(<SleepCallout run={record({ asleep_since: since, asleep_saved: true })} />)
    const row = screen.getByRole('button', { name: /Asleep since/ })
    expect(row.getAttribute('aria-expanded')).toBe('false')
    const about = screen.getByText(/Its processes, terminals, files and conversation come back where they were; connections to outside the session may not/)
    expect(row.getAttribute('aria-controls')).toBe(about.id)
    expect(about.hidden).toBe(true)
    fireEvent.click(row)
    expect(row.getAttribute('aria-expanded')).toBe('true')
    expect(about.hidden).toBe(false)
    expect(screen.queryByText(/running processes are not/)).toBeNull()
    expect(row.textContent).not.toContain('processes stopped')
  })

  it('says a session paused in memory resumes, never that it was saved to disk', () => {
    render(<SleepCallout run={record({ asleep_since: since, asleep_saved: true, asleep_in_memory: true })} />)
    const row = screen.getByRole('button', { name: /Asleep since/ })
    expect(row.textContent).not.toContain('processes stopped')
    fireEvent.click(row)
    expect(screen.getByText(/paused the idle session in the server’s memory.*press Wake to resume it/).hidden).toBe(false)
    expect(screen.queryByText(/saved the idle session to disk/)).toBeNull()
  })

  it('says on the folded row that a stopped container lost its processes', () => {
    render(<SleepCallout run={record({ asleep_since: since })} />)
    const row = screen.getByRole('button', { name: /Asleep since .* · processes stopped/ })
    expect(row.querySelector('.truncate')).toBeNull()
    fireEvent.click(row)
    expect(screen.getByText(/running processes are not/).hidden).toBe(false)
  })

  it('shows a wake in progress without offering another', () => {
    render(<SleepCallout run={record({ asleep_since: since, waking: true })} />)
    expect(screen.getByText('Waking… Starting the container and restoring the agent’s conversation.')).toBeDefined()
    expect(screen.queryByRole('button')).toBeNull()
  })
})
