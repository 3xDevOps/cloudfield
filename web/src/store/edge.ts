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

/** A read's answer lands unless a newer read's answer already did, so a
 * read that fails after an older one succeeded keeps that answer. A
 * failure records the error, keeping the last good answer for the same
 * viewer. A read for a viewer, or with the method, no longer current
 * lands nothing; it only clears what it would have left showing. */
export async function loadEdge(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  const viewer = s.identityKey
  const serves = (state: typeof s) => capability(state.capabilities).hasMethod('server.edge')
  if (!serves(s) || s.edgeFor !== viewer) {
    // No read started before this one may land.
    landed.set(store, read - 1)
    s.setEdge(null, viewer)
  }
  if (!serves(s)) return
  const current = () => {
    const now = store.getState()
    if (now.identityKey === viewer && serves(now)) return read > (landed.get(store) ?? 0)
    if (now.edgeFor === viewer || !serves(now)) now.setEdge(null, now.identityKey)
    return false
  }
  let edge: ServerEdge
  try {
    edge = await client.serverEdge()
  } catch (err) {
    if (current() && reads.get(store) === read) store.getState().setEdgeError(message(err))
    throw err
  }
  if (current()) {
    landed.set(store, read)
    store.getState().setEdge(edge, viewer)
  }
}
