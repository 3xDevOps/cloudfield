import { useId } from 'react'
import { AgentEntry, WorkGroup } from '@/components/ui/timeline'
import { cn } from '@/lib/utils'
import { sessionColumn } from '@/routes/run/session-rows'
import { AgentDot, AgentElapsed, agentStateWord, openAgent, summary, useAgent } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import { agentLabel, agentState, noAgents, topAgents } from '@/store/subagents'

const rowID = 'agents-bar'

function BarRow({ runID, id }: { runID: string; id: string }) {
  const agent = useAgent(runID, id)
  const current = useStore((s) => s.route.params.agent === id)
  if (!agent) return null
  const state = agentState(agent)
  const rows = agent.call?.task?.workflow?.agents
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
  return (
    <section aria-label="Subagents and workflows" data-slot="agents-bar" className={cn(sessionColumn, 'flex shrink-0 flex-col pb-1')}>
      <WorkGroup
        summary={summary(kinds)}
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
    </section>
  )
}
