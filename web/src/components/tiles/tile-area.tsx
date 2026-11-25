import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { focusView } from '@/components/shell/center-view'
import { changeTiles, focusInTile, narrowBelow, TileAreaContext, tileNode, viewsElsewhere } from '@/components/tiles/area'
import { TileDragLayer } from '@/components/tiles/drag'
import { fits, tileRects, type Rect } from '@/components/tiles/geometry'
import { Tile } from '@/components/tiles/tile'
import { Splitter } from '@/components/ui/splitter'
import { prefersReducedMotion, useElementSize } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { play, playEntrance, time } from '@/lib/motion'
import { runLabel } from '@/lib/status'
import { redirectRoute } from '@/lib/url-state'
import type { FrameTile, RunFrame } from '@/routes/run'
import { RunSession } from '@/routes/run/session-host'
import type { RunView } from '@/routes/run/views'
import { useShallow } from 'zustand/react/shallow'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import {
  activeTab,
  closeTab,
  controlsTile,
  focusTile,
  maximizeTile,
  resolveView,
  routeTiles,
  setRatio,
  showsRoute,
  splitTile,
  tileList,
  tilesAfter,
  visibleTiles,
  type Tile as TileNode,
  type TileSide,
  type TileTab,
} from '@/store/tiles'

const focusHeading = (tileId: string) => focusInTile(tileId, 'h1[tabindex], h2[tabindex], input')

const full: Rect = { left: 0, top: 0, width: 100, height: 100 }
const sides = ['left', 'top', 'width', 'height'] as const
const glides = new WeakMap<HTMLElement, Animation>()
/** A gliding tile draws its own edge: its moving side has no seam, and would cut the tile under it mid-word. */
const lifted = { zIndex: 1, boxShadow: '0 0 0 1px var(--control-border)' }

/**
 * Plays a tile from the rectangle it had, in percent of the area, to the one
 * it has now. Its body takes the final width at once and the tile clips it, so
 * nothing rewraps on the way, and so does a strip that grows, which then picks
 * its controls once; a terminal or Browser on show takes the final height too,
 * and is sized once, not on every frame. The end keyframe is
 * spelled out so that a glide cut short by the next one reads where the tile
 * is drawn, not a value already moving to the new rectangle.
 */
function glideTile(tile: HTMLElement, from: Rect, to: Rect): Animation {
  const strip = tile.firstElementChild as HTMLElement
  const body = tile.lastElementChild as HTMLElement
  const running = glides.get(tile)
  const start = Object.fromEntries(sides.map((side) => [side, running ? getComputedStyle(tile)[side] : `${from[side]}%`]))
  running?.cancel()
  body.style.width = body.style.height = body.style.flex = ''
  const { width, height } = body.getBoundingClientRect()
  body.style.width = strip.style.minWidth = `${width}px`
  if ([...body.querySelectorAll('.xterm, canvas')].some((surface) => !surface.closest('[inert]'))) {
    body.style.height = `${height}px`
    body.style.flex = 'none'
  }
  tile.style.overflow = 'clip'
  const end = Object.fromEntries(sides.map((side) => [side, `${to[side]}%`]))
  const animation = play(tile, [{ ...start, ...lifted }, { ...end, ...lifted }], { duration: '--duration-glide', easing: '--ease-glide' })
  glides.set(tile, animation)
  // Held at its start for the frame that paints what the change mounted: the clock would count that work as time passed, and the glide would begin part of the way in.
  animation.pause()
  requestAnimationFrame(() => requestAnimationFrame(() => {
    if (glides.get(tile) === animation) animation.play()
  }))
  const settle = () => {
    if (glides.get(tile) !== animation) return
    glides.delete(tile)
    tile.style.overflow = strip.style.minWidth = body.style.width = body.style.height = body.style.flex = ''
  }
  animation.finished.then(settle, settle)
  return animation
}

const fade = { motion: { duration: '--duration-200', easing: '--ease-enter' } } as const
/** No tile is growing in. `onScreen` is the tabs that were shown before one did. */
const settled = { tiles: [] as string[], onScreen: [] as string[] }

/** Where a tile that was not on screen grows from: the far edge of the room a tile of `before` gave up for it. */
function grownFrom(before: Record<string, Rect>, to: Rect): Rect | undefined {
  const host = Object.values(before).find((rect) =>
    rect.left <= to.left && rect.top <= to.top && rect.left + rect.width >= to.left + to.width && rect.top + rect.height >= to.top + to.height)
  if (!host) return undefined
  if (to.left > host.left) return { ...to, left: to.left + to.width, width: 0 }
  if (to.top > host.top) return { ...to, top: to.top + to.height, height: 0 }
  if (to.width < host.width) return { ...to, width: 0 }
  if (to.height < host.height) return { ...to, height: 0 }
  return undefined
}

interface FrameProps {
  tab: TileTab
  view: RunView
  slot: HTMLElement | null
  tile: FrameTile
}

interface SessionProps {
  Frame: typeof RunFrame
  run: RunRecord
  route: Record<string, string>
  frames: FrameProps[]
}

const sameFrame = (before: FrameProps, now: FrameProps) =>
  before.tab === now.tab && before.view === now.view && before.slot === now.slot && JSON.stringify(before.tile) === JSON.stringify(now.tile)

/** One session view inside its tile. The focused tile's frame reads the route; the others only their tab. */
const TileFrame = memo(function TileFrame({ Frame, run, route, tab, view, slot, tile }: Omit<SessionProps, 'frames'> & FrameProps & { slot: HTMLElement }) {
  const params = useMemo(
    () => (tile.focused ? { ...route, runId: run.id, view } : { runId: run.id, view, ...(tab.repo && { repo: tab.repo }) }),
    [tile.focused, route, run.id, view, tab.repo],
  )
  // The frame mounts a render after the route changed, when focus has already fallen to `main` for want of a heading.
  useEffect(() => {
    if (tile.focused && document.activeElement?.id === 'main') focusView()
  }, [])
  return createPortal(<Frame run={run} params={params} tile={tile} />, slot, tab.id)
}, (before, now) => before.Frame === now.Frame && before.run === now.run && (!now.tile.focused || before.route === now.route) && sameFrame(before, now))

/**
 * A session and its frames. It renders again only when what its tiles show
 * changed, so an event from one session stops at the area for every other.
 */
const SessionFrames = memo(function SessionFrames({ Frame, run, route, frames }: SessionProps) {
  return (
    <RunSession run={run} views={frames.map((frame) => frame.view)}>
      {frames.map(({ slot, ...frame }) => slot && <TileFrame key={frame.tab.id} Frame={Frame} run={run} route={route} slot={slot} {...frame} />)}
    </RunSession>
  )
}, (before, now) =>
  before.Frame === now.Frame && before.run === now.run && (before.route === now.route || !now.frames.some((frame) => frame.tile.focused))
  && before.frames.length === now.frames.length && now.frames.every((frame, index) => sameFrame(before.frames[index], frame)))

/**
 * The desktop session page: the tile layout, drawn flat so that a tile keeps
 * its place in the tree of elements, and with it its terminal and scroll
 * position, whatever splits and closes around it. `Frame` is the session
 * frame, passed in because its module is the one that renders this area.
 */
export function TileArea({ params, Frame }: { params: Record<string, string>; Frame: typeof RunFrame }) {
  const stored = useStore((s) => s.tiles)
  const tiles = tileList(stored.root)
  // Only the sessions the tabs name: an event from any other session renders nothing here.
  const runs = useStore(useShallow((s) => Object.fromEntries(tiles.flatMap((tile) => tile.tabs.map((tab) => [tab.runId, s.runs[tab.runId]])))))
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
  const [picking, setPicking] = useState<string | null>(null)
  const [noRoom, setNoRoom] = useState<TileSide | null>(null)
  const [pressed, setPressed] = useState(false)
  const pick = useCallback((tileId: string | null) => {
    setPicking(tileId)
    setNoRoom(null)
  }, [])

  const measured = area.width > 0
  const cramped = measured && tiles.length > 1 && !fits(stored.root, area)
  const layout = useMemo(() => (cramped ? { ...stored, maximized: true } : stored), [cramped, stored])
  useEffect(() => {
    useStore.setState({ tilesCramped: cramped })
  }, [cramped])
  const visible = visibleTiles(layout)
  // The tiles a maximized one parks stay mounted and hidden where they were, so restoring them reloads nothing.
  // A layout that does not fit has nowhere to keep them.
  const mounted = stored.maximized && !cramped ? tiles : visible
  const rects = tileRects(layout.root, area)
  const drawn = Object.fromEntries(mounted.map((tile) => [tile.id, layout.maximized && tile.id === layout.focused ? full : rects.tiles[tile.id]]))
  const narrow = (tileId: string) => measured && (drawn[tileId].width / 100) * area.width < narrowBelow

  const shown = mounted.flatMap((tile) => {
    const tab = activeTab(tile)
    const run = tab && runs[tab.runId]
    return run ? [{ tile, tab, run, view: resolveView(tab, run, cap, memory) }] : []
  })

  // A seam drag and a window resize change no shape, so they never glide.
  const shape = JSON.stringify([layout.maximized && layout.focused, layout.root], (key, value: unknown) =>
    key === 'ratio' || key === 'tabs' || key === 'active' ? undefined : value,
  )
  const last = useRef({ shape, cramped, drawn, tabs: [] as string[] })
  const ghosts = useRef<HTMLElement[]>([])
  // A tile that grows in is an empty pane until it lands, and a session that was not on screen mounts then: its strip
  // would be cut by the edge it grows from, and the mount would cost the glide its frames.
  const [opening, setOpening] = useState(settled)
  useLayoutEffect(() => {
    const before = last.current
    last.current = { shape, cramped, drawn, tabs: shown.map((frame) => frame.tab.id) }
    const closed = ghosts.current.splice(0)
    const glided: Animation[] = []
    const grew: HTMLElement[] = []
    if (before.shape !== shape && before.cramped === cramped && !prefersReducedMotion()) {
      for (const tile of document.querySelectorAll<HTMLElement>('[data-tile]')) {
        const from = before.drawn[tile.dataset.tile!]
        const to = drawn[tile.dataset.tile!]
        const grown = from ?? grownFrom(before.drawn, to)
        if (!grown) playEntrance(tile.lastElementChild!, fade)
        else if (sides.some((side) => grown[side] !== to[side])) {
          glided.push(glideTile(tile, grown, to))
          if (!from) grew.push(tile)
        }
      }
    }
    if (grew.length && time('--duration-glide')) {
      setOpening({ tiles: grew.map((tile) => tile.dataset.tile!), onScreen: before.tabs })
      void Promise.allSettled(glided.map((glide) => glide.finished)).then(() => {
        setOpening(settled)
        for (const tile of grew) {
          const kept = before.tabs.includes(tiles.find((each) => each.id === tile.dataset.tile)?.active ?? '')
          for (const part of [...tile.firstElementChild!.children, ...(kept ? [] : [tile.lastElementChild!])]) playEntrance(part, fade)
        }
      })
    }
    if (closed.length) void Promise.allSettled(glided.map((glide) => glide.finished)).then(() => closed.forEach((ghost) => ghost.remove()))
  })

  // A closed tile's picture stays under the glides of the tiles that take its room, so the moving edge covers a tile and not a hole.
  useEffect(() => useStore.subscribe((s, before) => {
    if (s.tiles === before.tiles || !time('--duration-glide') || prefersReducedMotion()) return
    const kept = new Set(tileList(s.tiles.root).map((tile) => tile.id))
    for (const closed of tileList(before.tiles.root)) {
      const node = kept.has(closed.id) ? null : tileNode(closed.id)
      if (!node || node.inert) continue
      const ghost = node.cloneNode(true) as HTMLElement
      for (const named of [ghost, ...ghost.querySelectorAll('[id], [data-tile], [data-tab]')]) for (const name of ['id', 'data-tile', 'data-tab']) named.removeAttribute(name)
      ghost.classList.replace('after:border-accent', 'after:border-transparent')
      ghost.firstElementChild!.querySelector('[data-slot=sliding-indicator]')?.remove()
      ghost.inert = true
      ghost.ariaHidden = 'true'
      node.parentElement!.prepend(ghost)
      ghosts.current.push(ghost)
    }
  }), [])

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
  }, [params, held])

  // After the route is followed: a view that stopped being offered, on a session that finished for instance, would leave two tabs showing one thing.
  useLayoutEffect(() => changeTiles((before) => before), [ctx])

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
      setPressed(event.type === 'pointerdown')
      changeTiles((before) => focusTile(before, tileId))
    }
    // After the click that follows the release.
    const release = () => setTimeout(() => setPressed(false))
    const press = (event: Event) => {
      tabbed = event instanceof KeyboardEvent && event.key === 'Tab'
      stray.current = false
      if (event.type === 'pointerdown') enter(event)
    }
    window.addEventListener('pointerdown', press, true)
    window.addEventListener('pointerup', release, true)
    window.addEventListener('pointercancel', release, true)
    window.addEventListener('keydown', press, true)
    window.addEventListener('focusin', enter, true)
    return () => {
      window.removeEventListener('pointerdown', press, true)
      window.removeEventListener('pointerup', release, true)
      window.removeEventListener('pointercancel', release, true)
      window.removeEventListener('keydown', press, true)
      window.removeEventListener('focusin', enter, true)
    }
  }, [])

  const value = useMemo(() => ({
    layout,
    cramped,
    ctx,
    say,
    link,
    picking,
    noRoom,
    pick,
    pressed,
    split: (tileId: string, side: 'right' | 'down') => {
      if (measured && !fits(splitTile(stored, tileId, side).root, area)) {
        pick(tileId)
        setNoRoom(side)
        say('No room to split: adds a tab')
        return
      }
      changeTiles((before) => splitTile(before, tileId, side))
      say(`Split ${side}`)
    },
  }), [layout, cramped, ctx, picking, noRoom, pick, pressed, stored, measured, area])

  const several = tiles.length > 1
  const focus = (tile: TileNode) => {
    const run = runs[activeTab(tile)?.runId ?? '']
    changeTiles((before) => focusTile(before, tile.id))
    say(`${run ? runLabel(run) : 'New tile'} ${layout.maximized ? 'maximized' : 'focused'}`)
  }
  useKeybindings('run', {
    'leave-run': () => {
      const { agent, ...rest } = params
      if (agent) redirectRoute(useStore, { name: 'run', params: rest })
      else if (stored.maximized) {
        changeTiles((before) => maximizeTile(before, null))
        say('Tiles restored')
      } else navigate('board')
    },
    ...(focusedTab && {
      'tile-picker': () => pick(stored.focused),
      ...(!layout.maximized && {
        'tile-split-right': (event: KeyboardEvent) => {
          event.preventDefault()
          value.split(stored.focused, 'right')
        },
        'tile-split-down': (event: KeyboardEvent) => {
          event.preventDefault()
          value.split(stored.focused, 'down')
        },
      }),
      ...(tiles.flatMap((tile) => tile.tabs).length > 1 && {
        'tile-close-tab': () => {
          changeTiles((before) => closeTab(before, focusedTab.id))
          say('Tab closed')
          requestAnimationFrame(() => focusHeading(useStore.getState().tiles.focused))
        },
      }),
    }),
    ...(several && {
      ...Object.fromEntries(tiles.slice(0, 9).map((tile, index) => [`tile-focus-${index + 1}`, (event: KeyboardEvent) => {
        event.preventDefault()
        focus(tile)
      }])),
      'tile-next': () => focus(tiles[(tiles.findIndex((tile) => tile.id === stored.focused) + 1) % tiles.length]),
    }),
    ...(several && !cramped && {
      'tile-maximize': () => {
        changeTiles((before) => maximizeTile(before, before.maximized ? null : before.focused))
        say(stored.maximized ? 'Tiles restored' : 'Tile maximized')
      },
    }),
  })

  return (
    <TileAreaContext value={value}>
      <div
        ref={areaRef}
        className="relative h-full min-h-0 overflow-hidden"
      >
        {mounted.map((tile) => (
          <Tile
            key={tile.id}
            tile={tile}
            rect={drawn[tile.id]}
            parked={!visible.includes(tile)}
            opening={opening.tiles.includes(tile.id)}
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
        {arrived && [...new Set(shown.map((frame) => frame.run))].map((run) => {
          const runId = run.id
          const frames = shown.filter((frame) => frame.run === run)
          const terminalHost = (frames.find((frame) => frame.view === 'terminal') ?? frames[0]).tile.id
          return (
            <SessionFrames
              key={JSON.stringify([identityKey, epoch, run.id, run.created_at])}
              Frame={Frame}
              run={run}
              route={params}
              frames={frames.map(({ tile, tab, view }) => {
                // A parked tile is told what it was told before it was parked.
                const on = visible.includes(tile) ? layout : { ...layout, maximized: false }
                return {
                  tab,
                  view,
                  slot: opening.tiles.includes(tile.id) && !opening.onScreen.includes(tab.id) ? null : slots[tile.id] ?? null,
                  tile: {
                    focused: tile.id === layout.focused,
                    controls: tile.id === controlsTile(on, runId)?.id,
                    alone: visible.length === 1 && visible.includes(tile),
                    terminalHost: tile.id === terminalHost,
                    chatOnScreen: frames.some((frame) => frame.view === 'session' && frame.tile !== tile && visibleTiles(on).includes(frame.tile)),
                    elsewhere: tiles.flatMap((other) => other.tabs).filter((other) => other.runId === runId && other.id !== tab.id).map((other) => resolveView(other, run, cap, memory)),
                    arrows: viewsElsewhere(on, tile.id, runId, ctx),
                    onArrow: link,
                  },
                }
              })}
            />
          )
        })}
        <span role="status" aria-live="polite" className="sr-only">{said}</span>
        <TileDragLayer />
      </div>
    </TileAreaContext>
  )
}
