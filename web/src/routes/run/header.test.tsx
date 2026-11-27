import { act, render, screen } from '@testing-library/react'
import { StateLine } from '@/routes/run/header'
import { useStore } from '@/store'
import { run, runRecords } from '@/test/fixtures'

describe('the header state of a launched Enhanced session', () => {
  const ack = { ok: true as const, seq: 0, replay: 0, epoch: 0, has_control: false }

  it('reads Starting until its agent runs', () => {
    const record = runRecords(run({ id: 'run_1', status: 'running', mode: 'acp', acp: true })).run_1!
    act(() => useStore.getState().acpAck('run_1', { ...ack, live: false, starting: true }))
    render(<StateLine run={record} agentName="Claude Code" narrow={false} controls />)
    expect(screen.getByText('Starting')).toBeDefined()

    act(() => useStore.getState().acpAck('run_1', { ...ack, live: true }))
    expect(screen.queryByText('Starting')).toBeNull()
  })
})
