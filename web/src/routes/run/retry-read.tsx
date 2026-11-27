import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

/** Retry beside a failed read. Both labels share one cell, so the error beside it does not wrap again while the read runs. */
export function RetryRead({ retrying, onRetry }: { retrying: boolean; onRetry: () => void }) {
  return (
    <Button size="sm" variant="quiet" disabled={retrying} onClick={onRetry}>
      <span className="grid">
        <span aria-hidden={retrying} className={cn('[grid-area:1/1]', retrying && 'invisible')}>Retry</span>
        <span aria-hidden={!retrying} className={cn('[grid-area:1/1]', !retrying && 'invisible')}>Retrying…</span>
      </span>
    </Button>
  )
}
