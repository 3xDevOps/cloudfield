import type { Event } from '@/lib/types'
import { createRootStore } from '@/store'
import { historyBudget, historyBudgetFor } from '@/store/lanes'

const event = (seq: number, type = 'run.status', workspace = 'wsp_1'): Event => ({
  id: `e${seq}`, seq, time: '2026-08-14T10:00:00Z', workspace_id: workspace, run_id: 'run_1', actor_id: '', type, payload: {},
})

describe('lane history', () => {
  it('keeps live lane events only while a view holds a read workspace, once each, and drops them on release', () => {
    const store = createRootStore()
    store.getState().appendLaneEvent(event(1))
    expect(store.getState().laneHistory).toEqual({})
    const release = store.getState().holdLanes()
    store.getState().setLaneHistory('wsp_1', { floor: -Infinity }, [event(2)])
    const reread = { ...event(6), payload: { reread: true } }
    for (const e of [event(3), event(3), event(1), event(4, 'run.diff'), event(5, 'run.status', 'wsp_2'), reread]) store.getState().appendLaneEvent(e)
    expect(store.getState().laneHistory.wsp_1.events.map((e) => e.seq)).toEqual([1, 2, 3])
    expect(store.getState().laneHistory.wsp_2).toBeUndefined()
    release()
    expect(store.getState().laneHistory).toEqual({})
  })

  it('sizes the budget by the window, raises it on request and forgets the raise with the last view', () => {
    const hour = 3_600_000
    expect([1, 6, 24, 168].map((hours) => historyBudgetFor(hours * hour))).toEqual([5000, 5000, 10_000, 25_000])
    const store = createRootStore()
    const release = store.getState().holdLanes()
    const other = store.getState().holdLanes()
    store.getState().raiseLaneBudget(25_000)
    store.getState().raiseLaneBudget(10_000)
    expect(store.getState().laneBudget).toBe(25_000)
    other()
    expect(store.getState().laneBudget).toBe(25_000)
    release()
    expect(store.getState().laneBudget).toBe(historyBudget)
    // A server reset drops history, and the budget raised for it.
    store.getState().holdLanes()
    store.getState().raiseLaneBudget(35_000)
    store.getState().resetSeq()
    expect(store.getState().laneBudget).toBe(historyBudget)
  })

  it('keeps the newest events up to the budget and moves the history floor to the oldest kept', () => {
    const store = createRootStore()
    store.getState().holdLanes()
    const timed = (seq: number) => ({ ...event(seq), time: new Date(Date.parse('2026-08-14T10:00:00Z') + seq * 1000).toISOString() })
    store.getState().setLaneHistory('wsp_1', { floor: -Infinity }, Array.from({ length: historyBudget }, (_, i) => timed(i + 1)))
    store.getState().appendLaneEvent(timed(historyBudget + 1))
    const history = store.getState().laneHistory.wsp_1
    expect(history.events).toHaveLength(historyBudget)
    expect(history.events[0].seq).toBe(2)
    expect(history.floor).toBe(Date.parse(timed(2).time))
    expect(history.olderSeq).toBe(2)
    expect(history.capped).toBe(true)
    // A raised budget keeps what the smaller one would drop.
    store.getState().raiseLaneBudget(historyBudget + 1)
    store.getState().appendLaneEvent(timed(historyBudget + 2))
    expect(store.getState().laneHistory.wsp_1.events).toHaveLength(historyBudget + 1)
  })
})
