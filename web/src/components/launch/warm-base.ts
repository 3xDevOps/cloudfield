import { useEffect, useRef } from 'react'
import { api } from '@/lib/api'

/** The server takes a launch within 10 seconds of a refresh without fetching the upstream again, and refreshes on a call from 5 seconds after one. */
const warmEvery = 5000

/** Refreshes the workspace's source mirror when the workspace is first named and, at most every 5 seconds, when `edit` changes, so a launch right after does not wait for the upstream. An empty workspace does nothing. */
export function useWarmBase(workspaceID: string, edit: unknown) {
  const last = useRef({ workspaceID: '', at: 0 })
  useEffect(() => {
    if (!workspaceID) return
    const now = Date.now()
    if (last.current.workspaceID === workspaceID && now - last.current.at < warmEvery) return
    last.current = { workspaceID, at: now }
    // The launch itself reports a source that cannot be fetched.
    api.workspaceMirrorWarm(workspaceID).catch(() => {})
  }, [workspaceID, edit])
}
