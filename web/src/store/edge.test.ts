import type { ServerEdge } from '@/lib/types'
import { createRootStore } from '@/store'
import { loadEdge } from '@/store/edge'
import { fakeApi } from '@/test/fixtures'

const edge: ServerEdge = {
  edge_url: 'https://edge-old.example.test', server_id: 'wqc4lsjvzdzrwq3k5dabdtajwj', access_policy: 'approved-devices',
  owner: null, connected: true, claimed: true, notice: { state: 'retiring', replacement: 'https://edge-new.example.test' },
}

function seeded() {
  const store = createRootStore()
  store.setState({ identityKey: 'alice@server', capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local: [] } })
  return store
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (err: Error) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

it('keeps the last good answer and records the error when a read fails', async () => {
  const store = seeded()
  const serverEdge = vi.fn<() => Promise<ServerEdge>>().mockResolvedValueOnce(edge).mockRejectedValueOnce(new Error('server.edge: connection reset'))
  await loadEdge(store, fakeApi({ serverEdge }))
  await expect(loadEdge(store, fakeApi({ serverEdge }))).rejects.toThrow('connection reset')
  expect(store.getState().edge).toEqual(edge)
  expect(store.getState().edgeError).toBe('server.edge: connection reset')
})

it('lands an older read that succeeds after a newer one failed', async () => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const newer = deferred<ServerEdge>()
  const serverEdge = vi.fn<() => Promise<ServerEdge>>().mockReturnValueOnce(older.promise).mockReturnValueOnce(newer.promise)
  const client = fakeApi({ serverEdge })
  const first = loadEdge(store, client)
  const second = loadEdge(store, client).catch(() => undefined)
  newer.reject(new Error('server.edge: 502 Bad Gateway'))
  await second
  older.resolve(edge)
  await first
  expect(store.getState().edge).toEqual(edge)
  expect(store.getState().edgeError).toBeNull()
})

it('drops an older answer once a newer one landed', async () => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const serverEdge = vi.fn<() => Promise<ServerEdge>>().mockReturnValueOnce(older.promise).mockResolvedValueOnce({ ...edge, notice: undefined })
  const client = fakeApi({ serverEdge })
  const first = loadEdge(store, client)
  await loadEdge(store, client)
  older.resolve(edge)
  await first
  expect(store.getState().edge?.notice).toBeUndefined()
})

it.each(['answers', 'fails'] as const)('drops a read for another viewer that %s late, keeping that viewer\'s answer', async (outcome) => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const bobs = { ...edge, notice: undefined }
  const serverEdge = vi.fn<() => Promise<ServerEdge>>().mockReturnValueOnce(older.promise).mockResolvedValueOnce(bobs)
  const client = fakeApi({ serverEdge })
  const first = loadEdge(store, client).catch(() => undefined)
  store.setState({ identityKey: 'bob@server' })
  await loadEdge(store, client)
  if (outcome === 'answers') older.resolve(edge)
  else older.reject(new Error('server.edge: connection reset'))
  await first
  expect(store.getState()).toMatchObject({ edge: bobs, edgeFor: 'bob@server', edgeError: null })
})

it('clears what a viewer change left showing when the read for the old viewer returns', async () => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const client = fakeApi({ serverEdge: vi.fn<() => Promise<ServerEdge>>().mockResolvedValueOnce(edge).mockReturnValueOnce(older.promise) })
  await loadEdge(store, client)
  const second = loadEdge(store, client)
  store.setState({ identityKey: 'bob@server' })
  older.resolve(edge)
  await second
  expect(store.getState()).toMatchObject({ edge: null, edgeFor: 'bob@server' })
})

it.each(['before', 'after'] as const)('lands no older answer once the method is gone, whether it returns %s the read that sees that', async (order) => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const client = fakeApi({ serverEdge: vi.fn<() => Promise<ServerEdge>>().mockReturnValueOnce(older.promise) })
  const first = loadEdge(store, client)
  store.setState({ capabilities: { gateway: 'local', methods: ['server.info'], ws: ['events'], local: [] } })
  if (order === 'before') {
    older.resolve(edge)
    await first
  }
  await loadEdge(store, client)
  store.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local: [] } })
  if (order === 'after') {
    older.resolve(edge)
    await first
  }
  expect(store.getState().edge).toBeNull()
})

it('lands no read for a viewer who left and came back since it started', async () => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const client = fakeApi({ serverEdge: vi.fn<() => Promise<ServerEdge>>().mockReturnValueOnce(older.promise) })
  const first = loadEdge(store, client)
  store.setState({ identityKey: 'bob@server' })
  store.setState({ identityKey: 'alice@server' })
  older.resolve(edge)
  await first
  expect(store.getState()).toMatchObject({ edge: null, edgeError: null })
})

it('clears the answer and the error when the method goes away, and lands no read from before', async () => {
  const store = seeded()
  const older = deferred<ServerEdge>()
  const serverEdge = vi.fn<() => Promise<ServerEdge>>().mockResolvedValueOnce(edge).mockRejectedValueOnce(new Error('server.edge: connection reset')).mockReturnValueOnce(older.promise)
  const client = fakeApi({ serverEdge })
  await loadEdge(store, client)
  await loadEdge(store, client).catch(() => undefined)
  const third = loadEdge(store, client)
  store.setState({ capabilities: { gateway: 'local', methods: ['server.info'], ws: ['events'], local: [] } })
  expect(store.getState()).toMatchObject({ edge: null, edgeError: null })
  store.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: ['events'], local: [] } })
  older.resolve(edge)
  await third
  expect(store.getState().edge).toBeNull()
})
