import { createContext } from 'react'
import { fits, sideOf } from '@/components/tiles/geometry'
import { capability } from '@/lib/capability'
import { hrefFor } from '@/lib/url-state'
import { isRunView, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import {
  activeTab,
  pickTab,
  resolveView,
  splitTile,
  tileList,
  tilesAfter,
  visibleTiles,
  type TileLayout,
  type TileSide,
  type ViewContext,
} from '@/store/tiles'

/** A tile narrower than this draws its strip, and a header row its line, with icons for words. */
export const narrowBelow = 560

export function tileNode(tileId: string) {
  return document.querySelector<HTMLElement>(`[data-tile="${tileId}"]`)
}

/** Moves the keyboard to the first of `selector` in a tile, or to the tile itself when it has none. */
export function focusInTile(tileId: string, selector: string) {
  const tile = tileNode(tileId)
  ;(tile?.querySelector<HTMLElement>(selector) ?? tile)?.focus({ preventScroll: true })
}

/** What the tile area shares with its tiles, strips and pickers. */
export interface TileAreaValue {
  /** The layout as drawn: maximized when the stored one does not fit. */
  layout: TileLayout
  /** The stored layout does not fit the area, so there is nothing to restore to. */
  cramped: boolean
  ctx: ViewContext
  say: (words: string) => void
  /** Draws the dashed outline on a tile, or on none. */
  link: (tileId: string | null) => void
  /** The tile whose "Add a tab" picker is open. */
  picking: string | null
  /** The split that found no room and opened that picker instead. */
  noRoom: TileSide | null
  pick: (tileId: string | null) => void
  /**
   * The tile whose strip a press holds as drawn, with or without its split controls, each tab at its width, tight or not, and
   * `active` drawn as the tab on show: until the pointer is off that strip, and while a popover or menu the strip opened is open.
   */
  resting: { tileId: string; controls: boolean; active: string; tight: boolean; widths: Record<string, number> } | null
  /** Opens an empty tile beside this one, or its picker when the area has no room for one. */
  split: (tileId: string, side: 'right' | 'down') => void
}

export const TileAreaContext = createContext<TileAreaValue | null>(null)

/** The press that focused a tile is still down: the one-line Chat field opens after it, or it would move what was pressed out from under its click. */
export const TilePressedContext = createContext(false)

/**
 * Applies a layout change a tile control asked for, then points the route at
 * the focused tab. `push` adds a history entry when the change opened a tab;
 * every other change replaces the entry, so Back never steps through focus
 * clicks, moved tabs and closes.
 */
export function changeTiles(change: (layout: TileLayout, ctx: ViewContext) => TileLayout, push = false) {
  const s = useStore.getState()
  const next = tilesAfter(s, change)
  const tiles = tileList(next.tiles.root)
  const tabCount = (list: typeof tiles) => list.reduce((count, tile) => count + tile.tabs.length, 0)
  const tab = activeTab(tiles.find((tile) => tile.id === next.tiles.focused)!)
  const run = tab && s.runs[tab.runId]
  const view = run ? resolveView(tab, run, capability(s.capabilities), s.runViewMemory) : tab?.view
  const named = s.route.params.view
  const routed = run ? resolveView({ runId: run.id, view: isRunView(named) ? named : undefined }, run, capability(s.capabilities), s.runViewMemory) : named
  let route = s.route
  if (!tab) {
    // An empty tile holds the focus: the route keeps naming the session it was split from while a tile shows it.
    if (!tiles.some((tile) => activeTab(tile)?.runId === s.route.params.runId)) route = { name: 'board', params: {} }
  } else if (s.route.name !== 'run' || s.route.params.runId !== tab.runId || routed !== view) {
    route = { name: 'run', params: { runId: tab.runId, ...(view && { view }), ...(tab.repo && { repo: tab.repo }) } }
  }
  if (next.tiles === s.tiles && route === s.route) return
  // The address is replaced first, so the store's listener finds it current and pushes nothing.
  if (!(push && tabCount(tiles) > tabCount(tileList(s.tiles.root))) && route !== s.route) window.history.replaceState(window.history.state, '', hrefFor(window.location.href, route))
  useStore.setState({ ...next, route })
}

/** Opens a session in a new tile right of the focused one, or as a tab of it when the area, which fills `main`, has no room. */
export function openToSide(runId: string) {
  const main = document.getElementById('main')?.getBoundingClientRect()
  changeTiles((layout, ctx) => {
    const focused = tileList(layout.root).find((tile) => tile.id === layout.focused)!
    const split = focused.tabs.length ? splitTile(layout, layout.focused, 'right') : layout
    const base = !main?.width || fits(split.root, main) ? split : layout
    return pickTab(base, base.focused, runId, ctx)
  }, true)
}

/** The views of `runId` that another tile than `tileId` shows, with the tile and the words that say where it is. */
export function viewsElsewhere(layout: TileLayout, tileId: string, runId: string, ctx: ViewContext): Partial<Record<RunView, { tile: string; where: string }>> {
  const run = ctx.runs[runId]
  if (!run) return {}
  const visible = visibleTiles(layout)
  return Object.fromEntries(tileList(layout.root).flatMap((tile) => {
    const tab = activeTab(tile)
    if (tile.id === tileId || tab?.runId !== runId) return []
    const side = sideOf(layout.root, tileId, tile.id)
    const where = !visible.includes(tile) ? 'In a parked tile' : `On screen in the tile ${side === 'left' || side === 'right' ? `at ${side}` : side}`
    return [[resolveView(tab, run, ctx.cap, ctx.memory), { tile: tile.id, where }]]
  }))
}
