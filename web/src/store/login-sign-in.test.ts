import { act, renderHook } from '@testing-library/react'
import { ApiError } from '@/lib/api'
import { useLoginSignIn, useLoginSignInWait } from '@/store/login-sign-in'
import { fakeApi } from '@/test/fixtures'

describe('waiting on a sign-in', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    useLoginSignIn.setState({ running: { id: 'lgn-wait0000001', name: 'spare' }, ended: null })
  })
  afterEach(() => vi.useRealTimers())

  it('waits again while the server has not answered, and ends on its answer', async () => {
    const refusal = 'scheduler: start the sign-in of login lgn-wait0000001: no capacity'
    const client = fakeApi({
      agentLoginResult: vi.fn<ReturnType<typeof fakeApi>['agentLoginResult']>()
        .mockRejectedValueOnce(new ApiError(503, 'agent.login.result: server did not answer agent.login.result within 31m0s', -32004))
        .mockRejectedValueOnce(new ApiError(503, 'agent.login.result: server unreachable: connection reset by peer', -32004))
        .mockRejectedValueOnce(new TypeError('Failed to fetch'))
        .mockRejectedValueOnce(new ApiError(409, `agent.login.result: ${refusal}`, -32003)),
    })
    const onEnded = vi.fn()
    renderHook(() => useLoginSignInWait(client, 'claude', onEnded, true))
    for (let i = 0; i < 2; i++) {
      await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
      expect(useLoginSignIn.getState().running?.id).toBe('lgn-wait0000001')
    }
    await act(async () => { await vi.advanceTimersByTimeAsync(1000) })
    expect(client.agentLoginResult).toHaveBeenCalledTimes(4)
    expect(useLoginSignIn.getState()).toEqual({ running: null, ended: { id: 'lgn-wait0000001', outcome: { refused: `agent.login.result: ${refusal}` } } })
    expect(onEnded).toHaveBeenCalledTimes(1)
  })
})
