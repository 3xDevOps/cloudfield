import { codeUnavailable, type ApiError, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearStatus } from '@/lib/types'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** linear.status for the caller; null before it is read or when the server
 * does not serve it. A failed read has `error`. Only Linear refusing the saved
 * key leaves connected true, since a key is then saved; any other failure
 * (service not configured, outage, timeout) leaves connected false, so the
 * Linear nav entry and a Disconnect row stay away while the error shows. */
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

const refusal = /authenticat|unauthori[sz]ed|forbidden|\b40[13]\b/i

export async function readLinear(client: Api): Promise<LinearStatus> {
  try {
    return await client.linearStatus()
  } catch (err) {
    const error = message(err)
    return { connected: (err as ApiError).code !== codeUnavailable && refusal.test(error), error }
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
