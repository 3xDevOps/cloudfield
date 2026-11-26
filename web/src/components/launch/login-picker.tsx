import { useEffect, useState } from 'react'
import { loginAgent } from '@/components/agents/agent-logins'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Api } from '@/lib/api'
import type { AgentLogin } from '@/lib/types'

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

/** Which of the member's own logins a launch starts on; '' lets the server choose. Shown only with more than one login. */
export function LoginPicker({ logins, value, onChange, disabled }: { logins: AgentLogin[]; value: string; onChange: (login: string) => void; disabled?: boolean }) {
  const first = logins.find((login) => login.automatic)
  return (
    <div className="flex flex-col gap-1">
      <Label htmlFor="launch-login">Login</Label>
      <Select value={value || automatic} onValueChange={(next) => onChange(next === automatic ? '' : next)} disabled={disabled}>
        <SelectTrigger id="launch-login" className="w-full min-w-0">
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={automatic}>{first ? `Automatic: starts on ${first.name}` : 'Automatic'}</SelectItem>
          {logins.map((login) => (
            <SelectItem key={login.id} value={login.id}>
              {login.name}{login.per_use ? ' · billed per use' : ''}{login.automatic ? '' : ' · Automatic off'}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      <p className="text-ui-sm text-muted">
        {value ? 'The session starts on this login; it may still move to another when this one runs out.' : 'Starts on the first login in your order with usage left.'}
      </p>
    </div>
  )
}
