import { useCallback, useContext, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useAssistantDocked } from '@/components/assistant/panel'
import { MissingRun } from '@/components/missing-run'
import { TileArea } from '@/components/tiles/tile-area'
import { replaceRouteKeepingFocus } from '@/components/shell/center-view'
import { Dialog, DialogContent, DialogTitle } from '@/components/ui/dialog'
import { Tabs, TabsContent } from '@/components/ui/tabs'
import { minReadableWidth, useIsMobile } from '@/lib/breakpoints'
import { useElementSize } from '@/lib/hooks'
import { useKeybindings } from '@/lib/keybindings'
import { redirectRoute } from '@/lib/url-state'
import { cn } from '@/lib/utils'
import { BrowserView } from '@/routes/browser'
import { ChangesView } from '@/routes/diff'
import { RunFiles } from '@/routes/files/run-files'
import { registerRoute, type RouteProps } from '@/routes/registry'
import { FrameFocusContext, RunAgentContext, RunParamsContext } from '@/routes/run/agent-context'
import { CapturesDialog } from '@/routes/run/captures'
import { RawEventsDialog } from '@/routes/run/raw-events'
import { CompletionPrompt } from '@/routes/run/completion-prompt'
import { Composer } from '@/routes/run/composer'
import { RunDetails } from '@/routes/run/details'
import { RunHeader, type RunNavigation } from '@/routes/run/header'
import { MultiplayerControls, useSharedPresence } from '@/routes/run/multiplayer-controls'
import { useKeyboardFold } from '@/routes/run/phone-header'
import { dockedRequestID } from '@/routes/run/request-dock'
import { notesRailMinWidth } from '@/routes/run/session-notes'
import { RunSession } from '@/routes/run/session-host'
import { SessionView } from '@/routes/run/session-view'
import { WorkflowChip, WorkflowPanel, workflowPanelID } from '@/routes/run/workflow-panel'
import { SubagentInspector } from '@/routes/run/subagent-inspector'
import { TerminalView } from '@/routes/run/terminal-view'
import { TraceView } from '@/routes/run/trace-view'
import { defaultView, isRunView, offeredViews, runRoute, type RunView } from '@/routes/run/views'
import { useStore } from '@/store'
import { useCapability } from '@/store/hooks'
import type { RunRecord } from '@/store/runs'
import { resolveView } from '@/store/tiles'
import type { NavigateOptions } from '@/store/ui'

/** Inactive views stay laid out but invisible: a terminal hidden with
 * display:none measures zero and would resize the shared PTY. */
const panel = 'absolute inset-0 outline-none'

/** The inline inspectors' widths: `w-80`, `w-[28rem]` and `w-80`. */
const detailsWidth = 320
const agentWidth = 448
const workflowWidth = 320

function RunRoute({ params }: RouteProps) {
  return useIsMobile() ? <PhoneRun params={params} /> : <TileArea params={params} Frame={RunFrame} />
}

/** A phone shows the route's session alone, with no tiles. */
function PhoneRun({ params }: RouteProps) {
  const run = useStore((s) => s.runs[params.runId])
  const identityKey = useStore((s) => s.identityKey)
  const epoch = useStore((s) => s.terminalCacheEpoch)
  const cap = useCapability()
  const memory = useStore((s) => s.runViewMemory)
  if (!run) return <MissingRun />
  const view = resolveView({ runId: run.id, view: isRunView(params.view) ? params.view : undefined }, run, cap, memory)
  return (
    <RunSession key={JSON.stringify([identityKey, epoch, run.id, run.created_at])} run={run} views={[view]}>
      <RunFrame run={run} params={params} />
    </RunSession>
  )
}

/** Where a frame sits among the session tiles. */
export interface FrameTile {
  focused: boolean
  /** This tile carries the session's controls: its focused tile, else its first. */
  controls: boolean
  /** No other tile is on screen. */
  alone: boolean
  /** This tile mounts the session's one Terminal. */
  terminalHost: boolean
  /** Another tile on screen shows this session's Chat. */
  chatOnScreen: boolean
  /** The views of this session that other tabs hold. */
  elsewhere: RunView[]
  /** Of those, the ones on screen in another tile, with that tile and the words that say where it is. */
  arrows: Partial<Record<RunView, { tile: string; where: string }>>
  /** Marks a tile while the arrow of the view it shows is pointed at. */
  onArrow: (tileId: string | null) => void
}

const lone: FrameTile = { focused: true, controls: true, alone: true, terminalHost: true, chatOnScreen: false, elsewhere: [], arrows: {}, onArrow: () => {} }

function useVisited(view: RunView, elsewhere: RunView[]) {
  const visited = useRef(new Set<RunView>())
  for (const held of elsewhere) visited.current.delete(held)
  visited.current.add(view)
  return visited.current
}

/**
 * One view of a session, inside a `RunSession`. Without `tile` it is the only
 * frame on screen and registers `leave-run` itself. `params` are the route's
 * in the focused tile and the tab's view and repo elsewhere.
 */
export function RunFrame({ run, params, tile }: { run: RunRecord; params: RouteProps['params']; tile?: FrameTile }) {
  const { focused, controls, alone, terminalHost, chatOnScreen, elsewhere, arrows, onArrow } = tile ?? lone
  const { agent, agentName, agentEntry, room, shells } = useContext(RunAgentContext)!
  const cap = useCapability()
  const mobile = useIsMobile()
  // Measured with the inspectors inside it, so opening one never changes the answer.
  const [rowRef, row] = useElementSize<HTMLDivElement>()
  const measured = row.width > 0
  const inline = alone && row.width >= detailsWidth + minReadableWidth
  const agentInline = alone && row.width >= agentWidth + minReadableWidth
  const navigate = useStore((s) => s.navigate)
  const memory = useStore((s) => s.runViewMemory)
  const detailsPreference = useStore((s) => s.detailsOpen)
  const setDetailsPreference = useStore((s) => s.setDetailsOpen)
  const [overlayDetails, setOverlayDetails] = useState(false)
  const workflowPreference = useStore((s) => s.workflowOpen)
  const setWorkflowPreference = useStore((s) => s.setWorkflowOpen)
  const [overlayWorkflow, setOverlayWorkflow] = useState(false)
  const [dialog, setDialog] = useState<'captures' | 'events' | null>(null)
  const returnTo = useRef<HTMLElement | null>(null)
  const [noteFocus, setNoteFocus] = useState(0)
  const textarea = useRef<HTMLTextAreaElement>(null)
  const dockedTextarea = useRef<HTMLTextAreaElement>(null)
  const [dockedOpen, setDockedOpen] = useState(false)
  useEffect(() => {
    if (!focused) setDockedOpen(false)
  }, [focused])

  const browserRequest = useStore((s) => s.browserRequests[run.id])
  const requestBrowser = useStore((s) => s.requestBrowser)
  const [paneRef, pane] = useElementSize<HTMLDivElement>()
  const notesWide = pane.width >= notesRailMinWidth

  const views = offeredViews(run, cap)
  const browser = views.includes('browser')
  const files = views.includes('files')
  const view = resolveView({ runId: run.id, view: isRunView(params.view) ? params.view : undefined }, run, cap, memory)
  const visited = useVisited(view, elsewhere)
  const setShownRunView = useStore((s) => s.setShownRunView)
  useEffect(() => {
    if (focused) setShownRunView(run.id, view)
  }, [focused, setShownRunView, run.id, view])

  // A tile off Chat keeps a field for the agent unless the session's Chat is on screen. A pending request is answered in Chat,
  // and a Browser or a Terminal keeps its height, which a field that opens and folds would change.
  const requested = useStore((s) => Boolean(s.acpSessions[run.id]?.pending.length))
  const docked = Boolean(tile) && run.acp === true && agent.steerable && !requested && (view === 'changes' || view === 'files' || view === 'trace') && !chatOnScreen

  const switchable = agentEntry?.switchable === true
  const shared = useSharedPresence(run, agent)
  const keyboard = useKeyboardFold()

  const agentID = focused && defaultView(run) === 'session' ? params.agent : undefined
  const agentOpener = useRef<HTMLElement | null>(null)
  const inlineNow = useRef(agentInline)
  inlineNow.current = agentInline
  const closeAgent = useCallback(() => redirectRoute(useStore, { name: 'run', params: { runId: run.id, view } }), [run.id, view])
  // An inline inspector squeezed out by the assistant docking closes rather than reopening as a modal over it.
  // The assistant docks a render before the row's new width arrives, so compare against the last width's state.
  const assistantDocked = useAssistantDocked()
  const rowBefore = useRef({ width: row.width, docked: assistantDocked })
  const resized = rowBefore.current.width !== row.width
  const squeezed = Boolean(agentID) && resized && !agentInline && assistantDocked && !rowBefore.current.docked
    && rowBefore.current.width >= agentWidth + minReadableWidth
  useEffect(() => {
    if (resized) rowBefore.current = { width: row.width, docked: assistantDocked }
    if (squeezed) replaceRouteKeepingFocus({ name: 'run', params: { runId: run.id, view } })
  }, [resized, squeezed, row.width, assistantDocked, run.id, view])

  // The Session tree rail sits beside the session when it fits; otherwise it opens as a sheet.
  const workflowInline = alone && row.width >= workflowWidth + minReadableWidth
  const besideDetails = row.width >= workflowWidth + detailsWidth + minReadableWidth
  const besideAgent = row.width >= workflowWidth + agentWidth + minReadableWidth
  const detailsOpen = inline ? detailsPreference && !agentID : overlayDetails
  const workflowOpen = workflowInline
    ? workflowPreference && (!agentID || besideAgent) && (!(inline && detailsOpen) || besideDetails)
    : overlayWorkflow
  const setDetails = useCallback((open: boolean) => {
    if (inline && open && agentID) closeAgent()
    if (inline) setDetailsPreference(open)
    else setOverlayDetails(open)
  }, [inline, setDetailsPreference, agentID, closeAgent])
  // A view change from inside the sheet, such as the git card's Review changes, leaves the sheet.
  useEffect(() => setOverlayWorkflow(false), [view])
  const setWorkflow = useCallback((open: boolean) => {
    if (!workflowInline) {
      setOverlayWorkflow(open)
      return
    }
    if (open && agentID && !besideAgent) closeAgent()
    if (open && inline && detailsOpen && !besideDetails) setDetailsPreference(false)
    setWorkflowPreference(open)
  }, [workflowInline, agentID, besideAgent, closeAgent, inline, detailsOpen, besideDetails, setDetailsPreference, setWorkflowPreference])
  useLayoutEffect(() => {
    if (!agentID) return
    agentOpener.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
    return () => {
      if (inlineNow.current && agentOpener.current?.isConnected) agentOpener.current.focus()
    }
  }, [Boolean(agentID)])

  // A phone's tabs swap the view in place, so back leaves the session instead of stepping through them.
  const go = useCallback((next: RunView, options?: NavigateOptions) => {
    if (mobile) {
      replaceRouteKeepingFocus(runRoute(run.id, next))
      if (options?.remember !== false) useStore.getState().rememberRunView(run.id, next)
    } else navigate('run', { runId: run.id, view: next }, options)
  }, [mobile, navigate, run.id])
  useEffect(() => {
    if (controls && browserRequest !== undefined && browser) go('browser')
  }, [controls, browserRequest, browser, go])
  const openDialog = (kind: 'captures' | 'events') => (opener: HTMLElement | null) => {
    returnTo.current = opener
    setDialog(kind)
  }
  const openCaptures = openDialog('captures')
  const nav: RunNavigation = {
    go,
    reveal: (cardID) => {
      returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      setDetails(true)
      requestAnimationFrame(() => requestAnimationFrame(() => {
        const card = document.getElementById(cardID)
        card?.scrollIntoView({ block: 'nearest' })
        ;(card?.querySelector<HTMLElement>('textarea') ?? card)?.focus()
      }))
    },
    focusComposer: () => {
      if (!mobile) return navigate('run', { runId: run.id, view: 'session', focus: 'composer' }, { remember: false })
      go('session', { remember: false })
      requestAnimationFrame(() => textarea.current?.focus())
    },
    focusRequest: () => {
      if (!inline) setOverlayDetails(false)
      go('session', { remember: false })
      requestAnimationFrame(() => requestAnimationFrame(() => document.getElementById(dockedRequestID(run.id))?.focus()))
    },
  }

  const [composeRequested, setComposeRequested] = useState(false)
  useEffect(() => {
    if (!focused || params.focus !== 'composer') return
    setComposeRequested(true)
    // Served once: a tile focused again would otherwise take the keyboard back. The address never carried it.
    useStore.setState((s) => ({ route: { ...s.route, params: Object.fromEntries(Object.entries(s.route.params).filter(([key]) => key !== 'focus')) } }))
  }, [focused, params])

  const free = views.filter((item) => !elsewhere.includes(item))
  const step = (delta: number) => go(free[(free.indexOf(view) + delta + free.length) % free.length]!)
  useKeybindings('run', focused ? {
    ...(!tile && { 'leave-run': () => (agentID ? closeAgent() : navigate('board')) }),
    'run-view-next': () => step(1),
    'run-view-previous': () => step(-1),
    'run-details': (event) => {
      event.preventDefault()
      returnTo.current = document.activeElement instanceof HTMLElement ? document.activeElement : null
      setDetails(!detailsOpen)
    },
    'focus-composer': () => (dockedTextarea.current ? dockedTextarea.current.focus() : nav.focusComposer()),
  } : {})
  useKeybindings('browser', {
    'browser-address': browser && focused ? (event) => {
      // Stopped as well, so a focused page is not sent the key.
      event.preventDefault()
      event.stopPropagation()
      requestBrowser(run.id, '')
    } : undefined,
  })

  // Chat's rail only shows open comments while Chat itself is on screen;
  // otherwise Details must keep showing them, or they appear nowhere.
  const notesShownInRail = view === 'session' && notesWide
  const details = (inset: boolean) => (
    <RunDetails run={run} view={view} agent={agent} agentName={agentName} room={room} nav={nav} inset={inset} noteFocus={noteFocus} notesWide={notesShownInRail} />
  )

  return (
    <RunParamsContext value={params}>
    <FrameFocusContext value={focused}>
    <Tabs value={view} onValueChange={(next) => isRunView(next) && go(next)} asChild>
    <div ref={rowRef} className="flex h-full min-w-0">
      <div className="flex min-w-0 flex-1 flex-col">
        <RunHeader
          run={run}
          view={view}
          views={views}
          arrows={arrows}
          onArrow={onArrow}
          agent={agent}
          nav={nav}
          keyboard={mobile && keyboard}
          detailsOpen={detailsOpen}
          onDetails={(opener) => {
            returnTo.current = opener
            setDetails(!detailsOpen)
          }}
          onCaptures={openCaptures}
          onEvents={openDialog('events')}
          onWorkflow={(opener) => {
            returnTo.current = opener
            setWorkflow(true)
          }}
          agentName={agentName}
          agentEntry={agentEntry}
          controls={controls}
          workflow={(
            <WorkflowChip
              run={run}
              open={workflowOpen}
              onToggle={(opener) => {
                returnTo.current = opener
                setWorkflow(!workflowOpen)
              }}
            />
          )}
        />
        {mobile && run.mode === 'acp' && view !== 'browser' && view !== 'files' && shared && !keyboard && view !== 'terminal' && <MultiplayerControls run={run} agent={agent} onPeople={() => nav.reveal('details-people')} />}
        <div ref={paneRef} className="relative min-h-0 min-w-0 flex-1">
          {visited.has('session') && (
            <TabsContent value="session" forceMount inert={view !== 'session'} className={cn(panel, view !== 'session' && 'invisible')}>
              <SessionView
                run={run}
                agent={agent}
                agentName={agentName}
                room={room}
                nav={nav}
                active={view === 'session'}
                textarea={textarea}
                focusComposer={composeRequested}
                onComposing={(focused) => {
                  if (focused) setComposeRequested(false)
                }}
                shells={shells}
                switchable={switchable}
                notesWide={notesWide}
              />
            </TabsContent>
          )}
          {view === 'trace' && (
            <TabsContent value="trace" className={panel}>
              <TraceView run={run} nav={nav} />
            </TabsContent>
          )}
          {terminalHost && (
            <TabsContent value="terminal" forceMount inert={view !== 'terminal'} className={cn(panel, view !== 'terminal' && 'invisible')}>
              <TerminalView run={run} agent={agent} shells={shells} onCaptures={openCaptures} />
            </TabsContent>
          )}
          {visited.has('changes') && (
            <TabsContent value="changes" forceMount inert={view !== 'changes'} className={cn(panel, view !== 'changes' && 'invisible')}>
              <ChangesView runID={run.id} agent={agent} agentName={agentName} />
            </TabsContent>
          )}
          {files && visited.has('files') && (
            <TabsContent value="files" forceMount inert={view !== 'files'} className={cn(panel, view !== 'files' && 'invisible')}>
              <RunFiles run={run} shown={view === 'files'} />
            </TabsContent>
          )}
          {view === 'browser' && (
            <TabsContent value="browser" className={panel}>
              <BrowserView runID={run.id} agent={agent} agentName={agentName} onCaptures={openCaptures} />
            </TabsContent>
          )}
        </div>
        {view !== 'session' && <CompletionPrompt run={run} className="border-t border-seam p-2" />}
        {docked && (
          <Composer
            run={run}
            agent={agent}
            room={room}
            textarea={dockedTextarea}
            docked
            collapsed={!focused || !dockedOpen}
            onFocusChange={(composing) => {
              if (composing) setDockedOpen(true)
            }}
            onEscape={() => {
              setDockedOpen(false)
              dockedTextarea.current?.blur()
            }}
          />
        )}
      </div>
      {agentInline && agentID && (
        <aside aria-label="Subagent" className="flex w-[28rem] shrink-0 flex-col border-l border-seam bg-canvas">
          <SubagentInspector key={agentID} run={run} agentID={agentID} agent={agent} nav={nav} onClose={closeAgent} />
        </aside>
      )}
      {workflowInline && workflowOpen && (
        <aside id={workflowPanelID} aria-label="Session tree" className="flex w-80 shrink-0 flex-col border-l border-seam bg-canvas">
          <WorkflowPanel run={run} view={view} agent={agent} nav={nav} onClose={() => setWorkflow(false)} />
        </aside>
      )}
      {!workflowInline && (
        <Dialog open={workflowOpen} onOpenChange={setWorkflow}>
          <DialogContent
            id={workflowPanelID}
            variant={mobile ? 'bottom' : 'side'}
            showCloseButton={false}
            aria-describedby={undefined}
            onCloseAutoFocus={(event) => {
              event.preventDefault()
              if (returnTo.current?.isConnected) returnTo.current.focus()
            }}
            className={mobile ? 'flex max-h-[85dvh] min-h-0 flex-col p-0' : 'flex w-80 flex-col p-0'}
          >
            <DialogTitle className="sr-only">Session tree</DialogTitle>
            <WorkflowPanel
              run={run}
              view={view}
              agent={agent}
              nav={nav}
              onClose={() => setWorkflow(false)}
              onLeave={() => {
                returnTo.current = null
                setWorkflow(false)
              }}
              className="flex-1"
            />
          </DialogContent>
        </Dialog>
      )}
      {inline && detailsOpen && (
        <aside id="run-details" aria-label="Session details" className="w-80 shrink-0 overflow-y-auto border-l border-seam bg-canvas">
          {details(true)}
        </aside>
      )}
      {!inline && (
        <Dialog open={detailsOpen} onOpenChange={setDetails}>
          <DialogContent
            id="run-details"
            variant={mobile ? 'bottom' : 'side'}
            aria-describedby={undefined}
            onOpenAutoFocus={(event) => {
              event.preventDefault()
              ;(event.currentTarget as HTMLElement).focus()
            }}
            onCloseAutoFocus={(event) => {
              if (!returnTo.current?.isConnected) return
              event.preventDefault()
              returnTo.current.focus()
            }}
            className={mobile ? undefined : 'w-80'}
          >
            <div className="flex min-h-0 flex-col gap-1 overflow-y-auto px-4 pt-4 max-md:p-0">
              <DialogTitle>Session details</DialogTitle>
              {details(false)}
            </div>
          </DialogContent>
        </Dialog>
      )}
      {measured && !agentInline && !squeezed && (
        <Dialog open={Boolean(agentID)} onOpenChange={(open) => !open && closeAgent()}>
          <DialogContent
            variant={mobile ? 'bottom' : 'side'}
            showCloseButton={false}
            aria-describedby={undefined}
            onOpenAutoFocus={(event) => event.preventDefault()}
            onCloseAutoFocus={(event) => {
              if (!agentOpener.current?.isConnected) return
              event.preventDefault()
              agentOpener.current.focus()
            }}
            className={mobile ? 'flex h-[calc(85dvh-var(--keyboard-inset,0px))] min-h-0 flex-col p-0' : 'w-[28rem]'}
          >
            <DialogTitle className="sr-only">Subagent</DialogTitle>
            {agentID && <SubagentInspector key={agentID} run={run} agentID={agentID} agent={agent} nav={nav} onClose={closeAgent} />}
          </DialogContent>
        </Dialog>
      )}
      <CapturesDialog
        runID={run.id}
        workspaceID={run.workspace_id}
        open={dialog === 'captures'}
        returnTo={returnTo.current}
        onOpenChange={(open) => setDialog(open ? 'captures' : null)}
        onAnswer={(fact) => {
          setDialog(null)
          useStore.getState().setNoteDraft(run.id, fact)
          setNoteFocus((n) => n + 1)
          setDetails(true)
        }}
      />
      <RawEventsDialog
        run={run}
        open={dialog === 'events'}
        onOpenChange={(open) => setDialog(open ? 'events' : null)}
        returnTo={returnTo.current}
      />
    </div>
    </Tabs>
    </FrameFocusContext>
    </RunParamsContext>
  )
}

registerRoute('run', RunRoute)
