import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { api, ApiError } from '@/lib/api'
import { SessionFailureCallout } from '@/routes/run/session-failure'
import type { RunShells } from '@/routes/run/shells'
import { useStore } from '@/store'
import type { AcpSession } from '@/store/sessions'
import { agentInfo, run, runRecords, serverInfo } from '@/test/fixtures'

afterEach(() => {
  vi.restoreAllMocks()
})

function signedOutSession(agentCommand?: string): AcpSession {
  return {
    turns: [],
    live: true,
    control: { has_control: true, control_session_id: 'session-1', control_generation: 2 },
    state: {
      auth: { authStatus: { kind: 'none' } },
      auth_methods: [{ id: 'login', name: 'Log in', type: 'terminal', args: ['--cli'] }],
      ...(agentCommand ? { agent_command: agentCommand } : {}),
    },
  } as unknown as AcpSession
}

function renderCallout(session: AcpSession) {
  const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running' })).run_1!
  useStore.setState({ acpSessions: { run_1: session } })
  const shells = {
    canAdd: true,
    open: vi.fn(async () => ({ terminal_id: 'tab-1', incarnation: 'shell-1' })),
  } as unknown as RunShells
  const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
  render(<SessionFailureCallout run={record} session={session} switchable={false} shells={shells} nav={nav} />)
}

describe('terminal login', () => {
  it('types the ACP server command with the method args, not the agent name', async () => {
    const input = vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
    renderCallout(signedOutSession('claude-agent-acp'))
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(input).toHaveBeenCalledWith(expect.objectContaining({ text: 'claude-agent-acp --cli' })))
  })

  it('types nothing when the server names no ACP command', async () => {
    const input = vi.spyOn(api, 'devTerminalInput').mockResolvedValue({ accepted: true })
    renderCallout(signedOutSession())
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(screen.getByRole('button', { name: 'Log in' })).toBeDefined())
    expect(input).not.toHaveBeenCalled()
  })
})

describe('signed-out login', () => {
  const problem = '~/.claude/.credentials.json: Claude OAuth access token is missing or invalid'

  it("says the account's login is signed out, shows the problem and sends the member to Agents", () => {
    useStore.setState({ info: serverInfo, agentList: { identity: null, agents: [agentInfo({ display_name: 'Claude Code', login_problem: problem, login_blocks_launch: true })] }, route: { name: 'run', params: { runId: 'run_1' } } })
    renderCallout(signedOutSession())
    expect(screen.getByText('Claude Code is signed out')).toBeDefined()
    expect(screen.getByText(/Sign in again on the Agents page, or from a terminal in this session/)).toBeDefined()
    expect(screen.getByText(problem)).toBeDefined()
    expect(screen.getByRole('button', { name: 'Log in' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Sign in on Agents' }))
    expect(useStore.getState().route.name).toBe('agents')
  })

  it("offers no Agents page for a run on another member's account", () => {
    useStore.setState({ info: serverInfo, agentList: { identity: null, agents: [agentInfo({ login_problem: problem })] } })
    const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running', account_member_id: 'mem_bob' })).run_1!
    useStore.setState({ acpSessions: { run_1: signedOutSession() } })
    const shells = { canAdd: true, open: vi.fn() } as unknown as RunShells
    render(<SessionFailureCallout run={record} session={signedOutSession()} switchable={false} shells={shells} nav={{ go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }} />)
    expect(screen.queryByRole('button', { name: 'Sign in on Agents' })).toBeNull()
    expect(screen.getByText('The agent is not signed in')).toBeDefined()
    expect(screen.getByText(/This session signs in with another member's account\. They sign in again on their Agents page or with/).textContent).toContain('aether agent login claude')
  })
})

describe('refused sign-in', () => {
  it("does not repeat the agent's refusal, which the failure already quotes", () => {
    const refusal = 'the agent refused: Authentication required'
    useStore.setState({ info: serverInfo, agentList: { identity: null, agents: [agentInfo({ display_name: 'Claude Code', login_problem: refusal })] } })
    renderCallout(signedOutSession())
    expect(screen.getByText("Claude Code's sign-in was refused")).toBeDefined()
    expect(screen.queryByText(refusal)).toBeNull()
  })
})

describe('a launched session whose agent failed to start', () => {
  const ack = { ok: true as const, seq: 0, replay: 0, epoch: 0, has_control: false }
  const failed = (description: string) => {
    useStore.setState({ acpSessions: {}, agentList: null, info: serverInfo })
    const store = useStore.getState()
    store.acpAck('run_1', { ...ack, live: false, starting: true })
    store.acpAck('run_1', { ...ack, seq: 1, replay: 1, live: false })
    store.acpFrames('run_1', [{ seq: 1, item: { seq: 1, epoch: 0, time: '2026-11-27T10:00:00Z', turn: 0, kind: 'notice', notice: { severity: 'error', title: 'Enhanced agent failed', description } } }])
    return useStore.getState().acpSessions.run_1!
  }

  it('offers a retry and Standard under the real error', () => {
    renderCallout(failed('start the agent: exec: "claude-agent-acp": executable file not found in $PATH'))
    expect(screen.getByText('Enhanced agent failed')).toBeDefined()
    expect(screen.getByText(/executable file not found in \$PATH/)).toBeDefined()
    for (const name of ['Retry Enhanced', 'Open in Standard']) expect(screen.getByRole('button', { name })).toBeDefined()
  })

  it('offers the sign-in steps when the agent refused the login', () => {
    renderCallout(failed('the agent refused: Authentication required; sign in again on the Agents page'))
    expect(screen.getByText('The agent is not signed in')).toBeDefined()
    expect(screen.getByText(/Authentication required; sign in again on the Agents page/)).toBeDefined()
    expect(screen.getByText(/then choose Retry Enhanced/)).toBeDefined()
    for (const name of ['Sign in on Agents', 'Open a terminal', 'Retry Enhanced']) expect(screen.getByRole('button', { name })).toBeDefined()
  })

  it('keeps "send your message again" for a refused turn on an agent that is asleep', () => {
    useStore.setState({ acpSessions: {}, agentList: null, info: serverInfo })
    const store = useStore.getState()
    store.acpAck('run_1', { ...ack, seq: 1, replay: 1, live: false })
    store.acpFrames('run_1', [{ seq: 1, item: { seq: 1, epoch: 0, time: '2026-11-27T10:00:00Z', turn: 0, kind: 'notice', notice: { severity: 'error', title: 'Prompt failed', description: 'Authentication required' } } }])
    renderCallout(useStore.getState().acpSessions.run_1!)
    expect(screen.getByText(/then send your message again/)).toBeDefined()
    expect(screen.queryByRole('button', { name: 'Retry Enhanced' })).toBeNull()
  })
})

describe('open in Standard', () => {
  beforeEach(() => {
    vi.spyOn(api, 'runCloseCheck').mockResolvedValue([{ run_id: 'run_1', warnings: [] }])
  })

  it('relaunches on the account, fallback, child-run setting and repositories of the run it replaces', async () => {
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'completed' }))
    const record = runRecords(run({
      id: 'run_1', harness: 'claude', status: 'running', member_id: 'mem-me', account_member_id: 'mem-me', requested_account_member_id: 'mem-ada',
      model: 'opus', fallback_account_member_ids: ['mem-me'], children: 'off',
      repos: [{ workspace_id: 'ws-1', name: 'api', path: '/repos/api', branch: 'b', primary: true }, { workspace_id: 'ws-2', name: 'web', path: '/repos/web', branch: 'b', primary: false }],
    })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced agent failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard session' }))
    await waitFor(() => expect(launch).toHaveBeenCalledWith({
      workspace_id: record.workspace_id, task: record.task, harness: 'claude', mode: 'tui', model: 'opus',
      account_member_id: 'mem-ada', fallback_account_member_ids: ['mem-me'], children: 'off', extra_workspace_ids: ['ws-2'],
    }))
  })

  it('names no login for the replacement, since the run does not say whether its login was named or chosen', async () => {
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'completed' }))
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced agent failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    const relaunch = async (over: Parameters<typeof run>[0]) => {
      launch.mockClear()
      const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running', member_id: 'mem-me', ...over })).run_1!
      const view = render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
      fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
      fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard session' }))
      await waitFor(() => expect(launch).toHaveBeenCalled())
      view.unmount()
      return launch.mock.lastCall![0]
    }
    expect(await relaunch({ account_member_id: 'mem-me', account_login: 'lgn-work0000001', account_login_name: 'work' })).not.toHaveProperty('login')
    expect(await relaunch({ account_member_id: 'mem-me', account_login: 'lgn-console0001', account_login_name: 'console', account_login_per_use: true })).not.toHaveProperty('login')
    expect(await relaunch({ account_member_id: 'mem-me', account_login: 'lgn-gone0000001' })).not.toHaveProperty('login')
  })

  it('starts a new Standard session when a switchable agent has no conversation to move', async () => {
    const refusal = new ApiError(200, 'run.mode.switch: scheduler: invalid session state transition: the agent has not reported its conversation yet; it does on its first turn', -32002, { reason: 'session_not_reported' })
    vi.spyOn(api, 'runModeSwitch').mockRejectedValue(refusal)
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'abandoned' }))
    const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running' })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced agent failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard session' }))
    await waitFor(() => expect(launch).toHaveBeenCalledWith(expect.objectContaining({ mode: 'tui', task: record.task })))
    expect(nav.go).not.toHaveBeenCalled()
  })

  it('lists close warnings before it closes the run it replaces', async () => {
    vi.spyOn(api, 'runCloseCheck').mockResolvedValue([{ run_id: 'run_1', warnings: [{ kind: 'messages', detail: '2 messages from other sessions unread or sent in the last 30 minutes', count: 2 }] }])
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'abandoned' }))
    const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running' })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced agent failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    const go = await screen.findByRole('button', { name: 'Close anyway and start Standard' })
    expect(screen.getByRole('list', { name: 'What closing affects' }).textContent).toBe('2 messages from other sessions unread or sent in the last 30 minutes.')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Keep this session' }))
    expect(screen.getByRole('button', { name: 'Keep this session' }).className).toContain('focus:outline-accent')
    fireEvent.click(go)
    await waitFor(() => expect(api.runClose).toHaveBeenCalledWith('run_1', 'abandoned'))
    expect(launch).toHaveBeenCalled()
  })

  it("launches a teammate's run on the viewer's own account and settings", async () => {
    const launch = vi.spyOn(api, 'runLaunch').mockResolvedValue(run({ id: 'run_2', status: 'running' }))
    vi.spyOn(api, 'runClose').mockResolvedValue(run({ id: 'run_1', status: 'completed' }))
    const record = runRecords(run({ id: 'run_1', harness: 'claude', status: 'running', member_id: 'mem-ada', account_member_id: 'mem-carol', no_fallback: true, children: 'on' })).run_1!
    const session = { turns: [{ items: [{ kind: 'notice', notice: { title: 'Enhanced agent failed' } }] }], live: false } as unknown as AcpSession
    useStore.setState({ acpSessions: { run_1: session }, info: { member: { id: 'mem-me' } } as never })
    const nav = { go: vi.fn(), reveal: vi.fn(), focusComposer: vi.fn(), focusRequest: vi.fn() }
    render(<SessionFailureCallout run={record} session={session} switchable={false} shells={{} as RunShells} nav={nav} />)
    fireEvent.click(screen.getByRole('button', { name: 'Open in Standard' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Start a Standard session' }))
    await waitFor(() => expect(launch).toHaveBeenCalledWith({ workspace_id: record.workspace_id, task: record.task, harness: 'claude', mode: 'tui' }))
  })
})
