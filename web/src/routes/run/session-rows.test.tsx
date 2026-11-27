import { act, fireEvent, render, screen } from '@testing-library/react'
import { TimelineRow, type RowContext } from '@/routes/run/session-rows'
import { useStore } from '@/store'
import type { SessionRow } from '@/store/session-rows'
import { alice, serverInfo } from '@/test/fixtures'

it('shows a held mail wake as an event that the member in control types now or dismisses', () => {
  useStore.setState({ info: serverInfo, members: { [alice.id]: alice }, roomActionError: {} })
  const decide = vi.fn(async () => {})
  const ctx: RowContext = { runID: 'run_1', task: 'task', ownerID: alice.id, hasAgentTerminal: true, openTerminal: () => {}, reveal: () => {}, steer: { decide } }
  const wake: SessionRow = {
    kind: 'user', id: 'wake_1', at: '2026-08-14T10:00:00Z', body: '1 unread message from another agent. Cloudfield will type a line telling the agent to read its inbox.',
    authorID: alice.id, via: 'agent mail', delivery: 'Queued', deliverAfter: '2099-01-01T00:00:00Z',
  }
  const { container, rerender } = render(<TimelineRow item={{ kind: 'row', key: wake.id, row: wake, open: false }} ctx={ctx} />)
  expect(container.querySelector('[data-slot=message-row]')).toBeNull()
  expect(screen.getByText(/^Agent mail · Types in \d+s$/)).toBeDefined()
  expect(screen.getByText(wake.body)).toBeDefined()
  expect(screen.getByRole('button', { name: 'Dismiss' })).toBeDefined()
  fireEvent.click(screen.getByRole('button', { name: 'Type it now' }))
  expect(decide).toHaveBeenCalledWith('wake_1', true)

  act(() => useStore.getState().setRoomActionError(wake.id, 'The terminal may have text you have not sent.'))
  expect(screen.getByRole('alert').textContent).toBe('The terminal may have text you have not sent.')

  const takeControl = vi.fn()
  rerender(<TimelineRow item={{ kind: 'row', key: wake.id, row: { ...wake, deliverAfter: '2026-08-14T10:00:45Z' }, open: false }} ctx={{ ...ctx, steer: { takeControl } }} />)
  expect(screen.getByText('Agent mail · Waiting for a pause')).toBeDefined()
  fireEvent.click(screen.getByRole('button', { name: 'Take control to decide' }))
  expect(takeControl).toHaveBeenCalled()

  rerender(<TimelineRow item={{ kind: 'row', key: wake.id, row: { ...wake, delivery: 'Denied', deliverAfter: undefined }, open: false }} ctx={ctx} />)
  expect(screen.getByText('Agent mail · Dismissed')).toBeDefined()
  expect(screen.queryByText(wake.body)).toBeNull()
  expect(screen.queryByRole('button')).toBeNull()
})

it('shows your own queued message without a decision', () => {
  useStore.setState({ info: serverInfo, members: { [alice.id]: alice } })
  const ctx: RowContext = { runID: 'run_1', task: 'task', ownerID: alice.id, hasAgentTerminal: true, openTerminal: () => {}, reveal: () => {}, steer: { decide: vi.fn(async () => {}) } }
  const own: SessionRow = { kind: 'user', id: 'own', at: '2026-08-14T10:00:00Z', body: 'rebase first', authorID: alice.id, delivery: 'Queued', deliverAfter: '2099-01-01T00:00:00Z' }
  render(<TimelineRow item={{ kind: 'row', key: own.id, row: own, open: false }} ctx={ctx} />)
  expect(screen.getByText(/^Delivers in \d+s$/)).toBeDefined()
  expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
})
