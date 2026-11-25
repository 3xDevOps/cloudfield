import type { TileNode, TileSide } from '@/store/tiles'

export interface Size {
  width: number
  height: number
}

export interface Rect extends Size {
  left: number
  top: number
}

interface Seam {
  id: string
  dir: 'row' | 'column'
  /** The line between the two sides, zero thick, in percent of the area. */
  rect: Rect
  /** The split the seam divides, in percent of the area. */
  within: Rect
  /** Where the seam sits and how far it may move, as the first side's share of the split. */
  share: number
  min: number
  max: number
}

interface TileRects {
  /** By tile id, in percent of the area. */
  tiles: Record<string, Rect>
  seams: Seam[]
}

/** No tile is drawn smaller than this, in pixels. */
const tileFloor: Size = { width: 420, height: 400 }

export function minSize(node: TileNode): Size {
  if ('tabs' in node) return tileFloor
  const a = minSize(node.a)
  const b = minSize(node.b)
  return node.dir === 'row'
    ? { width: a.width + b.width, height: Math.max(a.height, b.height) }
    : { width: Math.max(a.width, b.width), height: a.height + b.height }
}

export function fits(node: TileNode, area: Size): boolean {
  const min = minSize(node)
  return min.width <= area.width && min.height <= area.height
}

function place(node: TileNode, rect: Rect, size: Size, out: TileRects) {
  if ('tabs' in node) {
    out.tiles[node.id] = rect
    return
  }
  const row = node.dir === 'row'
  const extent = row ? size.width : size.height
  const min = (row ? minSize(node.a).width : minSize(node.a).height) / extent
  const max = 1 - (row ? minSize(node.b).width : minSize(node.b).height) / extent
  // A split too small for both floors keeps its stored ratio; the caller shows such a layout maximized.
  const share = min <= max ? Math.min(max, Math.max(min, node.ratio)) : node.ratio
  const a = row ? { ...rect, width: rect.width * share } : { ...rect, height: rect.height * share }
  const b = row
    ? { ...rect, left: rect.left + a.width, width: rect.width - a.width }
    : { ...rect, top: rect.top + a.height, height: rect.height - a.height }
  out.seams.push({ id: node.id, dir: node.dir, rect: row ? { ...b, width: 0 } : { ...b, height: 0 }, within: rect, share, min, max })
  place(node.a, a, row ? { ...size, width: size.width * share } : { ...size, height: size.height * share }, out)
  place(node.b, b, row ? { ...size, width: size.width * (1 - share) } : { ...size, height: size.height * (1 - share) }, out)
}

/** Where each tile and seam sits in an area of `area` pixels, with every ratio held inside both sides' floors. */
export function tileRects(root: TileNode, area: Size): TileRects {
  const out: TileRects = { tiles: {}, seams: [] }
  place(root, { left: 0, top: 0, width: 100, height: 100 }, area, out)
  return out
}

/** What a drop at a point inside a tile of `size` does: the outer 20% splits that side, the rest adds a tab. */
export function dropZone(size: Size, x: number, y: number): TileSide | 'center' {
  const edges: [TileSide, number][] = [
    ['left', x / size.width],
    ['right', 1 - x / size.width],
    ['up', y / size.height],
    ['down', 1 - y / size.height],
  ]
  const [side, distance] = edges.reduce((nearest, edge) => (edge[1] < nearest[1] ? edge : nearest))
  return distance < 0.2 ? side : 'center'
}

export function sideOf(node: TileNode, from: string, to: string): 'left' | 'right' | 'above' | 'below' | undefined {
  if ('tabs' in node) return undefined
  const holds = (within: TileNode, id: string): boolean => ('tabs' in within ? within.id === id : holds(within.a, id) || holds(within.b, id))
  const inA = (id: string) => holds(node.a, id)
  if (inA(from) === inA(to)) return sideOf(inA(from) ? node.a : node.b, from, to)
  if (node.dir === 'row') return inA(to) ? 'left' : 'right'
  return inA(to) ? 'above' : 'below'
}
