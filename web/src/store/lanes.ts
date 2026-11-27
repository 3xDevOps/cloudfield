import { isReread } from '@/lib/events'
import type { Event } from '@/lib/types'
import type { SliceCreator } from '@/store/slice'

export type LaneWindow = '1h' | '6h' | '24h' | '7d'
export type LaneGroup = 'status' | 'owner'
export type LaneEdgeKind = 'message' | 'overlap'

export const laneWindows: LaneWindow[] = ['1h', '6h', '24h', '7d']
export const laneEdgeKinds: LaneEdgeKind[] = ['message', 'overlap']

/** The event types Lanes reads: state changes for the bars and agent mail for the edges. */
export const laneEventTypes = ['run.status', 'coord.message', 'coord.message.acked']

/** Events held per workspace at least. A busy workspace's window can hold far more; past the budget the bars say history is not loaded. */
export const historyBudget = 5000
/** What **Load more** adds to the budget. */
export const historyStep = 10_000

const hour = 3_600_000

/**
 * The budget a window of `span` ms starts with. Held events cost the browser
 * about 0.3 KB of heap each and one re-derive of the view per 1,000-event
 * page, so 25,000 is about 8 MB a workspace.
 */
export function historyBudgetFor(span: number): number {
  if (span <= 6 * hour) return historyBudget
  return span <= 24 * hour ? 10_000 : 25_000
}

const arrivalsKept = 64

/** One workspace's history, read backward from the log head. */
export interface LaneHistory {
  /** Oldest first, unique by seq. */
  events: Event[]
  /** The next page's `before_seq`; undefined once the log is exhausted. */
  olderSeq?: number
  /** The time of the oldest event read; -Infinity once nothing older exists. */
  floor: number
  loading: boolean
  error: string | null
  /** Paging stopped on its event budget with older history unread. */
  capped: boolean
}

export interface LanesSlice {
  laneHistory: Record<string, LaneHistory>
  /** Agent mail that arrived live while a Lanes view was mounted, newest last; history pages never add to it. */
  laneArrivals: string[]
  /** The Lanes views mounted. History outlives them: live events keep it current until the event stream reconnects. */
  laneViews: number
  /** Events held per workspace now: the widest window's budget so far, plus what Load more added. */
  laneBudget: number
  /** Null follows the default: 1h while anything works, else 6h. */
  laneWindow: LaneWindow | null
  laneGroup: LaneGroup
  laneHidden: LaneEdgeKind[]
  /** The phone's Sessions tab shows lanes instead of the list. */
  sessionsLanes: boolean
  holdLanes: () => () => void
  /** Raises the budget to `budget`; it never falls while a view holds history. */
  raiseLaneBudget: (budget: number) => void
  appendLaneEvent: (event: Event) => void
  setLaneHistory: (workspaceID: string, patch: Partial<LaneHistory>, older?: Event[]) => void
  setLaneWindow: (window: LaneWindow | null) => void
  setLaneGroup: (group: LaneGroup) => void
  setLaneHidden: (hidden: LaneEdgeKind[]) => void
  setSessionsLanes: (lanes: boolean) => void
}

export const emptyLaneHistory: LaneHistory = { events: [], floor: Infinity, loading: false, error: null, capped: false }

export function normalizeLaneWindow(value: unknown): LaneWindow | null {
  return laneWindows.includes(value as LaneWindow) ? (value as LaneWindow) : null
}

export function normalizeLaneHidden(value: unknown): LaneEdgeKind[] {
  return Array.isArray(value) ? laneEdgeKinds.filter((kind) => value.includes(kind)) : []
}

function merged(events: Event[], more: Event[]): Event[] {
  const seen = new Set(events.map((event) => event.seq))
  const fresh = more.filter((event) => !seen.has(event.seq))
  return fresh.length ? [...events, ...fresh].sort((a, b) => a.seq - b.seq) : events
}

export const createLanesSlice: SliceCreator<LanesSlice> = (set, get) => ({
  laneHistory: {},
  laneArrivals: [],
  laneViews: 0,
  laneBudget: historyBudget,
  laneWindow: null,
  laneGroup: 'status',
  laneHidden: [],
  sessionsLanes: false,
  holdLanes: () => {
    set((s) => ({ laneViews: s.laneViews + 1 }))
    return () => set((s) => (s.laneViews === 1 ? { laneViews: 0, laneArrivals: [] } : { laneViews: s.laneViews - 1 }))
  },
  raiseLaneBudget: (budget) => set((s) => (budget > s.laneBudget ? { laneBudget: budget } : s)),
  appendLaneEvent: (event) => {
    const s = get()
    if (!laneEventTypes.includes(event.type) || isReread(event)) return
    const history = s.laneHistory[event.workspace_id]
    if (!history) return
    // Nearly every live event is newer than all held, which needs no search and no sort.
    const newest = history.events.at(-1)
    const inOrder = !newest || event.seq > newest.seq
    if (!inOrder && history.events.some((e) => e.seq === event.seq)) return
    const events = inOrder ? [...history.events, event] : merged(history.events, [event])
    const drop = events.length - s.laneBudget
    // Dropping the oldest moves the boundary up to the oldest kept, so bars before it read History not loaded.
    const next = drop > 0 ? { events: events.slice(drop), floor: Date.parse(events[drop].time), olderSeq: events[drop].seq, capped: true } : { events }
    const id = event.type === 'coord.message' ? (event.payload as { message_id?: string }).message_id : undefined
    set((state) => ({
      laneHistory: { ...state.laneHistory, [event.workspace_id]: { ...history, ...next } },
      ...(id && state.laneViews > 0 && { laneArrivals: [...state.laneArrivals.slice(1 - arrivalsKept), id] }),
    }))
  },
  setLaneHistory: (workspaceID, patch, older = []) =>
    set((s) => {
      const history = s.laneHistory[workspaceID] ?? emptyLaneHistory
      return {
        laneHistory: {
          ...s.laneHistory,
          [workspaceID]: { ...history, ...patch, events: merged(history.events, older) },
        },
      }
    }),
  setLaneWindow: (laneWindow) => set({ laneWindow }),
  setLaneGroup: (laneGroup) => set({ laneGroup }),
  setLaneHidden: (laneHidden) => set({ laneHidden }),
  setSessionsLanes: (sessionsLanes) => set({ sessionsLanes }),
})
