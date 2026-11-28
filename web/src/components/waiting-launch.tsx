import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ArrowLeft, Hourglass } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api } from '@/lib/api'
import { errorSentence, timeAgo } from '@/lib/format'
import { type LaunchWaiting, launchWaitFeature } from '@/lib/types'
import { useStore } from '@/store'

const pollMs = 5000

/** Whether the server keeps launches it has no room for. */
export function useLaunchWait(): boolean {
  return useStore((s) => !!s.info?.features?.includes(launchWaitFeature))
}

const open = (w: LaunchWaiting) => w.state === 'waiting' || w.state === 'starting'

const heading: Record<LaunchWaiting['state'], string> = {
  waiting: 'Waiting for room on the server',
  starting: 'Starting',
  started: 'Started',
  cancelled: 'Launch cancelled',
  expired: 'Launch gave up waiting',
  refused: 'Launch refused',
}

function useCancel(after: () => void) {
  const [cancelling, setCancelling] = useState('')
  const cancel = (id: string) => {
    setCancelling(id)
    api.launchWaitingCancel(id)
      .then(after, (err: unknown) => toast.error(`Could not cancel the launch: ${errorSentence(err)}`))
      .finally(() => setCancelling(''))
  }
  return { cancelling, cancel }
}

/**
 * The page of a session ID that is a launch waiting for room, not a session
 * yet. It asks every few seconds; the session's own page replaces it once the
 * session exists. An ID that is no waiting launch renders `fallback`.
 */
export function WaitingLaunch({ runId, fallback }: { runId: string; fallback: ReactNode }) {
  const navigate = useStore((s) => s.navigate)
  const [launch, setLaunch] = useState<LaunchWaiting | null>(null)
  const [missing, setMissing] = useState(false)
  const read = useCallback(
    (signal?: AbortSignal) =>
      api.launchWaitingList(runId, signal).then(
        (list) => setLaunch(list[0] ?? null),
        () => {
          if (!signal?.aborted) setMissing(true)
        },
      ),
    [runId],
  )
  useEffect(() => {
    const abort = new AbortController()
    void read(abort.signal)
    const timer = setInterval(() => void read(abort.signal), pollMs)
    return () => {
      abort.abort()
      clearInterval(timer)
    }
  }, [read])
  const { cancelling, cancel } = useCancel(() => void read())

  if (missing) return fallback
  if (!launch) return null
  const ended = !open(launch) && launch.state !== 'started'
  return (
    <section aria-label={heading[launch.state]} className="flex h-full min-w-0 w-full items-start px-3 py-3 sm:px-4 sm:py-4">
      <div className={`min-w-0 w-full max-w-3xl border-l-2 px-3 py-2.5 sm:px-4 ${ended ? 'border-state-failed bg-state-failed/10' : 'border-state-paused bg-state-paused/10'}`}>
        <div className="flex min-w-0 items-start gap-2.5">
          <Hourglass className={`mt-0.5 size-4 shrink-0 ${ended ? 'text-state-failed' : 'text-state-paused'}`} aria-hidden />
          <div className="min-w-0">
            <h1 className="text-title leading-5">{heading[launch.state]}</h1>
            {launch.task && <p className="mt-0.5 break-words text-ui leading-5">{launch.task}</p>}
            <p className="mt-1 text-ui-sm leading-5 text-muted">
              {open(launch)
                ? `No session exists yet. The server starts it by itself when it has room, and gives up ${until(launch.expires_at)}. Asked ${timeAgo(launch.created_at)}; tried ${launch.attempts} more ${launch.attempts === 1 ? 'time' : 'times'}; ${launch.passed} other ${launch.passed === 1 ? 'launch' : 'launches'} started first.`
                : `No session was started. Asked ${timeAgo(launch.created_at)}.`}
            </p>
            <p className="mt-1.5 text-ui-sm leading-5 text-muted">{open(launch) ? 'The server’s last refusal:' : 'Why:'}</p>
            <p role={ended ? 'alert' : undefined} className="break-words whitespace-pre-wrap font-mono text-ui-sm leading-5 select-text">
              {launch.refusal}
            </p>
          </div>
        </div>
        <div className="mt-2.5 flex flex-wrap gap-2">
          {open(launch) && (
            <Button variant="secondary" size="sm" disabled={cancelling === launch.id} onClick={() => cancel(launch.id)}>
              Cancel launch
            </Button>
          )}
          <Button variant="ghost" size="sm" onClick={() => navigate('board')}>
            <ArrowLeft className="size-3.5" aria-hidden />
            Back to board
          </Button>
        </div>
      </div>
    </section>
  )
}

function until(iso: string): string {
  return `at ${new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}`
}

/**
 * The member's launches that wait for room, and those that ended without a
 * session in the last day, above the session list: a launch that is not a
 * session yet has no row there.
 */
export function WaitingLaunches() {
  const offered = useLaunchWait()
  const navigate = useStore((s) => s.navigate)
  const [list, setList] = useState<LaunchWaiting[]>([])
  const read = useCallback((signal?: AbortSignal) => api.launchWaitingList(undefined, signal).then(setList, () => {}), [])
  useEffect(() => {
    if (!offered) return
    const abort = new AbortController()
    void read(abort.signal)
    const timer = setInterval(() => void read(abort.signal), pollMs)
    return () => {
      abort.abort()
      clearInterval(timer)
    }
  }, [offered, read])
  const { cancelling, cancel } = useCancel(() => void read())
  if (list.length === 0) return null
  return (
    <ul aria-label="Launches waiting for room" className="border-b border-seam/80">
      {list.map((w) => (
        <li key={w.id} className="flex min-w-0 items-start gap-2.5 border-t border-seam/60 px-3 py-2 first:border-t-0 sm:px-4">
          <Hourglass className={`mt-0.5 size-4 shrink-0 ${open(w) ? 'text-state-paused' : 'text-state-failed'}`} aria-hidden />
          <button type="button" className="min-w-0 flex-1 text-left" onClick={() => navigate('run', { runId: w.id })}>
            <span className="block truncate text-ui leading-5">{w.task || `${w.harness} session`}</span>
            <span className="block text-ui-sm leading-5 text-muted">
              {heading[w.state]} · asked {timeAgo(w.created_at)}
            </span>
            <span className="block break-words font-mono text-ui-sm leading-5 text-muted">{w.refusal}</span>
          </button>
          {open(w) && (
            <Button variant="secondary" size="sm" className="shrink-0" disabled={cancelling === w.id} onClick={() => cancel(w.id)}>
              Cancel
            </Button>
          )}
        </li>
      ))}
    </ul>
  )
}
