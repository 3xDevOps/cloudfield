import { capability, type Capability } from '@/lib/capability'
import { randomID } from '@/lib/utils'
import { defaultView, isRunView, offeredViews, type RunView } from '@/routes/run/views'
import type { RootState } from '@/store'
import type { RunRecord } from '@/store/runs'
import type { SliceCreator } from '@/store/slice'

export interface TileTab {
  id: string
  runId: string
  /** Absent until a view is chosen or the session loads: the tab then follows `runViewMemory`. */
  view?: RunView
  repo?: string
}

export interface Tile {
  id: string
  /** None makes this the empty picker tile. */
  tabs: TileTab[]
  /** A tab id, or '' in an empty tile. */
  active: string
}

export interface TileSplit {
  id: string
  /** `row` puts `a` left of `b`; `column` puts it above. */
  dir: 'row' | 'column'
  /** `a`'s share of the split, between 0 and 1. */
  ratio: number
  a: TileNode
  b: TileNode
}

export type TileNode = Tile | TileSplit

export interface TileLayout {
  root: TileNode
  /** Always an existing tile. */
  focused: string
  /** The focused tile fills the area and the others are parked. Never true with one tile. */
  maximized: boolean
}

export type TileSide = 'left' | 'right' | 'up' | 'down'

/** What a drag carries: a tab, or a session with the view it opens on. A sidebar row names neither. */
export interface TileDragSource {
  runId: string
  view?: RunView
  tabId?: string
}

/** What decides the view a tab shows. */
export interface ViewContext {
  runs: Record<string, RunRecord>
  cap: Capability
  memory: Record<string, RunView>
}

export function emptyTiles(): TileLayout {
  const id = randomID('tile')
  return { root: { id, tabs: [], active: '' }, focused: id, maximized: false }
}

/** The view a tab of `run` shows: its own, else the remembered one, and the default when the session does not offer that. */
export function resolveView(tab: Pick<TileTab, 'runId' | 'view'>, run: RunRecord, cap: Capability, memory: Record<string, RunView>): RunView {
  const asked = tab.view ?? memory[tab.runId]
  return asked && offeredViews(run, cap).includes(asked) ? asked : defaultView(run)
}

/** Undefined while the session is not loaded and nothing names a view. */
function shownView(tab: Pick<TileTab, 'runId' | 'view'>, ctx: ViewContext): RunView | undefined {
  const run = ctx.runs[tab.runId]
  return run ? resolveView(tab, run, ctx.cap, ctx.memory) : tab.view ?? ctx.memory[tab.runId]
}

/** Every tile in reading order: left before right, top before bottom. */
export function tileList(node: TileNode): Tile[] {
  return 'tabs' in node ? [node] : [...tileList(node.a), ...tileList(node.b)]
}

export function activeTab(tile: Tile): TileTab | undefined {
  return tile.tabs.find((tab) => tab.id === tile.active)
}

/** The tiles on screen: the focused one alone while maximized. */
export function visibleTiles(layout: TileLayout): Tile[] {
  const tiles = tileList(layout.root)
  return layout.maximized ? tiles.filter((tile) => tile.id === layout.focused) : tiles
}

/** The visible tiles showing `runId`, in reading order. */
export function tilesOf(layout: TileLayout, runId: string): Tile[] {
  return visibleTiles(layout).filter((tile) => activeTab(tile)?.runId === runId)
}

export function onScreenRuns(layout: TileLayout): string[] {
  return [...new Set(visibleTiles(layout).flatMap((tile) => activeTab(tile)?.runId ?? []))]
}

/** The tile that carries a session's controls: the focused one when it shows the session, else its first. */
export function controlsTile(layout: TileLayout, runId: string): Tile | undefined {
  const tiles = tilesOf(layout, runId)
  return tiles.find((tile) => tile.id === layout.focused) ?? tiles[0]
}

/** The tab, active or not, that holds `view` of `runId`. */
export function viewHolder(layout: TileLayout, runId: string, view: RunView, ctx: ViewContext): { tile: Tile; tab: TileTab } | undefined {
  for (const tile of tileList(layout.root)) {
    const tab = tile.tabs.find((candidate) => candidate.runId === runId && shownView(candidate, ctx) === view)
    if (tab) return { tile, tab }
  }
  return undefined
}

/** Null from `change` removes the tile, and its sibling takes the split's place. */
function mapTiles(node: TileNode, change: (tile: Tile) => TileNode | null): TileNode | null {
  if ('tabs' in node) return change(node)
  const a = mapTiles(node.a, change)
  const b = mapTiles(node.b, change)
  if (a === node.a && b === node.b) return node
  return a && b ? { ...node, a, b } : a ?? b
}

/** The layout around a changed tree: a focus that left with its tile moves to the nearest tile in reading order. */
function settled(before: TileLayout, root: TileNode | null, focused = before.focused, maximized = before.maximized): TileLayout {
  if (!root) return emptyTiles()
  const tiles = tileList(root)
  const kept = (tile: Tile) => tiles.some((other) => other.id === tile.id)
  const order = tileList(before.root)
  const at = order.findIndex((tile) => tile.id === focused)
  const next = tiles.find((tile) => tile.id === focused) ?? [...order.slice(at + 1), ...order.slice(0, at).reverse()].find(kept) ?? tiles[0]
  return { root, focused: next.id, maximized: maximized && tiles.length > 1 }
}

function inTile(layout: TileLayout, tileId: string, change: (tile: Tile) => TileNode | null): TileNode | null {
  return mapTiles(layout.root, (tile) => (tile.id === tileId ? change(tile) : tile))
}

/** `tile` without the tabs `gone` names; the tab after a closed active tab takes over, else the one before. */
function withoutTabs(tile: Tile, gone: (tab: TileTab) => boolean): Tile | null {
  const tabs = tile.tabs.filter((tab) => !gone(tab))
  if (tabs.length === tile.tabs.length) return tile
  if (!tabs.length) return null
  const at = tile.tabs.findIndex((tab) => tab.id === tile.active)
  const active = tile.tabs.slice(at).find((tab) => !gone(tab)) ?? tabs[tabs.length - 1]
  return { ...tile, tabs, active: active.id }
}

function closeTabs(layout: TileLayout, gone: (tab: TileTab) => boolean): TileLayout {
  return settled(layout, mapTiles(layout.root, (tile) => withoutTabs(tile, gone)))
}

/** Focusing a parked tile makes it the maximized one. */
export function focusTile(layout: TileLayout, tileId: string): TileLayout {
  return settled(layout, layout.root, tileId)
}

/** Null restores the parked tiles. */
export function maximizeTile(layout: TileLayout, tileId: string | null): TileLayout {
  return settled(layout, layout.root, tileId ?? layout.focused, tileId !== null)
}

export function activateTab(layout: TileLayout, tabId: string): TileLayout {
  const tile = tileList(layout.root).find((candidate) => candidate.tabs.some((tab) => tab.id === tabId))
  if (!tile) return layout
  return settled(layout, inTile(layout, tile.id, () => ({ ...tile, active: tabId })), tile.id)
}

export function closeTab(layout: TileLayout, tabId: string): TileLayout {
  return closeTabs(layout, (tab) => tab.id === tabId)
}

/** For the empty picker tile, which has no tab to close. */
export function closeTile(layout: TileLayout, tileId: string): TileLayout {
  return settled(layout, inTile(layout, tileId, () => null))
}

export function closeRun(layout: TileLayout, runId: string): TileLayout {
  return closeTabs(layout, (tab) => tab.runId === runId)
}

export function splitTile(layout: TileLayout, tileId: string, side: TileSide): TileLayout {
  const fresh: Tile = { id: randomID('tile'), tabs: [], active: '' }
  const first = side === 'left' || side === 'up'
  const root = inTile(layout, tileId, (tile) => ({
    id: randomID('split'),
    dir: side === 'left' || side === 'right' ? 'row' : 'column',
    ratio: 0.5,
    a: first ? fresh : tile,
    b: first ? tile : fresh,
  }))
  return settled(layout, root, fresh.id, false)
}

export function setRatio(layout: TileLayout, splitId: string, ratio: number): TileLayout {
  const withRatio = (node: TileNode): TileNode =>
    'tabs' in node ? node : node.id === splitId ? { ...node, ratio } : { ...node, a: withRatio(node.a), b: withRatio(node.b) }
  return { ...layout, root: withRatio(layout.root) }
}

/** Moves a tab to `tileId`, active and focused there. `index` counts the target's other tabs; omitted, the tab goes last. */
export function moveTab(layout: TileLayout, tabId: string, tileId: string, index?: number): TileLayout {
  const moved = tileList(layout.root).flatMap((tile) => tile.tabs).find((tab) => tab.id === tabId)
  if (!moved) return layout
  const root = mapTiles(layout.root, (tile) => {
    if (tile.id !== tileId) return withoutTabs(tile, (tab) => tab.id === tabId)
    const rest = tile.tabs.filter((tab) => tab.id !== tabId)
    const at = index ?? rest.length
    return { ...tile, tabs: [...rest.slice(0, at), moved, ...rest.slice(at)], active: tabId }
  })
  return settled(layout, root, tileId)
}

/**
 * Lands what a drag carries in `tileId`: in a new tile on `zone`'s side of it,
 * or as a tab of it at `index`. A view that a tab already holds moves that tab.
 */
export function dropTab(layout: TileLayout, source: TileDragSource, tileId: string, zone: TileSide | 'center', ctx: ViewContext, index?: number): TileLayout {
  const held = source.tabId ?? (source.view && viewHolder(layout, source.runId, source.view, ctx)?.tab.id)
  const split = zone === 'center' ? layout : splitTile(layout, tileId, zone)
  const target = zone === 'center' ? tileId : split.focused
  return held ? moveTab(split, held, target, index) : pickTab(split, target, source.runId, ctx, source.view)
}

function addTab(layout: TileLayout, tileId: string, tab: TileTab, replace: boolean): TileLayout {
  const root = inTile(layout, tileId, (tile) => {
    const at = tile.tabs.findIndex((other) => other.id === tile.active)
    return { ...tile, tabs: [...tile.tabs.slice(0, replace ? at : at + 1), tab, ...tile.tabs.slice(at + 1)], active: tab.id }
  })
  return settled(layout, root, tileId)
}

/**
 * Opens `runId` as a new tab of `tileId`. A view another tab already holds is
 * shown where it is. With no view named, a session already in the layout
 * takes its first view not in it, and its own tile is focused when it has none
 * left.
 */
export function openTab(layout: TileLayout, tileId: string, runId: string, ctx: ViewContext, view?: RunView): TileLayout {
  const tabs = tileList(layout.root).flatMap((tile) => tile.tabs).filter((tab) => tab.runId === runId)
  const run = ctx.runs[runId]
  const taken = tabs.map((tab) => shownView(tab, ctx))
  const free = view ?? (tabs.length ? (run ? offeredViews(run, ctx.cap) : []).find((offered) => !taken.includes(offered)) : undefined)
  if (tabs.length && !free) {
    const tile = controlsTile(layout, runId)
    return tile ? focusTile(layout, tile.id) : activateTab(layout, tabs[0].id)
  }
  const holder = free && viewHolder(layout, runId, free, ctx)
  if (holder) return activateTab(layout, holder.tab.id)
  return addTab(layout, tileId, { id: randomID('tab'), runId, ...(free && { view: free }) }, false)
}

/** `openTab` from a picker: an empty tile the session did not land in closes. */
export function pickTab(layout: TileLayout, tileId: string, runId: string, ctx: ViewContext, view?: RunView): TileLayout {
  const opened = openTab(layout, tileId, runId, ctx, view)
  const tile = tileList(opened.root).find((candidate) => candidate.id === tileId)
  return tile && !tile.tabs.length ? closeTile(opened, tileId) : opened
}

/** Whether the focused tile shows the session and view `params` name, comparing what each view resolves to. */
export function showsRoute(layout: TileLayout, params: Record<string, string>, ctx: ViewContext): boolean {
  const tab = tileList(layout.root).flatMap((tile) => (tile.id === layout.focused ? activeTab(tile) ?? [] : []))[0]
  if (tab?.runId !== params.runId) return false
  return !isRunView(params.view) || shownView({ runId: params.runId, view: params.view }, ctx) === shownView(tab, ctx)
}

/**
 * Where a navigation to a session lands, first match wins:
 * 1. a tab already holds the session on the named view: that tab;
 * 2. the focused tile shows the session: in place;
 * 3. another tile shows it: that tile;
 * 4. it is a background tab: the first one;
 * 5. else it replaces the focused tile's active tab, or fills an empty tile.
 * The tab takes the named view and the route's repo, and its tile the focus.
 * Rule 1 compares what the named view resolves to, so a view the session does
 * not offer lands on the tab that already shows its fallback.
 */
export function routeTiles(layout: TileLayout, params: Record<string, string>, ctx: ViewContext): TileLayout {
  const view = isRunView(params.view) ? params.view : undefined
  const shown = view && shownView({ runId: params.runId, view }, ctx)
  const placed = ({ id, runId, view: own }: TileTab): TileTab =>
    ({ id, runId, ...((view ?? own) && { view: view ?? own }), ...(params.repo && { repo: params.repo }) })
  const held = tileList(layout.root).flatMap((tile) => tile.tabs.filter((tab) => tab.runId === params.runId).map((tab) => ({ tile, tab })))
  const active = held.filter(({ tile, tab }) => tile.active === tab.id)
  const target =
    (shown && held.find(({ tab }) => shownView(tab, ctx) === shown)) ||
    active.find(({ tile }) => tile.id === layout.focused) ||
    active[0] ||
    held[0]
  if (!target) return addTab(layout, layout.focused, placed({ id: randomID('tab'), runId: params.runId }), true)
  const root = inTile(layout, target.tile.id, (tile) => ({
    ...tile,
    active: target.tab.id,
    tabs: tile.tabs.map((tab) => (tab === target.tab ? placed(tab) : tab)),
  }))
  return settled(layout, root, target.tile.id)
}

/**
 * Writes each loaded session's resolved view onto its tabs and closes a later
 * tab that repeats an earlier one. `prune` also closes the tabs of sessions
 * the store does not hold, so it is only for a store that knows every session.
 */
export function settleTiles(layout: TileLayout, ctx: ViewContext, prune = false): TileLayout {
  const seen = new Set<string>()
  const gone = new Set<string>()
  const root = mapTiles(layout.root, (tile) => {
    const tabs = tile.tabs.map((tab) => {
      const run = ctx.runs[tab.runId]
      if (!run) {
        if (prune) gone.add(tab.id)
        return tab
      }
      const view = resolveView(tab, run, ctx.cap, ctx.memory)
      const key = JSON.stringify([tab.runId, view])
      if (seen.has(key)) gone.add(tab.id)
      seen.add(key)
      return tab.view === view ? tab : { ...tab, view }
    })
    return tabs.every((tab, index) => tab === tile.tabs[index]) ? tile : { ...tile, tabs }
  })!
  return closeTabs({ ...layout, root }, (tab) => gone.has(tab.id))
}

function normalNode(value: unknown, ids: Set<string>): TileNode | null {
  if (!value || typeof value !== 'object') return null
  const { id, tabs, active, dir, ratio, a, b } = value as Record<string, unknown>
  if (typeof id !== 'string' || ids.has(id)) return null
  ids.add(id)
  if (Array.isArray(tabs)) {
    const kept: TileTab[] = []
    for (const tab of tabs as unknown[]) {
      if (!tab || typeof tab !== 'object') return null
      const { id: tabId, runId, view, repo } = tab as Record<string, unknown>
      if (typeof tabId !== 'string' || ids.has(tabId) || typeof runId !== 'string' || !runId) return null
      ids.add(tabId)
      kept.push({ id: tabId, runId, ...(typeof view === 'string' && isRunView(view) && { view }), ...(typeof repo === 'string' && repo && { repo }) })
    }
    return { id, tabs: kept, active: kept.find((tab) => tab.id === active)?.id ?? kept[0]?.id ?? '' }
  }
  const first = normalNode(a, ids)
  const second = normalNode(b, ids)
  if ((dir !== 'row' && dir !== 'column') || typeof ratio !== 'number' || !(ratio > 0 && ratio < 1) || !first || !second) return null
  return { id, dir, ratio, a: first, b: second }
}

/** A stored layout made safe to render: a malformed tree becomes the empty layout. */
export function normalizeTiles(value: unknown): TileLayout {
  const { root, focused, maximized } = (value ?? {}) as Record<string, unknown>
  const node = normalNode(root, new Set())
  if (!node) return emptyTiles()
  const tiles = tileList(node)
  return {
    root: node,
    focused: tiles.find((tile) => tile.id === focused)?.id ?? tiles[0].id,
    maximized: maximized === true && tiles.length > 1,
  }
}

export function normalizeSavedTiles(value: unknown): Record<string, TileLayout> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value).map(([who, layout]) => [who, normalizeTiles(layout)]))
}

export interface TilesSlice {
  /** The signed-in member's session tiles. */
  tiles: TileLayout
  /** The tile area is too small for `tiles`, so it draws the focused tile alone. */
  tilesCramped: boolean
  /** What is being dragged to a tile, where the pointer was when it lifted, and the rectangle of what it lifted from. */
  tileDrag: (TileDragSource & { x: number; y: number; from: DOMRect }) | null
  /** Each member's `tiles` on this browser, by identity key. */
  savedTiles: Record<string, TileLayout>
  /** For a store that just read every session from the server: closes the tabs of sessions that are gone. */
  pruneTiles: () => void
}

export function viewContext(s: RootState): ViewContext {
  return { runs: s.runs, cap: capability(s.capabilities), memory: s.runViewMemory }
}

/**
 * `tiles` after `change`, saved under the signed-in member. Every layout
 * change goes through here: once the store holds live sessions, the layout is
 * settled first, so a view that stopped being offered cannot make two tabs
 * show the same thing.
 */
export function tilesAfter(s: RootState, change: (layout: TileLayout, ctx: ViewContext) => TileLayout): Pick<TilesSlice, 'tiles' | 'savedTiles'> {
  const ctx = viewContext(s)
  const tiles = change(s.hydrated && s.staleSince === null ? settleTiles(s.tiles, ctx) : s.tiles, ctx)
  return { tiles, savedTiles: s.identityKey ? { ...s.savedTiles, [s.identityKey]: tiles } : s.savedTiles }
}

export const createTilesSlice: SliceCreator<TilesSlice> = (set) => ({
  tiles: emptyTiles(),
  tilesCramped: false,
  tileDrag: null,
  savedTiles: {},
  pruneTiles: () => set((s) => tilesAfter(s, (layout, ctx) => settleTiles(layout, ctx, true))),
})
