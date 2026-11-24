import { useCallback, useContext } from 'react'
import { MissingRun } from '@/components/missing-run'
import { changeTiles, TileAreaContext } from '@/components/tiles/area'
import type { Rect } from '@/components/tiles/geometry'
import { Picker } from '@/components/tiles/picker'
import { TabStrip } from '@/components/tiles/tab-strip'
import { Tabs, TabsContent } from '@/components/ui/tabs'
import { runLabel } from '@/lib/status'
import { cn } from '@/lib/utils'
import { runViewLabel } from '@/routes/run/views'
import { useStore } from '@/store'
import { activateTab, activeTab, resolveView, visibleTiles, type Tile as TileNode } from '@/store/tiles'

/**
 * One tile: the strip of session tabs over the body of the active one. The
 * area portals that session's frame into the body through `onSlot`; an empty
 * tile shows the picker instead.
 */
export function Tile({ tile, rect, parked, narrow, room, linked, onSlot }: {
  tile: TileNode
  /** In percent of the area. */
  rect: Rect
  /** Hidden behind the maximized tile, and still mounted. */
  parked: boolean
  narrow: boolean
  room: { right: boolean; down: boolean }
  /** An arrow that points at this tile's view is hovered or focused. */
  linked: boolean
  onSlot: (tileId: string, slot: HTMLElement | null) => void
}) {
  const { layout, ctx } = useContext(TileAreaContext)!
  const splitFrom = useStore((s) => s.route.params.runId)
  const dragging = useStore((s) => s.tileDrag !== null)
  const slot = useCallback((node: HTMLElement | null) => onSlot(tile.id, node), [onSlot, tile.id])
  const visible = visibleTiles(layout)
  const focused = layout.focused === tile.id
  const tab = activeTab(tile)
  const run = tab && ctx.runs[tab.runId]
  const name = run ? `${runLabel(run)}, ${runViewLabel[resolveView(tab, run, ctx.cap, ctx.memory)]}` : tab ? 'Session' : 'New tile'
  return (
    <Tabs value={tile.active} onValueChange={(id) => changeTiles((before) => activateTab(before, id))} asChild>
      <section
        data-tile={tile.id}
        tabIndex={-1}
        inert={parked}
        aria-hidden={parked || undefined}
        aria-label={`${name}, tile ${visible.indexOf(tile) + 1} of ${visible.length}`}
        style={{ left: `${rect.left}%`, top: `${rect.top}%`, width: `${rect.width}%`, height: `${rect.height}%` }}
        className={cn(
          'absolute flex min-h-0 min-w-0 flex-col border-seam bg-canvas outline-none after:pointer-events-none after:absolute after:inset-0 after:z-10 after:border',
          // A parked tile stays in sight under the glide of the tile that covers it.
          parked && 'invisible transition-[visibility] delay-[var(--duration-glide)] duration-0 motion-reduce:delay-0',
          layout.maximized && focused && 'z-1',
          rect.left > 0 && 'border-l',
          rect.top > 0 && 'border-t',
          linked ? 'after:border-dashed after:border-accent' : focused && visible.length > 1 && !dragging ? 'after:border-accent' : 'after:border-transparent',
        )}
      >
        <TabStrip tile={tile} narrow={narrow} room={room} />
        {tab ? (
          <TabsContent ref={slot} value={tab.id} forceMount className="relative flex-1">
            {!run && <MissingRun tiled />}
          </TabsContent>
        ) : (
          <div className="min-h-0 flex-1 overflow-y-auto p-6">
            <Picker tileId={tile.id} runId={splitFrom} autoFocus={focused} className="mx-auto max-w-lg" />
          </div>
        )}
      </section>
    </Tabs>
  )
}
