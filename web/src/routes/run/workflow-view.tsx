import { useId } from 'react'
import { Callout } from '@/components/ui/callout'
import { Markdown } from '@/components/ui/markdown'
import { SectionLabel } from '@/components/ui/section-label'
import { Shimmer } from '@/components/ui/shimmer'
import { StatusDot, type Tone } from '@/components/ui/status-dot'
import { AgentCard, duration, MessageRow, WorkGroup } from '@/components/ui/timeline'
import type { WorkflowAgent, WorkflowProgress } from '@/lib/session-types'
import { usageFormat } from '@/routes/run/context-usage'
import { firstLine, RunningElapsed } from '@/routes/run/subagent-cards'
import { useStore } from '@/store'
import { workflowRowState, workflowTally, type WorkflowRowState } from '@/store/subagents'

const rowStates: Record<WorkflowRowState, { tone: Tone; pulse?: boolean; word: string }> = {
  queued: { tone: 'neutral', word: 'Queued' },
  running: { tone: 'working', pulse: true, word: 'Running' },
  done: { tone: 'done', word: 'Done' },
  failed: { tone: 'failed', word: 'Failed' },
  skipped: { tone: 'neutral', word: 'Skipped' },
  stopped: { tone: 'paused', word: 'Stopped' },
  'not-started': { tone: 'neutral', word: 'Not started' },
}

const countOrder: WorkflowRowState[] = ['running', 'failed', 'queued', 'skipped', 'stopped', 'not-started']

function useFold(runID: string, key: string, initial: boolean): [boolean, () => void] {
  const open = useStore((s) => s.expandedRows[runID]?.[key] ?? initial)
  return [open, () => useStore.getState().setSessionRow(runID, key, !open)]
}

function Fold({ runID, foldKey, summary, initial = false, children }: { runID: string; foldKey: string; summary: string; initial?: boolean; children: React.ReactNode }) {
  const [open, toggle] = useFold(runID, foldKey, initial)
  const bodyID = useId()
  return (
    <div className="flex flex-col gap-1">
      <WorkGroup summary={summary} expanded={open} controls={bodyID} onToggle={toggle} />
      <div id={bodyID} hidden={!open}>{open && children}</div>
    </div>
  )
}

function rowLine(row: WorkflowAgent, state: WorkflowRowState): React.ReactNode {
  switch (state) {
    case 'running':
      return <Shimmer text={[row.tool, row.tool_summary].filter(Boolean).join(': ') || 'Starting'} className="block w-fit max-w-full truncate" />
    case 'done':
      return row.cached ? 'Reused a cached result' : firstLine(row.result) ?? 'Finished'
    case 'failed':
      return <span className="text-state-failed">{row.error || 'Failed'}</span>
    default:
      return rowStates[state].word
  }
}

function AgentRow({ runID, foldKey, row, live }: { runID: string; foldKey: string; row: WorkflowAgent; live: boolean }) {
  const state = workflowRowState(row, live)
  const [open, toggle] = useFold(runID, foldKey, state === 'failed')
  const { tone, pulse, word } = rowStates[state]
  const facts = [
    row.model,
    row.tool_calls && `${row.tool_calls} ${row.tool_calls === 1 ? 'tool call' : 'tool calls'}`,
    row.tokens && `${usageFormat.format(row.tokens)} tokens`,
  ].filter(Boolean).join(' · ')
  return (
    <li>
      <AgentCard
        dot={<StatusDot tone={tone} pulse={pulse} />}
        label={row.label}
        type={row.type}
        accessibleName={`${row.label}${row.type ? `, ${row.type}` : ''}, ${word}`}
        current={false}
        expanded={open}
        onOpen={toggle}
        meta={
          <>
            {state === 'running' && row.started_at ? <RunningElapsed startedAt={row.started_at} /> : duration(row.duration_ms)}
            {row.attempt && <span>attempt {row.attempt}</span>}
          </>
        }
        progress={rowLine(row, state)}
      />
      {open && (
        <div className="flex flex-col gap-2 py-1 pr-2 pl-[1.625rem] text-ui-sm">
          {facts && <p className="text-muted tabular-nums">{facts}</p>}
          {row.prompt && <MessageRow author="Prompt">{row.prompt}</MessageRow>}
          {row.error
            ? <><SectionLabel className="text-state-failed">Error</SectionLabel><p className="break-words whitespace-pre-wrap text-state-failed">{row.error}</p></>
            : row.result && <><SectionLabel>Result</SectionLabel><Markdown text={row.result} /></>}
        </div>
      )}
    </li>
  )
}

function doneOf(rows: WorkflowAgent[]): string {
  return `${rows.filter((row) => row.state === 'done').length} of ${rows.length}`
}

export function WorkflowView({ runID, agentID, workflow, live, loading, script, report }: {
  runID: string
  agentID: string
  /** Absent for a workflow recorded before the server reported progress. */
  workflow: WorkflowProgress | undefined
  live: boolean
  /** Shown in place of the rows while a cut call is read whole. */
  loading?: React.ReactNode
  script?: React.ReactNode
  report?: React.ReactNode
}) {
  const rows = workflow?.agents ?? []
  const logs = workflow?.logs ?? []
  const phases = workflow?.phases ?? []
  const tally = workflowTally(rows, live)
  const tokens = rows.reduce((sum, row) => sum + (row.tokens ?? 0), 0)
  const counts = [
    `${doneOf(rows)} ${rows.length === 1 ? 'agent' : 'agents'} done`,
    ...countOrder.filter((state) => tally[state]).map((state) => `${tally[state]} ${rowStates[state].word.toLowerCase()}`),
    ...(tokens ? [`${usageFormat.format(tokens)} tokens`] : []),
  ].join(' · ')
  const old = !live && !loading && !workflow
  const list = (items: WorkflowAgent[]) => (
    <ul className="flex flex-col">
      {items.map((row) => <AgentRow key={row.index} runID={runID} foldKey={`wf:${agentID}:${row.index}`} row={row} live={live} />)}
    </ul>
  )
  const unphased = rows.filter((row) => !phases.some((phase) => phase.index === (row.phase ?? 0)))
  return (
    <div data-slot="workflow-view" className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 pb-4 text-ui-sm">
      {rows.length > 0 && (
        <div className="flex flex-col gap-1 text-muted">
          <p className="tabular-nums">{counts}</p>
          <p>Agents inside a workflow report status and previews, not a transcript.</p>
          {workflow?.omitted ? <p>{workflow.omitted} more {workflow.omitted === 1 ? 'agent is' : 'agents are'} not shown or counted.</p> : null}
        </div>
      )}
      {loading}
      {old && <Callout tone="neutral">This workflow ran before Cloudfield recorded per-agent progress. Only its script is available.</Callout>}
      {rows.length === 0 && !loading && !old && (
        <p className="text-muted">{live ? 'Starting. Agents appear here as the script launches them.' : 'This workflow launched no agents.'}</p>
      )}
      {unphased.length > 0 && list(unphased)}
      {phases.map((phase, at) => {
        const own = rows.filter((row) => (row.phase ?? 0) === phase.index)
        return (
          <section key={phase.index} className="flex flex-col gap-1">
            <SectionLabel as="h3">{at + 1}. {phase.title}{own.length > 0 && ` · ${doneOf(own)} done`}</SectionLabel>
            {list(own)}
          </section>
        )
      })}
      {logs.length > 0 && (
        <Fold runID={runID} foldKey={`wf:${agentID}:log`} summary={`Log · last ${logs.length} ${logs.length === 1 ? 'line' : 'lines'}`}>
          <pre className="overflow-x-auto font-code text-ui-sm whitespace-pre-wrap text-muted">{logs.join('\n')}</pre>
        </Fold>
      )}
      {script && <Fold runID={runID} foldKey={`wf:${agentID}:script`} summary="Script" initial={old}>{script}</Fold>}
      {report}
    </div>
  )
}
