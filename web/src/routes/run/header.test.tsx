import { act, render, screen } from '@testing-library/react'
import { Tabs } from '@/components/ui/tabs'
import { StateLine } from '@/routes/run/header'
import { PhoneRunHeader } from '@/routes/run/phone-header'
import { WorkflowChip } from '@/routes/run/workflow-panel'
import { useStore } from '@/store'
import { run, runRecords } from '@/test/fixtures'

describe('the header state of a launched Enhanced session', () => {
  const ack = { ok: true as const, seq: 0, replay: 0, epoch: 0, has_control: false }
  const record = runRecords(run({ id: 'run_1', status: 'running', state: undefined, mode: 'acp', acp: true })).run_1!

  beforeEach(() => {
    useStore.setState({ acpSessions: {}, startingRuns: {} })
    useStore.getState().acpStream('run_1', 'live')
  })

  it('reads Starting until its agent runs', () => {
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true }))
    render(<StateLine run={record} agentName="Claude Code" narrow={false} controls />)
    expect(screen.getByRole('status').textContent).toBe('Starting')
    expect(screen.getByRole('status').parentElement!.title).toMatch(/^Starting since .* A message you send now is delivered when the agent is ready\./)

    act(() => useStore.getState().acpAck('run_1', { ...ack, live: true }))
    expect(screen.getByRole('status').textContent).toBe('Running')
  })

  it('drops a Starting its closed stream can no longer correct', () => {
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true }))
    render(<StateLine run={record} agentName="Claude Code" narrow={false} controls />)
    act(() => useStore.getState().acpStream('run_1', 'connecting'))
    expect(screen.getByRole('status').textContent).toBe('Running')
  })

  it('reads Starting on the session tree chip that stands in for the state', () => {
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true }))
    render(<WorkflowChip run={record} open={false} onToggle={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Session tree: Starting' })).toBeDefined()

    act(() => useStore.getState().acpAck('run_1', { ...ack, live: true }))
    expect(screen.getByRole('button', { name: 'Session tree: Running' })).toBeDefined()
  })

  it('reads Starting on a phone', () => {
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true }))
    render(
      <Tabs value="session">
        <PhoneRunHeader run={record} view="session" views={['session', 'terminal', 'changes']} nav={nav} agentName="Claude Code" keyboard={false} primary={null} detailsOpen={false} onDetails={vi.fn()} onWorkflow={vi.fn()} extra={[]} pullRequest={null} />
      </Tabs>,
    )
    expect(screen.getByRole('status').textContent).toBe('Starting')

    act(() => useStore.getState().acpAck('run_1', { ...ack, live: true }))
    expect(screen.getByRole('status').textContent).toBe('Running')
  })
})
