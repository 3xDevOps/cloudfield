import type { LinearIssue } from '@/lib/types'

/** The link travels in linear_issue_url only: a URL in the task would link the session again after an Unlink. */
export function linearTask(issue: Pick<LinearIssue, 'identifier' | 'title'>): string {
  return `${issue.identifier} ${issue.title}`
}
