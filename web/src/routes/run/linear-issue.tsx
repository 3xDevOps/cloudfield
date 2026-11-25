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
 * The run's Linear issue as a link to Linear. The header shows the issue's
 * status when it is wide enough; `details` always does, as one link; `compact`
 * (the phone header) reads and shows none.
 */
export function RunLinearIssue({ run, client = api, compact = false, details = false, className }: { run: Pick<Run, 'linear_issue' | 'linear_issue_url'>; client?: Api; compact?: boolean; details?: boolean; className?: string }) {
  const identifier = run.linear_issue ?? ''
  const lookup = useIssue(client, identifier, !compact)
  if (!identifier || !run.linear_issue_url) return null
  const issue = lookup && 'issue' in lookup ? lookup.issue : null
  const error = lookup && 'error' in lookup ? lookup.error : null
  return (
    <a
      className={cn('flex shrink-0 items-center gap-1 rounded-control text-ui-sm text-muted underline-offset-2 hover:text-text hover:underline coarse:h-11 coarse:px-1', className)}
      href={run.linear_issue_url}
      target="_blank"
      rel="noreferrer"
      title={error ? `Linear status unavailable: ${error}` : issue?.title}
      aria-label={`Linear issue ${identifier}${issue ? `, ${issue.state.name}` : ''}`}
    >
      <Linear aria-hidden className="size-3.5" />
      <span className="tabular-nums">{identifier}</span>
      {issue && <span className={cn('pl-1', !details && 'hidden @lg/state:inline')}>{issue.state.name}</span>}
    </a>
  )
}
