import { useEffect, useRef } from 'react'
import { api } from '@/lib/api'

/** The server refreshes on a call from 5 seconds after its last refresh. */
const warmEvery = 5000

export function useWarmBase(workspaceID: string, edit: unknown) {
  const next = useRef({ workspaceID: '', at: 0 })
  useEffect(() => {
    if (!workspaceID) {
      next.current = { workspaceID: '', at: 0 }
      return
    }
    const sent = Date.now()
    if (next.current.workspaceID === workspaceID && sent < next.current.at) return
    const call = { workspaceID, at: Infinity }
    next.current = call
    // A source that cannot be fetched holds a server-wide lock for each try; the launch itself reports it.
    api.workspaceMirrorWarm(workspaceID).then(
      () => {
        call.at = sent + warmEvery
      },
      () => {},
    )
  }, [workspaceID, edit])
}
