import { useContext, useLayoutEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { createPortal } from 'react-dom'
import { ArrowRightLeft, Columns2, Plus, Rows2 } from '@/components/icons'
import { changeTiles, TileAreaContext, tileNode } from '@/components/tiles/area'
import { dropZone, fits, tileRects, type Rect } from '@/components/tiles/geometry'
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
import { activeTab, dropTab, tileList, viewHolder, type TileDragSource, type TileSide, type ViewContext } from '@/store/tiles'

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
  /** Where a split leaves the tile it splits. */
  stays?: Rect
  tile: DOMRect
  strip: DOMRect
  /** Where a new tab lands in the strip. */
  tabsEnd: number
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
      const others = [...node.querySelectorAll<HTMLElement>('[data-tab]')].filter((tab) => tab.dataset.tab !== source.tabId)
      const rects = others.map((tab) => tab.getBoundingClientRect())
      const slot = rects.filter((tab) => tab.left + tab.width / 2 < x).length
      const edge = slot < rects.length ? rects[slot].left : rects.at(-1)?.right ?? strip.left
      // Counted among all the tile's tabs: a strip folds those that do not fit into a menu.
      const rest = tabs.filter((tab) => tab.id !== source.tabId)
      const index = slot < others.length ? rest.findIndex((tab) => tab.id === others[slot].dataset.tab) : rest.length
      return { tileId, zone: 'center', index, rect: { left: Math.max(edge - 1, strip.left + 4), top: strip.top, width: 2, height: strip.height }, tile, strip, tabsEnd: edge }
    }
    const side = y < strip.bottom ? 'center' : dropZone(tile, x - tile.left, y - tile.top)
    const area = node.parentElement!.getBoundingClientRect()
    const split = side !== 'center' && !layout.maximized ? dropTab(layout, source, tileId, side, ctx) : null
    // Laid out as the drop will be: a seam pushed to make a floor is not a half, and a tile the drag empties gives up its room.
    const after = split && fits(split.root, area) ? tileRects(split.root, area).tiles : null
    if (!after && own) return null
    const drawn = (rect: Rect): Rect => ({
      left: area.left + (rect.left / 100) * area.width,
      top: area.top + (rect.top / 100) * area.height,
      width: (rect.width / 100) * area.width,
      height: (rect.height / 100) * area.height,
    })
    return {
      tileId,
      zone: after && side !== 'center' ? side : 'center',
      rect: after ? drawn(after[split!.focused]) : { left: tile.left, top: tile.top, width: tile.width, height: tile.height },
      stays: after ? drawn(after[tileId]) : undefined,
      tile,
      strip,
      tabsEnd: [...node.querySelectorAll('[data-tab]')].at(-1)?.getBoundingClientRect().right ?? strip.left,
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
const ghost = 'fixed flex items-center gap-1.5 px-3 text-ui font-medium'

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
  const plate = useRef<HTMLDivElement>(null)
  const point = moved ?? drag
  const target = drag && point && targetAt(drag, point.x, point.y, ctx)
  const run = useStore((s) => (s.tileDrag ? s.runs[s.tileDrag.runId] : undefined))
  const title = run ? runLabel(run) : 'Session'
  const name = drag?.view ? `${title} · ${runViewLabel[drag.view]}` : title
  const held = drag && (drag.tabId ?? (drag.view && viewHolder(useStore.getState().tiles, drag.runId, drag.view, ctx)?.tab.id))
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
    const left = Math.max(bounds.left + 4, Math.min(point.x + 12, bounds.right - node.offsetWidth - 4))
    let top = Math.max(bounds.top + 4, Math.min(point.y + 16, bounds.bottom - node.offsetHeight - 4))
    // Clear of the plate that names what lands.
    if (plate.current && target) {
      const { offsetWidth: width, offsetHeight: height } = plate.current
      const plateLeft = target.rect.left + (target.rect.width - width) / 2
      const plateTop = target.rect.top + (target.rect.height - height) / 2
      if (left < plateLeft + width && left + node.offsetWidth > plateLeft && top < plateTop + height + 8 && top + node.offsetHeight > plateTop - 8) {
        const below = plateTop + height + 8
        top = below + node.offsetHeight + 4 <= bounds.bottom ? below : plateTop - 8 - node.offsetHeight
      }
    }
    node.style.left = `${left}px`
    node.style.top = `${top}px`
  })

  if (!drag) return null
  const ViewIcon = drag.view && runViewIcon[drag.view]
  const ActionIcon = target && (target.index === undefined ? actionIcon[target.zone] : ArrowRightLeft)
  const split = target?.stays ? target : null
  const tiles = tileList(useStore.getState().tiles.root)
  const stays = split && activeTab(tiles.find((tile) => tile.id === split.tileId)!)
  const staysRun = stays ? ctx.runs[stays.runId] : undefined
  const closes = target && tileNode(tiles.find((tile) => tile.tabs.length === 1 && tile.tabs[0].id === held)?.id ?? '')?.getBoundingClientRect()
  const named = (
    <>
      {run && <ChipSession run={run} />}
      <span className="min-w-0 truncate">{title}</span>
      {ViewIcon && (
        <span className="flex shrink-0 items-center gap-1 text-muted">
          <span>·</span>
          <ViewIcon className="size-3.5" />
          {runViewLabel[drag.view!]}
        </span>
      )}
    </>
  )
  return createPortal(
    <div aria-hidden className={cn('fixed inset-0 z-[51] cursor-grabbing select-none', point && inside(drag.from, point.x, point.y) && 'pointer-events-none')}>
      {closes && <div style={{ left: closes.left, top: closes.top, width: closes.width, height: closes.height }} className="fixed bg-canvas/80" />}
      {target && (
        // A split's preview moves in the same frame as its scrims: an easing box would leave what it has not reached yet undimmed.
        <div
          key={split ? 'split' : 'whole'}
          data-slot="drop-preview"
          style={target.rect}
          className={cn('fixed border border-accent', split ? 'bg-canvas/80' : 'bg-accent-soft transition-[left,top,width,height] duration-hover ease-hover motion-reduce:transition-none')}
        >
          {split && (
            <div data-slot="drop-lands" className="absolute inset-0 flex items-center justify-center bg-accent-soft px-3">
              <div ref={plate} className={cn(ghost, 'static h-7 min-w-0 rounded-control bg-canvas px-2')}>{named}</div>
            </div>
          )}
        </div>
      )}
      {split && (
        // The tile that stays, named at the top of the room it keeps, over a scrim: what shows there now is not where it will be.
        <div style={split.stays} className="fixed bg-canvas/80">
          <div style={{ height: split.strip.height }} data-slot="drop-stays" className={cn(ghost, 'static border-b border-seam bg-chrome text-muted')}>
            {staysRun && <ChipSession run={staysRun} />}
            <span className="min-w-0 truncate">{staysRun ? runLabel(staysRun) : stays ? 'Session' : 'New tile'}</span>
          </div>
        </div>
      )}
      {target && !split && target.index === undefined && (
        // Over the strip's own controls from the last tab on, so the tab to come covers no half of a button.
        <div
          style={{ left: Math.min(target.tabsEnd, target.strip.right - 160), top: target.strip.top, right: window.innerWidth - target.strip.right, height: target.strip.height }}
          className={cn('fixed flex border-b border-seam', useStore.getState().tiles.focused === target.tileId ? 'bg-canvas' : 'bg-chrome')}
        >
          <div data-slot="drop-tab" className={cn(ghost, 'static min-w-0 border border-dashed border-accent bg-canvas')}>{named}</div>
        </div>
      )}
      <div
        ref={chip}
        data-slot="drag-chip"
        style={{ maxWidth: (target?.tile.width ?? window.innerWidth) - 8 }}
        className="fixed flex h-7 items-center gap-2 rounded-control border border-seam bg-raised px-2 text-ui font-medium shadow-overlay"
      >
        {named}
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
