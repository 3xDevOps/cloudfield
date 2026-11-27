import { useContext, useEffect, useRef, useState } from 'react'
import type * as React from 'react'
import { ArrowUpRight, Camera, Eye, MessageSquarePlus, Network, PanelRight, ScrollText } from '@/components/icons'
import { permissionLabel, permissions } from '@/components/launch/permission-control'
import { RunActions, type MenuPoint } from '@/components/run-actions'
import { RunRetention } from '@/components/run-retention'
import { narrowBelow } from '@/components/tiles/area'
import { useTileDrag } from '@/components/tiles/drag'
import { ConnectionLine } from '@/components/shell/connection'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { ProgressBar } from '@/components/ui/progress-bar'
import { TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Tooltip } from '@/components/ui/tooltip'
import { StatusDot } from '@/components/ui/status-dot'
import { useIsMobile } from '@/lib/breakpoints'
import { useRunAccountWords } from '@/lib/run-account'
import { extraRepoCount, repoLabel } from '@/lib/repos'
import { isSudo } from '@/lib/run-requests'
import type { AgentInfo } from '@/lib/types'
import { useClock } from '@/lib/clock'
import { useElementSize, useReducedMotion } from '@/lib/hooks'
import { shortcutLabel } from '@/lib/keybindings'
import { needsYou, terminalAction } from '@/lib/needs-you'
import { runLabel, stateName, stateTitle, stateWords, toneOf, waitLine } from '@/lib/status'
import { cn } from '@/lib/utils'
import { watchHint, watchRoute } from '@/lib/watches'
import { FrameFocusContext } from '@/routes/run/agent-context'
import { modeLabel } from '@/routes/run/agent-name'
import type { AgentTerminal } from '@/routes/run/agent-terminal'
import { RunLineage, useChildRunsItems } from '@/routes/run/child-runs'
import { useModeSwitch } from '@/routes/run/mode-switch'
import { PresenceControls, PresenceErrors } from '@/routes/run/multiplayer-controls'
import { useOpenInEditor } from '@/routes/run/open-in-editor'
import { RunLinearIssue } from '@/routes/run/linear-issue'
import { useRunPullRequest } from '@/routes/run/pull-request'
import { requestCardID } from '@/routes/run/requests'
import { PhoneRunHeader } from '@/routes/run/phone-header'
import { runViewIcon } from '@/routes/run/view-icons'
import { runViewLabel, type RunView } from '@/routes/run/views'
import type { FrameTile } from '@/routes/run'
import { useStore } from '@/store'
import { queuedSteers, unansweredQuestions } from '@/store/collaboration'
import { useHeaderPrimary, useNeedsYouRuns, useRunPresentation } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import type { NavigateOptions } from '@/store/ui'
import { agentState } from '@/store/subagents'
import { stateContextOf } from '@/store/selectors'
import { runWatch } from '@/store/watches'

export interface RunNavigation {
  go: (view: RunView, options?: NavigateOptions) => void
  reveal: (cardID: string) => void
  /** Opens Chat with its composer focused. */
  focusComposer: () => void
  focusRequest: () => void
}

interface FrameAction {
  label: string
  act: () => void
}

/** The needs-you table names the action; the frame performs it in place. */
export function usePrimaryAction(run: RunRecord, view: RunView, agent: AgentTerminal, nav: RunNavigation): FrameAction | null {
  const now = useClock()
  const condition = useStore((s) => needsYou(run, stateContextOf(s, now)))
  const room = useStore((s) => s.roomMessages[run.id])
  const selfID = useStore((s) => s.info?.member.id)
  const shown = useRunPresentation(run)
  const requested = useStore((s) => Boolean(s.acpSessions[run.id]?.pending.length))
  // A finished run asks nothing of anyone, so Review is not a needs-you action.
  if (!condition) {
    return shown.state === 'ready' && run.member_id === selfID && view !== 'changes'
      ? { label: 'Review', act: () => nav.go('changes', { remember: false }) }
      : null
  }

  const action = condition.action(run)
  const openTerminal = (): FrameAction | null =>
    !agent.hasAgentTerminal || view === 'terminal'
      ? null
      : { label: action.label, act: () => { nav.go('terminal', { remember: false }); if (!agent.localControl && !agent.controlUnavailable) agent.session.takeControl() } }

  // Chat answers a pending request on its card, which is the tile's one filled button.
  if (action.kind === 'reply') return requested && view === 'session' ? null : { label: action.label, act: nav.focusComposer }
  switch (condition.target) {
    case 'changes':
      return view === 'changes' ? null : { label: action.label, act: () => nav.go('changes', { remember: false }) }
    case 'notes': {
      const question = unansweredQuestions(room ?? []).find((m) => m.actor_id !== run.member_id)
      return { label: action.label, act: () => nav.reveal(question ? requestCardID.question(question.id) : 'details-needs-you') }
    }
    case 'request': {
      if (action.label === terminalAction.label) return openTerminal()
      const steer = condition.id === 'queued-message' ? queuedSteers(room ?? []).find((m) => m.actor_id !== selfID) : undefined
      const input = run.pending_inputs?.find((request) => isSudo(request) === (condition.id === 'sudo'))
      if (!steer && input && run.acp && !isSudo(input)) return { label: action.label, act: () => nav.focusRequest() }
      const card = steer ? requestCardID.steer(steer.id) : input ? requestCardID.input(input.id) : 'details-needs-you'
      return { label: action.label, act: () => nav.reveal(card) }
    }
    case 'terminal':
      return openTerminal()
    case 'run':
      return null
  }
}

/** A phone's header line cuts what a wait last read short, so it gets a line of its own. */
function WaitLine({ run }: { run: RunRecord }) {
  const shown = useRunPresentation(run)
  const now = useClock()
  const line = shown.state === 'needs-you' && shown.wait?.kind === 'system_fault'
    ? stateWords(shown, undefined, now).detail
    : waitLine(shown.wait, shown.state, now)
  if (!line) return null
  return (
    <p className="flex gap-1.5 border-b border-seam px-4 py-1 text-ui-sm text-muted">
      <StatusDot tone={toneOf(shown)} className="mt-1" />
      <span className="min-w-0 break-words">{line}</span>
    </p>
  )
}

/** The state's dot and label, for which `chip` stands in, then on a wide row the reason and what the session runs as. */
function StateLine({ run, agentName, narrow, controls, chip }: { run: RunRecord; agentName: string; narrow: boolean; controls: boolean; chip?: React.ReactNode }) {
  const presented = useRunPresentation(run)
  const asks = useNeedsYouRuns([run.id]).has(run.id)
  const presentation = run.switching
    ? { state: 'working' as const, reason: `Switching to ${modeLabel[run.switching] ?? run.switching}…` }
    : presented
  const { state } = presentation
  const subagents = useStore((s) =>
    state === 'waiting-agents'
      ? Object.values(s.acpSessions[run.id]?.agents.agents ?? {}).filter((agent) => agentState(agent) === 'running').length
      : 0,
  )
  const since = state === 'ready' && run.review_changed_at ? run.review_changed_at : run.stateChangedAt
  const words = stateWords(presentation, since, useClock())
  const detail = [
    subagents > 0 && !chip && `${subagents} ${subagents === 1 ? 'subagent' : 'subagents'} running`,
    words.label !== stateName(presentation) && words.label,
    words.detail,
  ]
    .filter(Boolean)
    .join(' · ')
  const account = useRunAccountWords(run)
  const mode = modeLabel[run.mode] ?? run.mode
  const watch = useStore((s) => runWatch(s, run.id))
  const navigate = useStore((s) => s.navigate)
  const part = 'flex h-full min-w-0 items-center gap-1.5'
  const [reasonRef, reason] = useElementSize<HTMLDivElement>()
  // Under its 96px floor the reason goes whole, so that it and not the view switch gives way.
  const bare = narrow || (reason.width > 0 && reason.width < 96)
  return (
    <>
      {chip ? (
        <>
          <span role="status" className="sr-only">{stateName(presentation)}</span>
          {chip}
        </>
      ) : (
        <span className="flex shrink-0 items-center gap-1.5 text-ui-sm" title={stateTitle(presentation, since)}>
          <StatusDot tone={toneOf(presentation)} pulse={state === 'working'} />
          <span role={controls ? 'status' : undefined} className="font-medium">{stateName(presentation)}</span>
        </span>
      )}
      {/* One line of a wrapping row: a part that does not fit whole wraps out of sight with its separator, and the reason, first, is the one that truncates. */}
      <div
        ref={reasonRef}
        className="flex h-full min-w-0 flex-1 basis-0 flex-wrap content-start gap-x-1.5 overflow-hidden whitespace-nowrap text-ui-sm text-muted"
        title={narrow ? undefined : stateTitle(presentation, since)}
      >
        {!bare && (
          <>
            {detail && (
              <span className={part}>
                <span aria-hidden>·</span>
                <span className={asks && !run.switching ? 'min-w-0 truncate text-text' : 'min-w-0 truncate'}>{detail}</span>
              </span>
            )}
            {run.linear_issue && (
              <span className={part}>
                <span aria-hidden>·</span>
                <RunLinearIssue run={run} />
              </span>
            )}
            {extraRepoCount(run) > 0 && (
              <span className={part}>
                <span aria-hidden>·</span>
                <span className="min-w-0 truncate" title={run.repos!.map((repo) => repo.name).join(', ')}>{repoLabel(run.repos![0]!.name, run)}</span>
              </span>
            )}
            <span className={part}>
              <span aria-hidden>·</span>
              <span className="min-w-0 truncate" title={[agentName, mode, run.model, run.effort].filter(Boolean).join(' · ')}>
                <span className="sr-only">{agentName} · </span>
                {mode}
              </span>
            </span>
            {account && (
              <span className={part}>
                <span aria-hidden>·</span>
                <span className="min-w-0 truncate">{account}</span>
              </span>
            )}
            {run.via && (
              <span className={part}>
                <span aria-hidden>·</span>
                <span className="min-w-0 truncate">Launched via {run.via}</span>
              </span>
            )}
          </>
        )}
      </div>
      {/* What the session costs stays when every other part has gone. */}
      {run.account_login_per_use && <Badge className="shrink-0">Billed per use</Badge>}
      {!narrow && run.permission && run.permission !== 'default' && (
        <Badge tone={run.permission === 'root' ? 'needs-you' : 'neutral'} className="shrink-0" title={permissions.find((p) => p.value === run.permission)?.detail}>{permissionLabel(run.permission)}</Badge>
      )}
      {!narrow && controls && watch && (
        <Button
          variant="quiet"
          size="sm"
          hint={watchHint(watch)}
          className="shrink-0"
          onClick={() => {
            const route = watchRoute(watch.id)
            navigate(route.name, route.params)
          }}
        >
          <Eye aria-hidden />
          <span className="sr-only">{watch.state === 'paused' ? 'Watch paused' : watch.state === 'error' ? 'Watch failing' : 'Watch on'}</span>
        </Button>
      )}
    </>
  )
}

/** The switch scrolls sideways when the row is too narrow for every view, a
 * phone with a pull request beside it for instance. It keeps the shown view's
 * tab in sight as the view or the room for it changes, and fades its right
 * edge while more tabs lie past it. */
function ViewSwitch({ runId, view, views, go, arrows, onArrow, icons }: {
  runId: string
  view: RunView
  views: RunView[]
  go: (view: RunView) => void
  arrows: FrameTile['arrows']
  onArrow: FrameTile['onArrow']
  /** Icons for words, each named by its tooltip, where the words would crowd out the state's reason. */
  icons: boolean
}) {
  const strip = useRef<HTMLDivElement>(null)
  const [more, setMore] = useState(false)
  const dragFrom = useTileDrag()
  const lifted = useStore((s) => (s.tileDrag?.runId === runId && !s.tileDrag.tabId ? s.tileDrag.view : undefined))
  const measure = () => {
    const list = strip.current
    if (list) setMore(list.scrollLeft + list.clientWidth < list.scrollWidth - 1)
  }
  useEffect(() => {
    const list = strip.current
    if (!list) return
    const observer = new ResizeObserver(() => {
      list.querySelector('[data-state=active]')?.scrollIntoView({ block: 'nearest', inline: 'nearest' })
      measure()
    })
    observer.observe(list)
    return () => observer.disconnect()
  }, [view])
  return (
    <TabsList
      ref={strip}
      look="segmented"
      aria-label="Session views"
      onScroll={measure}
      className={cn('overflow-x-auto [scrollbar-width:none]', more && '[mask-image:linear-gradient(to_right,black_calc(100%-1.5rem),transparent)]')}
    >
      {views.map((item) => {
        const Icon = runViewIcon[item]
        const arrow = arrows[item]
        const hint = [icons && runViewLabel[item], arrow?.where].filter(Boolean).join('\n')
        return (
          <Tooltip key={item} content={hint} side="bottom" disabled={!hint}>
            <TabsTrigger
              value={item}
              aria-label={icons ? runViewLabel[item] : undefined}
              className={cn('scroll-mx-6', lifted === item && 'outline-1 -outline-offset-1 outline-control outline-dashed')}
              // Every view but the shown one can be dragged to a tile, so it opens on the click, not the press.
              {...(item !== view && {
                onPointerDown: dragFrom({ runId, view: item }),
                onMouseDown: (event: React.MouseEvent<HTMLElement>) => {
                  event.preventDefault()
                  event.currentTarget.focus()
                },
                onClick: () => go(item),
              })}
              {...(arrow && {
                onPointerEnter: () => onArrow(arrow.tile),
                onPointerLeave: () => onArrow(null),
                onFocus: () => onArrow(arrow.tile),
                onBlur: () => onArrow(null),
              })}
            >
              {icons ? <Icon aria-hidden className="size-3.5" /> : runViewLabel[item]}
              {arrow && <ArrowUpRight aria-hidden className="size-2.5 text-muted" />}
            </TabsTrigger>
          </Tooltip>
        )
      })}
    </TabsList>
  )
}

export function RunHeader({
  run,
  view,
  views,
  arrows,
  onArrow,
  agent,
  nav,
  keyboard,
  detailsOpen,
  onDetails,
  onComment,
  onCaptures,
  onEvents,
  menuAt,
  onMenuClose,
  agentName,
  agentEntry,
  workflow,
  onWorkflow,
  controls,
}: {
  run: RunRecord
  agentName: string
  agentEntry: AgentInfo | undefined
  /** The Session tree chip, which is the state's dot and label on a wide desktop tile. */
  workflow: React.ReactNode
  /** Opens the session tree, which a phone or a narrow tile lists under the session's actions instead of the chip. */
  onWorkflow: (returnTo: HTMLElement | null) => void
  view: RunView
  views: RunView[]
  arrows: FrameTile['arrows']
  onArrow: FrameTile['onArrow']
  agent: AgentTerminal
  nav: RunNavigation
  /** The phone's soft keyboard is up, so the header folds to one row. */
  keyboard: boolean
  detailsOpen: boolean
  onDetails: (returnTo: HTMLElement | null) => void
  onComment: (returnTo: HTMLElement | null) => void
  onCaptures: (returnTo: HTMLElement | null) => void
  onEvents: (returnTo: HTMLElement | null) => void
  /** This desktop tile carries the session's controls; its other tiles show the switch and the state alone. */
  controls: boolean
  /** Where a right-click opened the session's menu, desktop only. */
  menuAt: MenuPoint | null
  onMenuClose: () => void
}) {
  const mobile = useIsMobile()
  // The row is sized from itself, not its tile: docked Details and Session tree take their width out of it.
  const [rowRef, row] = useElementSize<HTMLElement>()
  const narrow = row.width > 0 && row.width < narrowBelow
  const icons = row.width > 0 && row.width < 800
  const primary = usePrimaryAction(run, view, agent, nav)
  const modeSwitch = useModeSwitch(run, agent, agentEntry)
  const pullRequest = useRunPullRequest(run, agent, agentName)
  const editor = useOpenInEditor(run)
  const childRunsItems = useChildRunsItems(run)
  const focused = useContext(FrameFocusContext)
  const reducedMotion = useReducedMotion()
  useHeaderPrimary(focused && Boolean(primary))
  const extra = [
    { id: 'comment', label: 'Leave a comment…', Icon: MessageSquarePlus, onSelect: onComment },
    ...(modeSwitch.item ? [modeSwitch.item] : []),
    ...(editor.item ? [editor.item] : []),
    ...childRunsItems,
    { id: 'captures', label: 'Captures…', Icon: Camera, onSelect: onCaptures },
    { id: 'events', label: 'Raw events…', Icon: ScrollText, onSelect: onEvents },
  ]
  const dialogs = (
    <>
      {modeSwitch.dialog}
      {pullRequest.panel}
      {editor.dialog}
    </>
  )

  if (mobile) {
    return (
      <>
        <PhoneRunHeader
          run={run}
          view={view}
          views={views}
          nav={nav}
          agentName={agentName}
          keyboard={keyboard}
          primary={primary}
          detailsOpen={detailsOpen}
          onDetails={onDetails}
          onWorkflow={onWorkflow}
          extra={extra}
          pullRequest={pullRequest.status}
        />
        {!keyboard && <WaitLine run={run} />}
        {!keyboard && <RunLineage run={run} />}
        <RunRetention run={run} className="border-b border-seam px-4 py-2" />
        {dialogs}
      </>
    )
  }

  const Heading = focused ? 'h1' : 'h2'
  return (
    <>
      <header ref={rowRef} data-slot="pane-header" className="@container/views relative flex h-8 shrink-0 items-center gap-1.5 border-b border-seam px-2 coarse:h-11">
        <Heading tabIndex={-1} className="sr-only">{runLabel(run)}</Heading>
        <ViewSwitch runId={run.id} view={view} views={views} go={nav.go} arrows={arrows} onArrow={onArrow} icons={icons} />
        <StateLine run={run} agentName={agentName} narrow={narrow} controls={controls} chip={controls && !narrow ? workflow : undefined} />
        {controls && (
          <div role="toolbar" aria-label="Session actions" className="flex shrink-0 items-center gap-1">
            {!narrow && pullRequest.status}
            {primary && <Button size="sm" variant={focused ? 'primary' : 'secondary'} onClick={primary.act}>{primary.label}</Button>}
            {run.mode === 'acp' && <PresenceControls run={run} agent={agent} wide={!narrow} />}
            <Button
              variant="ghost"
              size="icon"
              label={detailsOpen ? 'Hide details' : 'Show details'}
              hint={`Details (${shortcutLabel('run-details')})`}
              aria-expanded={detailsOpen}
              aria-controls="run-details"
              className={cn(detailsOpen && 'bg-hover-chrome text-text')}
              onClick={(event) => onDetails(event.currentTarget)}
            >
              <PanelRight />
            </Button>
            <RunActions
              run={run}
              compact
              extra={narrow ? [{ id: 'workflow', label: 'Session tree', Icon: Network, onSelect: onWorkflow }, ...pullRequest.more, ...extra] : extra}
              at={menuAt}
              onAtClose={onMenuClose}
            />
          </div>
        )}
        {controls && agent.takeover.holderProgress !== undefined && !agent.takeover.review && (
          <ProgressBar
            role="progressbar"
            aria-label="A teammate is holding to request control"
            value={reducedMotion ? 1 : agent.takeover.holderProgress}
            max={1}
            className="absolute inset-x-0 bottom-0 h-0.5 rounded-none bg-transparent"
            fillClassName="rounded-none bg-state-failed"
          />
        )}
      </header>
      {/* Drawn in each of the session's tiles so that focus moves no pane, and read out from one. */}
      <div aria-hidden={!controls || undefined} className="contents">
        {run.mode === 'acp' && <PresenceErrors run={run} agent={agent} />}
      </div>
      <RunLineage run={run} />
      <RunRetention run={run} className="border-b border-seam px-4 py-2" />
      <div aria-hidden={!controls || undefined} className="contents">
        <ConnectionLine className="border-b border-seam px-4 py-1" />
      </div>
      {dialogs}
    </>
  )
}
