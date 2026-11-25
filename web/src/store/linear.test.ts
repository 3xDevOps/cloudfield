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

  it.each([
    ['Authentication required, not authenticated', 'linear: Linear refused the API key: Authentication required, not authenticated'],
    ['a 401 status', 'linear: Linear refused the API key: 401 Unauthorized: {}'],
  ])('keeps a saved key connected and refused when Linear says %s', async (_, text) => {
    const store = storeWith(['linear.status'])
    await loadLinear(store, fakeApi({ linearStatus: vi.fn(async () => { throw new ApiError(500, `linear.status: ${text}`, -32603) }) }))

    expect(store.getState().linear).toEqual({ connected: true, refused: true, error: `linear.status: ${text}` })
  })

  it.each([
    ['a timeout', 'linear: Post "https://api.linear.app/graphql": context deadline exceeded'],
    ['an unreadable key file', 'linear: read key of member m1: permission denied'],
    ['a proxy refusing the connection', 'linear: Post "https://api.linear.app/graphql": Forbidden'],
    ['a server error from Linear', 'linear: 502 Bad Gateway: <html>'],
  ])('keeps a saved key connected, not refused, when the read fails with %s', async (_, text) => {
    const store = storeWith(['linear.status'])
    await loadLinear(store, fakeApi({ linearStatus: vi.fn(async () => { throw new ApiError(500, `linear.status: ${text}`, -32603) }) }))

    expect(store.getState().linear).toEqual({ connected: true, refused: false, error: `linear.status: ${text}` })
  })

  it.each([
    ['an unconfigured service', new ApiError(500, 'linear service not configured', codeUnavailable)],
    ['a proxy 401', new ApiError(401, 'linear.status: 401 Unauthorized')],
    ['a proxy 403 with a JSON error of its own', new ApiError(403, 'linear.status: Forbidden by policy', -32001)],
    ['a transport failure', new TypeError('Failed to fetch')],
  ])('assumes no key when the read fails with %s', async (_, failure) => {
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
