import { describe, expect, it } from 'vitest'
import { capability } from '@/lib/capability'
import { offeredViews, type RunView } from '@/routes/run/views'
import { createRootStore } from '@/store'
import { hydrate } from '@/store/sync'
import {
  activateTab,
  closeRun,
  closeTab,
  controlsTile,
  emptyTiles,
  focusTile,
  maximizeTile,
  moveTab,
  normalizeTiles,
  onScreenRuns,
  openTab,
  resolveView,
  routeTiles,
  setRatio,
  settleTiles,
  splitTile,
  tileList,
  tilesAfter,
  tilesOf,
  viewHolder,
  visibleTiles,
  type Tile,
  type TileLayout,
  type TileNode,
  type TileSplit,
  type TileTab,
  type ViewContext,
  viewContext,
} from '@/store/tiles'
import { fakeApi, run, runRecords } from '@/test/fixtures'

const tab = (id: string, runId: string, view?: RunView): TileTab => ({ id, runId, ...(view && { view }) })
const tile = (id: string, ...tabs: TileTab[]): Tile => ({ id, tabs, active: tabs[0]?.id ?? '' })
const split = (id: string, dir: TileSplit['dir'], a: TileNode, b: TileNode): TileSplit => ({ id, dir, ratio: 0.5, a, b })
const layout = (root: TileNode, focused: string, maximized = false): TileLayout => ({ root, focused, maximized })

const everything = capability({ gateway: 'local', methods: ['*'], ws: [] })
const enhanced = run({ id: 'run_1', mode: 'acp', acp: true })
const standard = run({ id: 'run_2' })
const context = (memory: Record<string, RunView> = {}): ViewContext => ({ runs: runRecords(enhanced, standard), cap: everything, memory })

/** The layout as tile ids with their tabs, the active one starred. */
function drawn(l: TileLayout): string[] {
  return tileList(l.root).map((t) => `${t.id}: ${t.tabs.map((x) => `${x.id}${x.id === t.active ? '*' : ''}`).join(' ')}`)
}

const two = () => layout(split('s', 'row', tile('left', tab('a', 'run_1', 'session'), tab('b', 'run_2')), tile('right', tab('c', 'run_1', 'changes'))), 'left')

describe('the view a tab shows', () => {
  it('offers Trace only to an Enhanced session, and Files and Browser only where they can work', () => {
    expect(offeredViews(runRecords(enhanced).run_1, everything)).toEqual(['session', 'trace', 'terminal', 'changes', 'files', 'browser'])
    expect(offeredViews(runRecords(standard).run_2, everything)).toEqual(['session', 'terminal', 'changes', 'files', 'browser'])
    expect(offeredViews(runRecords(run({ status: 'merged' })).run_1, everything)).toEqual(['session', 'terminal', 'changes'])
    expect(offeredViews(runRecords(standard).run_2, capability(null))).toEqual(['session', 'terminal', 'changes'])
  })

  it('is its own view, else the remembered one, else the default', () => {
    const record = runRecords(standard).run_2
    expect(resolveView({ runId: 'run_2', view: 'changes' }, record, everything, { run_2: 'files' })).toBe('changes')
    expect(resolveView({ runId: 'run_2' }, record, everything, { run_2: 'files' })).toBe('files')
    expect(resolveView({ runId: 'run_2' }, record, everything, {})).toBe('terminal')
    expect(resolveView({ runId: 'run_1' }, runRecords(enhanced).run_1, everything, {})).toBe('session')
  })

  it('falls back to the default when the session no longer offers the stored view', () => {
    const ended = runRecords(run({ id: 'run_2', status: 'merged' })).run_2
    expect(resolveView({ runId: 'run_2', view: 'browser' }, ended, everything, {})).toBe('terminal')
    expect(resolveView({ runId: 'run_2', view: 'trace' }, runRecords(standard).run_2, everything, {})).toBe('terminal')
  })
})

describe('tile operations', () => {
  it('splits a tile on each side into a new, focused, empty tile', () => {
    const start = layout(tile('one', tab('a', 'run_1')), 'one')
    for (const [side, dir, first] of [['right', 'row', false], ['down', 'column', false], ['left', 'row', true], ['up', 'column', true]] as const) {
      const next = splitTile(start, 'one', side)
      const root = next.root as TileSplit
      const fresh = first ? root.a : root.b
      expect(root).toMatchObject({ dir, ratio: 0.5 })
      expect(first ? root.b : root.a).toBe(start.root)
      expect(fresh).toMatchObject({ tabs: [], active: '' })
      expect(next.focused).toBe(fresh.id)
    }
  })

  it('restores the parked tiles when the maximized tile is split', () => {
    const next = splitTile(maximizeTile(two(), 'left'), 'left', 'down')
    expect(next.maximized).toBe(false)
    expect(tileList(next.root)).toHaveLength(3)
  })

  it('activates a tab and focuses its tile', () => {
    const next = activateTab(layout(two().root, 'right'), 'b')
    expect(drawn(next)).toEqual(['left: a b*', 'right: c*'])
    expect(next.focused).toBe('left')
  })

  it('closes a tab and activates the next one, or the one before the last', () => {
    const three = layout(tile('one', tab('a', 'run_1'), tab('b', 'run_2'), tab('c', 'run_3')), 'one')
    expect(drawn(closeTab(three, 'a'))).toEqual(['one: b* c'])
    expect(drawn(closeTab(activateTab(three, 'c'), 'c'))).toEqual(['one: a b*'])
    expect(drawn(closeTab(three, 'b'))).toEqual(['one: a* c'])
  })

  it('removes a tile with its last tab, and the neighbour takes the space and the focus', () => {
    const start = layout(two().root, 'right')
    const next = closeTab(start, 'c')
    expect(next.root).toBe((start.root as TileSplit).a)
    expect(drawn(next)).toEqual(['left: a* b'])
    expect(next.focused).toBe('left')
  })

  it('moves focus to the nearest tile in reading order when the focused one closes', () => {
    const three = (focused: string) =>
      layout(split('s', 'row', tile('one', tab('a', 'run_1')), split('t', 'column', tile('two', tab('b', 'run_2')), tile('three', tab('c', 'run_3')))), focused)
    expect(closeTab(three('one'), 'a').focused).toBe('two')
    expect(closeTab(three('two'), 'b').focused).toBe('three')
    expect(closeTab(three('three'), 'c').focused).toBe('two')
    expect(closeTab(three('one'), 'c').focused).toBe('one')
  })

  it('leaves one empty tile when the last tab closes', () => {
    const next = closeTab(layout(tile('one', tab('a', 'run_1')), 'one'), 'a')
    expect(next.root).toMatchObject({ tabs: [], active: '' })
    expect(next.focused).toBe(next.root.id)
  })

  it('closes every tab of a session', () => {
    expect(drawn(closeRun(layout(two().root, 'right'), 'run_1'))).toEqual(['left: b*'])
  })

  it('moves a tab to another tile, to a position in its own strip, and out of a tile that then closes', () => {
    expect(drawn(moveTab(two(), 'b', 'right'))).toEqual(['left: a*', 'right: c b*'])
    expect(drawn(moveTab(two(), 'b', 'right', 0))).toEqual(['left: a*', 'right: b* c'])
    expect(drawn(moveTab(two(), 'b', 'left', 0))).toEqual(['left: b* a', 'right: c*'])
    const merged = moveTab(two(), 'c', 'left')
    expect(drawn(merged)).toEqual(['left: a b c*'])
    expect(merged.focused).toBe('left')
  })

  it('focuses a tile, and a parked one becomes the maximized tile', () => {
    expect(focusTile(two(), 'right')).toMatchObject({ focused: 'right', maximized: false })
    const parked = focusTile(maximizeTile(two(), 'left'), 'right')
    expect(parked).toMatchObject({ focused: 'right', maximized: true })
    expect(visibleTiles(parked).map((t) => t.id)).toEqual(['right'])
  })

  it('maximizes a tile, restores, and never maximizes a lone tile', () => {
    const max = maximizeTile(two(), 'right')
    expect(max).toMatchObject({ focused: 'right', maximized: true })
    expect(maximizeTile(max, null)).toMatchObject({ focused: 'right', maximized: false })
    expect(closeTab(max, 'c').maximized).toBe(false)
    expect(maximizeTile(layout(tile('one'), 'one'), 'one').maximized).toBe(false)
  })

  it('sets the ratio of one split', () => {
    const nested = layout(split('s', 'row', tile('one'), split('t', 'column', tile('two'), tile('three'))), 'one')
    const root = setRatio(nested, 't', 0.3).root as TileSplit
    expect(root.ratio).toBe(0.5)
    expect((root.b as TileSplit).ratio).toBe(0.3)
  })
})

describe('opening a session in a tile', () => {
  it('adds a session new to the layout after the active tab, on no view of its own', () => {
    const next = openTab(two(), 'right', 'run_3', context())
    expect(drawn(next)[1]).toMatch(/^right: c \S+\*$/)
    expect(next.focused).toBe('right')
    expect(tileList(next.root)[1].tabs[1]).not.toHaveProperty('view')
  })

  it('gives a session already in the layout its first view not in it', () => {
    const next = openTab(two(), 'right', 'run_1', context())
    expect(tileList(next.root)[1].tabs.map((x) => x.view)).toEqual(['changes', 'trace'])
    const standardTwice = openTab(two(), 'right', 'run_2', context())
    expect(tileList(standardTwice.root)[1].tabs[1]).toMatchObject({ runId: 'run_2', view: 'session' })
  })

  it('focuses the session where it is when every view is taken', () => {
    const ended = { ...context(), runs: runRecords(run({ id: 'run_1', status: 'merged' })) }
    const full = layout(split('s', 'row', tile('left', tab('a', 'run_1', 'terminal'), tab('b', 'run_1', 'session')), tile('right', tab('c', 'run_1', 'changes'), tab('d', 'run_2'))), 'right')
    const next = openTab(activateTab(full, 'd'), 'right', 'run_1', ended)
    expect(drawn(next)).toEqual(['left: a* b', 'right: c d*'])
    expect(next.focused).toBe('left')
  })

  it('shows a named view where another tab already holds it', () => {
    const next = openTab(two(), 'left', 'run_1', context(), 'changes')
    expect(drawn(next)).toEqual(['left: a* b', 'right: c*'])
    expect(next.focused).toBe('right')
    expect(tileList(openTab(two(), 'left', 'run_1', context(), 'files').root)[0].tabs[1]).toMatchObject({ runId: 'run_1', view: 'files' })
  })

  it('fills an empty tile', () => {
    const empty = splitTile(two(), 'right', 'down')
    const next = openTab(empty, empty.focused, 'run_3', context())
    expect(tileList(next.root).map((t) => t.tabs.length)).toEqual([2, 1, 1])
  })
})

describe('selectors', () => {
  const same = layout(
    split('s', 'row', tile('left', tab('a', 'run_1', 'session'), tab('b', 'run_2')), split('t', 'column', tile('top', tab('c', 'run_1', 'changes')), tile('bottom', tab('d', 'run_3')))),
    'top',
  )

  it('lists the sessions on screen once each, and the tiles of one', () => {
    expect(onScreenRuns(same)).toEqual(['run_1', 'run_3'])
    expect(tilesOf(same, 'run_1').map((t) => t.id)).toEqual(['left', 'top'])
    expect(tilesOf(same, 'run_2')).toEqual([])
    expect(onScreenRuns(maximizeTile(same, 'bottom'))).toEqual(['run_3'])
  })

  it('puts the session controls on its focused tile, else its first', () => {
    expect(controlsTile(same, 'run_1')?.id).toBe('top')
    expect(controlsTile(focusTile(same, 'bottom'), 'run_1')?.id).toBe('left')
    expect(controlsTile(same, 'run_2')).toBeUndefined()
  })

  it('finds the tab that holds a view by what it shows, background tabs included', () => {
    expect(viewHolder(same, 'run_1', 'changes', context())?.tile.id).toBe('top')
    expect(viewHolder(same, 'run_2', 'terminal', context())?.tab.id).toBe('b')
    expect(viewHolder(same, 'run_2', 'files', context({ run_2: 'files' }))?.tab.id).toBe('b')
    expect(viewHolder(same, 'run_1', 'files', context())).toBeUndefined()
  })
})

describe('navigating to a session', () => {
  const go = (l: TileLayout, params: Record<string, string>, ctx = context()) => routeTiles(l, params, ctx)

  it('rule 1: shows the tab that already holds the named view, wherever it is', () => {
    const background = layout(split('s', 'row', tile('left', tab('a', 'run_2'), tab('b', 'run_1', 'changes')), tile('right', tab('c', 'run_1', 'session'))), 'right')
    const next = go(background, { runId: 'run_1', view: 'changes' })
    expect(drawn(next)).toEqual(['left: a b*', 'right: c*'])
    expect(next.focused).toBe('left')
  })

  it('rule 1 reads the view a tab resolves to, not only the stored one', () => {
    const next = go(layout(split('s', 'row', tile('left', tab('a', 'run_2')), tile('right', tab('c', 'run_1'))), 'right'), { runId: 'run_2', view: 'terminal' })
    expect(next.focused).toBe('left')
    expect(drawn(next)).toEqual(['left: a*', 'right: c*'])
  })

  it('rule 2: changes the focused tile in place, and keeps the route repo on the tab', () => {
    const next = go(two(), { runId: 'run_1', view: 'files' })
    expect(tileList(next.root)[0].tabs[0]).toEqual({ id: 'a', runId: 'run_1', view: 'files' })
    expect(next.focused).toBe('left')
    const changes = go(layout(tile('one', tab('a', 'run_1', 'changes')), 'one'), { runId: 'run_1', view: 'changes', repo: 'wsp_2' })
    expect(tileList(changes.root)[0].tabs[0]).toEqual({ id: 'a', runId: 'run_1', view: 'changes', repo: 'wsp_2' })
    expect(tileList(go(changes, { runId: 'run_1', view: 'changes' }).root)[0].tabs[0]).toEqual({ id: 'a', runId: 'run_1', view: 'changes' })
  })

  it('rule 2 keeps the view when none is named', () => {
    expect(go(two(), { runId: 'run_1' })).toEqual(two())
  })

  it('rule 3: focuses another tile that shows the session', () => {
    const next = go(layout(split('s', 'row', tile('left', tab('b', 'run_2')), tile('right', tab('c', 'run_1', 'changes'))), 'left'), { runId: 'run_1' })
    expect(next.focused).toBe('right')
    expect(tileList(next.root)[1].tabs[0].view).toBe('changes')
    const viewed = go(layout(split('s', 'row', tile('left', tab('b', 'run_2')), tile('right', tab('c', 'run_1', 'changes'))), 'left'), { runId: 'run_1', view: 'files' })
    expect(tileList(viewed.root)[1].tabs[0].view).toBe('files')
  })

  it('rule 4: activates the first background tab of the session', () => {
    const next = go(layout(split('s', 'row', tile('left', tab('a', 'run_3')), tile('right', tab('c', 'run_3'), tab('b', 'run_2', 'changes'))), 'left'), { runId: 'run_2', view: 'files' })
    expect(drawn(next)).toEqual(['left: a*', 'right: c b*'])
    expect(next.focused).toBe('right')
    expect(tileList(next.root)[1].tabs[1].view).toBe('files')
  })

  it('rule 5: replaces the active tab of the focused tile, or fills an empty one', () => {
    const next = go(two(), { runId: 'run_3', view: 'changes', repo: 'all' })
    const [left, right] = tileList(next.root)
    expect(left.tabs.map((x) => x.runId)).toEqual(['run_3', 'run_2'])
    expect(left.tabs[0]).toMatchObject({ view: 'changes', repo: 'all' })
    expect(left.active).toBe(left.tabs[0].id)
    expect(right.tabs).toEqual([tab('c', 'run_1', 'changes')])

    const filled = go(emptyTiles(), { runId: 'run_3' })
    expect(tileList(filled.root)[0].tabs).toEqual([{ id: tileList(filled.root)[0].active, runId: 'run_3' }])
  })

  it('moves maximize to a parked tile the navigation lands in', () => {
    const next = go(maximizeTile(two(), 'left'), { runId: 'run_1', view: 'changes' })
    expect(next).toMatchObject({ focused: 'right', maximized: true })
  })

  it('lands a view the session no longer offers on the tab that shows its fallback', () => {
    const ended: ViewContext = { ...context(), runs: runRecords(run({ id: 'run_1', status: 'merged' })) }
    const start = layout(split('s', 'row', tile('left', tab('a', 'run_1', 'terminal')), tile('right', tab('c', 'run_1', 'changes'))), 'right')
    const next = go(start, { runId: 'run_1', view: 'browser' }, ended)
    expect(next.focused).toBe('left')
    expect(settleTiles(next, ended)).toEqual({ ...start, focused: 'left' })
  })

  it('ignores a view name that is not a view', () => {
    expect(go(two(), { runId: 'run_1', view: 'nope' })).toEqual(two())
  })
})

describe('settling a layout', () => {
  it('writes resolved views onto the tabs of loaded sessions', () => {
    const next = settleTiles(layout(tile('one', tab('a', 'run_1'), tab('b', 'run_2', 'trace'), tab('c', 'run_9')), 'one'), context({ run_1: 'changes' }))
    expect(tileList(next.root)[0].tabs).toEqual([tab('a', 'run_1', 'changes'), tab('b', 'run_2', 'terminal'), tab('c', 'run_9')])
  })

  it('closes a later tab that shows what an earlier one does', () => {
    const next = settleTiles(layout(split('s', 'row', tile('left', tab('a', 'run_2', 'terminal')), tile('right', tab('b', 'run_2', 'trace'))), 'right'), context())
    expect(drawn(next)).toEqual(['left: a*'])
    expect(next.focused).toBe('left')
  })

  it('returns the same tiles when nothing changes', () => {
    const start = layout(two().root, 'left')
    const pinned = settleTiles(start, context({ run_2: 'terminal' }))
    expect(settleTiles(pinned, context()).root).toBe(pinned.root)
  })

  it('closes the tabs of unknown sessions only when asked to prune', () => {
    const start = layout(split('s', 'row', tile('left', tab('a', 'run_9')), tile('right', tab('b', 'run_1'), tab('c', 'run_8'))), 'left')
    expect(drawn(settleTiles(start, context()))).toEqual(['left: a*', 'right: b* c'])
    const pruned = settleTiles(start, context(), true)
    expect(drawn(pruned)).toEqual(['right: b*'])
    expect(pruned.focused).toBe('right')
  })
})

describe('a stored layout', () => {
  it('is kept when well formed, with unknown fields dropped', () => {
    const stored = { ...two(), extra: 1, root: { ...two().root, extra: 1 } }
    expect(normalizeTiles(JSON.parse(JSON.stringify(stored)))).toEqual(two())
  })

  it('has its focus, maximize, active tab and views repaired', () => {
    const repaired = normalizeTiles({
      root: { id: 'one', tabs: [{ id: 'a', runId: 'run_1', view: 'nope', repo: 7 }, { id: 'b', runId: 'run_2', view: 'files' }], active: 'gone' },
      focused: 'gone',
      maximized: true,
    })
    expect(repaired).toEqual(layout({ id: 'one', tabs: [tab('a', 'run_1'), tab('b', 'run_2', 'files')], active: 'a' }, 'one'))
  })

  it.each([
    ['nothing', undefined],
    ['a string', 'tiles'],
    ['no root', { focused: 'one' }],
    ['a tile without tabs', { root: { id: 'one' }, focused: 'one' }],
    ['a tab without a session', { root: { id: 'one', tabs: [{ id: 'a' }], active: 'a' }, focused: 'one' }],
    ['a tab that is not an object', { root: { id: 'one', tabs: ['a'], active: 'a' }, focused: 'one' }],
    ['a split missing a side', { root: { id: 's', dir: 'row', ratio: 0.5, a: { id: 'one', tabs: [], active: '' } }, focused: 'one' }],
    ['a ratio outside the split', { root: { ...two().root, ratio: 1 }, focused: 'left' }],
    ['an unknown direction', { root: { ...two().root, dir: 'diagonal' }, focused: 'left' }],
    ['a repeated id', { root: { ...two().root, b: { id: 'left', tabs: [], active: '' } }, focused: 'left' }],
  ])('becomes the empty layout from %s', (_, stored) => {
    const empty = normalizeTiles(stored)
    expect(empty.root).toMatchObject({ tabs: [], active: '' })
    expect(empty).toMatchObject({ focused: empty.root.id, maximized: false })
  })
})

describe('the tiles in the store', () => {
  const alice = 'origin\u0000\u0000mem_alice'
  const bob = 'origin\u0000\u0000mem_bob'
  const tabs = (s: { tiles: TileLayout }) => tileList(s.tiles.root).map((t) => t.tabs.map((x) => `${x.runId}${x.view ? `:${x.view}` : ''}`).join(' '))

  it('follows navigate in the same change as the route', () => {
    const store = createRootStore()
    const seen: string[] = []
    store.subscribe((s) => seen.push(`${s.route.params.runId} ${tabs(s).join('|')}`))
    store.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    expect(seen).toEqual(['run_1 run_1:changes'])
    store.getState().navigate('board')
    expect(tabs(store.getState())).toEqual(['run_1:changes'])
  })

  it('writes no saved layout before sign-in', () => {
    const store = createRootStore()
    store.getState().navigate('run', { runId: 'run_1' })
    expect(tabs(store.getState())).toEqual(['run_1'])
    expect(store.getState().savedTiles).toEqual({})
  })

  it('keeps each member their own layout across a reload', () => {
    const store = createRootStore()
    store.getState().setIdentityKey(alice)
    store.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    store.setState((s) => ({ tiles: splitTile(s.tiles, s.tiles.focused, 'right') }))
    store.getState().navigate('run', { runId: 'run_2' })
    store.getState().navigate('board')
    const saved = store.getState().tiles
    expect(tabs(store.getState())).toEqual(['run_1:changes', 'run_2'])

    const reloaded = createRootStore()
    expect(tabs(reloaded.getState())).toEqual([''])
    reloaded.getState().setIdentityKey(alice)
    expect(reloaded.getState().tiles).toEqual(saved)
    reloaded.getState().setIdentityKey(bob)
    expect(tabs(reloaded.getState())).toEqual([''])
    reloaded.getState().setIdentityKey(alice)
    expect(reloaded.getState().tiles).toEqual(saved)
  })

  it('replaces the layout on sign-in, then lands the current route in it', () => {
    const store = createRootStore()
    store.getState().setIdentityKey(alice)
    store.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    store.setState((s) => ({ tiles: splitTile(s.tiles, s.tiles.focused, 'right') }))
    store.getState().navigate('run', { runId: 'run_2' })
    store.getState().navigate('board')

    const linked = createRootStore()
    linked.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    linked.getState().setIdentityKey(alice)
    expect(tabs(linked.getState())).toEqual(['run_1:changes', 'run_2'])
    expect(tileList(linked.getState().tiles.root)[0].id).toBe(linked.getState().tiles.focused)
    expect(linked.getState().savedTiles[alice]).toBe(linked.getState().tiles)

    const fresh = createRootStore()
    fresh.getState().navigate('run', { runId: 'run_3' })
    fresh.getState().setIdentityKey(alice)
    expect(tabs(fresh.getState())).toEqual(['run_3', 'run_2'])
  })

  it('loads a stored payload without tiles, and makes a malformed one empty', () => {
    window.localStorage.setItem('aether.ui', JSON.stringify({ state: { theme: 'light' }, version: 12 }))
    expect(createRootStore().getState().savedTiles).toEqual({})
    window.localStorage.setItem('aether.ui', JSON.stringify({ state: { savedTiles: { [alice]: { root: 3 } } }, version: 12 }))
    const store = createRootStore()
    store.getState().setIdentityKey(alice)
    expect(tabs(store.getState())).toEqual([''])
  })

  it('closes the tabs of a deleted session', () => {
    const store = createRootStore()
    store.getState().setIdentityKey(alice)
    store.getState().navigate('run', { runId: 'run_1' })
    store.getState().removeRun('run_1')
    expect(tabs(store.getState())).toEqual([''])
    expect(store.getState().savedTiles[alice]).toBe(store.getState().tiles)
  })

  it('closes the tabs of a removed workspace', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().navigate('run', { runId: 'run_1' })
    store.getState().removeWorkspace('wsp_1')
    expect(tabs(store.getState())).toEqual([''])
  })

  it('prunes unknown sessions only after a live hydration, never from a stale snapshot', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.setState({ staleSince: 1 })
    store.getState().navigate('run', { runId: 'run_gone', view: 'changes' })
    store.setState((s) => ({ tiles: splitTile(s.tiles, s.tiles.focused, 'right') }))
    store.getState().navigate('run', { runId: 'run_1', view: 'trace' })
    store.getState().navigate('run', { runId: 'run_gone' })
    store.getState().navigate('run', { runId: 'run_1' })
    expect(tabs(store.getState())).toEqual(['run_gone:changes', 'run_1:trace'])

    await hydrate(store, fakeApi())
    expect(tabs(store.getState())).toEqual(['run_1:terminal'])
    expect(store.getState().savedTiles[store.getState().identityKey!]).toBe(store.getState().tiles)
  })

  it('settles the live layout before a change, so a view that ended cannot repeat another', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi({ runList: async () => [run({ mode: 'acp', acp: true })] }))
    store.getState().navigate('run', { runId: 'run_1', view: 'browser' })
    const empty = splitTile(store.getState().tiles, store.getState().tiles.focused, 'right')
    store.setState({ tiles: openTab(empty, empty.focused, 'run_1', viewContext(store.getState())) })
    expect(tabs(store.getState())).toEqual(['run_1:browser', 'run_1:session'])

    store.getState().patchRun('run_1', { status: 'merged' })
    store.getState().navigate('run', { runId: 'run_1', view: 'changes' })
    expect(tabs(store.getState())).toEqual(['run_1:changes'])
  })

  it('leaves the layout alone when a change names a tab the settle closed', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi({ runList: async () => [run({ mode: 'acp', acp: true })] }))
    store.getState().navigate('run', { runId: 'run_1', view: 'browser' })
    const empty = splitTile(store.getState().tiles, store.getState().tiles.focused, 'right')
    store.setState({ tiles: openTab(empty, empty.focused, 'run_1', viewContext(store.getState())) })
    const [first, second] = tileList(store.getState().tiles.root)
    store.getState().patchRun('run_1', { status: 'merged' })

    store.setState((s) => tilesAfter(s, (l) => activateTab(l, second.active)))
    expect(tabs(store.getState())).toEqual(['run_1:session'])
    store.setState({ tiles: layout(split('s', 'row', first, second), first.id) })
    store.setState((s) => tilesAfter(s, (l) => moveTab(l, second.active, first.id)))
    expect(tabs(store.getState())).toEqual(['run_1:session'])
  })
})
