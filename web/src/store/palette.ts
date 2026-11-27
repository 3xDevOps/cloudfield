import type { Command } from '@/lib/commands'
import type { CloseOutcome, LinearLink, RunCloseCheck, WatchTarget } from '@/lib/types'
import { saveLaunchDraft, type LaunchDraft } from '@/components/launch/draft'
import type { SliceCreator } from '@/store/slice'

/** Every form the shell hosts; sweep this list rather than keep a copy. */
export const paletteDialogs = ['launch', 'launch-drafts', 'inject', 'forward', 'close', 'clear-done', 'release-finished', 'watch'] as const

export type PaletteDialog = (typeof paletteDialogs)[number]

export interface PaletteSlice {
  paletteOpen: boolean
  paletteDialog: PaletteDialog | null
  /** Only run-specific forms carry a run id. */
  paletteRunID: string | null
  paletteForwardTarget: string | null
  /** The pull request the Watch form is for; a run's comes from paletteRunID. */
  paletteWatchTarget: WatchTarget | null
  /** The task the launch form opens with: text shared to Aether. */
  paletteLaunchTask: string
  /** The Linear issue a launch from the Linear page links to; null for none. */
  paletteLaunchLinear: LinearLink | null
  launchDraft: LaunchDraft | null
  savedLaunchDrafts: LaunchDraft[]
  updateLaunchDrafts: (draft: LaunchDraft | null, changeSaved: (current: LaunchDraft[]) => LaunchDraft[]) => string | null
  launchDraftError: string | null
  setLaunchDraft: (draft: LaunchDraft | null, persist?: boolean) => string | null
  clearLaunchDraftError: () => void
  /** A verb that closes a run with close warnings, held until the owner confirms. */
  closeConfirmation: { check: RunCloseCheck; asked: string; outcome: CloseOutcome; command: Command } | null
  /** Runs whose close check is in flight, so a second press does not check again. */
  closeChecking: Record<string, true>
  togglePalette: (open?: boolean) => void
  openPaletteDialog: (dialog: PaletteDialog, runID?: string) => void
  openLaunchWithTask: (task: string, linear?: LinearLink) => void
  openForwardDialog: (target: string) => void
  openWatchDialog: (target: WatchTarget) => void
  closePaletteDialog: () => void
  openCloseConfirmation: (confirmation: { check: RunCloseCheck; asked: string; outcome: CloseOutcome; command: Command }) => void
  dismissCloseConfirmation: () => void
  setCloseChecking: (runID: string, checking: boolean) => void
}

export const createPaletteSlice: SliceCreator<PaletteSlice> = (set, get) => ({
  paletteOpen: false,
  paletteDialog: null,
  paletteRunID: null,
  paletteForwardTarget: null,
  paletteWatchTarget: null,
  paletteLaunchTask: '',
  paletteLaunchLinear: null,
  launchDraft: null,
  savedLaunchDrafts: [],
  updateLaunchDrafts: (launchDraft, changeSaved) => {
    const state = get()
    const linkStatusPending = state.capabilities === null || (state.capabilities.local?.includes('link.status') && !state.linkStatus)
    const { saved, error } = saveLaunchDraft(state.identityKey, launchDraft, !state.linkStatus?.edge_url && !linkStatusPending, state.savedLaunchDrafts, changeSaved)
    if (!error) set({ launchDraft, savedLaunchDrafts: saved })
    return error
  },
  launchDraftError: null,
  setLaunchDraft: (launchDraft, persist = true) => {
    const state = get()
    const linkStatusPending = state.capabilities === null || (state.capabilities.local?.includes('link.status') && !state.linkStatus)
    const { saved, error } = saveLaunchDraft(state.identityKey, launchDraft, persist && !state.linkStatus?.edge_url && !linkStatusPending, state.savedLaunchDrafts)
    set({
      launchDraft,
      savedLaunchDrafts: saved,
      launchDraftError: error ? `Could not save launch draft: ${error}` : persist ? null : state.launchDraftError,
    })
    return error
  },
  clearLaunchDraftError: () => set({ launchDraftError: null }),
  closeConfirmation: null,
  closeChecking: {},
  togglePalette: (open) => set((s) => ({ paletteOpen: open ?? !s.paletteOpen })),
  openPaletteDialog: (dialog, runID) =>
    set((s) => ({
      paletteOpen: false,
      paletteDialog: dialog === 'launch' && !s.launchDraft && s.savedLaunchDrafts.length ? 'launch-drafts' : dialog,
      paletteRunID: runID ?? null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
      paletteLaunchTask: '',
      paletteLaunchLinear: null,
    })),
  openLaunchWithTask: (task, linear) =>
    set({
      paletteOpen: false,
      paletteDialog: 'launch',
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
      paletteLaunchTask: task,
      paletteLaunchLinear: linear ?? null,
    }),
  openForwardDialog: (target) =>
    set({
      paletteOpen: false,
      paletteDialog: 'forward',
      paletteRunID: null,
      paletteForwardTarget: target,
      paletteWatchTarget: null,
    }),
  openWatchDialog: (target) =>
    set({
      paletteOpen: false,
      paletteDialog: 'watch',
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: target,
    }),
  closePaletteDialog: () =>
    set({
      paletteDialog: null,
      paletteRunID: null,
      paletteForwardTarget: null,
      paletteWatchTarget: null,
      paletteLaunchTask: '',
      paletteLaunchLinear: null,
    }),
  // A later check's answer never replaces the confirmation on screen, whose
  // Close anyway would then run another session's close.
  openCloseConfirmation: (confirmation) => set((s) => (s.closeConfirmation ? {} : { paletteOpen: false, closeConfirmation: confirmation })),
  dismissCloseConfirmation: () => set({ closeConfirmation: null }),
  setCloseChecking: (runID, checking) =>
    set((s) => {
      const closeChecking = { ...s.closeChecking }
      if (checking) closeChecking[runID] = true
      else delete closeChecking[runID]
      return { closeChecking }
    }),
})
