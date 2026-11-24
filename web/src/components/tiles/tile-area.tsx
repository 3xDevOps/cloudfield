import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { changeTiles, TileAreaContext, viewsElsewhere } from '@/components/tiles/area'
import { fits, tileRects } from '@/components/tiles/geometry'
import { Tile } from '@/components/tiles/tile'
import { Splitter } from '@/components/ui/splitter'
import { useElementSize } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { redirectRoute } from '@/lib/url-state'
import type { FrameTile, RunFrame } from '@/routes/run'
import { RunSession } from '@/routes/run/session-host'
import type { RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import {
  activeTab,
  controlsTile,
  focusTile,
  maximizeTile,
  onScreenRuns,
  resolveView,
  routeTiles,
  setRatio,
  showsRoute,
  splitTile,
  tileList,
  tilesAfter,
  visibleTiles,
  type TileTab,
} from '@/store/tiles'

/** A tile narrower than this draws its chrome with icons for words. */
const narrowBelow = 560

/** One session view inside its tile. The focused tile's frame reads the route; the others only their tab. */
function TileFrame({ Frame, run, tab, view, slot, tile, route }: {
  Frame: typeof RunFrame
  run: RunRecord
  tab: TileTab
  view: RunView
  slot: HTMLElement
  tile: FrameTile
  route: Record<string, string>
}) {
  const params = useMemo(
    () => (tile.focused ? { ...route, runId: run.id, view } : { runId: run.id, view, ...(tab.repo && { repo: tab.repo }) }),
    [tile.focused, route, run.id, view, tab.repo],
  )
  return createPortal(<Frame run={run} params={params} tile={tile} />, slot, tab.id)
}

/**
 * The desktop session page: the tile layout, drawn flat so that a tile keeps
 * its place in the tree of elements, and with it its terminal and scroll
 * position, whatever splits and closes around it. `Frame` is the session
 * frame, passed in because its module is the one that renders this area.
 */
export function TileArea({ params, Frame }: { params: Record<string, string>; Frame: typeof RunFrame }) {
  const stored = useStore((s) => s.tiles)
  const runs = useStore((s) => s.runs)
  const memory = useStore((s) => s.runViewMemory)
  const identityKey = useStore((s) => s.identityKey)
  const epoch = useStore((s) => s.terminalCacheEpoch)
  const navigate = useStore((s) => s.navigate)
  const cap = useCapability()
  const ctx = useMemo(() => ({ runs, cap, memory }), [runs, cap, memory])
  const [areaRef, area] = useElementSize<HTMLDivElement>()
  const root = useRef<HTMLDivElement | null>(null)
  const rootRef = useCallback((node: HTMLDivElement | null) => {
    root.current = node
    areaRef(node)
  }, [areaRef])
  const [slots, setSlots] = useState<Record<string, HTMLElement | null>>({})
  const onSlot = useCallback((tileId: string, slot: HTMLElement | null) => setSlots((before) => ({ ...before, [tileId]: slot })), [])
  const [linked, link] = useState<string | null>(null)
  const [said, say] = useState('')

  const tiles = tileList(stored.root)
  const measured = area.width > 0
  const cramped = measured && tiles.length > 1 && !fits(stored.root, area)
  const layout = useMemo(() => (cramped ? { ...stored, maximized: true } : stored), [cramped, stored])
  useEffect(() => {
    useStore.setState({ tilesCramped: cramped })
  }, [cramped])
  const visible = visibleTiles(layout)
  const rects = tileRects(layout.root, area)

  // The layout follows the route when the route changes without it: a deep link, a phone widened to a desktop.
  // No session mounts until it has. An empty focused tile is left alone: it waits for a pick while the route
  // keeps naming the session it was split from. Between route changes the layout leads and `changeTiles`
  // moves the route with it.
  const focusedTab = activeTab(tiles.find((tile) => tile.id === stored.focused)!)
  const held = tiles.some((tile) => tile.tabs.some((tab) => tab.runId === params.runId))
  const synced = focusedTab ? showsRoute(stored, params, ctx) : tiles.some((tile) => activeTab(tile)?.runId === params.runId)
  const landed = useRef<Record<string, string> | null>(null)
  const arrived = synced || landed.current === params || (landed.current !== null && focusedTab?.runId === params.runId)
  useLayoutEffect(() => {
    const fresh = landed.current !== params
    landed.current = params
    // A deleted session takes its tab with it. An empty layout shows it again, as not found; otherwise the route follows the focus.
    if (fresh ? !synced : !held && !focusedTab) useStore.setState((s) => tilesAfter(s, (before, context) => routeTiles(before, params, context)))
    else if (!held) changeTiles((before) => before)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params, held])

  // Native and on capture: a frame is portaled into its tile, and a terminal stops the events it handles.
  useEffect(() => {
    const node = root.current!
    const focus = (event: Event) => {
      const tileId = (event.target as Element).closest<HTMLElement>('[data-tile]')?.dataset.tile
      if (tileId && tileId !== useStore.getState().tiles.focused) changeTiles((before) => focusTile(before, tileId))
    }
    node.addEventListener('pointerdown', focus, true)
    node.addEventListener('focusin', focus, true)
    return () => {
      node.removeEventListener('pointerdown', focus, true)
      node.removeEventListener('focusin', focus, true)
    }
  }, [])

  useKeybindings('run', {
    'leave-run': () => {
      const { agent, ...rest } = params
      if (agent) redirectRoute(useStore, { name: 'run', params: rest })
      else if (stored.maximized) {
        changeTiles((before) => maximizeTile(before, null))
        say('Tiles restored')
      } else navigate('board')
    },
  })

  const value = useMemo(() => ({ layout, cramped, ctx, say, link }), [layout, cramped, ctx])
  const shown = visible.flatMap((tile) => {
    const tab = activeTab(tile)
    const run = tab && runs[tab.runId]
    return run ? [{ tile, tab, run, view: resolveView(tab, run, cap, memory) }] : []
  })

  return (
    <TileAreaContext value={value}>
      <div
        ref={rootRef}
        className="relative h-full min-h-0 overflow-hidden"
      >
        {visible.map((tile) => {
          const rect = layout.maximized ? { left: 0, top: 0, width: 100, height: 100 } : rects.tiles[tile.id]
          return (
            <Tile
              key={tile.id}
              tile={tile}
              rect={rect}
              narrow={measured && (rect.width / 100) * area.width < narrowBelow}
              room={{
                right: !measured || fits(splitTile(stored, tile.id, 'right').root, area),
                down: !measured || fits(splitTile(stored, tile.id, 'down').root, area),
              }}
              linked={linked === tile.id}
              onSlot={onSlot}
            />
          )
        })}
        {!layout.maximized && rects.seams.map((seam) => {
          const row = seam.dir === 'row'
          const extent = ((row ? seam.within.width : seam.within.height) / 100) * (row ? area.width : area.height)
          const resize = (ratio: number) => useStore.setState((s) => tilesAfter(s, (before) => setRatio(before, seam.id, ratio)))
          return (
            <Splitter
              key={seam.id}
              orientation={row ? 'vertical' : 'horizontal'}
              label="Resize the tiles"
              value={seam.share * extent}
              min={seam.min * extent}
              max={seam.max * extent}
              onResize={(size) => resize(size / extent)}
              onEven={() => resize(0.5)}
              style={{ left: `${seam.rect.left}%`, top: `${seam.rect.top}%`, ...(row ? { height: `${seam.rect.height}%` } : { width: `${seam.rect.width}%` }) }}
            />
          )
        })}
        {arrived && onScreenRuns(layout).map((runId) => {
          const run = runs[runId]
          const frames = shown.filter((frame) => frame.run.id === runId)
          if (!run) return null
          const terminalHost = (frames.find((frame) => frame.view === 'terminal') ?? frames[0]).tile.id
          const controls = controlsTile(layout, runId)?.id
          return (
            <RunSession key={JSON.stringify([identityKey, epoch, run.id, run.created_at])} run={run} views={frames.map((frame) => frame.view)}>
              {frames.map(({ tile, tab, view }) => {
                const slot = slots[tile.id]
                const elsewhere = viewsElsewhere(layout, tile.id, runId, ctx)
                return slot && (
                  <TileFrame
                    key={tab.id}
                    Frame={Frame}
                    run={run}
                    tab={tab}
                    view={view}
                    slot={slot}
                    route={params}
                    tile={{
                      focused: tile.id === layout.focused,
                      controls: tile.id === controls,
                      alone: visible.length === 1,
                      terminalHost: tile.id === terminalHost,
                      elsewhere: tiles.flatMap((other) => other.tabs).filter((other) => other.runId === runId && other.id !== tab.id).map((other) => resolveView(other, run, cap, memory)),
                      arrows: Object.fromEntries(Object.entries(elsewhere).map(([held, { where }]) => [held, where])),
                      onArrow: (held) => link(held ? elsewhere[held]?.tile ?? null : null),
                    }}
                  />
                )
              })}
            </RunSession>
          )
        })}
        <span role="status" aria-live="polite" className="sr-only">{said}</span>
      </div>
    </TileAreaContext>
  )
}
