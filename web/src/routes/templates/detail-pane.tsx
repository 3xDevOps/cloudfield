import { useId, useRef, useState, type ReactNode } from 'react'
import { modeLabel } from '@/components/launch/modes'
import { ChevronLeft, Clock, Ellipsis } from '@/components/icons'
import { FillProgress, PromptFill, PromptFooter, runIfReady, useEntranceOnChange, type PrimaryAction, type Typed } from '@/components/templates/prompt-fill'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuSeparator, MenuTrigger } from '@/components/ui/menu'
import { RelativeTime } from '@/components/ui/relative-time'
import { money } from '@/lib/format'
import type { Schedule, Template } from '@/lib/types'
import { describeRule } from '@/routes/templates/schedule'

function Timing({ template, schedule }: { template: Template; schedule?: Schedule }) {
  if (!schedule) return <>Saved <RelativeTime at={template.created_at} /></>
  const fire = schedule.next_fire_at ? ['next', schedule.next_fire_at] as const : schedule.last_fire_at ? ['last', schedule.last_fire_at] as const : null
  const shown = describeRule(schedule.cron)
  return (
    <>
      Scheduled {'words' in shown ? shown.words : <><code className="font-code">{shown.cron}</code> {shown.zone}</>}
      {fire && <>, {fire[0]} <RelativeTime at={fire[1]} /></>}
    </>
  )
}

export function DetailPane({
  template,
  schedule,
  agentName,
  typed,
  onValue,
  primary,
  saving,
  notice,
  onBack,
  onEdit,
  onSchedule,
  onDuplicate,
  onDelete,
}: {
  template: Template
  schedule?: Schedule
  agentName: string
  typed: Typed
  onValue: (name: string, value: string) => void
  primary: PrimaryAction
  saving: boolean
  notice?: ReactNode
  onBack?: () => void
  onEdit?: () => void
  onSchedule?: () => void
  onDuplicate?: () => void
  onDelete?: () => void
}) {
  const headingID = useId()
  const body = useRef<HTMLDivElement>(null)
  const [hint, setHint] = useState<string | null>(null)
  useEntranceOnChange(body, template.name)
  const menu = onDuplicate || onDelete
  return (
    <section aria-labelledby={headingID} className="flex min-h-0 min-w-0 flex-1 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto">
        <div ref={body} className="mx-auto flex w-full max-w-3xl min-w-0 flex-col gap-3 px-4 py-4 sm:px-6">
          {onBack && (
            <Button variant="ghost" size="sm" className="-ml-2 self-start" onClick={onBack}>
              <ChevronLeft />Templates
            </Button>
          )}
          {notice}
          <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-2">
            <div className="flex min-w-40 flex-1 items-center gap-2">
              <h2 id={headingID} className="min-w-0 truncate text-title text-text">{template.name}</h2>
              <Badge>{modeLabel(template.mode)}</Badge>
            </div>
            <FillProgress template={template} typed={typed} />
            <div className="flex shrink-0 items-center gap-1">
              {onEdit && <Button variant="secondary" size="sm" onClick={onEdit}>Edit</Button>}
              {onSchedule && (
                <Button variant="secondary" size="sm" disabled={saving} onClick={onSchedule}>
                  <Clock />Schedule
                </Button>
              )}
              {menu && (
                <Menu>
                  <MenuTrigger asChild>
                    <Button variant="ghost" size="icon-sm" label={`More for ${template.name}`}>
                      <Ellipsis />
                    </Button>
                  </MenuTrigger>
                  <MenuContent align="end">
                    {onDuplicate && <MenuItem onSelect={onDuplicate}>Duplicate</MenuItem>}
                    {onDelete && (
                      <>
                        {onDuplicate && <MenuSeparator />}
                        <MenuItem tone="danger" disabled={saving} onSelect={onDelete}>Delete</MenuItem>
                      </>
                    )}
                  </MenuContent>
                </Menu>
              )}
            </div>
          </div>
          <p className="flex min-w-0 flex-wrap items-center gap-x-1.5 text-ui-sm text-muted">
            <AgentGlyph agent={template.harness} />
            <span>{agentName}</span>
            <span aria-hidden>·</span>
            <span className={template.model ? 'font-code' : undefined}>{template.model || 'agent default'}</span>
            {template.effort && (
              <>
                <span aria-hidden>·</span>
                <span>{template.effort}</span>
              </>
            )}
            <span aria-hidden>·</span>
            <span><Timing template={template} schedule={schedule} /></span>
            {template.budget_usd ? (
              <>
                <span aria-hidden>·</span>
                <span>{money.format(template.budget_usd)} budget hint</span>
              </>
            ) : null}
          </p>
          <PromptFill template={template} typed={typed} onValue={onValue} onHint={setHint} onComplete={() => runIfReady(primary)} />
        </div>
      </div>
      <PromptFooter template={template} typed={typed} hint={hint} primary={primary} className="mx-auto w-full max-w-3xl sm:px-6" />
    </section>
  )
}
