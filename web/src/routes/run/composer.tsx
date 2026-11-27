import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { DictationButton } from '@/components/dictation/dictation-button'
import { DictationNotice } from '@/components/dictation/dictation-notice'
import { useDictationState } from '@/components/dictation/state'
import { ArrowUp, CornerUpRight, ListPlus, Play, RefreshCw, Square } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Textarea } from '@/components/ui/textarea'
import { Tooltip } from '@/components/ui/tooltip'
import { api } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { canReopenRun } from '@/lib/commands'
import { useFileDrop } from '@/lib/file-drag'
import { coarsePointer, useMediaQuery } from '@/lib/hooks'
import { isPress, shortcutLabel, useKeybindings } from '@/lib/keybindings'
import { message } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import type { ConfigOption } from '@/lib/session-types'
import { notFinishedYet } from '@/lib/status'
import { useImplicitControl, type AgentTerminal } from '@/routes/run/agent-terminal'
import { AttachmentChips } from '@/routes/run/composer-attachments'
import { AttachButton, DropOverlay, dropRoom } from '@/routes/run/composer-dropzone'
import { hasImages, useComposerImages, type ComposerImageState } from '@/routes/run/composer-images'
import { ContextWindow } from '@/routes/run/context-window'
import { usePromptHistory } from '@/routes/run/composer-history'
import { commandSuggestions, currentChoice, OptionPills, SuggestionList, triggerAt, useFileSuggestions, type Suggestion } from '@/routes/run/composer-menus'
import { beginDraftRequest, composerBlock, composerDraft, draftSendKey, enhancedBlock, pillFor, pillHint, saveComposerDraft, sentDraft, useComposerDraft, type ComposerDraft, type Pill } from '@/routes/run/composer-state'
import { TemplateInsert } from '@/routes/run/composer-templates'
import type { RunRoom } from '@/routes/run/room'
import { TypingIndicator, useTypingSignal, useWakeOnTyping } from '@/routes/run/typing'
import { sessionColumn } from '@/routes/run/session-rows'
import { sameIdentity, useStore } from '@/store'
import { useCapability, useSelf } from '@/store/hooks'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'
import type { RunRecord } from '@/store/runs'
import { sessionLease } from '@/store/session-stream'
import { backgroundWait } from '@/store/sessions'
import { cn } from '@/lib/utils'

interface ComposerProps {
  run: RunRecord
  agent: AgentTerminal
  room: RunRoom
  textarea: React.RefObject<HTMLTextAreaElement | null>
  onFocusChange: (focused: boolean) => void
  /** The gate can keep the box unmounted, so focusing waits for it to mount. */
  autoFocus?: boolean
  /** One line with a send button, for a docked box nobody has opened. The field is the same one, so a press on it keeps its focus when the box opens. */
  collapsed?: boolean
  /** Under a view other than Chat: the frame spans its tile below a seam. */
  docked?: boolean
  dock?: React.ReactNode
  onEscape: () => void
  onDropStatus?: (status: { limit: number; blocked?: string; upload?: (files: File[]) => Promise<void> }) => void
}

function ComposerFrame({ docked, children }: { docked?: boolean; children: React.ReactNode }) {
  return (
    <div className={cn('shrink-0 bg-canvas pb-[max(0px,calc(var(--keyboard-inset,0px)-env(safe-area-inset-bottom)))]', docked && 'border-t border-seam')}>
      <div className={cn('flex flex-col gap-2', docked ? 'px-4 pt-2 pb-4' : cn(sessionColumn, 'pb-4'))}>{children}</div>
    </div>
  )
}

function Closed({ runID, reason, action, failure }: { runID: string; reason: string; action?: React.ReactNode; failure?: string }) {
  return (
    <>
      {failure && <p role="alert" className="text-ui-sm text-state-failed">{failure}</p>}
      <TypingIndicator runID={runID} />
      <div className="flex min-h-14 min-w-0 items-center gap-2 rounded-composer border border-seam bg-chrome py-2 pl-4 pr-2">
        <p role="status" className="min-w-0 flex-1 text-ui-sm text-muted">{reason}</p>
        {action}
      </div>
    </>
  )
}

function ClosedRun({ run, reason, dock }: { run: RunRecord; reason: string; dock?: React.ReactNode }) {
  const [busy, setBusy] = useState(false)
  const [failure, setFailure] = useState<string>()
  const relaunch = async () => {
    const same = sameIdentity()
    setBusy(true)
    setFailure(undefined)
    try {
      const relaunched = await api.runRelaunch(run.id)
      if (same()) useStore.getState().upsertRun(relaunched)
    } catch (err) {
      setFailure(message(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <ComposerFrame>
      {dock}
      <Closed
        runID={run.id}
        reason={reason}
        failure={failure}
        action={<Button variant="primary" disabled={busy} onClick={() => void relaunch()}><RefreshCw aria-hidden="true" />{busy ? 'Relaunching…' : 'Relaunch'}</Button>}
      />
    </ComposerFrame>
  )
}

const optionUnsent = 'This tab did not get control of the session, so the option was not changed.'

const pillLook: Record<Pill, { label: string; Icon: typeof ArrowUp; variant: 'primary' | 'danger' }> = {
  send: { label: 'Send', Icon: ArrowUp, variant: 'primary' },
  steer: { label: 'Steer', Icon: CornerUpRight, variant: 'primary' },
  queue: { label: 'Queue', Icon: ListPlus, variant: 'primary' },
  interrupt: { label: 'Interrupt', Icon: Square, variant: 'danger' },
  'stop-waiting': { label: 'Stop waiting', Icon: Square, variant: 'danger' },
  resume: { label: 'Resume', Icon: Play, variant: 'primary' },
}

function ComposerBox({ textarea, autoFocus, collapsed, value, onChange, onCaret, onFocusChange, onSend, onQueue, onEscape, placeholder, describedBy, menu, onKeyDown, combobox, onPaste, readOnly, images, attach, footer }: {
  textarea: React.RefObject<HTMLTextAreaElement | null>
  autoFocus?: boolean
  collapsed?: boolean
  value: string
  onChange: (value: string, caret: number) => void
  onCaret?: (caret: number) => void
  onFocusChange: (focused: boolean) => void
  onSend: () => void
  onQueue?: () => void
  onEscape: () => void
  placeholder: string
  describedBy: string
  menu?: React.ReactNode
  onKeyDown?: (event: React.KeyboardEvent<HTMLTextAreaElement>) => void
  combobox?: { expanded: boolean; controls: string; active?: string }
  onPaste?: (event: React.ClipboardEvent<HTMLTextAreaElement>) => void
  readOnly?: boolean
  images?: React.ReactNode
  /** What a file dropped on the box joins, and whether the box takes none now. */
  attach: { images: ComposerImageState; disabled: boolean }
  footer: React.ReactNode
}) {
  const coarse = useMediaQuery(coarsePointer)
  const room = dropRoom(attach.images, attach.disabled)
  const drop = useFileDrop({ onFiles: (files) => void attach.images.upload(files), limit: room.limit })
  const [focused, setFocused] = useState(false)
  const caretPlaced = useRef(false)
  const dictating = useDictationState(textarea).active
  const more = value.trimEnd().split('\n').length - 1
  useEffect(() => {
    if (autoFocus) textarea.current?.focus()
  }, [autoFocus, textarea])
  // A draft shows its first line.
  useLayoutEffect(() => {
    if (collapsed) textarea.current?.scrollTo(0, 0)
  }, [collapsed, textarea])
  // The box opens with its footer where the one line was pressed: for 400ms or until the pointer moves the footer takes no press, or a double-click would open a menu.
  const [was, setWas] = useState(collapsed)
  const [opening, setOpening] = useState(false)
  if (was !== collapsed) {
    setWas(collapsed)
    setOpening(!collapsed)
  }
  useEffect(() => {
    if (!opening) return
    const done = () => setOpening(false)
    const timer = setTimeout(done, 400)
    window.addEventListener('pointermove', done, { once: true })
    return () => {
      clearTimeout(timer)
      window.removeEventListener('pointermove', done)
    }
  }, [opening])
  // The table stands plain keys down inside a field, so the textarea answers
  // Enter itself; this registration keeps Send listed in the shortcuts reference.
  useKeybindings('composer', focused && !coarse && !dictating ? {
    'composer-send': (event) => {
      event.preventDefault()
      onSend()
    },
    ...(onQueue ? {
      'composer-queue': (event: KeyboardEvent) => {
        event.preventDefault()
        onQueue()
      },
    } : {}),
  } : {})
  return (
    <div className="relative" {...drop.handlers}>
      <DictationNotice textarea={textarea} className="mb-2" />
      {menu}
      {drop.count !== null && <DropOverlay count={drop.count} refused={drop.refused} blocked={room.blocked} limit={room.limit} />}
      <div
        className={cn('@container overflow-hidden rounded-composer border border-seam bg-chrome', focused && 'border-control', collapsed && 'flex items-center pr-1')}
        onMouseDown={(event) => {
          if (opening && event.target === event.currentTarget) event.preventDefault()
        }}
      >
      {!collapsed && images}
      <Textarea
        variant="embedded"
        ref={textarea}
        aria-label="Message the agent"
        aria-describedby={describedBy}
        role={combobox ? 'combobox' : undefined}
        aria-expanded={combobox?.expanded}
        aria-controls={combobox?.expanded ? combobox.controls : undefined}
        aria-activedescendant={combobox?.active}
        aria-autocomplete={combobox ? 'list' : undefined}
        rows={collapsed ? 1 : 2}
        value={value}
        readOnly={readOnly}
        onPaste={onPaste}
        placeholder={collapsed ? 'Message the agent' : placeholder}
        className={cn('resize-none', collapsed ? 'my-2 h-5 min-h-0 flex-1 overflow-hidden py-0 whitespace-pre [mask-image:linear-gradient(to_right,black_calc(100%-1.5rem),transparent)] coarse:h-5' : '[field-sizing:content] max-md:[--composer-lines:7.5rem]')}
        style={{ maxHeight: 'var(--composer-lines, 12rem)' }}
        onFocus={(event) => {
          // A textarea that mounts with a draft starts its caret before the text.
          if (!caretPlaced.current) {
            caretPlaced.current = true
            event.target.setSelectionRange(value.length, value.length)
            onCaret?.(value.length)
          }
          setFocused(true)
          onFocusChange(true)
        }}
        onSelect={(event) => onCaret?.(event.currentTarget.selectionStart)}
        onBlur={() => {
          setFocused(false)
          onFocusChange(false)
        }}
        onChange={(event) => onChange(event.target.value, event.target.selectionStart)}
        onKeyDown={(event) => {
          onKeyDown?.(event)
          if (event.defaultPrevented || event.nativeEvent.isComposing) return
          if (!coarse && isPress(event.nativeEvent, 'composer-send')) {
            event.preventDefault()
            if (!dictating) onSend()
          } else if (event.key === 'Escape' && !value.trim()) {
            event.preventDefault()
            onEscape()
          }
        }}
      />
      {collapsed ? (
        <>
          {more > 0 && <span className="shrink-0 px-1 text-ui-sm text-muted">+{more} {more === 1 ? 'line' : 'lines'}</span>}
          <Button variant="ghost" size="icon-sm" label="Send" disabled={!value.trim() || readOnly} onClick={onSend}>
            <ArrowUp />
          </Button>
        </>
      ) : (
        <div className={cn('wrap-from-end min-w-0 items-center gap-1 px-2 pb-2', opening && 'pointer-events-none')}>{footer}</div>
      )}
      </div>
    </div>
  )
}

function ComposerHint({ id, uploading, description }: { id: string; uploading: boolean; description: string }) {
  return uploading
    ? <p id={id} role="status" className="line-clamp-2 px-4 text-ui-sm text-muted">Uploading…</p>
    : <p id={id} className="sr-only">{description}</p>
}

function SendActions({ pill, label, hint, description, disabled, onClick, onCancel, dictation }: {
  pill: Pill
  label: string
  hint?: string
  description: string
  disabled: boolean
  onClick: () => void
  onCancel?: () => void
  dictation: React.ReactNode
}) {
  const look = pillLook[pill]
  const tip = [hint ? `${look.label} (${hint})` : look.label, description].filter(Boolean).join('. ')
  const stop = pill === 'interrupt' ? 'Interrupt the agent' : pill === 'stop-waiting' ? 'Stop waiting for background work' : undefined
  const icon = <look.Icon className={cn(stop && 'size-3 fill-current coarse:size-4')} />
  return (
    <div className="ml-auto flex shrink-0 items-center gap-1">
      {dictation}
      {onCancel && <Button size="sm" variant="ghost" onClick={onCancel}>Cancel</Button>}
      <Tooltip content={tip}>
        <span className="inline-flex">
          {(pill === 'send' || stop) && label === look.label ? (
            <Button size="round" variant={look.variant} label={stop ?? look.label} hint={false} disabled={disabled} onClick={onClick}>
              {icon}
            </Button>
          ) : (
            <Button
              size="pill"
              variant={look.variant}
              hint={false}
              aria-label={stop}
              disabled={disabled}
              onClick={onClick}
            >
              {icon}
              {label}
            </Button>
          )}
        </span>
      </Tooltip>
    </div>
  )
}

function StandardComposer({ run, agent, room, textarea, autoFocus, onFocusChange, onEscape, onDropStatus }: ComposerProps) {
  const self = useSelf(run.workspace_id)
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const coarse = useMediaQuery(coarsePointer)
  const draft = useComposerDraft(run.id)
  const { body } = draft
  useTypingSignal(run.id, body)
  const busy = Boolean(draft.busy)
  const late = usePastDeadline(busy)
  const dictating = useDictationState(textarea).active
  const hintID = useId()
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const wakeOnTyping = useWakeOnTyping(run, maySteer)
  const block = composerBlock(run, maySteer, maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run))
  const images = useComposerImages(run.id, !block && cap.hasMethod('terminal.file'))
  const attachOff = busy || !cap.hasMethod('terminal.file')
  const drop = dropRoom(images, Boolean(block) || attachOff)
  useEffect(() => onDropStatus?.({ ...drop, upload: images.upload }), [drop.blocked, drop.limit, images.upload, onDropStatus])
  const { uploading, uploadError } = images
  const control = useImplicitControl(run, agent)
  const hint = !control.waits
    ? 'Sends to the agent’s terminal.'
    : 'Delivers in 45 s unless the controller decides sooner.'
  const error = uploadError ?? (room.errorFromComposer ? room.error : undefined)

  const send = () => {
    if (!body.trim() || busy || !images.isReady()) return
    const post = async () => {
      const latest = composerDraft(run.id)
      const text = latest.body.trim()
      const attachments = images.getPaths()
      if (!text || latest.busy || !images.isReady()) return
      const idempotency = draftSendKey(latest, text, attachments)
      const request = beginDraftRequest(run.id, 'busy', { idempotency, error: undefined })
      const posted = await room.post({ kind: 'steer_request', body: text, attachments, key: idempotency.key })
      request.settle(posted ? sentDraft : {})
    }
    if (control.canAct) control.withControl(() => void post())
    else void post()
  }

  if (block) return <ComposerFrame><Closed runID={run.id} reason={block} /></ComposerFrame>

  return (
    <ComposerFrame>
      {error && (
        <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <Button size="sm" variant="ghost" onClick={() => {
            images.clearError()
            if (room.errorFromComposer) room.clearError()
          }}>Dismiss</Button>
        </div>
      )}
      <TypingIndicator runID={run.id} />
      <ComposerBox
        textarea={textarea}
        autoFocus={autoFocus}
        value={body}
        readOnly={Boolean(draft.busy)}
        onChange={(next) => {
          saveComposerDraft(run.id, { body: next })
          wakeOnTyping(next)
        }}
        onFocusChange={onFocusChange}
        onSend={send}
        onEscape={onEscape}
        placeholder={notFinishedYet(run) ? 'Say what is left to do' : 'Message the agent'}
        describedBy={hintID}
        onPaste={images.onPaste}
        images={<AttachmentChips images={images} disabled={busy} />}
        attach={{ images, disabled: attachOff }}
        footer={(
          <>
            <div className="flex items-center gap-1">
              <AttachButton images={images} disabled={attachOff} textarea={textarea} />
              <TemplateInsert run={run} textarea={textarea} disabled={busy} />
            </div>
            <SendActions
              pill="send"
              label={late ? stillWaiting : busy ? 'Sending…' : 'Send'}
              hint={coarse ? undefined : shortcutLabel('composer-send')}
              description={hint}
              disabled={!body.trim() || busy || !images.ready || dictating}
              onClick={send}
              onCancel={late ? () => saveComposerDraft(run.id, { busy: undefined }) : undefined}
              dictation={<DictationButton textarea={textarea} onInsert={(next) => saveComposerDraft(run.id, { body: next })} />}
            />
          </>
        )}
      />
      <ComposerHint id={hintID} uploading={uploading} description={hint} />
    </ComposerFrame>
  )
}

function useQueueHeld(active: boolean): boolean {
  const [held, setHeld] = useState(false)
  useEffect(() => {
    if (!active) {
      setHeld(false)
      return
    }
    const update = (event: KeyboardEvent) => setHeld((event.metaKey || event.ctrlKey) && event.shiftKey)
    const clear = () => setHeld(false)
    window.addEventListener('keydown', update)
    window.addEventListener('keyup', update)
    window.addEventListener('blur', clear)
    return () => {
      window.removeEventListener('keydown', update)
      window.removeEventListener('keyup', update)
      window.removeEventListener('blur', clear)
    }
  }, [active])
  return held
}

function EnhancedComposer({ run, agent, textarea, autoFocus, collapsed, docked, onFocusChange, dock, onEscape, onDropStatus }: ComposerProps) {
  const mobile = useIsMobile()
  const self = useSelf(run.workspace_id)
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const session = useStore((s) => s.acpSessions[run.id])
  const paused = useStore((s) => s.pausedRuns[run.id] ?? run.paused ?? false)
  const control = useImplicitControl(run, agent)
  const coarse = useMediaQuery(coarsePointer)
  const draft = useComposerDraft(run.id)
  const { body, error } = draft
  useTypingSignal(run.id, body)
  const busy = Boolean(draft.busy)
  const late = usePastDeadline(busy)
  const dictating = useDictationState(textarea).active
  const setBody = (next: string) => saveComposerDraft(run.id, { body: next })
  const [caret, setCaret] = useState(0)
  const [focused, setFocused] = useState(false)
  const [active, setActive] = useState(0)
  const hintID = useId()
  const listID = useId()
  const queueHeld = useQueueHeld(focused)

  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  // A docked composer unmounts while a wake resubscribes the agent stream, which would take the field from under the member.
  const wakeOnTyping = useWakeOnTyping(run, maySteer && !docked)
  const state = session?.state
  const stream = session?.stream
  const connecting = stream === 'live' || stream === 'refused' ? undefined : stream === 'reconnecting' || stream === 'offline' ? 'Reconnecting to the agent…' : 'Connecting to the agent…'
  // Control and the background wait read as off while the stream is down: the composer keeps its last live shape.
  const lastLive = useRef<{ canAct: boolean; waiting: boolean }>(undefined)
  if (stream === 'live') lastLive.current = { canAct: control.canAct, waiting: backgroundWait(run, session) !== null }
  const shape = lastLive.current
  // A sleeping session's ack says its agent is gone, and holds until the wake's resubscribed stream is live, whichever side of the wake's event that falls.
  const sleepAck = useRef<'held' | 'resubscribing'>(undefined)
  if (run.asleep_since) sleepAck.current = 'held'
  else if (sleepAck.current && stream !== 'live') sleepAck.current = 'resubscribing'
  else if (sleepAck.current === 'resubscribing') sleepAck.current = undefined
  const gate = enhancedBlock({
    run,
    maySteer,
    canReopen: maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run),
    refused: stream === 'refused' ? session?.streamError ?? 'The agent stream was refused.' : undefined,
    sessionLive: shape && !sleepAck.current ? session?.live ?? false : true,
    pending: session?.pending.length ?? 0,
    hasLease: shape?.canAct ?? true,
  })
  const moderated = !shape?.canAct
  const turnRunning = state?.turn_in_flight ?? false
  const waiting = shape?.waiting ?? false
  const blocked = Boolean(gate || connecting)
  // A sleeping session's agent reports its prompt types once a wake starts it.
  const supportsImages = state?.prompt_images === true || Boolean(run.asleep_since)
  const images = useComposerImages(run.id, !blocked && cap.hasMethod('terminal.file'))
  const attachOff = busy || blocked || !cap.hasMethod('terminal.file')
  const drop = dropRoom(images, attachOff)
  useEffect(() => onDropStatus?.({ ...drop, upload: images.upload }), [drop.blocked, drop.limit, images.upload, onDropStatus])
  const refusedImages = !supportsImages && hasImages(images.previews)
  const hasContent = Boolean(body.trim()) || images.previews.length > 0
  const pill = moderated ? 'send' : pillFor({ paused, turnRunning, backgroundWait: waiting, steering: state?.steering ?? false, queueHeld, empty: !hasContent })

  const [dismissed, setDismissed] = useState(false)
  const trigger = focused && !dismissed ? triggerAt(body, caret) : null
  const files = useFileSuggestions(run.workspace_id, run.id, trigger?.kind === '@' ? trigger.query : null)
  const suggestions: Suggestion[] = trigger?.kind === '/' ? commandSuggestions(state?.commands ?? [], trigger.query) : trigger?.kind === '@' ? files : []
  const current = Math.min(active, Math.max(0, suggestions.length - 1))
  const recall = usePromptHistory(run.id, body, (next) => {
    setBody(next)
    setDismissed(true)
  })

  const pick = (item: Suggestion) => {
    if (composerDraft(run.id).busy) return
    if (!trigger) return
    const next = body.slice(0, trigger.start) + item.insert + body.slice(caret)
    const at = trigger.start + item.insert.length
    setBody(next)
    setCaret(at)
    setActive(0)
    requestAnimationFrame(() => textarea.current?.setSelectionRange(at, at))
  }

  const modelOption = state?.config_options?.find((option) => option.category === 'model' && option.type === 'select')
  const modelName = (modelOption && currentChoice(modelOption)?.name) ?? run.model
  const canCompact = state?.commands?.some((command) => command.name === 'compact') ?? false
  const compactBlock = connecting ?? (moderated
    ? 'Take control of the conversation to compact it.'
    : busy || hasContent
      ? 'Clear the message box first, so nothing you typed is replaced.'
      : undefined)
  const compact = () => {
    setBody('/compact ')
    setCaret(9)
    requestAnimationFrame(() => {
      textarea.current?.focus()
      textarea.current?.setSelectionRange(9, 9)
    })
  }

  const attempt = async (action: () => Promise<unknown>) => {
    if (composerDraft(run.id).busy) return false
    const request = beginDraftRequest(run.id, 'busy', { error: undefined, busyPrompt: false })
    try {
      await action()
      request.settle()
      return true
    } catch (err) {
      request.settle({ error: message(err) })
      return false
    }
  }

  const deliver = async (steer: boolean) => {
    const latest = composerDraft(run.id)
    const text = latest.body.trim()
    const attachments = images.getPaths()
    const lease = sessionLease(useStore, run.id)
    if ((!text && !attachments.length) || latest.busy || !images.isReady() || (!lease && !moderated)) return
    if (!supportsImages && hasImages(latest.images)) {
      saveComposerDraft(run.id, { error: 'This agent does not support image prompts.' })
      return
    }
    const idempotency = draftSendKey(latest, text, attachments)
    const request = beginDraftRequest(run.id, 'busy', { idempotency, error: undefined, busyPrompt: true })
    // After Cancel a failure still shows on the draft it was sent from; a success stays silent.
    const settle = (settled: Partial<ComposerDraft>) => {
      const draft = composerDraft(run.id)
      if (request.current()) request.settle(settled)
      else if (settled.error && !draft.busy && draft.idempotency?.key === idempotency.key) saveComposerDraft(run.id, settled)
    }
    try {
      const result = await api.runInject(run.id, text, idempotency.key, { steer, lease, attachments })
      useStore.getState().upsertRoomMessage(result.message)
      const delivery = result.receipt === 'not_sent' || result.receipt === 'uncertain' ? result.receipt : result.message.state
      if (delivery === 'sent' || delivery === 'queued') settle(sentDraft)
      else if (delivery === 'uncertain') settle({ error: result.message.failure?.message ?? 'Delivery is uncertain. Your draft was kept; check the conversation before retrying.' })
      else settle({ error: result.message.failure?.message ?? 'The message was not sent. Your draft was kept.', idempotency: undefined })
    } catch (err) {
      settle({ error: message(err) })
    }
  }

  const act = (chosen: Pill) => {
    if (connecting || composerDraft(run.id).busy || images.isUploading()) return
    if (moderated) void deliver(false)
    else control.withControl(() => perform(chosen))
  }
  const perform = (chosen: Pill) => {
    if (composerDraft(run.id).busy || images.isUploading()) return
    const lease = sessionLease(useStore, run.id)
    switch (chosen) {
      case 'resume':
        void attempt(() => api.runResume(run.id))
        return
      case 'interrupt':
      case 'stop-waiting':
        if (lease) void attempt(() => api.runACPCancel(run.id, lease))
        return
      default:
        void deliver(chosen === 'steer')
    }
  }

  const setOption = (option: ConfigOption, value: string) => new Promise<boolean>((resolve) => {
    control.withControl(() => {
      const lease = sessionLease(useStore, run.id)
      return attempt(() => lease ? api.runACPSetOption(run.id, option.id, value, lease) : Promise.reject(new Error(optionUnsent))).then(resolve)
    }, () => void attempt(() => Promise.reject(new Error(optionUnsent))).then(resolve))
  })

  if (docked && blocked) return null
  if (gate) {
    return (
      <ComposerFrame>
        {dock}
        <Closed
          runID={run.id}
          reason={gate.reason}
          failure={gate.interrupt ? error : undefined}
          action={gate.interrupt && turnRunning && (
            <Button size="sm" variant={mobile ? 'secondary' : 'danger'} aria-label="Interrupt the agent" hint={pillHint.interrupt} disabled={busy || Boolean(connecting)} onClick={() => act('interrupt')}>
              <Square className="fill-current" />
              Interrupt
            </Button>
          )}
        />
        {gate.interrupt && <p role="status" className="sr-only">{connecting}</p>}
      </ComposerFrame>
    )
  }

  const stops = pill === 'interrupt' || pill === 'stop-waiting'
  const pillDisabled = busy || Boolean(connecting) || images.uploading || (!stops && pill !== 'resume' && (!images.ready || !hasContent || refusedImages || dictating))
  const hintText = connecting ?? (moderated ? (control.waits ? 'Delivers in 45 s unless the controller decides sooner.' : '') : turnRunning || pill === 'stop-waiting' || pill === 'resume' ? pillHint[pill] : '')
  const keyHint = coarse || stops ? undefined : pill === 'steer' ? `${shortcutLabel('composer-send')}; ${shortcutLabel('composer-queue')} queues` : shortcutLabel(pill === 'queue' && state?.steering ? 'composer-queue' : 'composer-send')

  return (
    <ComposerFrame docked={docked}>
      {dock}
      {images.uploadError && (
        <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
          <span className="min-w-0 flex-1 break-words">{images.uploadError}</span>
          <Button size="sm" variant="ghost" onClick={images.clearError}>Dismiss</Button>
        </div>
      )}
      {error && (
        <div role="alert" className="flex items-start gap-2 text-ui-sm text-state-failed">
          <span className="min-w-0 flex-1 break-words">{error}</span>
          <Button size="sm" variant="ghost" onClick={() => saveComposerDraft(run.id, { error: undefined })}>Dismiss</Button>
        </div>
      )}
      <TypingIndicator runID={run.id} />
      <ComposerBox
        textarea={textarea}
        autoFocus={autoFocus}
        collapsed={collapsed}
        value={body}
        readOnly={busy}
        onPaste={images.onPaste}
        images={<AttachmentChips images={images} disabled={busy} />}
        attach={{ images, disabled: attachOff }}
        footer={(
          <>
            <div className="flex items-center gap-1">
              <AttachButton
                images={images}
                disabled={attachOff}
                textarea={textarea}
                unsupported={refusedImages ? 'This agent does not support image prompts.' : undefined}
              />
              <TemplateInsert run={run} textarea={textarea} disabled={busy} />
            </div>
            <OptionPills options={state?.config_options ?? []} disabled={busy || moderated || Boolean(connecting)} onSet={setOption} />
            {state?.usage && state.usage.size > 0 && (
              <ContextWindow
                runID={run.id}
                harness={run.harness}
                usage={state.usage}
                model={modelName}
                compact={canCompact ? { onCompact: compact, blocked: compactBlock } : undefined}
              />
            )}
            <SendActions
              pill={pill}
              label={late ? stillWaiting : busy && draft.busyPrompt ? 'Sending…' : pillLook[pill].label}
              hint={keyHint}
              description={hintText}
              disabled={pillDisabled}
              onClick={() => act(pill)}
              onCancel={late && draft.busyPrompt ? () => saveComposerDraft(run.id, { busy: undefined }) : undefined}
              dictation={<DictationButton textarea={textarea} onInsert={setBody} />}
            />
          </>
        )}
        onChange={(value, at) => {
          setBody(value)
          wakeOnTyping(value)
          setCaret(at)
          setActive(0)
          setDismissed(false)
        }}
        onCaret={setCaret}
        onFocusChange={(next) => {
          setFocused(next)
          onFocusChange(next)
        }}
        onSend={() => {
          if (hasContent) act(pill)
        }}
        onQueue={() => {
          if (hasContent) act(turnRunning ? 'queue' : 'send')
        }}
        onEscape={onEscape}
        placeholder={connecting ?? (turnRunning || waiting ? 'Message the agent while it works' : notFinishedYet(run) ? 'Say what is left to do, / for commands, @ for files' : 'Message the agent, / for commands, @ for files')}
        describedBy={hintID}
        combobox={{ expanded: suggestions.length > 0, controls: listID, active: suggestions.length ? `${listID}-${current}` : undefined }}
        menu={<SuggestionList id={listID} items={suggestions} active={current} onPick={pick} />}
        onKeyDown={(event) => {
          if (busy) return
          if (suggestions.length === 0) {
            recall(event)
            return
          }
          if (event.nativeEvent.isComposing) return
          if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
            event.preventDefault()
            setActive((current + (event.key === 'ArrowDown' ? 1 : -1) + suggestions.length) % suggestions.length)
          } else if ((event.key === 'Enter' && !event.metaKey && !event.ctrlKey) || event.key === 'Tab') {
            event.preventDefault()
            pick(suggestions[current]!)
          } else if (event.key === 'Escape') {
            event.preventDefault()
            setDismissed(true)
          }
        }}
      />
      <ComposerHint id={hintID} uploading={images.uploading} description={hintText} />
      <p role="status" className="sr-only">{connecting}</p>
    </ComposerFrame>
  )
}

export function Composer(props: ComposerProps) {
  const { run, dock } = props
  const self = useSelf(run.workspace_id)
  const cap = useCapability()
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const canReopen = maySteer && cap.hasMethod('run.relaunch') && canReopenRun(run)
  useEffect(() => {
    if (canReopen) props.onDropStatus?.({ limit: 0, blocked: 'Files cannot be attached now' })
  }, [canReopen, props.onDropStatus])
  if (canReopen) return <ClosedRun key={run.id} run={run} reason={composerBlock(run, maySteer, true)!} dock={dock} />
  return props.run.acp ? <EnhancedComposer key={props.run.id} {...props} /> : <StandardComposer key={props.run.id} {...props} />
}
