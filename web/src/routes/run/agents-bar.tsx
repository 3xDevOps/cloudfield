import { useId } from 'react'
import { StatusDot } from '@/components/ui/status-dot'
import { AgentEntry, WorkGroup } from '@/components/ui/timeline'
import { cn } from '@/lib/utils'
import { useRunParams } from '@/routes/run/agent-context'
import { sessionColumn } from '@/routes/run/session-rows'
import { AgentDot, AgentElapsed, agentStateWord, openAgent, summary, useAgent } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import { agentLabel, agentState, noAgents, topAgents } from '@/store/subagents'

const rowID = 'agents-bar'

function BarRow({ runID, id }: { runID: string; id: string }) {
  const agent = useAgent(runID, id)
  const current = useRunParams().agent === id
  if (!agent) return null
  const state = agentState(agent)
  const rows = agent.call?.task?.workflow?.agents
  const failed = rows?.filter((row) => row.state === 'failed').length ?? 0
  return (
    <li>
      <AgentEntry
        dot={<AgentDot state={state} />}
        label={agentLabel(agent)}
        current={current}
        onOpen={() => openAgent(runID, id)}
        state={
          <>
            <span className={cn(rows && 'sr-only')}>{agentStateWord(state)} </span>
            {rows && <span>{`${rows.filter((row) => row.state === 'done').length} of ${rows.length} ${rows.length === 1 ? 'agent' : 'agents'} done `}</span>}
            {failed > 0 && <span className="text-state-failed">{failed} failed </span>}
            <AgentElapsed agent={agent} state={state} />
          </>
        }
      />
    </li>
  )
}

export function AgentsBar({ runID }: { runID: string }) {
  const agents = useStore((s) => topAgents(s.acpSessions[runID]?.agents ?? noAgents))
  const open = useStore((s) => s.expandedRows[runID]?.[rowID] === true)
  const listID = useId()
  if (!agents) return null
  const kinds = agents.split('\n')
  const tone = kinds.some((kind) => kind.includes(':running:')) ? 'working' : kinds.some((kind) => kind.includes(':failed:')) ? 'failed' : kinds.every((kind) => kind.includes(':done:')) ? 'done' : 'neutral'
  return (
    <section aria-label="Subagents and workflows" data-slot="agents-bar" className={cn(sessionColumn, 'flex shrink-0 flex-col pb-1')}>
      <div className="flex flex-col rounded-control border border-seam bg-chrome px-2">
      <WorkGroup
        summary={<span className="flex min-w-0 items-center gap-2"><StatusDot tone={tone} pulse={tone === 'working'} /><span className="truncate">{summary(kinds)}</span></span>}
        expanded={open}
        controls={listID}
        onToggle={() => useStore.getState().setSessionRow(runID, rowID, !open)}
      />
      <ul id={listID} hidden={!open} className="max-h-48 overflow-y-auto max-md:max-h-[30dvh]">
        {open && kinds.map((kind) => {
          const id = kind.slice(kind.indexOf(':', 2) + 1)
          return <BarRow key={id} runID={runID} id={id} />
        })}
      </ul>
      </div>
    </section>
  )
}
