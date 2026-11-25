import type { Capability } from '@/lib/capability'
import type { Run } from '@/lib/types'
import { isLiveRun } from '@/routes/files/sources'
import { isTerminal, type RunRecord } from '@/store/runs'
import type { Route } from '@/store/ui'

export const runViews = ['session', 'trace', 'terminal', 'changes', 'files', 'browser'] as const

export type RunView = (typeof runViews)[number]

export const runViewLabel: Record<RunView, string> = {
  session: 'Chat',
  trace: 'Trace',
  terminal: 'Terminal',
  changes: 'Changes',
  files: 'Files',
  browser: 'Browser',
}

export function isRunView(value: string | undefined): value is RunView {
  return (runViews as readonly string[]).includes(value ?? '')
}

export function isRunRoute(route: Route, runID: string): boolean {
  return route.name === 'run' && route.params.runId === runID
}

export function runRoute(runID: string, view?: RunView): Route {
  return { name: 'run', params: view ? { runId: runID, view } : { runId: runID } }
}

export function defaultView(run: { mode: string; acp?: boolean }): RunView {
  return run.mode === 'acp' || run.acp ? 'session' : 'terminal'
}

/** A live run on a gateway that serves the browser has a Browser view. */
export function hasBrowser(run: Pick<RunRecord, 'status'>, cap: Capability): boolean {
  return cap.hasMethod('dev.browser.status') && !isTerminal(run.status)
}

/** A run that still has its checkout, on a gateway that serves it, has a Files view. */
export function hasFiles(run: Run, cap: Capability): boolean {
  return cap.hasMethod('files.tree') && isLiveRun(run)
}

export function offeredViews(run: RunRecord, cap: Capability): RunView[] {
  return runViews.filter((view) =>
    (view !== 'browser' || hasBrowser(run, cap)) && (view !== 'files' || hasFiles(run, cap)) && (view !== 'trace' || run.acp === true))
}
