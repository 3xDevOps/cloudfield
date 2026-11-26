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

it('refreshes the mirror when a workspace is named, then at most every 5 seconds of edits', () => {
  vi.mocked(api.workspaceMirrorWarm).mockRejectedValueOnce(new Error('gitengine: mirror offline'))
  const { rerender } = renderHook(({ workspace, task }) => useWarmBase(workspace, task), { initialProps: { workspace: '', task: '' } })
  expect(api.workspaceMirrorWarm).not.toHaveBeenCalled()

  rerender({ workspace: 'ws-1', task: '' })
  expect(vi.mocked(api.workspaceMirrorWarm).mock.calls).toEqual([['ws-1']])

  vi.advanceTimersByTime(4000)
  rerender({ workspace: 'ws-1', task: 'fix the' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledOnce()

  vi.advanceTimersByTime(1000)
  rerender({ workspace: 'ws-1', task: 'fix the bug' })
  expect(api.workspaceMirrorWarm).toHaveBeenCalledTimes(2)

  rerender({ workspace: 'ws-2', task: 'fix the bug' })
  expect(vi.mocked(api.workspaceMirrorWarm).mock.calls.at(-1)).toEqual(['ws-2'])
})
