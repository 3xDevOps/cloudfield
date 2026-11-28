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
export function Tile({ tile, phone, rect, parked, opening, narrow, room, linked, onSlot }: {
  tile: TileNode
  /** A phone draws the body alone: no strip, and nothing that marks a tile or the panel of a strip tab. */
  phone: boolean
  /** In percent of the area. */
  rect: Rect
  /** Hidden behind the maximized tile, and still mounted. */
  parked: boolean
  /** Growing in: a bare strip until it lands. */
  opening: boolean
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
        data-tile={phone ? undefined : tile.id}
        data-tile-rest={!focused || undefined}
        tabIndex={-1}
        inert={parked}
        aria-hidden={parked || undefined}
        aria-label={phone ? undefined : `${name}, tile ${visible.indexOf(tile) + 1} of ${visible.length}`}
        style={{ left: `${rect.left}%`, top: `${rect.top}%`, width: `${rect.width}%`, height: `${rect.height}%` }}
        className={cn(
          'absolute isolate flex min-h-0 min-w-0 flex-col border-seam bg-canvas outline-none after:pointer-events-none after:absolute after:inset-0 after:z-10 after:border',
          // A parked tile stays in sight under the glide of the tile that covers it.
          parked && 'invisible transition-[visibility] delay-[var(--duration-glide)] duration-0 motion-reduce:delay-0',
          layout.maximized && focused && 'z-1',
          rect.left > 0 && 'border-l',
          rect.top > 0 && 'border-t',
          linked ? 'after:border-dashed after:border-accent' : focused && visible.length > 1 && !dragging ? 'after:border-accent' : 'after:border-transparent',
        )}
      >
        {!phone && <TabStrip tile={tile} opening={opening} narrow={narrow} room={room} />}
        {tab ? (
          <TabsContent ref={slot} value={tab.id} forceMount className="relative flex-1" {...(phone && { role: undefined, tabIndex: undefined, 'aria-labelledby': undefined })}>
            {!run && <MissingRun tiled={!phone} runId={tab.runId} />}
          </TabsContent>
        ) : (
          <div className={cn('flex min-h-0 flex-1 justify-center p-6', opening && 'opacity-0')}>
            <Picker tileId={tile.id} runId={splitFrom} autoFocus={focused} className="w-full max-w-lg" />
          </div>
        )}
      </section>
    </Tabs>
  )
}
