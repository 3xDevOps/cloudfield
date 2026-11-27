import { useEffect, useRef } from 'react'
import { api } from '@/lib/api'

/** The server takes a launch within 10 seconds of a refresh without fetching the upstream again, and refreshes on a call from 5 seconds after one. */
const warmEvery = 5000

/** Refreshes the workspace's source mirror when the workspace is first named and, at most every 5 seconds, when `edit` changes, so a launch right after does not wait for the upstream. One call runs at a time, and none follows a failed one until the workspace changes or is emptied, as a closed panel does. */
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
