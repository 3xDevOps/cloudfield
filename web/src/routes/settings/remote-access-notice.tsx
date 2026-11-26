import { CopyableCommand } from '@/components/copyable-command'
import { Globe } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { StatusDot } from '@/components/ui/status-dot'
import type { EdgeNeed } from '@/lib/needs-you'
import { cn } from '@/lib/utils'
import { wideIndent } from '@/routes/board/run-card'
import { useStore } from '@/store'

/** How a super admin moves the server off the address `need` names. */
export function EdgeMoveSteps({ need }: { need: EdgeNeed }) {
  if (!need.move) {
    return (
      <p className="min-w-0 break-words text-ui-sm">
        The edge names a new address that is not a plain https address, so no command is shown: <span className="break-all font-code">{need.notice.replacement}</span>. Confirm the new address with whoever runs the edge.
      </p>
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
      <p>Move this server to {need.notice.replacement}, another address of the same edge. Run on the server's host, adding the service's --config &lt;file&gt; if it has one:</p>
      <CopyableCommand command={need.move} wrap />
      <p>then apply it:</p>
      <CopyableCommand command={need.restart} />
      {need.notice.first_use && (
        <p className="text-muted">This came with the server's first connection to the edge, when it pinned the edge's key: confirm the new address with whoever runs the edge before you move.</p>
      )}
    </div>
  )
}

/** The Needs you entry for a retiring or retired edge address. */
export function EdgeNoticeCard({ need }: { need: EdgeNeed }) {
  const navigate = useStore((s) => s.navigate)
  const tone = need.notice.state === 'retired' ? 'failed' : 'needs-you'
  return (
    <Card raised className="@container" data-edge-notice={need.edgeURL}>
      <div className="flex items-start gap-3">
        <Globe aria-hidden className={cn('mt-0.75 size-3.5 shrink-0', tone === 'failed' ? 'text-state-failed' : 'text-state-needs-you')} />
        <h3 className="min-w-0 text-ui font-medium break-words text-text">{need.reason}</h3>
      </div>
      <div className={cn('mt-1 min-w-0', wideIndent)}>
        <EdgeMoveSteps need={need} />
      </div>
      <div className={cn('mt-2 flex min-w-0 items-center gap-1.5 text-ui-sm text-muted', wideIndent)}>
        <StatusDot tone={tone} label={need.notice.state === 'retired' ? 'Retired' : 'Retiring'} />
        <span className="min-w-0 flex-1 truncate">Run on the server's host</span>
        <Button variant="ghost" size="sm" onClick={() => navigate('settings', { section: 'edge' })}>Remote access</Button>
      </div>
    </Card>
  )
}
