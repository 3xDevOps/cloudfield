import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { RefreshCw } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { ProgressBar } from '@/components/ui/progress-bar'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import type { Tone } from '@/components/ui/status-dot'
import { api, ApiError, type Api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import type { AccountAccess, Member, UsageProvider, UsageProviderName, UsageProviderStatus, UsageWindow } from '@/lib/types'
import { SettingRow, SettingsSection } from '@/routes/settings/layout'
import { useStore } from '@/store'

const METHOD_NOT_FOUND = -32601

type UsageClient = Pick<Api, 'accountList' | 'accountUsage'>
type Failure = { kind: 'unauthenticated' | 'unsupported' | 'error'; message: string }

const providerLabels: Record<UsageProviderName, string> = { claude: 'Claude', codex: 'Codex' }
const providerOrder: UsageProviderName[] = ['claude', 'codex']

const statusText: Record<UsageProviderStatus, string> = {
  ok: 'Current',
  stale: 'Stale',
  unauthenticated: 'Not signed in to this provider',
  unsupported: 'Not available on this server',
  unavailable: 'The provider could not report usage right now',
  error: 'The provider returned an error',
}

function failureOf(error: unknown): Failure {
  if (error instanceof ApiError && (error.status === 401 || error.status === 403)) {
    return { kind: 'unauthenticated', message: 'Your dashboard sign-in is no longer authorized.' }
  }
  if (error instanceof ApiError && (error.status === 404 || error.code === METHOD_NOT_FOUND)) {
    return { kind: 'unsupported', message: 'This server does not report account usage. Update the server to see it.' }
  }
  if (error instanceof Error && error.message) return { kind: 'error', message: error.message }
  return { kind: 'error', message: 'The usage service did not respond.' }
}

export function currentWindows(provider: UsageProvider | undefined, now: number): UsageWindow[] {
  if (!provider || (provider.status !== 'ok' && provider.status !== 'stale')) return []
  return provider.windows.filter((window) => {
    if (!Number.isFinite(window.used_percent) || window.used_percent < 0 || window.used_percent > 100) return false
    if (!window.resets_at) return true
    const reset = Date.parse(window.resets_at)
    return Number.isFinite(reset) && reset > now
  })
}

function resetsIn(resetsAt: string | undefined, now: number): string | null {
  const reset = resetsAt ? Date.parse(resetsAt) : NaN
  if (!Number.isFinite(reset) || reset <= now) return null
  const minutes = Math.max(1, Math.ceil((reset - now) / 60_000))
  if (minutes < 60) return `resets in ${minutes}m`
  const hours = Math.floor(minutes / 60)
  if (hours >= 24) return hours % 24 === 0 ? `resets in ${hours / 24}d` : `resets in ${Math.floor(hours / 24)}d ${hours % 24}h`
  return minutes % 60 === 0 ? `resets in ${hours}h` : `resets in ${hours}h ${minutes % 60}m`
}

/** The server's ExhaustedPercent (internal/fallback): a window used this far counts as run out. */
const exhaustedPercent = 95

/** The windows internal/fallback judges a Claude Code login on, and only from a current read; a per-model window shows usage alone. */
const startWindows = ['five_hour', 'seven_day']

/** "At its limit (7 days) · resets in 1d 16h" while a window the server judges is run out; the one that resets last says when the login serves again. */
export function limitWords(provider: UsageProvider | undefined, now: number): string | null {
  if (provider?.status !== 'ok') return null
  const spent = currentWindows(provider, now).filter((window) => startWindows.includes(window.id) && window.used_percent >= exhaustedPercent)
  if (spent.length === 0) return null
  const resets = (window: UsageWindow) => (window.resets_at ? Date.parse(window.resets_at) : Infinity)
  const last = spent.reduce((latest, window) => (resets(window) > resets(latest) ? window : latest))
  const reset = resetsIn(last.resets_at, now)
  return `At its limit (${last.label})${reset ? ` · ${reset}` : ''}`
}

type MeterTone = Extract<Tone, 'failed' | 'needs-you' | 'done'>

export function tone(percent: number): MeterTone {
  if (percent >= 100) return 'failed'
  if (percent >= 80) return 'needs-you'
  return 'done'
}

const fill: Record<MeterTone, string> = {
  failed: 'bg-state-failed',
  'needs-you': 'bg-state-needs-you',
  done: 'bg-state-done',
}

/** One meter per usage window; `label` names the login or provider in each meter's accessible name. */
export function UsageMeters({ label, windows, now }: { label: string; windows: UsageWindow[]; now: number }) {
  return (
    <ul className="flex min-w-0 flex-col gap-3">
      {windows.map((window) => {
        const reset = resetsIn(window.resets_at, now)
        return (
          <li key={window.id} className="flex min-w-0 flex-col gap-1">
            <div className="flex min-w-0 items-baseline justify-between gap-2 text-ui-sm">
              <span className="min-w-0 truncate text-text">{window.label}</span>
              <span className="shrink-0 tabular-nums text-muted">
                {Math.round(window.used_percent)}% used{reset && ` · ${reset}`}
              </span>
            </div>
            <ProgressBar
              role="progressbar"
              aria-label={`${label} ${window.label} usage`}
              value={window.used_percent}
              fillClassName={fill[tone(window.used_percent)]}
            />
          </li>
        )
      })}
    </ul>
  )
}

function ProviderRow({ name, provider, now, own }: { name: UsageProviderName; provider: UsageProvider | undefined; now: number; own: boolean }) {
  const label = providerLabels[name]
  const status = provider?.status ?? 'unavailable'
  const windows = currentWindows(provider, now)
  const help = status === 'ok' || status === 'stale'
    ? [provider?.plan && `Plan: ${provider.plan}`, status === 'stale' && `Stale: last values from the last successful check${provider?.error ? `: ${provider.error}` : ''}`]
        .filter(Boolean).join('. ') || undefined
    : provider?.error ?? statusText[status]
  return (
    <SettingRow
      label={label}
      help={status === 'unauthenticated' && own
        ? <>{help} <Button variant="link" size="sm" onClick={() => useStore.getState().navigate('agents')}>Sign in on Agents</Button></>
        : help}
    >
      {windows.length > 0 ? (
        <UsageMeters label={label} windows={windows} now={now} />
      ) : (status === 'ok' || status === 'stale') && <p className="text-ui-sm text-muted">No current measured windows.</p>}
    </SettingRow>
  )
}

function accountsFor(access: AccountAccess, self: Member): Member[] {
  const byID = new Map<string, Member>()
  for (const account of [self, ...access.accounts]) byID.set(account.id, account)
  return [...byID.values()]
}

export function UsageSection({ client = api }: { client?: UsageClient }) {
  const selfID = useStore((s) => s.info?.member?.id ?? null)
  const connection = useStore((s) => s.connection)
  const [selectedID, setSelectedID] = useState<string | null>(selfID)
  const [accounts, setAccounts] = useState<Member[]>(() => {
    const self = useStore.getState().info?.member
    return self ? [self] : []
  })
  const [providers, setProviders] = useState<{ accountID: string; list: UsageProvider[] } | null>(null)
  const [failure, setFailure] = useState<Failure | null>(null)
  const [loading, setLoading] = useState(false)
  const requestID = useRef(0)
  const selected = useRef(selectedID)
  selected.current = selectedID
  useClock()
  const now = Date.now()

  useEffect(() => {
    setSelectedID(selfID)
    setProviders(null)
    setFailure(null)
  }, [selfID])

  useEffect(() => {
    const self = useStore.getState().info?.member
    if (!self || connection !== 'live') return
    let live = true
    client.accountList().then(
      (access) => live && setAccounts(accountsFor(access, self)),
      () => live && setAccounts([self]),
    )
    return () => {
      live = false
    }
  }, [client, connection, selfID])

  const request = useCallback(async (refresh: boolean) => {
    const account = selected.current
    if (!selfID || !account) return
    const id = ++requestID.current
    setLoading(true)
    try {
      const result = await client.accountUsage({
        ...(account === selfID ? {} : { account_member_id: account }),
        ...(refresh ? { refresh: true } : {}),
      })
      if (id !== requestID.current) return
      if (result.account_member_id !== account) {
        setFailure({ kind: 'error', message: 'The usage response belonged to a different account.' })
        setProviders(null)
        return
      }
      setProviders({ accountID: account, list: result.providers })
      setFailure(null)
    } catch (error) {
      if (id !== requestID.current) return
      const next = failureOf(error)
      setFailure(next)
      setProviders((previous) => next.kind !== 'error' || previous?.accountID !== account ? null : {
        accountID: account,
        list: previous.list.map((provider) => provider.status === 'ok' ? { ...provider, status: 'stale' as const, error: next.message } : provider),
      })
    } finally {
      if (id === requestID.current) setLoading(false)
    }
  }, [client, selfID])

  useEffect(() => {
    if (connection !== 'live' || !selectedID) return
    setProviders(null)
    setFailure(null)
    void request(false)
  }, [connection, request, selectedID])

  const byName = useMemo(() => new Map((providers?.list ?? []).map((provider) => [provider.provider, provider])), [providers])
  if (!selfID) return null

  return (
    <SettingsSection title="Subscription usage" id="subscription-usage" scope="Your account">
      <SettingRow
        label="Account"
        labelFor="usage-account"
        help="Subscription usage the server reads for Claude and Codex logins. Other agents and API keys are not measured."
        control={(
          <>
            <Select value={selectedID ?? undefined} onValueChange={setSelectedID}>
              <SelectTrigger id="usage-account" className="w-full min-w-0 sm:w-44">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {accounts.map((account) => (
                  <SelectItem key={account.id} value={account.id}>
                    {account.id === selfID ? `${account.display_name} (you)` : account.display_name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button size="icon" variant="ghost" label="Refresh usage" disabled={loading} onClick={() => void request(true)}>
              <RefreshCw />
            </Button>
          </>
        )}
      >
        {failure && (failure.kind !== 'error' || !providers) && (
          <Callout tone={failure.kind === 'unsupported' ? 'neutral' : 'failed'} role={failure.kind === 'error' ? 'alert' : 'status'}>
            {failure.message}
          </Callout>
        )}
        {!providers && !failure && <p role="status" className="text-ui-sm text-muted">{loading ? 'Reading usage…' : 'No usage measured yet.'}</p>}
      </SettingRow>
      {providers && providerOrder.map((name) => <ProviderRow key={name} name={name} provider={byName.get(name)} now={now} own={providers.accountID === selfID} />)}
    </SettingsSection>
  )
}
