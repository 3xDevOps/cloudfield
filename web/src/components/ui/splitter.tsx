import type * as React from 'react'
import { useDrag } from '@/lib/hooks'
import { splitterTarget } from '@/lib/keys'
import { cn, focusRing } from '@/lib/utils'

/**
 * The handle on the line between two panes. `value`, `min` and `max` are the
 * first pane's size in pixels: the left one of a vertical line, the upper one
 * of a horizontal line.
 */
export function Splitter({
  orientation,
  value,
  min,
  max,
  label,
  onResize,
  onEven,
  className,
  ...props
}: Omit<React.ComponentProps<'div'>, 'onResize'> & {
  orientation: 'vertical' | 'horizontal'
  value: number
  min: number
  max: number
  label: string
  onResize: (value: number) => void
  /** A double click asks for both panes to be the same size. */
  onEven: () => void
}) {
  const beginDrag = useDrag()
  const vertical = orientation === 'vertical'
  const resize = (next: number) => onResize(Math.min(max, Math.max(min, next)))

  const startResize = (event: React.PointerEvent) => {
    event.preventDefault()
    const start = vertical ? event.clientX : event.clientY
    const drag = beginDrag()
    window.addEventListener('pointermove', (ev) => resize(value + (vertical ? ev.clientX : ev.clientY) - start), { signal: drag.signal })
    for (const end of ['pointerup', 'pointercancel']) {
      window.addEventListener(end, () => drag.abort(), { signal: drag.signal })
    }
  }

  const resizeKey = (event: React.KeyboardEvent) => {
    const next = splitterTarget(event.key, vertical
      ? { value, min, max, grow: 'ArrowRight', shrink: 'ArrowLeft' }
      : { value, min, max, grow: 'ArrowDown', shrink: 'ArrowUp' })
    if (next === null) return
    event.preventDefault()
    resize(next)
  }

  return (
    <div
      role="separator"
      data-slot="splitter"
      aria-orientation={orientation}
      aria-label={label}
      aria-valuenow={Math.round(value)}
      aria-valuemin={Math.round(min)}
      aria-valuemax={Math.round(max)}
      tabIndex={0}
      onPointerDown={startResize}
      onKeyDown={resizeKey}
      onDoubleClick={onEven}
      className={cn(
        focusRing,
        // Without `touch-none` the browser claims a touch drag as a pan
        // and cancels the pointer stream this listens to.
        'absolute z-10 touch-none hover:bg-hover-chrome',
        vertical ? 'w-[7px] -translate-x-1/2 cursor-col-resize coarse:w-6' : 'h-[7px] -translate-y-1/2 cursor-row-resize coarse:h-6',
        className,
      )}
      {...props}
    />
  )
}
