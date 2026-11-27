import { useId, useState } from 'react'
import { kindWords, loginAgent, names, reach } from '@/components/agents/agent-logins'
import { SignInTerminal } from '@/components/assistant/sign-in'
import { PerUseBadge } from '@/components/per-use-badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import type { Member } from '@/lib/types'
import { sameIdentity } from '@/store'
import { useHeaderPrimary } from '@/store/hooks'
import type { LoginSignIn, LoginSignInOutcome } from '@/store/login-sign-in'

// The server refuses a second sign-in while one runs, naming it; its terminal reattaches until it ends.
const runningSignIn = /the sign-in of login "([^"]+)" \((lgn-[^)\s]+)\) is still running/

/** The server's AgentLoginSignInTimeout, counted from the terminal's first opening. */
const signInRunsMs = 15 * 60_000

function LeaveNote({ opened }: { opened?: number }) {
  useClock()
  const minutes = opened === undefined ? undefined : Math.max(1, Math.ceil((opened + signInRunsMs - Date.now()) / 60_000))
  return (
    <>
      You can leave this page: the sign-in keeps running for {minutes === undefined ? 'up to 15 minutes from its start' : `up to ${minutes} more minute${minutes === 1 ? '' : 's'}`}, and{' '}
      <strong className="font-medium">Add login</strong> offers <strong className="font-medium">Return to the sign-in</strong>.
    </>
  )
}

/**
 * Names a new Claude Code login, runs its sign-in in a terminal of its own and shows what the server made of it.
 * `running` is the sign-in the page knows is still running, which `resume` opens at once; the page waits on its result and passes it as `ended`.
 */
export function AddLogin({ client, sharedWith, running, ended, resume, onRunning, onOpened, onClose }: {
  client: Pick<Api, 'agentLoginAdd' | 'agentLoginSocket'>
  /** Whom the account is shared with; null until read. */
  sharedWith: Member[] | null
  running: LoginSignIn | null
  ended: { id: string; outcome: LoginSignInOutcome } | null
  resume?: boolean
  onRunning: (signIn: LoginSignIn) => void
  /** The terminal of a sign-in this panel started opened for the first time. */
  onOpened: (id: string) => void
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [signIn, setSignIn] = useState<(LoginSignIn & { fresh?: boolean }) | null>(resume ? running : null)
  const field = useId()
  useHeaderPrimary(true)

  const open = (next: LoginSignIn) => {
    setError(null)
    setSignIn(next)
  }

  const named = error ? runningSignIn.exec(error) : null
  // A refusal naming a sign-in that has since ended no longer blocks.
  const refusedBy = named && ended?.id !== named[2] ? { id: named[2], name: named[1] } : null
  const blocking = refusedBy ?? running

  if (signIn) {
    const outcome = ended?.id === signIn.id ? ended.outcome : null
    return (
      <div className="flex flex-col gap-3">
        <h2 className="text-title">Sign in to <span className="font-code">{signIn.name}</span></h2>
        {outcome && ('login' in outcome ? (
          <Callout tone="done" role="status" title={`Added login ${outcome.login.name}`}>
            <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
              {[kindWords(outcome.login), outcome.login.email, outcome.login.organization_name].filter(Boolean).join(' · ')}
              {outcome.login.per_use && <PerUseBadge />}
            </span>
            {outcome.login.per_use && <p className="text-ui-sm">{reach(outcome.login, sharedWith)}</p>}
          </Callout>
        ) : (
          <Callout tone="failed" role="alert" title={`Login ${signIn.name} was not added`} actions={<Button size="sm" variant="secondary" onClick={() => setSignIn(null)}>Start again</Button>}>
            <span className="font-code">{outcome.refused}</span>
          </Callout>
        ))}
        <SignInTerminal
          key={signIn.id}
          agent={{ name: loginAgent, display_name: 'Claude Code' }}
          socket={() => client.agentLoginSocket(signIn.id)}
          reattaches
          onAttached={signIn.fresh ? () => onOpened(signIn.id) : undefined}
          onDone={onClose}
          ended={outcome !== null}
          cancel={outcome ? undefined : { onCancel: onClose, note: <LeaveNote opened={running?.id === signIn.id ? running.opened : undefined} /> }}
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
        const same = sameIdentity()
        setBusy(true)
        setError(null)
        client.agentLoginAdd(loginAgent, chosen).then(
          ({ login_id: id }) => {
            if (!same()) return
            setBusy(false)
            onRunning({ id, name: chosen })
            setSignIn({ id, name: chosen, fresh: true })
          },
          (err: unknown) => {
            if (!same()) return
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
        Sign in to another Claude subscription or a Console account beside your default login.
        An API key is not added here: keep it as your default login or as a workspace secret with <span className="font-code">cloudfield workspace env set --secret ANTHROPIC_API_KEY</span>.
      </p>
      <div className="flex flex-col gap-1">
        <Label htmlFor={field}>Name</Label>
        <Input id={field} value={name} autoFocus autoComplete="off" spellCheck={false} placeholder="work" disabled={blocking !== null} className="max-w-xs" onChange={(event) => setName(event.target.value)} />
        <p className="text-ui-sm text-muted">Lowercase letters, digits and <span className="font-code">-</span>, up to 32 characters.</p>
      </div>
      <p className="text-ui-sm text-text">
        It starts with Automatic and Use for shares on: Cloudfield may start or move your sessions onto it, and
        {sharedWith?.length ? ` sessions of ${names(sharedWith)} can run on it too.` : ' teammates you share your account with can use it too.'} Turn either off with the switches on its row under Claude Code.
      </p>
      {blocking && (refusedBy || !error) ? (
        <Callout tone="neutral" role="status" title={<>Sign-in for <span className="font-code">{blocking.name}</span> is still running</>} actions={returnButton}>
          {refusedBy ? <span className="font-code">{error}</span> : 'Finish or cancel it before you add another login.'}
        </Callout>
      ) : error && (
        <Callout tone="failed" role="alert" title="Sign-in not started" actions={returnButton}>
          <span className="font-code">{error}</span>
        </Callout>
      )}
      <div className="flex gap-2">
        <Button type="submit" variant={blocking ? 'secondary' : 'primary'} disabled={busy || !name.trim() || blocking !== null}>Start sign-in</Button>
        <Button type="button" variant="ghost" disabled={busy} onClick={onClose}>Cancel</Button>
      </div>
    </form>
  )
}
