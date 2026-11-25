import { Linear } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import type { LinearLink } from '@/lib/types'

/** The launch sends `link.url` as linear_issue_url; this is where that shows and can be undone. */
export function LinkedIssue({ link, onUnlink }: { link: LinearLink; onUnlink: () => void }) {
  return (
    <div className="flex min-w-0 items-center gap-2 rounded-control border border-seam bg-chrome py-1 pr-1 pl-2 text-ui-sm text-muted">
      <Linear aria-hidden className="size-3.5 shrink-0" />
      <span className="min-w-0 flex-1 truncate"><a className="tabular-nums text-accent underline-offset-2 hover:underline" href={link.url} target="_blank" rel="noreferrer">{link.identifier}</a> · will be linked</span>
      <Button type="button" variant="ghost" size="sm" aria-label={`Unlink ${link.identifier}`} onClick={onUnlink}>Unlink</Button>
    </div>
  )
}

/** An issue picked while a draft is open: the draft stays until the member takes the issue instead. */
export function OfferedIssue({ draft, offered, onUse }: { draft: LinearLink | null; offered: LinearLink; onUse: () => void }) {
  return (
    <Callout
      tone="needs-you"
      role="status"
      title={`Your unfinished ${draft?.identifier ?? 'session'} draft was kept.`}
      actions={<Button type="button" size="sm" variant="secondary" onClick={onUse}>Use {offered.identifier} instead</Button>}
    >
      {offered.identifier} was not loaded. Using it replaces the draft's task and link.
    </Callout>
  )
}
