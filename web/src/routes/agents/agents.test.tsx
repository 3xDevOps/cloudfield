import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { toast } from 'sonner'
import { vi } from 'vitest'
import { splitArgv } from '@/components/agents/add-agent'
import type * as apiModule from '@/lib/api'
import { api } from '@/lib/api'
import { configPrompt } from '@/lib/config-prompt'
import { lookupRoute } from '@/routes/registry'
import '@/routes/agents'
import { useStore } from '@/store'
import { useLaunchDefaults } from '@/store/launch-defaults'
import { useLoginSignIn } from '@/store/login-sign-in'
import { agentInfo, serverInfo } from '@/test/fixtures'
import { pickOption } from '@/test/select'
import { StubSocket } from '@/test/stub-socket'

// vi.mock factories are hoisted above static imports, so the fixture module
// must be loaded inside the factory (same as terminal.test.tsx).
vi.mock('@/lib/api', async (importOriginal) => {
  const actual = await importOriginal<typeof apiModule>()
  const { fakeApi } = await import('@/test/fixtures')
  return { ...actual, api: fakeApi(), API_BASE: '/api/v1', ApiError: Error }
})

function mount() {
  const View = lookupRoute('agents')
  if (!View) throw new Error('agents route not registered')
  return render(<View params={{}} />)
}

async function flush() {
  await act(async () => {})
}

beforeEach(() => {
  useStore.setState(useStore.getInitialState(), true)
  vi.mocked(api.agentList).mockResolvedValue([
    agentInfo({ display_name: 'Claude Code', glyph: 'claude', login_found: true, enhanced: 'adapter' }),
    agentInfo({ name: 'myagent', display_name: 'myagent', glyph: 'custom', source: 'member', installed: false, install_script: undefined, enhanced: 'none' }),
  ])
  useStore.setState({
    capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach'] },
    hydrated: true,
  })
})

describe('splitArgv', () => {
  it('splits on spaces and drops empty words', () => {
    expect(splitArgv('mycli -p {task}')).toEqual(['mycli', '-p', '{task}'])
    expect(splitArgv('  mycli  {task} ')).toEqual(['mycli', '{task}'])
  })
})

describe('agents page', () => {
  it('shows list-shaped placeholders while agent.list is pending', async () => {
    vi.mocked(api.agentList).mockReturnValue(new Promise<never>(() => {}))
    mount()

    expect((await screen.findByRole('status', { name: 'Loading agents' })).querySelectorAll('li')).toHaveLength(3)
  })

  it('reads agent.list once, under the identity hydration sets', async () => {
    vi.mocked(api.agentList).mockClear()
    useStore.setState({ hydrated: false })
    mount()
    await flush()
    expect(api.agentList).not.toHaveBeenCalled()
    act(() => useStore.setState({ identityKey: 'alice' }))
    act(() => useStore.setState({ hydrated: true }))
    await flush()
    expect(api.agentList).toHaveBeenCalledTimes(1)
    expect(useStore.getState().agentList?.identity).toBe('alice')
  })

  it('lists installed agents first and folds the rest under More agents', async () => {
    mount()
    await flush()

    const rows = within(screen.getByRole('list', { name: 'Agents' })).getAllByRole('listitem')
    expect(rows).toHaveLength(1)
    expect(rows[0].textContent).toContain('Claude Code')
    expect(rows[0].textContent).toContain('Installed · Login found')
    expect(rows[0].textContent).toContain('Standard · Enhanced')
    expect(within(rows[0]).getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
    expect(within(rows[0]).getByRole('combobox', { name: 'Default mode for Claude Code' })).toBeDefined()
    expect(screen.queryByRole('list', { name: 'More agents' })).toBeNull()

    fireEvent.click(screen.getByRole('button', { name: 'More agents (1)' }))
    const more = within(screen.getByRole('list', { name: 'More agents' })).getAllByRole('listitem')
    expect(more[0].textContent).toContain('Not installed')
    expect(within(more[0]).getByLabelText('Supports Standard only')).toBeDefined()
    expect(within(more[0]).getByRole('button', { name: 'Set up myagent' })).toBeDefined()
    expect(within(more[0]).queryByRole('combobox')).toBeNull()
  })

  it('asks an installed agent without a login to log in', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ display_name: 'Claude Code', login_found: false })])
    mount()
    await flush()

    expect(screen.getByRole('button', { name: 'Log in Claude Code' })).toBeDefined()
    expect(screen.queryByRole('button', { name: 'More agents (0)' })).toBeNull()
  })

  it('explains Standard and Enhanced in a dialog', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: "What's the difference?" }))
    const dialog = within(screen.getByRole('dialog', { name: 'Standard or Enhanced' }))
    expect(dialog.getByText('Works with every agent.')).toBeDefined()
    expect(dialog.queryByRole('radio')).toBeNull()
  })

  it('keeps a default mode per agent without making it the most recent launch', async () => {
    useStore.setState({ launchDefaults: { claude: { mode: 'tui', at: 42 } } })
    mount()
    await flush()

    await pickOption(screen.getByRole('combobox', { name: 'Default mode for Claude Code' }), 'Enhanced')
    expect(useStore.getState().launchDefaults.claude).toEqual({ mode: 'acp', at: 42 })
  })

  it('opens the launch dialog on the agent Run names', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: 'Run Claude Code' }))
    expect(useStore.getState().paletteDialog).toBe('launch')
    expect(useStore.getState().launchDefaults.claude?.mode).toBe('tui')
  })

  it('runs an agent in the mode saved on the server, not this browser\'s', async () => {
    useLaunchDefaults.setState({ result: null, error: null })
    vi.mocked(api.launchDefaultsGet).mockResolvedValue({ revision: 2, defaults: { agents: { claude: { mode: 'acp' } } } })
    useStore.setState({ launchDefaults: { claude: { mode: 'tui', at: 42 } } })
    mount()
    await flush()
    expect(screen.getByRole('combobox', { name: 'Default mode for Claude Code' }).textContent).toContain('Enhanced')

    fireEvent.click(screen.getByRole('button', { name: 'Run Claude Code' }))
    await waitFor(() => expect(api.launchDefaultsSet).toHaveBeenCalledWith({ defaults: { agent: 'claude', agents: { claude: { mode: 'acp' } } }, revision: 2 }))
  })

  it('opens the setup with the comparison, and comes back from it', async () => {
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Set up again' }))
    expect(screen.getByRole('radiogroup', { name: 'How sessions show Claude Code' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Run Claude Code' }))
  })

  it('removes an agent after confirming, and reloads the list', async () => {
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove Claude Code?' })
    expect(dialog.textContent).toContain('Deletes ~/.local/bin/claude')
    expect(dialog.textContent).toContain("New sessions can't start it; running sessions keep going.")
    expect(dialog.textContent).toContain('Its login, settings and package files stay. Set up installs it again.')

    vi.mocked(api.agentList).mockClear()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await flush()
    expect(api.agentRemove).toHaveBeenCalledWith('claude')
    expect(api.agentList).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('alertdialog')).toBeNull()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect((document.activeElement as HTMLElement | null)?.dataset.agentAction).toBe('claude')
  })

  it('keeps the dialog open with the refusal when the server refuses a removal', async () => {
    vi.mocked(api.agentRemove).mockRejectedValueOnce(new Error('agent "claude" is not installed in your environment: ~/.local/bin/claude does not exist'))
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    fireEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }))
    await flush()
    expect(within(screen.getByRole('alertdialog')).getByRole('alert').textContent).toContain('is not installed in your environment')
  })

  it('offers removing a member agent', async () => {
    mount()
    await flush()
    fireEvent.click(screen.getByRole('button', { name: 'More agents (1)' }))
    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for myagent' }), { button: 0, ctrlKey: false })
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove…' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove myagent?' })
    expect(dialog.textContent).toContain('Deletes your myagent definition.')
    expect(dialog.textContent).toContain('Add agent… registers it again.')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await flush()
    await new Promise((resolve) => setTimeout(resolve, 0))
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Add agent…' }))
  })

  it('hides Remove without agent.remove', async () => {
    useStore.setState({ capabilities: { gateway: 'server', methods: ['agent.list', 'agent.install', 'run.launch'], ws: [] } })
    mount()
    await flush()

    fireEvent.pointerDown(screen.getByRole('button', { name: 'More for Claude Code' }), { button: 0, ctrlKey: false })
    expect(await screen.findByRole('menuitem', { name: 'Set up again' })).toBeDefined()
    expect(screen.queryByRole('menuitem', { name: 'Remove…' })).toBeNull()
  })

  it('adds a custom agent from the header', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: 'Add agent…' }))
    expect(screen.getByRole('form', { name: 'Add agent' })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
  })

  it('keeps git identity, GitHub and agent config files collapsed', async () => {
    mount()
    await flush()

    fireEvent.click(screen.getByRole('button', { name: /^Git identity/ }))
    expect(screen.queryByRole('form', { name: 'Git identity' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Set it in Profile' })).toBeDefined()
    expect(screen.getByRole('button', { name: /^GitHub/ })).toBeDefined()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    expect(screen.getByRole('region', { name: 'Agent config files' })).toBeDefined()
  })

  it.each(['config.roots', 'config.import'])('offers no agent config files when %s is unavailable', async (missing) => {
    useStore.setState({
      capabilities: {
        gateway: 'remote',
        methods: ['agent.list', 'config.roots', 'config.import'].filter((method) => method !== missing),
        ws: [],
      },
    })
    mount()
    await flush()

    expect(screen.queryByRole('button', { name: /^Agent config files/ })).toBeNull()
  })
})

describe('Claude Code logins', () => {
  const claudeRow = () => within(screen.getByRole('list', { name: 'Agents' })).getAllByRole('listitem')[0]
  const loginsServer = () => useStore.setState({ info: { ...serverInfo, features: ['logins'] } })

  beforeEach(() => {
    useStore.setState({ capabilities: { gateway: 'local', methods: ['*'], ws: ['events', 'attach', 'terminal'] } })
    loginsServer()
    useLoginSignIn.setState({ running: null, ended: null })
    vi.mocked(api.agentLoginResult).mockReset()
    StubSocket.install()
  })

  const startSignIn = async (name: string) => {
    fireEvent.click(within(claudeRow()).getByRole('button', { name: 'Add login' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Name' }), { target: { value: name } })
    fireEvent.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect(await screen.findByRole('heading', { name: `Sign in to ${name}` })).toBeDefined()
  }

  it('shows no login controls and reads no logins on a server without added logins', async () => {
    useStore.setState({ info: { ...serverInfo, features: ['permission'] } })
    vi.mocked(api.agentLoginList).mockClear()
    mount()
    await flush()
    expect(within(claudeRow()).queryByRole('button', { name: 'Add login' })).toBeNull()
    expect(screen.queryByRole('region', { name: 'Claude Code logins' })).toBeNull()
    expect(api.agentLoginList).not.toHaveBeenCalled()
  })

  it('leaves the row of a member with only the default login as it was, plus Add login', async () => {
    useStore.setState({ info: { ...serverInfo, features: [] } })
    const view = mount()
    await flush()
    const before = claudeRow().textContent
    expect(within(claudeRow()).queryByRole('button', { name: 'Add login' })).toBeNull()
    view.unmount()

    loginsServer()
    mount()
    await flush()
    expect(api.agentLoginList).toHaveBeenCalledWith('claude')
    expect(within(claudeRow()).getByRole('button', { name: 'Add login' })).toBeDefined()
    expect(claudeRow().textContent?.replace('Add login', '')).toBe(before)
    expect(screen.queryByRole('region', { name: 'Claude Code logins' })).toBeNull()
    expect(api.accountList).not.toHaveBeenCalled()
  })

  it('lists several logins under the Claude Code row and names the teammates shares reach', async () => {
    vi.mocked(api.agentLoginList).mockResolvedValue([
      { id: 'default', name: 'default', kind: 'subscription', automatic: true, shared: true, position: 0 },
      { id: 'lgn-work0000001', name: 'work', kind: 'subscription', automatic: true, shared: true, position: 1 },
    ])
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [], shared_with: [{ id: 'mem-bob', display_name: 'Bob' } as never] })
    mount()
    await flush()
    const logins = within(claudeRow()).getByRole('region', { name: 'Claude Code logins' })
    expect(within(logins).getAllByRole('listitem').map((item) => item.getAttribute('aria-label'))).toEqual(['Login default', 'Login work'])
    expect(within(claudeRow()).getAllByRole('button', { name: 'Add login' })).toHaveLength(1)
    await waitFor(() => expect(logins.textContent).toContain('Your account is shared with Bob'))
    expect(claudeRow().textContent).toContain('Installed · 2 logins')
    expect(within(claudeRow()).queryByRole('link')).toBeNull()
  })

  it('keeps a sign-in left running on the Claude Code row until it ends, and returns to it, with one wait on its result', async () => {
    let end: (login: never) => void = () => {}
    const result = new Promise<never>((resolve) => { end = resolve })
    vi.mocked(api.agentLoginAdd).mockResolvedValue({ login_id: 'lgn-spare000001' })
    vi.mocked(api.agentLoginResult).mockReturnValue(result)
    mount()
    await flush()
    await startSignIn('spare')
    expect(api.agentLoginResult).toHaveBeenCalledTimes(1)
    expect(api.agentLoginResult).toHaveBeenCalledWith('claude', 'lgn-spare000001', expect.any(AbortSignal))
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    expect(claudeRow().textContent).toContain('Sign-in for spare is still running.')

    fireEvent.click(within(claudeRow()).getByRole('button', { name: 'Return to the sign-in' }))
    expect(screen.getByRole('heading', { name: 'Sign in to spare' })).toBeDefined()
    expect(api.agentLoginResult).toHaveBeenCalledTimes(1)

    vi.mocked(api.agentLoginList).mockClear()
    await act(async () => end({ id: 'lgn-spare000001', name: 'spare', kind: 'subscription', automatic: true, shared: true, position: 1 } as never))
    expect(screen.getByText('Added login spare')).toBeDefined()
    expect(api.agentLoginList).toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    expect(claudeRow().textContent).not.toContain('still running')
  })

  it('waits again when the gateway gives up on the result, and never calls the sign-in not added', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    onTestFinished(() => { vi.useRealTimers() })
    let end: (login: never) => void = () => {}
    vi.mocked(api.agentLoginAdd).mockResolvedValue({ login_id: 'lgn-spare000002' })
    vi.mocked(api.agentLoginResult)
      .mockRejectedValueOnce(new Error('agent.login.result: server did not answer agent.login.result within 31m0s'))
      .mockReturnValueOnce(new Promise<never>((resolve) => { end = resolve }))
    mount()
    await flush()
    await startSignIn('spare')
    await act(async () => { await vi.advanceTimersByTimeAsync(2500) })
    expect(api.agentLoginResult).toHaveBeenCalledTimes(2)
    expect(screen.queryByText(/was not added/)).toBeNull()
    expect(screen.getByRole('button', { name: 'Cancel sign-in' })).toBeDefined()
    await act(async () => end({ id: 'lgn-spare000002', name: 'spare', kind: 'subscription', automatic: true, shared: true, position: 1 } as never))
    expect(screen.getByText('Added login spare')).toBeDefined()
  })

  it('remembers a running sign-in on another page, and offers it on return before any refusal', async () => {
    vi.mocked(api.agentLoginAdd).mockResolvedValue({ login_id: 'lgn-spare000003' })
    vi.mocked(api.agentLoginResult).mockReturnValue(new Promise<never>(() => {}))
    const first = mount()
    await flush()
    await startSignIn('spare')
    first.unmount()

    vi.mocked(api.agentLoginAdd).mockClear()
    mount()
    await flush()
    expect(claudeRow().textContent).toContain('Sign-in for spare is still running.')
    fireEvent.click(within(claudeRow()).getByRole('button', { name: 'Add login' }))
    expect(screen.getByRole('status').textContent).toContain('Sign-in for spare is still running')
    expect(screen.getByRole('button', { name: 'Start sign-in' })).toHaveProperty('disabled', true)
    expect(api.agentLoginAdd).not.toHaveBeenCalled()
    expect(api.agentLoginResult).toHaveBeenCalledTimes(2)
    expect(vi.mocked(api.agentLoginResult).mock.calls[0]![2]!.aborted).toBe(true)
  })

  const two = [
    { id: 'default', name: 'default', kind: 'subscription' as const, automatic: true, shared: true, position: 0 },
    { id: 'lgn-work0000001', name: 'work', kind: 'subscription' as const, email: 'alice@work.example.test', automatic: true, shared: true, position: 1 },
  ]

  it("shows nothing of the member the gateway was bound to before once it is bound to another", async () => {
    useStore.setState({ identityKey: 'member:alice' })
    vi.mocked(api.agentLoginList).mockResolvedValue(two)
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [], shared_with: [] })
    mount()
    await flush()
    expect(within(claudeRow()).getByRole('region', { name: 'Claude Code logins' }).textContent).toContain('alice@work.example.test')

    vi.mocked(api.agentLoginList).mockReturnValue(new Promise(() => {}))
    act(() => useStore.setState({ identityKey: 'member:bob' }))
    await flush()
    expect(screen.queryByRole('region', { name: 'Claude Code logins' })).toBeNull()
    expect(screen.queryByText(/alice@work/)).toBeNull()
  })

  it('says why whom the account is shared with is unknown instead of calling it not shared', async () => {
    vi.mocked(api.agentLoginList).mockResolvedValue(two)
    vi.mocked(api.accountList).mockRejectedValue(new Error('account.list: server unreachable: dial tcp: connection refused'))
    mount()
    await flush()
    const logins = within(claudeRow()).getByRole('region', { name: 'Claude Code logins' })
    await waitFor(() => expect(logins.textContent).toContain('Whom your account is shared with was not read: account.list: server unreachable: dial tcp: connection refused'))
    expect(logins.textContent).not.toContain('not shared')
  })

  it('offers Run when an added login is found though the default one is not', async () => {
    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ display_name: 'Claude Code', glyph: 'claude', login_found: false, enhanced: 'adapter' })])
    vi.mocked(api.agentLoginList).mockResolvedValue([{ ...two[0]!, kind: undefined }, two[1]!])
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [], shared_with: [] })
    const view = mount()
    await flush()
    expect(within(claudeRow()).getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
    expect(within(claudeRow()).queryByRole('button', { name: 'Log in Claude Code' })).toBeNull()
    view.unmount()

    vi.mocked(api.agentList).mockResolvedValue([agentInfo({ display_name: 'Claude Code', glyph: 'claude', login_found: true, login_problem: 'OAuth token has expired', login_blocks_launch: true, enhanced: 'adapter' })])
    useStore.setState({ agentList: null })
    mount()
    await flush()
    expect(claudeRow().textContent).toContain('Installed · 2 logins · default login signed out')
    expect(within(claudeRow()).getByRole('button', { name: 'Run Claude Code' })).toBeDefined()
  })

  it('closes Add login and stops waiting on a sign-in when the server stops keeping logins', async () => {
    vi.mocked(api.agentLoginAdd).mockResolvedValue({ login_id: 'lgn-spare000009' })
    vi.mocked(api.agentLoginResult).mockReturnValue(new Promise<never>(() => {}))
    mount()
    await flush()
    await startSignIn('spare')
    fireEvent.click(screen.getByRole('button', { name: 'All agents' }))
    fireEvent.click(within(claudeRow()).getByRole('button', { name: 'Add login' }))
    expect(screen.getByRole('form', { name: 'Add a Claude Code login' })).toBeDefined()
    const calls = () => [api.agentLoginList, api.agentLoginAdd, api.agentLoginResult].map((call) => vi.mocked(call).mock.calls.length)
    const before = calls()

    act(() => useStore.setState({ info: { ...serverInfo, features: [] } }))
    await flush()
    expect(screen.queryByRole('form', { name: 'Add a Claude Code login' })).toBeNull()
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
    expect(claudeRow().textContent).not.toContain('still running')
    expect(vi.mocked(api.agentLoginResult).mock.calls.at(-1)![2]!.aborted).toBe(true)
    expect(calls()).toEqual(before)
  })

  it('opens Add login in the page, naming whom the new login serves', async () => {
    vi.mocked(api.accountList).mockResolvedValue({ accounts: [], shared_with: [{ id: 'mem-bob', display_name: 'Bob' } as never] })
    mount()
    await flush()
    fireEvent.click(within(claudeRow()).getByRole('button', { name: 'Add login' }))
    const form = screen.getByRole('form', { name: 'Add a Claude Code login' })
    await waitFor(() => expect(form.textContent).toContain('and sessions of Bob can run on it too.'))
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(screen.getByRole('list', { name: 'Agents' })).toBeDefined()
  })
})

describe('agent config files', () => {
  const writeText = vi.fn<(text: string) => Promise<void>>()

  beforeEach(() => {
    writeText.mockReset().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
  })

  afterEach(() => {
    Reflect.deleteProperty(navigator, 'clipboard')
    vi.restoreAllMocks()
  })

  async function open() {
    mount()
    await flush()
    fireEvent.click(screen.getByRole('button', { name: /^Agent config files/ }))
    return within(screen.getByRole('region', { name: 'Agent config files' }))
  }

  it('shows the prompt and copies exactly it', async () => {
    const section = await open()
    expect(section.getByLabelText('Prompt').textContent).toBe(configPrompt)
    await act(async () => fireEvent.click(section.getByRole('button', { name: 'Copy prompt' })))
    expect(writeText.mock.calls).toEqual([[configPrompt]])
  })

  it('selects the prompt and says so when the clipboard refuses', async () => {
    writeText.mockRejectedValue(new DOMException('Clipboard denied', 'NotAllowedError'))
    const info = vi.spyOn(toast, 'info')
    const section = await open()
    await act(async () => fireEvent.click(section.getByRole('button', { name: 'Copy prompt' })))
    expect(info).toHaveBeenCalledWith('Selected. Press Ctrl+C to copy.')
    expect(window.getSelection()?.toString()).toBe(configPrompt)
  })

  it('opens remote files', async () => {
    const section = await open()
    fireEvent.click(section.getByRole('button', { name: 'Open remote files' }))
    expect(useStore.getState().route).toEqual({ name: 'files', params: { home: 'open' } })
  })
})
