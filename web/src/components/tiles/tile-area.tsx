import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { changeTiles, TileAreaContext, viewsElsewhere } from '@/components/tiles/area'
import { fits, tileRects, type Rect } from '@/components/tiles/geometry'
import { Tile } from '@/components/tiles/tile'
import { Splitter } from '@/components/ui/splitter'
import { prefersReducedMotion, useElementSize } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { play, playEntrance } from '@/lib/motion'
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

/** A tile narrower than this draws its strip with icons for words. */
const narrowBelow = 560

function focusHeading(tileId: string) {
  const tile = [...document.querySelectorAll<HTMLElement>('[data-tile]')].find((node) => node.dataset.tile === tileId)
  tile?.querySelector<HTMLElement>('h1[tabindex], h2[tabindex]')?.focus({ preventScroll: true })
}

const full: Rect = { left: 0, top: 0, width: 100, height: 100 }
const sides = ['left', 'top', 'width', 'height'] as const
const glides = new WeakMap<HTMLElement, Animation>()

/**
 * Plays a tile from the rectangle it had, in percent of the area, to the one
 * it has now. Its body takes the final size at once and the tile clips it, so
 * a terminal or Browser inside is sized once, not on every frame. The end
 * keyframe is spelled out so that a glide cut short by the next one reads
 * where the tile is drawn, not a value already moving to the new rectangle.
 */
function glideTile(tile: HTMLElement, from: Rect, to: Rect) {
  const body = tile.lastElementChild as HTMLElement
  const running = glides.get(tile)
  const start = Object.fromEntries(sides.map((side) => [side, running ? getComputedStyle(tile)[side] : `${from[side]}%`]))
  running?.cancel()
  body.style.width = body.style.height = body.style.flex = ''
  const { width, height } = body.getBoundingClientRect()
  body.style.width = `${width}px`
  body.style.height = `${height}px`
  body.style.flex = 'none'
  tile.style.overflow = 'clip'
  const end = Object.fromEntries(sides.map((side) => [side, `${to[side]}%`]))
  const animation = play(tile, [{ ...start, zIndex: 1 }, { ...end, zIndex: 1 }], { duration: '--duration-glide', easing: '--ease-glide' })
  glides.set(tile, animation)
  const settle = () => {
    if (glides.get(tile) !== animation) return
    glides.delete(tile)
    tile.style.overflow = body.style.width = body.style.height = body.style.flex = ''
  }
  animation.finished.then(settle, settle)
}

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
  const drawn = Object.fromEntries(visible.map((tile) => [tile.id, layout.maximized ? full : rects.tiles[tile.id]]))
  const narrow = (tileId: string) => measured && (drawn[tileId].width / 100) * area.width < narrowBelow

  // A seam drag and a window resize change no shape, so they never glide.
  const shape = JSON.stringify([layout.maximized && layout.focused, layout.root], (key, value: unknown) =>
    key === 'ratio' || key === 'tabs' || key === 'active' ? undefined : value,
  )
  const last = useRef({ shape, cramped, drawn })
  useLayoutEffect(() => {
    const before = last.current
    last.current = { shape, cramped, drawn }
    if (before.shape === shape || before.cramped !== cramped || prefersReducedMotion()) return
    for (const tile of document.querySelectorAll<HTMLElement>('[data-tile]')) {
      const from = before.drawn[tile.dataset.tile!]
      const to = drawn[tile.dataset.tile!]
      if (!from) playEntrance(tile.lastElementChild!, { motion: { duration: '--duration-200', easing: '--ease-enter' } })
      else if (sides.some((side) => from[side] !== to[side])) glideTile(tile, from, to)
    }
  })

  // No session mounts until the layout has followed the route.
  const focusedTab = activeTab(tiles.find((tile) => tile.id === stored.focused)!)
  const held = tiles.some((tile) => tile.tabs.some((tab) => tab.runId === params.runId))
  const synced = focusedTab ? showsRoute(stored, params, ctx) : tiles.some((tile) => activeTab(tile)?.runId === params.runId)
  const landed = useRef<Record<string, string> | null>(null)
  const arrived = synced || landed.current === params || (landed.current !== null && focusedTab?.runId === params.runId)
  useLayoutEffect(() => {
    const fresh = landed.current !== params
    landed.current = params
    // A deleted session takes its tab with it. An empty layout shows it again, as not found; otherwise the route follows the focus.
    if (fresh ? !synced : !held && tiles.every((tile) => !tile.tabs.length)) useStore.setState((s) => tilesAfter(s, (before, context) => routeTiles(before, params, context)))
    else if (!held) changeTiles((before) => before)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [params, held])

  // Only a press or the Tab key focuses a tile: after a jump to another tile, a closing popover or the pressed
  // control takes DOM focus back, and that focus is sent on to the focused tile, once.
  // Native and on capture: a frame is portaled into its tile, and a terminal stops the events it handles.
  const byHand = useRef(stored.focused)
  const stray = useRef(false)
  useLayoutEffect(() => {
    if (byHand.current === stored.focused) return
    byHand.current = stored.focused
    stray.current = true
    const from = document.activeElement?.closest<HTMLElement>('[data-tile]')
    if (from && from.dataset.tile !== stored.focused) focusHeading(stored.focused)
  }, [stored.focused])
  useEffect(() => {
    let tabbed = false
    const enter = (event: Event) => {
      const tileId = (event.target as Element).closest<HTMLElement>('[data-tile]')?.dataset.tile
      const focused = useStore.getState().tiles.focused
      if (!tileId || tileId === focused) return
      if (event.type === 'focusin' && !tabbed) {
        if (stray.current) focusHeading(focused)
        stray.current = false
        return
      }
      byHand.current = tileId
      changeTiles((before) => focusTile(before, tileId))
    }
    const press = (event: Event) => {
      tabbed = event instanceof KeyboardEvent && event.key === 'Tab'
      stray.current = false
      if (event.type === 'pointerdown') enter(event)
    }
    window.addEventListener('pointerdown', press, true)
    window.addEventListener('keydown', press, true)
    window.addEventListener('focusin', enter, true)
    return () => {
      window.removeEventListener('pointerdown', press, true)
      window.removeEventListener('keydown', press, true)
      window.removeEventListener('focusin', enter, true)
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
        ref={areaRef}
        className="relative h-full min-h-0 overflow-hidden"
      >
        {visible.map((tile) => (
          <Tile
            key={tile.id}
            tile={tile}
            rect={drawn[tile.id]}
            narrow={narrow(tile.id)}
            room={{
              right: !measured || fits(splitTile(stored, tile.id, 'right').root, area),
              down: !measured || fits(splitTile(stored, tile.id, 'down').root, area),
            }}
            linked={linked === tile.id}
            onSlot={onSlot}
          />
        ))}
        {measured && !layout.maximized && rects.seams.map((seam) => {
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
                      chatOnScreen: frames.some((frame) => frame.view === 'session' && frame.tile !== tile),
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
