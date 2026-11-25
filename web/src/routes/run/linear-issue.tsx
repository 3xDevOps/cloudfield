import { useEffect, useState } from 'react'
import { Linear } from '@/components/icons'
import { api, type Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { LinearIssue, Run } from '@/lib/types'
import { cn } from '@/lib/utils'
import { useStore } from '@/store'

type Lookup = { state: LinearIssue['state'] } | { error: string } | null

/** The issue's status, read with the member's own Linear key; none without a key. */
function useIssueState(client: Api, identifier: string): Lookup {
  const connected = useStore((s) => s.linear?.connected === true)
  const [found, setFound] = useState<{ identifier: string; lookup: Lookup } | null>(null)
  useEffect(() => {
    if (!connected) return
    let live = true
    client.linearIssue(identifier).then(
      (issue) => { if (live) setFound({ identifier, lookup: { state: issue.state } }) },
      (err: unknown) => { if (live) setFound({ identifier, lookup: { error: message(err) } }) },
    )
    return () => { live = false }
  }, [client, connected, identifier])
  return connected && found?.identifier === identifier ? found.lookup : null
}

/** The run's Linear issue as a link to Linear, with its status when the member has a key. */
export function RunLinearIssue({ run, client = api, compact = false, className }: { run: Pick<Run, 'linear_issue' | 'linear_issue_url'>; client?: Api; compact?: boolean; className?: string }) {
  const identifier = run.linear_issue ?? ''
  const lookup = useIssueState(client, identifier)
  if (!identifier || !run.linear_issue_url) return null
  const state = lookup && 'state' in lookup ? lookup.state : null
  const error = lookup && 'error' in lookup ? lookup.error : null
  return (
    <a
      className={cn('flex shrink-0 items-center gap-1 rounded-control text-ui-sm text-muted underline-offset-2 hover:text-text hover:underline coarse:h-11 coarse:px-1', className)}
      href={run.linear_issue_url}
      target="_blank"
      rel="noreferrer"
      title={error ? `Linear status unavailable: ${error}` : undefined}
      aria-label={`Linear issue ${identifier}${state ? `, ${state.name}` : ''}`}
    >
      <Linear aria-hidden className="size-3.5" />
      <span className="tabular-nums">{identifier}</span>
      {state && !compact && <span className="hidden pl-1 @lg/state:inline">{state.name}</span>}
    </a>
  )
}
