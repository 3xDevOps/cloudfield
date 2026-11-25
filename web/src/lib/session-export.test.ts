import { outputCap, sessionMarkdown } from '@/lib/session-export'
import type { SessionFrame, SessionItem } from '@/lib/session-types'
import { fakeApi, run } from '@/test/fixtures'

const now = new Date('2026-08-15T09:00:00Z')

function frame(seq: number, over: Partial<SessionItem>, truncated = false): SessionFrame {
  return { seq, truncated, item: { seq, epoch: 1, time: '2026-08-14T10:02:00Z', turn: 1, kind: 'message', ...over } }
}

const said = (role: 'user' | 'assistant', id: string, text: string): Partial<SessionItem> => ({ message: { role, message_id: id, text } })

test('an Enhanced session exports every page of its history under a header of the run record', async () => {
  const long = 'x'.repeat(outputCap + 7)
  const older = [
    frame(3, said('user', 'u1', 'fix the login bug')),
    frame(4, { kind: 'reset' }),
    frame(5, { kind: 'notice', notice: { severity: 'warning', title: 'Started a new agent conversation' } }),
  ]
  const newer = [
    frame(6, said('assistant', 'a1', 'Looking ')),
    frame(7, { kind: 'tool_call', tool_call: { id: 't1', title: 'go test ./...', status: 'in_progress', output: 'ok' } }),
    frame(8, { kind: 'tool_call', tool_call: { id: 't1', title: 'go test ./...', status: 'completed', exit_code: 0, output: long } }),
    frame(9, said('assistant', 'a1', 'cut'), true),
    frame(10, { kind: 'tool_call', tool_call: { id: 't2', title: 'cat big.log' } }, true),
  ]
  const client = fakeApi({
    runWithPullRequests: vi.fn(async () => ({
      run: run({ id: 'run-abc123', acp: true, status: 'failed', reason: 'sandbox lost', model: 'opus', title: 'Fix login' }),
      pull_requests: [{ repository: 'acme/shop', number: 7, url: 'https://github.com/acme/shop/pull/7', state: 'open', checked_at: '2026-08-14T11:00:00Z' }],
    })),
    runACPHistory: vi.fn(async (_run: string, before?: number) =>
      before === undefined ? { frames: newer, oldest_seq: 3 } : { frames: older, oldest_seq: 3, truncated_before: true }),
    runACPItem: vi.fn(async (_run: string, seq: number) => {
      if (seq === 10) throw new Error('item expired')
      return { ...frame(9, said('assistant', 'a1', 'into it.')).item! }
    }),
  })

  const text = await sessionMarkdown(client, 'run-abc123', 'shop', now)

  expect(vi.mocked(client.runACPHistory).mock.calls).toEqual([['run-abc123', undefined], ['run-abc123', 6]])
  expect(text.split('\n\n## Conversation')[0]).toBe([
    '# Fix login',
    '',
    '- Session: `run-abc123`',
    '- Workspace: shop',
    '- Branch: `aether/run-1-checkout`',
    '- Agent: claude, opus',
    '- Status: failed (sandbox lost)',
    '- Pull request: https://github.com/acme/shop/pull/7 (open)',
    '- Exported: 2026-08-15T09:00:00.000Z',
  ].join('\n'))
  const body = text.split('## Conversation\n\n')[1]!
  expect(body.split('\n\n').filter((block) => !block.startsWith('```'))).toEqual([
    '[earlier conversation has expired]',
    '### User',
    'fix the login bug',
    '**New agent conversation**',
    '**Notice (warning):** Started a new agent conversation',
    '### Assistant',
    'Looking into it.',
    '### Tool (completed, exit 0)',
    '### Tool',
  ])
  expect(body).toContain('```\ngo test ./...\n```\n\n```\nok' + 'x'.repeat(outputCap - 2) + '\n[... 9 more characters]\n```')
  expect(body).toContain('```\ncat big.log [truncated; run.acp.item: item expired]\n```')
})

test('a Standard session exports its terminal history oldest first', async () => {
  const line = (text: string) => ({ cursor: text, time: 0, text })
  const client = fakeApi({
    runWithPullRequests: vi.fn(async () => ({ run: run({ id: 'run-tui1' }) })),
    terminalHistory: vi.fn(async ({ before }: { before?: string }) =>
      before
        ? { lines: [line('$ make'), line('```')], has_more: false, truncated_before: true }
        : { lines: [line('ok')], has_more: true, next_cursor: 'c1' }),
  })

  const text = await sessionMarkdown(client, 'run-tui1', undefined, now)

  expect(text).toContain('- Workspace: wsp_1\n')
  expect(text.endsWith('## Terminal\n\n[earlier output has expired]\n\n````\n$ make\n```\nok\n````\n')).toBe(true)
})
