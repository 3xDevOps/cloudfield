import { useContext, useState } from 'react'
import { Columns2, LayoutGrid, Maximize2, Minimize2, PanelLeft, Plus, Rows2, X } from '@/components/icons'
import { ProtectedMark } from '@/components/protected-mark'
import { changeTiles, TileAreaContext } from '@/components/tiles/area'
import { Picker } from '@/components/tiles/picker'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FadeStrip } from '@/components/ui/fade-strip'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { StatusDot } from '@/components/ui/status-dot'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { runLabel, stateName, stateTitle, toneOf } from '@/lib/status'
import { cn } from '@/lib/utils'
import { runViewIcon } from '@/routes/run/view-icons'
import { runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { useNeedsYouRuns, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { closeTab, closeTile, focusTile, maximizeTile, resolveView, splitTile, tileList, visibleTiles, type Tile, type TileSide, type TileTab } from '@/store/tiles'

/** What a tab or a parked tile's chip says about its session: the title, and the view when the session has two or more tabs. */
function TabLabel({ run, tab, background, narrow }: { run: RunRecord; tab: TileTab; background: boolean; narrow: boolean }) {
  const { layout, ctx } = useContext(TileAreaContext)!
  const shown = useRunPresentation(run)
  const asks = useNeedsYouRuns([run.id]).has(run.id)
  const view = resolveView(tab, run, ctx.cap, ctx.memory)
  const several = tileList(layout.root).flatMap((tile) => tile.tabs).filter((other) => other.runId === run.id).length > 1
  const badge = asks && background && !narrow
  const Icon = runViewIcon[view]
  return (
    <>
      <span className="sr-only">{asks ? 'Needs you' : stateName(shown)}: </span>
      {!badge && <StatusDot tone={asks ? 'needs-you' : toneOf(shown)} />}
      {!narrow && <AgentGlyph agent={run.harness} />}
      <span className="min-w-0 truncate" title={stateTitle(shown, shown.state === 'ready' && run.review_changed_at ? run.review_changed_at : run.stateChangedAt)}>{runLabel(run)}</span>
      {several && (
        <span className="flex shrink-0 items-center gap-1 text-muted">
          <span aria-hidden>·</span>
          <Icon aria-hidden className="size-3" />
          {runViewLabel[view]}
        </span>
      )}
      {run.protected && <ProtectedMark />}
      {badge && <Badge tone="needs-you">Needs you</Badge>}
    </>
  )
}

function SessionTab({ tile, tab, narrow, onClose }: { tile: Tile; tab: TileTab; narrow: boolean; onClose: () => void }) {
  const { layout, cramped, ctx, say } = useContext(TileAreaContext)!
  const run = ctx.runs[tab.runId]
  const active = tile.active === tab.id
  const title = run ? runLabel(run) : 'Session'
  return (
    <div className={cn('flex h-full min-w-0 items-center', active ? 'shrink-0' : 'min-w-24')}>
      <TabsTrigger
        value={tab.id}
        className={cn('min-w-0 shrink px-2', layout.focused !== tile.id && 'data-[state=active]:text-muted')}
        onDoubleClick={() => {
          if (cramped || tileList(layout.root).length < 2) return
          changeTiles((before) => maximizeTile(before, tile.id))
          say(`${title} maximized`)
        }}
        onKeyDown={(event) => {
          if (event.key !== 'Delete' && event.key !== 'Backspace') return
          event.preventDefault()
          onClose()
          requestAnimationFrame(() => {
            const taker = [...document.querySelectorAll<HTMLElement>('[data-tile]')].find((node) => node.dataset.tile === useStore.getState().tiles.focused)
            taker?.querySelector<HTMLElement>('[role="tab"][data-state="active"]')?.focus()
          })
        }}
      >
        {run ? <TabLabel run={run} tab={tab} background={!active} narrow={narrow} /> : title}
      </TabsTrigger>
      <Button variant="ghost" size="icon-sm" tabIndex={-1} label={`Close ${title}`} onClick={onClose} className="-ml-1 shrink-0">
        <X />
      </Button>
    </div>
  )
}

/** A tile's top row: its session tabs, "+", and on the right the split and maximize controls or the parked tiles. */
export function TabStrip({ tile, narrow, room }: {
  tile: Tile
  /** The tile is under 560px wide: icons for words, and fewer controls. */
  narrow: boolean
  /** Whether the area has room for a tile on each side of this one. */
  room: { right: boolean; down: boolean }
}) {
  const { layout, cramped, ctx, say } = useContext(TileAreaContext)!
  const navigate = useStore((s) => s.navigate)
  const sidebarCollapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const [adding, setAdding] = useState(false)
  const tiles = tileList(layout.root)
  const focused = layout.focused === tile.id
  const run = ctx.runs[tile.tabs.find((tab) => tab.id === tile.active)?.runId ?? '']

  const close = (tab: TileTab) => {
    if (tiles.length === 1 && tile.tabs.length === 1) return navigate('board')
    changeTiles((before) => closeTab(before, tab.id))
    say('Tab closed')
  }
  const split = (side: TileSide, fits: boolean) => {
    if (!fits) {
      setAdding(true)
      return
    }
    changeTiles((before) => splitTile(before, tile.id, side))
    say(`Split ${side}`)
  }
  const splitButton = (side: 'right' | 'down', Icon: typeof Columns2) => {
    const fits = side === 'right' ? room.right : room.down
    const label = `Split ${side}`
    return narrow
      ? <Button variant="ghost" size="icon-sm" label={label} hint={fits ? label : 'No room to split: adds a tab'} onClick={() => split(side, fits)}><Icon /></Button>
      : <Button variant="ghost" size="sm" hint={fits ? undefined : 'No room to split: adds a tab'} onClick={() => split(side, fits)}><Icon aria-hidden />{label}</Button>
  }

  return (
    <div className={cn('flex h-8 shrink-0 items-center gap-1 border-b border-seam pr-1 coarse:h-11', focused ? 'bg-canvas' : 'bg-chrome')}>
      {sidebarCollapsed && visibleTiles(layout)[0] === tile && (
        <Button variant="ghost" size="icon-sm" label="Open sidebar" onClick={toggleSidebar} className="ml-1">
          <PanelLeft />
        </Button>
      )}
      {tile.tabs.length > 0 ? (
        <FadeStrip className="h-full flex-initial">
          <TabsList aria-label="Sessions in this tile" className={cn('h-full gap-0 coarse:h-full', !focused && '[&>[data-slot=sliding-indicator]]:hidden')}>
            {tile.tabs.map((tab) => <SessionTab key={tab.id} tile={tile} tab={tab} narrow={narrow} onClose={() => close(tab)} />)}
          </TabsList>
        </FadeStrip>
      ) : (
        <div className="flex h-full items-center border-b-2 border-accent pl-3 text-ui font-medium">
          New tile
          <Button
            variant="ghost"
            size="icon-sm"
            label="Close tile"
            onClick={() => {
              changeTiles((before) => closeTile(before, tile.id))
              say('Tile closed')
            }}
          >
            <X />
          </Button>
        </div>
      )}
      {tile.tabs.length > 0 && (
        <Popover open={adding} onOpenChange={setAdding}>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="Add a tab"><Plus /></Button>
          </PopoverTrigger>
          <PopoverContent>
            <Picker tileId={tile.id} runId={run?.id} autoFocus onPicked={() => setAdding(false)} />
          </PopoverContent>
        </Popover>
      )}
      <span className="flex-1" />
      {layout.maximized ? (
        <>
          <div className="flex h-6 min-w-0 items-center gap-1 rounded-control border border-seam px-1.5 animate-fade-in motion-reduce:animate-none">
            <LayoutGrid aria-hidden className="size-3.5 shrink-0 text-muted" />
            <FadeStrip className="flex items-center gap-1">
              {tiles.filter((parked) => parked.id !== tile.id).map((parked) => {
                const tab = parked.tabs.find((candidate) => candidate.id === parked.active)
                const parkedRun = tab && ctx.runs[tab.runId]
                return (
                  <Button
                    key={parked.id}
                    variant="ghost"
                    size="sm"
                    className="h-5 max-w-48 shrink-0 gap-1.5 px-1.5"
                    onClick={() => {
                      changeTiles((before) => focusTile(before, parked.id))
                      say(`${parkedRun ? runLabel(parkedRun) : 'New tile'} maximized`)
                    }}
                  >
                    {tab && parkedRun ? <TabLabel run={parkedRun} tab={tab} background narrow /> : 'New tile'}
                  </Button>
                )
              })}
            </FadeStrip>
          </div>
          {!cramped && (
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                changeTiles((before) => maximizeTile(before, null))
                say('Tiles restored')
              }}
            >
              <Minimize2 aria-hidden />
              Restore tiles
            </Button>
          )}
        </>
      ) : (
        <>
          {focused && tile.tabs.length > 0 && splitButton('right', Columns2)}
          {focused && tile.tabs.length > 0 && splitButton('down', Rows2)}
          {tiles.length > 1 && (focused || !narrow) && tile.tabs.length > 0 && (
            <Button
              variant="ghost"
              size="icon-sm"
              label="Maximize"
              onClick={() => {
                changeTiles((before) => maximizeTile(before, tile.id))
                say('Tile maximized')
              }}
            >
              <Maximize2 />
            </Button>
          )}
        </>
      )}
    </div>
  )
}
