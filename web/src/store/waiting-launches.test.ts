import { describe, expect, it, vi } from 'vitest'
import { ApiError } from '@/lib/api'
import type { LaunchWaiting } from '@/lib/types'
import { createRootStore } from '@/store'
import { loadWaitingLaunches } from '@/store/waiting-launches'
import { fakeApi, run, serverInfo } from '@/test/fixtures'

const launch = (over: Partial<LaunchWaiting> = {}): LaunchWaiting => ({
  id: 'run_w', workspace_id: 'ws_1', harness: 'claude', state: 'waiting', refusal: 'no room',
  attempts: 1, passed: 0, created_at: '2026-08-14T10:00:00Z', expires_at: '2026-08-15T10:00:00Z', ...over,
})

function keeping() {
  const store = createRootStore()
  store.getState().setInfo({ ...serverInfo, features: ['launch-wait'] })
  return store
}

describe('loadWaitingLaunches', () => {
  it('asks by ID after an open launch the list no longer has, and keeps how it ended', async () => {
    const store = keeping()
    const list = vi.fn(async (id?: string) => (id ? [launch({ state: 'cancelled', refusal: 'cancelled by mem-alice' })] : [launch()]))
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: list }))
    expect(store.getState().ownLaunches).toEqual(['run_w'])

    // The server leaves a launch its member cancelled out of the list.
    const gone = vi.fn(async (id?: string) => (id ? [launch({ state: 'cancelled', refusal: 'cancelled by mem-alice' })] : []))
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: gone }))
    expect(gone).toHaveBeenCalledWith('run_w', undefined)
    expect(store.getState().waitingLaunches.run_w?.state).toBe('cancelled')
    expect(store.getState().ownLaunches).toEqual([])

    // Ended, it is not asked after again, and stays for the tab that shows it.
    gone.mockClear()
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: gone }))
    expect(gone).toHaveBeenCalledTimes(1)
    expect(store.getState().waitingLaunches.run_w?.state).toBe('cancelled')
  })

  it('gives a started launch over to its session, fetched when the store has not received it', async () => {
    const store = keeping()
    const runGet = vi.fn(async () => run({ id: 'run_w' }))
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: async () => [launch({ state: 'started', refusal: '' })], runGet }))
    expect(runGet).toHaveBeenCalledWith('run_w')
    expect(store.getState().runs.run_w).toBeDefined()
    expect(store.getState().waitingLaunches).toEqual({})
  })

  it('forgets a started launch whose session was deleted, and keeps one whose session could not be read', async () => {
    const store = keeping()
    const started = async () => [launch({ state: 'started', refusal: '' })]
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: started, runGet: async () => Promise.reject(new ApiError(503, 'run.get: server restarting')) }))
    expect(store.getState().waitingLaunches.run_w?.state).toBe('started')

    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: started, runGet: async () => Promise.reject(new ApiError(404, 'run.get: not found')) }))
    expect(store.getState().waitingLaunches).toEqual({})
    expect(store.getState().runs.run_w).toBeUndefined()
  })

  it('lets what the store learned during the read stand: a launch just made, and one just cancelled', async () => {
    const store = keeping()
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: async () => [launch()] }))
    let answer: (list: LaunchWaiting[]) => void = () => {}
    const reading = loadWaitingLaunches(store, fakeApi({
      launchWaitingList: (id?: string) => (id ? Promise.resolve([]) : new Promise<LaunchWaiting[]>((resolve) => { answer = resolve })),
    }))
    store.getState().upsertWaitingLaunch(launch({ id: 'run_new' }), true)
    store.getState().upsertWaitingLaunch(launch({ state: 'cancelled', refusal: 'cancelled by mem-alice' }))
    answer([launch()])
    await reading
    expect(store.getState().waitingLaunches.run_new?.state).toBe('waiting')
    expect(store.getState().waitingLaunches.run_w?.state).toBe('cancelled')
    expect(store.getState().ownLaunches).toEqual(['run_w', 'run_new'])
  })

  it('leaves out a shown ID that is no launch, and holds nothing for a server that keeps none', async () => {
    const store = keeping()
    const list = vi.fn(async (id?: string) => (id ? Promise.reject(new ApiError(404, 'launch.waiting.list: no waiting launch run_x')) : [launch()]))
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: list }), ['run_x'])
    expect(Object.keys(store.getState().waitingLaunches)).toEqual(['run_w'])

    store.getState().setInfo(serverInfo)
    list.mockClear()
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: list }))
    expect(list).not.toHaveBeenCalled()
    expect(store.getState().waitingLaunches).toEqual({})
  })

  it('fails on an error that is not an answer, and keeps what it held', async () => {
    const store = keeping()
    await loadWaitingLaunches(store, fakeApi({ launchWaitingList: async () => [launch()] }))
    await expect(loadWaitingLaunches(store, fakeApi({ launchWaitingList: async () => Promise.reject(new ApiError(503, 'server restarting')) }))).rejects.toThrow('server restarting')
    expect(store.getState().waitingLaunches.run_w?.state).toBe('waiting')
  })
})
