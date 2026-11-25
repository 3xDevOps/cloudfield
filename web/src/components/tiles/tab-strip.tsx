import { useContext } from 'react'
import { ChevronDown, Columns2, LayoutGrid, Maximize2, Minimize2, PanelLeft, Plus, Rows2, X } from '@/components/icons'
import { ProtectedMark } from '@/components/protected-mark'
import { changeTiles, focusInTile, TileAreaContext } from '@/components/tiles/area'
import { useTileDrag } from '@/components/tiles/drag'
import { Picker } from '@/components/tiles/picker'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
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
import { activateTab, closeTab, closeTile, focusTile, maximizeTile, resolveView, tileList, visibleTiles, type Tile, type TileTab } from '@/store/tiles'

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
      <span className={cn('min-w-[4ch] shrink-[100] truncate', background && 'max-w-60')} title={`${runLabel(run)}\n${stateTitle(shown, shown.state === 'ready' && run.review_changed_at ? run.review_changed_at : run.stateChangedAt)}`}>{runLabel(run)}</span>
      {several && (
        <span data-slot="tab-view" className="flex min-w-0 items-center gap-1 text-muted">
          <span aria-hidden>·</span>
          <Icon aria-hidden className="size-3" />
          <span className={cn('truncate', background && (narrow || asks) && 'sr-only')}>{runViewLabel[view]}</span>
        </span>
      )}
      {run.protected && <ProtectedMark />}
      {badge && <Badge tone="needs-you"><StatusDot tone="needs-you" />Needs you</Badge>}
    </>
  )
}

function SessionTab({ tile, tab, narrow, onClose }: {
  tile: Tile
  tab: TileTab
  /** The tile is narrow or the strip is tight: a background tab is its dot, its title and its view's icon. */
  narrow: boolean
  onClose: () => void
}) {
  const { layout, cramped, ctx, say } = useContext(TileAreaContext)!
  const run = ctx.runs[tab.runId]
  const active = tile.active === tab.id
  const title = run ? runLabel(run) : 'Session'
  const dragFrom = useTileDrag()
  const lifted = useStore((s) => s.tileDrag?.tabId === tab.id)
  return (
    <div
      data-tab={tab.id}
      className={cn(
        'flex h-full max-w-full min-w-28 items-center overflow-hidden border-r border-seam',
        lifted && 'outline-1 -outline-offset-1 outline-control outline-dashed *:opacity-40',
        // A tight strip cuts every background title to the same width, with room for a view's icon clear of the close button.
        narrow && !active && 'flex-1 basis-0 has-data-[slot=tab-view]:min-w-32',
        // The floor that keeps four letters of the title beside a view's name or the badge.
        (active || !narrow) && 'has-data-[slot=badge]:min-w-48 has-data-[slot=tab-view]:min-w-48',
        !active && !narrow && 'shrink-[100]',
      )}
    >
      <TabsTrigger
        value={tab.id}
        onPointerDown={dragFrom({ tabId: tab.id, runId: tab.runId, view: run && resolveView(tab, run, ctx.cap, ctx.memory) })}
        className="min-w-0 shrink group-data-[look=underline]/tabs:pt-0.5 group-data-[look=underline]/tabs:pr-7 group-data-[look=underline]/tabs:pl-3 [&_[data-slot=status-dot]]:size-2.5 [&_[data-slot=tab-view]]:shrink-0"
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

/** A parked chip or a tab is never cut shorter than this, in pixels: past it the pill, or the last tabs, fold into a menu. */
const chipFloor = 112
/** The floor of a tab that names its view or carries the badge. */
const wideFloor = 192
/** The floor of a tight strip's background tab that shows its view's icon. */
const iconFloor = 128

/** The tiles the maximized one parks, those that need you first, each named by the tab that does: a chip each in the room the tabs leave, or one menu. */
function ParkedPill({ tile }: { tile: Tile }) {
  const { layout, ctx, say } = useContext(TileAreaContext)!
  const [roomRef, room] = useElementSize<HTMLDivElement>()
  const others = tileList(layout.root).filter((other) => other.id !== tile.id)
  const asking = useNeedsYouRuns(others.flatMap((parked) => parked.tabs.map((tab) => tab.runId)))
  const all = others.map((parked) => {
    const tab = parked.tabs.find((candidate) => asking.has(candidate.runId)) ?? parked.tabs.find((candidate) => candidate.id === parked.active)
    return { parked, tab, run: tab && ctx.runs[tab.runId] }
  })
  const chips = [...all.filter(({ run }) => run && asking.has(run.id)), ...all.filter(({ run }) => !run || !asking.has(run.id))]
  const open = ({ parked, tab, run }: (typeof chips)[number]) => {
    changeTiles((before) => (tab ? activateTab(before, tab.id) : focusTile(before, parked.id)))
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
  const lifted = useStore((s) => s.tileDrag?.tabId === tile.active)
  const [tabsRef, tabsRoom] = useElementSize<HTMLDivElement>()
  const asking = useNeedsYouRuns(tile.tabs.map((tab) => tab.runId))
  // 36px for "+" and 92px for the menu. Tabs that do not fit at their floor fold into it from the end, those that need you last.
  const tight = tabsRoom.width > 0 && tabsRoom.width < 36 + tile.tabs.length * wideFloor
  const held = tiles.flatMap((other) => other.tabs)
  const floor = (tab: TileTab) => (held.filter((other) => other.runId === tab.runId).length < 2 ? chipFloor : tab.id === tile.active ? wideFloor : iconFloor)
  const over = tight ? 36 + tile.tabs.reduce((sum, tab) => sum + floor(tab), 0) - tabsRoom.width : 0
  const background = tile.tabs.filter((tab) => tab.id !== tile.active).reverse()
  let freed = 0
  const folded = [...background.filter((tab) => !asking.has(tab.runId)), ...background.filter((tab) => asking.has(tab.runId))].filter((tab) => {
    const fold = over > 0 && freed < over + 92
    if (fold) freed += floor(tab)
    return fold
  })

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
      <div ref={tabsRef} className="flex h-full min-w-0 flex-1 items-center gap-1">
        {tile.tabs.length > 0 ? (
          <>
            <TabsList aria-label="Sessions in this tile" className={cn('h-full gap-0 overflow-hidden coarse:h-full', (!focused || lifted) && '[&>[data-slot=sliding-indicator]]:hidden')}>
              {tile.tabs.filter((tab) => !folded.includes(tab)).map((tab) => <SessionTab key={tab.id} tile={tile} tab={tab} narrow={narrow || tight} onClose={() => close(tab)} />)}
            </TabsList>
            {folded.length > 0 && (
              <Menu>
                <MenuTrigger asChild>
                  <Button variant="ghost" size="sm" className="h-6 shrink-0 border border-seam px-1.5">
                    {folded.some((tab) => asking.has(tab.runId)) && <StatusDot tone="needs-you" />}
                    {folded.length} more
                    <ChevronDown aria-hidden />
                  </Button>
                </MenuTrigger>
                <MenuContent>
                  {[...folded].reverse().map((tab) => (
                    <MenuItem key={tab.id} onSelect={() => changeTiles((before) => activateTab(before, tab.id))}>
                      {ctx.runs[tab.runId] ? <TabLabel run={ctx.runs[tab.runId]} tab={tab} background narrow={false} /> : 'Session'}
                    </MenuItem>
                  ))}
                </MenuContent>
              </Menu>
            )}
            <PopoverTrigger asChild>
              <Button variant="ghost" size="icon-sm" label="Add a tab"><Plus /></Button>
            </PopoverTrigger>
            <PopoverContent className="flex max-h-[min(calc(100dvh-16px),var(--radix-popover-content-available-height))] flex-col">
              <Picker tileId={tile.id} runId={run?.id} autoFocus onPicked={() => pick(null)} />
            </PopoverContent>
          </>
        ) : (
          <div className={cn('flex h-full items-center border-b-2 pt-0.5 pl-3 text-ui font-medium', focused ? 'border-accent' : 'border-transparent')}>
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
      </div>
      {maximized && <ParkedPill tile={tile} />}
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
