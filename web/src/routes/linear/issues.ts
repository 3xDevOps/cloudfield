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

/** The link travels in linear_issue_url only: a URL in the task would link the session again after an Unlink. */
export function linearTask(issue: Pick<LinearIssue, 'identifier' | 'title'>): string {
  return `${issue.identifier} ${issue.title}`
}
