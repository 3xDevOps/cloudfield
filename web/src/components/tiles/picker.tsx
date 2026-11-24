import { useContext, useEffect, useState } from 'react'
import { ArrowUpRight, Plus } from '@/components/icons'
import { changeTiles, TileAreaContext, viewsElsewhere } from '@/components/tiles/area'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Button } from '@/components/ui/button'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { RelativeTime } from '@/components/ui/relative-time'
import { StatusDot } from '@/components/ui/status-dot'
import { Tooltip } from '@/components/ui/tooltip'
import { canLaunch } from '@/lib/commands'
import { runLabel, toneOf } from '@/lib/status'
import { cn } from '@/lib/utils'
import { runViewIcon } from '@/routes/run/view-icons'
import { isRunView, offeredViews, runViewLabel, runViews } from '@/routes/run/views'
import { useStore } from '@/store'
import { useSelfRole, useSidebarGroups } from '@/store/hooks'
import type { RunRow } from '@/store/selectors'
import { activeTab, onScreenRuns, pickTab, resolveView, tileList } from '@/store/tiles'

const viewOf = (value: string) => runViews.find((view) => runViewLabel[view] === value)

/**
 * "Open in this tile": the other views of `runId` first, then every session.
 * It fills an empty tile, and opens from a strip's "+" to add a tab.
 */
export function Picker({ tileId, runId, autoFocus, onPicked, className }: {
  tileId: string
  /** The session whose other views lead: the tile's own, or the one an empty tile was split from. */
  runId: string | undefined
  autoFocus: boolean
  onPicked?: () => void
  className?: string
}) {
  const { layout, ctx, link } = useContext(TileAreaContext)!
  const groups = useSidebarGroups()
  const role = useSelfRole()
  const openDialog = useStore((s) => s.openPaletteDialog)
  const routeView = useStore((s) => s.route.params.view)
  // cmdk reports the item the pointer or the arrow keys are on only while its value is controlled.
  const [pointed, setPointed] = useState('')
  useEffect(() => () => link(null), [link])

  const run = runId ? ctx.runs[runId] : undefined
  const own = activeTab(tileList(layout.root).find((tile) => tile.id === tileId)!)
  // Not offered: the view this tile shows, or the one an empty tile was split from, which the route still names.
  const shown = run && resolveView(own?.runId === run.id ? own : { runId: run.id, view: isRunView(routeView) ? routeView : undefined }, run, ctx.cap, ctx.memory)
  const elsewhere = run ? viewsElsewhere(layout, tileId, run.id, ctx) : {}
  const onScreen = onScreenRuns(layout)
  const rows = groups.map((group) => ({ ...group, rows: group.runs.flatMap((tree) => [tree, ...tree.children]) }))
  const pick = (id: string, view?: (typeof runViews)[number]) => {
    changeTiles((before, context) => pickTab(before, tileId, id, context, view), true)
    onPicked?.()
  }
  const session = (row: RunRow) => (
    <CommandItem key={row.run.id} value={`${runLabel(row.run)} ${row.run.id}`} onSelect={() => pick(row.run.id)}>
      <StatusDot tone={toneOf(row)} />
      <span className="min-w-0 flex-1 truncate">{runLabel(row.run)}</span>
      <RelativeTime at={row.since} compact title={undefined} className="text-ui-sm text-muted" />
      <AgentGlyph agent={row.run.harness} />
    </CommandItem>
  )

  return (
    <section aria-label="Open in this tile" className={cn('flex min-h-0 flex-col gap-2', className)}>
      <h2 className="text-ui font-medium">Open in this tile</h2>
      <Command
        label="Find a session or view"
        className="rounded-none bg-transparent"
        value={pointed}
        onValueChange={(value) => {
          setPointed(value)
          const view = viewOf(value)
          link((view && elsewhere[view]?.tile) ?? null)
        }}
      >
        <CommandInput autoFocus={autoFocus} placeholder="Find a session or view" />
        <CommandList>
          <CommandEmpty>No session or view matches.</CommandEmpty>
          {run && (
            <CommandGroup heading={`Other views of ${runLabel(run)}`} className="[&_[cmdk-group-items]]:flex [&_[cmdk-group-items]]:flex-wrap [&_[cmdk-group-items]]:gap-1">
              {offeredViews(run, ctx.cap).filter((view) => view !== shown).map((view) => {
                const Icon = runViewIcon[view]
                return (
                  <Tooltip key={view} content={elsewhere[view]?.where} disabled={!elsewhere[view]}>
                    <CommandItem value={runViewLabel[view]} onSelect={() => pick(run.id, view)} className="border border-seam">
                      <Icon aria-hidden />
                      {runViewLabel[view]}
                      {elsewhere[view] && <ArrowUpRight aria-hidden className="size-2.5" />}
                    </CommandItem>
                  </Tooltip>
                )
              })}
            </CommandGroup>
          )}
          {rows.map((group) => {
            const off = group.rows.filter((row) => !onScreen.includes(row.run.id))
            return off.length > 0 && <CommandGroup key={group.key} heading={group.label}>{off.map(session)}</CommandGroup>
          })}
          {rows.some((group) => group.rows.some((row) => onScreen.includes(row.run.id))) && (
            <CommandGroup heading="Already on screen">
              {rows.flatMap((group) => group.rows.filter((row) => onScreen.includes(row.run.id))).map(session)}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
      {canLaunch({ cap: ctx.cap, role }) && (
        <Button
          variant="secondary"
          className="self-start"
          onClick={() => {
            onPicked?.()
            openDialog('launch')
          }}
        >
          <Plus aria-hidden />
          New session
        </Button>
      )}
    </section>
  )
}
