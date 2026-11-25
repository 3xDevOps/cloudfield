import { ApiError, codeNotFound, codeUnavailable } from '@/lib/api'
import { outputCap, sessionMarkdown, stallDelays } from '@/lib/session-export'
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
    frame(11, { kind: 'tool_call', tool_call: { id: 't2', title: 'cat big.log', status: 'completed' } }),
    frame(12, { kind: 'thought', message: { role: 'thought', message_id: 'th', text: 'cut' } }, true),
    frame(13, { kind: 'tool_call', truncated: true, tool_call: { id: 't3', parent_id: 't2', title: 'Read shot.png', content: [{ type: 'image', mime_type: 'image/png' }], raw_output: [{ source: { data: 'iVBOR' + 'A'.repeat(600) } }] } }),
    frame(14, { kind: 'tool_call', tool_call: { id: 't4', title: 'Read photo.jpg', raw_output: '[{"source":{"data":"' + 'A'.repeat(600) } }),
    frame(15, said('assistant', 'a2', 'Run:\n```sh\nmake')),
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
    '### Tool (completed)',
    '[truncated; run.acp.item: item expired]',
    '### Tool (subagent)',
    '[cut by the server]',
    '### Tool',
    '### Assistant',
    'Run:\n```sh\nmake\n```\n',
  ])
  expect(vi.mocked(client.runACPItem).mock.calls.map(([, seq]) => seq)).toEqual([9, 10])
  expect(body).toContain('```\ngo test ./...\n```\n\n```\n[... 9 earlier characters]\n' + 'x'.repeat(outputCap) + '\n```')
  expect(body).toContain('```\nRead shot.png\n```\n\n```\n[image, image/png]\n```')
  expect(body).toContain('```\nRead photo.jpg\n```\n\n```\n[{"source":{"data":"[base64 data, 600 characters]\n```')
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

const page = (lines: string[], next?: string) => ({ lines: lines.map((text) => ({ cursor: text, time: 0, text })), has_more: next !== undefined, next_cursor: next })

test('a terminal page the server ran out of time on is read again, and the export fails when it never advances', async () => {
  vi.useFakeTimers()
  onTestFinished(() => void vi.useRealTimers())
  const stalled = { lines: [], has_more: true, next_cursor: 'c1' }
  const pages = [page(['ok'], 'c1'), stalled, page(['$ make', 'ok'])]
  const client = fakeApi({
    runWithPullRequests: vi.fn(async () => ({ run: run({ id: 'run-tui1' }) })),
    terminalHistory: vi.fn(async () => pages.shift() ?? stalled),
  })
  const done = sessionMarkdown(client, 'run-tui1', undefined, now)
  await vi.advanceTimersByTimeAsync(stallDelays[0]!)
  expect((await done).endsWith('```\n$ make\nok\n```\n')).toBe(true)

  pages.push(page(['ok'], 'c1'))
  const failed = sessionMarkdown(client, 'run-tui1', undefined, now).catch((err: Error) => err.message)
  await vi.advanceTimersByTimeAsync(stallDelays.reduce((sum, delay) => sum + delay, 0))
  expect(await failed).toBe('terminal.history did not advance after 5 retries; nothing was saved')
})

test('a missing transcript and an expired cursor are marked instead of failing the export', async () => {
  const history = vi.fn(async (): Promise<ReturnType<typeof page>> => {
    throw new ApiError(200, 'terminal transcript not found', codeNotFound)
  })
  const client = fakeApi({ runWithPullRequests: vi.fn(async () => ({ run: run({ id: 'run-tui1', branch: '' }) })), terminalHistory: history })
  const none = await sessionMarkdown(client, 'run-tui1', undefined, now)
  expect(none.endsWith('## Terminal\n\n[no terminal history]\n')).toBe(true)
  expect(none).not.toContain('Branch')

  history.mockResolvedValueOnce(page(['ok'], 'c1')).mockRejectedValueOnce(new ApiError(200, 'earlier terminal history has expired', codeUnavailable))
  expect((await sessionMarkdown(client, 'run-tui1', undefined, now)).endsWith('[earlier output has expired]\n\n```\nok\n```\n')).toBe(true)

  history.mockRejectedValueOnce(new ApiError(200, 'terminal history is unavailable', codeUnavailable))
  await expect(sessionMarkdown(client, 'run-tui1', undefined, now)).rejects.toThrow('terminal history is unavailable')
})
