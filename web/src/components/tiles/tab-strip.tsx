import { useContext, useEffect, useRef } from 'react'
import { ChevronDown, Columns2, LayoutGrid, Maximize2, Minimize2, PanelLeft, Plus, Rows2, X } from '@/components/icons'
import { ProtectedMark } from '@/components/protected-mark'
import { changeTiles, focusInTile, TileAreaContext } from '@/components/tiles/area'
import { useTileDrag } from '@/components/tiles/drag'
import { Picker } from '@/components/tiles/picker'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { FadeStrip } from '@/components/ui/fade-strip'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { Popover, PopoverAnchor, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { StatusDot } from '@/components/ui/status-dot'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useElementSize } from '@/lib/hooks'
import { runLabel, stateName, stateTitle, toneOf } from '@/lib/status'
import { redirectRoute } from '@/lib/url-state'
import { cn } from '@/lib/utils'
import { runViewIcon } from '@/routes/run/view-icons'
import { runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { useNeedsYouRuns, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { closeTab, closeTile, focusTile, maximizeTile, resolveView, tileList, visibleTiles, type Tile, type TileTab } from '@/store/tiles'

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
      {/* A background tab keeps its floor for the view's name. */}
      {!narrow && !(several && background) && <AgentGlyph agent={run.harness} />}
      <span className="min-w-0 shrink-[100] truncate" title={`${runLabel(run)}\n${stateTitle(shown, shown.state === 'ready' && run.review_changed_at ? run.review_changed_at : run.stateChangedAt)}`}>{runLabel(run)}</span>
      {several && (
        <span className="flex min-w-0 items-center gap-1 text-muted">
          <span aria-hidden>·</span>
          <Icon aria-hidden className="size-3" />
          <span className="truncate">{runViewLabel[view]}</span>
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
  const shell = useRef<HTMLDivElement>(null)
  const dragFrom = useTileDrag()
  const asks = useNeedsYouRuns([tab.runId]).has(tab.runId)
  useEffect(() => {
    if (active) shell.current?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
  }, [active])
  return (
    <div
      ref={shell}
      data-tab={tab.id}
      className={cn(
        'flex h-full items-center border-r border-seam',
        active ? 'max-w-full shrink-0' : 'max-w-64 min-w-24 overflow-hidden',
        // A background tab that needs you keeps room for its badge and stays in sight however far the strip scrolls.
        !active && asks && cn('sticky inset-x-0 z-1 min-w-40', layout.focused === tile.id ? 'bg-canvas' : 'bg-chrome'),
      )}
    >
      <TabsTrigger
        value={tab.id}
        onPointerDown={dragFrom({ tabId: tab.id, runId: tab.runId, view: run && resolveView(tab, run, ctx.cap, ctx.memory) })}
        className="min-w-0 shrink group-data-[look=underline]/tabs:pr-7 group-data-[look=underline]/tabs:pl-3 [&_[data-slot=status-dot]]:size-2.5"
        onDoubleClick={() => {
          if (cramped || tileList(layout.root).length < 2) return
          changeTiles((before) => maximizeTile(before, tile.id))
          say(`${title} maximized`)
        }}
        onKeyDown={(event) => {
          if (event.key !== 'Delete' && event.key !== 'Backspace') return
          event.preventDefault()
          onClose()
          requestAnimationFrame(() => focusInTile(useStore.getState().tiles.focused, '[role="tab"][data-state="active"]'))
        }}
      >
        {run ? <TabLabel run={run} tab={tab} background={!active} narrow={narrow} /> : title}
      </TabsTrigger>
      <Button variant="ghost" size="icon-sm" tabIndex={-1} label={`Close ${title}`} onClick={onClose} className="mr-1 -ml-7 shrink-0">
        <X />
      </Button>
    </div>
  )
}

/** A parked chip is never cut shorter than this, in pixels: past it the pill folds into a menu. */
const chipFloor = 112

/** The tiles the maximized one parks, those that need you first: a chip each in the room the tabs leave, or one menu. */
function ParkedPill({ tile }: { tile: Tile }) {
  const { layout, ctx, say } = useContext(TileAreaContext)!
  const [roomRef, room] = useElementSize<HTMLDivElement>()
  const all = tileList(layout.root).filter((other) => other.id !== tile.id).map((parked) => {
    const tab = parked.tabs.find((candidate) => candidate.id === parked.active)
    return { parked, tab, run: tab && ctx.runs[tab.runId] }
  })
  const asking = useNeedsYouRuns(all.flatMap(({ run }) => run?.id ?? []))
  const chips = [...all.filter(({ run }) => run && asking.has(run.id)), ...all.filter(({ run }) => !run || !asking.has(run.id))]
  const open = ({ parked, run }: (typeof chips)[number]) => {
    changeTiles((before) => focusTile(before, parked.id))
    say(`${run ? runLabel(run) : 'New tile'} maximized`)
  }
  const label = (chip: (typeof chips)[number]) => (chip.tab && chip.run ? <TabLabel run={chip.run} tab={chip.tab} background narrow={false} /> : 'New tile')
  const pill = 'flex h-6 min-w-0 items-center gap-1 rounded-control border border-seam px-1.5 animate-fade-in motion-reduce:animate-none'
  return (
    <div ref={roomRef} className="flex min-w-24 flex-1 justify-end">
      {room.width > 0 && room.width < chips.length * chipFloor + 32 ? (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="sm" className={cn(pill, 'shrink-0')}>
              <LayoutGrid aria-hidden className="text-muted" />
              {asking.size > 0 && <StatusDot tone="needs-you" />}
              {chips.length} parked
              <ChevronDown aria-hidden />
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            {chips.map((chip) => <MenuItem key={chip.parked.id} onSelect={() => open(chip)}>{label(chip)}</MenuItem>)}
          </MenuContent>
        </Menu>
      ) : (
        <div className={pill}>
          <LayoutGrid aria-hidden className="size-3.5 shrink-0 text-muted" />
          {chips.map((chip) => (
            <Button key={chip.parked.id} variant="ghost" size="sm" className="h-5 min-w-22 shrink gap-1.5 px-1.5" onClick={() => open(chip)}>
              {label(chip)}
            </Button>
          ))}
        </div>
      )}
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
  const { layout, cramped, ctx, say, picking, noRoom, pick, split } = useContext(TileAreaContext)!
  const sidebarCollapsed = useStore((s) => s.sidebarCollapsed)
  const toggleSidebar = useStore((s) => s.toggleSidebar)
  const tiles = tileList(layout.root)
  const focused = layout.focused === tile.id
  // A parked tile keeps the strip it had, in sight under the glide of the tile that covers it.
  const maximized = layout.maximized && focused
  const run = ctx.runs[tile.tabs.find((tab) => tab.id === tile.active)?.runId ?? '']

  const close = (tab: TileTab) => {
    if (tiles.length === 1 && tile.tabs.length === 1) return redirectRoute(useStore, { name: 'board', params: {} })
    changeTiles((before) => closeTab(before, tab.id))
    say('Tab closed')
  }
  const splitButton = (side: 'right' | 'down', Icon: typeof Columns2) => {
    const fits = side === 'right' ? room.right : room.down
    const label = `Split ${side}`
    const button = narrow
      ? <Button variant="ghost" size="icon-sm" label={label} hint={fits ? label : 'No room to split: adds a tab'} onClick={() => split(tile.id, side)}><Icon /></Button>
      : <Button variant="ghost" size="sm" hint={fits ? undefined : 'No room to split: adds a tab'} onClick={() => split(tile.id, side)}><Icon aria-hidden />{label}</Button>
    // The picker a refused split opens hangs from the button that was pressed.
    return picking === tile.id && noRoom === side ? <PopoverAnchor asChild>{button}</PopoverAnchor> : button
  }

  return (
    <Popover open={picking === tile.id} onOpenChange={(open) => pick(open ? tile.id : null)}>
    <div className={cn('flex h-8 shrink-0 items-center gap-1 border-b border-seam pr-1 coarse:h-11', focused ? 'bg-canvas' : 'bg-chrome')}>
      {sidebarCollapsed && visibleTiles(layout)[0] === tile && (
        <Button variant="ghost" size="icon-sm" label="Open sidebar" onClick={toggleSidebar} className="ml-1">
          <PanelLeft />
        </Button>
      )}
      {tile.tabs.length > 0 ? (
        <FadeStrip className={cn('h-full flex-initial', maximized && 'max-w-1/2')}>
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
        <>
          <PopoverTrigger asChild>
            <Button variant="ghost" size="icon-sm" label="Add a tab"><Plus /></Button>
          </PopoverTrigger>
          <PopoverContent className="flex flex-col">
            <Picker tileId={tile.id} runId={run?.id} autoFocus onPicked={() => pick(null)} />
          </PopoverContent>
        </>
      )}
      {maximized ? <ParkedPill tile={tile} /> : <div className="min-w-0 flex-1" />}
      {maximized ? !cramped && (
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
    </Popover>
  )
}
