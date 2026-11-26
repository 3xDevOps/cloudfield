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
  return (
    <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
      <p>Move this server to {need.notice.replacement}, another address of the same edge. Run on the server's host:</p>
      <CopyableCommand command={need.move} />
      <p>then apply it:</p>
      <CopyableCommand command={need.restart} />
      {need.notice.first_use && (
        <p className="text-muted">The edge sent this with the connection that pinned its key, so it is trusted on first use only.</p>
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
        <StatusDot tone={tone} label="Needs you" />
        <span className="min-w-0 flex-1 truncate">Needs you · on the server's host</span>
        <Button variant="ghost" size="sm" onClick={() => navigate('settings', { section: 'edge' })}>Remote access</Button>
      </div>
    </Card>
  )
}
