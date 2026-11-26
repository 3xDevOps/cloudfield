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

/** Only the latest read lands. A failed read keeps the last good answer for
 * the same viewer and records the error; a read for another viewer, or a
 * viewer change while it waits, clears the slice. */
export async function loadEdge(store: RootStore, client: Api): Promise<void> {
  const read = (reads.get(store) ?? 0) + 1
  reads.set(store, read)
  const s = store.getState()
  const servesEdge = capability(s.capabilities).hasMethod('server.edge')
  if (!servesEdge || s.edgeFor !== s.identityKey) s.setEdge(null, s.identityKey)
  if (!servesEdge) return
  let edge: ServerEdge
  try {
    edge = await client.serverEdge()
  } catch (err) {
    const now = store.getState()
    if (reads.get(store) === read) {
      if (now.identityKey === s.identityKey) now.setEdgeError(message(err))
      else now.setEdge(null, now.identityKey)
    }
    throw err
  }
  const now = store.getState()
  if (reads.get(store) === read) now.setEdge(now.identityKey === s.identityKey ? edge : null, now.identityKey)
}
