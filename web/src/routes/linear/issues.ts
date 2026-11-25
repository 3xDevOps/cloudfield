import type { Tone } from '@/components/ui/status-dot'
import type { LinearIssue } from '@/lib/types'

export function linearTone(state: Pick<LinearIssue['state'], 'name' | 'type'>): Tone {
  if (state.name === 'In Review') return 'review'
  switch (state.type) {
    case 'started': return 'working'
    case 'completed': return 'done'
    case 'canceled': return 'closed'
    default: return 'neutral'
  }
}

export function linearTask(issue: Pick<LinearIssue, 'identifier' | 'title' | 'url'>): string {
  return `${issue.identifier} ${issue.title}\n\nLinked Linear issue: ${issue.identifier} ${issue.url}`
}
