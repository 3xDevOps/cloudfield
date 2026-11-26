import { useEffect, useId, useState } from 'react'
import { AnthropicNote, loginAgent } from '@/components/agents/agent-logins'
import { SignInTerminal } from '@/components/assistant/sign-in'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Api } from '@/lib/api'
import { message } from '@/lib/format'
import type { AgentLogin } from '@/lib/types'

type Outcome = { login: AgentLogin } | { refused: string }

/** Names a new Claude Code login, runs its sign-in in a terminal of its own and shows what the server made of it. */
export function AddLogin({ client, onClose }: { client: Pick<Api, 'agentLoginAdd' | 'agentLoginResult' | 'agentLoginSocket'>; onClose: () => void }) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [signIn, setSignIn] = useState<{ id: string; name: string } | null>(null)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const field = useId()

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

  if (signIn) {
    return (
      <div className="flex flex-col gap-3">
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
          back={outcome ? undefined : { label: 'Cancel sign-in', onBack: onClose }}
        >
          Sign in with the account for <span className="font-code">{signIn.name}</span>. The sign-in runs <span className="font-code">claude auth login</span> in a container of its own; your other logins are not touched.
        </SignInTerminal>
      </div>
    )
  }

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
            setSignIn({ id, name: chosen })
          },
          (err: unknown) => {
            setBusy(false)
            setError(message(err))
          },
        )
      }}
    >
      <h2 className="text-title">Add a Claude Code login</h2>
      <p className="text-ui text-muted">
        Keep another Claude subscription or a Console sign-in beside the login in your home. Cloudfield lists it under Claude Code and can start or move sessions onto it. A Console API key is not accepted here.
      </p>
      <div className="flex flex-col gap-1">
        <Label htmlFor={field}>Name</Label>
        <Input id={field} value={name} autoFocus autoComplete="off" spellCheck={false} placeholder="work" className="max-w-xs" onChange={(event) => setName(event.target.value)} />
        <p className="text-ui-sm text-muted">Lowercase letters, digits and <span className="font-code">-</span>, up to 32 characters.</p>
      </div>
      {error && <Callout tone="failed" role="alert" title="Not started"><span className="font-code">{error}</span></Callout>}
      <AnthropicNote />
      <div className="flex gap-2">
        <Button type="submit" disabled={busy || !name.trim()}>Start sign-in</Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
      </div>
    </form>
  )
}
