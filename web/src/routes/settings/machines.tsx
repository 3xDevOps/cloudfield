import { type ReactNode, useCallback, useEffect, useRef, useState } from 'react'
import { RefreshCw } from '@/components/icons'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { CopyButton } from '@/components/ui/copy-button'
import { ProgressBar } from '@/components/ui/progress-bar'
import { RelativeTime } from '@/components/ui/relative-time'
import type { Tone } from '@/components/ui/status-dot'
import { verbatim } from '@/components/update-banner-shared'
import { api, type Api } from '@/lib/api'
import { formatBytes, formatDuration, message } from '@/lib/format'
import { runLabel } from '@/lib/status'
import type { ServerCapacity, ServerCapacityCount, ServerCapacityDisk, ServerCapacityRefusal } from '@/lib/types'
import { cn } from '@/lib/utils'
import { SettingRow, SettingsSection, TryAgain } from '@/routes/settings/layout'
import { useStore } from '@/store'

/** The same pace as Insights › System, which polls system.stats every 5 seconds. */
const readEveryMs = 5000

type CapacityClient = Pick<Api, 'serverCapacity'>

function useCapacity(client: CapacityClient) {
  const connection = useStore((s) => s.connection)
  const identity = useStore((s) => s.identityKey)
  const [result, setResult] = useState<ServerCapacity | null>(null)
  const [error, setError] = useState('')
  const [reading, setReading] = useState(false)
  const [shown, setShown] = useState(() => document.visibilityState === 'visible')
  const [reads, setReads] = useState(0)

  useEffect(() => {
    setResult(null)
    setError('')
  }, [identity])

  useEffect(() => {
    const onChange = () => setShown(document.visibilityState === 'visible')
    document.addEventListener('visibilitychange', onChange)
    return () => document.removeEventListener('visibilitychange', onChange)
  }, [])

  useEffect(() => {
    if (connection !== 'live' || !shown) return
    let stopped = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const read = async () => {
      setReading(true)
      try {
        const next = await client.serverCapacity()
        if (stopped) return
        setResult(next)
        setError('')
      } catch (err) {
        if (stopped) return
        setError(message(err))
      } finally {
        if (!stopped) setReading(false)
      }
      timer = setTimeout(() => void read(), readEveryMs)
    }
    void read()
    return () => {
      stopped = true
      clearTimeout(timer)
    }
  }, [client, connection, shown, identity, reads])

  const refresh = useCallback(() => setReads((n) => n + 1), [])
  return { result, error, reading, refresh }
}

export function MachinesSection({ client = api }: { client?: CapacityClient }) {
  const { result, error, reading, refresh } = useCapacity(client)
  const failed = error && (
    <Callout tone="failed" role="alert" title="Could not read the machine" actions={<TryAgain onClick={refresh} />}>
      <p className="font-code text-ui-sm whitespace-pre-wrap [overflow-wrap:anywhere]">{error}</p>
    </Callout>
  )
  if (!result) {
    return (
      <SettingsSection title="This machine" id="machine">
        <SettingRow label="Memory, disk and session VMs" help={error ? null : 'Reading the machine…'}>{failed}</SettingRow>
      </SettingsSection>
    )
  }
  return <Machine result={result} failed={failed} reading={reading} refresh={refresh} />
}

function Machine({ result: r, failed, reading, refresh }: { result: ServerCapacity; failed: ReactNode; reading: boolean; refresh: () => void }) {
  const refused = refusedFor(r)
  return (
    <>
      <SettingsSection title="This machine" id="machine">
        <SettingRow
          label="A launch now"
          help={`Read again every ${readEveryMs / 1000} seconds while this page is open.`}
          control={<Button size="sm" variant="secondary" disabled={reading} onClick={refresh}><RefreshCw />Refresh</Button>}
        >
          <p className="flex flex-wrap items-center gap-2 text-ui">
            {refused.length === 0
              ? <><Badge tone="done">Fits</Badge>A launch would start: memory and disk are above what it needs.</>
              : <><Badge tone="failed">Refused</Badge>A launch would be refused for {joinWords(refused)}.</>}
          </p>
          {failed}
        </SettingRow>
        <MemoryRow r={r} />
        <StallRow r={r} />
        {r.disk_error && (
          <SettingRow label="Disk" help="Not measured, so a launch is refused:">
            <p className={cn(verbatim, 'text-state-failed')}>{r.disk_error}</p>
          </SettingRow>
        )}
        {r.disks.map((d) => <DiskRow key={d.name} disk={d} need={r.start_disk_bytes} />)}
        <VMsRow vms={r.vms} />
      </SettingsSection>
      <SettingsSection title="Last refusals">
        <RefusalRow what="launch" refusal={r.last_launch_refusal} />
        <RefusalRow what="wake" refusal={r.last_wake_refusal} />
      </SettingsSection>
      <Counts day={r.day} fortnight={r.fortnight} />
    </>
  )
}

function refusedFor(r: ServerCapacity): string[] {
  const reasons: string[] = []
  if (r.memory_error) reasons.push('memory, which could not be measured')
  else if (r.memory_available_bytes < r.memory_floor_bytes + r.start_memory_bytes) reasons.push('memory')
  if (r.server_stall !== undefined && r.server_stall >= r.server_stall_limit) reasons.push('the memory stall')
  if (r.disk_error) reasons.push('disk, which could not be measured')
  for (const d of r.disks) if (d.free_bytes < d.reserve_bytes + r.start_disk_bytes) reasons.push(`disk on ${d.name}`)
  return reasons
}

function joinWords(words: string[]): string {
  return words.length < 2 ? words.join('') : `${words.slice(0, -1).join(', ')} and ${words.at(-1)}`
}

/** Free space on a track of the total, with a mark at what the server keeps and one at what a launch needs. */
function Room({ label, free, total, keep, need }: { label: string; free: number; total: number; keep: number; need: number }) {
  const fits = free >= keep + need
  const at = (bytes: number) => `${Math.min(100, (bytes / Math.max(total, 1)) * 100)}%`
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <div className="relative">
        <ProgressBar
          role="meter"
          aria-label={label}
          aria-valuetext={`${formatBytes(free)} free of ${formatBytes(total)}; a launch needs ${formatBytes(keep + need)}`}
          value={free}
          max={total}
          className="h-2"
          fillClassName={fits ? 'bg-state-done' : 'bg-state-failed'}
        />
        <span aria-hidden className="absolute -top-1 h-4 w-0.5 -translate-x-1/2 rounded-full bg-icon-faint" style={{ left: at(keep) }} />
        <span aria-hidden className="absolute -top-1 h-4 w-0.5 -translate-x-1/2 rounded-full bg-text" style={{ left: at(keep + need) }} />
      </div>
      <p className="flex flex-wrap gap-x-3 text-ui-xs text-muted tabular-nums">
        <span className="flex items-center gap-1"><span aria-hidden className="h-3 w-0.5 rounded-full bg-icon-faint" />Kept free {formatBytes(keep)}</span>
        <span className="flex items-center gap-1"><span aria-hidden className="h-3 w-0.5 rounded-full bg-text" />A launch needs {formatBytes(keep + need)} free</span>
      </p>
    </div>
  )
}

function MemoryRow({ r }: { r: ServerCapacity }) {
  if (r.memory_error) {
    return (
      <SettingRow label="Memory" help="Not measured, so a launch is refused:">
        <p className={cn(verbatim, 'text-state-failed')}>{r.memory_error}</p>
      </SettingRow>
    )
  }
  const available = r.memory_available_bytes
  const floor = r.memory_floor_bytes
  const [tone, word]: [Tone, string] = available < floor ? ['failed', 'Under the floor'] : available < floor + r.start_memory_bytes ? ['failed', 'Launch refused'] : ['done', 'Launch fits']
  return (
    <SettingRow
      label="Memory"
      help={`${formatBytes(available)} available of ${formatBytes(r.memory_total_bytes)}. The server keeps ${formatBytes(floor)} free, and a launch needs ${formatBytes(r.start_memory_bytes)} above that.${available < floor ? ' Under it the server freezes session VMs and saves them to disk.' : ''}`}
      control={<Badge tone={tone}>{word}</Badge>}
    >
      <Room label="Memory available" free={available} total={r.memory_total_bytes} keep={floor} need={r.start_memory_bytes} />
    </SettingRow>
  )
}

function StallRow({ r }: { r: ServerCapacity }) {
  const line = (what: string, value: number | undefined, limit: number, atLimit: string, missing: string) => (
    <li className="flex min-w-0 flex-wrap items-baseline gap-x-2">
      <span className="text-text">{what}</span>
      {value === undefined
        ? <span className="text-muted">Not measured: {missing}.</span>
        : (
            <span className={cn('tabular-nums', value >= limit ? 'text-state-failed' : 'text-muted')}>
              {value.toFixed(1)}%, limit {limit.toFixed(0)}%{value >= limit && ` · ${atLimit}`}
            </span>
          )}
    </li>
  )
  return (
    <SettingRow label="Memory stall" help="The share of time tasks waited for memory. At its limit the server acts.">
      <ul className="flex flex-col gap-1 text-ui-sm">
        {line('Server, last 60 seconds, refuses launches:', r.server_stall, r.server_stall_limit, 'launches refused', "the server's own cgroup reports no memory pressure")}
        {line('Host, last 10 seconds, saves session VMs:', r.host_stall, r.host_stall_limit, 'VMs are saved if it stays there', 'the kernel reports no memory pressure')}
      </ul>
      {r.stall_error && <p className={cn(verbatim, 'text-state-failed')}>{r.stall_error}</p>}
    </SettingRow>
  )
}

function DiskRow({ disk: d, need }: { disk: ServerCapacityDisk; need: number }) {
  const fits = d.free_bytes >= d.reserve_bytes + need
  return (
    <SettingRow
      label={`Disk: ${d.name}`}
      help={`${formatBytes(d.free_bytes)} free of ${formatBytes(d.total_bytes)}. The server keeps ${formatBytes(d.reserve_bytes)} free, and a launch needs ${formatBytes(need)} above that.`}
      control={<Badge tone={fits ? 'done' : 'failed'}>{fits ? 'Launch fits' : 'Launch refused'}</Badge>}
    >
      <Room label={`${d.name} free`} free={d.free_bytes} total={d.total_bytes} keep={d.reserve_bytes} need={need} />
    </SettingRow>
  )
}

function VMsRow({ vms }: { vms: ServerCapacity['vms'] }) {
  const cells: [number, string][] = [
    [vms.awake, 'Awake'],
    [vms.frozen, 'Frozen by the memory brake'],
    [vms.paused, 'Asleep in memory'],
    [vms.saved, 'Asleep on disk'],
  ]
  return (
    <SettingRow label="Session VMs" help="The VMs of open sessions.">
      <dl className="grid grid-cols-2 gap-2 sm:grid-cols-4">
        {cells.map(([count, what]) => (
          <div key={what} className="flex min-w-0 flex-col rounded-control border border-seam px-3 py-2">
            <dd className={cn('text-prose font-medium tabular-nums', what.startsWith('Frozen') && count > 0 && 'text-state-needs-you')}>{count}</dd>
            <dt className="order-last text-ui-sm text-muted">{what}</dt>
          </div>
        ))}
      </dl>
    </SettingRow>
  )
}

function RefusalRow({ what, refusal }: { what: 'launch' | 'wake'; refusal?: ServerCapacityRefusal }) {
  const text = useRef<HTMLParagraphElement>(null)
  const navigate = useStore((s) => s.navigate)
  const session = useStore((s) => (refusal?.run_id && s.runs[refusal.run_id] ? runLabel(s.runs[refusal.run_id]) : refusal?.run_id))
  const label = `Last ${what} refused`
  if (!refusal) return <SettingRow label={label} help="None in 14 days." />
  const when = new Date(refusal.at)
  return (
    <SettingRow
      label={label}
      help={(
        <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <RelativeTime at={refusal.at} className="text-text" />
          <span className="tabular-nums">{Number.isNaN(when.getTime()) ? refusal.at : when.toLocaleString()}</span>
          {refusal.cause && <Badge>{refusal.cause}</Badge>}
          {refusal.op !== what && <Badge>{refusal.op}</Badge>}
        </span>
      )}
      control={<CopyButton text={refusal.text} label={`Copy why the last ${what} was refused`} source={text} />}
    >
      <p ref={text} className="max-h-48 min-w-0 overflow-auto rounded-control border border-seam bg-chrome px-2 py-1.5 font-code text-ui-sm leading-5 whitespace-pre-wrap [overflow-wrap:anywhere] select-text">
        {refusal.text}
      </p>
      {refusal.run_id && (
        <p className="flex min-w-0 items-center gap-1 text-ui-sm text-muted">
          Session
          <Button variant="link" size="sm" className="h-auto min-w-0 p-0" onClick={() => navigate('run', { runId: refusal.run_id! })}>
            <span className="truncate">{session}</span>
          </Button>
        </p>
      )}
    </SettingRow>
  )
}

const counted = (counts: ServerCapacityCount[], match: (c: ServerCapacityCount) => boolean) =>
  counts.filter(match).reduce((n, c) => n + c.count, 0)

const launchRefusal = (c: ServerCapacityCount) => c.kind === 'refusal' && (c.op === 'launch' || c.op === 'launch-retry')
const wakeRefusal = (c: ServerCapacityCount) => c.kind === 'refusal' && c.op === 'wake'
const memoryRefusal = (c: ServerCapacityCount) => c.kind === 'refusal' && (c.cause === 'memory' || c.cause === 'stall')

function waitsText(counts: ServerCapacityCount[]): string {
  const started = counts.filter((c) => c.kind === 'wait-end' && c.cause === 'started')
  const n = started.reduce((sum, c) => sum + c.count, 0)
  if (n === 0) return '–'
  const total = started.reduce((sum, c) => sum + (c.waited_seconds ?? 0), 0)
  const longest = Math.max(...started.map((c) => c.max_waited_seconds ?? 0))
  return `${formatDuration((total / n) * 1000)} mean, ${formatDuration(longest * 1000)} longest`
}

function idleShare(counts: ServerCapacityCount[]): string {
  const refusals = counts.filter(memoryRefusal)
  const used = refusals.reduce((sum, c) => sum + (c.used_bytes ?? 0), 0)
  if (used === 0) return '–'
  return `${Math.round((100 * refusals.reduce((sum, c) => sum + (c.idle_hour_bytes ?? 0), 0)) / used)}%`
}

const counterName = (c: ServerCapacityCount) => [c.kind, c.op, c.cause].filter(Boolean).join(' ')

function Counts({ day, fortnight }: { day: ServerCapacityCount[]; fortnight: ServerCapacityCount[] }) {
  if (fortnight.length === 0) {
    return (
      <SettingsSection title="Counted">
        <SettingRow label="Nothing counted in 14 days" help="No launch or wake was refused, nothing waited for room, and no session VM was frozen or saved for memory." />
      </SettingsSection>
    )
  }
  const rows: [string, string | null, (counts: ServerCapacityCount[]) => string][] = [
    ['Launches refused', "A waiting subsession's retries count each time.", (counts) => String(counted(counts, launchRefusal))],
    ['Subsessions that waited for room', null, (counts) => String(counted(counts, (c) => c.kind === 'wait'))],
    ['Wait before a start', 'Waits that ended in a start.', waitsText],
    ['Memory in use at memory refusals that sessions idle over an hour held', null, idleShare],
    ['Wakes refused', null, (counts) => String(counted(counts, wakeRefusal))],
  ]
  const byName = new Map(day.map((c) => [counterName(c), c]))
  const head = 'px-3 py-1.5 text-right font-normal text-muted'
  return (
    <SettingsSection title="Counted">
      <div className="min-w-0 overflow-x-auto">
        <table className="w-full text-ui">
          <caption className="sr-only">Refusals and waits over the last 24 hours and 14 days</caption>
          <thead className="text-ui-sm">
            <tr className="border-b border-seam">
              <th className="px-4 py-1.5 text-left font-normal text-muted" scope="col"><span className="sr-only">What</span></th>
              <th className={head} scope="col">24 hours</th>
              <th className={cn(head, 'pr-4')} scope="col">14 days</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-seam">
            {rows.map(([label, help, value]) => (
              <tr key={label}>
                <th scope="row" className="px-4 py-2 text-left align-top font-normal">
                  <span className="text-text">{label}</span>
                  {help && <span className="block text-ui-sm text-muted">{help}</span>}
                </th>
                <td className="px-3 py-2 text-right align-top tabular-nums">{value(day)}</td>
                <td className="py-2 pr-4 pl-3 text-right align-top tabular-nums">{value(fortnight)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <details className="min-w-0 border-t border-seam px-4 py-2 text-ui-sm">
        <summary className="cursor-pointer py-1 text-muted">All {fortnight.length} counters</summary>
        <div className="min-w-0 overflow-x-auto pb-1">
          <table className="w-full">
            <thead>
              <tr className="text-muted">
                <th className="py-1 pr-3 text-left font-normal" scope="col">Counter</th>
                <th className="px-3 py-1 text-right font-normal" scope="col">24 hours</th>
                <th className="px-3 py-1 text-right font-normal" scope="col">14 days</th>
                <th className="py-1 pl-3 text-right font-normal" scope="col">Last</th>
              </tr>
            </thead>
            <tbody>
              {fortnight.map((c) => (
                <tr key={counterName(c)}>
                  <th scope="row" className="py-0.5 pr-3 text-left font-code font-normal whitespace-nowrap">{counterName(c)}</th>
                  <td className="px-3 py-0.5 text-right tabular-nums">{byName.get(counterName(c))?.count ?? 0}</td>
                  <td className="px-3 py-0.5 text-right tabular-nums">{c.count}</td>
                  <td className="py-0.5 pl-3 text-right whitespace-nowrap text-muted"><RelativeTime at={c.last_at} /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </details>
    </SettingsSection>
  )
}
