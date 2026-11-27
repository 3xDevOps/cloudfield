import { Coins } from '@/components/icons'
import { Badge } from '@/components/ui/badge'

/**
 * Marks a login, or a session on one, that is billed per use. `payer` joins the badge's own words where nothing beside it
 * names who pays; `title` says whom the use is charged to, and is then also what a screen reader reads.
 */
export function PerUseBadge({ className, payer, title }: { className?: string; payer?: string; title?: string }) {
  return (
    <Badge className={className} title={title}>
      <Coins aria-hidden />
      <span aria-hidden={title ? true : undefined}>Billed per use{payer && ` · ${payer}`}</span>
      {title && <span className="sr-only">{title}</span>}
    </Badge>
  )
}
