// The local gateway runs the edge device flow and keeps the device token;
// this screen never sees it.

import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { toast } from 'sonner'
import { CopyableCommand } from '@/components/copyable-command'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { FormField } from '@/components/ui/form-field'
import { Input } from '@/components/ui/input'
import { InsetList, InsetListItem } from '@/components/ui/inset-list'
import { StatusDot } from '@/components/ui/status-dot'
import { ApiError, type Api } from '@/lib/api'
import { parseClaim, parseJoin } from '@/lib/claim-link'
import { edgeHost, message, providerName } from '@/lib/format'
import { isApprovalCode, shellQuote } from '@/lib/shell'
import type { EdgeAccount, EdgeLinkResult, EdgeLogin, EdgeStatusNotice, EdgeRefusal, EdgeServer } from '@/lib/types'
import type { HandedLink } from '@/lib/url-state'
import { useStore } from '@/store'
import type { Capability } from '@/lib/capability'

const pollMs = 2000

type Phase =
  | { name: 'checking' }
  | { name: 'signed-out' }
  | { name: 'waiting'; login: EdgeLogin }
  | { name: 'signed-in'; edges: SignedInEdge[]; chosen: string | null }

type SignedInEdge = { edge: string; account: EdgeAccount; retiring?: EdgeStatusNotice }

// Pre-wrapped: a server's refusal lists the commands that fix it, one per line.
function ErrorLine({ children }: { children: string }) {
  return <Callout tone="failed" role="alert" className="self-stretch whitespace-pre-wrap">{children}</Callout>
}

type SignIn = {
  phase: Phase
  setPhase: (phase: Phase) => void
  error: string | null
  starting: boolean
  /** Starts a sign-in at `target`, else at the hook's edge. */
  signIn: (target?: string) => Promise<void>
}

/** The gateway's edge sign-in as edge.status reports it, started here and
 * polled until it ends. With `edge`, only that edge counts. */
function useEdgeSignIn(client: Api, edge?: string): SignIn {
  const [phase, setPhase] = useState<Phase>({ name: 'checking' })
  const [error, setError] = useState<string | null>(null)
  const [starting, setStarting] = useState(false)

  useEffect(() => {
    let ignore = false
    setPhase({ name: 'checking' })
    setError(null)
    void (async () => {
      try {
        const status = await client.localEdgeStatus()
        if (ignore) return
        const login = !edge || status.login?.edge === edge ? status.login : undefined
        if (login?.state === 'pending') {
          setPhase({ name: 'waiting', login })
          return
        }
        if (login?.state === 'failed' && login.error) setError(login.error)
        const edges = status.edges.filter((e) => !edge || e.edge === edge)
        const signedIn = edges.flatMap((e) => (e.account ? [{ edge: e.edge, account: e.account, retiring: e.retiring }] : []))
        if (signedIn.length > 0) {
          setPhase({ name: 'signed-in', edges: signedIn, chosen: signedIn.length === 1 ? signedIn[0].edge : null })
          return
        }
        const unreadable = edges.find((e) => e.error)?.error
        if (unreadable) setError(unreadable)
        setPhase({ name: 'signed-out' })
      } catch (err) {
        if (ignore) return
        setError(message(err))
        setPhase({ name: 'signed-out' })
      }
    })()
    return () => {
      ignore = true
    }
  }, [client, edge])

  const waitingEdge = phase.name === 'waiting' ? phase.login.edge : null
  const waitingCode = phase.name === 'waiting' ? phase.login.user_code : null
  useEffect(() => {
    if (!waitingEdge) return
    let live = true
    let timer: ReturnType<typeof setTimeout>
    const poll = () => {
      timer = setTimeout(() => {
        client.localEdgeStatus().then(
          (status) => {
            if (!live) return
            const login = status.login
            if (login?.edge !== waitingEdge || login.user_code !== waitingCode) {
              setError('Sign-in was replaced by another window. Try again.')
              setPhase({ name: 'signed-out' })
            } else if (login.state === 'signed_in' && login.account) {
              setError(null)
              setPhase({
                name: 'signed-in',
                edges: [{ edge: login.edge, account: login.account }],
                chosen: login.edge,
              })
            } else if (login.state === 'failed') {
              setError(login.error ?? 'sign in failed')
              setPhase({ name: 'signed-out' })
            } else {
              poll()
            }
          },
          (err: unknown) => {
            if (!live) return
            // The gateway is local; keep waiting, and say what it answered.
            setError(message(err))
            poll()
          },
        )
      }, pollMs)
    }
    poll()
    return () => {
      live = false
      clearTimeout(timer)
    }
  }, [client, waitingEdge, waitingCode])

  const signIn = async (target?: string) => {
    setStarting(true)
    setError(null)
    // Opened before the await so it is not blocked as a popup. The desktop
    // shell refuses blank windows and opens the address in the system browser.
    const popup = window.open('about:blank', '_blank')
    if (popup) popup.opener = null
    try {
      const login = await client.localEdgeLogin(target ?? edge)
      if (popup) popup.location.replace(login.verification_uri)
      else window.open(login.verification_uri, '_blank', 'noopener,noreferrer')
      setPhase({ name: 'waiting', login })
    } catch (err) {
      popup?.close()
      setError(message(err))
    } finally {
      setStarting(false)
    }
  }

  return { phase, setPhase, error, starting, signIn }
}

function SignInProgress({ signIn: { phase, error, starting, signIn } }: { signIn: SignIn }) {
  return (
    <>
      {error && <ErrorLine>{error}</ErrorLine>}
      {phase.name === 'signed-out' && (
        <Button size="sm" disabled={starting} onClick={() => void signIn()}>
          {starting ? 'Signing in…' : 'Sign in with GitHub'}
        </Button>
      )}
      {phase.name === 'waiting' && (
        <div className="flex min-w-0 flex-col gap-2">
          <p>
            Open{' '}
            <a
              href={phase.login.verification_uri}
              target="_blank"
              rel="noopener noreferrer"
              className="break-all font-code text-accent underline underline-offset-2"
            >
              {phase.login.verification_uri}
            </a>{' '}
            and confirm this code:
          </p>
          <p className="font-code text-title tracking-wider select-all">
            {phase.login.user_code}
          </p>
          {edgeHost(phase.login.signin_origin) !== edgeHost(phase.login.edge) && (
            <p className="text-muted">
              <span className="font-code">{edgeHost(phase.login.signin_origin)}</span> signs you in
              for the edge <span className="font-code">{edgeHost(phase.login.edge)}</span>.
            </p>
          )}
          <p role="status" className="text-muted">
            Waiting for you to confirm the code in the browser…
          </p>
        </div>
      )}
    </>
  )
}

export function LinkBeforeSignIn({ onHand }: { onHand: (link: HandedLink) => void }) {
  const [text, setText] = useState('')
  const [error, setError] = useState<string | null>(null)
  return (
    <form
      className="flex min-w-0 max-w-sm flex-col items-start gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        const link = text.trim()
        if (parseJoin(link)) onHand({ kind: 'join', link })
        else if (parseClaim(link)) onHand({ kind: 'claim', link })
        else setError('That is not an invitation or claim link.')
      }}
    >
      <FormField
        className="self-stretch"
        label="Have an invitation or claim link?"
        error={error ?? undefined}
      >
        <Input className="font-code" autoComplete="off" value={text} onChange={(e) => { setText(e.target.value); setError(null) }} />
      </FormField>
      <Button type="submit" size="sm" variant="secondary" disabled={!text.trim()}>
        Open link
      </Button>
    </form>
  )
}

export function EdgeSignIn({
  client,
  claim,
  onHand,
  onLinked,
  onSignedOut,
}: {
  client: Api
  /** A claim link handed to the dashboard, filled into the claim form. The
   * edge it names is the only one signed in to. */
  claim?: string
  /** Called with a join link, or a claim link naming another edge, pasted
   * into the claim form. */
  onHand: (link: HandedLink) => void
  onLinked: (result: EdgeLinkResult) => void
  onSignedOut: (signedOut: boolean) => void
}) {
  const claimEdge = claim === undefined ? undefined : parseClaim(claim)?.edge
  const signIn = useEdgeSignIn(client, claimEdge)
  const { phase, setPhase } = signIn
  const signedIn = phase.name === 'signed-in' ? phase.edges : []
  const picked = phase.name === 'signed-in' ? phase.edges.find((e) => e.edge === phase.chosen) : undefined
  const signedOut = phase.name === 'signed-out'
  useEffect(() => {
    onSignedOut(signedOut)
    return () => onSignedOut(false)
  }, [signedOut, onSignedOut])

  return (
    <section aria-label="Sign in" className="flex min-w-0 flex-col items-start gap-3 text-ui">
      {phase.name !== 'signed-in' && (
        <p className="text-text">
          {claim !== undefined ? (
            <>
              Claim link received. Sign in{claimEdge && <> to <span className="font-code">{linkHost(claimEdge)}</span></>} with
              GitHub to claim the server.
            </>
          ) : (
            <>
              This computer and the server both connect out to Cloudfield&apos;s relay, the edge, so neither needs an open
              port. Sign in with GitHub to see the servers your account reaches.
            </>
          )}
        </p>
      )}
      <SignInProgress signIn={signIn} />
      {phase.name === 'signed-in' && (
        <EdgeChoice
          edges={phase.edges}
          chosen={phase.chosen}
          onChoose={(edge) => setPhase({ ...phase, chosen: edge })}
        />
      )}
      {phase.name === 'signed-in' &&
        phase.edges.map((e) => e.edge !== picked?.edge && retiringNotice(e, phase.edges, signIn))}
      {picked && (
        <ServerPicker
          key={`${picked.edge} ${claim ?? ''}`}
          client={client}
          edge={picked.edge}
          account={picked.account}
          notice={retiringNotice(picked, signedIn, signIn)}
          retired={picked.retiring?.state === 'retired'}
          claim={claim}
          onHand={onHand}
          onLinked={onLinked}
        />
      )}
    </section>
  )
}

const joinRetryMs = 5000

/** A join link handed to the dashboard. Without the gateway's edge.join, as
 * on a dashboard served over a tailnet, it shows the command instead. */
export function JoinLink({
  client,
  caps,
  link,
  onLinked,
}: {
  client: Api
  caps: Capability
  link: string
  onLinked: (result: EdgeLinkResult) => void
}) {
  const join = parseJoin(link)
  if (!join) return <ErrorLine>{`${link} is not a join link`}</ErrorLine>
  if (!caps.hasLocal('edge.join')) return <LinkByCommand link={link} command={`aether link ${shellQuote(link)}`} />
  return <JoinPanel client={client} link={link} join={join} onLinked={onLinked} />
}

export function LinkByCommand({ link, command }: { link: string; command: string }) {
  return (
    <section aria-label="Link from a terminal" className="flex min-w-0 flex-col gap-2 text-ui">
      <p>This dashboard cannot link a server itself. Open this link in the Cloudfield desktop app:</p>
      <p className="break-all font-code select-all">{link}</p>
      <p>or run on the computer to link:</p>
      <CopyableCommand command={command} />
    </section>
  )
}

function JoinPanel({
  client,
  link,
  join,
  onLinked,
}: {
  client: Api
  link: string
  join: { edge: string; server: string }
  onLinked: (result: EdgeLinkResult) => void
}) {
  const signIn = useEdgeSignIn(client, join.edge)
  const { phase, setPhase } = signIn
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState<{ refusal: EdgeRefusal; text: string } | null>(null)
  const [resign, setResign] = useState<string | null>(null)
  const live = useRef(true)
  const retry = useRef<ReturnType<typeof setTimeout>>(undefined)

  useEffect(() => {
    live.current = true
    return () => {
      live.current = false
      clearTimeout(retry.current)
    }
  }, [])

  useEffect(() => {
    if (phase.name === 'waiting') setResign(null)
  }, [phase.name])

  const attempt = async () => {
    setBusy(true)
    setError(null)
    setResign(null)
    try {
      const result = await client.localEdgeJoin(link)
      if (result.remote_error) toast.error(result.remote_error)
      // The gateway now serves the joined server, so the dashboard follows
      // it even after this panel is gone.
      if (live.current) onLinked(result)
      else useStore.getState().reconnect()
    } catch (err) {
      if (!live.current) return
      const refusal = err instanceof ApiError ? err.data?.refusal : undefined
      if (refusal?.kind === 'device-pending') {
        setPending({ refusal, text: message(err) })
        retry.current = setTimeout(() => void attempt(), joinRetryMs)
        return
      }
      setPending(null)
      if (refusal?.kind === 'join-wrong' && refusal.user && refusal.account) setResign('Sign in with another account')
      if (refusal?.kind === 'identity-stale') setResign('Sign in again')
      if (refusal?.kind === 'not-signed-in' || refusal?.kind === 'token-revoked') setPhase({ name: 'signed-out' })
      setError(message(err))
    } finally {
      if (live.current) setBusy(false)
    }
  }

  const account = phase.name === 'signed-in' ? phase.edges[0]?.account : undefined

  if (pending) {
    return (
      <section aria-label="Join" role="status" className="flex min-w-0 flex-col gap-2 text-ui">
        {isApprovalCode(pending.refusal.approval_code) ? (
          <p className="whitespace-pre-wrap">{pending.text}</p>
        ) : (
          <>
            <p className="font-medium">
              Waiting for an admin of {pending.refusal.server ?? join.server} to approve this device
            </p>
            <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm">
              <dt className="text-muted">Device</dt>
              <dd className="min-w-0 break-all">{pending.refusal.device}</dd>
              <dt className="text-muted">Device key</dt>
              <dd className="min-w-0 break-all font-code">{pending.refusal.fingerprint}</dd>
            </dl>
          </>
        )}
        <p className="text-muted">This page asks again every {joinRetryMs / 1000} seconds.</p>
      </section>
    )
  }

  return (
    <section aria-label="Join" className="flex min-w-0 flex-col items-start gap-3 text-ui">
      <p className="font-medium">
        You are invited to the server <span className="font-code">{join.server}</span>.
      </p>
      {error && <ErrorLine>{error}</ErrorLine>}
      {account && resign ? (
        <>
          <SignInProgress signIn={signIn} />
          <Button size="sm" disabled={signIn.starting} onClick={() => void signIn.signIn()}>
            {signIn.starting ? 'Signing in…' : resign}
          </Button>
        </>
      ) : account ? (
        <>
          <p>
            This device is signed in to <span className="font-code">{edgeHost(join.edge)}</span> as{' '}
            <span className="font-medium">{accountName(account)}</span>. The server admits only the account the
            invitation names.
          </p>
          <Button size="sm" disabled={busy} onClick={() => void attempt()}>
            {busy ? 'Joining…' : 'Join'}
          </Button>
        </>
      ) : (
        phase.name !== 'checking' && (
          <>
            <p>
              Sign in to <span className="font-code">{edgeHost(join.edge)}</span> with the GitHub account your admin
              invited.
            </p>
            <SignInProgress signIn={signIn} />
          </>
        )
      )}
    </section>
  )
}

/** An edge origin in the form edgeclient.Canonical gives it, null for
 * anything else. It compares as the gateway does, so https://127.1 and
 * https://127.0.0.1 stay two addresses. */
function canonicalEdge(origin: string): string | null {
  const m = /^(https?):\/\/([^/?#@\s]+)\/?$/.exec(origin)
  if (!m) return null
  const host = m[2].toLowerCase()
  return m[1] === 'https' ? `https://${host.replace(/:443$/, '')}` : `http://${host}`
}

function retiringNotice(e: SignedInEdge, edges: SignedInEdge[], signIn: SignIn) {
  if (!e.retiring) return null
  const replacement = canonicalEdge(e.retiring.replacement)
  const there = edges.find((o) => replacement !== null && canonicalEdge(o.edge) === replacement)
  return <RetiringNotice key={e.edge} edge={e.edge} notice={e.retiring} signedInThere={there?.edge} signIn={signIn} />
}

/** What an edge address says of itself. Nothing authenticates it, so the
 * sign-in field beside it is filled only with an address this computer
 * already knows, or the one the linked server's verified notice names;
 * signing in stays the person's step. */
export function RetiringNotice({
  edge,
  notice,
  signedInThere,
  signIn,
}: {
  edge: string
  notice: EdgeStatusNotice
  /** The signed-in edge at the replacement, when there is one. */
  signedInThere?: string
  signIn: SignIn
}) {
  const server = useStore((s) => s.edge)
  const own = server?.notice
  const verified =
    own && !own.first_use && canonicalEdge(server.edge_url) === canonicalEdge(edge) &&
    canonicalEdge(own.replacement) !== null && canonicalEdge(own.replacement) === canonicalEdge(notice.replacement)
      ? own.replacement
      : null
  const trusted = notice.known ? notice.replacement : verified
  // What the person typed belongs to the notice and the trust it was typed under.
  const scope = `${edge} ${notice.state} ${notice.replacement} ${trusted ?? ''}`
  const [edit, setEdit] = useState<{ scope: string; value: string } | null>(null)
  const target = edit?.scope === scope ? edit.value : (trusted ?? '')
  return (
    <div className="mb-1 flex min-w-0 flex-col gap-2 self-stretch">
      <Callout
        tone={notice.state === 'retired' ? 'failed' : 'needs-you'}
        className="self-stretch"
        title={
          <>
            <span className="font-code">{edgeHost(edge)}</span> says it{' '}
            {notice.state === 'retired' ? 'is retired' : 'is retiring'}
            {notice.state === 'retiring' && notice.date && (
              <>
                {' '}on <span className="whitespace-nowrap">{notice.date}</span>
              </>
            )}
          </>
        }
      >
        <p>
          It says its new address is <span className="break-words font-code">{notice.replacement}</span>
        </p>
        <p className="text-muted">
          {signedInThere ? (
            <>
              You are signed in to <span className="font-code">{edgeHost(signedInThere)}</span> too: choose it above
              and link your server again through it.
            </>
          ) : trusted ? (
            'Sign in at the new address below, then link your server again there.'
          ) : (
            'Confirm that address with whoever runs the edge before you sign in there below.'
          )}
        </p>
      </Callout>
      {!signedInThere && (
        <form
          aria-label="Sign in to another edge"
          className="flex min-w-0 max-w-md flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault()
            void signIn.signIn(target.trim())
          }}
        >
          <FormField className="min-w-0 flex-1 basis-56" label="Sign in to another edge address">
            <Input
              className="font-code"
              autoComplete="off"
              value={target}
              onChange={(e) => setEdit({ scope, value: e.target.value })}
            />
          </FormField>
          <Button type="submit" variant="secondary" disabled={!target.trim() || signIn.starting}>
            {signIn.starting ? 'Signing in…' : 'Sign in'}
          </Button>
        </form>
      )}
    </div>
  )
}

function accountName(account: EdgeAccount): string {
  const name = account.login ?? account.email ?? account.name ?? account.subject
  const provider = providerName[account.provider] ?? account.provider
  return `${name} (${provider})`
}

function EdgeChoice({
  edges,
  chosen,
  onChoose,
}: {
  edges: SignedInEdge[]
  chosen: string | null
  onChoose: (edge: string) => void
}) {
  if (edges.length < 2) return null
  return (
    <fieldset className="flex min-w-0 flex-col gap-1.5">
      <legend className="mb-1.5">
        You are signed in to {edges.length} edges. Link through:
      </legend>
      {edges.map((e) => (
        <label key={e.edge} className="flex min-w-0 items-center gap-2">
          <input
            type="radio"
            name="edge"
            checked={chosen === e.edge}
            onChange={() => onChoose(e.edge)}
          />
          <span className="min-w-0 break-all">
            <span className="font-code">{edgeHost(e.edge)}</span> as {accountName(e.account)}
          </span>
        </label>
      ))}
    </fieldset>
  )
}

const policyLine: Record<EdgeServer['access_policy'], string> = {
  account: 'signing in is enough',
  'approved-devices': 'a new device waits for approval',
}

function ServerPicker({
  client,
  edge,
  account,
  notice,
  retired,
  claim,
  onHand,
  onLinked,
}: {
  client: Api
  edge: string
  account: EdgeAccount
  /** The signed-in edge's retiring notice, shown under the line that names it. */
  notice: ReactNode
  /** The edge address is retired: its refusals do not change on a retry. */
  retired: boolean
  claim?: string
  onHand: (link: HandedLink) => void
  onLinked: (result: EdgeLinkResult) => void
}) {
  const [servers, setServers] = useState<EdgeServer[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [code, setCode] = useState(claim ?? '')
  const [serverID, setServerID] = useState('')
  const [confirming, setConfirming] = useState<EdgeServer | null>(null)

  const load = useCallback(async () => {
    setError(null)
    try {
      setServers((await client.localEdgeServers(edge)).servers)
    } catch (err) {
      setError(message(err))
    }
  }, [client, edge])

  useEffect(() => {
    void load()
  }, [load])

  const link = async (run: () => Promise<EdgeLinkResult>) => {
    setBusy(true)
    setError(null)
    try {
      const result = await run()
      if (result.remote_error) toast.error(result.remote_error)
      onLinked(result)
    } catch (err) {
      setError(message(err))
    } finally {
      setBusy(false)
    }
  }

  const claimForm = (
    <form
      aria-label="Add a server"
      className="flex min-w-0 max-w-sm flex-col items-start gap-2"
      onSubmit={(e) => {
        e.preventDefault()
        if (parseJoin(code)) {
          onHand({ kind: 'join', link: code.trim() })
          return
        }
        const claim = parseClaim(code)
        if (claim?.edge && !sameEdge(claim.edge, edge)) {
          onHand({ kind: 'claim', link: code.trim() })
          return
        }
        void link(() => client.localEdgeClaim(claim?.code ?? code.trim(), edge))
      }}
    >
      <FormField
        className="self-stretch"
        label="Claim or invite link"
        help={<>The <span className="font-code">aether://claim</span> link <span className="font-code">aether-server setup</span> printed, valid for 30 minutes (<span className="font-code">sudo aether-server edge claim-code</span> prints a new one), or an invite link from an admin. Claiming makes {accountName(account)} the server&apos;s admin.</>}
      >
        <Input className="font-code" autoComplete="off" value={code} disabled={busy} onChange={(e) => setCode(e.target.value)} />
      </FormField>
      <Button type="submit" size="sm" disabled={busy || !code.trim()}>
        {busy ? 'Claiming…' : 'Claim and link'}
      </Button>
    </form>
  )

  return (
    <div className="flex min-w-0 flex-col gap-3 self-stretch">
      <p>
        Signed in to <span className="font-code">{edgeHost(edge)}</span> as{' '}
        <span className="font-medium">{accountName(account)}</span>.
      </p>
      {error && (
        <div className="flex min-w-0 flex-col items-start gap-2">
          <ErrorLine>{error}</ErrorLine>
          {servers === null && !retired && (
            <Button size="sm" variant="secondary" onClick={() => void load()}>
              Retry
            </Button>
          )}
        </div>
      )}
      {notice}
      {servers?.length === 0 && (
        <p className="text-muted">
          Your account reaches no servers yet. Paste the claim link <span className="font-code">aether-server setup</span>{' '}
          printed, or the invite link an admin sent {account.login ?? account.email ?? 'your account'}.
        </p>
      )}
      {servers && servers.length > 0 && (
        <InsetList aria-label="Your servers">
          {servers.map((server) => (
            <InsetListItem key={server.id}>
              <StatusDot tone={server.online ? 'done' : 'neutral'} />
              <span className="flex min-w-0 flex-1 flex-col">
                <span className="truncate font-medium">{server.name}</span>
                <span className="truncate text-ui-sm text-muted">
                  {server.online ? 'Online' : 'Offline'} · {server.role} · {policyLine[server.access_policy]}
                </span>
              </span>
              <Button
                size="sm"
                variant="secondary"
                disabled={busy}
                aria-label={`Link ${server.name}`}
                onClick={() => setConfirming(server)}
              >
                Link
              </Button>
            </InsetListItem>
          ))}
        </InsetList>
      )}
      {confirming && (
        <ConfirmPin
          key={confirming.id}
          client={client}
          edge={edge}
          server={confirming}
          busy={busy}
          onCancel={() => setConfirming(null)}
          onConfirm={() =>
            void link(async () => ({
              ...(await client.localEdgeLink(confirming.id, edge)),
              server_name: confirming.name,
            }))
          }
        />
      )}
      {!retired && claimForm}
      {!retired && (
        <Collapsible>
          <CollapsibleTrigger>Other ways to link</CollapsibleTrigger>
          <CollapsibleContent className="flex flex-col gap-4 pt-2 pl-5">
            <form
              aria-label="Link by server id"
              className="flex min-w-0 max-w-sm flex-col items-start gap-2"
              onSubmit={(e) => {
                e.preventDefault()
                void link(() => client.localEdgeLink(serverID.trim(), edge))
              }}
            >
              <FormField
                className="self-stretch"
                label="Server id from your admin"
                help={<>The admin reads it from <span className="font-code">aether-server edge status</span>. The link accepts only the server this id names.</>}
              >
                <Input className="font-code" autoComplete="off" value={serverID} disabled={busy} onChange={(e) => setServerID(e.target.value)} />
              </FormField>
              <Button type="submit" size="sm" variant="secondary" disabled={busy || !serverID.trim()}>
                Link by id
              </Button>
            </form>
          </CollapsibleContent>
        </Collapsible>
      )}
    </div>
  )
}

function sameEdge(a: string, b: string): boolean {
  try {
    return new URL(a).origin === new URL(b).origin
  } catch {
    return a === b
  }
}

function linkHost(edge: string): string {
  try {
    return new URL(edge).host
  } catch {
    return edge
  }
}

/** The id comes from the edge, so only comparing it with the admin's rules
 * out an edge that lists a false one. */
function ConfirmPin({
  client,
  edge,
  server,
  busy,
  onCancel,
  onConfirm,
}: {
  client: Api
  edge: string
  server: EdgeServer
  busy: boolean
  onCancel: () => void
  onConfirm: () => void
}) {
  const [fingerprint, setFingerprint] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    client.localEdgeHostKey(server.id, edge).then(
      (key) => {
        if (live) setFingerprint(key.fingerprint)
      },
      (err: unknown) => {
        if (live) setError(message(err))
      },
    )
    return () => {
      live = false
    }
  }, [client, edge, server.id])

  return (
    <section
      aria-label="Confirm server"
      className="flex min-w-0 flex-col gap-2 rounded-panel border border-seam p-3"
    >
      <p className="font-medium">Link {server.name}?</p>
      <dl className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3 gap-y-1 text-ui-sm">
        <dt className="text-muted">Server id</dt>
        <dd className="min-w-0 break-all font-code">{server.id}</dd>
        <dt className="text-muted">Host key</dt>
        <dd className="min-w-0 break-all font-code">
          {fingerprint ?? (error ? 'not read' : 'reading…')}
        </dd>
      </dl>
      {error && <ErrorLine>{error}</ErrorLine>}
      <p className="text-ui-sm text-muted">
        From now on this link accepts only this host key, directly and through the edge. An edge that lists a false
        id can send a first link to another server: compare the id with the one{' '}
        <span className="font-code">aether-server edge status</span> prints on the server, or ask its admin.
      </p>
      <div className="flex min-w-0 flex-wrap gap-2">
        <Button size="sm" disabled={busy || fingerprint === null} onClick={onConfirm}>
          {busy ? 'Linking…' : 'Link and pin'}
        </Button>
        <Button size="sm" variant="secondary" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </section>
  )
}
