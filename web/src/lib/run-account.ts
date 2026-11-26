import type { Run } from '@/lib/types'

/** The account a run spends, when it is worth saying: an added login, or a fallback to another account. */
export function runAccountWords(run: Run, owner: string): string | null {
  const fallback = run.requested_account_member_id ? ' (fallback)' : ''
  if (run.account_login_name) return `On ${owner}'s account, login ${run.account_login_name}${fallback}`
  return fallback ? `On ${owner}'s account${fallback}` : null
}
