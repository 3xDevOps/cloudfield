import { type ReactNode, useCallback, useEffect, useId, useState } from 'react'
import { ChevronDown, ChevronUp, Trash2 } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Callout } from '@/components/ui/callout'
import { Input } from '@/components/ui/input'
import { Switch } from '@/components/ui/switch'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import type { Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { message } from '@/lib/format'
import type { AgentLogin, Member, UsageProvider } from '@/lib/types'
import { currentWindows, UsageMeters } from '@/routes/settings/usage'

/** Only Claude Code has added logins. */
export const loginAgent = 'claude'

export const severalLoginsDocs = 'https://github.com/3xDevOps/cloudfield/blob/main/docs/agents.md#several-logins'

export type LoginsClient = Pick<Api, 'agentLoginRename' | 'agentLoginRemove' | 'agentLoginOrder' | 'agentLoginAutomatic' | 'agentLoginShare' | 'accountUsage'>

export function useAgentLogins(client: Pick<Api, 'agentLoginList'>, enabled: boolean) {
  const [logins, setLogins] = useState<AgentLogin[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const reload = useCallback(() => {
    if (!enabled) return
    client.agentLoginList(loginAgent).then(
      (list) => {
        setLogins(list)
        setError(null)
      },
      (err: unknown) => setError(message(err)),
    )
  }, [client, enabled])
  useEffect(reload, [reload])
  return { logins: enabled ? logins : null, error: enabled ? error : null, reload }
}

export function AnthropicNote() {
  return (
    <p className="text-ui-sm text-muted">
      Anthropic&apos;s pages describe keeping several accounts and using a Console account at a subscription limit; they do not settle moving between two of your own subscriptions at a limit.{' '}
      <a className="text-accent hover:underline" href={severalLoginsDocs} target="_blank" rel="noopener noreferrer">What they say</a>
    </p>
  )
}

function kindWords(login: AgentLogin): string {
  if (login.kind === 'subscription') return login.plan ? `Subscription · ${login.plan}` : 'Subscription'
  if (login.kind === 'console') return 'Console'
  if (login.kind === 'api_key') return 'API key'
  return 'No login found'
}

function names(members: Member[]): string {
  const list = members.map((member) => member.display_name)
  return list.length < 2 ? list.join('') : `${list.slice(0, -1).join(', ')} and ${list[list.length - 1]}`
}

/** Whether a teammate's session can land on the login: teammates never name a login, so only an automatic one reaches them. */
function reach(login: AgentLogin, sharedWith: Member[]): string {
  if (!login.shared) return 'Off for shares: your sessions only'
  if (sharedWith.length === 0) return 'No share reaches it: your account is not shared'
  if (!login.automatic) return 'No share reaches it while Automatic is off'
  return `Shares reach it: ${names(sharedWith)}`
}

/** The server's own rule: the default login is the files in the member's home. */
const defaultFixed = 'The default login is the one in your home; it is not renamed or removed here'

type Usage = { provider?: UsageProvider; error?: string }

function useLoginUsage(client: Pick<Api, 'accountUsage'>, logins: AgentLogin[]): Record<string, Usage> {
  const [usage, setUsage] = useState<Record<string, Usage>>({})
  // Sorted, so a reorder reads nothing again.
  const ids = logins.map((login) => login.id).sort().join(' ')
  useEffect(() => {
    let live = true
    for (const id of ids.split(' ')) {
      client.accountUsage(id === 'default' ? {} : { login: id }).then(
        (result) => live && setUsage((current) => ({ ...current, [id]: { provider: result.providers.find((p) => p.provider === loginAgent) } })),
        (err: unknown) => live && setUsage((current) => ({ ...current, [id]: { error: message(err) } })),
      )
    }
    return () => { live = false }
  }, [client, ids])
  return usage
}

function RenameForm({ login, client, onDone, onCancel }: { login: AgentLogin; client: LoginsClient; onDone: () => void; onCancel: () => void }) {
  const [name, setName] = useState(login.name)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const id = useId()
  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(event) => {
        event.preventDefault()
        setBusy(true)
        setError(null)
        client.agentLoginRename(loginAgent, login.id, name.trim()).then(onDone, (err: unknown) => {
          setBusy(false)
          setError(message(err))
        })
      }}
    >
      <label htmlFor={id} className="sr-only">New name for {login.name}</label>
      <span className="flex flex-wrap items-center gap-2">
        <Input id={id} value={name} autoFocus autoComplete="off" spellCheck={false} className="w-48 max-w-full" onChange={(event) => setName(event.target.value)} />
        <Button size="sm" type="submit" variant="secondary" disabled={busy || !name.trim()}>Save</Button>
        <Button size="sm" type="button" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
      </span>
      {error && <p role="alert" className="font-code text-ui-sm break-words text-state-failed">{error}</p>}
    </form>
  )
}

export function RemoveLogin({ login, client, onRemoved, onClose }: { login: AgentLogin; client: LoginsClient; onRemoved: () => void; onClose: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  return (
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">Remove login {login.name}?</AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            Deletes this login&apos;s files from the server, so no session starts or moves onto it again. It does not sign the login out at Anthropic: its sign-in stays valid there until Anthropic expires it or you revoke it.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error !== null && <Callout tone="failed" role="alert" title="Not removed"><span className="font-code">{error}</span></Callout>}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              setBusy(true)
              setError(null)
              client.agentLoginRemove(loginAgent, login.id).then(
                () => {
                  onRemoved()
                  onClose()
                },
                (err: unknown) => {
                  setBusy(false)
                  setError(message(err))
                },
              )
            }}
          >
            Remove
          </AlertDialogAction>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function LoginRow({ login, index, count, usage, sharedWith, busy, onMove, onAutomatic, onShare, onRename, onRemove, renaming }: {
  login: AgentLogin
  index: number
  count: number
  usage: Usage | undefined
  sharedWith: Member[]
  busy: boolean
  onMove: (by: number) => void
  onAutomatic: (on: boolean) => void
  onShare: (on: boolean) => void
  onRename: () => void
  onRemove: () => void
  renaming?: ReactNode
}) {
  const id = useId()
  const windows = currentWindows(usage?.provider, Date.now())
  const usageError = usage?.error ?? (windows.length === 0 ? usage?.provider?.error : undefined)
  const added = login.id !== 'default'
  const who = [login.email, login.organization_name].filter(Boolean).join(' · ')
  return (
    <li aria-label={`Login ${login.name}`} className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 lg:flex-row lg:items-start lg:gap-4">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        {renaming ?? (
          <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
            <span className="min-w-0 truncate text-ui font-medium text-text">{login.name}</span>
            <span className="text-ui-sm text-muted">{kindWords(login)}</span>
            {login.per_use && <Badge>Billed per use</Badge>}
          </span>
        )}
        {who && <span className="min-w-0 truncate text-ui-sm text-muted" title={who}>{who}</span>}
        <span className="text-ui-sm text-muted">{reach(login, sharedWith)}</span>
        {usageError && <span className="text-ui-sm text-muted">Usage not read: <span className="font-code">{usageError}</span></span>}
        {windows.length > 0 && (
          <div className="mt-1.5 max-w-sm">
            <UsageMeters label={`Login ${login.name}`} windows={windows} now={Date.now()} />
          </div>
        )}
      </div>
      <div className="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-1">
        <span className="flex items-center">
          <Button variant="ghost" size="icon-sm" label={`Move ${login.name} up`} disabled={busy || index === 0} onClick={() => onMove(-1)}>
            <ChevronUp />
          </Button>
          <Button variant="ghost" size="icon-sm" label={`Move ${login.name} down`} disabled={busy || index === count - 1} onClick={() => onMove(1)}>
            <ChevronDown />
          </Button>
        </span>
        <span className="flex min-h-7 items-center gap-2 coarse:min-h-11">
          <label htmlFor={`${id}-auto`} className="text-ui-sm text-text">Automatic</label>
          <Switch id={`${id}-auto`} checked={login.automatic} disabled={busy} onCheckedChange={onAutomatic} />
        </span>
        <span className="flex min-h-7 items-center gap-2 coarse:min-h-11">
          <label htmlFor={`${id}-share`} className="text-ui-sm text-text">Use for shares</label>
          <Switch id={`${id}-share`} checked={login.shared} disabled={busy} onCheckedChange={onShare} />
        </span>
        <span className="flex items-center gap-1">
          <Button variant="ghost" size="sm" disabled={busy || !added} hint={added ? undefined : defaultFixed} aria-label={`Rename ${login.name}`} onClick={onRename}>Rename</Button>
          <Button variant="ghost" size="icon-sm" label={added ? `Remove ${login.name}` : defaultFixed} disabled={busy || !added} onClick={onRemove}>
            <Trash2 />
          </Button>
        </span>
      </div>
    </li>
  )
}

/** The member's logins of Claude Code in priority order, each with its controls beside it. */
export function LoginList({ logins, sharedWith, client, onChanged }: {
  logins: AgentLogin[]
  sharedWith: Member[]
  client: LoginsClient
  onChanged: () => void
}) {
  const usage = useLoginUsage(client, logins)
  useClock()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [removing, setRemoving] = useState<AgentLogin | null>(null)
  const act = (call: Promise<unknown>) => {
    setBusy(true)
    setError(null)
    call.then(onChanged, (err: unknown) => setError(message(err))).finally(() => setBusy(false))
  }
  const move = (index: number, by: number) => {
    const ids = logins.map((login) => login.id)
    ;[ids[index], ids[index + by]] = [ids[index + by], ids[index]]
    act(client.agentLoginOrder(loginAgent, ids))
  }
  return (
    <section aria-label="Claude Code logins" className="flex min-w-0 basis-full flex-col gap-3 border-t border-seam pt-3 sm:pl-11">
      <p className="text-ui-sm text-muted">
        Sessions start on the first login with usage left and move down the list when one runs out. A login with Automatic off is used only when you pick it at launch.{' '}
        {sharedWith.length > 0
          ? `Your account is shared with ${names(sharedWith)}: logins on for shares serve their sessions too.`
          : 'Your account is not shared with anyone.'}
      </p>
      {error && <Callout tone="failed" role="alert"><span className="font-code">{error}</span></Callout>}
      <ol aria-label="Claude Code logins in priority order" className="flex flex-col divide-y divide-seam">
        {logins.map((login, index) => (
          <LoginRow
            key={login.id}
            login={login}
            index={index}
            count={logins.length}
            usage={usage[login.id]}
            sharedWith={sharedWith}
            busy={busy}
            onMove={(by) => move(index, by)}
            onAutomatic={(on) => act(client.agentLoginAutomatic(loginAgent, login.id, on))}
            onShare={(on) => act(client.agentLoginShare(loginAgent, login.id, on))}
            onRename={() => setRenaming(login.id)}
            onRemove={() => setRemoving(login)}
            renaming={renaming === login.id
              ? <RenameForm login={login} client={client} onCancel={() => setRenaming(null)} onDone={() => { setRenaming(null); onChanged() }} />
              : undefined}
          />
        ))}
      </ol>
      <AnthropicNote />
      {removing && <RemoveLogin login={removing} client={client} onRemoved={onChanged} onClose={() => setRemoving(null)} />}
    </section>
  )
}
