import { renderHook, waitFor } from '@testing-library/react'
import type { Event, RunMessage } from '@/lib/types'
import { createRootStore } from '@/store'
import { loadMessagePage, scopeMessages, useMessageList, type MessageScope } from '@/store/messages'
import { applyEvent, hydrate } from '@/store/sync'
import { fakeApi, run, workspace } from '@/test/fixtures'

function runMessage(over: Partial<RunMessage> = {}): RunMessage {
  return {
    id: 'msg-1',
    workspace_id: workspace.id,
    from_run_id: 'run_worker',
    to_run_id: 'run_1',
    kind: 'message',
    body: 'hello',
    created_at: '2026-10-05T10:00:00Z',
    ...over,
  }
}

const workspaceScope: MessageScope = { kind: 'workspace', workspaceID: workspace.id }
const threadScope: MessageScope = { kind: 'thread', workspaceID: workspace.id, correlationID: 'corr-1' }
const runScope: MessageScope = { kind: 'run', workspaceID: workspace.id, runID: 'run_1' }

const ids = (messages: RunMessage[]) => messages.map((m) => m.id)

describe('messages slice', () => {
  it('keeps a session\'s list after its last viewer, until the session leaves the store, and drops a thread\'s with its last', async () => {
    const store = createRootStore()
    store.getState().upsertRun(run())
    const client = fakeApi({ coordMessagesList: vi.fn(async () => ({ messages: [runMessage({ id: 'a' })] })) })
    const first = renderHook(() => useMessageList(store, client, runScope))
    const second = renderHook(() => useMessageList(store, client, { ...runScope }))
    const thread = renderHook(() => useMessageList(store, client, threadScope))
    await waitFor(() => expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a']))
    await waitFor(() => expect(ids(scopeMessages(store.getState(), threadScope))).toEqual(['a']))

    thread.unmount()
    expect(store.getState().messageLists['thread:corr-1']).toBeUndefined()
    first.unmount()
    second.unmount()
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a'])

    store.getState().setRuns([])
    expect(store.getState().messageLists['run:run_1']).toBeUndefined()
    expect(store.getState().runMessages).toEqual({})
  })

  it('keeps a list on screen when its session leaves the store, and drops it with its last viewer', async () => {
    const store = createRootStore()
    store.getState().upsertRun(run())
    const client = fakeApi({ coordMessagesList: vi.fn(async () => ({ messages: [runMessage({ id: 'a' })] })) })
    const viewer = renderHook(() => useMessageList(store, client, runScope))
    await waitFor(() => expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a']))

    store.setState({ runs: {} })
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a'])
    viewer.unmount()
    expect(store.getState().messageLists['run:run_1']).toBeUndefined()
  })

  it('forgets a kept list\'s failed read when its last viewer leaves', async () => {
    const store = createRootStore()
    store.getState().upsertRun(run())
    const client = fakeApi({ coordMessagesList: vi.fn().mockRejectedValue(new Error('coord.messages.list: server unreachable')) })
    const viewer = renderHook(() => useMessageList(store, client, runScope))
    await waitFor(() => expect(store.getState().messageErrors['run:run_1']).toContain('server unreachable'))

    viewer.unmount()
    expect(store.getState().messageErrors['run:run_1']).toBeUndefined()
    expect(store.getState().messageLists['run:run_1']).toBeDefined()
  })

  it('merges pages by id, oldest first, and pages back to exhaustion', () => {
    const store = createRootStore()
    const a = runMessage({ id: 'a', created_at: '2026-10-05T10:00:00.1Z' })
    const b = runMessage({ id: 'b', created_at: '2026-10-05T10:00:00.11Z' })
    const c = runMessage({ id: 'c', created_at: '2026-10-05T10:00:01Z' })

    store.getState().setMessagePage(runScope, [c, b], 'cursor-b')
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['b', 'c'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBe('cursor-b')

    store.getState().setMessagePage(runScope, [b, a], undefined, true)
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a', 'b', 'c'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBeNull()

    // A newest-page refresh that overlaps keeps the older pages and the exhausted cursor.
    const d = runMessage({ id: 'd', created_at: '2026-10-05T10:00:02Z' })
    store.getState().setMessagePage(runScope, [d, c], 'cursor-c')
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['a', 'b', 'c', 'd'])
    expect(store.getState().messageLists['run:run_1'].nextBefore).toBeNull()
  })

  it('keeps the server\'s insertion order for same-instant messages across pages', () => {
    const store = createRootStore()
    const [first, second, third, fourth] = ['d', 'c', 'a', 'b'].map((id) => runMessage({ id }))

    store.getState().setMessagePage(runScope, [third, second], 'cursor-c')
    store.getState().setMessagePage(runScope, [first], undefined, true)
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['d', 'c', 'a'])

    store.getState().setMessagePage(runScope, [fourth, third], 'cursor-a')
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['d', 'c', 'a', 'b'])
  })

  it('replaces a list when the newest page skips past everything held', () => {
    const store = createRootStore()
    store.getState().setMessagePage(workspaceScope, [runMessage({ id: 'old' })], undefined)
    const newer = runMessage({ id: 'new', created_at: '2026-10-05T11:00:00Z' })
    store.getState().setMessagePage(workspaceScope, [newer], 'cursor-new')
    expect(ids(scopeMessages(store.getState(), workspaceScope))).toEqual(['new'])
    expect(store.getState().messageLists[`workspace:${workspace.id}`].nextBefore).toBe('cursor-new')
  })

  it('keeps an acknowledgement a stale list read does not know about', () => {
    const store = createRootStore()
    store.getState().setMessagePage(threadScope, [runMessage()], undefined)
    store.getState().applyMessageAcked('msg-1', '2026-10-05T10:05:00Z')
    expect(store.getState().runMessages['msg-1'].acked_at).toBe('2026-10-05T10:05:00Z')

    store.getState().setMessagePage(threadScope, [runMessage({ body: 'hello' })], undefined)
    expect(store.getState().runMessages['msg-1'].acked_at).toBe('2026-10-05T10:05:00Z')
    store.getState().applyMessageAcked('unknown', '2026-10-05T10:05:00Z')
    expect(store.getState().runMessages.unknown).toBeUndefined()
  })

  it('reads a scope with its own filter and records a failed read', async () => {
    const store = createRootStore()
    const list = vi.fn()
      .mockResolvedValueOnce({ messages: [runMessage()], next_before: 'cursor-1' })
      .mockResolvedValueOnce({ messages: [runMessage({ id: 'msg-0', created_at: '2026-10-05T09:00:00Z' })] })
      .mockRejectedValueOnce(new Error('coord.messages.list: server unreachable'))
    const client = fakeApi({ coordMessagesList: list })

    await loadMessagePage(store, client, threadScope)
    await loadMessagePage(store, client, threadScope, true)
    await loadMessagePage(store, client, threadScope, true)
    expect(list.mock.calls.map(([params]) => params)).toEqual([
      { workspace_id: workspace.id, run_id: undefined, correlation_id: 'corr-1', before: undefined },
      { workspace_id: workspace.id, run_id: undefined, correlation_id: 'corr-1', before: 'cursor-1' },
    ])
    expect(ids(scopeMessages(store.getState(), threadScope))).toEqual(['msg-0', 'msg-1'])

    renderHook(() => useMessageList(store, client, threadScope))
    await waitFor(() => expect(store.getState().messageErrors['thread:corr-1']).toContain('server unreachable'))
  })
})

describe('reads that answer late', () => {
  it('keeps the newest read of a list when an earlier one answers after it', async () => {
    const store = createRootStore()
    store.getState().upsertRun(run())
    let fail!: (err: Error) => void
    const coordMessagesList = vi.fn()
      .mockImplementationOnce(() => new Promise((_, reject) => { fail = reject }))
      .mockResolvedValueOnce({ messages: [runMessage({ id: 'newer' })] })
    const client = fakeApi({ coordMessagesList })
    const view = renderHook(() => useMessageList(store, client, runScope))
    await waitFor(() => expect(coordMessagesList).toHaveBeenCalledTimes(1))
    await loadMessagePage(store, client, runScope)
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['newer'])
    fail(new Error('server did not answer coord.messages.list within 1m0s'))
    await Promise.resolve()
    await Promise.resolve()
    expect(store.getState().messageErrors['run:run_1']).toBeUndefined()
    view.unmount()
  })

  it('drops a read that answers after another member signed in', async () => {
    const store = createRootStore()
    store.getState().upsertRun(run())
    store.getState().setIdentityKey('first')
    let answer!: (page: { messages: RunMessage[] }) => void
    const client = fakeApi({ coordMessagesList: vi.fn(() => new Promise<{ messages: RunMessage[] }>((resolve) => { answer = resolve })) })
    const view = renderHook(() => useMessageList(store, client, runScope))
    await waitFor(() => expect(client.coordMessagesList).toHaveBeenCalledTimes(1))
    store.getState().setIdentityKey('second')
    answer({ messages: [runMessage({ id: 'for-the-first-member' })] })
    await Promise.resolve()
    await Promise.resolve()
    expect(scopeMessages(store.getState(), runScope)).toEqual([])
    view.unmount()
  })
})

describe('agent message events', () => {
  const messageEvent = (seq: number, payload: Record<string, unknown>, type = 'coord.message'): Event => ({
    id: `coord-${seq}`,
    seq,
    time: '2026-10-05T10:00:00Z',
    workspace_id: workspace.id,
    run_id: 'run_worker',
    actor_id: '',
    type,
    payload,
  })

  it('re-reads every loaded list the message belongs to, on screen or kept', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().upsertRun(run({ id: 'run_worker' }))
    const empty = fakeApi()
    for (const scope of [runScope, threadScope, { kind: 'run', workspaceID: workspace.id, runID: 'run_other' } as const]) renderHook(() => useMessageList(store, empty, scope))
    const offScreen = renderHook(() => useMessageList(store, empty, { kind: 'run', workspaceID: workspace.id, runID: 'run_worker' }))
    await waitFor(() => expect(Object.keys(store.getState().messageLists)).toHaveLength(4))
    offScreen.unmount()
    expect(store.getState().messageLists['run:run_worker']).toBeDefined()

    const arrived = runMessage({ id: 'msg-2', kind: 'question' })
    const list = vi.fn(async (_: { correlation_id?: string; run_id?: string }) => ({ messages: [arrived] }))
    const client = fakeApi({ coordMessagesList: list })
    const payload = {
      message_id: 'msg-2', workspace_id: workspace.id, correlation_id: 'corr-1',
      from_run_id: 'run_worker', to_run_id: 'run_1', kind: 'question',
    }

    expect(await applyEvent(store, messageEvent(8, payload), client)).toBe(true)
    expect(list.mock.calls.map(([params]) => params.correlation_id ?? params.run_id).sort()).toEqual(['corr-1', 'run_1', 'run_worker'])
    expect(ids(scopeMessages(store.getState(), runScope))).toEqual(['msg-2'])
    expect(ids(scopeMessages(store.getState(), { kind: 'run', workspaceID: workspace.id, runID: 'run_worker' }))).toEqual(['msg-2'])

    const acked = { message_id: 'msg-2', to_run_id: 'run_1', acked_at: '2026-10-05T10:01:00Z' }
    expect(await applyEvent(store, messageEvent(9, acked, 'coord.message.acked'), client)).toBe(true)
    expect(store.getState().runMessages['msg-2'].acked_at).toBe('2026-10-05T10:01:00Z')
  })

  it('records no error for a kept list whose re-read fails off screen', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    store.getState().upsertRun(run())
    renderHook(() => useMessageList(store, fakeApi(), runScope)).unmount()
    await waitFor(() => expect(store.getState().messageLists['run:run_1']).toBeDefined())

    const client = fakeApi({ coordMessagesList: vi.fn().mockRejectedValue(new Error('coord.messages.list: server unreachable')) })
    const payload = { message_id: 'msg-2', workspace_id: workspace.id, from_run_id: 'run_worker', to_run_id: 'run_1', kind: 'message' }
    expect(await applyEvent(store, messageEvent(8, payload), client)).toBe(true)
    expect(client.coordMessagesList).toHaveBeenCalledTimes(1)
    expect(store.getState().messageErrors['run:run_1']).toBeUndefined()
  })

  it('re-reads a list whose first read failed when a message arrives', async () => {
    const store = createRootStore()
    await hydrate(store, fakeApi())
    const arrived = runMessage({ id: 'msg-2' })
    const list = vi.fn()
      .mockRejectedValueOnce(new Error('coord.messages.list: server unreachable'))
      .mockResolvedValueOnce({ messages: [arrived] })
    const client = fakeApi({ coordMessagesList: list })
    renderHook(() => useMessageList(store, client, threadScope))
    await waitFor(() => expect(store.getState().messageErrors['thread:corr-1']).toContain('server unreachable'))

    const payload = { message_id: 'msg-2', workspace_id: workspace.id, correlation_id: 'corr-1', from_run_id: 'run_worker', to_run_id: 'run_1', kind: 'message' }
    expect(await applyEvent(store, messageEvent(8, payload), client)).toBe(true)
    expect(ids(scopeMessages(store.getState(), threadScope))).toEqual(['msg-2'])
    expect(store.getState().messageErrors['thread:corr-1']).toBeUndefined()
  })
})
