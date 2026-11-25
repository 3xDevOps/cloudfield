import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearStatus } from '@/lib/types'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** linear.status for the caller; null before it is read or when the server
 * does not serve it. A failed read leaves connected true with the error: the
 * server answers a member without a key with connected false, so a failure
 * means a key is saved and the read failed (refused key, outage or timeout). The Linear nav entry follows it. */
export interface LinearSlice {
  linear: LinearStatus | null
  /** Counts Connect and Disconnect, so a read that started before one cannot overwrite it. */
  linearWrites: number
  setLinear: (linear: LinearStatus | null) => void
  writeLinear: (linear: LinearStatus | null) => void
}

export const createLinearSlice: SliceCreator<LinearSlice> = (set) => ({
  linear: null,
  linearWrites: 0,
  setLinear: (linear) => set({ linear }),
  writeLinear: (linear) => set((s) => ({ linear, linearWrites: s.linearWrites + 1 })),
})

export async function readLinear(client: Api): Promise<LinearStatus> {
  try {
    return await client.linearStatus()
  } catch (err) {
    return { connected: true, error: message(err) }
  }
}

export async function loadLinear(store: RootStore, client: Api): Promise<void> {
  const viewer = store.getState().identityKey
  if (!capability(store.getState().capabilities).hasMethod('linear.status')) {
    store.getState().setLinear(null)
    return
  }
  const writes = store.getState().linearWrites
  const status = await readLinear(client)
  const now = store.getState()
  if (now.identityKey === viewer && now.linearWrites === writes) now.setLinear(status)
}
