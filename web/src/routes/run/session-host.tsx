import { useRef } from 'react'
import type * as React from 'react'
import { memberName } from '@/lib/member-name'
import { agentDisplayNames, useAgentList } from '@/routes/agents/use-agents'
import { RunAgentContext } from '@/routes/run/agent-context'
import { useAgentTerminal } from '@/routes/run/agent-terminal'
import { useRunRoom } from '@/routes/run/room'
import { useRunShells } from '@/routes/run/shells'
import { defaultView, type RunView } from '@/routes/run/views'
import { TakeoverDialog } from '@/routes/terminal/takeover-dialog'
import { useStore } from '@/store'
import type { RunRecord } from '@/store/runs'

/**
 * What a session holds once, however many frames show it: the agent terminal's
 * attach, the room, the shells and the takeover prompt. `views` are the views
 * its frames show now.
 */
export function RunSession({ run, views, children }: { run: RunRecord; views: RunView[]; children: React.ReactNode }) {
  const members = useStore((s) => s.members)
  const mergedInto = useStore((s) => s.mergedInto)
  // Latched: a session that has shown its default view keeps asking for write control from any other view.
  const surfaceShown = useRef(false)
  surfaceShown.current ||= views.includes(defaultView(run))
  const agent = useAgentTerminal(run, surfaceShown.current)
  const { agents } = useAgentList()
  const agentName = agentDisplayNames(agents)[run.harness] ?? run.harness
  const agentEntry = agents?.find((entry) => entry.name === run.harness)
  const shells = useRunShells(run.id, views.includes('terminal'))
  const room = useRunRoom(run, agent.roomControl)
  const requesterID = agent.takeover.review?.requester_member_id
  const holding = !agent.localControl ? agent.takeover.interaction.phase : undefined

  return (
    <RunAgentContext value={{ agent, agentName, agentEntry, room, shells }}>
      {children}
      <span role="status" aria-live="polite" aria-atomic="true" className="sr-only">
        {holding === 'holding'
          ? `Keep holding to request control. ${agent.takeover.interaction.seconds} seconds remaining.`
          : holding === 'review'
            ? `Control requested. Waiting for the controller's decision. ${agent.takeover.interaction.seconds} seconds remaining.`
            : ''}
      </span>
      <TakeoverDialog
        open={Boolean(agent.takeover.review)}
        requesterName={requesterID ? memberName({ members, mergedInto }, requesterID) : ''}
        seconds={agent.takeover.interaction.seconds}
        pending={agent.takeover.decisionPending}
        error={agent.session.takeoverError}
        onDecide={agent.takeover.decide}
      />
    </RunAgentContext>
  )
}
