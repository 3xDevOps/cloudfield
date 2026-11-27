import { renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useWarmBase } from '@/components/launch/warm-base'
import { api } from '@/lib/api'

vi.mock('@/lib/api', async () => {
  const { fakeApi } = await import('@/test/fixtures')
  return { api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

beforeEach(() => {
  vi.useFakeTimers()
  vi.clearAllMocks()
})
afterEach(() => vi.useRealTimers())

it('refreshes the mirror when a workspace is named, then at most every 5 seconds of edits, one call at a time, until one fails', async () => {
  const { rerender } = renderHook(({ workspace, task }) => useWarmBase(workspace, task), { initialProps: { workspace: '', task: '' } })
  expect(api.workspaceMirrorWarm).not.toHaveBeenCalled()

  rerender({ workspace: 'ws-1', task: '' })
  expect(vi.mocked(api.workspaceMirrorWarm).mock.calls).toEqual([['ws-1']])

  await vi.advanceTimersByTimeAsync(4000)
  rerender({ workspace: 'ws-1', task: 'fix' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledOnce()

  let fail = (_: Error) => {}
  vi.mocked(api.workspaceMirrorWarm).mockReturnValueOnce(new Promise((_, reject) => (fail = reject)))
  await vi.advanceTimersByTimeAsync(1000)
  rerender({ workspace: 'ws-1', task: 'fix the' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledTimes(2)

  await vi.advanceTimersByTimeAsync(6000)
  rerender({ workspace: 'ws-1', task: 'fix the bug' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledTimes(2)

  fail(new Error('gitengine: mirror offline'))
  await vi.advanceTimersByTimeAsync(6000)
  rerender({ workspace: 'ws-1', task: 'fix the bug now' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledTimes(2)

  rerender({ workspace: 'ws-2', task: 'fix the bug now' })
  expect(vi.mocked(api.workspaceMirrorWarm).mock.calls.at(-1)).toEqual(['ws-2'])
})
