import { type ReactNode, useCallback, useEffect, useId, useRef, useState } from 'react'
import { ArrowDown, ArrowUp, Gauge, Pencil, Trash2 } from '@/components/icons'
import { PerUseBadge } from '@/components/per-use-badge'
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
import { currentWindows, limitWords, UsageMeters } from '@/routes/settings/usage'
import { useStore } from '@/store'

/** Only Claude Code has added logins. */
export const loginAgent = 'claude'

export type LoginsClient = Pick<Api, 'agentLoginRename' | 'agentLoginRemove' | 'agentLoginOrder' | 'agentLoginAutomatic' | 'agentLoginShare' | 'accountUsage'>

export function useAgentLogins(client: Pick<Api, 'agentLoginList'>, enabled: boolean) {
  const [logins, setLogins] = useState<AgentLogin[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  // Only the newest read lands, so an older answer arriving last never replaces it.
  const reads = useRef(0)
  const reload = useCallback((): Promise<void> => {
    if (!enabled) return Promise.resolve()
    const read = ++reads.current
    return client.agentLoginList(loginAgent).then(
      (list) => {
        if (read !== reads.current) return
        setLogins(list)
        setError(null)
      },
      (err: unknown) => {
        if (read === reads.current) setError(message(err))
      },
    )
  }, [client, enabled])
  useEffect(() => { void reload() }, [reload])
  return { logins: enabled ? logins : null, error: enabled ? error : null, reload }
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

/** The Use for shares switch's state, and whom it reaches: teammates never name a login, so only an automatic one reaches them. */
function reach(login: AgentLogin, sharedWith: Member[]): string {
  if (!login.shared) return 'Off for shares'
  if (sharedWith.length === 0) return 'On for shares: your account is not shared'
  if (!login.automatic) return 'On for shares: no share reaches it while Automatic is off'
  return `On for shares: also serves ${names(sharedWith)}`
}

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

// The server names the caller's own sessions that hold a login after "your sessions", and points at the CLI to revoke a teammate's share.
const ownSessions = /your sessions (.*?) \(end them\)/
const runIDs = /\brun-[0-9a-z]{10}\b/g
const revokeCommand = 'cloudfield account revoke'

export function RemoveLogin({ login, client, onRemoved, onClose }: { login: AgentLogin; client: LoginsClient; onRemoved: () => void; onClose: () => void }) {
  const navigate = useStore((s) => s.navigate)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const go = (...route: Parameters<typeof navigate>) => {
    onClose()
    navigate(...route)
  }
  const runs = [...new Set(ownSessions.exec(error ?? '')?.[1].match(runIDs))]
  const who = [login.email, login.organization_name].filter(Boolean).join(' · ')
  return (
    <AlertDialog open onOpenChange={() => !busy && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle className="break-words">Remove the login “{login.name}”?</AlertDialogTitle>
          <AlertDialogDescription className="break-words">
            {who && <>Signed in as {who}. </>}Deletes this login&apos;s files from the server, so no session starts or moves onto it again. It does not sign the login out at Anthropic: its sign-in stays valid there until Anthropic expires it or you revoke it.
          </AlertDialogDescription>
        </AlertDialogHeader>
        {error !== null && (
          <Callout tone="failed" role="alert" title="Not removed">
            <span className="font-code">{error}</span>
            {(runs.length > 0 || error.includes(revokeCommand)) && (
              <span className="mt-1 flex flex-wrap gap-x-3">
                {runs.map((id) => <Button key={id} variant="link" className="text-ui-sm" onClick={() => go('run', { runId: id })}>Open {id}</Button>)}
                {error.includes(revokeCommand) && <Button variant="link" className="text-ui-sm" onClick={() => go('members')}>Revoke a share on Members</Button>}
              </span>
            )}
          </Callout>
        )}
        <AlertDialogFooter>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            variant={error === null ? 'danger' : 'secondary'}
            disabled={busy || error !== null}
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

function SwitchRow({ label, checked, disabled, onChange }: { label: string; checked: boolean; disabled: boolean; onChange: (on: boolean) => void }) {
  const id = useId()
  return (
    <span className="flex min-h-7 items-center justify-between gap-3 coarse:min-h-11">
      <label htmlFor={id} className="text-ui-sm text-text">{label}</label>
      <Switch id={id} checked={checked} disabled={disabled} onCheckedChange={onChange} />
    </span>
  )
}

function LoginRow({ login, index, count, firstAutomatic, usage, sharedWith, busy, onMove, onAutomatic, onShare, onRename, onRemove, renaming }: {
  login: AgentLogin
  index: number
  count: number
  /** This login is the first in the order with Automatic on. */
  firstAutomatic: boolean
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
  const now = Date.now()
  const windows = currentWindows(usage?.provider, now)
  const limit = limitWords(usage?.provider, now)
  // A Console login has no usage windows by design; the server says so with `unsupported`.
  const perUseWindows = login.per_use && !usage?.error && usage?.provider?.status === 'unsupported'
  const usageError = perUseWindows ? undefined : usage?.error ?? (windows.length === 0 ? usage?.provider?.error : undefined)
  const added = login.id !== 'default'
  const who = [login.email, login.organization_name].filter(Boolean).join(' · ')
  return (
    <li aria-label={`Login ${login.name}`} className="flex gap-2 py-3 first:pt-0 last:pb-0">
      <div className="flex shrink-0 flex-col items-center gap-1">
        <span className="flex h-6 items-center text-ui-sm text-muted tabular-nums coarse:h-11">{index + 1}</span>
        <Button variant="secondary" size="icon-sm" label={`Move ${login.name} up`} disabled={busy || index === 0} onClick={() => onMove(-1)}>
          <ArrowUp />
        </Button>
        <Button variant="secondary" size="icon-sm" label={`Move ${login.name} down`} disabled={busy || index === count - 1} onClick={() => onMove(1)}>
          <ArrowDown />
        </Button>
      </div>
      <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-2 lg:grid-cols-[minmax(0,1fr)_auto_3rem]">
        <div className="col-start-1 row-start-1 flex min-h-6 min-w-0 items-center coarse:min-h-11">
          {renaming ?? (
            <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
              <span className="min-w-0 truncate text-ui font-medium text-text">{login.name}</span>
              <span className="text-ui-sm text-muted">{kindWords(login)}</span>
              {firstAutomatic && <Badge>Tried first</Badge>}
              {login.per_use && <PerUseBadge />}
              {limit && <span className="inline-flex items-center gap-1 text-ui-sm text-state-needs-you"><Gauge aria-hidden className="size-3.5 shrink-0" />{limit}</span>}
            </span>
          )}
        </div>
        {added && !renaming && (
          <div className="col-start-2 row-start-1 flex items-center justify-end lg:col-start-3">
            <Button variant="ghost" size="icon-sm" label={`Rename ${login.name}`} disabled={busy} onClick={onRename}>
              <Pencil />
            </Button>
            <Button variant="ghost" size="icon-sm" label={`Remove ${login.name}`} disabled={busy} onClick={onRemove}>
              <Trash2 />
            </Button>
          </div>
        )}
        <div className="col-span-2 flex min-w-0 flex-col gap-0.5 lg:col-span-1 lg:col-start-1 lg:row-start-2">
          {who && <span className="min-w-0 truncate text-ui-sm text-muted" title={who}>{who}</span>}
          <span className="text-ui-sm text-muted">{reach(login, sharedWith)}</span>
          {login.per_use && login.automatic && !firstAutomatic && (
            <span className="text-ui-sm text-muted">Automatic sessions can move here when the logins above it run out.</span>
          )}
          {!added && (
            <span className="text-ui-sm text-muted">
              Your default login, the one <span className="font-code">cloudfield agent login claude</span> signs in. It keeps its name and is not removed here.
            </span>
          )}
        </div>
        <div className="col-span-2 flex flex-col lg:col-span-1 lg:col-start-2 lg:row-span-3 lg:row-start-1">
          <SwitchRow label="Automatic" checked={login.automatic} disabled={busy} onChange={onAutomatic} />
          <SwitchRow label="Use for shares" checked={login.shared} disabled={busy} onChange={onShare} />
        </div>
        {(perUseWindows || usageError || windows.length > 0) && (
          <div className="col-span-2 min-w-0 lg:col-span-1 lg:col-start-1 lg:row-start-3">
            {perUseWindows && <span className="text-ui-sm text-muted">No usage windows: billed per use</span>}
            {usageError && <span className="text-ui-sm text-muted">Usage not read: <span className="font-code">{usageError}</span></span>}
            {windows.length > 0 && (
              <div className="max-w-sm">
                <UsageMeters label={`Login ${login.name}`} windows={windows} now={now} />
              </div>
            )}
          </div>
        )}
      </div>
    </li>
  )
}

/** The member's logins of Claude Code in priority order, each with its controls beside it. */
export function LoginList({ logins, sharedWith, client, onChanged }: {
  logins: AgentLogin[]
  sharedWith: Member[]
  client: LoginsClient
  /** Reads the list again; the controls wait for it, so the next move starts from the order the server keeps. */
  onChanged: () => Promise<void>
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
    call.then(onChanged, (err: unknown) => {
      setError(message(err))
      return onChanged()
    }).finally(() => setBusy(false))
  }
  const move = (index: number, by: number) => {
    const ids = logins.map((login) => login.id)
    ;[ids[index], ids[index + by]] = [ids[index + by], ids[index]]
    act(client.agentLoginOrder(loginAgent, ids))
  }
  const firstAutomatic = logins.find((login) => login.automatic)?.id
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
            firstAutomatic={login.id === firstAutomatic}
            usage={usage[login.id]}
            sharedWith={sharedWith}
            busy={busy}
            onMove={(by) => move(index, by)}
            onAutomatic={(on) => act(client.agentLoginAutomatic(loginAgent, login.id, on))}
            onShare={(on) => act(client.agentLoginShare(loginAgent, login.id, on))}
            onRename={() => setRenaming(login.id)}
            onRemove={() => setRemoving(login)}
            renaming={renaming === login.id
              ? <RenameForm login={login} client={client} onCancel={() => setRenaming(null)} onDone={() => { setRenaming(null); void onChanged() }} />
              : undefined}
          />
        ))}
      </ol>
      {removing && <RemoveLogin login={removing} client={client} onRemoved={onChanged} onClose={() => setRemoving(null)} />}
    </section>
  )
}
