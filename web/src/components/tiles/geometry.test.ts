import { describe, expect, it } from 'vitest'
import { dropZone, fits, minSize, sideOf, tileRects } from '@/components/tiles/geometry'
import type { Tile, TileNode, TileSplit } from '@/store/tiles'

const tile = (id: string): Tile => ({ id, tabs: [], active: '' })
const row = (id: string, a: TileNode, b: TileNode, ratio = 0.5): TileSplit => ({ id, dir: 'row', ratio, a, b })
const column = (id: string, a: TileNode, b: TileNode, ratio = 0.5): TileSplit => ({ id, dir: 'column', ratio, a, b })

const one = tile('a')
const sideBySide = row('r', tile('a'), tile('b'))
const stacked = column('c', tile('a'), tile('b'))
const three = row('r', tile('a'), column('c', tile('b'), tile('d')))
const columns = row('r', tile('a'), row('q', tile('b'), tile('d')))
const grid = row('r', column('c', tile('a'), tile('b')), column('e', tile('d'), tile('f')))

describe('minimum size', () => {
  it('adds the floors along a split and takes the larger across it', () => {
    expect(minSize(one)).toEqual({ width: 420, height: 400 })
    expect(minSize(sideBySide)).toEqual({ width: 840, height: 400 })
    expect(minSize(stacked)).toEqual({ width: 420, height: 800 })
    expect(minSize(three)).toEqual({ width: 840, height: 800 })
    expect(minSize(grid)).toEqual({ width: 840, height: 800 })
  })
})

describe('fit', () => {
  // The area beside a 260px sidebar, under the window chrome.
  it('caps a 1280x800 window at two columns', () => {
    const area = { width: 1020, height: 760 }
    expect(fits(one, area)).toBe(true)
    expect(fits(sideBySide, area)).toBe(true)
    expect(fits(stacked, area)).toBe(false)
    expect(fits(columns, area)).toBe(false)
  })

  it('holds four tiles in a 1920x1200 window', () => {
    const area = { width: 1660, height: 1160 }
    expect(fits(grid, area)).toBe(true)
    expect(fits(columns, area)).toBe(true)
    expect(fits(row('x', grid, tile('g')), area)).toBe(true)
    expect(fits(row('x', columns, tile('g')), area)).toBe(false)
  })

  it('holds one column in a 960px window', () => {
    expect(fits(sideBySide, { width: 700, height: 560 })).toBe(false)
    expect(fits(one, { width: 700, height: 560 })).toBe(true)
  })
})

describe('rectangles', () => {
  const area = { width: 2000, height: 1200 }

  it('gives one tile the whole area and no seam', () => {
    expect(tileRects(one, area)).toEqual({ tiles: { a: { left: 0, top: 0, width: 100, height: 100 } }, seams: [] })
  })

  it('divides a split by its ratio, in percent', () => {
    const { tiles, seams } = tileRects(row('r', tile('a'), column('c', tile('b'), tile('d'), 0.4), 0.25), area)
    expect(tiles).toEqual({
      a: { left: 0, top: 0, width: 25, height: 100 },
      b: { left: 25, top: 0, width: 75, height: 40 },
      d: { left: 25, top: 40, width: 75, height: 60 },
    })
    expect(seams).toEqual([
      { id: 'r', dir: 'row', rect: { left: 25, top: 0, width: 0, height: 100 }, within: { left: 0, top: 0, width: 100, height: 100 }, share: 0.25, min: 0.21, max: 0.79 },
      { id: 'c', dir: 'column', rect: { left: 25, top: 40, width: 75, height: 0 }, within: { left: 25, top: 0, width: 75, height: 100 }, share: 0.4, min: 400 / 1200, max: 1 - 400 / 1200 },
    ])
  })

  it('holds a ratio inside both floors', () => {
    const narrow = tileRects(row('r', tile('a'), tile('b'), 0.1), { width: 1000, height: 600 })
    expect(narrow.tiles.a.width).toBeCloseTo(42)
    expect(narrow.tiles.b).toMatchObject({ left: narrow.tiles.a.width, width: 100 - narrow.tiles.a.width })
    expect(narrow.seams[0]).toMatchObject({ share: 0.42, min: 0.42, max: 1 - 0.42 })
    expect(tileRects(row('r', tile('a'), tile('b'), 0.9), { width: 1000, height: 600 }).tiles.a.width).toBeCloseTo(58)
  })

  it('measures a nested floor against its own split, not the area', () => {
    const { tiles } = tileRects(row('r', tile('a'), row('q', tile('b'), tile('d'), 0.9), 0.3), { width: 1400, height: 600 })
    expect(tiles.a.width).toBeCloseTo(30)
    expect((tiles.d.width / 100) * 1400).toBeCloseTo(420)
  })

  it('keeps the stored ratio when the area cannot hold both floors or is not measured', () => {
    expect(tileRects(row('r', tile('a'), tile('b'), 0.1), { width: 600, height: 600 }).tiles.a.width).toBeCloseTo(10)
    expect(tileRects(sideBySide, { width: 0, height: 0 }).tiles).toEqual({
      a: { left: 0, top: 0, width: 50, height: 100 },
      b: { left: 50, top: 0, width: 50, height: 100 },
    })
  })
})

describe('drop zone', () => {
  const size = { width: 1000, height: 500 }

  it('splits a side from its outer fifth and adds a tab from the centre', () => {
    expect(dropZone(size, 100, 250)).toBe('left')
    expect(dropZone(size, 900, 250)).toBe('right')
    expect(dropZone(size, 500, 50)).toBe('up')
    expect(dropZone(size, 500, 450)).toBe('down')
    expect(dropZone(size, 500, 250)).toBe('center')
    expect(dropZone(size, 200, 100)).toBe('center')
    expect(dropZone(size, 199, 250)).toBe('left')
  })

  it('picks the nearer edge in a corner', () => {
    expect(dropZone(size, 50, 75)).toBe('left')
    expect(dropZone(size, 150, 25)).toBe('up')
    expect(dropZone(size, 950, 425)).toBe('right')
  })
})

describe('relative side', () => {
  it('names the side one tile is on from another', () => {
    expect(sideOf(sideBySide, 'a', 'b')).toBe('right')
    expect(sideOf(sideBySide, 'b', 'a')).toBe('left')
    expect(sideOf(stacked, 'a', 'b')).toBe('below')
    expect(sideOf(stacked, 'b', 'a')).toBe('above')
  })

  it('reads the split that separates the two tiles', () => {
    expect(sideOf(three, 'd', 'a')).toBe('left')
    expect(sideOf(three, 'a', 'd')).toBe('right')
    expect(sideOf(three, 'b', 'd')).toBe('below')
    expect(sideOf(grid, 'f', 'd')).toBe('above')
    expect(sideOf(grid, 'a', 'a')).toBeUndefined()
  })
})
