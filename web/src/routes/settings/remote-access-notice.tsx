import { CopyableCommand } from '@/components/copyable-command'
import { Globe } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import { StatusDot } from '@/components/ui/status-dot'
import type { EdgeNeed } from '@/lib/needs-you'
import { cn } from '@/lib/utils'
import { wideIndent } from '@/routes/board/run-card'
import { useStore } from '@/store'

function Unusable({ address }: { address?: string }) {
  return (
    <>
      <p>The edge sent a new address that is not a plain https address, so there is no command to run:</p>
      <code className="min-w-0 border-l-2 border-current pl-2.5 font-code text-muted break-all">{address}</code>
      <p>Confirm the new address with whoever runs the edge.</p>
    </>
  )
}

function FirstUse() {
  return <p>This came with the server's first connection to the edge, when it pinned the edge's key: confirm the new address with whoever runs the edge before you move.</p>
}

/** How a super admin moves the server off the address `need` is about:
 * the one a restart applies when that is leaving too, else the one it is
 * at. A replacement that is not a plain https address gets no command,
 * and a first-use notice is cautioned before any instruction. */
export function EdgeMoveSteps({ need }: { need: EdgeNeed }) {
  const { pending, notice } = need
  if (pending) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
        {need.noticeReason && <p className="break-words">{need.noticeReason}; the server still runs there.</p>}
        {pending.first_use && <FirstUse />}
        {need.moveOn ? (
          <>
            <p className="break-words">The server's options file names {pending.edge_url}, which is itself {pending.state}, so a restart would move the server to a leaving address. Move it to {pending.replacement} first. Run on the server's host, adding the service's --config &lt;file&gt; if it has one.</p>
            <p>1. Move the server:</p>
            <CopyableCommand command={need.moveOn} wrap />
            <p>2. Restart it to apply:</p>
            <CopyableCommand command={need.restart} />
          </>
        ) : <Unusable address={pending.replacement} />}
      </div>
    )
  }
  if (!notice) return null
  if (!need.move) {
    return <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm"><Unusable address={notice.replacement} /></div>
  }
  const caution = notice.first_use && <FirstUse />
  if (notice.moved_to) {
    return (
      <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
        {caution}
        <p className="break-words">The server's options file already names {notice.moved_to}; the server still runs at {need.edgeURL}. Restart it on the server's host to apply:</p>
        <CopyableCommand command={need.restart} />
      </div>
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-1.5 text-ui-sm">
      {caution}
      <p>Move this server to {notice.replacement}, another address of the same edge. Run on the server's host, adding the service's --config &lt;file&gt; if it has one.</p>
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
  const tone = need.state === 'retired' ? 'failed' : 'needs-you'
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
        <StatusDot tone={tone} label={need.state === 'retired' ? 'Retired' : 'Retiring'} />
        <span className="min-w-0 flex-1 break-words">{need.state === 'retired' ? 'Retired' : 'Retiring'} · Run on the server's host</span>
        <Button variant="ghost" size="sm" onClick={() => navigate('settings', { section: 'edge' })}>Remote access</Button>
      </div>
    </Card>
  )
}
