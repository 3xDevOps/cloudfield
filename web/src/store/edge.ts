import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { ServerEdge } from '@/lib/types'
import type { RootStore } from '@/store'
import { capability } from '@/lib/capability'
import type { SliceCreator } from '@/store/slice'

/** server.edge, for Settings › Remote access and the device cards; null
 * before it is read or when the server does not serve it. */
export interface EdgeSlice {
  edge: ServerEdge | null
  /** The identityKey `edge` was read for. */
  edgeFor: string | null
  /** Why the latest read failed; `edge` is then the last good answer, if any. */
  edgeError: string | null
  setEdge: (edge: ServerEdge | null, viewer: string | null) => void
  setEdgeError: (error: string) => void
}

export const createEdgeSlice: SliceCreator<EdgeSlice> = (set) => ({
  edge: null,
  edgeFor: null,
  edgeError: null,
  setEdge: (edge, viewer) => set({ edge, edgeFor: viewer, edgeError: null }),
  setEdgeError: (edgeError) => set({ edgeError }),
})

const reads = new WeakMap<RootStore, number>()
const landed = new WeakMap<RootStore, number>()
const generations = new WeakMap<RootStore, number>()

const servesEdge = (state: ReturnType<RootStore['getState']>) => capability(state.capabilities).hasMethod('server.edge')

/** A change of viewer, or of whether the server serves server.edge, clears
 * the slice and keeps every read started before it from landing. */
export function watchEdge(store: RootStore) {
  store.subscribe((state, previous) => {
    if (state.identityKey === previous.identityKey &&
      (state.capabilities === previous.capabilities || servesEdge(state) === servesEdge(previous))) return
    generations.set(store, (generations.get(store) ?? 0) + 1)
    if (state.edge !== null || state.edgeError !== null || state.edgeFor !== state.identityKey) state.setEdge(null, state.identityKey)
  })
}

/** A read's answer lands unless a newer read's answer already did, so a
 * read that fails after an older one succeeded keeps that answer. A
 * failure records the error, keeping the last good answer. A read started
 * before a change watchEdge sees lands nothing. */
export async function loadEdge(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  if (!servesEdge(s)) return
  if (s.edgeFor !== s.identityKey) s.setEdge(null, s.identityKey)
  const generation = generations.get(store) ?? 0
  const current = () => generations.get(store) === generation && read > (landed.get(store) ?? 0)
  let edge: ServerEdge
  try {
    edge = await client.serverEdge()
  } catch (err) {
    if (current() && reads.get(store) === read) store.getState().setEdgeError(message(err))
    throw err
  }
  if (current()) {
    landed.set(store, read)
    store.getState().setEdge(edge, s.identityKey)
  }
}
