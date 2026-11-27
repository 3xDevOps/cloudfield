import { type ReactNode, useEffect, useState } from 'react'
import { loginAgent } from '@/components/agents/agent-logins'
import { Coins } from '@/components/icons'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import type { AgentLogin, UsageProvider } from '@/lib/types'
import { limitWords } from '@/routes/settings/usage'

const automatic = '@automatic'

/** The member's own Claude Code logins, read while `enabled`; null until read or when the read fails. */
export function useOwnLogins(client: Pick<Api, 'agentLoginList'>, enabled: boolean): AgentLogin[] | null {
  const [logins, setLogins] = useState<AgentLogin[] | null>(null)
  useEffect(() => {
    if (!enabled) return
    let live = true
    client.agentLoginList(loginAgent).then((list) => live && setLogins(list), () => live && setLogins(null))
    return () => { live = false }
  }, [client, enabled])
  return enabled ? logins : null
}

function orList(names: string[]): string {
  return names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}`
}

/** The logins billed per use a session started on Automatic can move to, after the first automatic one; '' when none. */
export function automaticReachesPerUse(logins: AgentLogin[]): string {
  const first = logins.find((login) => login.automatic)
  return orList(logins.filter((login) => login.automatic && login.per_use && login !== first).map((login) => login.name))
}

function CostLine({ children }: { children: ReactNode }) {
  return (
    <p className="flex items-start gap-1.5 text-ui-sm text-text">
      <Coins aria-hidden className="mt-0.5 size-3.5 shrink-0 text-muted" />
      <span className="min-w-0 break-words">{children}</span>
    </p>
  )
}

/**
 * Which of the member's own logins a launch starts on; '' lets the server choose. Shown only with more than one login.
 * `usage` is any login's usage the dialog already read, by login id, for its limit state.
 */
export function LoginPicker({ logins, value, onChange, usage = {}, now, disabled }: {
  logins: AgentLogin[]
  value: string
  onChange: (login: string) => void
  usage?: Record<string, UsageProvider | undefined>
  now: number
  disabled?: boolean
}) {
  const first = logins.find((login) => login.automatic)
  const perUse = automaticReachesPerUse(logins)
  const picked = logins.find((login) => login.id === value)
  const detail = (login: AgentLogin) => [login.per_use && 'billed per use', !login.automatic && 'Automatic off', limitWords(usage[login.id], now)].filter(Boolean).join(' · ')
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Label htmlFor="launch-login">Login</Label>
      <Select value={value || automatic} onValueChange={(next) => onChange(next === automatic ? '' : next)} disabled={disabled}>
        <SelectTrigger id="launch-login" className="w-full min-w-0">
          <SelectValue />
        </SelectTrigger>
        {/* As wide as the field, so a long option wraps instead of leaving the screen. */}
        <SelectContent className="max-w-(--radix-select-trigger-width)">
          <SelectItem value={automatic} description={first ? `Tries ${first.name} first` : undefined}>Automatic</SelectItem>
          {logins.map((login) => (
            <SelectItem key={login.id} value={login.id} description={detail(login) || undefined}>{login.name}</SelectItem>
          ))}
        </SelectContent>
      </Select>
      {picked ? (
        <>
          <p className="text-ui-sm text-muted">The session starts on this login; it may still move to another when this one runs out.</p>
          {picked.per_use && <CostLine><span className="font-code">{picked.name}</span> is billed per use.</CostLine>}
        </>
      ) : (
        <>
          <p className="text-ui-sm text-muted">Starts on the first login in your order with usage left.</p>
          {first?.per_use && <CostLine>Can start on {first.name}, billed per use.</CostLine>}
          {perUse && <CostLine>Can move to {perUse}, billed per use, when the logins before it run out.</CostLine>}
        </>
      )}
    </div>
  )
}
