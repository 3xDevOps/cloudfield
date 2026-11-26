import { useRef } from 'react'
import { useShallow } from 'zustand/react/shallow'
import { ControlMatrix } from '@/components/control-matrix'
import { ChevronDown, Shield, Users } from '@/components/icons'
import { connectionLabel } from '@/components/shell/connection'
import { Avatar } from '@/components/ui/avatar'
import { Button } from '@/components/ui/button'
import { Menu, MenuContent, MenuItem, MenuTrigger } from '@/components/ui/menu'
import { clockTime } from '@/lib/format'
import { memberName, resolveMember } from '@/lib/member-name'
import { allowed } from '@/lib/permissions'
import type { RoomController } from '@/lib/types'
import { cn } from '@/lib/utils'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { useDotFade } from '@/routes/run/dot-fade'
import { ControlButton } from '@/routes/terminal/control-button'
import { useStore } from '@/store'
import { useSelf } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'

function heldElsewhere(controller: RoomController | undefined, selfID: string | null, agent: AgentTerminal): boolean {
  return !agent.localControl && controller?.member_id === selfID && controller.control_session_id !== agent.controlSessionID
}

export function usePresenceSummary(run: RunRecord, agent: AgentTerminal): string {
  const self = useSelf()
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const status = useStore((s) => s.roomStatus[run.id])
  const statusControl = useStore((s) => s.roomStatusControl[run.id])
  const statusError = useStore((s) => s.roomStatusError[run.id])
  const stream = useStore((s) => s.acpSessions[run.id]?.stream)
  if (agent.localControl) return 'You control'
  if (!status) return statusError ? 'Control unknown' : 'Checking control…'
  const holder = status.controller
  const who = !holder ? 'Nobody controls'
    : holder.member_id !== self.id ? `${memberName({ members, mergedInto }, holder.member_id)} controls`
      : heldElsewhere(holder, self.id, agent) ? `You control in another tab since ${clockTime(holder.acquired_at)}`
        : 'You control'
  const connection = run.acp ? stream : agent.session.state.connection
  const stale = connection !== 'live' || statusControl !== agent.roomControl || Boolean(statusError)
  return stale ? `${who} (last known)` : who
}

/** Someone else watches, another member or tab holds control, or control or presence failed: what a phone keeps the strip for. */
export function useSharedPresence(run: RunRecord, agent: AgentTerminal): boolean {
  const self = useSelf()
  const status = useStore((s) => s.roomStatus[run.id])
  const presenceError = useStore((s) => s.roomStatusError[run.id])
  const sessionError = useStore((s) => {
    const session = s.acpSessions[run.id]
    return Boolean(session?.controlError || session?.takeoverError || session?.streamError)
  })
  if (presenceError || sessionError) return true
  // Only the owner takes free control implicitly; anyone else needs the strip's button.
  if (!agent.localControl && (status?.controller || run.member_id !== self.id)) return true
  return status?.watchers.some((id) => id !== self.id) ?? false
}

function usePresence(run: RunRecord, agent: AgentTerminal) {
  const self = useSelf(run.workspace_id)
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  const summary = usePresenceSummary(run, agent)
  const status = useStore((s) => s.roomStatus[run.id])
  const presenceError = useStore((s) => s.roomStatusError[run.id])
  const session = useStore(useShallow((s) => {
    const held = s.acpSessions[run.id]
    return { stream: held?.stream, controlError: held?.controlError, takeoverError: held?.takeoverError, streamError: held?.streamError }
  }))
  const steerOthers = useStore((s) => s.workspaces[run.workspace_id]?.steer_others)
  const stream = session.stream ?? 'connecting'
  const controllerID = agent.localControl ? self.id : status?.controller?.member_id
  return {
    self,
    summary,
    status,
    presenceError,
    stream,
    streamError: session.streamError,
    controlError: session.controlError ?? session.takeoverError,
    connection: stream === 'refused' ? 'Offline' : connectionLabel[stream],
    maySteer: allowed('steer', self, { owner: run.member_id, protected: run.protected, steerOthers }),
    controllerID,
    person: (id: string) => ({ name: memberName({ members, mergedInto }, id), color: resolveMember({ members, mergedInto }, id).member?.color }),
  }
}

/** The full-width strip a phone keeps under its header. */
export function MultiplayerControls({ run, agent, onPeople }: {
  run: RunRecord
  agent: AgentTerminal
  onPeople: () => void
}) {
  const strip = useRef<HTMLDivElement>(null)
  useDotFade(strip)
  const { self, summary, status, presenceError, stream, streamError, controlError, connection, maySteer, controllerID, person } = usePresence(run, agent)
  const controller = controllerID ? person(controllerID) : undefined
  const controllerSummary = controllerID === self.id && controller ? `${controller.name} · ${summary}` : summary
  const staleWatchers = stream !== 'live' || Boolean(presenceError)

  return (
    <div
      ref={strip}
      role="group"
      aria-label="Multiplayer controls"
      className="@container/multiplayer relative isolate shrink-0 touch-pan-y overflow-clip border-b border-seam bg-chrome px-2 py-1">
      <ControlMatrix
        appearance={agent.localControl ? 'active'
          : agent.controlUnavailable ? 'hidden' : agent.roomControl?.loss ?? 'hidden'}
        takeoverProgress={agent.takeover.holderProgress}
        breathing={false}
      />
      <div className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1">
        <Button size="sm" variant="ghost" className="control-matrix-clear shrink-0" hint="See people and notes for this session" onClick={onPeople}>
          <Users />
          <span className="@max-[30rem]/multiplayer:sr-only">Multiplayer</span>
        </Button>
        <div className="flex min-w-24 flex-1">
          <div className="control-matrix-clear flex min-w-0 items-center gap-2 px-1 text-ui-sm">
            <span className="shrink-0 text-muted">Watching{status && staleWatchers && ' (last known)'}</span>
            <div role="group" aria-label="Session watchers" tabIndex={status?.watchers.length ? 0 : undefined} className="flex min-w-0 flex-1 touch-pan-x items-center gap-3 overflow-x-auto whitespace-nowrap">
              {status ? status.watchers.length > 0 ? status.watchers.map((id) => {
                const { name, color } = person(id)
                return (
                  <span key={id} className="inline-flex shrink-0 items-center gap-1.5">
                    <Avatar name={name} color={color} />
                    <span className="@max-[34rem]/multiplayer:sr-only">{name}</span>
                  </span>
                )
              }) : <span className="text-muted">Nobody</span> : <span className="text-muted">{presenceError ? 'Unavailable' : 'Checking…'}</span>}
            </div>
          </div>
        </div>
        <div className="control-matrix-clear ml-auto flex min-w-0 items-center justify-end gap-2">
          {run.protected && <span className="inline-flex shrink-0 items-center gap-1 text-ui-sm text-muted"><Shield />Protected</span>}
          <div role="group" aria-label="Session controller" className="flex min-w-0 items-center gap-1.5 text-ui-sm text-muted">
            {agent.steerable && controller && <Avatar name={controller.name} color={controller.color} />}
            <span className="min-w-0 max-w-48 truncate">{agent.steerable ? controllerSummary : agent.starting ? 'Starting…' : 'Session finished · read-only'}</span>
          </div>
          {agent.steerable && stream !== 'live' && <span className="shrink-0 text-ui-sm text-state-needs-you">{connection}</span>}
          {agent.steerable && maySteer && (
            <ControlButton
              ownsControl={agent.localControl}
              unavailable={agent.controlUnavailable || Boolean(run.switching)}
              onTakeControl={agent.session.takeControl}
              onReleaseControl={agent.session.releaseControl}
              takeover={agent.takeover.interaction}
            />
          )}
          {agent.steerable && !maySteer && <span className="shrink-0 text-ui-sm text-muted">View only</span>}
        </div>
      </div>
      {controlError && <p role="alert" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-state-failed">Control request failed: {controlError}</p>}
      {streamError && <p role="status" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-state-failed">{streamError}</p>}
      {presenceError && <p role="status" className="control-matrix-clear w-fit px-1 py-1 text-ui-sm text-muted">Presence unavailable: {presenceError}</p>}
    </div>
  )
}

/**
 * The strip folded into a desktop tile's header row: people are avatars named
 * by their tooltips, the controller's has the sky ring, and Release is inside
 * the "You control" menu.
 */
export function PresenceControls({ run, agent, wide }: {
  run: RunRecord
  agent: AgentTerminal
  /** At 560px and up: words beside the avatars once the row is 960px, where the state's reason can spare the room. */
  wide: boolean
}) {
  const { self, summary, status, stream, connection, maySteer, controllerID, person } = usePresence(run, agent)
  const controller = controllerID ? person(controllerID) : undefined
  const watchers = status?.watchers.filter((id) => id !== self.id && id !== controllerID) ?? []
  const ringed = 'ring-1 ring-accent ring-offset-1 ring-offset-canvas'
  const words = 'hidden @min-[60rem]/views:block'
  return (
    <div role="group" aria-label="Multiplayer controls" className="flex shrink-0 items-center gap-1 text-ui-sm text-muted">
      {watchers.length > 0 && (
        <span role="group" aria-label="Session watchers" className="flex items-center gap-1">
          {watchers.map((id) => <Avatar key={id} size="header" {...person(id)} />)}
        </span>
      )}
      {agent.steerable && stream !== 'live' && <span className="text-state-needs-you">{connection}</span>}
      {agent.steerable && agent.localControl && controller && (
        <Menu>
          <MenuTrigger asChild>
            <Button variant="ghost" size="sm" aria-label="You control" className="px-1">
              <Avatar size="header" {...controller} className={ringed} />
              {wide && <span className="hidden items-center gap-1.5 @min-[60rem]/views:flex">You control<ChevronDown aria-hidden /></span>}
            </Button>
          </MenuTrigger>
          <MenuContent align="end">
            <MenuItem disabled={agent.controlUnavailable || Boolean(run.switching)} onSelect={() => agent.session.releaseControl()}>Release</MenuItem>
          </MenuContent>
        </Menu>
      )}
      {agent.steerable && !agent.localControl && (
        <>
          {(controller || wide) && (
            <span role="group" aria-label="Session controller" title={summary} className="flex min-w-0 items-center gap-1.5">
              {controller && <Avatar size="header" {...controller} className={ringed} />}
              {wide && <span className={cn(words, 'max-w-48 truncate')}>{summary}</span>}
            </span>
          )}
          {maySteer ? (
            <ControlButton
              ownsControl={false}
              unavailable={agent.controlUnavailable || Boolean(run.switching)}
              onTakeControl={agent.session.takeControl}
              onReleaseControl={agent.session.releaseControl}
              takeover={agent.takeover.interaction}
            />
          ) : wide && <span className={words}>View only</span>}
        </>
      )}
    </div>
  )
}

/** What the strip says under itself when control, the stream or presence failed, as lines under a desktop tile's header row. */
export function PresenceErrors({ run, agent }: { run: RunRecord; agent: AgentTerminal }) {
  const { controlError, streamError, presenceError } = usePresence(run, agent)
  const line = 'border-b border-seam px-4 py-1 text-ui-sm'
  return (
    <>
      {controlError && <p role="alert" className={cn(line, 'text-state-failed')}>Control request failed: {controlError}</p>}
      {streamError && <p role="status" className={cn(line, 'text-state-failed')}>{streamError}</p>}
      {presenceError && <p role="status" className={cn(line, 'text-muted')}>Presence unavailable: {presenceError}</p>}
    </>
  )
}
