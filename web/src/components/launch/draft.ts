import type { Fallback, ChildRunsChoice } from '@/components/launch/launch-options'
import type { LimitsDraft } from '@/components/launch/run-limits'
import type { LaunchMode, LinearLink, Permission } from '@/lib/types'
import { message } from '@/lib/format'

export interface LaunchDraft {
  version: 1
  id: string
  workspace: string
  extras: string[]
  account: string
  fallback: Fallback
  permission: Permission
  childRuns: ChildRunsChoice
  limits: LimitsDraft
  harness: string
  mode: LaunchMode
  model: string
  effort: string
  task: string
  /** The Linear issue the launch will link; absent for none. */
  linear?: LinearLink
}

export const launchDraftLinkStatusPending = 'Could not confirm this browser gateway link; launch drafts are staying in memory.'

const storageKey = (identity: string) => `aether.launch-draft.v1:${encodeURIComponent(identity)}`
const strings = (value: unknown): value is string[] => Array.isArray(value) && value.every((item) => typeof item === 'string')

function validDraft(draft: LaunchDraft | null): draft is LaunchDraft {
  return !(!draft || draft.version !== 1 || !draft.id ||
    !['id', 'workspace', 'account', 'harness', 'model', 'effort', 'task'].every((key) => typeof draft[key as keyof LaunchDraft] === 'string') ||
    !strings(draft.extras) || !draft.fallback || typeof draft.fallback.off !== 'boolean' ||
    !(draft.fallback.order === null || strings(draft.fallback.order)) || !strings(draft.fallback.excluded) ||
    !['root', 'default', 'isolated'].includes(draft.permission) || !['', 'on', 'off'].includes(draft.childRuns) ||
    !(draft.linear === undefined || (typeof draft.linear?.identifier === 'string' && typeof draft.linear.url === 'string')) ||
    !['tui', 'acp', 'headless'].includes(draft.mode) || !draft.limits ||
    !['cpus', 'memory', 'pids'].every((key) => typeof draft.limits[key as keyof LimitsDraft] === 'string'))
}

export function readLaunchDraft(identity: string, persistent = true): { draft: LaunchDraft | null; saved: LaunchDraft[]; error: string | null } {
  if (!persistent) return { draft: null, saved: [], error: null }
  try {
    const stored = JSON.parse(localStorage.getItem(storageKey(identity)) ?? 'null')
    if (stored === null) return { draft: null, saved: [], error: null }
    if (validDraft(stored)) return { draft: stored, saved: [], error: null }
    if (stored.version === 2 && (stored.draft === null || validDraft(stored.draft)) &&
      Array.isArray(stored.saved) && stored.saved.every(validDraft)) {
      return { draft: stored.draft, saved: stored.saved, error: null }
    }
    return { draft: null, saved: [], error: 'Saved launch draft has an invalid format.' }
  } catch (error) {
    return { draft: null, saved: [], error: `Could not read saved launch draft: ${message(error)}` }
  }
}

export function saveLaunchDraft(identity: string | null, draft: LaunchDraft | null, persistent = true, saved: LaunchDraft[] = []): string | null {
  if (!identity) return null
  if (!persistent) return null
  try {
    if (saved.length) localStorage.setItem(storageKey(identity), JSON.stringify({ version: 2, draft, saved }))
    else if (draft) localStorage.setItem(storageKey(identity), JSON.stringify(draft))
    else localStorage.removeItem(storageKey(identity))
    return null
  } catch (error) { return message(error) }
}

export function clearLaunchDraft(identity: string | null): string | null {
  if (!identity) return null
  try {
    localStorage.removeItem(storageKey(identity))
    return null
  } catch (error) { return message(error) }
}
