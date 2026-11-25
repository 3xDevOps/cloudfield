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
  /** Set by a popover, which closes on a pick; an empty tile's own picker has none and says a drag fills it too. */
  onPicked?: () => void
  className?: string
}) {
  const { layout, ctx, link, noRoom } = useContext(TileAreaContext)!
  const [search, setSearch] = useState('')
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
  const refused = noRoom && onPicked
  const pick = (id: string, view?: (typeof runViews)[number]) => {
    changeTiles((before, context) => pickTab(before, tileId, id, context, view), true)
    onPicked?.()
  }
  const parent = new Map(groups.flatMap((group) => group.runs.flatMap((tree) => tree.children.map((child) => [child.run.id, tree.run.id] as const))))
  const session = (row: RunRow, indent = false) => (
    <CommandItem key={row.run.id} value={`${runLabel(row.run)} ${row.run.id}`} onSelect={() => pick(row.run.id)} className={cn(indent && 'pl-6')}>
      <StatusDot tone={toneOf(row)} />
      <span className="min-w-0 flex-1 truncate">{runLabel(row.run)}</span>
      <RelativeTime at={row.since} compact title={undefined} className="text-ui-sm text-muted" />
      <AgentGlyph agent={row.run.harness} />
    </CommandItem>
  )

  return (
    <section aria-label="Open in this tile" className={cn('flex min-h-0 flex-col gap-2', className)}>
      <h2 className="px-3 text-ui font-medium">Open in this tile</h2>
      {refused && <p className="px-3 text-ui-sm text-muted">No room to split {noRoom}. Add a tab here instead.</p>}
      <Command
        label="Find a session or view"
        className="flex-1 rounded-none bg-transparent not-focus-within:[&_[cmdk-item][data-selected=true]:not(:hover)]:bg-transparent"
        value={pointed}
        onValueChange={(value) => {
          setPointed(value)
          const view = viewOf(value)
          link((view && elsewhere[view]?.tile) ?? null)
        }}
      >
        <CommandInput
          autoFocus={autoFocus}
          placeholder="Find a session or view"
          value={search}
          onValueChange={setSearch}
          wrapperClassName="mx-1 h-8 shrink-0 rounded-control border border-control bg-canvas focus-within:border-accent focus-within:shadow-field-focus coarse:h-11"
          className="px-0 focus-visible:outline-0 focus-visible:shadow-none"
        />
        <CommandList className="max-h-none flex-1 [mask-image:linear-gradient(to_bottom,black_calc(100%-2rem),transparent)] [&_[cmdk-list-sizer]]:pb-8">
          <CommandEmpty>{search ? 'No session or view matches.' : 'No sessions in this workspace yet.'}</CommandEmpty>
          {run && (
            <CommandGroup heading={`Other views of ${runLabel(run)}`} className="[&_[cmdk-group-items]]:grid [&_[cmdk-group-items]]:grid-cols-[repeat(auto-fit,minmax(6rem,1fr))] [&_[cmdk-group-items]]:gap-1">
              {offeredViews(run, ctx.cap).filter((view) => view !== shown).map((view) => {
                const Icon = runViewIcon[view]
                return (
                  <CommandItem key={view} value={runViewLabel[view]} onSelect={() => pick(run.id, view)} className="border border-seam px-0">
                    {/* cmdk takes the item's own pointer moves, which a tooltip opens on. */}
                    <Tooltip content={elsewhere[view]?.where} disabled={!elsewhere[view]} side="bottom">
                      <span className="flex items-center gap-2 self-stretch px-2">
                        <Icon aria-hidden />
                        {runViewLabel[view]}
                        {elsewhere[view] && <ArrowUpRight aria-hidden className="size-2.5" />}
                      </span>
                    </Tooltip>
                  </CommandItem>
                )
              })}
            </CommandGroup>
          )}
          {rows.map((group) => {
            const off = group.rows.filter((row) => !onScreen.includes(row.run.id))
            return off.length > 0 && <CommandGroup key={group.key} heading={group.label}>{off.map((row) => session(row, off.some((above) => above.run.id === parent.get(row.run.id))))}</CommandGroup>
          })}
          {rows.some((group) => group.rows.some((row) => onScreen.includes(row.run.id))) && (
            <CommandGroup heading="Already on screen">
              {rows.flatMap((group) => group.rows.filter((row) => onScreen.includes(row.run.id))).map((row) => session(row))}
            </CommandGroup>
          )}
        </CommandList>
      </Command>
      <div className="flex shrink-0 items-center gap-3 border-t border-seam px-1 pt-2 text-ui-sm text-muted empty:hidden">
        {canLaunch({ cap: ctx.cap, role }) && (
          <Button
            variant="secondary"
            onClick={() => {
              onPicked?.()
              openDialog('launch')
            }}
          >
            <Plus aria-hidden />
            New session
          </Button>
        )}
        {!onPicked && 'or drag a session or a view here'}
      </div>
    </section>
  )
}
