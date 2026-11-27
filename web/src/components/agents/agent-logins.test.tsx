import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { type ComponentProps } from 'react'
import { AddLogin } from '@/components/agents/add-login'
import { LoginList, useAgentLogins } from '@/components/agents/agent-logins'
import type { Api } from '@/lib/api'
import type { AgentLogin, Member } from '@/lib/types'
import { useStore } from '@/store'
import { alice, bob, fakeApi } from '@/test/fixtures'
import { StubSocket } from '@/test/stub-socket'

const logins: AgentLogin[] = [
  { id: 'default', name: 'default', kind: 'subscription', plan: 'max', email: 'alice@example.test', organization_name: 'Alice personal', automatic: true, shared: true, position: 0 },
  { id: 'lgn-work0000001', name: 'work', kind: 'subscription', plan: 'pro', email: 'alice@work.example.test', organization_name: 'Example Co', automatic: true, shared: false, position: 1 },
  { id: 'lgn-console0001', name: 'console', kind: 'console', email: 'alice@example.test', organization_name: 'Alice API', per_use: true, automatic: false, shared: true, position: 2 },
]

function show(over: Parameters<typeof fakeApi>[0] = {}, sharedWith: Member[] = [bob]) {
  const client = fakeApi(over)
  const onChanged = vi.fn(async () => {})
  render(<LoginList logins={logins} sharedWith={sharedWith} client={client} onChanged={onChanged} />)
  return { client, onChanged }
}

const row = (name: string) => screen.getByRole('listitem', { name: `Login ${name}` })

describe('login list', () => {
  it('lists the logins in order with what each one is and whom shares reach', async () => {
    const { client } = show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: params.login ? [] : [{ provider: 'claude' as const, status: 'ok' as const, windows: [{ id: 'five_hour', label: '5-hour', used_percent: 42 }], checked_at: '2026-11-26T10:00:00Z' }],
      })),
    })
    const items = within(screen.getByRole('list', { name: 'Claude Code logins in priority order' })).getAllByRole('listitem')
    expect(items.map((item) => item.getAttribute('aria-label'))).toEqual(['Login default', 'Login work', 'Login console'])

    expect(items.map((item) => item.textContent?.charAt(0))).toEqual(['1', '2', '3'])
    expect(row('default').textContent).toContain('Subscription · max')
    expect(within(row('default')).getByText('Tried first').dataset.slot).toBe('badge')
    expect(row('work').textContent).not.toContain('Tried first')
    expect(row('default').textContent).toContain('alice@example.test · Alice personal')
    expect(row('default').textContent).toContain(`On for shares: also serves ${bob.display_name}`)
    expect(await within(row('default')).findByRole('progressbar', { name: 'Login default 5-hour usage' })).toBeDefined()
    expect(row('work').textContent).toContain('Off for shares')
    expect(row('work').textContent).not.toContain('your sessions')
    expect(within(row('work')).queryByText('Billed per use')).toBeNull()
    expect(within(row('console')).getByText('Billed per use')).toBeDefined()
    expect(row('console').textContent).toContain('On for shares: no share reaches it while Automatic is off')
    expect(row('console').textContent).not.toContain('Automatic sessions can move here')
    expect(screen.getByText(`Your account is shared with ${bob.display_name}: logins on for shares serve their sessions too.`, { exact: false })).toBeDefined()
    expect(client.accountUsage).toHaveBeenCalledWith({})
    expect(client.accountUsage).toHaveBeenCalledWith({ login: 'lgn-work0000001' })

    expect(within(row('default')).queryByRole('button', { name: 'Rename default' })).toBeNull()
    expect(within(row('default')).queryByRole('button', { name: 'Remove default' })).toBeNull()
    expect(row('default').textContent).toContain('Your default login, the one cloudfield agent login claude signs in. It keeps its name and is not removed here.')
    expect(within(row('work')).getByRole('button', { name: 'Rename work' })).toBeDefined()
    expect(within(row('default')).getByRole('button', { name: 'Move default up' })).toHaveProperty('disabled', true)
    expect(within(row('console')).getByRole('button', { name: 'Move console down' })).toHaveProperty('disabled', true)
  })

  it('says when the account is shared with nobody', () => {
    show({}, [])
    expect(row('default').textContent).toContain('On for shares: your account is not shared')
    expect(screen.getByText('Your account is not shared with anyone.', { exact: false })).toBeDefined()
  })

  it('sends the whole order, Automatic and a rename, then reads the list again', async () => {
    const user = userEvent.setup()
    const { client, onChanged } = show({
      agentLoginOrder: vi.fn(async () => ({})),
      agentLoginAutomatic: vi.fn(async () => ({})),
      agentLoginRename: vi.fn(async () => ({})),
    })
    await user.click(within(row('work')).getByRole('button', { name: 'Move work down' }))
    expect(client.agentLoginOrder).toHaveBeenCalledWith('claude', ['default', 'lgn-console0001', 'lgn-work0000001'])
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))

    await user.click(within(row('console')).getByRole('switch', { name: 'Automatic' }))
    expect(client.agentLoginAutomatic).toHaveBeenCalledWith('claude', 'lgn-console0001', true)
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(2))

    await user.click(screen.getByRole('button', { name: 'Rename work' }))
    const field = screen.getByRole('textbox', { name: 'New name for work' })
    await user.clear(field)
    await user.type(field, 'office')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(client.agentLoginRename).toHaveBeenCalledWith('claude', 'lgn-work0000001', 'office')
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(3))
  })

  it('shows a refused rename and a refused order verbatim', async () => {
    const user = userEvent.setup()
    show({
      agentLoginRename: vi.fn(async () => { throw new Error('you already have a login named "console"') }),
      agentLoginOrder: vi.fn(async () => { throw new Error('login_ids must name each of your logins once, default included') }),
    })
    await user.click(screen.getByRole('button', { name: 'Rename work' }))
    const field = screen.getByRole('textbox', { name: 'New name for work' })
    await user.clear(field)
    await user.type(field, 'console')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect((await screen.findByRole('alert')).textContent).toBe('you already have a login named "console"')
    await user.click(screen.getByRole('button', { name: 'Cancel' }))

    await user.click(within(row('work')).getByRole('button', { name: 'Move work up' }))
    expect((await screen.findByRole('alert')).textContent).toBe('login_ids must name each of your logins once, default included')
  })

  it('keeps the arrows off until the list is read again after a move, and reads it again after a refused one', async () => {
    const user = userEvent.setup()
    let reread = () => {}
    const onChanged = vi.fn(() => new Promise<void>((resolve) => { reread = resolve }))
    const client = fakeApi({
      agentLoginOrder: vi.fn<Api['agentLoginOrder']>().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error('login_ids must name each of your logins once, default included')),
    })
    render(<LoginList logins={logins} sharedWith={[]} client={client} onChanged={onChanged} />)
    await user.click(within(row('default')).getByRole('button', { name: 'Move default down' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    expect(within(row('work')).getByRole('button', { name: 'Move work down' })).toHaveProperty('disabled', true)
    await act(async () => reread())
    expect(within(row('work')).getByRole('button', { name: 'Move work down' })).toHaveProperty('disabled', false)

    await user.click(within(row('work')).getByRole('button', { name: 'Move work down' }))
    expect((await screen.findByRole('alert')).textContent).toBe('login_ids must name each of your logins once, default included')
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('says which login is at its limit and when it serves again, in days past 48 hours', async () => {
    const hour = 3_600_000
    const at = (hours: number) => new Date(Date.now() + hours * hour).toISOString()
    const windows: Record<string, { id: string; label: string; used_percent: number; resets_at: string }[]> = {
      default: [{ id: 'five_hour', label: '5 hours', used_percent: 94, resets_at: at(3) }, { id: 'seven_day', label: '7 days', used_percent: 58, resets_at: at(90) }],
      'lgn-work0000001': [{ id: 'five_hour', label: '5 hours', used_percent: 95, resets_at: at(1) }, { id: 'seven_day', label: '7 days', used_percent: 100, resets_at: at(40) }],
    }
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => ({
        account_member_id: alice.id,
        login: params.login,
        providers: [{ provider: 'claude' as const, status: 'ok' as const, windows: windows[params.login ?? 'default'] ?? [], checked_at: at(0) }],
      })),
    })
    expect(await within(row('work')).findByText('At its limit (7 days) · resets in 40h')).toBeDefined()
    expect(await within(row('default')).findByText('58% used · resets in 3d 18h')).toBeDefined()
    expect(row('default').textContent).not.toContain('At its limit')
    expect(row('console').textContent).not.toContain('At its limit')
  })

  it('turns Use for shares through agent.login.share, and shows its refusal verbatim and reads the list again', async () => {
    const user = userEvent.setup()
    const refusal = 'you have no login "lgn-work0000001"'
    const share = vi.fn<Api['agentLoginShare']>().mockResolvedValueOnce({}).mockRejectedValueOnce(new Error(refusal))
    const { client, onChanged } = show({ agentLoginShare: share })
    expect(within(row('work')).getByRole('switch', { name: 'Use for shares' }).getAttribute('aria-checked')).toBe('false')
    await user.click(within(row('work')).getByRole('switch', { name: 'Use for shares' }))
    expect(client.agentLoginShare).toHaveBeenCalledWith('claude', 'lgn-work0000001', true)
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(1))
    await user.click(within(row('default')).getByRole('switch', { name: 'Use for shares' }))
    expect(client.agentLoginShare).toHaveBeenLastCalledWith('claude', 'default', false)
    expect((await screen.findByRole('alert')).textContent).toBe(refusal)
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it("says a Console login has no usage windows, and shows any other usage error verbatim", async () => {
    show({
      accountUsage: vi.fn(async (params: { login?: string } = {}) => {
        if (params.login === 'lgn-work0000001') throw new Error('account.usage: claude: credential file unavailable')
        return {
          account_member_id: alice.id,
          login: params.login,
          providers: params.login === 'lgn-console0001'
            ? [{ provider: 'claude' as const, status: 'unsupported' as const, windows: [], checked_at: '2026-11-26T10:00:00Z', error: 'an Anthropic Console sign-in is billed by the API and has no usage windows' }]
            : [],
        }
      }),
    })
    expect(await within(row('console')).findByText('No usage windows: billed per use')).toBeDefined()
    expect(row('console').textContent).not.toContain('Usage not read')
    expect(await within(row('work')).findByText('account.usage: claude: credential file unavailable')).toBeDefined()
    expect(row('work').textContent).toContain('Usage not read: ')
  })

  it('says automatic sessions can move onto a login billed per use', () => {
    const client = fakeApi()
    render(<LoginList logins={logins.map((login) => ({ ...login, automatic: true }))} sharedWith={[]} client={client} onChanged={async () => {}} />)
    expect(row('console').textContent).toContain('Automatic sessions can move here when the logins above it run out.')
    expect(row('work').textContent).not.toContain('Automatic sessions can move here')
  })

  it('confirms a removal, says Anthropic keeps the sign-in, and shows a refusal verbatim with ways to act on it', async () => {
    const user = userEvent.setup()
    const refusal = "login \"work\" is in use by your sessions run-7kq2m9x4ab (end them) and 1 of your teammates' sessions, which hold it until they end and their containers go (revoking the share you gave a teammate, with cloudfield account revoke <member-id>, moves their running sessions off it)"
    const navigate = vi.fn()
    const original = useStore.getState().navigate
    useStore.setState({ navigate })
    onTestFinished(() => { useStore.setState({ navigate: original }) })
    const { client, onChanged } = show({ agentLoginRemove: vi.fn(async () => { throw new Error(refusal) }) })
    await user.click(screen.getByRole('button', { name: 'Remove work' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Remove the login “work”?' })
    expect(dialog.textContent).toContain('Signed in as alice@work.example.test · Example Co.')
    expect(dialog.textContent).toContain('It does not sign the login out at Anthropic')
    expect(within(dialog).getByRole('button', { name: 'Remove' }).className).toContain('bg-state-failed')
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(client.agentLoginRemove).toHaveBeenCalledWith('claude', 'lgn-work0000001')
    expect(await within(dialog).findByText(refusal)).toBeDefined()
    expect(within(dialog).getByRole('button', { name: 'Remove' })).toHaveProperty('disabled', true)
    expect(within(dialog).getByRole('button', { name: 'Remove' }).className).not.toContain('bg-state-failed')
    expect(onChanged).not.toHaveBeenCalled()
    await user.click(within(dialog).getByRole('button', { name: 'Open run-7kq2m9x4ab' }))
    expect(navigate).toHaveBeenCalledWith('run', { runId: 'run-7kq2m9x4ab' })
    expect(screen.queryByRole('alertdialog')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Remove work' }))
    await user.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Remove' }))
    await user.click(await screen.findByRole('button', { name: 'Revoke a share on Members' }))
    expect(navigate).toHaveBeenLastCalledWith('members')
  })
})

describe('login list reads', () => {
  it('keeps the newest read when an older one answers last', async () => {
    const answers: ((list: AgentLogin[]) => void)[] = []
    const client = fakeApi({ agentLoginList: vi.fn(() => new Promise<AgentLogin[]>((resolve) => { answers.push(resolve) })) })
    const { result } = renderHook(() => useAgentLogins(client, true))
    let newest = Promise.resolve()
    act(() => { newest = result.current.reload() })
    await act(async () => {
      answers[1]!(logins)
      await newest
    })
    await act(async () => answers[0]!([logins[0]!]))
    expect(result.current.logins).toEqual(logins)
  })

  it('links only the sessions a refusal lists, never a login named like one', async () => {
    const user = userEvent.setup()
    const named = { ...logins[1]!, name: 'run-abcdefghij' }
    const client = fakeApi({ agentLoginRemove: vi.fn(async () => { throw new Error('login "run-abcdefghij" is in use by your sessions run-7kq2m9x4ab, run-8kq2m9x4ab (end them)') }) })
    render(<LoginList logins={[logins[0]!, named]} sharedWith={[]} client={client} onChanged={async () => {}} />)
    await user.click(screen.getByRole('button', { name: 'Remove run-abcdefghij' }))
    const dialog = screen.getByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: 'Remove' }))
    expect(await within(dialog).findByRole('button', { name: 'Open run-7kq2m9x4ab' })).toBeDefined()
    expect(within(dialog).getByRole('button', { name: 'Open run-8kq2m9x4ab' })).toBeDefined()
    expect(within(dialog).queryByRole('button', { name: 'Open run-abcdefghij' })).toBeNull()
  })
})

function addLogin(props: Partial<ComponentProps<typeof AddLogin>> & Pick<ComponentProps<typeof AddLogin>, 'client'>) {
  return render(<AddLogin running={null} ended={null} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} {...props} />)
}

const signInSockets = () => StubSocket.opened.filter((socket) => socket.url.includes('agent_login='))
const accept = (socket: StubSocket) => act(() => {
  socket.onopen?.()
  socket.onmessage?.({ data: JSON.stringify({ ok: true, has_control: true, cols: 80, rows: 24, replay: 0 }) })
})
const inputs = (socket: StubSocket) => socket.frames().filter((frame) => (frame as { type?: string }).type === 'input')

describe('add login', () => {
  beforeEach(() => StubSocket.install())

  it('shows a refused name verbatim and starts no sign-in', async () => {
    const user = userEvent.setup()
    const refusal = 'login name "Work": use lowercase letters, digits and \'-\', starting with a letter or digit'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'Work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect(client.agentLoginAdd).toHaveBeenCalledWith('claude', 'Work')
    expect((await screen.findByRole('alert')).textContent).toBe(`Sign-in not started${refusal}`)
    expect(client.agentLoginSocket).not.toHaveBeenCalled()
  })

  it('opens the sign-in on the new login, waits on nothing itself, and shows the outcome the page passes', async () => {
    const user = userEvent.setup()
    const onRunning = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000001' })) })
    const { rerender } = addLogin({ client, onRunning })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-new00000001'))
    expect(onRunning).toHaveBeenCalledWith({ id: 'lgn-new00000001', name: 'work' })
    expect(client.agentLoginResult).not.toHaveBeenCalled()
    const login = { id: 'lgn-new00000001', name: 'work', kind: 'subscription' as const, plan: 'pro', email: 'alice@work.example.test', automatic: true, shared: true, position: 1 }
    rerender(<AddLogin client={client} running={null} ended={{ id: 'lgn-new00000001', outcome: { login } }} onRunning={onRunning} onOpened={() => {}} onClose={() => {}} />)
    expect(await screen.findByText('Added login work')).toBeDefined()
    expect(screen.getByText('Subscription · pro · alice@work.example.test')).toBeDefined()
  })

  it('shows the server refusing the sign-in verbatim and starts again', async () => {
    const user = userEvent.setup()
    const refusal = 'this sign-in is the account and organisation your login "default" is signed in to, so it was not added; sign in to another account to add a login'
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000002' })) })
    const { rerender } = addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'twin')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    await screen.findByRole('heading', { name: 'Sign in to twin' })
    rerender(<AddLogin client={client} running={null} ended={{ id: 'lgn-new00000002', outcome: { refused: refusal } }} onRunning={() => {}} onOpened={() => {}} onClose={() => {}} />)
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain('Login twin was not added')
    expect(within(alert).getByText(refusal)).toBeDefined()
    await user.click(within(alert).getByRole('button', { name: 'Start again' }))
    expect(screen.getByRole('textbox', { name: 'Name' })).toBeDefined()
  })

  it('returns to a sign-in still running when the server refuses another', async () => {
    const user = userEvent.setup()
    const refusal = 'store: conflict: the sign-in of login "work" (lgn-run00000001) is still running; finish it first'
    const onRunning = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error(refusal) }) })
    addLogin({ client, onRunning })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const running = await screen.findByRole('status')
    expect(running.textContent).toContain('The sign-in of work is still running')
    expect(within(running).getByText(refusal)).toBeDefined()
    expect(onRunning).toHaveBeenCalledWith({ id: 'lgn-run00000001', name: 'work' })
    expect(screen.getByRole('button', { name: 'Start sign-in' })).toHaveProperty('disabled', true)
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveProperty('disabled', true)
    const back = within(running).getByRole('button', { name: 'Return to the sign-in' })
    expect(back.className).toContain('bg-action')
    await user.click(back)
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-run00000001'))
    expect(screen.getByRole('heading', { name: 'Sign in to work' })).toBeDefined()
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' }).textContent).toContain('Sign in with the account for work.')
  })

  it('offers a sign-in the page knows is running before any refusal, and opens it at once to resume', async () => {
    const user = userEvent.setup()
    const client = fakeApi()
    const running = { id: 'lgn-run00000002', name: 'spare' }
    const { unmount } = addLogin({ client, running })
    expect(screen.getByRole('status').textContent).toContain('The sign-in of spare is still running')
    expect(screen.getByRole('button', { name: 'Start sign-in' })).toHaveProperty('disabled', true)
    await user.click(screen.getByRole('button', { name: 'Return to the sign-in' }))
    await waitFor(() => expect(client.agentLoginSocket).toHaveBeenCalledWith('lgn-run00000002'))
    unmount()

    addLogin({ client, running, resume: true })
    expect(screen.getByRole('heading', { name: 'Sign in to spare' })).toBeDefined()
  })

  it('says how long a sign-in this page started has left, and only that one', async () => {
    const user = userEvent.setup()
    const onOpened = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000004' })) })
    const { unmount } = addLogin({ client, onOpened })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const panel = await screen.findByRole('region', { name: 'Sign in to Claude Code' })
    expect(panel.textContent).toContain('You can leave this page: the sign-in keeps running for up to 15 minutes from its start, and Add login offers Return to the sign-in.')
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    accept(signInSockets()[0]!)
    expect(onOpened).toHaveBeenCalledWith('lgn-new00000004')
    unmount()

    const opened = Date.now() - 5 * 60_000
    addLogin({ client, running: { id: 'lgn-new00000004', name: 'spare', opened }, resume: true, onOpened })
    expect(screen.getByRole('region', { name: 'Sign in to Claude Code' }).textContent).toContain('keeps running for up to 10 more minutes')
    await waitFor(() => expect(signInSockets()).toHaveLength(2))
    accept(signInSockets()[1]!)
    expect(onOpened).toHaveBeenCalledTimes(1)
  })

  it('ends the sign-in only through Cancel sign-in, which sends Ctrl-C, after the terminal is up if it is still starting', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => ({ login_id: 'lgn-new00000003' })) })
    addLogin({ client, onClose })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'spare')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    const panel = await screen.findByRole('region', { name: 'Sign in to Claude Code' })
    expect(within(panel).queryByRole('button', { name: 'Close' })).toBeNull()
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    const socket = signInSockets()[0]!

    await user.click(within(panel).getByRole('button', { name: 'Cancel sign-in' }))
    expect(onClose).not.toHaveBeenCalled()
    expect(within(panel).getByRole('status').textContent).toBe('Cancelling: the sign-in ends as soon as its terminal is up.')
    expect(within(panel).getByRole('button', { name: 'Cancel sign-in' })).toHaveProperty('disabled', true)
    accept(socket)
    expect(inputs(socket)).toEqual([{ type: 'input', data: '\x03' }])
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('sends Ctrl-C at once to a sign-in whose terminal is up', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    addLogin({ client: fakeApi(), running: { id: 'lgn-run00000005', name: 'spare' }, resume: true, onClose })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    const socket = signInSockets()[0]!
    accept(socket)
    await user.click(screen.getByRole('button', { name: 'Cancel sign-in' }))
    expect(inputs(socket)).toEqual([{ type: 'input', data: '\x03' }])
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('keeps a sign-in whose socket dropped running and reattaches to it', async () => {
    addLogin({ client: fakeApi(), running: { id: 'lgn-run00000006', name: 'spare' }, resume: true })
    await waitFor(() => expect(signInSockets()).toHaveLength(1))
    accept(signInSockets()[0]!)
    act(() => signInSockets()[0]!.onclose?.({ code: 1006 }))
    const panel = screen.getByRole('region', { name: 'Sign in to Claude Code' })
    expect(within(panel).getByRole('status').textContent).toBe('Reconnecting to the sign-in…')
    expect(within(panel).getByRole('button', { name: 'Cancel sign-in' })).toBeDefined()
    expect(within(panel).queryByRole('button', { name: 'Done' })).toBeNull()
    await waitFor(() => expect(signInSockets()).toHaveLength(2), { timeout: 3000 })
    expect(signInSockets()[1]!.url).toBe(signInSockets()[0]!.url)
    accept(signInSockets()[1]!)
    expect(within(panel).getByRole('status').textContent).toBe('Starting the sign-in…')
  })

  it('offers no return for any other refusal', async () => {
    const user = userEvent.setup()
    const client = fakeApi({ agentLoginAdd: vi.fn(async () => { throw new Error('store: conflict: you already have a login named "work"') }) })
    addLogin({ client })
    await user.type(screen.getByRole('textbox', { name: 'Name' }), 'work')
    await user.click(screen.getByRole('button', { name: 'Start sign-in' }))
    expect((await screen.findByRole('alert')).textContent).toBe('Sign-in not startedstore: conflict: you already have a login named "work"')
    expect(screen.queryByRole('button', { name: 'Return to the sign-in' })).toBeNull()
  })
})
