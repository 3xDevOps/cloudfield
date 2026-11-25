import { isRunLocal } from '@/routes/browser/address'
import { openInFiles } from '@/routes/files/open'
import { parsePath } from '@/routes/files/paths'
import { hasBrowser, hasFiles } from '@/routes/run/views'
import { useStore } from '@/store'
import { capability } from '@/lib/capability'

/** Hands a link printed in a run's terminal to the run: a run-local address
 * to its Browser, a path in its checkout to its Files view. False for any
 * other link, or when the run has no such view, so the caller opens it the
 * ordinary way. */
export function openRunLink(runID: string, uri: string): boolean {
  const state = useStore.getState()
  const run = state.runs[runID]
  if (!run) return false
  const cap = capability(state.capabilities)
  if (isRunLocal(uri) && hasBrowser(run, cap)) {
    state.requestBrowser(runID, uri)
    return true
  }
  const file = parsePath(uri)
  if (!file || !hasFiles(run, cap)) return false
  openInFiles(runID, file)
  return true
}
