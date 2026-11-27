import { Coins } from '@/components/icons'
import { Badge } from '@/components/ui/badge'

/** Marks a login, or a session on one, that is billed per use. */
export function PerUseBadge({ className }: { className?: string }) {
  return <Badge className={className}><Coins aria-hidden />Billed per use</Badge>
}
