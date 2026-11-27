import { memberName } from '@/lib/member-name'
import type { Run } from '@/lib/types'
import { useStore } from '@/store'

/** The account a run spends, when it is worth saying: an added login, or a fallback to another account. */
export function useRunAccountWords(run: Run): string | null {
  const id = run.account_member_id || run.member_id
  const own = useStore((s) => s.info?.member.id === id)
  const owner = useStore((s) => memberName(s, id))
  const whose = own ? 'your' : `${owner}'s`
  const fallback = run.requested_account_member_id ? ' (fallback)' : ''
  if (run.account_login_name) return `On ${whose} account, login ${run.account_login_name}${fallback}`
  return fallback ? `On ${whose} account${fallback}` : null
}
