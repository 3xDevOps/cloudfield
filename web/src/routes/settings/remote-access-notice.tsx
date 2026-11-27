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
  if (need.notice.edge_url_flag) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
        <p className="break-words">The service starts the server with --edge-url {need.edgeURL}, and a restart keeps that flag, so edge move does not apply. Change the flag to {need.notice.replacement} where the service starts cloudfield-server serve, then restart it on the server's host:</p>
        <CopyableCommand command={need.restart} />
      </div>
    )
  }
  if (need.notice.moved_to) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
        <p className="break-words">The server's options file already names {need.notice.moved_to}; the server still runs at {need.edgeURL}. Restart it on the server's host to apply:</p>
        <CopyableCommand command={need.restart} />
      </div>
    )
  }
  if (!need.move) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
        <p>The edge sent a new address that is not a plain https address, so there is no command to run:</p>
        <code className="min-w-0 border-l-2 border-current pl-2.5 font-code text-muted break-all">{need.notice.replacement}</code>
        <p>Confirm the new address with whoever runs the edge.</p>
      </div>
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
      <p>Move this server to {need.notice.replacement}, another address of the same edge. Run on the server's host, adding the service's --config &lt;file&gt; if it has one.</p>
      {need.notice.first_use && (
        <p>This came with the server's first connection to the edge, when it pinned the edge's key: confirm the new address with whoever runs the edge before you move.</p>
      )}
      <p>1. Move the server:</p>
      <CopyableCommand command={need.move} wrap />
      <p>2. Restart it to apply:</p>
      <CopyableCommand command={need.restart} />
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
        <span className="min-w-0 flex-1 break-words">{need.notice.state === 'retired' ? 'Retired' : 'Retiring'} · Run on the server's host</span>
        <Button variant="ghost" size="sm" onClick={() => navigate('settings', { section: 'edge' })}>Remote access</Button>
      </div>
    </Card>
  )
}
