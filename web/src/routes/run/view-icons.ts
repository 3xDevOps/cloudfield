import { ChartGantt, FileDiff, FolderTree, Globe, MessageSquare, SquareTerminal, type LucideIcon } from '@/components/icons'
import type { RunView } from '@/routes/run/views'

export const runViewIcon: Record<RunView, LucideIcon> = {
  session: MessageSquare,
  trace: ChartGantt,
  terminal: SquareTerminal,
  changes: FileDiff,
  files: FolderTree,
  browser: Globe,
}
