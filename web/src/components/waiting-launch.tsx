import { type ReactNode, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { ArrowLeft, CircleX, Hourglass } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { api, ApiError } from '@/lib/api'
import { useIsMobile } from '@/lib/breakpoints'
import { errorSentence, formatDuration, timeAgo } from '@/lib/format'
import type { LaunchWaiting } from '@/lib/types'
import { useStore } from '@/store'
import { keepsLaunches, launchOpen, refreshWaitingLaunches } from '@/store/waiting-launches'

const retryMs = 5000

/** Whether the server keeps launches it has no room for. */
export function useLaunchWait(): boolean {
  return useStore(keepsLaunches)
}

const heading: Record<LaunchWaiting['state'], string> = {
  waiting: 'Waiting for room on the server',
  starting: 'Starting',
  started: 'Started',
  cancelled: 'Launch cancelled',
  expired: 'Launch gave up waiting',
  refused: 'Launch refused',
}

/** Cancels by ID; the server's answer is the launch as it ended, or as it is when it could not be cancelled. */
function useCancel() {
  const [cancelling, setCancelling] = useState<ReadonlySet<string>>(new Set())
  const busy = (id: string, on: boolean) =>
    setCancelling((ids) => {
      const next = new Set(ids)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  const cancel = (id: string) => {
    busy(id, true)
    api.launchWaitingCancel(id)
      .then(
        (launch) => useStore.getState().upsertWaitingLaunch(launch),
        (err: unknown) => toast.error(`Could not cancel the launch: ${errorSentence(err)}`),
      )
      .finally(() => busy(id, false))
  }
  return { cancelling, cancel }
}

/**
 * The page of a session ID that is a launch waiting for room, not a session
 * yet. The store asks after an open launch every few seconds, and the
 * session's own page replaces this one once the session exists. An ID the
 * server says is no launch renders `fallback`.
 */
export function WaitingLaunch({ runId, fallback }: { runId: string; fallback: ReactNode }) {
  const navigate = useStore((s) => s.navigate)
  // A phone opens a session over its tab bar, where the list is "Sessions".
  const mobile = useIsMobile()
  const launch = useStore((s) => s.waitingLaunches[runId])
  const openDialog = useStore((s) => s.openPaletteDialog)
  const [none, setNone] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const known = launch !== undefined
  useEffect(() => {
    if (known || none) return
    const abort = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const ask = () =>
      api.launchWaitingList(runId, abort.signal)
        .then(async ([found]) => {
          // A launch that started is its session, which this tile shows once
          // the store has it; deleted since, it is nothing.
          const run = found?.state === 'started' ? await api.runGet(runId) : undefined
          if (abort.signal.aborted) return
          if (run) useStore.getState().upsertRun(run)
          else if (found) useStore.getState().upsertWaitingLaunch(found)
          else setNone(true)
        })
        .catch((err: unknown) => {
          if (abort.signal.aborted) return
          if (err instanceof ApiError && err.status === 404) return setNone(true)
          // Not an answer about the launch: say so and ask again.
          setError(errorSentence(err))
          timer = setTimeout(() => void ask(), retryMs)
        })
    void ask()
    return () => {
      abort.abort()
      clearTimeout(timer)
    }
  }, [runId, known, none])
  const { cancelling, cancel } = useCancel()

  if (none) return fallback
  const back = (
    <Button variant="secondary" size="sm" onClick={() => navigate(mobile ? 'sessions' : 'board')}>
      <ArrowLeft className="size-3.5" aria-hidden />
      {mobile ? 'Back to sessions' : 'Back to board'}
    </Button>
  )
  if (!launch) {
    // A phone shows this page over its tab bar, so even an unanswered read needs the way out.
    return (
      <section aria-label="Session unavailable" className="h-full min-w-0 w-full px-3 py-3 sm:px-4 sm:py-4">
        {error !== null && (
          <p role="alert" className="mb-2 max-w-3xl break-words whitespace-pre-wrap text-ui leading-5 text-muted">
            Could not ask the server whether {runId} is a launch waiting for room: {error} Asking again.
          </p>
        )}
        {(error !== null || mobile) && back}
      </section>
    )
  }
  const open = launchOpen(launch)
  const over = !open && launch.state !== 'started'
  const Icon = over ? CircleX : Hourglass
  return (
    <section aria-label={heading[launch.state]} className="flex h-full min-w-0 w-full items-start px-3 py-3 sm:px-4 sm:py-4">
      <div className={`min-w-0 w-full max-w-3xl border-l-2 px-3 py-2.5 sm:px-4 ${over ? 'border-state-failed bg-state-failed/10' : 'border-state-paused bg-state-paused/10'}`}>
        <div className="flex min-w-0 items-start gap-2.5">
          <Icon className={`mt-0.5 size-4 shrink-0 ${over ? 'text-state-failed' : 'text-state-paused'}`} aria-hidden />
          <div className="min-w-0">
            <h1 className="text-title leading-5">{heading[launch.state]}</h1>
            {launch.task && <p className="mt-0.5 break-words text-ui leading-5">{launch.task}</p>}
            {open && (
              <ul className="mt-1 text-ui-sm leading-5 text-muted">
                <li>No session exists yet. The server starts it by itself when it has room.</li>
                <li>
                  Gives up in {left(launch)}, {at(launch.expires_at)}.
                </li>
                <li>
                  Asked {timeAgo(launch.created_at)}; tried {launch.attempts} {launch.attempts === 1 ? 'time' : 'times'} since.
                </li>
                {launch.passed > 0 && (
                  <li>
                    {launch.passed} other {launch.passed === 1 ? 'launch' : 'launches'} started while it waited.
                  </li>
                )}
              </ul>
            )}
            {over && (
              <p className="mt-1 text-ui-sm leading-5 text-muted">
                No session was started. Asked {timeAgo(launch.created_at)}
                {launch.ended_at ? `; ended ${at(launch.ended_at)}` : ''}.
              </p>
            )}
            {launch.state === 'started' && <p className="mt-1 text-ui-sm leading-5 text-muted">Its session exists; opening it.</p>}
            {launch.refusal && (
              <>
                <p className="mt-1.5 text-ui-sm leading-5 text-muted">{open ? 'The server’s last refusal:' : 'Why:'}</p>
                <p role={over ? 'alert' : undefined} className="break-words whitespace-pre-wrap font-mono text-ui-sm leading-5 select-text">
                  {launch.refusal}
                </p>
              </>
            )}
          </div>
        </div>
        <div className="mt-2.5 flex flex-wrap gap-2">
          {open && (
            <Button variant="secondary" size="sm" disabled={cancelling.has(launch.id)} onClick={() => cancel(launch.id)}>
              Cancel launch
            </Button>
          )}
          {over && (
            <Button variant="secondary" size="sm" onClick={() => openDialog('launch')}>
              New session
            </Button>
          )}
          {back}
        </div>
      </div>
    </section>
  )
}

const left = (w: LaunchWaiting) => formatDuration(Math.max(0, Date.parse(w.expires_at) - Date.now()))

function at(iso: string): string {
  return `at ${new Date(iso).toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'short' })}`
}

/**
 * The member's launches that wait for room, and those that ended without a
 * session in the last day, above a session list: a launch that is not a
 * session yet has no row there. `narrow` is the sidebar's: the launch's page
 * has the refusal and Cancel.
 */
export function WaitingLaunches({ narrow }: { narrow?: boolean }) {
  const offered = useLaunchWait()
  const navigate = useStore((s) => s.navigate)
  const launches = useStore((s) => s.waitingLaunches)
  const own = useStore((s) => s.ownLaunches)
  const shown = useStore((s) => (s.route.name === 'run' ? s.route.params.runId : undefined))
  useEffect(() => {
    // A launch made elsewhere shows when a list opens, without waiting for the minute's read.
    if (offered) refreshWaitingLaunches(useStore, api)
  }, [offered])
  const { cancelling, cancel } = useCancel()
  const list = own.flatMap((id) => launches[id] ?? [])
  if (list.length === 0) return null
  return (
    <ul aria-label="Launches waiting for room" className={`border-b border-seam/80 ${narrow ? '' : 'mx-3 sm:mx-4'}`}>
      {list.map((w) => {
        const over = !launchOpen(w)
        const Icon = over ? CircleX : Hourglass
        return (
          <li
            key={w.id}
            aria-current={narrow && shown === w.id ? 'page' : undefined}
            className={`flex min-w-0 flex-wrap items-start gap-x-2.5 gap-y-1.5 border-t border-seam/60 px-3 py-2 first:border-t-0 aria-[current]:bg-hover-chrome border-l-2 ${over ? 'border-l-state-failed' : 'border-l-transparent'}`}
          >
            <Icon className={`mt-0.5 size-4 shrink-0 ${over ? 'text-state-failed' : 'text-state-paused'}`} aria-hidden />
            <Button
              variant="quiet"
              className="block min-w-0 flex-1 shrink text-left font-normal whitespace-normal text-text hover:not-aria-disabled:no-underline"
              onClick={() => navigate('run', { runId: w.id })}
            >
              <span className="block truncate text-ui leading-5">{w.task || `${w.harness} session`}</span>
              <span className={`block text-ui-sm leading-5 text-muted ${narrow ? 'truncate' : ''}`}>
                <span className={over ? 'text-state-failed' : undefined}>{heading[w.state]}</span>
                {!narrow && <> <span className="whitespace-nowrap">· asked {timeAgo(w.created_at)}</span></>}
                {!narrow && !over && <> <span className="whitespace-nowrap">· gives up in {left(w)}</span></>}
              </span>
              {/* The launch's page has the refusal in full. */}
              {!narrow && <span className="line-clamp-2 break-words font-mono text-ui-sm leading-5 text-muted">{w.refusal}</span>}
            </Button>
            {!narrow && !over && (
              // Under the text on a phone, where beside it the button takes a third of the row.
              <div className="shrink-0 max-sm:basis-full max-sm:pl-6.5">
                <Button variant="secondary" size="sm" disabled={cancelling.has(w.id)} onClick={() => cancel(w.id)}>
                  Cancel launch
                </Button>
              </div>
            )}
          </li>
        )
      })}
    </ul>
  )
}
