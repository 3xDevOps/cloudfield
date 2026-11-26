import { useEffect, useState } from 'react'
import { Linear } from '@/components/icons'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearIssue, Run } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

type Lookup = { issue: LinearIssue } | { error: string } | null

/** The issue, read with the member's own Linear key; none without a key or when `enabled` is false. */
function useIssue(client: Api, identifier: string, enabled: boolean): Lookup {
  const connected = useStore((s) => s.linear?.connected === true)
  const [found, setFound] = useState<{ identifier: string; lookup: Lookup } | null>(null)
  const read = enabled && connected && identifier !== ''
  useEffect(() => {
    if (!read) return
    let live = true
    client.linearIssue(identifier).then(
      (issue) => { if (live) setFound({ identifier, lookup: { issue } }) },
      (err: unknown) => { if (live) setFound({ identifier, lookup: { error: message(err) } }) },
    )
    return () => { live = false }
  }, [client, read, identifier])
  return read && found?.identifier === identifier ? found.lookup : null
}

/**
 * The run's Linear issue as a link to Linear. `details` adds the issue's
 * status; the header shows the identifier with the issue title as its tooltip;
 * `compact` (the phone header) reads nothing.
 */
export function RunLinearIssue({ run, client = api, compact = false, details = false, className }: { run: Pick<Run, 'linear_issue' | 'linear_issue_url'>; client?: Api; compact?: boolean; details?: boolean; className?: string }) {
  const identifier = run.linear_issue ?? ''
  const lookup = useIssue(client, identifier, !compact)
  if (!identifier || !run.linear_issue_url) return null
  const issue = lookup && 'issue' in lookup ? lookup.issue : null
  const error = lookup && 'error' in lookup ? lookup.error : null
  const label = `Linear issue ${identifier}${issue ? `, ${issue.state.name}` : ''}`
  const title = error ? `Linear status unavailable: ${error}` : issue?.title
  const link = 'flex shrink-0 items-center gap-1 rounded-control text-ui-sm underline-offset-2 hover:underline coarse:h-11 coarse:px-1'
  if (details) {
    return (
      <span className={cn('flex min-w-0 items-center gap-2 text-ui-sm', className)}>
        <a className={cn(link, 'text-accent coarse:h-auto coarse:px-0')} href={run.linear_issue_url} target="_blank" rel="noreferrer" title={title} aria-label={`Linear issue ${identifier}`}>
          <Linear aria-hidden className="size-3.5" />
          <span className="tabular-nums">{identifier}</span>
        </a>
        {issue && <span className="text-muted">{issue.state.name}</span>}
      </span>
    )
  }
  return (
    <a
      className={cn(link, 'text-muted hover:text-text', className)}
      href={run.linear_issue_url}
      target="_blank"
      rel="noreferrer"
      title={title}
      aria-label={label}
    >
      <Linear aria-hidden className="size-3.5" />
      <span className="tabular-nums">{identifier}</span>
    </a>
  )
}
