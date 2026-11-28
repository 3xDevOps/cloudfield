import { type ReactNode, useCallback, useEffect, useId, useRef, useState } from 'react'
import { ArrowDown, ArrowUp, Gauge, Pencil, Trash2 } from '@/components/icons'
import { PerUseBadge } from '@/components/per-use-badge'
import { Button } from '@/components/ui/button'
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
import { useMediaQuery } from '@/lib/hooks'
import { message } from '@/lib/format'
import type { AgentLogin, Member, UsageProvider } from '@/lib/types'
import { currentWindows, limitWords, UsageMeters } from '@/routes/settings/usage'
import { useStore } from '@/store'
import { stillWaiting, usePastDeadline } from '@/store/optimistic'

/** Only Claude Code has added logins. */
export const loginAgent = 'claude'

const loginsGuide = 'https://github.com/3xDevOps/cloudfield/blob/main/docs/agents.md#several-logins'

export type LoginsClient = Pick<Api, 'agentLoginRename' | 'agentLoginRemove' | 'agentLoginOrder' | 'agentLoginAutomatic' | 'agentLoginShare' | 'accountUsage'>

/** The signed-in member's logins: a read made for another identity never shows. */
export function useAgentLogins(client: Pick<Api, 'agentLoginList'>, enabled: boolean) {
  const identity = useStore((s) => s.identityKey)
  const [read, setRead] = useState<{ identity: string | null; logins: AgentLogin[] | null; error: string | null }>({ identity: null, logins: null, error: null })
  // Only the newest read lands, so an older answer arriving last never replaces it.
  const reads = useRef(0)
  // Each caller waits for the newest read, which says whether a list read since the call is now shown.
  const waiting = useRef<((shown: boolean) => void)[]>([])
  const reload = useCallback((): Promise<boolean> => {
    if (!enabled) return Promise.resolve(false)
    const read = ++reads.current
    const at = useStore.getState().identityKey
    const done = new Promise<boolean>((resolve) => waiting.current.push(resolve))
    const settle = (landed: boolean) => {
      for (const resolve of waiting.current.splice(0)) resolve(landed)
    }
    client.agentLoginList(loginAgent).then(
      (list) => {
        if (read !== reads.current) return
        setRead({ identity: at, logins: list, error: null })
        settle(true)
      },
      (err: unknown) => {
        if (read !== reads.current) return
        setRead((current) => ({ identity: at, logins: current.identity === at ? current.logins : null, error: message(err) }))
        settle(false)
      },
    )
    return done
  }, [client, enabled])
  useEffect(() => { void reload() }, [reload, identity])
  const shown = enabled && read.identity === identity
  return { logins: shown ? read.logins : null, error: shown ? read.error : null, reload }
}

export function kindWords(login: AgentLogin): string {
  if (login.kind === 'subscription') return login.plan ? `Subscription · ${login.plan}` : 'Subscription'
  if (login.kind === 'console') return 'Console'
  if (login.kind === 'api_key') return 'API key'
  return 'No login found'
}

export function names(members: Member[]): string {
  const list = members.map((member) => member.display_name)
  return list.length < 2 ? list.join('') : `${list.slice(0, -1).join(', ')} and ${list[list.length - 1]}`
}

/** Whom a login billed per use charges: its organisation where the sign-in named one, else the login. */
export function perUsePayer(login: AgentLogin): string {
  return login.organization_name || `login ${login.name}`
}

/** Whose sessions Use for shares lets run on a login, and for one billed per use who is charged. */
export function shareWords(login: AgentLogin, sharedWith: Member[] | null): string {
  const who = sharedWith?.length ? names(sharedWith) : 'teammates you share your account with'
  return `Sessions of ${who} can run here too${login.per_use ? `, charged per use to ${perUsePayer(login)}` : ''}.`
}

/**
 * What Use for shares does for a login beyond the switch's own state, or null: teammates never name a login, so only an automatic
 * one reaches them, and a default login kept as an API key or a Console profile has no file a share mounts. `sharedWith` is null until read.
 */
export function reach(login: AgentLogin, sharedWith: Member[] | null): string | null {
  if (!login.shared) return null
  if (login.id === 'default' && (login.kind === 'api_key' || login.kind === 'console')) return 'A share cannot use this kind of login.'
  if (!login.automatic) return 'No new teammate session starts or moves onto it while Automatic is off; one already on it keeps running.'
  return sharedWith?.length ? shareWords(login, sharedWith) : null
}

/** What Automatic does for a login, where it adds a fact: a login billed per use charges for the sessions that move to it. */
export function automaticWords(login: AgentLogin): string | null {
  return login.per_use ? `Sessions can move here when the automatic logins above it run out, charged per use to ${perUsePayer(login)}.` : null
}

/** A login's badge when it is billed per use, naming whom it charges. */
export function LoginPerUseBadge({ login }: { login: AgentLogin }) {
  return login.per_use ? <PerUseBadge title={`Billed per use to ${perUsePayer(login)}`} /> : null
}

function Pending({ label }: { label: string }) {
  const late = usePastDeadline(true)
  return <span role="status" className="text-ui-sm text-muted">{late ? stillWaiting : label}</span>
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

function RenameForm({ login, client, onDone, onFailed, onCancel }: { login: AgentLogin; client: LoginsClient; onDone: () => void; onFailed: () => void; onCancel: () => void }) {
  const [name, setName] = useState(login.name)
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy)
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
          onFailed()
        })
      }}
    >
      <label htmlFor={id} className="sr-only">New name for {login.name}</label>
      <span className="flex flex-wrap items-center gap-2">
        <Input id={id} value={name} autoFocus autoComplete="off" spellCheck={false} className="w-48 max-w-full" onChange={(event) => setName(event.target.value)} />
        <Button size="sm" type="submit" variant="secondary" disabled={busy || !name.trim()}>{late ? stillWaiting : busy ? 'Saving…' : 'Save'}</Button>
        <Button size="sm" type="button" variant="ghost" disabled={busy} onClick={onCancel}>Cancel</Button>
      </span>
      {error && <p role="alert" className="font-code text-ui-sm break-words text-state-failed">{error}</p>}
    </form>
  )
}

// The server names the caller's own sessions that hold a login after "your sessions", and points at the CLI to revoke a teammate's share.
const ownSessions = /your sessions (.*?) \(end them/
const runIDs = /\brun-[0-9a-z]{10}\b/g
const revokeCommand = 'cloudfield account revoke'
const teammateSessions = "of your teammates' sessions"

export function RemoveLogin({ login, client, onChanged, onClose }: { login: AgentLogin; client: LoginsClient; onChanged: () => void; onClose: () => void }) {
  const navigate = useStore((s) => s.navigate)
  const [busy, setBusy] = useState(false)
  const late = usePastDeadline(busy)
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
            {!login.shared && error.includes(teammateSessions) && (
              <p className="mb-1">Use for shares is off, so no teammate&apos;s session starts or moves onto this login now; one already on it holds it until it ends.</p>
            )}
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
          {error === null && <AlertDialogAction
            variant="danger"
            disabled={busy}
            onClick={(event) => {
              event.preventDefault()
              setBusy(true)
              setError(null)
              client.agentLoginRemove(loginAgent, login.id).then(
                () => {
                  onChanged()
                  onClose()
                },
                (err: unknown) => {
                  setBusy(false)
                  setError(message(err))
                  onChanged()
                },
              )
            }}
          >
            {late ? stillWaiting : busy ? 'Removing…' : 'Remove'}
          </AlertDialogAction>}
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

/** A login's switch; `detail` says what it does for this login. */
export function SwitchRow({ label, detail, checked, pending, onChange }: { label: string; detail?: string; checked: boolean; pending: boolean; onChange: (on: boolean) => void }) {
  const id = useId()
  return (
    <span className="flex min-h-7 items-center justify-between gap-3 coarse:min-h-11">
      <span className="flex min-w-0 flex-col">
        <label htmlFor={id} className="text-ui-sm text-text">{label}</label>
        {detail && <span id={`${id}-detail`} className="text-ui-sm text-muted">{detail}</span>}
      </span>
      <span className="flex shrink-0 items-center gap-2">
        {pending && <Pending label="Saving…" />}
        <Switch
          id={id}
          checked={checked}
          aria-disabled={pending || undefined}
          aria-describedby={detail ? `${id}-detail` : undefined}
          className="aria-disabled:cursor-not-allowed aria-disabled:opacity-50"
          onCheckedChange={(on) => { if (!pending) onChange(on) }}
        />
      </span>
    </span>
  )
}

/**
 * Pending login controls by `<control>:<login id>` (and `order` with the login it moves), and the last refusal.
 * A failure without an answer may hide a committed change, so every outcome reads the list again through `onChanged`.
 */
export function useLoginControls(onChanged: () => Promise<unknown>) {
  const [pending, setPending] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  const act = (key: string, value: string, call: Promise<unknown>, reread = onChanged) => {
    setPending((current) => ({ ...current, [key]: value }))
    setError(null)
    call.then(reread, (err: unknown) => {
      setError(message(err))
      return reread()
    }).finally(() => setPending(({ [key]: _, ...rest }) => rest))
  }
  return { pending, error, act }
}

type Control = 'automatic' | 'shared'

function LoginRow({ login, index, count, firstAutomatic, usage, sharedWith, ordering, moving, pending, onMove, onAutomatic, onShare, onRename, onRemove, renaming }: {
  login: AgentLogin
  index: number
  count: number
  /** This login is the first in the order with Automatic on. */
  firstAutomatic: boolean
  usage: Usage | undefined
  sharedWith: Member[] | null
  /** An order call or the read after it is pending, or that read failed: every arrow waits, since the next order is built from the list. */
  ordering: boolean
  /** That call moved this login. */
  moving: boolean
  pending: Partial<Record<Control, true>>
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
  // A failed refresh keeps the last windows read, which are not current.
  const staleError = windows.length > 0 && usage?.provider?.status === 'stale' ? usage.provider.error : undefined
  const added = login.id !== 'default'
  const who = [login.email, login.organization_name].filter(Boolean).join(' · ')
  const shares = reach(login, sharedWith)
  const automatic = login.automatic && !firstAutomatic ? automaticWords(login) : null
  // Below Tailwind's lg the switches stack under the facts, and each line sits under the switch it explains.
  const stacked = useMediaQuery('(max-width: 1023px)')
  return (
    <li aria-label={`Login ${login.name}`} className="flex gap-2 py-3 first:pt-0 last:pb-0">
      <div className="flex shrink-0 flex-col items-center gap-1">
        <span className="flex h-6 items-center text-ui-sm text-muted tabular-nums coarse:h-11">{index + 1}</span>
        <Button variant={index === 0 ? 'ghost' : 'secondary'} size="icon-sm" label={`Move ${login.name} up`} disabled={ordering || index === 0} onClick={() => onMove(-1)}>
          <ArrowUp />
        </Button>
        <Button variant={index === count - 1 ? 'ghost' : 'secondary'} size="icon-sm" label={`Move ${login.name} down`} disabled={ordering || index === count - 1} onClick={() => onMove(1)}>
          <ArrowDown />
        </Button>
      </div>
      <div className="grid min-w-0 flex-1 grid-cols-[minmax(0,1fr)_auto] items-start gap-x-4 gap-y-2 lg:grid-cols-[minmax(0,1fr)_auto_3rem]">
        <div className="col-start-1 row-start-1 flex min-h-6 min-w-0 items-center coarse:min-h-11">
          {renaming ?? (
            <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-0.5">
              <span className="min-w-0 truncate text-ui font-medium text-text">{login.name}</span>
              <span className="text-ui-sm text-muted">{kindWords(login)}{firstAutomatic && ' · tried first'}</span>
              <LoginPerUseBadge login={login} />
              {limit && <span className="inline-flex items-center gap-1 text-ui-sm text-state-needs-you"><Gauge aria-hidden className="size-3.5 shrink-0" />{limit}</span>}
            </span>
          )}
        </div>
        {added && !renaming && (
          <div className="col-start-2 row-start-1 flex items-center justify-end lg:col-start-3">
            <Button variant="ghost" size="icon-sm" label={`Rename ${login.name}`} onClick={onRename}>
              <Pencil />
            </Button>
            <Button variant="ghost" size="icon-sm" label={`Remove ${login.name}`} onClick={onRemove}>
              <Trash2 />
            </Button>
          </div>
        )}
        <div className="col-span-2 flex min-w-0 flex-col gap-0.5 lg:col-span-1 lg:col-start-1 lg:row-start-2">
          {moving && <Pending label="Moving…" />}
          {who && <span className="min-w-0 truncate text-ui-sm text-muted" title={who}>{who}</span>}
          {!stacked && shares && <span className="text-ui-sm text-muted">{shares}</span>}
          {!stacked && automatic && <span className="text-ui-sm text-muted">{automatic}</span>}
          {!added && (
            <span className="text-ui-sm text-muted">
              Your default login, the one <span className="font-code">cloudfield agent login claude</span> signs in. It cannot be renamed or removed.
            </span>
          )}
        </div>
        <div className="col-span-2 flex flex-col lg:col-span-1 lg:col-start-2 lg:row-span-3 lg:row-start-1">
          <SwitchRow label="Automatic" detail={stacked ? automatic ?? undefined : undefined} checked={login.automatic} pending={!!pending.automatic} onChange={onAutomatic} />
          <SwitchRow label="Use for shares" detail={stacked ? shares ?? undefined : undefined} checked={login.shared} pending={!!pending.shared} onChange={onShare} />
        </div>
        {(perUseWindows || usageError || windows.length > 0) && (
          <div className="col-span-2 min-w-0 lg:col-span-1 lg:col-start-1 lg:row-start-3">
            {perUseWindows && <span className="text-ui-sm text-muted">No usage windows</span>}
            {usageError && <span className="text-ui-sm text-muted">Usage not read: <span className="font-code">{usageError}</span></span>}
            {staleError && <span className="text-ui-sm text-muted">Last read, not current: <span className="font-code">{staleError}</span></span>}
            {windows.length > 0 && (
              <div className="max-w-sm">
                <UsageMeters label={`Login ${login.name}`} windows={windows} now={now} days />
              </div>
            )}
          </div>
        )}
      </div>
    </li>
  )
}

/** The member's logins of Claude Code in priority order, each with its controls beside it. */
export function LoginList({ logins, sharedWith, sharedError, client, onChanged }: {
  logins: AgentLogin[]
  /** Whom the account is shared with; null until `account.list` answers. */
  sharedWith: Member[] | null
  sharedError?: string | null
  client: LoginsClient
  /** Reads the list again, resolving true once a list read since is shown; a pending control waits for it, so it shows what the server keeps. */
  onChanged: () => Promise<boolean>
}) {
  const usage = useLoginUsage(client, logins)
  useClock()
  const { pending, error, act } = useLoginControls(onChanged)
  const [renaming, setRenaming] = useState<string | null>(null)
  const [removing, setRemoving] = useState<AgentLogin | null>(null)
  // The read after an order call failed, so the order shown may not be the server's until another list lands.
  const [unread, setUnread] = useState(false)
  const [shown, setShown] = useState(logins)
  if (shown !== logins) {
    setShown(logins)
    setUnread(false)
  }
  const move = (index: number, by: number) => {
    const ids = logins.map((login) => login.id)
    ;[ids[index], ids[index + by]] = [ids[index + by], ids[index]]
    act('order', logins[index]!.id, client.agentLoginOrder(loginAgent, ids), () => onChanged().then((landed) => setUnread(!landed)))
  }
  const firstAutomatic = logins.find((login) => login.automatic)?.id
  // The launch dialog offers no pick with one login, so a lone login with Automatic off starts nothing.
  const lone = logins.length === 1 ? logins[0] : undefined
  return (
    <section aria-label="Claude Code logins" className="flex min-w-0 basis-full flex-col gap-3 border-t border-seam pt-3 sm:pl-11">
      <p className="text-ui-sm text-muted">
        {lone && !lone.automatic
          ? <>Your only login has Automatic off, so a session that names no login is refused until you turn Automatic on, here or with <span className="font-code">cloudfield agent logins auto {lone.name} on</span>.</>
          : `Sessions start on the first automatic login in your order with usage left, and move to the first other one with usage left when it runs out.${lone ? '' : ' A login with Automatic off is used only when you pick it at launch.'}`}
        {sharedWith && (sharedWith.length > 0
          ? ` Your account is shared with ${names(sharedWith)}: logins on for shares serve their sessions too.`
          : ' Your account is not shared with anyone.')}
        {!sharedWith && sharedError && <> Whom your account is shared with was not read: <span className="font-code">{sharedError}</span></>}
        {' '}<a href={loginsGuide} target="_blank" rel="noreferrer" className="text-accent underline-offset-2 hover:underline">How logins work</a>
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
            ordering={'order' in pending || unread}
            moving={pending.order === login.id}
            pending={{ ...(`automatic:${login.id}` in pending && { automatic: true }), ...(`shared:${login.id}` in pending && { shared: true }) }}
            onMove={(by) => move(index, by)}
            onAutomatic={(on) => act(`automatic:${login.id}`, '', client.agentLoginAutomatic(loginAgent, login.id, on))}
            onShare={(on) => act(`shared:${login.id}`, '', client.agentLoginShare(loginAgent, login.id, on))}
            onRename={() => setRenaming(login.id)}
            onRemove={() => setRemoving(login)}
            renaming={renaming === login.id
              ? <RenameForm login={login} client={client} onCancel={() => setRenaming(null)} onFailed={() => void onChanged()} onDone={() => { setRenaming(null); void onChanged() }} />
              : undefined}
          />
        ))}
      </ol>
      {removing && (
        <RemoveLogin
          login={removing}
          client={client}
          onChanged={() => void onChanged()}
          onClose={() => setRemoving(null)}
        />
      )}
    </section>
  )
}
