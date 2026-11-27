import { fireEvent, render, screen } from '@testing-library/react'
import { TimelineRow, type RowContext } from '@/routes/run/session-rows'
import { useStore } from '@/store'
import type { SessionRow } from '@/store/session-rows'
import { alice, serverInfo } from '@/test/fixtures'

it('shows a held mail wake as agent mail that the member in control types now or dismisses', () => {
  useStore.setState({ info: serverInfo, members: { [alice.id]: alice } })
  const decide = vi.fn(async () => {})
  const ctx: RowContext = { runID: 'run_1', task: 'task', ownerID: alice.id, hasAgentTerminal: true, openTerminal: () => {}, reveal: () => {}, steer: { decide } }
  const wake: SessionRow = {
    kind: 'user', id: 'wake_1', at: '2026-08-14T10:00:00Z', body: '1 unread message from another agent.',
    authorID: alice.id, via: 'agent mail', delivery: 'Queued', deliverAfter: '2099-01-01T00:00:00Z',
  }
  const { rerender } = render(<TimelineRow item={{ kind: 'row', key: wake.id, row: wake, open: false }} ctx={ctx} />)
  expect(screen.getByText('Agent mail')).toBeDefined()
  expect(screen.getByText(/^Typed in \d+s$/)).toBeDefined()
  expect(screen.getByRole('button', { name: 'Not now' })).toBeDefined()
  fireEvent.click(screen.getByRole('button', { name: 'Type it now' }))
  expect(decide).toHaveBeenCalledWith('wake_1', true)

  rerender(<TimelineRow item={{ kind: 'row', key: wake.id, row: { ...wake, deliverAfter: '2026-08-14T10:00:45Z' }, open: false }} ctx={ctx} />)
  expect(screen.getByText('Typed when quiet')).toBeDefined()

  rerender(<TimelineRow item={{ kind: 'row', key: wake.id, row: { ...wake, delivery: 'Denied', deliverAfter: undefined }, open: false }} ctx={ctx} />)
  expect(screen.getByText('Dismissed')).toBeDefined()
  expect(screen.queryByRole('button', { name: 'Type it now' })).toBeNull()
})

it('shows your own queued message without a decision', () => {
  useStore.setState({ info: serverInfo, members: { [alice.id]: alice } })
  const ctx: RowContext = { runID: 'run_1', task: 'task', ownerID: alice.id, hasAgentTerminal: true, openTerminal: () => {}, reveal: () => {}, steer: { decide: vi.fn(async () => {}) } }
  const own: SessionRow = { kind: 'user', id: 'own', at: '2026-08-14T10:00:00Z', body: 'rebase first', authorID: alice.id, delivery: 'Queued', deliverAfter: '2099-01-01T00:00:00Z' }
  render(<TimelineRow item={{ kind: 'row', key: own.id, row: own, open: false }} ctx={ctx} />)
  expect(screen.getByText(/^Delivers in \d+s$/)).toBeDefined()
  expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
})
