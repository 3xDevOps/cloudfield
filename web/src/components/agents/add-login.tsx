import { useEffect, useId, useState } from 'react'
import { loginAgent } from '@/components/agents/agent-logins'
import { SignInTerminal } from '@/components/assistant/sign-in'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentLogin } from '@/lib/types'
import { useHeaderPrimary } from '@/store/hooks'

type Outcome = { login: AgentLogin } | { refused: string }

/** A sign-in of a new login, which outlives the screen that started it. */
export type LoginSignIn = { id: string; name: string }

// The server refuses a second sign-in while one runs, naming it; its terminal reattaches until it ends.
const runningSignIn = /the sign-in of login "([^"]+)" \((lgn-[^)\s]+)\) is still running/

/**
 * Names a new Claude Code login, runs its sign-in in a terminal of its own and shows what the server made of it.
 * `running` is a sign-in this page knows is still running, which `resume` opens at once.
 */
export function AddLogin({ client, running, resume, onRunning, onClose }: {
  client: Pick<Api, 'agentLoginAdd' | 'agentLoginResult' | 'agentLoginSocket'>
  running: LoginSignIn | null
  resume?: boolean
  onRunning: (signIn: LoginSignIn) => void
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [signIn, setSignIn] = useState<LoginSignIn | null>(resume ? running : null)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const field = useId()
  useHeaderPrimary(true)

  useEffect(() => {
    if (!signIn) return
    let live = true
    client.agentLoginResult(loginAgent, signIn.id).then(
      (login) => live && setOutcome({ login }),
      (err: unknown) => live && setOutcome({ refused: message(err) }),
    )
    return () => { live = false }
  }, [client, signIn])

  const again = () => {
    setSignIn(null)
    setOutcome(null)
  }
  const open = (next: LoginSignIn) => {
    setError(null)
    setSignIn(next)
  }

  const refusedBy = error ? runningSignIn.exec(error) : null
  const blocking = refusedBy ? { id: refusedBy[2], name: refusedBy[1] } : running

  if (signIn) {
    return (
      <div className="flex flex-col gap-3">
        <h2 className="text-title">Sign in to <span className="font-code">{signIn.name}</span></h2>
        {outcome && ('login' in outcome ? (
          <Callout tone="done" role="status" title={`Added login ${outcome.login.name}`}>
            {[outcome.login.kind === 'console' ? 'Console, billed per use' : outcome.login.plan ? `Subscription · ${outcome.login.plan}` : 'Subscription', outcome.login.email, outcome.login.organization_name].filter(Boolean).join(' · ')}
          </Callout>
        ) : (
          <Callout tone="failed" role="alert" title={`Login ${signIn.name} was not added`} actions={<Button size="sm" variant="secondary" onClick={again}>Start again</Button>}>
            <span className="font-code">{outcome.refused}</span>
          </Callout>
        ))}
        <SignInTerminal
          key={signIn.id}
          agent={{ name: loginAgent, display_name: 'Claude Code' }}
          socket={() => client.agentLoginSocket(signIn.id)}
          onDone={onClose}
          cancel={outcome ? undefined : {
            onCancel: onClose,
            note: <>Leaving this page keeps the sign-in running, for up to 15 minutes from its start; <strong className="font-medium">Add login</strong> then offers <strong className="font-medium">Return to the sign-in</strong>.</>,
          }}
        >
          Sign in with the account for <span className="font-code">{signIn.name}</span>. The sign-in runs <span className="font-code">claude auth login</span> in a container of its own; your other logins are not touched.
        </SignInTerminal>
      </div>
    )
  }

  const returnButton = blocking && <Button size="sm" onClick={() => open(blocking)}>Return to the sign-in</Button>
  return (
    <form
      aria-label="Add a Claude Code login"
      className="flex max-w-xl flex-col gap-3"
      onSubmit={(event) => {
        event.preventDefault()
        const chosen = name.trim()
        setBusy(true)
        setError(null)
        client.agentLoginAdd(loginAgent, chosen).then(
          ({ login_id: id }) => {
            setBusy(false)
            onRunning({ id, name: chosen })
            setSignIn({ id, name: chosen })
          },
          (err: unknown) => {
            setBusy(false)
            const refusal = message(err)
            const found = runningSignIn.exec(refusal)
            if (found) onRunning({ id: found[2], name: found[1] })
            setError(refusal)
          },
        )
      }}
    >
      <h2 className="text-title">Add a Claude Code login</h2>
      <p className="text-ui text-muted">
        Keep another Claude subscription or a Console sign-in beside your default login. Cloudfield lists it under Claude Code and can start or move sessions onto it.
        A Console API key is not added here: keep it as your default login or as a workspace secret with <span className="font-code">cloudfield workspace env set --secret ANTHROPIC_API_KEY</span>.
      </p>
      <div className="flex flex-col gap-1">
        <Label htmlFor={field}>Name</Label>
        <Input id={field} value={name} autoFocus autoComplete="off" spellCheck={false} placeholder="work" className="max-w-xs" onChange={(event) => setName(event.target.value)} />
        <p className="text-ui-sm text-muted">Lowercase letters, digits and <span className="font-code">-</span>, up to 32 characters.</p>
      </div>
      {error ? (
        <Callout tone="failed" role="alert" title="Not started" actions={returnButton}>
          <span className="font-code">{error}</span>
        </Callout>
      ) : running && (
        <Callout tone="neutral" role="status" title={<>The sign-in of <span className="font-code">{running.name}</span> is still running</>} actions={returnButton}>
          Finish or cancel it before you add another login.
        </Callout>
      )}
      <div className="flex gap-2">
        <Button type="submit" variant={blocking ? 'secondary' : 'primary'} disabled={busy || !name.trim() || blocking !== null}>Start sign-in</Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
      </div>
    </form>
  )
}
