import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import type { Api } from '@/lib/api'
import type { RunMessage } from '@/lib/types'
import { MessageRow } from './message-row'

const report: RunMessage = {
  id: 'msg-1',
  workspace_id: 'ws-1',
  from_run_id: 'run-child',
  to_run_id: 'run-parent',
  kind: 'report',
  correlation_id: 'rpt-1',
  body: '2 findings',
  created_at: '2026-11-27T08:00:00Z',
  outcome: 'success',
  summary: '2 findings',
  report_body_bytes: 2048,
}

describe('MessageRow', () => {
  it('opens a report\'s body on request', async () => {
    const coordMessagesList = vi.fn().mockResolvedValue({ messages: [{ ...report, report_body: '1. x.go:12 loses a wake\n2. y.go:40 mails twice' }] })
    render(<MessageRow message={report} client={{ coordMessagesList } as unknown as Api} />)
    expect(screen.queryByLabelText('Full report')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Full report (2.0 KB)' }))
    expect(coordMessagesList).toHaveBeenCalledWith({ workspace_id: 'ws-1', correlation_id: 'rpt-1', report_bodies: true })
    await waitFor(() => expect(screen.getByLabelText('Full report').textContent).toBe('1. x.go:12 loses a wake\n2. y.go:40 mails twice'))
    await userEvent.click(screen.getByRole('button', { name: 'Hide full report' }))
    expect(screen.queryByLabelText('Full report')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Full report (2.0 KB)' }))
    expect(coordMessagesList).toHaveBeenCalledTimes(1)
  })

  it('shows the error when the body cannot be read', async () => {
    const coordMessagesList = vi.fn().mockRejectedValue(new Error('coord.messages.list: permission denied'))
    render(<MessageRow message={report} client={{ coordMessagesList } as unknown as Api} />)
    await userEvent.click(screen.getByRole('button', { name: 'Full report (2.0 KB)' }))
    expect((await screen.findByRole('alert')).textContent).toContain('Reading the full report failed: coord.messages.list: permission denied')
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(coordMessagesList).toHaveBeenCalledTimes(2)
  })

  it('offers nothing for a report without a body', () => {
    render(<MessageRow message={{ ...report, report_body_bytes: undefined }} />)
    expect(screen.queryByRole('button', { name: /Full report/ })).toBeNull()
  })
})
