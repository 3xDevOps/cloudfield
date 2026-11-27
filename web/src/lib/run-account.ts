import { memberName } from '@/lib/member-name'
import type { Run } from '@/lib/types'
import { useStore } from '@/store'
import { useLoginsServer } from '@/store/hooks'

/** The added login a run is on: its name, or its id once the login is removed; '' on the default login. */
export function runLoginName(run: Pick<Run, 'account_login' | 'account_login_name'>): string {
  if (run.account_login_name) return run.account_login_name
  return run.account_login && run.account_login !== 'default' ? run.account_login : ''
}

/** The account a run spends, when it is worth saying: an added login, or a fallback to another account. */
export function useRunAccountWords(run: Run): string | null {
  const id = run.account_member_id || run.member_id
  const own = useStore((s) => s.info?.member.id === id)
  const owner = useStore((s) => memberName(s, id))
  const whose = own ? 'your' : `${owner}'s`
  const fallback = run.requested_account_member_id ? ' (fallback)' : ''
  const login = runLoginName(run)
  if (login) return `On ${whose} account, login ${login}${fallback}`
  return fallback ? `On ${whose} account${fallback}` : null
}

/** The same words for a surface that named no account before logins: from a server without them, a run's fallback stays off it. */
export function useRunAccountLine(run: Run): string | null {
  const words = useRunAccountWords(run)
  const loginsServer = useLoginsServer()
  const carries = run.account_login !== undefined || run.account_login_name !== undefined || run.account_login_per_use !== undefined
  return loginsServer || carries ? words : null
}
