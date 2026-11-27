import { useRef, useState } from 'react'
import type { LaunchDraft } from '@/components/launch/draft'
import { Plus, Trash2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { dialogOpener, handOverFocus, useReturnFocus } from '@/lib/hooks'
import { toast } from 'sonner'
import { useStore } from '@/store'

function draftTitle(draft: LaunchDraft) {
  return draft.task.trim().split('\n', 1)[0] || draft.linear?.identifier || 'Untitled draft'
}

export function DraftPicker() {
  const drafts = useStore((s) => s.savedLaunchDrafts)
  const workspaces = useStore((s) => s.workspaces)
  const close = useStore((s) => s.closePaletteDialog)
  const [opener] = useState(() => dialogOpener(document.activeElement))
  const returnFocus = useReturnFocus()
  const deleteButtons = useRef(new Map<string, HTMLButtonElement>())
  const startButton = useRef<HTMLButtonElement>(null)
  const [deleting, setDeleting] = useState<string | null>(null)

  const start = () => {
    handOverFocus(opener)
    useStore.getState().openLaunchWithTask('')
  }

  const take = (draft: LaunchDraft, resume: boolean) => {
    const state = useStore.getState()
    const error = state.updateLaunchDrafts((current) => {
      const selected = current.find(({ id }) => id === draft.id)
      return {
        ...(resume && selected ? { draft: selected } : {}),
        saved: current.filter(({ id }) => id !== draft.id),
      }
    })
    if (error) {
      toast.error(`Could not ${resume ? 'continue' : 'delete'} draft: ${error}`)
      return
    }
    const next = useStore.getState()
    if (resume && next.launchDraft?.id === draft.id) {
      start()
      return
    }
    if (resume) toast.error('This draft is no longer available.')
    if (!next.savedLaunchDrafts.length) close()
    else {
      setDeleting(null)
      startButton.current?.focus()
    }
  }

  return (
    <Dialog open onOpenChange={(open) => { if (!open) close() }}>
      <DialogContent {...returnFocus} className="grid-rows-[auto_minmax(0,1fr)_auto]" onCloseAutoFocus={(event) => {
        if (useStore.getState().paletteDialog === 'launch') event.preventDefault()
        else returnFocus.onCloseAutoFocus(event)
      }}>
        <DialogHeader>
          <DialogTitle>New session</DialogTitle>
          <DialogDescription>Continue a saved draft or start a new session.</DialogDescription>
        </DialogHeader>
        <ul aria-label="Saved drafts" className="min-w-0 divide-y divide-seam overflow-y-auto rounded-panel border border-seam">
          {drafts.map((draft) => (
            <li key={draft.id} className="flex min-w-0 flex-wrap items-center justify-between gap-2 p-3">
              <div className="min-w-0 flex-1 basis-36">
                <p className="line-clamp-2 break-words font-medium">{draftTitle(draft)}</p>
                <p className="truncate text-ui-sm text-muted">{[workspaces[draft.workspace]?.name, draft.harness].filter(Boolean).join(' · ')}</p>
              </div>
              <div className="flex flex-wrap gap-2">
                {deleting === draft.id ? (
                  <>
                    <Button size="sm" variant="secondary" autoFocus onClick={() => { setDeleting(null); requestAnimationFrame(() => deleteButtons.current.get(draft.id)?.focus()) }}>Keep draft</Button>
                    <Button size="sm" variant="danger" onClick={() => take(draft, false)}>Delete draft</Button>
                  </>
                ) : (
                  <>
                    <Button size="sm" variant="secondary" aria-label={`Continue draft: ${draftTitle(draft)}`} onClick={() => take(draft, true)}>Continue</Button>
                    <Button size="icon-sm" variant="ghost" label={`Delete draft: ${draftTitle(draft)}`} ref={(button) => { if (button) deleteButtons.current.set(draft.id, button); else deleteButtons.current.delete(draft.id) }} onClick={() => setDeleting(draft.id)}><Trash2 /></Button>
                  </>
                )}
              </div>
            </li>
          ))}
        </ul>
        <DialogFooter><Button ref={startButton} onClick={start}><Plus /> Start new session</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
