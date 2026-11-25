import { Linear } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'

function identifierOf(url: string): string {
  return /\/issue\/([^/]+)/.exec(url)?.[1] ?? 'the Linear issue'
}

/** The launch sends `url` as linear_issue_url whatever the task says; this is where that shows and can be undone. */
export function LinkedIssue({ url, kept, onUnlink }: { url: string; kept: boolean; onUnlink: () => void }) {
  const identifier = identifierOf(url)
  if (!kept) {
    return (
      <Callout tone="needs-you" role="status" title={`${identifier} is not linked`}>
        The unfinished draft below was restored instead of the new task, and a draft does not link a session.
      </Callout>
    )
  }
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-control border border-seam bg-chrome py-1 pr-1 pl-2 text-ui-sm text-muted">
      <Linear aria-hidden className="size-3.5 shrink-0" />
      <span className="min-w-0 flex-1 truncate"><span className="tabular-nums text-text">{identifier}</span> · will be linked</span>
      <Button type="button" variant="ghost" size="sm" aria-label={`Unlink ${identifier}`} onClick={onUnlink}>Unlink</Button>
    </div>
  )
}
