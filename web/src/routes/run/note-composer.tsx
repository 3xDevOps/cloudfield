import { useEffect, useRef } from 'react'
import { ArrowUp } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { RunRoom } from '@/routes/run/room'
import { useStore } from '@/store'

export function NoteComposer({ runID, room, focus, onFocused }: { runID: string; room: RunRoom; focus: boolean; onFocused: () => void }) {
  const body = useStore((s) => s.noteDrafts[runID] ?? '')
  const input = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (!focus) return
    // A frame later: the Details dialog focuses itself as it opens, after this effect.
    const frame = requestAnimationFrame(() => {
      input.current?.focus()
      onFocused()
    })
    return () => cancelAnimationFrame(frame)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focus])
  return (
    <form
      className="flex items-center gap-2"
      onSubmit={(event) => {
        event.preventDefault()
        if (body.trim()) void room.note(body.trim())
      }}
    >
      <Input ref={input} aria-label="Add a note" placeholder="Add a note for people on this session" value={body} onChange={(event) => useStore.getState().setNoteDraft(runID, event.target.value)} />
      <Button type="submit" size="round" variant="primary" label="Add note" disabled={!body.trim()}>
        <ArrowUp />
      </Button>
    </form>
  )
}
