import type { LinearIssue } from '@/lib/types'

/** The link travels in linear_issue_url only: a URL in the task would link the session again after an Unlink. */
export function linearTask(issue: Pick<LinearIssue, 'identifier' | 'title'>): string {
  return `${issue.identifier} ${issue.title}`
}

/** https://linear.app/<workspace>/issue/<ID> without the slug, query or fragment, as the server stores linear_issue_url. */
export function canonicalIssueURL(raw: string): string {
  const match = /^https:\/\/linear\.app\/([^/?#]+)\/issue\/([^/?#]+)/.exec(raw)
  return match ? `https://linear.app/${match[1]}/issue/${match[2]!.toUpperCase()}` : raw
}
