import { ApiError, type Api } from '@/lib/api'
import { type LaunchWaiting, launchWaitFeature } from '@/lib/types'
import type { RootState, RootStore } from '@/store'
import type { SliceCreator } from '@/store/slice'

/**
 * Launches the server keeps because it has no room, by the session ID
 * reserved for each: the member's own, which `ownLaunches` lists in the
 * server's order with those that ended without a session in the last day,
 * and any other the viewer has open in a tab.
 */
export interface WaitingLaunchesSlice {
  waitingLaunches: Record<string, LaunchWaiting>
  ownLaunches: string[]
  /** The identityKey they were read for. */
  launchesFor: string | null
  setWaitingLaunches: (launches: Record<string, LaunchWaiting>, own: string[], viewer: string | null) => void
  upsertWaitingLaunch: (launch: LaunchWaiting) => void
}

export const createWaitingLaunchesSlice: SliceCreator<WaitingLaunchesSlice> = (set) => ({
  waitingLaunches: {},
  ownLaunches: [],
  launchesFor: null,
  setWaitingLaunches: (waitingLaunches, ownLaunches, launchesFor) => set({ waitingLaunches, ownLaunches, launchesFor }),
  upsertWaitingLaunch: (launch) => set((s) => ({ waitingLaunches: { ...s.waitingLaunches, [launch.id]: launch } })),
})

/** Whether the server keeps launches it has no room for. */
export const keepsLaunches = (s: RootState) => !!s.info?.features?.includes(launchWaitFeature)

export const launchOpen = (w: LaunchWaiting) => w.state === 'waiting' || w.state === 'starting'

/**
 * Reads the member's waiting launches, and by ID each launch the store holds
 * as open that the list no longer has: one the member cancelled, or another
 * member's. A launch that started gives way to its session, which is fetched
 * when the store has not received it. With `shown`, the IDs of the tabs that
 * show no session, it asks after each of those too and forgets every other
 * launch that is not the member's.
 */
export async function loadWaitingLaunches(store: RootStore, client: Api, shown?: string[], signal?: AbortSignal): Promise<void> {
  // Another viewer's launches go before the read, which may fail.
  let before = store.getState()
  if (!keepsLaunches(before) || before.launchesFor !== before.identityKey) {
    before.setWaitingLaunches({}, [], before.identityKey)
    before = store.getState()
  }
  if (!keepsLaunches(before)) return
  const own = await client.launchWaitingList(undefined, signal)
  const next: Record<string, LaunchWaiting> = Object.fromEntries(own.map((w) => [w.id, w]))
  const asked = new Set([...(shown ?? []), ...Object.values(before.waitingLaunches).filter(launchOpen).map((w) => w.id)])
  for (const id of asked) {
    if (next[id] || store.getState().runs[id]) continue
    try {
      const [launch] = await client.launchWaitingList(id, signal)
      if (launch) next[id] = launch
    } catch (err) {
      if (!(err instanceof ApiError && err.status === 404)) throw err
    }
  }
  for (const launch of Object.values(next)) {
    if (launch.state !== 'started') continue
    if (!store.getState().runs[launch.id]) store.getState().upsertRun(await client.runGet(launch.id))
    delete next[launch.id]
  }
  if (signal?.aborted) return
  const now = store.getState()
  // An ended launch a tab shows is not asked after again until `shown` names it.
  for (const [id, launch] of Object.entries(shown ? {} : now.waitingLaunches)) {
    if (!next[id] && !launchOpen(launch) && !now.runs[id]) next[id] = launch
  }
  if (now.identityKey === before.identityKey) now.setWaitingLaunches(next, own.map((w) => w.id).filter((id) => next[id]), now.identityKey)
}
