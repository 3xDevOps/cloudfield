import { ApiError, type Api } from '@/lib/api'
import { type LaunchWaiting, launchWaitFeature } from '@/lib/types'
import type { RootState, RootStore } from '@/store'
import { coalesce } from '@/store/coalesce'
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
  /** `own` is for a launch the member just made, which no read has listed yet. */
  upsertWaitingLaunch: (launch: LaunchWaiting, own?: boolean) => void
}

export const createWaitingLaunchesSlice: SliceCreator<WaitingLaunchesSlice> = (set) => ({
  waitingLaunches: {},
  ownLaunches: [],
  launchesFor: null,
  setWaitingLaunches: (waitingLaunches, ownLaunches, launchesFor) => set({ waitingLaunches, ownLaunches, launchesFor }),
  upsertWaitingLaunch: (launch, own) =>
    set((s) => ({
      waitingLaunches: { ...s.waitingLaunches, [launch.id]: launch },
      ownLaunches: own && !s.ownLaunches.includes(launch.id) ? [...s.ownLaunches, launch.id] : s.ownLaunches,
    })),
})

/** Whether the server keeps launches it has no room for. */
export const keepsLaunches = (s: RootState) => !!s.info?.features?.includes(launchWaitFeature)

export const launchOpen = (w: LaunchWaiting) => w.state === 'waiting' || w.state === 'starting'

/** A launch whose ending the store has yet to see: open, or started with its session not received. */
export const launchUnsettled = (w: LaunchWaiting) => launchOpen(w) || w.state === 'started'

const notFound = (err: unknown) => err instanceof ApiError && err.status === 404

/**
 * Reads the member's waiting launches, and by ID each launch the store holds
 * unsettled that the list no longer has: one the member cancelled, one that
 * started, or another member's. A launch that started gives way to its
 * session, which is fetched when the store has not received it; a session
 * deleted since leaves nothing. With `shown`, the IDs of the tabs that show
 * no session, it asks after each of those too and forgets every other launch
 * that is not the member's. What the store learned while the read was under
 * way, such as a launch just made or cancelled, stands.
 */
export async function loadWaitingLaunches(store: RootStore, client: Api, shown?: string[], signal?: AbortSignal): Promise<void> {
  // Another viewer's launches go before the read, which may fail.
  let before = store.getState()
  if (!keepsLaunches(before) || before.launchesFor !== before.identityKey) {
    before.setWaitingLaunches({}, [], before.identityKey)
    before = store.getState()
  }
  if (!keepsLaunches(before)) return
  const same = () => store.getState().identityKey === before.identityKey && !signal?.aborted
  const own = await client.launchWaitingList(undefined, signal)
  const next: Record<string, LaunchWaiting> = Object.fromEntries(own.map((w) => [w.id, w]))
  const asked = [...new Set([...(shown ?? []), ...Object.values(before.waitingLaunches).filter(launchUnsettled).map((w) => w.id)])]
    .filter((id) => !next[id] && !before.runs[id])
  await Promise.all(asked.map(async (id) => {
    try {
      const [launch] = await client.launchWaitingList(id, signal)
      if (launch) next[id] = launch
    } catch (err) {
      if (!notFound(err)) throw err
    }
  }))
  await Promise.all(Object.values(next).filter((w) => w.state === 'started').map(async (launch) => {
    if (!store.getState().runs[launch.id]) {
      try {
        const run = await client.runGet(launch.id)
        if (!same()) return
        store.getState().upsertRun(run)
      } catch (err) {
        // Not an answer: the launch stays, and the next read asks again.
        if (!notFound(err)) return
      }
    }
    delete next[launch.id]
  }))
  if (!same()) return
  const now = store.getState()
  const ids = own.map((w) => w.id)
  for (const [id, launch] of Object.entries(now.waitingLaunches)) {
    if (now.runs[id]) continue
    if (before.waitingLaunches[id] !== launch) {
      next[id] = launch
      if (now.ownLaunches.includes(id) && !ids.includes(id)) ids.push(id)
    } else if (!shown && !next[id] && !launchUnsettled(launch)) {
      // An ended launch a tab shows is not asked after again until `shown` names it.
      next[id] = launch
    }
  }
  now.setWaitingLaunches(next, ids.filter((id) => next[id]), now.identityKey)
}

/** One read of the member's launches at a time, whoever asks; a failed read leaves what the store holds. */
export function refreshWaitingLaunches(store: RootStore, client: Api, signal?: AbortSignal): void {
  coalesce(store, 'waiting-launches', () => loadWaitingLaunches(store, client, undefined, signal).catch(() => {}))
}
