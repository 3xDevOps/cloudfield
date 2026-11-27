import { memo, useState } from 'react'
import { ArrowRight, CircleHelp, ClipboardCheck, type LucideIcon, MessageSquare, Reply } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { RelativeTime } from '@/components/ui/relative-time'
import { api, type Api } from '@/lib/api'
import { formatBytes } from '@/lib/format'
import { plainReason } from '@/lib/reasons'
import { runLabel } from '@/lib/status'
import type { RunMessage, RunMessageKind } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

/** Overrides a participant's label; undefined keeps it. */
type ParticipantLabel = (runID: string, run: RunRecord | undefined) => string | undefined

const kinds: Record<RunMessageKind, { icon: LucideIcon; word: string }> = {
  message: { icon: MessageSquare, word: 'Message' },
  question: { icon: CircleHelp, word: 'Question' },
  reply: { icon: Reply, word: 'Reply' },
  report: { icon: ClipboardCheck, word: 'Report' },
}

export function MessageKindGlyph({ kind }: { kind: string }) {
  const known = kinds[kind as RunMessageKind] ?? kinds.message
  const Icon = known.icon
  return <Icon role="img" aria-label={known.word} className="size-3.5 shrink-0 text-muted" />
}

export function deliveryWord(message: Pick<RunMessage, 'delivered_at' | 'acked_at'>): string {
  if (message.acked_at) return 'Acknowledged'
  if (message.delivered_at) return 'Delivered'
  return 'Sent'
}

function Participant({ runID, label }: { runID: string; label?: ParticipantLabel }) {
  const run = useStore((s) => s.runs[runID])
  const navigate = useStore((s) => s.navigate)
  const open = useStore((s) => s.route.name === 'run' && s.route.params.runId === runID)
  const name = label?.(runID, run) ?? (run && runLabel(run))
  const title = run ? runLabel(run) : name
  if (!name) return <span className="text-ui text-muted">Unknown session</span>
  if (open) return <span className="max-w-64 min-w-0 shrink truncate text-ui text-muted" title={title}>{name}</span>
  return (
    <Button variant="quiet" className="max-w-64 min-w-0 shrink" title={title} onClick={() => navigate('run', { runId: runID })}>
      <span className="truncate">{name}</span>
    </Button>
  )
}

export function Participants({ from, to, label }: { from: string; to: string; label?: ParticipantLabel }) {
  return (
    <span className="inline-flex max-w-full min-w-0 items-center gap-1 align-bottom">
      <Participant runID={from} label={label} />
      <ArrowRight aria-label="to" role="img" className="size-3 shrink-0 text-muted" />
      <Participant runID={to} label={label} />
    </span>
  )
}

export const MessageRow = memo(function MessageRow({
  message,
  onThread,
  label,
  flush = false,
  client = api,
}: {
  message: RunMessage
  onThread?: (thread: string) => void
  label?: ParticipantLabel
  flush?: boolean
  client?: Api
}) {
  const [expanded, setExpanded] = useState(false)
  // The report's body: unset until asked for, then the text or the error.
  const [full, setFull] = useState<{ open: boolean; loading?: boolean; body?: string; error?: string }>({ open: false })
  const bodyBytes = message.kind === 'report' ? (message.report_body_bytes ?? 0) : 0
  const toggleFull = () => {
    if (full.open || full.body !== undefined) return setFull((state) => ({ ...state, open: !state.open }))
    setFull({ open: true, loading: true })
    client
      .coordMessagesList({ workspace_id: message.workspace_id, correlation_id: message.correlation_id, report_bodies: true })
      .then((page) => {
        const row = page.messages.find((m) => m.id === message.id)
        setFull(row?.report_body ? { open: true, body: row.report_body } : { open: true, error: 'The server returned no body for this report.' })
      })
      .catch((error: unknown) => setFull({ open: true, error: error instanceof Error ? error.message : String(error) }))
  }
  const long = message.body.split('\n').length > 3 || message.body.length > 280
  const thread = message.correlation_id || (message.kind === 'question' || message.kind === 'report' ? message.id : '')
  const report = message.kind === 'report' ? [['Outcome', message.outcome], ['Summary', message.summary === message.body ? '' : message.summary], ['Next', message.next_action && plainReason(message.next_action)]].filter(([, value]) => value) : []
  return (
    <article aria-label={`${kinds[message.kind]?.word ?? 'Message'} ${message.id}`} className={cn('grid min-w-0 grid-cols-[0.875rem_minmax(0,1fr)] gap-x-2 gap-y-1 text-ui', !flush && 'px-4 py-2')}>
      <span className="flex h-5 items-center">
        <MessageKindGlyph kind={message.kind} />
      </span>
      <header className="flex min-w-0 flex-wrap items-center gap-x-3">
        <span className="max-w-full min-w-0 flex-[1_0_auto]">
          <Participants from={message.from_run_id} to={message.to_run_id} label={label} />
        </span>
        <span className="flex shrink-0 items-center gap-1.5 text-ui-sm text-muted tabular-nums max-sm:basis-full">
          {message.kind !== 'message' && <span>{kinds[message.kind]?.word} ·</span>}
          <span>{deliveryWord(message)} ·</span>
          <RelativeTime at={message.created_at} />
        </span>
      </header>
      {message.body && (
        <p className={expanded ? 'col-start-2 break-words whitespace-pre-wrap text-text' : 'col-start-2 line-clamp-3 break-words whitespace-pre-wrap text-text'}>
          {message.body}
        </p>
      )}
      {report.length > 0 && (
        <dl className="col-start-2 grid grid-cols-[auto_minmax(0,1fr)] gap-x-2 text-ui-sm">
          {report.map(([term, value]) => (
            <div key={term} className="contents">
              <dt className="text-muted">{term}</dt>
              <dd className="break-words text-text">{value}</dd>
            </div>
          ))}
        </dl>
      )}
      {full.open && (
        <div className="col-start-2 min-w-0">
          {full.loading && <p className="text-ui-sm text-muted">Loading the full report…</p>}
          {full.error && <p role="alert" className="text-ui-sm break-words text-state-failed">{full.error}</p>}
          {full.body !== undefined && (
            <pre aria-label="Full report" className="max-h-96 min-w-0 overflow-auto border border-seam/70 bg-chrome p-3 font-code text-ui-sm break-words whitespace-pre-wrap text-text">
              {full.body}
            </pre>
          )}
        </div>
      )}
      {(long || bodyBytes > 0 || (thread && onThread)) && (
        <div className="col-start-2 flex gap-3">
          {long && <Button variant="link" size="sm" onClick={() => setExpanded((open) => !open)}>{expanded ? 'Show less' : 'Show more'}</Button>}
          {bodyBytes > 0 && (
            <Button variant="link" size="sm" aria-expanded={full.open} onClick={toggleFull}>
              {full.open ? 'Hide full report' : `Full report (${formatBytes(bodyBytes)})`}
            </Button>
          )}
          {thread && onThread && <Button variant="link" size="sm" onClick={() => onThread(thread)}>Thread</Button>}
        </div>
      )}
    </article>
  )
})
