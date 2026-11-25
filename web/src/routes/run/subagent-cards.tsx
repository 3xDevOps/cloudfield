import { memo, useEffect, useId, useRef } from 'react'
import { AgentCard, AgentEntry, duration, WorkGroup } from '@/components/ui/timeline'
import { Shimmer } from '@/components/ui/shimmer'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { api } from '@/lib/api'
import { useClock } from '@/lib/clock'
import type { WorkflowProgress } from '@/lib/session-types'
import { redirectRoute } from '@/lib/url-state'
import { useRunParams } from '@/routes/run/agent-context'
import { useStore } from '@/store'
import { loadOlderItems } from '@/store/session-stream'
import {
  activityLabel,
  agentLabel,
  agentLastMessage,
  agentReport,
  agentState,
  agentType,
  launchUnloaded,
  workflowTally,
  type AgentState,
  type Subagent,
} from '@/store/subagents'

const tones: Record<AgentState, { tone: Tone; pulse?: boolean; word: string }> = {
  running: { tone: 'working', pulse: true, word: 'Running' },
  done: { tone: 'done', word: 'Done' },
  failed: { tone: 'failed', word: 'Failed' },
  stopped: { tone: 'paused', word: 'Stopped' },
  launched: { tone: 'neutral', word: 'Launched' },
  unknown: { tone: 'neutral', word: 'Not loaded' },
}

export const agentStateWord = (state: AgentState) => tones[state].word
export const agentTone = (state: AgentState) => tones[state].tone

const launchPages = 5

/** Reads older history until every live subagent's launch is held: a reload
 * replays only the newest items, which can leave out a launch that still runs. */
export function useLiveLaunches(runID: string, enabled: boolean) {
  const paged = useRef({ generation: -1, pages: 0 })
  const generation = useStore((s) => {
    const session = s.acpSessions[runID]
    const missing = enabled && session !== undefined && session.more && !session.olderLoading && !session.olderError && launchUnloaded(session.agents, session.state?.tasks, session.oldestSeq)
    return missing ? session.historyGeneration : undefined
  })
  useEffect(() => {
    if (generation === undefined) return
    if (paged.current.generation !== generation) paged.current = { generation, pages: 0 }
    if (paged.current.pages++ < launchPages) void loadOlderItems(useStore, api, runID)
  }, [generation, runID])
}

/** replace swaps the open subagent without a history entry. */
export function openAgent(runID: string, id: string, replace = false) {
  const params = { runId: runID, view: 'session', agent: id }
  if (replace) redirectRoute(useStore, { name: 'run', params })
  else useStore.getState().navigate('run', params)
}

export function AgentDot({ state }: { state: AgentState }) {
  const { tone, pulse } = tones[state]
  return <StatusDot tone={tone} pulse={pulse} />
}

function runningMinutes(startedAt: string | undefined, now: number): string {
  const minutes = startedAt ? Math.floor((now - Date.parse(startedAt)) / 60_000) : 0
  if (minutes < 1) return '<1m'
  return minutes < 60 ? `${minutes}m` : `${Math.floor(minutes / 60)}h ${minutes % 60}m`
}

export function RunningElapsed({ startedAt }: { startedAt: string | undefined }) {
  useClock()
  return <>{runningMinutes(startedAt, Date.now())}</>
}

export function AgentElapsed({ agent, state }: { agent: Subagent; state: AgentState }) {
  if (!agent.startedAt) return null
  if (state === 'running') return <RunningElapsed startedAt={agent.startedAt} />
  if (state === 'launched' || state === 'unknown') return null
  const end = agent.settledAt ?? (state === 'stopped' ? agent.turnEndedAt : undefined)
  return <>{end ? duration(Date.parse(end) - Date.parse(agent.startedAt)) : null}</>
}

const markdownMarks = /^(#{1,6}\s+|[-*+]\s+|\d+[.)]\s+|>\s*)+|\*\*|__|`/g

export function firstLine(text: string | undefined): string | undefined {
  return text?.split('\n').map((line) => line.trim().replace(markdownMarks, '').trim()).find((line) => /[\p{L}\p{N}]/u.test(line))
}

function workflowLine(workflow: WorkflowProgress, state: AgentState): string | undefined {
  const rows = workflow.agents ?? []
  const tally = workflowTally(rows, state === 'running')
  switch (state) {
    case 'running': {
      const phases = workflow.phases ?? []
      const at = phases.map((phase) => rows.some((row) => row.state === 'running' && (row.phase ?? 0) === phase.index)).lastIndexOf(true)
      const counts = [tally.running && `${tally.running} running`, at === -1 && tally.queued && `${tally.queued} queued`, tally.failed && `${tally.failed} failed`].filter(Boolean).join(' · ')
      return [at !== -1 && `Phase ${at + 1} of ${phases.length}: ${phases[at]!.title}`, counts].filter(Boolean).join(' · ') || undefined
    }
    case 'done':
      return tally.done ? [`${plural(tally.done, 'agent')} finished`, tally.failed && `${tally.failed} failed`].filter(Boolean).join(' · ') : undefined
    case 'failed': {
      const failed = rows.find((row) => row.state === 'failed')
      return failed && [`${tally.failed} of ${plural(rows.length, 'agent')} failed`, firstLine(failed.error)].filter(Boolean).join(': ')
    }
    case 'stopped':
      return tally['not-started'] ? `Stopped with ${plural(tally['not-started'], 'agent')} not started` : undefined
    default:
      return undefined
  }
}

function Progress({ runID, agent, state }: { runID: string; agent: Subagent; state: AgentState }) {
  const more = useStore((s) => s.acpSessions[runID]?.more ?? false)
  const workflow = agent.call?.task?.workflow
  const line = workflow && workflowLine(workflow, state)
  const report = state === 'done' || state === 'failed' ? firstLine(agentReport(agent) ?? (agent.call?.task && !agent.workflow ? agentLastMessage(agent) : undefined)) : undefined
  const activity = agent.latest ? activityLabel(agent.latest) : undefined
  switch (state) {
    case 'running':
      return <Shimmer text={line ?? activity ?? (agent.call?.task ? 'Running in the background' : agent.activeSeq !== undefined ? 'Working' : 'Starting')} className="block w-fit max-w-full truncate" />
    case 'done':
      return <>{line ?? report ?? 'Finished'}</>
    case 'failed':
      return <span className="text-state-failed">{line ?? report ?? 'Failed'}</span>
    case 'stopped':
      return <>{line ?? (agent.call?.task ? 'Stopped' : 'Stopped when its turn ended')}</>
    case 'launched':
      return <>Launched in the background</>
    case 'unknown': {
      const why = more ? 'Its launch is in history not loaded yet' : 'Its launch is not in the conversation history'
      return <>{activity ? `${why} · ${activity}` : why}</>
    }
  }
}

export function useAgent(runID: string, id: string): Subagent | undefined {
  return useStore((s) => s.acpSessions[runID]?.agents.agents[id])
}

function NestedAgent({ runID, id }: { runID: string; id: string }) {
  const agent = useAgent(runID, id)
  if (!agent) return null
  const state = agentState(agent)
  return <AgentEntry dot={<AgentDot state={state} />} label={agentLabel(agent)} state={agentStateWord(state)} onOpen={() => openAgent(runID, id)} />
}

const SubagentCard = memo(function SubagentCard({ runID, id }: { runID: string; id: string }) {
  const agent = useAgent(runID, id)
  const open = useRunParams().agent === id
  if (!agent) return null
  const state = agentState(agent)
  const type = agentType(agent)
  const label = agentLabel(agent)
  const rows = agent.call?.task?.workflow?.agents
  return (
    <div data-slot="subagent-card">
      <AgentCard
        dot={<AgentDot state={state} />}
        label={label}
        type={type}
        accessibleName={`${label}${type ? `, ${type}` : ''}, ${agentStateWord(state)}`}
        current={open}
        onOpen={() => openAgent(runID, id)}
        meta={
          <>
            {agent.tools > 0 && <span className="hidden sm:inline">{agent.tools} {agent.tools === 1 ? 'tool' : 'tools'}</span>}
            {agent.children.length > 0 && <span className="hidden sm:inline">{agent.children.length} nested</span>}
            {rows && <span>{`${rows.filter((row) => row.state === 'done').length} of ${plural(rows.length, 'agent')}`}</span>}
            <AgentElapsed agent={agent} state={state} />
          </>
        }
        progress={<Progress runID={runID} agent={agent} state={state} />}
      />
      {agent.workflow && agent.children.map((child) => <NestedAgent key={child} runID={runID} id={child} />)}
    </div>
  )
})

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? '' : 's'}`
}

export function summary(kinds: string[]): string {
  const states = kinds.map((kind) => kind.split(':')[1] as AgentState)
  const workflows = kinds.filter((kind) => kind.startsWith('w:')).length
  const agents = kinds.length - workflows
  const counted = [agents && plural(agents, 'subagent'), workflows && plural(workflows, 'workflow')].filter(Boolean).join(' and ')
  const parts = (['running', 'failed', 'stopped'] as const)
    .map((state) => [states.filter((s) => s === state).length, state] as const)
    .filter(([count]) => count > 0)
    .map(([count, state]) => `${count} ${state}`)
  return [counted, ...parts.length ? [parts.join(', ')] : []].join(' · ')
}

export function SubagentGroup({ runID, rowID, ids }: { runID: string; rowID: string; ids: string[] }) {
  const open = useStore((s) => s.expandedRows[runID]?.[rowID] ?? true)
  const listID = useId()
  const kinds = useStore((s) => ids.map((id) => {
    const agent = s.acpSessions[runID]?.agents.agents[id]
    return `${agent?.workflow ? 'w' : 'a'}:${agent ? agentState(agent) : 'unknown'}`
  }).join(' '))
  if (ids.length === 1) return <SubagentCard runID={runID} id={ids[0]!} />
  return (
    <div data-slot="subagent-group" className="flex flex-col">
      <WorkGroup
        summary={summary(kinds.split(' '))}
        expanded={open}
        controls={listID}
        onToggle={() => useStore.getState().setSessionRow(runID, rowID, !open)}
      />
      <div id={listID} className="flex flex-col">
        {open && ids.map((id) => <SubagentCard key={id} runID={runID} id={id} />)}
      </div>
    </div>
  )
}
