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

function readLaunchDraft(identity: string): { draft: LaunchDraft | null; saved: LaunchDraft[]; error: string | null } {
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

export function saveLaunchDraft(
  identity: string | null,
  draft: LaunchDraft | null,
  persistent: boolean,
  saved: LaunchDraft[],
  changeSaved: (current: LaunchDraft[]) => LaunchDraft[] = (current) => current,
): { saved: LaunchDraft[]; error: string | null } {
  if (!identity || !persistent) return { saved: changeSaved(saved), error: null }
  const stored = readLaunchDraft(identity)
  const next = changeSaved(stored.error ? saved : stored.saved)
  try {
    if (next.length) localStorage.setItem(storageKey(identity), JSON.stringify({ version: 2, draft, saved: next }))
    else if (draft) localStorage.setItem(storageKey(identity), JSON.stringify(draft))
    else localStorage.removeItem(storageKey(identity))
    return { saved: next, error: null }
  } catch (error) { return { saved, error: message(error) } }
}

export function recoverLaunchDrafts(
  identity: string,
  active: LaunchDraft | null,
  savedInMemory: LaunchDraft[],
) {
  const restored = readLaunchDraft(identity)
  if (!savedInMemory.length || restored.error) {
    return {
      launchDraft: active ?? restored.draft,
      savedLaunchDrafts: restored.error ? savedInMemory : restored.saved,
      launchDraftError: restored.error,
    }
  }
  const launchDraft = active ?? restored.draft
  const saved = [...savedInMemory, ...restored.saved.filter(({ id }) => !savedInMemory.some((draft) => draft.id === id))]
  const write = saveLaunchDraft(identity, launchDraft, true, saved, () => saved)
  return { launchDraft, savedLaunchDrafts: saved, launchDraftError: write.error }
}

export function clearLaunchDraft(identity: string | null): string | null {
  if (!identity) return null
  try {
    localStorage.removeItem(storageKey(identity))
    return null
  } catch (error) { return message(error) }
}
