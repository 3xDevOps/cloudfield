import { ApiError, codeUnavailable, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearStatus } from '@/lib/types'
import type { RootStore } from '@/store'
import { capability } from '@/store/hooks'
import type { SliceCreator } from '@/store/slice'

/** linear.status for the caller; null before it is read or when the server
 * does not serve it. A failed read has `error`. The server answers an empty
 * status when no key is saved, so any error carrying its own "linear: " text
 * means a key is saved: connected stays true, and `refused` tells Linear
 * refusing the key from Linear being unreachable. An unconfigured service or
 * an error that is not the server's (a proxy, a dropped connection) leaves
 * connected false, since nothing says a key exists. */
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

const serverText = /(?:^|: )linear: (.*)$/s
const refusal = /^Linear refused the API key: /

export async function readLinear(client: Api): Promise<LinearStatus> {
  try {
    return await client.linearStatus()
  } catch (err) {
    const error = message(err)
    const linear = err instanceof ApiError && err.code !== undefined && err.code !== codeUnavailable ? serverText.exec(error) : null
    if (!linear) return { connected: false, error }
    return { connected: true, error, refused: refusal.test(linear[1]!) }
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
