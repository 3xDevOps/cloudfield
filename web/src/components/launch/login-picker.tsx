import { type ReactNode, useEffect, useId, useState } from 'react'
import { kindWords, loginAgent } from '@/components/agents/agent-logins'
import { Coins } from '@/components/icons'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentLogin, UsageProvider } from '@/lib/types'
import { limitWords } from '@/routes/settings/usage'

const automatic = '@automatic'

/** The member's own Claude Code logins, read anew each time `enabled` turns on or `attempt` changes; null while off or reading. */
export function useOwnLogins(client: Pick<Api, 'agentLoginList'>, enabled: boolean, attempt = 0): { logins: AgentLogin[] | null; error: string | null } | null {
  const [read, setRead] = useState<{ logins: AgentLogin[] | null; error: string | null } | null>(null)
  useEffect(() => {
    if (!enabled) return
    let live = true
    client.agentLoginList(loginAgent).then(
      (list) => live && setRead({ logins: list, error: null }),
      (err: unknown) => live && setRead({ logins: null, error: message(err) }),
    )
    return () => {
      live = false
      // A list read before must not stand for the next one: a login removed since would still be offered.
      setRead(null)
    }
  }, [client, enabled, attempt])
  return enabled ? read : null
}

function orList(names: string[]): string {
  return names.length < 2 ? names.join('') : `${names.slice(0, -1).join(', ')} or ${names[names.length - 1]}`
}

/** The automatic logins billed per use a session can move to from the login it starts on; '' when none. */
export function perUseReach(logins: AgentLogin[], start: AgentLogin): string {
  return orList(logins.filter((login) => login.automatic && login.per_use && login !== start).map((login) => login.name))
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
 * Whether the session may move to another login, under the line naming the logins billed per use a move could reach.
 * `from` is the login it starts on, `picked` whether the member picked it rather than Automatic. It sets `no_fallback`, the state the Fallback checkbox also sets.
 */
export function PerUseMove({ reach, from, picked, moves, onMoves, disabled }: {
  reach: string
  from: AgentLogin
  picked: boolean
  moves: boolean
  onMoves: (on: boolean) => void
  disabled?: boolean
}) {
  const id = useId()
  const when = !picked ? 'when the logins before it run out' : from.per_use ? `when ${from.name}'s credit is spent` : `when ${from.name} runs out`
  return (
    <div className="flex flex-col gap-1">
      <CostLine>{moves ? `Can move to ${reach}, billed per use, ${when}.` : `Stays on ${from.name}, so it never moves to ${reach}.`}</CostLine>
      <div className="flex min-h-7 items-center justify-between gap-3 coarse:min-h-11">
        <Label htmlFor={id}>Let this session move to another login</Label>
        <Switch id={id} checked={moves} disabled={disabled} onCheckedChange={onMoves} />
      </div>
    </div>
  )
}

/**
 * Which of the member's own logins a launch starts on; '' lets the server choose. A `value` the list no longer holds shows no choice.
 * `usage` is any login's usage the dialog already read, by login id, for its limit state; `moves` is whether automatic changes are on.
 * `move` is the PerUseMove the dialog shows under the help, when it does not show it elsewhere.
 */
export function LoginPicker({ logins, value, onChange, usage = {}, now, moves, move, disabled }: {
  logins: AgentLogin[]
  value: string
  onChange: (login: string) => void
  usage?: Record<string, UsageProvider | undefined>
  now: number
  moves: boolean
  move?: ReactNode
  disabled?: boolean
}) {
  const first = logins.find((login) => login.automatic)
  const picked = logins.find((login) => login.id === value)
  const known = value === '' || picked !== undefined
  const pickedLimit = picked && limitWords(usage[picked.id], now)
  const detail = (login: AgentLogin) => [kindWords(login), login.per_use && 'billed per use', !login.automatic && 'Automatic off', limitWords(usage[login.id], now)].filter(Boolean).join(' · ')
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <Label htmlFor="launch-login">Login</Label>
      <Select value={known ? value || automatic : ''} onValueChange={(next) => onChange(next === automatic ? '' : next)} disabled={disabled}>
        <SelectTrigger id="launch-login" className="w-full min-w-0">
          <SelectValue placeholder="Pick a login" />
        </SelectTrigger>
        {/* As wide as the field, so a long option wraps instead of leaving the screen. */}
        <SelectContent className="max-w-(--radix-select-trigger-width)">
          <SelectItem value={automatic} description={first ? `Tries ${first.name} first` : undefined}>Automatic</SelectItem>
          {logins.map((login) => (
            <SelectItem key={login.id} value={login.id} description={detail(login)}>{login.name}</SelectItem>
          ))}
        </SelectContent>
      </Select>
      {picked ? (
        <>
          <p className="text-ui-sm text-muted">
            {!moves
              ? 'The session starts on this login and stays on it.'
              : pickedLimit
                ? 'This login is at its limit, so the session may start on the next login in your order with usage left.'
                : picked.per_use
                  ? 'The session starts on this login. It has no usage windows, so it moves only when its credit is spent, to the first other automatic login in your order with usage left.'
                  : 'The session starts on this login, and moves to the first other automatic login in your order with usage left when it runs out.'}
          </p>
          {picked.per_use && <CostLine><span className="font-code">{picked.name}</span> is billed per use.</CostLine>}
          {move}
        </>
      ) : known && (
        <>
          <p className="text-ui-sm text-muted">{moves ? 'Starts on the first automatic login in your order with usage left, and moves down the order when it runs out.' : 'Starts on the first automatic login in your order and stays on it.'}</p>
          {first?.per_use && <CostLine>Can start on {first.name}, billed per use.</CostLine>}
          {move}
        </>
      )}
    </div>
  )
}
