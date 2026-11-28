import { useEffect, useId, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Callout } from '@/components/ui/callout'
import { Spinner } from '@/components/ui/spinner'
import { WorkGroup } from '@/components/ui/timeline'
import { api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import { clockTime, errorSentence } from '@/lib/format'
import { allowed } from '@/lib/permissions'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import { isTerminal, type RunRecord } from '@/store/runs'

/** An idle session whose container Aether stopped or whose VM it saved, and the wake that brings it back. */
export function SleepCallout({ run }: { run: RunRecord }) {
  const now = useClock()
  const self = useSelf(run.workspace_id)
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const maySteer = allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers })
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>()
  const [open, setOpen] = useState(false)
  const aboutID = useId()
  const since = run.asleep_since
  // A failure belongs to one sleep; the next one starts clean.
  useEffect(() => setError(undefined), [since])
  // A closed session reopens through Relaunch, which its composer offers.
  if (!since || isTerminal(run.status)) return null
  const waking = run.waking === true || busy
  const failure = error ?? run.wake_error
  const wake = async () => {
    setBusy(true)
    setError(undefined)
    try {
      await api.runWake(run.id)
    } catch (err) {
      setError(errorSentence(err))
    } finally {
      setBusy(false)
    }
  }
  const failed = failure !== undefined && !waking
  const saved = run.asleep_saved === true
  const at = <time dateTime={since}>{clockTime(since, now)}</time>
  const about = (saved
    ? 'Cloudfield saved the idle session to disk. Its processes, terminals, files and conversation come back where they were; connections to outside the session may not.'
    : 'Cloudfield stopped the idle container. The conversation, files and checkout are kept; running processes are not.')
    + (maySteer ? (saved ? ' Send a message or press Wake to restore it.' : ' Send a message or press Wake to start it.') : '')
  const wakeButton = (variant: 'link' | 'secondary') => maySteer && (
    <Button size="sm" variant={variant} hint={saved ? 'Restores the saved session' : 'Starts the container and restores the conversation'} onClick={() => void wake()}>
      Wake
    </Button>
  )
  if (failed) {
    return (
      <Callout tone="failed" title="Wake failed" actions={wakeButton('secondary')}>
        <p>Asleep since {at}. {about}</p>
        <p className="mt-1">{failure}</p>
      </Callout>
    )
  }
  return (
    <Callout tone="paused" className="rounded-control border border-transparent px-2 py-0">
      {waking ? (
        <p className="flex min-h-6 items-center gap-2 py-0.5 text-ui-sm text-muted coarse:min-h-11">
          <Spinner label="Waking" />
          <span className="min-w-0 flex-1 break-words">Waking… {saved ? 'Restoring the saved session.' : 'Starting the container and restoring the agent’s conversation.'}</span>
        </p>
      ) : (
        <>
          <div className="flex items-center gap-3">
            <div className="min-w-0 flex-1">
              <WorkGroup
                summary={<>Asleep since {at}{!saved && ' · processes stopped'}</>}
                expanded={open}
                controls={aboutID}
                wrap
                onToggle={() => setOpen(!open)}
              />
            </div>
            {wakeButton('link')}
          </div>
          <p id={aboutID} hidden={!open} className="pb-1 pl-5.5 text-ui-sm text-muted">{about}</p>
        </>
      )}
    </Callout>
  )
}
