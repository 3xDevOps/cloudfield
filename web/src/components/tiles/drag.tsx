import { useContext, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { createPortal } from 'react-dom'
import { Columns2, Plus, Rows2 } from '@/components/icons'
import { changeTiles, TileAreaContext } from '@/components/tiles/area'
import { dropZone, fits, type Rect } from '@/components/tiles/geometry'
import { AgentGlyph } from '@/components/ui/agent-glyph'
import { StatusDot } from '@/components/ui/status-dot'
import { useDrag } from '@/lib/hooks'
import { runLabel, toneOf } from '@/lib/status'
import { cn } from '@/lib/utils'
import { runViewIcon } from '@/routes/run/view-icons'
import { runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { dropTab, splitTile, tileList, viewHolder, type TileDragSource, type TileSide, type ViewContext } from '@/store/tiles'

/** How far a press travels, in pixels, before it is a drag and not a click. */
const threshold = 4

/** The handler that lets a press on a tab, a view or a sidebar row lift it once the pointer has moved. A mouse or a pen only. */
export function useTileDrag(): (source: TileDragSource) => (event: React.PointerEvent) => void {
  const beginDrag = useDrag()
  return (source) => (event) => {
    if (event.button !== 0 || event.pointerType === 'touch' || !document.querySelector('[data-tile]')) return
    const { clientX, clientY } = event
    const from = event.currentTarget.getBoundingClientRect()
    const drag = beginDrag()
    for (const native of ['selectstart', 'dragstart']) window.addEventListener(native, (ev) => ev.preventDefault(), { signal: drag.signal })
    for (const end of ['pointerup', 'pointercancel']) window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
    window.addEventListener('pointermove', (ev) => {
      if (Math.hypot(ev.clientX - clientX, ev.clientY - clientY) < threshold) return
      drag.abort()
      useStore.setState({ tileDrag: { ...source, from, x: ev.clientX, y: ev.clientY } })
    }, { signal: drag.signal })
  }
}

interface DropTarget {
  tileId: string
  zone: TileSide | 'center'
  /** Where a moved tab lands among the strip's other tabs. */
  index?: number
  /** What the drop will cover, in viewport pixels. */
  rect: Rect
  tile: DOMRect
}

const inside = (rect: DOMRect, x: number, y: number) => x >= rect.left && x < rect.right && y >= rect.top && y < rect.bottom

/** What a drop at a point does, or null where it would change nothing: also over what was lifted, so a shaky click is not a drop. */
function targetAt(source: TileDragSource & { from: DOMRect }, x: number, y: number, ctx: ViewContext): DropTarget | null {
  if (inside(source.from, x, y)) return null
  const layout = useStore.getState().tiles
  const held = source.tabId ?? (source.view && viewHolder(layout, source.runId, source.view, ctx)?.tab.id)
  for (const node of document.querySelectorAll<HTMLElement>('[data-tile]:not([inert])')) {
    const tile = node.getBoundingClientRect()
    if (!inside(tile, x, y)) continue
    const tileId = node.dataset.tile!
    const tabs = tileList(layout.root).find((candidate) => candidate.id === tileId)!.tabs
    const own = tabs.some((tab) => tab.id === held)
    if (own && tabs.length === 1) return null
    const strip = node.firstElementChild!.getBoundingClientRect()
    if (source.tabId && y < strip.bottom) {
      const others = [...node.querySelectorAll<HTMLElement>('[data-tab]')].filter((tab) => tab.dataset.tab !== source.tabId).map((tab) => tab.getBoundingClientRect())
      const index = others.filter((tab) => tab.left + tab.width / 2 < x).length
      const edge = index < others.length ? others[index].left : others.at(-1)?.right ?? strip.left
      return { tileId, zone: 'center', index, rect: { left: edge - 1, top: strip.top, width: 2, height: strip.height }, tile }
    }
    const side = y < strip.bottom ? 'center' : dropZone(tile, x - tile.left, y - tile.top)
    const zone = side !== 'center' && !layout.maximized && fits(splitTile(layout, tileId, side).root, node.parentElement!.getBoundingClientRect()) ? side : 'center'
    if (zone === 'center' && own) return null
    const row = zone === 'left' || zone === 'right'
    const column = zone === 'up' || zone === 'down'
    return {
      tileId,
      zone,
      rect: {
        left: tile.left + (zone === 'right' ? tile.width / 2 : 0),
        top: tile.top + (zone === 'down' ? tile.height / 2 : 0),
        width: row ? tile.width / 2 : tile.width,
        height: column ? tile.height / 2 : tile.height,
      },
      tile,
    }
  }
  return null
}

function swallowClick() {
  const swallow = (event: Event) => {
    event.stopPropagation()
    event.preventDefault()
  }
  window.addEventListener('click', swallow, { capture: true, once: true })
  setTimeout(() => window.removeEventListener('click', swallow, true))
}

function ChipSession({ run }: { run: RunRecord }) {
  const shown = useRunPresentation(run)
  return (
    <>
      <StatusDot tone={toneOf(shown)} />
      <AgentGlyph agent={run.harness} />
    </>
  )
}

const actionIcon = { left: Columns2, right: Columns2, up: Rows2, down: Rows2, center: Plus }

/**
 * A drag in flight: the cover that keeps a terminal or a Browser from taking
 * the pointer, the preview of where the drop lands and the chip that names it.
 * Escape cancels, and the click that follows the release is swallowed. Over
 * what was lifted the cover lets the pointer through, so a release there is
 * still the click it began as.
 */
export function TileDragLayer() {
  const drag = useStore((s) => s.tileDrag)
  const { ctx, say } = useContext(TileAreaContext)!
  const [moved, setMoved] = useState<{ x: number; y: number } | null>(null)
  const chip = useRef<HTMLDivElement>(null)
  const point = moved ?? drag
  const target = drag && point && targetAt(drag, point.x, point.y, ctx)
  const run = useStore((s) => (s.tileDrag ? s.runs[s.tileDrag.runId] : undefined))
  const title = run ? runLabel(run) : 'Session'
  const name = drag?.view ? `${title} · ${runViewLabel[drag.view]}` : title
  const action = (to: DropTarget) => (to.zone !== 'center' ? `Split ${to.zone}` : to.index === undefined ? 'Add as tab' : 'Move tab')

  useLayoutEffect(() => {
    if (!drag) return
    const active = new AbortController()
    const { signal } = active
    const end = () => useStore.setState({ tileDrag: null })
    for (const native of ['selectstart', 'dragstart']) window.addEventListener(native, (event) => event.preventDefault(), { signal })
    window.addEventListener('pointermove', (event) => {
      // A button let go outside the window sends no pointerup.
      if (event.buttons === 0) end()
      else setMoved({ x: event.clientX, y: event.clientY })
    }, { signal })
    window.addEventListener('pointerup', (event) => {
      end()
      if (inside(drag.from, event.clientX, event.clientY)) return
      swallowClick()
      const to = targetAt(drag, event.clientX, event.clientY, ctx)
      if (!to) return
      changeTiles((before, context) => dropTab(before, drag, to.tileId, to.zone, context, to.index), true)
      say(`${name}: ${action(to)}`)
    }, { signal })
    window.addEventListener('pointercancel', end, { signal })
    window.addEventListener('keydown', (event) => {
      event.stopPropagation()
      if (event.key !== 'Escape') return
      event.preventDefault()
      const released = new AbortController()
      for (const type of ['pointerup', 'pointerdown']) {
        window.addEventListener(type, () => {
          released.abort()
          if (type === 'pointerup') swallowClick()
        }, { capture: true, signal: released.signal })
      }
      end()
      say('Drag cancelled')
    }, { capture: true, signal })
    return () => {
      active.abort()
      setMoved(null)
      end()
    }
  }, [drag])

  useLayoutEffect(() => {
    const node = chip.current
    if (!node || !point) return
    const bounds = target?.tile ?? { left: 0, top: 0, right: window.innerWidth, bottom: window.innerHeight }
    node.style.left = `${Math.max(bounds.left + 4, Math.min(point.x + 12, bounds.right - node.offsetWidth - 4))}px`
    node.style.top = `${Math.max(bounds.top + 4, Math.min(point.y + 16, bounds.bottom - node.offsetHeight - 4))}px`
  })

  if (!drag) return null
  const ViewIcon = drag.view && runViewIcon[drag.view]
  const ActionIcon = target && actionIcon[target.zone]
  return createPortal(
    <div aria-hidden className={cn('fixed inset-0 z-50 cursor-grabbing select-none', point && inside(drag.from, point.x, point.y) && 'pointer-events-none')}>
      {target && (
        <div
          data-slot="drop-preview"
          style={target.rect}
          className="fixed flex items-center justify-center overflow-hidden border border-accent bg-canvas text-ui font-medium transition-[left,top,width,height] duration-hover ease-hover before:absolute before:inset-0 before:bg-accent-soft motion-reduce:transition-none"
        >
          {target.index === undefined && (
            <span className="relative flex min-w-0 items-center gap-2 px-3">
              {ViewIcon && <ViewIcon className="size-3.5 shrink-0 text-accent" />}
              <span className="truncate">{drag.view ? `${runViewLabel[drag.view]} - ${title}` : title}</span>
            </span>
          )}
        </div>
      )}
      <div
        ref={chip}
        data-slot="drag-chip"
        style={{ maxWidth: (target?.tile.width ?? window.innerWidth) - 8 }}
        className="fixed flex h-7 items-center gap-2 rounded-control border border-seam bg-raised px-2 text-ui font-medium shadow-overlay"
      >
        {run && <ChipSession run={run} />}
        <span className="min-w-0 truncate">{title}</span>
        {ViewIcon && (
          <span className="flex shrink-0 items-center gap-1 text-muted">
            <span>·</span>
            <ViewIcon className="size-3.5" />
            {runViewLabel[drag.view!]}
          </span>
        )}
        {target && ActionIcon && (
          <span className="flex shrink-0 items-center gap-1.5 border-l border-seam pl-2 text-accent">
            <ActionIcon className="size-3.5" />
            {action(target)}
          </span>
        )}
      </div>
    </div>,
    document.body,
  )
}
