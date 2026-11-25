import { ApiError, codeUnavailable } from '@/lib/api'
import { createRootStore } from '@/store'
import { loadLinear } from '@/store/linear'
import { fakeApi } from '@/test/fixtures'

function storeWith(methods: string[]) {
  const store = createRootStore()
  store.setState({ capabilities: { gateway: 'test', methods, ws: [] } })
  return store
}

describe('loadLinear', () => {
  it('stores the status for a member with a key', async () => {
    const store = storeWith(['linear.status'])
    await loadLinear(store, fakeApi({ linearStatus: vi.fn(async () => ({ connected: true, name: 'Ada' })) }))

    expect(store.getState().linear).toEqual({ connected: true, name: 'Ada' })
  })

  it('keeps a saved key connected, with Linear\'s error, when Linear refuses it', async () => {
    const store = storeWith(['linear.status'])
    await loadLinear(store, fakeApi({ linearStatus: vi.fn(async () => { throw new ApiError(500, 'Authentication required', -32603) }) }))

    expect(store.getState().linear).toEqual({ connected: true, error: 'Authentication required' })
  })

  it.each([
    ['an unconfigured service', new ApiError(500, 'linear service not configured', codeUnavailable)],
    ['a timeout', new ApiError(500, 'linear: Post "https://api.linear.app/graphql": context deadline exceeded', -32603)],
    ['a transport failure', new TypeError('Failed to fetch')],
  ])('does not call a key saved when the read fails with %s', async (_, failure) => {
    const store = storeWith(['linear.status'])
    await loadLinear(store, fakeApi({ linearStatus: vi.fn(async () => { throw failure }) }))

    expect(store.getState().linear).toEqual({ connected: false, error: failure.message })
  })

  it('clears the status on a server without linear.status', async () => {
    const store = storeWith(['run.list'])
    store.getState().setLinear({ connected: true })
    const client = fakeApi()
    await loadLinear(store, client)

    expect(store.getState().linear).toBeNull()
    expect(client.linearStatus).not.toHaveBeenCalled()
  })

  it('drops a read that finishes after the identity changed', async () => {
    const store = storeWith(['linear.status'])
    let finish: (s: { connected: boolean }) => void = () => {}
    const read = loadLinear(store, fakeApi({ linearStatus: vi.fn(() => new Promise<{ connected: boolean }>((resolve) => { finish = resolve })) }))
    store.setState({ identityKey: 'someone-else' })
    finish({ connected: true })
    await read

    expect(store.getState().linear).toBeNull()
  })

  it('drops a read that started before a connect or disconnect', async () => {
    const store = storeWith(['linear.status'])
    let finish: (s: { connected: boolean }) => void = () => {}
    const read = loadLinear(store, fakeApi({ linearStatus: vi.fn(() => new Promise<{ connected: boolean }>((resolve) => { finish = resolve })) }))
    store.getState().writeLinear({ connected: false })
    finish({ connected: true })
    await read

    expect(store.getState().linear).toEqual({ connected: false })
  })
})

describe('the Linear nav entry', () => {
  it('appears only for a member with a key', async () => {
    const { surfaces } = await import('@/lib/surfaces')
    const { capability } = await import('@/store/hooks')
    const base = capability({ gateway: 'test', methods: ['linear.issues'], ws: [] })
    expect(surfaces({ ...base, linearConnected: false }).some((s) => s.name === 'linear')).toBe(false)
    expect(surfaces({ ...base, linearConnected: true }).some((s) => s.name === 'linear')).toBe(true)
  })
})

describe('the Linear page without a key', () => {
  it('is withheld only when the server lacks linear.issues', async () => {
    const { withheld } = await import('@/lib/surfaces')
    const { capability } = await import('@/store/hooks')
    const without = capability({ gateway: 'test', methods: ['linear.status'], ws: [] })
    const withMethod = capability({ gateway: 'test', methods: ['linear.issues'], ws: [] })
    expect(withheld('linear', { ...withMethod, linearConnected: false })).toBe(false)
    expect(withheld('linear', { ...without, linearConnected: false })).toBe(true)
  })
})
